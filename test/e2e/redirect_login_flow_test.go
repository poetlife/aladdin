//go:build e2e

package e2e

import (
	"context"
	"strings"
	"testing"

	"connectrpc.com/connect"

	identityv1 "github.com/poetlife/aladdin/api/gen/aladdin/identity/v1"
	"github.com/poetlife/aladdin/internal/identity"
	"github.com/poetlife/aladdin/internal/rbac"
	"github.com/poetlife/aladdin/internal/server"
)

// 本文件端到端验证重定向型登录渠道的接入。
//
// 它跑的是**真实的存储与完整的中间件链**：这条路径有一半的价值在于"它是
// 浏览器直连的非 RPC 入口"，只在 handler 层测会把那一半漏掉。
//
// Google 与 GitHub 走的是同一段流程，因此每一条用例都对**两条渠道各跑一遍**
// （共享形状见 redirect_login_test.go）。只测一条渠道，另一条上的差异——端点
// 路径、cookie 名、失败标记——会被完全漏掉。
//
// **不联网**：渠道凭证的校验器是一个可控替身；与渠道的真实交互由
// internal/identity 的用例逐条覆盖。

const e2ePublicBaseURL = "https://aladdin.example.test"

// staticVerifier 把任何凭证都解成同一个渠道身份。
//
// 它不做失败分支：与渠道的真实交互、以及各类失败如何分类，由
// internal/identity 的用例逐条覆盖；这里验证的是**接入**。
type staticVerifier struct {
	identity identity.VerifiedIdentity
}

func (v staticVerifier) Verify(context.Context, string) (identity.VerifiedIdentity, error) {
	return v.identity, nil
}

// startBothChannelsServer 起一个同时装了 Google 与 GitHub 两条渠道的服务端。
func startBothChannelsServer(t *testing.T) harness {
	t.Helper()

	return startServerWith(t, rbac.RoleViewer, testScope,
		withChannels(googleChannel(newFakeGoogleToken("google-sub-a", "a@example.com")),
			identity.Channel{
				Source:   identity.SourceGithub,
				ClientID: "Iv1.e2e",
				Verifier: staticVerifier{identity: identity.VerifiedIdentity{
					ExternalID: "424242", Display: "octocat",
				}},
			}),
		withPublicBaseURL(e2ePublicBaseURL),
	)
}

// 下发的渠道清单由服务端派生，且两种协议看到同一份。
//
// 比对的是**来源集合**：渠道都是重定向型之后，AuthMethod 里只剩 source，
// 客户端标识由服务端持有、不经客户端过手（见 identity.proto 的 AuthMethod）。
func TestAuthMethodsListedOverBothProtocols(t *testing.T) {
	h := startBothChannelsServer(t)

	grpcClient, ctx := grpcIdentity(t, h, "")
	overGRPC, err := grpcClient.GetAuthMethods(ctx, &identityv1.GetAuthMethodsRequest{})
	if err != nil {
		t.Fatalf("gRPC 查询登录方式失败: %v", err)
	}

	connectResp, err := connectIdentity(t, h, "").GetAuthMethods(context.Background(),
		connect.NewRequest(&identityv1.GetAuthMethodsRequest{}))
	if err != nil {
		t.Fatalf("Connect 查询登录方式失败: %v", err)
	}
	overConnect := connectResp.Msg

	gotGRPC := sourcesOf(overGRPC.GetMethods())
	gotConnect := sourcesOf(overConnect.GetMethods())
	if len(gotGRPC) != 2 {
		t.Fatalf("gRPC 下发了 %d 条渠道，期望 2 条：%v", len(gotGRPC), gotGRPC)
	}
	if !gotGRPC[identity.SourceGoogle] || !gotGRPC[identity.SourceGithub] {
		t.Errorf("gRPC 下发的渠道集合 = %v", gotGRPC)
	}
	// 两种协议必须看到同一份：只测一种协议等于没测另一种。
	for source := range gotGRPC {
		if !gotConnect[source] {
			t.Errorf("渠道 %s 在 Connect 下不见了：gRPC=%v Connect=%v", source, gotGRPC, gotConnect)
		}
	}
	if len(gotConnect) != len(gotGRPC) {
		t.Errorf("两条协议下发的渠道数不同：gRPC=%v Connect=%v", gotGRPC, gotConnect)
	}
}

