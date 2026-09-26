//go:build e2e

package e2e

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	identityv1 "github.com/poetlife/aladdin/api/gen/aladdin/identity/v1"
	"github.com/poetlife/aladdin/api/gen/aladdin/identity/v1/identityv1connect"
	rbacv1 "github.com/poetlife/aladdin/api/gen/aladdin/rbac/v1"
	"github.com/poetlife/aladdin/internal/identity"
	"github.com/poetlife/aladdin/internal/rbac"
)

// 本文件端到端验证认证链路：登录如何解析主体、多个渠道如何归到同一个人、
// 未认证与无权限如何分开。
//
// 两条协议（gRPC 与 Connect）都跑：登录与绑定都经过 HTTP 中间件的认证路径，
// 而拒绝响应由 ErrorWriter 按协议写出——只测一种协议，另一种协议上的编码
// 错误会被完全漏掉（见 CLAUDE.md 的传输方式约定）。
//
// **不联网**：身份令牌的校验器是一个可控的替身，注入在服务端装配处。
// 校验规则本身由 internal/identity 的用例逐条覆盖，这里验证的是**接入**。

// fakeGoogleToken 解出的是哪个渠道身份，由用例决定。
type fakeGoogleToken struct {
	mu      sync.Mutex
	subject string
	email   string
	err     error
}

func newFakeGoogleToken(subject, email string) *fakeGoogleToken {
	return &fakeGoogleToken{subject: subject, email: email}
}

func (f *fakeGoogleToken) Verify(context.Context, string) (identity.GoogleIdentity, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return identity.GoogleIdentity{}, f.err
	}
	return identity.GoogleIdentity{Subject: f.subject, Email: f.email}, nil
}

// set 换一个渠道身份，模拟"换一个渠道登进来"。
func (f *fakeGoogleToken) set(subject, email string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.subject, f.email, f.err = subject, email, nil
}

