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
// 错误会被完全漏掉（见 AGENTS.md 的传输方式约定）。
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

func (f *fakeGoogleToken) Verify(context.Context, string) (identity.VerifiedIdentity, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return identity.VerifiedIdentity{}, f.err
	}
	return identity.VerifiedIdentity{ExternalID: f.subject, Display: f.email}, nil
}

// googleChannel 把假校验器登记成 Google 渠道。
func googleChannel(verifier identity.TokenVerifier) identity.Channel {
	return identity.Channel{
		Source:   identity.SourceGoogle,
		ClientID: "e2e-google-client",
		Verifier: verifier,
	}
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
//
// 对外地址是重定向型渠道的前提：起点交给渠道的授权地址、以及回跳前端的地址
// 都由它构造，没有它这条路径整体缺席。
func startIdentityServer(t *testing.T) (*fakeGoogleToken, harness) {
	t.Helper()
	fake := newFakeGoogleToken("google-sub-a", "a@example.com")
	return fake, startServerWith(t, rbac.RoleViewer, testScope,
		withChannels(googleChannel(fake)), withPublicBaseURL(e2ePublicBaseURL))
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
// **凭证在连接建立时注入**（见 authz_test.go 里 harness.dial 的 credentialInterceptor），
// 因此"以谁的身份调用"由 token 决定：空 token 即匿名调用。
func grpcIdentity(t *testing.T, h harness, token string) (identityv1.IdentityServiceClient, context.Context) {
	t.Helper()
	c := h.dial(t, token, testScope)
	ctx, cancel := c.Context()
	t.Cleanup(cancel)
	return identityv1.NewIdentityServiceClient(c.Conn()), ctx
}

// loginOverGRPC 匿名走一次完整的重定向登录，再返回一个**带会话凭证**的客户端。
//
// 两步仍然是必要的，只是形状跟着渠道变了：登录是**浏览器直连的公开入口**，
// 匿名调用它时还没有任何凭证；会话凭证由回调交回前端，客户端之后才在建立
// 连接时把它注入（凭证只在 dial 时注入，见 grpcIdentity）。
func loginOverGRPC(t *testing.T, h harness) (identityv1.IdentityServiceClient, context.Context, string) {
	t.Helper()
	token := loginGoogleOverHTTP(t, h)
	client, ctx := grpcIdentity(t, h, token)
	return client, ctx, token
}

// bindChannelOverGRPC 走一次完整的绑定：浏览器侧起点 + 回调产出待绑定凭据，
// 再由这份凭据的持有者用**已认证的** gRPC 会话兑换。
//
// 渠道凭证不在这里：它由回调用与登录完全相同的校验器校验过，并记成一份一次性
// 待绑定凭据，经 HttpOnly cookie 交给浏览器。归属只取兑换方会话代表的主体，
// 请求里没有主体字段——因此"拿别人的凭据来绑"不会把别人的进入方式夺走。
func bindChannelOverGRPC(
	t *testing.T,
	h harness,
	client identityv1.IdentityServiceClient,
	ctx context.Context,
	c redirectChannelCase,
) (*identityv1.CompleteIdentityBindingResponse, error) {
	t.Helper()

	pending := redirectBindPendingCookie(t, noFollowClient(), httpBase(h), c)
	return client.CompleteIdentityBinding(withPendingBindingCookie(ctx, pending),
		&identityv1.CompleteIdentityBindingRequest{Source: c.source})
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

// 渠道凭证不成立时**不交付任何会话**：回调把浏览器送回前端，并带一个固定的
// 失败标记。
//
// 失败的形状不再是 RPC 错误码：凭证由渠道直接交给浏览器的直连端点，调用方
// 根本没有经手它的机会，因此"未认证"在这里的落点就是"拿不到会话"。
//
// 这层意思仍然保留：**未认证不是无权限**。同一份受保护方法，匿名调用得到
// Unauthenticated，而一个有效但零权限的会话得到 PermissionDenied。混为一谈
// 会让客户端把一次权限配置问题放大成一次登录风暴。
func TestInvalidGoogleTokenIsUnauthenticated(t *testing.T) {
	fake, h := startIdentityServer(t)
	fake.fail(identity.ErrInvalidToken)

	c := googleRedirectCase()
	client := noFollowClient()
	base := httpBase(h)

	state := startRedirect(t, client, base, c, "")
	res := redirectCallback(t, client, base, c, state, "坏令牌", state.Value)
	if got := fragmentParam(t, res.location, "error"); got != c.loginFailed {
		t.Fatalf("跳转目标 = %q，期望带固定失败标记 %q", res.location, c.loginFailed)
	}
	if fragmentParam(t, res.location, "token") != "" {
		t.Error("凭证不成立不得交付任何会话凭证")
	}

	// 未认证：匿名调用受保护方法。
	// WhoAmI 是 authenticated_only 的方法，匿名调用没有主体可取。
	anonClient, anonCtx := grpcIdentity(t, h, "")
	if _, err := anonClient.WhoAmI(anonCtx, &identityv1.WhoAmIRequest{}); rpcCode(err) != codeUnauthenticated {
		t.Fatalf("匿名调用受保护方法的错误码 = %d，期望 Unauthenticated（%v）", rpcCode(err), err)
	}
	// 两条协议结论一致：拒绝响应由 ErrorWriter 按协议写出，只测一种会漏掉
	// 另一种上的编码错误。
	if _, err := connectClient(t, h, "", testScope).ListRoles(context.Background(),
		connect.NewRequest(&rbacv1.ListRolesRequest{Scope: testScope})); rpcCode(err) != codeUnauthenticated {
		t.Fatalf("Connect 侧匿名调用受保护方法的错误码 = %d，期望 Unauthenticated（%v）", rpcCode(err), err)
	}

	// 无权限：换回一份能成立的凭证，拿一个有效但零权限的会话问同一条路径。
	fake.set("google-sub-a", "a@example.com")
	zeroPermToken := loginGoogleOverHTTP(t, h)
	rbacClient := rbacv1.NewRBACServiceClient(h.dial(t, zeroPermToken, testScope).Conn())
	if _, err := rbacClient.ListRoles(anonCtx, &rbacv1.ListRolesRequest{Scope: testScope}); rpcCode(err) != codePermissionDenied {
		t.Fatalf("零权限会话的错误码 = %d，期望 PermissionDenied（%v）", rpcCode(err), err)
	}
}

// 未启用 Google 渠道时两个浏览器直连端点都**不存在**。
//
// 报"没找到"而不是"未实现"：它们是浏览器导航，没有 RPC 那套错误码可用。
// 两个端点一起缺席是有意的——只关掉起点会让一条直接构造的回调地址仍然可达。
//
// 每条渠道各跑一遍：渠道清单里少注册一条、或注册了却漏在放行清单之外，
// 都会在这里暴露。
func TestGoogleLoginDisabledEndpointsNotFound(t *testing.T) {
	h := startServer(t, rbac.RoleViewer, testScope)
	client := noFollowClient()

	for _, c := range redirectChannelCases() {
		for _, path := range []string{c.startPath, c.callbackPath} {
			resp, err := client.Get(httpBase(h) + path)
			if err != nil {
				t.Fatalf("访问 %s 失败: %v", path, err)
			}
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusNotFound {
				t.Errorf("%s 状态码 = %d，期望 404", path, resp.StatusCode)
			}
		}
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

	// 换一个渠道身份，把它绑到当前主体上：浏览器侧导航产出待绑定凭据，
	// 再用当前会话兑换。
	fake.set("google-sub-b", "b@example.com")
	if _, err := bindChannelOverGRPC(t, h, client, ctx, googleRedirectCase()); err != nil {
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

// 绑定的归属由**发起者**决定：拿别人渠道的凭据去绑，结果是拒绝。
//
// 第一个人已经占住那个身份，第二个人再拿同一份渠道凭据来绑，撞上唯一归属。
// 若这里返回成功，就意味着任何能走通一次别人渠道导航的人都能把别人的进入
// 方式夺走。
//
// 第一个人在这里必须是**非空主体**：只有一条身份的空主体会被认领而不是拒绝
// ——登录与绑定两条去向的归属语义完全相同（认领见 TestCompleteIdentityBindingReclaimsVacantChannel）。
func TestCompleteIdentityBindingRefusesTakenChannel(t *testing.T) {
	fake, h := startIdentityServer(t)

	// 第一个人登录一次，占住 google-sub-a。
	firstClient, firstCtx, _ := loginOverGRPC(t, h)

	// 再给他绑上第二个渠道，使他不再是空主体。
	fake.set("google-sub-a2", "a2@example.com")
	if _, err := bindChannelOverGRPC(t, h, firstClient, firstCtx, googleRedirectCase()); err != nil {
		t.Fatalf("给第一个人绑定第二个渠道失败: %v", err)
	}

	// 第二个人登录，并**带上自己的凭证**（绑定需要已认证的调用方）。
	fake.set("google-sub-b", "b@example.com")
	secondClient, secondCtx, _ := loginOverGRPC(t, h)

	// 第二个人拿**第一人**的凭据发起绑定：第一个人非空，必须拒绝。
	fake.set("google-sub-a", "a@example.com")
	_, err := bindChannelOverGRPC(t, h, secondClient, secondCtx, googleRedirectCase())
	if got := rpcCode(err); got != codeAlreadyExists {
		t.Fatalf("错误码 = %d，期望 AlreadyExists（%v）", got, err)
	}
}

// 已属于空主体的身份在兑换时被认领，而不是被拒绝。
//
// 认领的必要性：一个人先单独登录过一次（得到一个只有一条身份的零权限主体），
// 之后在另一个主体上做绑定。若一律按"已占用"拒绝，用户看到的是一个再也解不
// 开的死结——那个空主体没有任何人能用，却挡住了他唯一的进入方式。
func TestCompleteIdentityBindingReclaimsVacantChannel(t *testing.T) {
	fake, h := startIdentityServer(t)

	// 第一个人单独登录，得到一个只有 google-sub-a 的零权限主体。
	throwawayClient, throwawayCtx, _ := loginOverGRPC(t, h)
	throwaway, err := throwawayClient.WhoAmI(throwawayCtx, &identityv1.WhoAmIRequest{})
	if err != nil {
		t.Fatalf("WhoAmI 失败: %v", err)
	}

	// 第二个人登录（占住 google-sub-b）。
	fake.set("google-sub-b", "b@example.com")
	secondClient, secondCtx, _ := loginOverGRPC(t, h)
	second, err := secondClient.WhoAmI(secondCtx, &identityv1.WhoAmIRequest{})
	if err != nil {
		t.Fatalf("WhoAmI 失败: %v", err)
	}
	if second.GetSubjectId() == throwaway.GetSubjectId() {
		t.Fatal("夹具坏了：两个渠道登录到了同一个主体")
	}

	// 第二个人拿**第一人**的凭据绑定：第一人是空主体，应当被认领。
	fake.set("google-sub-a", "a@example.com")
	resp, err := bindChannelOverGRPC(t, h, secondClient, secondCtx, googleRedirectCase())
	if err != nil {
		t.Fatalf("认领失败: %v", err)
	}
	if !resp.GetReclaimed() {
		t.Error("发生了认领，reclaimed 却为 false")
	}

	// 原主体只剩零身份。
	left, err := throwawayClient.ListIdentities(throwawayCtx, &identityv1.ListIdentitiesRequest{})
	if err != nil {
		t.Fatalf("列出原主体失败: %v", err)
	}
	if n := len(left.GetIdentities()); n != 0 {
		t.Errorf("原主体还剩 %d 条身份，期望 0", n)
	}

	// 再用这个渠道登录，得到的应当是第二个人的主体。
	againClient, againCtx, _ := loginOverGRPC(t, h)
	again, err := againClient.WhoAmI(againCtx, &identityv1.WhoAmIRequest{})
	if err != nil {
		t.Fatalf("WhoAmI 失败: %v", err)
	}
	if again.GetSubjectId() != second.GetSubjectId() {
		t.Errorf("再次登录得到 %q，期望 %q", again.GetSubjectId(), second.GetSubjectId())
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
	if _, err := bindChannelOverGRPC(t, h, client, ctx, googleRedirectCase()); err != nil {
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
