//go:build e2e

// Package e2e 端到端验证鉴权链路。
//
// 它与单元测试的分工：internal/rbac 的测试验证**判定逻辑**，
// 这里的测试验证**判定如何被接入**——注解解析、主体提取、
// 作用域解析、拒绝语义到状态码的映射，全部走真实的 gRPC 连接。
//
// 运行：make test-e2e
package e2e

import (
	"context"
	"errors"
	"net"
	"path/filepath"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	identityv1 "github.com/poetlife/aladdin/api/gen/aladdin/identity/v1"
	rbacv1 "github.com/poetlife/aladdin/api/gen/aladdin/rbac/v1"
	"github.com/poetlife/aladdin/internal/config"
	"github.com/poetlife/aladdin/internal/database"
	"github.com/poetlife/aladdin/internal/galaxy"
	galaxygormstore "github.com/poetlife/aladdin/internal/galaxy/gormstore"
	"github.com/poetlife/aladdin/internal/identity"
	identitygormstore "github.com/poetlife/aladdin/internal/identity/gormstore"
	"github.com/poetlife/aladdin/internal/objectstore"
	"github.com/poetlife/aladdin/internal/observability"
	profilegormstore "github.com/poetlife/aladdin/internal/profile/gormstore"
	"github.com/poetlife/aladdin/internal/rbac"
	"github.com/poetlife/aladdin/internal/rbac/gormstore"
	"github.com/poetlife/aladdin/internal/server"
	"github.com/poetlife/aladdin/internal/server/interceptor"
)

const (
	testToken   = "e2e-token"
	testSubject = "e2e-user"
	testScope   = "tenant/acme"
)

// harness 是一次端到端测试的全部依赖。
type harness struct {
	// srv 是这台被测服务端。
	//
	// 用例用它注入第二个主体（归属、生命周期这类用例需要"另一个人"），
	// 走的是与生产完全相同的入口：Authenticator 与 Store。
	srv     *server.Server
	address string
	// logs 是服务端在内置接线上吐出的日志。
	//
	// 它是可读的观察点，用来断言"某样东西**没有**进日志"——凭证、口令、
	// 头像字节都属于这一类，而它们的价值恰在于"没出现"，只有真去读一遍
	// 才能守住。
	logs *observer.ObservedLogs
	// objects 是这个装配里的私有区对象存储。
	//
	// 直传之后**服务端不接触字节**，因此"客户端把字节传上去"这一步由测试
	// 扮演：签发凭证之后往这个实例里写一份，再提交（见 uploadAsset）。
	objects *objectstore.MemoryStore
	// public 是公开区的假存储，用来断言"只上架了被引用的资产"。
	public *galaxy.MemoryPublicStore
	// publishBase 是发布域的取值，bucket 是发布物素材所在的桶——**公开区与
	// 私有区共用它**，因此断言对外地址与响应头里的允许来源时用的是同一个主机。
	publishBase string
	bucket      string
}

// startServer 在随机端口上启动服务端，并注入一个测试主体。
func startServer(t *testing.T, roleID string, scope rbac.Scope) harness {
	t.Helper()
	return startServerWith(t, roleID, scope)
}

// harnessOption 在装配之前调整配置或依赖。
type harnessOption func(cfg *config.ServerConfig, ident *server.IdentityStores)

// withChannels 注入登录渠道。
//
// 不注入时那几条登录路径整体缺席：机器凭证照常可用，渠道登录返回"未实现"
// ——未启用的登录方式不该退化成一个可用的后门。
func withChannels(channels ...identity.Channel) harnessOption {
	return func(_ *config.ServerConfig, ident *server.IdentityStores) {
		ident.Channels = identity.NewRegistry(channels...)
	}
}

// withPublicBaseURL 设置对外源，供重定向型登录渠道的用例使用。
func withPublicBaseURL(baseURL string) harnessOption {
	return func(cfg *config.ServerConfig, _ *server.IdentityStores) {
		cfg.PublicBaseURL = baseURL
	}
}