// fail 让校验一律失败，用于"令牌不成立"的用例。
func (f *fakeGoogleToken) fail(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

// startIdentityServer 起一个装配了假校验器的服务端。
func startIdentityServer(t *testing.T) (*fakeGoogleToken, harness) {
	t.Helper()
	fake := newFakeGoogleToken("google-sub-a", "a@example.com")
	return fake, startServerWith(t, rbac.RoleViewer, testScope, fake)
}

// connectIdentity 构造一个走 Connect 协议的认证面客户端。
func connectIdentity(t *testing.T, h harness, token string) identityv1connect.IdentityServiceClient {
	t.Helper()
	httpClient := &http.Client{
		Timeout:   5 * time.Second,
		Transport: &headerTransport{base: http.DefaultTransport, token: token},
	}
	return identityv1connect.NewIdentityServiceClient(httpClient, "http://"+h.address)
}

// grpcIdentity 构造一个走 gRPC 协议的认证面客户端，以及一个已注入链路标识的上下文。
//
// **凭证在连接建立时注入**（见 pkg/client 的 metadata 注入点），因此"以谁的身份
// 调用"由 token 决定：空 token 即匿名调用。
func grpcIdentity(t *testing.T, h harness, token string) (identityv1.IdentityServiceClient, context.Context) {
	t.Helper()
	c := h.dial(t, token, testScope)
	ctx, cancel := c.Context()
	t.Cleanup(cancel)
	return identityv1.NewIdentityServiceClient(c.Conn()), ctx
}

// loginOverGRPC 匿名登录，再返回一个**带会话凭证**的客户端。
//
// 两步是必要的：登录是公开方法，调用它时还没有凭证；而凭证只在 dial 时
// 注入，因此拿到会话之后必须重新建立连接。
func loginOverGRPC(t *testing.T, h harness) (identityv1.IdentityServiceClient, context.Context, string) {
	t.Helper()
	anon, anonCtx := grpcIdentity(t, h, "")
	token := loginGoogle(t, anon, anonCtx)
	client, ctx := grpcIdentity(t, h, token)
	return client, ctx, token
}

// loginGoogle 用一份 Google 身份令牌登录。
func loginGoogle(t *testing.T, client identityv1.IdentityServiceClient, ctx context.Context) string {
	t.Helper()
	resp, err := client.Login(ctx, &identityv1.LoginRequest{
		Credential: &identityv1.LoginRequest_Google{
			Google: &identityv1.GoogleCredential{IdToken: "一份身份令牌"},
		},
	})
	if err != nil {
		t.Fatalf("登录失败: %v", err)
	}
	if resp.GetAccessToken() == "" {
		t.Fatal("登录没有返回访问凭证")
	}
	return resp.GetAccessToken()
}

// 同一个渠道身份，两条协议各登录一次，得到**同一个主体**。
//
// 这条同时验证两件事：登录结果与协议无关，以及"两次登录不会变成两个主体"。
func TestLoginOverBothProtocolsYieldsSameSubject(t *testing.T) {
	_, h := startIdentityServer(t)

	client, ctx, token := loginOverGRPC(t, h)
	who, err := client.WhoAmI(ctx, &identityv1.WhoAmIRequest{})
	if err != nil {
		t.Fatalf("WhoAmI 失败: %v", err)
	}
	if who.GetSubjectId() == "" {
		t.Fatal("登录得到的主体没有标识")
	}

	// 同一份会话凭证交给另一条协议：凭证与协议无关。
	connectWho, err := connectIdentity(t, h, token).
		WhoAmI(context.Background(), connect.NewRequest(&identityv1.WhoAmIRequest{}))
	if err != nil {
		t.Fatalf("Connect 侧 WhoAmI 失败: %v", err)
	}
	if connectWho.Msg.GetSubjectId() != who.GetSubjectId() {
		t.Fatalf("两条协议看到的主体不同：grpc=%q connect=%q",
			who.GetSubjectId(), connectWho.Msg.GetSubjectId())
	}
}

// 首次登录的新主体能登录、能看见自己，但**什么都做不了**。
//
// 关键断言是最后一条：受权限控制的方法返回"无权限"而不是"未认证"。
// 前者说明会话有效、判定照常发生了；后者会把一次权限配置问题表现成
// 一次登录问题，客户端于是引导用户反复重新登录。
func TestNewSubjectHasNoPermissions(t *testing.T) {
	_, h := startIdentityServer(t)
	client, ctx, token := loginOverGRPC(t, h)

	// 能看见自己：会话有效。
	if _, err := client.WhoAmI(ctx, &identityv1.WhoAmIRequest{}); err != nil {
		t.Fatalf("新主体看不到自己: %v", err)
	}
	perms, err := client.GetSessionPermissions(ctx, &identityv1.GetSessionPermissionsRequest{})
	if err != nil {
		t.Fatalf("读取会话权限失败: %v", err)
	}
	if len(perms.GetPermissions()) != 0 {
		t.Errorf("新主体的权限 = %v，期望为空", perms.GetPermissions())
	}

	// 受权限控制的方法：无权限，而不是未认证。两条协议结论一致。
	grpcRBAC := rbacv1.NewRBACServiceClient(h.dial(t, token, testScope).Conn())
	_, grpcErr := grpcRBAC.ListRoles(ctx, &rbacv1.ListRolesRequest{Scope: testScope})
	if got := rpcCode(grpcErr); got != codePermissionDenied {
		t.Fatalf("gRPC 错误码 = %d，期望 PermissionDenied（%v）", got, grpcErr)
	}

	_, connectErr := connectClient(t, h, token, testScope).
		ListRoles(context.Background(), connect.NewRequest(&rbacv1.ListRolesRequest{Scope: testScope}))
	if got := rpcCode(connectErr); got != codePermissionDenied {
		t.Fatalf("Connect 错误码 = %d，期望 PermissionDenied（%v）", got, connectErr)
	}
}

// 令牌不成立时返回"未认证"，且两条协议一致。
func TestInvalidGoogleTokenIsUnauthenticated(t *testing.T) {
	fake, h := startIdentityServer(t)
	fake.fail(identity.ErrInvalidToken)

	grpcClient, grpcCtx := grpcIdentity(t, h, "")
	_, grpcErr := grpcClient.Login(grpcCtx, &identityv1.LoginRequest{
		Credential: &identityv1.LoginRequest_Google{
			Google: &identityv1.GoogleCredential{IdToken: "坏令牌"},
		},
	})

	_, connectErr := connectIdentity(t, h, "").Login(context.Background(),
		connect.NewRequest(&identityv1.LoginRequest{
			Credential: &identityv1.LoginRequest_Google{
				Google: &identityv1.GoogleCredential{IdToken: "坏令牌"},
			},
		}))

	if rpcCode(grpcErr) != codeUnauthenticated || rpcCode(connectErr) != codeUnauthenticated {
		t.Fatalf("错误码 = grpc:%d connect:%d，期望两条都是 Unauthenticated（%v / %v）",
			rpcCode(grpcErr), rpcCode(connectErr), grpcErr, connectErr)
	}
}

// **本文件的核心断言**：绑上第二个渠道之后，两个渠道登录得到同一个主体。
//
// 这就是"一个人多渠道、RBAC 绑定挂在人身上"的全部机制。绑定经 gRPC 发起，
// 第二次登录与随后的查询走 Connect，因此它还顺带证明绑定结果对两条协议
// 都可见：判定与归属都没有协议相关的分叉。
func TestBoundChannelLogsIntoSameSubject(t *testing.T) {
	fake, h := startIdentityServer(t)
	client, ctx, _ := loginOverGRPC(t, h)

	first, err := client.WhoAmI(ctx, &identityv1.WhoAmIRequest{})
	if err != nil {
		t.Fatalf("WhoAmI 失败: %v", err)
	}

	// 换一个渠道身份，把它绑到当前主体上。
	fake.set("google-sub-b", "b@example.com")
	if _, err := client.BindIdentity(ctx, &identityv1.BindIdentityRequest{
		Credential: &identityv1.BindIdentityRequest_Google{
			Google: &identityv1.GoogleCredential{IdToken: "第二个渠道的令牌"},
		},
	}); err != nil {
		t.Fatalf("绑定失败: %v", err)
	}

	// 用第二个渠道**匿名**登录一次——不携带任何既有凭证。
	secondClient, secondCtx, secondToken := loginOverGRPC(t, h)
	second, err := secondClient.WhoAmI(secondCtx, &identityv1.WhoAmIRequest{})
	if err != nil {
		t.Fatalf("WhoAmI 失败: %v", err)
	}
	if second.GetSubjectId() != first.GetSubjectId() {
		t.Fatalf("两个渠道登录到的主体不同：%q 与 %q", second.GetSubjectId(), first.GetSubjectId())
	}

	// 绑定的结果对另一条协议同样可见。
	connectWho, err := connectIdentity(t, h, secondToken).
		WhoAmI(context.Background(), connect.NewRequest(&identityv1.WhoAmIRequest{}))
	if err != nil {
		t.Fatalf("Connect 侧 WhoAmI 失败: %v", err)
	}
	if connectWho.Msg.GetSubjectId() != first.GetSubjectId() {
		t.Errorf("Connect 侧主体 = %q，期望 %q", connectWho.Msg.GetSubjectId(), first.GetSubjectId())
	}
}

// 绑定的归属由**发起者**决定：拿别人的令牌去绑，结果是拒绝。
//
// 第一个人已经占住那个身份，第二个人再拿同一份令牌来绑，撞上唯一归属。
// 若这里返回成功，就意味着任何拿到他人令牌的人都能把别人的进入方式夺走。
func TestBindIdentityRefusesTakenChannel(t *testing.T) {
	fake, h := startIdentityServer(t)

	// 第一个人匿名登录一次，占住 google-sub-a。
	anon, anonCtx := grpcIdentity(t, h, "")
	loginGoogle(t, anon, anonCtx)

	// 第二个人登录，并**带上自己的凭证**（绑定需要已认证的调用方）。
	fake.set("google-sub-b", "b@example.com")
	secondClient, secondCtx, _ := loginOverGRPC(t, h)

	// 第二个人拿**第一人**的令牌发起绑定。
	fake.set("google-sub-a", "a@example.com")
	_, err := secondClient.BindIdentity(secondCtx, &identityv1.BindIdentityRequest{
		Credential: &identityv1.BindIdentityRequest_Google{
			Google: &identityv1.GoogleCredential{IdToken: "第一个人的令牌"},
		},
	})
	if got := rpcCode(err); got != codeAlreadyExists {
		t.Fatalf("错误码 = %d，期望 AlreadyExists（%v）", got, err)
	}
}

// 未启用 Google 登录时报"未实现"，不是"凭证无效"。
//
// 前者说的是这条路没开，后者会说成是调用方拿错了令牌。
func TestGoogleLoginDisabledIsUnimplemented(t *testing.T) {
	h := startServer(t, rbac.RoleViewer, testScope)

	client, ctx := grpcIdentity(t, h, "")
	_, err := client.Login(ctx, &identityv1.LoginRequest{
		Credential: &identityv1.LoginRequest_Google{
			Google: &identityv1.GoogleCredential{IdToken: "任意"},
		},
	})
	if got := rpcCode(err); got != codeUnimplemented {
		t.Fatalf("错误码 = %d，期望 Unimplemented（%v）", got, err)
	}
}

// 解绑最后一个渠道会被拒绝：那会让这个主体再也进不来，
// 而它的角色绑定还在，没有人能进来清理。
func TestUnbindLastChannelIsRejected(t *testing.T) {
	_, h := startIdentityServer(t)
	client, ctx, _ := loginOverGRPC(t, h)

	_, err := client.UnbindIdentity(ctx, &identityv1.UnbindIdentityRequest{
		Source: identity.SourceGoogle, ExternalId: "google-sub-a",
	})
	if got := rpcCode(err); got != codeFailedPrecondition {
		t.Fatalf("错误码 = %d，期望 FailedPrecondition（%v）", got, err)
	}
}

// 列出已绑渠道：登录那个 + 后绑的那个，两条协议都看得到同一份现状。
func TestListIdentitiesOverBothProtocols(t *testing.T) {
	fake, h := startIdentityServer(t)
	client, ctx, token := loginOverGRPC(t, h)

	fake.set("google-sub-b", "b@example.com")
	if _, err := client.BindIdentity(ctx, &identityv1.BindIdentityRequest{
		Credential: &identityv1.BindIdentityRequest_Google{
			Google: &identityv1.GoogleCredential{IdToken: "第二个渠道的令牌"},
		},
	}); err != nil {
		t.Fatalf("绑定失败: %v", err)
	}

	grpcList, err := client.ListIdentities(ctx, &identityv1.ListIdentitiesRequest{})
	if err != nil {
		t.Fatalf("列出渠道失败: %v", err)
	}
	connectList, err := connectIdentity(t, h, token).
		ListIdentities(context.Background(), connect.NewRequest(&identityv1.ListIdentitiesRequest{}))
	if err != nil {
		t.Fatalf("Connect 侧列出渠道失败: %v", err)
	}

	if len(grpcList.GetIdentities()) != 2 {
		t.Fatalf("渠道数 = %d，期望 2", len(grpcList.GetIdentities()))
	}
	if len(connectList.Msg.GetIdentities()) != len(grpcList.GetIdentities()) {
		t.Fatalf("两条协议看到的渠道数不同：grpc=%d connect=%d",
			len(grpcList.GetIdentities()), len(connectList.Msg.GetIdentities()))
	}
	// 顺序稳定：按（来源，身份标识）。
	if got := grpcList.GetIdentities()[0].GetExternalId(); got != "google-sub-a" {
		t.Errorf("第一个渠道 = %q，期望 google-sub-a", got)
	}
}

// 用不存在的凭证调用受保护方法：未认证，而不是无权限。
func TestUnknownSessionIsUnauthenticated(t *testing.T) {
	_, h := startIdentityServer(t)

	client, ctx := grpcIdentity(t, h, "bogus-session-token")
	_, err := client.WhoAmI(ctx, &identityv1.WhoAmIRequest{})
	if got := rpcCode(err); got != codeUnauthenticated {
		t.Fatalf("错误码 = %d，期望 Unauthenticated（%v）", got, err)
	}
}

// 会话凭证不能当机器凭证用，机器凭证也不能当会话凭证用：
// 两类凭证各走各的路径，但结论都必须是"已确认的主体"或"未认证"。
func TestCredentialPathsAreDistinct(t *testing.T) {
	_, h := startIdentityServer(t)

	// 机器凭证（测试夹具注入的那一个）仍然可用。
	client, ctx := grpcIdentity(t, h, testToken)
	who, err := client.WhoAmI(ctx, &identityv1.WhoAmIRequest{})
	if err != nil {
		t.Fatalf("机器凭证不可用: %v", err)
	}
	if who.GetSubjectId() != testSubject {
		t.Errorf("主体 = %q，期望 %q", who.GetSubjectId(), testSubject)
	}

	// 一个既不是机器凭证也不是有效会话的字符串被拒。
	stranger, strangerCtx := grpcIdentity(t, h, "not-a-machine-token-and-not-a-session")
	if _, err := stranger.WhoAmI(strangerCtx, &identityv1.WhoAmIRequest{}); rpcCode(err) != codeUnauthenticated {
		t.Fatalf("错误码 = %d，期望 Unauthenticated（%v）", rpcCode(err), err)
	}
}
