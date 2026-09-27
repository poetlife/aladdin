//go:build e2e

package e2e

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"connectrpc.com/connect"

	identityv1 "github.com/poetlife/aladdin/api/gen/aladdin/identity/v1"
	"github.com/poetlife/aladdin/internal/identity"
	"github.com/poetlife/aladdin/internal/rbac"
	"github.com/poetlife/aladdin/internal/server"
)

// 本文件端到端验证重定向型登录渠道（GitHub）的接入。
//
// 它跑的是**真实的存储与完整的中间件链**：这条路径有一半的价值在于"它是
// 浏览器直连的非 RPC 入口"，只在 handler 层测会把那一半漏掉。
//
// **不联网**：渠道凭证的校验器是一个可控替身；与 GitHub 的真实交互由
// internal/identity 的用例逐条覆盖。

const e2ePublicBaseURL = "https://aladdin.example.test"

// staticVerifier 把任何凭证都解成同一个渠道身份。
//
// 它不做失败分支：与 GitHub 的真实交互、以及各类失败如何分类，由
// internal/identity 的用例逐条覆盖；这里验证的是**接入**。
type staticVerifier struct {
	identity identity.VerifiedIdentity
}

func (v staticVerifier) Verify(context.Context, string) (identity.VerifiedIdentity, error) {
	return v.identity, nil
}

// startGithubServer 起一个同时装了 Google 与 GitHub 两条渠道的服务端。
func startGithubServer(t *testing.T) harness {
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
func TestAuthMethodsListedOverBothProtocols(t *testing.T) {
	h := startGithubServer(t)

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

	gotGRPC := sourceToClientID(overGRPC.GetMethods())
	gotConnect := sourceToClientID(overConnect.GetMethods())
	if len(gotGRPC) != 2 {
		t.Fatalf("gRPC 下发了 %d 条渠道，期望 2 条：%v", len(gotGRPC), gotGRPC)
	}
	if gotGRPC[identity.SourceGoogle] == "" || gotGRPC[identity.SourceGithub] != "Iv1.e2e" {
		t.Errorf("gRPC 下发的渠道清单 = %v", gotGRPC)
	}
	// 两种协议必须看到同一份：只测一种协议等于没测另一种。
	for source, clientID := range gotGRPC {
		if gotConnect[source] != clientID {
			t.Errorf("渠道 %s 在两种协议下的客户端标识不一致：%q vs %q",
				source, clientID, gotConnect[source])
		}
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
func TestGithubRedirectLoginEndToEnd(t *testing.T) {
	h := startGithubServer(t)
	client := noFollowClient()
	base := "http://" + h.address

	// 起点端点：把浏览器交给 GitHub，并写下一个凭据 cookie。
	startResp, err := client.Get(base + identity.GithubStartPath)
	if err != nil {
		t.Fatalf("访问起点端点失败: %v", err)
	}
	_ = startResp.Body.Close()
	if startResp.StatusCode != http.StatusFound {
		t.Fatalf("起点端点状态码 = %d，期望 302（被中间件挡住时会是一个拒绝响应）", startResp.StatusCode)
	}
	state := stateCookie(t, startResp)
	if state == "" {
		t.Fatal("起点端点没有写下登录凭据")
	}

	// 回调端点：凭据相符，完成登录并把会话凭证送回前端。
	callbackURL := base + identity.GithubCallbackPath + "?code=an-auth-code&state=" + url.QueryEscape(state)
	req, err := http.NewRequest(http.MethodGet, callbackURL, nil)
	if err != nil {
		t.Fatalf("构造回调请求失败: %v", err)
	}
	req.AddCookie(&http.Cookie{Name: "aladdin_github_state", Value: state})
	callbackResp, err := client.Do(req)
	if err != nil {
		t.Fatalf("访问回调端点失败: %v", err)
	}
	_ = callbackResp.Body.Close()
	if callbackResp.StatusCode != http.StatusFound {
		t.Fatalf("回调端点状态码 = %d，期望 302", callbackResp.StatusCode)
	}

	location := callbackResp.Header.Get("Location")
	prefix := e2ePublicBaseURL + server.FrontendCallbackPath + "#"
	if !strings.HasPrefix(location, prefix) {
		t.Fatalf("跳转目标 = %q，期望 %q 开头", location, prefix)
	}
	token := fragmentParam(t, location, "token")
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
}

// 凭据不符时**不签发任何会话**。
func TestGithubCallbackRejectsMismatchedStateEndToEnd(t *testing.T) {
	h := startGithubServer(t)
	client := noFollowClient()
	base := "http://" + h.address

	startResp, err := client.Get(base + identity.GithubStartPath)
	if err != nil {
		t.Fatalf("访问起点端点失败: %v", err)
	}
	_ = startResp.Body.Close()
	state := stateCookie(t, startResp)

	// 地址里的 state 与 cookie 换成不同的取值。
	req, err := http.NewRequest(http.MethodGet,
		base+identity.GithubCallbackPath+"?code=an-auth-code&state=a-different-state", nil)
	if err != nil {
		t.Fatalf("构造回调请求失败: %v", err)
	}
	req.AddCookie(&http.Cookie{Name: "aladdin_github_state", Value: state})
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("访问回调端点失败: %v", err)
	}
	_ = resp.Body.Close()

	location := resp.Header.Get("Location")
	if !strings.Contains(location, "error=") {
		t.Fatalf("跳转目标 = %q，期望带一个失败标记", location)
	}
	if strings.Contains(location, "token=") {
		t.Error("凭据不符不得交付任何会话凭证")
	}
}

// noFollowClient 不跟随跳转：跳转本身就是被测对象。
func noFollowClient() *http.Client {
	return &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// stateCookie 取出起点端点写下的登录凭据。
func stateCookie(t *testing.T, resp *http.Response) string {
	t.Helper()
	for _, cookie := range resp.Cookies() {
		if cookie.Name == "aladdin_github_state" {
			return cookie.Value
		}
	}
	return ""
}

// fragmentParam 取出跳转目标里某个 fragment 的取值。
func fragmentParam(t *testing.T, location, key string) string {
	t.Helper()
	u, err := url.Parse(location)
	if err != nil {
		t.Fatalf("跳转目标无法解析: %v", err)
	}
	params, err := url.ParseQuery(u.Fragment)
	if err != nil {
		t.Fatalf("fragment 无法解析: %v", err)
	}
	return params.Get(key)
}

// sourceToClientID 把渠道清单整理成"来源 → 客户端标识"。
func sourceToClientID(methods []*identityv1.AuthMethod) map[string]string {
	out := make(map[string]string, len(methods))
	for _, m := range methods {
		out[m.GetSource()] = m.GetClientId()
	}
	return out
}