// startServerWith 允许在装配前注入渠道与对外源，供认证链路的用例使用。
func startServerWith(t *testing.T, roleID string, scope rbac.Scope, opts ...harnessOption) harness {
	t.Helper()

	cfg := config.DefaultServer()
	cfg.Address = "127.0.0.1:0"
	// 只记录、不输出：不需要日志的用例读不出差别，需要断言"某样东西没进日志"
	// 的用例则有了观察点（见 harness.logs）。
	core, logs := observer.New(zapcore.DebugLevel)
	logger := zap.New(core)

	// 遥测必须真的建起来：otel.Tracer 取的是调用当时注册的实现，
	// 没有 provider 时 span 无效，响应头也就不会被回写——
	// 那时端到端测试看到的"没有 traceparent"是夹具的问题，不是被测代码的问题。
	provider, err := observability.NewProvider(context.Background(), observability.ProviderOptions{
		ServiceName: "aladdin-server-e2e",
		SampleRatio: 1,
	})
	if err != nil {
		t.Fatalf("构建遥测失败: %v", err)
	}
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })

	// 端到端测试用**真实的持久化存储**，不是内存存储：被测的是"判定如何
	// 被接入"，而接入方式之一就是它读的数据从哪来。用内存存储会让这一层
	// 变成测试夹具自己搭的样子，掩盖掉真实路径上的问题（表没建、写入没落盘、
	// 错误没被翻译）。每个用例一个临时目录，用例之间互不干扰。
	store, err := gormstore.Open(context.Background(), config.DatabaseConfig{
		Driver: string(database.DialectSQLite),
		DSN:    filepath.Join(t.TempDir(), "e2e.db"),
	}, logger)
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	// 私有区对象存储用内存实现替代：契约要求测试不访问任何网络（见
	// docs/design/objectstore/README.md）。**同一个实例交给头像与资产两处**，
	// 因为生产上它们本来就是同一个桶上的同一套机制。
	objects := objectstore.NewMemoryStore()

	// 公开区与发布域：让发布这条路在端到端测试里是**启用**的，于是"发布—对外
	// 地址返回产物"会被真的走一遍，而不是因为没配而整条跳过。发布没有自己的桶：
	// 公开区与上面那个私有区共用同一个（靠逐对象的公开读区分）。
	bucket := "https://aladdin-1250000000.cos.ap-guangzhou.myqcloud.com"
	publishBase := "https://pub.example.com"
	public := galaxy.NewMemoryPublicStore(objects)
	origin, err := galaxy.NewPublicOrigin(bucket, publishBase)
	if err != nil {
		t.Fatalf("构造发布地址失败: %v", err)
	}

	// 认证模块的存储同样落在真实连接上：会话与身份别名是这条链路上的
	// 一等数据，用内存实现在这里等于把"表没建、写入没落盘"挡在测试之外。
	identityStore := identitygormstore.NewIdentityStore(store.DB())
	ident := server.IdentityStores{
		Identities: identity.NewIdentities(identityStore, store),
		Sessions:   identity.NewSessions(identitygormstore.New(store.DB())),
	}
	for _, opt := range opts {
		opt(&cfg, &ident)
	}
	srv := server.New(cfg, logger, nil, store, ident, server.ProfileStores{
		// 档案的存储同样落在真实连接上。
		Profiles: profilegormstore.New(store.DB()),
		// 头像字节走直传：签发的那份凭证在测试里由 objects 扮演对象存储。
		Avatars: objects,
	}, server.GalaxyStores{
		Projects: galaxygormstore.New(store.DB()),
		Assets:   objects,
		Public:   public,
		Origin:   origin,
	})
	if err := srv.Store().PutSubject(context.Background(), rbac.Subject{
		ID: testSubject, Type: rbac.SubjectTypeUser, DefaultScope: scope,
	}); err != nil {
		t.Fatalf("注入测试主体失败: %v", err)
	}
	// 范围也要登记：绑定只能指向已登记的范围，因此走服务的授予路径（管理面用例）
	// 需要这一条（见 docs/design/rbac/scopes.md）。直接写的绑定不受它约束，
	// 但夹具要能同时覆盖"读/写都走服务"的用例。
	if scope != rbac.GlobalScope {
		if err := srv.Store().PutScope(context.Background(), rbac.ScopeDefinition{Path: string(scope)}); err != nil {
			t.Fatalf("登记测试范围失败: %v", err)
		}
	}
	if err := srv.Store().Bind(context.Background(), rbac.RoleBinding{
		SubjectID: testSubject, RoleID: roleID, Scope: scope,
	}); err != nil {
		t.Fatalf("注入绑定失败: %v", err)
	}
	srv.Authenticator().Add(testToken, rbac.Subject{
		ID: testSubject, Type: rbac.SubjectTypeUser, DefaultScope: scope,
	})

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("监听失败: %v", err)
	}

	// 服务端 goroutine 只向 channel 写，不碰 *testing.T：
	// t.Logf 在测试结束后调用会与 testing 包内部状态竞争，
	// 而且真出现问题时日志早已失去归属。
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(lis) }()

	t.Cleanup(func() {
		_ = lis.Close()
		if err := <-errCh; err != nil && !errors.Is(err, net.ErrClosed) && !errors.Is(err, grpc.ErrServerStopped) {
			t.Errorf("服务端异常退出: %v", err)
		}
	})

	return harness{
		srv:         srv,
		address:     lis.Addr().String(),
		logs:        logs,
		objects:     objects,
		public:      public,
		publishBase: publishBase,
		bucket:      bucket,
	}
}