// 未启用任何渠道时清单为空——前端据此不渲染任何入口。
func TestAuthMethodsEmptyWhenNoChannelEnabled(t *testing.T) {
	h := startServer(t, rbac.RoleViewer, testScope)

	resp, err := connectIdentity(t, h, "").GetAuthMethods(context.Background(),
		connect.NewRequest(&identityv1.GetAuthMethodsRequest{}))
	if err != nil {
		t.Fatalf("查询登录方式失败: %v", err)
	}
	if n := len(resp.Msg.GetMethods()); n != 0 {
		t.Errorf("下发了 %d 条渠道，期望 0 条", n)
	}
}

// 走一次完整的重定向登录：起点端点写凭据，回调端点换出会话。
//
// 会话与身份别名都落在**真实数据库**上：这条路径的价值之一就是"表真的建了、
// 写入真的落盘了"。
func TestRedirectLoginEndToEnd(t *testing.T) {
	for _, c := range redirectChannelCases() {
		t.Run(c.source, func(t *testing.T) {
			h := startBothChannelsServer(t)
			client := noFollowClient()
			base := httpBase(h)

			// 起点端点：把浏览器交给渠道，并写下一个凭据 cookie。
			state := startRedirect(t, client, base, c, "")
			if state.Value == "" {
				t.Fatal("起点端点写下了空凭据")
			}

			// 回调端点：凭据相符，完成登录并把会话凭证送回前端。
			res := redirectCallback(t, client, base, c, state, "an-auth-code", state.Value)

			prefix := e2ePublicBaseURL + server.FrontendCallbackPath + "#"
			if !strings.HasPrefix(res.location, prefix) {
				t.Fatalf("跳转目标 = %q，期望 %q 开头", res.location, prefix)
			}
			token := fragmentParam(t, res.location, "token")
			if token == "" {
				t.Fatal("跳转目标里没有会话凭证")
			}

			// 签发的会话必须真的被认证路径认下来——并且两种协议认下来的是同一个主体。
			grpcClient, ctx := grpcIdentity(t, h, token)
			overGRPC, err := grpcClient.WhoAmI(ctx, &identityv1.WhoAmIRequest{})
			if err != nil {
				t.Fatalf("gRPC 用签发的会话调用失败: %v", err)
			}
			connectResp, err := connectIdentity(t, h, token).WhoAmI(context.Background(),
				connect.NewRequest(&identityv1.WhoAmIRequest{}))
			if err != nil {
				t.Fatalf("Connect 用签发的会话调用失败: %v", err)
			}

			if overGRPC.GetSubjectId() == "" {
				t.Error("会话没有代表任何主体")
			}
			if overGRPC.GetSubjectId() != connectResp.Msg.GetSubjectId() {
				t.Errorf("两种协议对同一份会话给出的主体不一致：%q vs %q",
					overGRPC.GetSubjectId(), connectResp.Msg.GetSubjectId())
			}
			// 新主体是零权限的：这是预期路径，不是错误路径。走真实的 RBAC 引擎问一次，
			// 而不是去读绑定表——用户看得见的是"界面上什么都没有"。
			perms, err := grpcClient.GetSessionPermissions(ctx, &identityv1.GetSessionPermissionsRequest{})
			if err != nil {
				t.Fatalf("查询会话权限失败: %v", err)
			}
			if n := len(perms.GetPermissions()); n != 0 {
				t.Errorf("新主体带着 %d 个权限码，期望零权限", n)
			}
		})
	}
}

