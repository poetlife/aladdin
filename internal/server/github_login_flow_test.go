package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"go.uber.org/zap"

	"github.com/poetlife/aladdin/internal/config"
	"github.com/poetlife/aladdin/internal/identity"
	"github.com/poetlife/aladdin/internal/server/interceptor"
)

// GitHub 两个浏览器直连端点的行为用例。
//
// 它们**走真实的 mux 与认证中间件**：这条路径的价值有一半在于"它是一个不被
// rbac 方法注解体系挡住的非 RPC 入口"，只测 handler 会把这一半漏掉。
//
// **不联网**：校验器是一个可控替身。

const testPublicBaseURL = "https://aladdin.example.com"

// githubFlowHarness 装配一条只包含两个浏览器直连端点的最小链路。
func githubFlowHarness(t *testing.T, publicBaseURL string, channels ...identity.Channel) (*httptest.Server, identityFixture) {
	t.Helper()

	fixture := newIdentityFixture(t, channels...)
	cfg := config.DefaultServer()
	cfg.PublicBaseURL = publicBaseURL
	flow := NewGithubLoginFlow(fixture.service, cfg, zap.NewNop())

	mux := http.NewServeMux()
	mux.HandleFunc(identity.GithubStartPath, flow.Start)
	mux.HandleFunc(identity.GithubCallbackPath, flow.Callback)

	authn := &authMiddleware{
		authn: &credentialAuthenticator{
			machine:  interceptor.NewTokenAuthenticator(),
			sessions: fixture.sessions,
		},
		errorWriter: connect.NewErrorWriter(),
		logger:      zap.NewNop(),
	}

	srv := httptest.NewServer(authn.wrap(mux))
	t.Cleanup(srv.Close)
	return srv, fixture
}

// githubChannel 造一条已启用的 GitHub 渠道。
func githubChannel(verifier identity.TokenVerifier) identity.Channel {
	return identity.Channel{Source: identity.SourceGithub, ClientID: "Iv1.example", Verifier: verifier}
}