// dial 构造一个走**原生 gRPC**（h2c）的客户端。
//
// 端到端测试要证明"同一个请求无论走哪条协议，判定结论相同"，因此这里需要一条
// gRPC 连接作为对照——浏览器与**命令行都走 Connect**（见 pkg/client），Connect
// 那条路径的客户端由 connect_client_test.go 自己构造。
//
// 凭证与作用域由本函数注入：裸连接不带任何凭证，而"以谁的身份调用"正是这些
// 断言的前提。
func (h harness) dial(t *testing.T, token, scope string) *grpcClient {
	t.Helper()
	conn, err := grpc.NewClient(h.address,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithChainUnaryInterceptor(credentialInterceptor(token, scope)),
		// 流式调用要单独装一个：unary 那个不覆盖它，而订阅通道是流式的。
		grpc.WithChainStreamInterceptor(credentialStreamInterceptor(token, scope)),
	)
	if err != nil {
		t.Fatalf("建立 gRPC 连接失败: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return &grpcClient{conn: conn, timeout: 5 * time.Second}
}

// grpcClient 是测试用的 gRPC 客户端：一条连接加一份调用超时。
type grpcClient struct {
	conn    *grpc.ClientConn
	timeout time.Duration
}

// Conn 返回底层连接，用于构造各服务的客户端。
func (c *grpcClient) Conn() *grpc.ClientConn { return c.conn }

// Context 返回一个带超时的 context。
func (c *grpcClient) Context() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), c.timeout)
}

// credentialInterceptor 把凭证与作用域注入 gRPC 的 metadata。
//
// 头名取自服务端的常量（那是唯一来源），不在这里手写字符串。
func credentialInterceptor(token, scope string) grpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		return invoker(withCredentials(ctx, token, scope), method, req, reply, cc, opts...)
	}
}

// credentialStreamInterceptor 与上面那个同源：流式调用要单独装一个，因为
// WithChainUnaryInterceptor 不覆盖它们。
//
// 少了它的表现是"开了流却没有凭证"——一次看不出原因的 Unauthenticated。订阅
// 事件通道的 Watch 正是流式的，因此这条不是可有可无的补充。
func credentialStreamInterceptor(token, scope string) grpc.StreamClientInterceptor {
	return func(ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn, method string, streamer grpc.Streamer, opts ...grpc.CallOption) (grpc.ClientStream, error) {
		return streamer(withCredentials(ctx, token, scope), desc, cc, method, opts...)
	}
}

