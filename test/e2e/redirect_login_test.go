//go:build e2e

package e2e

import (
	"context"
	"net/http"
	"net/url"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/grpc/metadata"

	identityv1 "github.com/poetlife/aladdin/api/gen/aladdin/identity/v1"
	"github.com/poetlife/aladdin/internal/identity"
)

// 本文件是重定向型登录渠道在端到端测试里的**共用形状**。
//
// Google 与 GitHub 走的是同一段流程，端到端层面只有这几处差异：来源标识、
// 两个浏览器直连端点的路径、导航凭据的 cookie 名、失败标记。把它们按渠道
// 参数化，是为了让每一条用例对两条渠道各跑一遍——任何"只对某一条渠道成立"
// 的假设会在这里暴露，而不是等到只有那条渠道的用户遇到。
//
// 为什么 cookie 名在测试里写成**字面量**：服务端那套派生（aladdin_<source>_state）
// 是包内未导出的实现细节，外部测试拿不到它。写死恰好把**浏览器侧契约**固定
// 住——cookie 名一改，存量进行中的导航就全断了，这个字面量就是那道闸。

// pendingBindingCookie 是承载"服务端已校验、等待绑定"的身份的 cookie 名。
//
// 与导航凭据同理，它是浏览器与兑换 RPC 之间的契约，端到端测试按字面量固定它。
const pendingBindingCookie = "aladdin_identity_binding"

// redirectChannelCase 是一条重定向型渠道在端到端测试里的全部差异。
type redirectChannelCase struct {
	source       string
	startPath    string
	callbackPath string
	// stateCookie 是起点写下、回调校验的导航凭据 cookie 名。
	stateCookie string
	// loginFailed / bindFailed 是回跳前端时附在地址上的固定失败标记。
	loginFailed string
	bindFailed  string
}

// redirectChannelCases 返回全部渠道。逐条跑，不挑渠道。
func redirectChannelCases() []redirectChannelCase {
	return []redirectChannelCase{googleRedirectCase(), githubRedirectCase()}
}

func googleRedirectCase() redirectChannelCase {
	return redirectChannelCase{
		source:       identity.SourceGoogle,
		startPath:    identity.GoogleStartPath,
		callbackPath: identity.GoogleCallbackPath,
		stateCookie:  "aladdin_google_state",
		loginFailed:  "google_login_failed",
		bindFailed:   "google_bind_failed",
	}
}

func githubRedirectCase() redirectChannelCase {
	return redirectChannelCase{
		source:       identity.SourceGithub,
		startPath:    identity.GithubStartPath,
		callbackPath: identity.GithubCallbackPath,
		stateCookie:  "aladdin_github_state",
		loginFailed:  "github_login_failed",
		bindFailed:   "github_bind_failed",
	}
}

// httpBase 是被测服务端的明文地址：端到端测试直接打它。
//
// 对外源（e2ePublicBaseURL）只用于断言"跳转目标是服务端按配置构造的"，
// 不用来发请求——否则一个"用请求头拼跳转目标"的缺陷会被夹具掩盖。
func httpBase(h harness) string { return "http://" + h.address }