// 凭据不符时**不签发任何会话**。
func TestRedirectCallbackRejectsMismatchedStateEndToEnd(t *testing.T) {
	for _, c := range redirectChannelCases() {
		t.Run(c.source, func(t *testing.T) {
			h := startBothChannelsServer(t)
			client := noFollowClient()
			base := httpBase(h)

			state := startRedirect(t, client, base, c, "")

			// 地址里的 state 与 cookie 换成不同的取值。
			res := redirectCallback(t, client, base, c, state, "an-auth-code", "a-different-state")

			if !strings.Contains(res.location, "error=") {
				t.Fatalf("跳转目标 = %q，期望带一个失败标记", res.location)
			}
			if strings.Contains(res.location, "token=") {
				t.Error("凭据不符不得交付任何会话凭证")
			}
		})
	}
}

// sourcesOf 把渠道清单整理成一个来源集合。
//
// 用集合而不是切片：下发的顺序是实现细节，端到端要固定的是"哪些渠道可见"。
func sourcesOf(methods []*identityv1.AuthMethod) map[string]bool {
	out := make(map[string]bool, len(methods))
	for _, m := range methods {
		out[m.GetSource()] = true
	}
	return out
}

// 先用一条渠道单独登录过一次、再用另一条渠道登录并绑定它：
// 空主体被认领，之后前者登录进的是后者的主体。
//
// 这条把"绑定"与"登录"两条真实路径接起来：单测各自冻结一半，只有端到端
// 才能证明"认领之后，下一次重定向登录真的落到目标主体"。
//
// **两个方向各跑一遍**：只测一个方向的话，"能被认领的空主体恰好是某一条
// 渠道"这种实现细节不会被发现。
func TestRedirectBindReclaimsVacantSubjectEndToEnd(t *testing.T) {
	for _, tc := range []struct {
		standalone redirectChannelCase
		binder     redirectChannelCase
	}{
		{githubRedirectCase(), googleRedirectCase()},
		{googleRedirectCase(), githubRedirectCase()},
	} {
		t.Run(tc.standalone.source+"→"+tc.binder.source, func(t *testing.T) {
			h := startBothChannelsServer(t)
			client := noFollowClient()
			base := httpBase(h)

			// 1. 先用 standalone 渠道单独登录一次，得到零权限主体 E。
			standalone := redirectLoginToken(t, client, base, tc.standalone)
			throwaway := whoAmISubject(t, h, standalone)

			// 2. 再用 binder 渠道登录，得到另一个主体 A。
			adminToken := redirectLoginToken(t, client, base, tc.binder)
			admin := whoAmISubject(t, h, adminToken)
			if admin == throwaway {
				t.Fatal("两个渠道首次登录应当是不同主体——否则这条用例测不到认领")
			}

			// 3. 从 A 的会话发起 standalone 渠道的绑定，走完整的两段式：导航 + 已认证兑换。
			pending := redirectBindPendingCookie(t, client, base, tc.standalone)
			bindResp, err := connectIdentity(t, h, adminToken).CompleteIdentityBinding(
				context.Background(), bindingRequest(pending, tc.standalone.source))
			if err != nil {
				t.Fatalf("兑换待绑定凭据失败: %v", err)
			}
			if !bindResp.Msg.GetReclaimed() {
				t.Error("该渠道身份属于空主体，期望发生认领")
			}
			if cookies := bindResp.Header().Values("Set-Cookie"); len(cookies) == 0 ||
				!strings.Contains(cookies[0], pendingBindingCookie+"=") {
				t.Errorf("兑换成功后没有清掉浏览器侧凭据: %v", cookies)
			}

			// 4. 再用 standalone 渠道登录一次：这次必须落到 A，而不是原来的空主体 E。
			again := redirectLoginToken(t, client, base, tc.standalone)
			if got := whoAmISubject(t, h, again); got != admin {
				t.Errorf("认领后 %s 登录得到 %q，期望 %q", tc.standalone.source, got, admin)
			}
		})
	}
}