// withCredentials 是这两处**唯一**注入凭证与作用域的地方。
func withCredentials(ctx context.Context, token, scope string) context.Context {
	if token != "" {
		ctx = metadata.AppendToOutgoingContext(ctx, interceptor.HeaderAuthorization, "Bearer "+token)
	}
	if scope != "" {
		ctx = metadata.AppendToOutgoingContext(ctx, interceptor.HeaderScope, scope)
	}
	return ctx
}

func TestWhoAmI(t *testing.T) {
	h := startServer(t, rbac.RoleViewer, testScope)
	c := h.dial(t, testToken, testScope)

	ctx, cancel := c.Context()
	defer cancel()

	resp, err := identityv1.NewIdentityServiceClient(c.Conn()).WhoAmI(ctx, &identityv1.WhoAmIRequest{})
	if err != nil {
		t.Fatalf("WhoAmI 失败: %v", err)
	}
	if resp.GetSubjectId() != testSubject {
		t.Errorf("主体 = %q, want %q", resp.GetSubjectId(), testSubject)
	}
}

// TestSessionPermissionsAreExpanded 验证前端拿到的是**展开后**的权限码集合。
//
// 这是前端能保持"只做集合成员判断"的前提：继承与作用域展开在服务端完成。
// 通配也在展开之列——它是最容易被漏掉的一条，因为它在服务端判定里本来就有
// 另一处实现（Matches），漏展开时判定照常放行，只有前端会一片空白。
func TestSessionPermissionsAreExpanded(t *testing.T) {
	tests := []struct {
		name string
		role string
		want []rbac.PermissionCode
	}{
		{
			name: "具体权限码原样下发",
			role: rbac.RoleViewer,
			want: []rbac.PermissionCode{
				rbac.PermissionRbacRoleRead,
				rbac.PermissionRbacSubjectRead,
				rbac.PermissionRbacScopeRead,
			},
		},
		{
			name: "整个集合就是一个通配",
			role: rbac.RoleSystemAdmin,
			want: rbac.AllPermissionCodes,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := startServer(t, tt.role, testScope)
			c := h.dial(t, testToken, testScope)

			ctx, cancel := c.Context()
			defer cancel()

			resp, err := identityv1.NewIdentityServiceClient(c.Conn()).
				GetSessionPermissions(ctx, &identityv1.GetSessionPermissionsRequest{})
			if err != nil {
				t.Fatalf("GetSessionPermissions 失败: %v", err)
			}

			want := map[string]bool{}
			for _, p := range tt.want {
				want[p.String()] = true
			}
			if len(resp.GetPermissions()) != len(want) {
				t.Fatalf("权限码 = %v, want %d 条", resp.GetPermissions(), len(want))
			}
			for _, p := range resp.GetPermissions() {
				if !want[p] {
					t.Errorf("出现非预期权限码 %q", p)
				}
			}
		})
	}
}

// TestScopeContainmentOverWire 验证作用域包含规则在真实链路上生效。
func TestScopeContainmentOverWire(t *testing.T) {
	h := startServer(t, rbac.RoleViewer, testScope)

	tests := []struct {
		name    string
		scope   string
		wantErr codes.Code
	}{
		{"子作用域放行", "tenant/acme/project/web", codes.OK},
		{"自身作用域放行", "tenant/acme", codes.OK},
		{"父作用域拒绝", "tenant", codes.PermissionDenied},
		{"兄弟作用域拒绝", "tenant/other", codes.PermissionDenied},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := h.dial(t, testToken, tt.scope)
			ctx, cancel := c.Context()
			defer cancel()

			_, err := rbacv1.NewRBACServiceClient(c.Conn()).
				ListRoles(ctx, &rbacv1.ListRolesRequest{Scope: tt.scope})
			if got := status.Code(err); got != tt.wantErr {
				t.Fatalf("状态码 = %s, want %s (err=%v)", got, tt.wantErr, err)
			}
		})
	}
}