// noFollowClient 不跟随跳转：跳转本身就是被测对象。
func noFollowClient() *http.Client {
	return &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// startRedirect 走一次起点端点，返回服务端写下的导航凭据 cookie。
//
// purpose 为空表示登录；"bind" 表示这次导航是为绑定发起的。
func startRedirect(t *testing.T, client *http.Client, base string, c redirectChannelCase, purpose string) *http.Cookie {
	t.Helper()

	target := base + c.startPath
	if purpose != "" {
		target += "?purpose=" + url.QueryEscape(purpose)
	}
	resp, err := client.Get(target)
	if err != nil {
		t.Fatalf("访问 %s 起点端点失败: %v", c.source, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusFound {
		t.Fatalf("%s 起点端点状态码 = %d，期望 302（被中间件挡住时会是一个拒绝响应）",
			c.source, resp.StatusCode)
	}
	for _, cookie := range resp.Cookies() {
		if cookie.Name == c.stateCookie {
			if cookie.Value == "" {
				t.Fatalf("%s 起点端点写下了空凭据", c.source)
			}
			return cookie
		}
	}
	t.Fatalf("%s 起点端点没有写下导航凭据 cookie %s", c.source, c.stateCookie)
	return nil
}

// redirectCallbackResult 是一次回调响应里调用方需要的东西；响应体已在 helper 里关闭。
type redirectCallbackResult struct {
	location string
	cookies  []*http.Cookie
}

// redirectCallback 走一次回调端点。cookie 为 nil 表示不携带导航凭据。
//
// 三类失败的形状（凭据缺失、凭据不符、服务端没发出过）在这里不分类：它们都
// 表现为"跳回前端并带失败标记"，分类由 internal/server 的用例逐条覆盖。
func redirectCallback(t *testing.T, client *http.Client, base string, c redirectChannelCase, cookie *http.Cookie, code, state string) redirectCallbackResult {
	t.Helper()

	target, err := url.Parse(base + c.callbackPath)
	if err != nil {
		t.Fatalf("构造 %s 回调地址失败: %v", c.source, err)
	}
	q := target.Query()
	if code != "" {
		q.Set("code", code)
	}
	if state != "" {
		q.Set("state", state)
	}
	target.RawQuery = q.Encode()

	req, err := http.NewRequest(http.MethodGet, target.String(), nil)
	if err != nil {
		t.Fatalf("构造回调请求失败: %v", err)
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("访问 %s 回调端点失败: %v", c.source, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusFound {
		t.Fatalf("%s 回调端点状态码 = %d，期望 302", c.source, resp.StatusCode)
	}
	return redirectCallbackResult{location: resp.Header.Get("Location"), cookies: resp.Cookies()}
}

// redirectLoginToken 匿名走一次完整的重定向登录，返回交付的会话凭证。
//
// 会话凭证在回跳地址的 fragment 里：fragment 不会发往服务端，因此不进访问
// 日志、也不进跳转来源。这里只做"把它取出来"这一件事。
func redirectLoginToken(t *testing.T, client *http.Client, base string, c redirectChannelCase) string {
	t.Helper()

	state := startRedirect(t, client, base, c, "")
	res := redirectCallback(t, client, base, c, state, "an-auth-code", state.Value)
	token := fragmentParam(t, res.location, "token")
	if token == "" {
		t.Fatalf("%s 登录回调没有交付会话凭证: %q", c.source, res.location)
	}
	return token
}

// redirectBindPendingCookie 走一次绑定起点 + 回调，返回待绑定凭据 cookie。
//
// 绑定回调**不签发会话**：它只产出这份 HttpOnly 凭据，归属留到之后那次
// 已认证的兑换里决定（见 docs/design/identity/identity-linking.md）。
func redirectBindPendingCookie(t *testing.T, client *http.Client, base string, c redirectChannelCase) *http.Cookie {
	t.Helper()

	state := startRedirect(t, client, base, c, "bind")
	res := redirectCallback(t, client, base, c, state, "an-auth-code", state.Value)
	if got := fragmentParam(t, res.location, "binding"); got != c.source {
		t.Fatalf("%s 绑定回调跳转目标 = %q，期望带 binding=%s", c.source, res.location, c.source)
	}
	if fragmentParam(t, res.location, "token") != "" {
		t.Fatalf("%s 绑定回调交付了会话凭证: %q", c.source, res.location)
	}
	for _, cookie := range res.cookies {
		if cookie.Name == pendingBindingCookie {
			return cookie
		}
	}
	t.Fatalf("%s 绑定回调没有写下待绑定凭据 cookie %s", c.source, pendingBindingCookie)
	return nil
}

// loginGoogleOverHTTP 匿名走一次完整的 Google 重定向登录，返回交付的会话凭证。
//
// 登录是**浏览器直连的公开入口**：调用它时还没有任何凭证，因此这里不需要
// 客户端，也不需要先建立 RPC 连接。校验渠道凭证的仍是注入在服务端装配处的
// 替身（见 startIdentityServer），所以这里不联网；"以谁的身份进来"由用例在
// 登录前用 fake.set 决定。
func loginGoogleOverHTTP(t *testing.T, h harness) string {
	t.Helper()
	return redirectLoginToken(t, noFollowClient(), httpBase(h), googleRedirectCase())
}

// pendingBindingHeader 把待绑定 cookie 摊成 Cookie 头的取值。
//
// 只取 name=value：resp.Cookies() 里的对象还带着 Path 等属性，直接把它
// String() 放进请求头会是一条非法请求。
func pendingBindingHeader(cookie *http.Cookie) string {
	return cookie.Name + "=" + cookie.Value
}

// bindingRequest 造一份走 Connect 的兑换请求。
func bindingRequest(cookie *http.Cookie, source string) *connect.Request[identityv1.CompleteIdentityBindingRequest] {
	req := connect.NewRequest(&identityv1.CompleteIdentityBindingRequest{Source: source})
	req.Header().Set("Cookie", pendingBindingHeader(cookie))
	return req
}

// withPendingBindingCookie 给 gRPC 调用注入 Cookie 头。
//
// gRPC 没有独立的 cookie 概念，它就是普通请求头；服务端的兑换读的是请求头里的
// Cookie，因此这里按同名元数据传——两条协议走的是同一条判定路径。
func withPendingBindingCookie(ctx context.Context, cookie *http.Cookie) context.Context {
	return metadata.AppendToOutgoingContext(ctx, "cookie", pendingBindingHeader(cookie))
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

// whoAmISubject 用一份会话问出它代表的真实主体。
func whoAmISubject(t *testing.T, h harness, token string) string {
	t.Helper()
	resp, err := connectIdentity(t, h, token).WhoAmI(
		context.Background(), connect.NewRequest(&identityv1.WhoAmIRequest{}))
	if err != nil {
		t.Fatalf("WhoAmI 失败: %v", err)
	}
	return resp.Msg.GetSubjectId()
}