// noRedirectClient 不跟随跳转：跳转本身就是被测对象。
func noRedirectClient() *http.Client {
	return &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// startGithubLogin 走一次起点端点，返回跳转目标与写下的 cookie。
func startGithubLogin(t *testing.T, baseURL string) (string, *http.Cookie) {
	t.Helper()

	resp, err := noRedirectClient().Get(baseURL + identity.GithubStartPath)
	if err != nil {
		t.Fatalf("访问起点端点失败: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusFound {
		t.Fatalf("起点端点状态码 = %d，期望 302", resp.StatusCode)
	}
	for _, cookie := range resp.Cookies() {
		if cookie.Name == githubStateCookie {
			return resp.Header.Get("Location"), cookie
		}
	}
	t.Fatal("起点端点没有写下登录凭据 cookie")
	return "", nil
}

// 起点端点把浏览器交给 GitHub，并把一次性凭据放进一个不可被脚本读取的 cookie。
//
// 它也顺带固定了"这条路径不被认证中间件挡住"——被挡住的话会得到一个
// 来自方法注解体系的拒绝，而不是 302。
func TestGithubStartRedirectsToAuthorize(t *testing.T) {
	srv, _ := githubFlowHarness(t, testPublicBaseURL, githubChannel(verifierFor("1001")))

	location, cookie := startGithubLogin(t, srv.URL)

	u, err := url.Parse(location)
	if err != nil {
		t.Fatalf("跳转目标无法解析: %v", err)
	}
	if u.Host != "github.com" {
		t.Errorf("跳转目标是 %q，期望 github.com", u.Host)
	}
	q := u.Query()
	if q.Get("client_id") != "Iv1.example" {
		t.Errorf("client_id = %q", q.Get("client_id"))
	}
	if q.Get("redirect_uri") != testPublicBaseURL+identity.GithubCallbackPath {
		t.Errorf("redirect_uri = %q，期望由对外源派生", q.Get("redirect_uri"))
	}
	if q.Get("state") == "" || q.Get("state") != cookie.Value {
		t.Errorf("地址里的 state 与 cookie 不一致：%q vs %q", q.Get("state"), cookie.Value)
	}
	if _, hasScope := q["scope"]; hasScope {
		t.Errorf("授权地址带了 scope：%q", location)
	}

	if !cookie.HttpOnly {
		t.Error("凭据 cookie 必须不可被脚本读取，否则一次脚本注入就能读走它")
	}
	if cookie.SameSite != http.SameSiteLaxMode {
		// Strict 会让 cookie 恰好在从 github.com 跳回来的那次导航上不发送，
		// 于是每一次登录都会失败。这条不是风格偏好。
		t.Errorf("SameSite = %v，期望 Lax", cookie.SameSite)
	}
	if cookie.Path != "/auth/github" {
		t.Errorf("cookie 作用路径 = %q，期望限于该渠道的路径", cookie.Path)
	}
	if !cookie.Secure {
		t.Error("对外源是 https 时 cookie 应当要求加密传输")
	}
}

// cookie 是否要求加密传输由**对外源的协议**决定，而不是写死：写死 true 会让
// 本地回环上的 http 开发无法登录。
func TestGithubStartCookieNotSecureOnLoopbackHTTP(t *testing.T) {
	srv, _ := githubFlowHarness(t, "http://localhost:5173", githubChannel(verifierFor("1001")))

	_, cookie := startGithubLogin(t, srv.URL)
	if cookie.Secure {
		t.Error("对外源是本地 http 时 cookie 不该要求加密传输，否则本地无法登录")
	}
}

// 凭据相符时完成登录：跳回前端，会话凭证在 fragment 里。
func TestGithubCallbackCompletesLogin(t *testing.T) {
	verifier := verifierFor("1001")
	srv, fixture := githubFlowHarness(t, testPublicBaseURL, githubChannel(verifier))

	_, cookie := startGithubLogin(t, srv.URL)
	location := callback(t, srv.URL, cookie, "the-code", cookie.Value)

	if !strings.HasPrefix(location, testPublicBaseURL+FrontendCallbackPath+"#") {
		t.Fatalf("跳转目标 = %q，期望回到前端的回调页", location)
	}
	token := fragmentValue(t, location, frontendTokenFragment)
	if token == "" {
		t.Fatal("跳转目标里没有会话凭证")
	}
	if _, err := fixture.sessions.Verify(context.Background(), token); err != nil {
		t.Errorf("交付的凭证不可用: %v", err)
	}
	if n := fixture.issuedSessions(); n != 1 {
		t.Errorf("签发了 %d 条会话，期望 1 条", n)
	}
}

// 跳转目标**只由配置构造**：伪造 Host 头不改变它。
//
// 用请求头构造跳转目标等于给攻击者一个把会话凭证送到任意主机的原语。
func TestGithubStartIgnoresSpoofedHost(t *testing.T) {
	srv, _ := githubFlowHarness(t, testPublicBaseURL, githubChannel(verifierFor("1001")))

	req, err := http.NewRequest(http.MethodGet, srv.URL+identity.GithubStartPath, nil)
	if err != nil {
		t.Fatalf("构造请求失败: %v", err)
	}
	req.Host = "evil.example.com"
	req.Header.Set("X-Forwarded-Host", "evil.example.com")

	resp, err := noRedirectClient().Do(req)
	if err != nil {
		t.Fatalf("访问起点端点失败: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	location := resp.Header.Get("Location")
	if strings.Contains(location, "evil.example.com") {
		t.Errorf("跳转目标被请求头影响了：%q", location)
	}
}

// 凭据缺失或不符一律**不签发任何会话**。
//
// 若不在这里拦住，攻击者可以把自己账号的授权码塞进受害者的浏览器，让服务端
// 为攻击者的主体签发会话——受害者此后的一切操作都落在攻击者能登录的账号上。
func TestGithubCallbackRejectsBadState(t *testing.T) {
	srv, fixture := githubFlowHarness(t, testPublicBaseURL, githubChannel(verifierFor("1001")))

	_, cookie := startGithubLogin(t, srv.URL)

	cases := map[string]struct {
		state string
		jar   bool
	}{
		"凭据不符":     {"a-different-state", true},
		"缺凭据参数":    {"", true},
		"缺 cookie": {"whatever", false},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			location := callback(t, srv.URL, cookieFor(c.jar, cookie), "the-code", c.state)

			if got := fragmentValue(t, location, frontendErrorFragment); got != githubLoginFailed {
				t.Fatalf("跳转目标 = %q，期望带一个固定的失败标记", location)
			}
			if fragmentValue(t, location, frontendTokenFragment) != "" {
				t.Error("失败的登录不得交付任何会话凭证")
			}
			// 日志里没有凭证不代表库里没有：直接确认没有签发。
			if n := fixture.issuedSessions(); n != 0 {
				t.Errorf("签发了 %d 条会话，期望 0 条", n)
			}
		})
	}
}

// 校验不通过（渠道说这份凭证不成立）同样不签发会话。
func TestGithubCallbackRejectsInvalidCredential(t *testing.T) {
	srv, fixture := githubFlowHarness(t, testPublicBaseURL,
		githubChannel(fakeVerifier{err: identity.ErrInvalidToken}))

	_, cookie := startGithubLogin(t, srv.URL)
	location := callback(t, srv.URL, cookie, "the-code", cookie.Value)

	if fragmentValue(t, location, frontendTokenFragment) != "" {
		t.Error("校验失败不得交付会话凭证")
	}
	if n := fixture.issuedSessions(); n != 0 {
		t.Errorf("签发了 %d 条会话，期望 0 条", n)
	}
}

// 未启用该渠道时两个端点都不存在。
func TestGithubEndpointsAbsentWhenChannelDisabled(t *testing.T) {
	// 只装 Google：GitHub 这条路整体缺席。
	srv, _ := githubFlowHarness(t, testPublicBaseURL, googleChannel(verifierFor("1001")))
	client := noRedirectClient()

	for _, path := range []string{identity.GithubStartPath, identity.GithubCallbackPath} {
		resp, err := client.Get(srv.URL + path)
		if err != nil {
			t.Fatalf("访问 %s 失败: %v", path, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s 状态码 = %d，期望 404", path, resp.StatusCode)
		}
	}
}

// callback 走一次回调端点，返回跳转目标。cookie 为 nil 表示不携带凭据 cookie。
func callback(t *testing.T, baseURL string, cookie *http.Cookie, code, state string) string {
	t.Helper()

	target, err := url.Parse(baseURL + identity.GithubCallbackPath)
	if err != nil {
		t.Fatalf("构造回调地址失败: %v", err)
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
		t.Fatalf("构造请求失败: %v", err)
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}

	resp, err := noRedirectClient().Do(req)
	if err != nil {
		t.Fatalf("访问回调端点失败: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusFound {
		t.Fatalf("回调端点状态码 = %d，期望 302", resp.StatusCode)
	}
	return resp.Header.Get("Location")
}

// cookieFor 按需返回 cookie 指针，使"不携带 cookie"这一情形能表达出来。
func cookieFor(present bool, cookie *http.Cookie) *http.Cookie {
	if !present {
		return nil
	}
	return cookie
}

// fragmentValue 取出跳转目标里某个 fragment 的取值。
func fragmentValue(t *testing.T, location, key string) string {
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