// TestUnauthenticatedDistinctFromPermissionDenied 验证两类失败不被混淆。
//
// 混用会让客户端在权限不足时反复刷新凭证，把一次配置错误放大成登录风暴。
func TestUnauthenticatedDistinctFromPermissionDenied(t *testing.T) {
	t.Run("未携带凭证", func(t *testing.T) {
		h := startServer(t, rbac.RoleViewer, testScope)
		c := h.dial(t, "", testScope)
		ctx, cancel := c.Context()
		defer cancel()

		_, err := rbacv1.NewRBACServiceClient(c.Conn()).
			ListRoles(ctx, &rbacv1.ListRolesRequest{Scope: testScope})
		if got := status.Code(err); got != codes.Unauthenticated {
			t.Fatalf("状态码 = %s, want Unauthenticated", got)
		}
	})

	t.Run("凭证无效", func(t *testing.T) {
		h := startServer(t, rbac.RoleViewer, testScope)
		c := h.dial(t, "bogus-token", testScope)
		ctx, cancel := c.Context()
		defer cancel()

		_, err := rbacv1.NewRBACServiceClient(c.Conn()).
			ListRoles(ctx, &rbacv1.ListRolesRequest{Scope: testScope})
		if got := status.Code(err); got != codes.Unauthenticated {
			t.Fatalf("状态码 = %s, want Unauthenticated", got)
		}
	})

	t.Run("已认证但无权限", func(t *testing.T) {
		// auditor 没有 rbac.role.write，尝试写角色应得到 PermissionDenied。
		h := startServer(t, rbac.RoleAuditor, testScope)
		c := h.dial(t, testToken, testScope)
		ctx, cancel := c.Context()
		defer cancel()

		_, err := rbacv1.NewRBACServiceClient(c.Conn()).
			PutRole(ctx, &rbacv1.PutRoleRequest{
				Scope: testScope,
				Role:  &rbacv1.Role{Id: "new-role", DisplayName: "新角色"},
			})
		if got := status.Code(err); got != codes.PermissionDenied {
			t.Fatalf("状态码 = %s, want PermissionDenied", got)
		}
	})
}

// TestDenialReasonIsStructured 验证拒绝原因是结构化枚举而非自由文本。
//
// CLI 与前端都依赖它做分支，因此它必须能被机器读取。
func TestDenialReasonIsStructured(t *testing.T) {
	h := startServer(t, rbac.RoleViewer, testScope)
	// 请求父作用域：有权限但作用域不够，应得到 scope_mismatch。
	c := h.dial(t, testToken, "tenant")

	ctx, cancel := c.Context()
	defer cancel()

	_, err := rbacv1.NewRBACServiceClient(c.Conn()).
		ListRoles(ctx, &rbacv1.ListRolesRequest{Scope: "tenant"})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("期望 PermissionDenied，实际 %v", err)
	}

	detail := denialDetail(t, err)
	if detail.GetReason() != rbac.ReasonScopeMismatch {
		t.Errorf("原因 = %s, want %s", detail.GetReason(), rbac.ReasonScopeMismatch)
	}
}

// TestPublicMethodNeedsNoCredential 验证公开方法真的公开，且仅限白名单内的方法。
func TestPublicMethodNeedsNoCredential(t *testing.T) {
	h := startServer(t, rbac.RoleViewer, testScope)
	c := h.dial(t, "", "")

	ctx, cancel := c.Context()
	defer cancel()

	_, err := identityv1.NewIdentityServiceClient(c.Conn()).
		Login(ctx, &identityv1.LoginRequest{
			Credential: &identityv1.LoginRequest_Token{
				Token: &identityv1.TokenCredential{Token: testToken},
			},
		})
	if err != nil {
		t.Fatalf("公开方法不应要求凭证: %v", err)
	}
}

// denialDetail 从错误中取出结构化的拒绝详情。
//
// 服务端是 Connect 服务，这里走 gRPC 协议：Connect 把详情编码进
// grpc-status-details-bin，grpc-go 能直接解出来，不需要额外处理。
func denialDetail(t *testing.T, err error) *rbacv1.DenialDetail {
	t.Helper()
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("不是 gRPC 状态错误: %v", err)
	}
	for _, d := range st.Details() {
		if detail, ok := d.(*rbacv1.DenialDetail); ok {
			return detail
		}
	}
	t.Fatalf("错误中未携带 DenialDetail: %v", err)
	return nil
}
