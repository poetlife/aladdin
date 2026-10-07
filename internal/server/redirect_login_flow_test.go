package server

import (
	"context"
	"errors"
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

// 重定向型登录渠道两个浏览器直连端点的行为用例。
//
// 它们**走真实的 mux 与认证中间件**：这条路径的价值有一半在于"它是一个不被
// rbac 方法注解体系挡住的非 RPC 入口"，只测 handler 会把这一半漏掉。
//
// **每个用例对两条渠道各跑一遍。** 这就是"渠道之间共用同一段流程"这句话的
// 可执行形式：任何"只对 GitHub 成立"的假设都会在这里暴露。渠道之间的差异
// （宿主、客户端标识、授权范围、端点路径、cookie 名、失败标记）集中在
// redirectCases 里。
//
// **不联网**：校验器是一个可控替身。

const testPublicBaseURL = "https://aladdin.example.com"

// redirectCase 是一条渠道在一次用例里的全部差异。
type redirectCase struct {
	channel redirectChannel
	// authorizeHost 是起点应当把浏览器交给哪个宿主。
	authorizeHost string
	// clientID 是这条渠道在注册表里登记的公开客户端标识。
	clientID string
	// wantScope 是授权地址上应当出现的授权范围；空表示这条渠道不申请任何范围。
	wantScope string
}

// redirectCases 返回全部渠道。逐条跑，不挑渠道。
func redirectCases() []redirectCase {
	return []redirectCase{
		{
			channel:       newGoogleRedirectChannel(),
			authorizeHost: "accounts.google.com",
			clientID:      "google-client.example",
			// openid 是"换回可自证身份的令牌"的必要条件，email 是可读标识；
			// 不申请 profile（见 docs/design/identity/google-login.md）。
			wantScope: "openid email",
		},
		{
			channel:       newGithubRedirectChannel(),
			authorizeHost: "github.com",
			clientID:      "Iv1.example",
			// GitHub 不申请任何范围：取回身份键与登录名不需要额外授权。
			wantScope: "",
		},
	}
}

// enabled 把这条渠道与一份校验器装配成注册表里的渠道。
func (c redirectCase) enabled(verifier identity.TokenVerifier) identity.Channel {
	return identity.Channel{Source: c.channel.source, ClientID: c.clientID, Verifier: verifier}
}

// redirectFlowHarness 装配一条只包含两个浏览器直连端点的最小链路。
func redirectFlowHarness(t *testing.T, publicBaseURL string, channel redirectChannel, channels ...identity.Channel) (*httptest.Server, identityFixture) {
	t.Helper()

	fixture := newIdentityFixture(t, channels...)
	cfg := config.DefaultServer()
	cfg.PublicBaseURL = publicBaseURL
	flow := NewRedirectLoginFlow(fixture.service, cfg, zap.NewNop(), channel)

	mux := http.NewServeMux()
	mux.HandleFunc(channel.startPath, flow.Start)
	mux.HandleFunc(channel.callbackPath, flow.Callback)

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

// noRedirectClient 不跟随跳转：跳转本身就是被测对象。
func noRedirectClient() *http.Client {
	return &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// startFlow 走一次起点端点。purpose 是导航上的**原始线值**：
// 空表示登录，flowPurposeBind 的线值表示绑定。
func startFlow(t *testing.T, c redirectCase, baseURL, purpose string) (string, *http.Cookie) {
	t.Helper()

	target := baseURL + c.channel.startPath
	if purpose != "" {
		target += "?purpose=" + url.QueryEscape(purpose)
	}
	resp, err := noRedirectClient().Get(target)
	if err != nil {
		t.Fatalf("访问起点端点失败: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusFound {
		t.Fatalf("起点端点状态码 = %d，期望 302", resp.StatusCode)
	}
	for _, cookie := range resp.Cookies() {
		if cookie.Name == c.channel.stateCookie() {
			return resp.Header.Get("Location"), cookie
		}
	}
	t.Fatal("起点端点没有写下导航凭据 cookie")
	return "", nil
}

// 起点端点把浏览器交给渠道，并把一次性凭据放进一个不可被脚本读取的 cookie。
//
// 它也顺带固定了"这条路径不被认证中间件挡住"——被挡住的话会得到一个
// 来自方法注解体系的拒绝，而不是 302。
func TestRedirectStartSendsBrowserToChannel(t *testing.T) {
	for _, c := range redirectCases() {
		t.Run(c.channel.source, func(t *testing.T) {
			srv, _ := redirectFlowHarness(t, testPublicBaseURL, c.channel, c.enabled(verifierFor("1001")))

			location, cookie := startFlow(t, c, srv.URL, "")

			u, err := url.Parse(location)
			if err != nil {
				t.Fatalf("跳转目标无法解析: %v", err)
			}
			if u.Host != c.authorizeHost {
				t.Errorf("跳转目标是 %q，期望 %s", u.Host, c.authorizeHost)
			}
			q := u.Query()
			if q.Get("client_id") != c.clientID {
				t.Errorf("client_id = %q", q.Get("client_id"))
			}
			if q.Get("redirect_uri") != testPublicBaseURL+c.channel.callbackPath {
				t.Errorf("redirect_uri = %q，期望由对外源派生", q.Get("redirect_uri"))
			}
			if q.Get("state") == "" || q.Get("state") != cookie.Value {
				t.Errorf("地址里的 state 与 cookie 不一致：%q vs %q", q.Get("state"), cookie.Value)
			}
			// 授权范围按渠道取值：多要一项就多一次用户被授权页吓退的机会，
			// 少要一项则换不回可确定身份的东西。
			if got := strings.Join(strings.Fields(q.Get("scope")), " "); got != c.wantScope {
				t.Errorf("scope = %q，期望 %q", got, c.wantScope)
			}

			if !cookie.HttpOnly {
				t.Error("凭据 cookie 必须不可被脚本读取，否则一次脚本注入就能读走它")
			}
			if cookie.SameSite != http.SameSiteLaxMode {
				// Strict 会让 cookie 恰好在从渠道跳回来的那次导航上不发送，
				// 于是每一次登录都会失败。这条不是风格偏好。
				t.Errorf("SameSite = %v，期望 Lax", cookie.SameSite)
			}
			if want := c.channel.stateCookiePath(); cookie.Path != want {
				t.Errorf("cookie 作用路径 = %q，期望 %q", cookie.Path, want)
			}
			if !cookie.Secure {
				t.Error("对外源是 https 时 cookie 应当要求加密传输")
			}
		})
	}
}

// cookie 是否要求加密传输由**对外源的协议**决定，而不是写死：写死 true 会让
// 本地回环上的 http 开发无法登录。
func TestRedirectStartCookieNotSecureOnLoopbackHTTP(t *testing.T) {
	for _, c := range redirectCases() {
		t.Run(c.channel.source, func(t *testing.T) {
			srv, _ := redirectFlowHarness(t, "http://localhost:5173", c.channel, c.enabled(verifierFor("1001")))

			_, cookie := startFlow(t, c, srv.URL, "")
			if cookie.Secure {
				t.Error("对外源是本地 http 时 cookie 不该要求加密传输，否则本地无法登录")
			}
		})
	}
}

// 凭据相符时完成登录：跳回前端，会话凭证在 fragment 里。
func TestRedirectCallbackCompletesLogin(t *testing.T) {
	for _, c := range redirectCases() {
		t.Run(c.channel.source, func(t *testing.T) {
			srv, fixture := redirectFlowHarness(t, testPublicBaseURL, c.channel, c.enabled(verifierFor("1001")))

			_, cookie := startFlow(t, c, srv.URL, "")
			location := callback(t, c, srv.URL, cookie, "the-code", cookie.Value)

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
		})
	}
}

// 跳转目标**只由配置构造**：伪造 Host 头不改变它。
//
// 用请求头构造跳转目标等于给攻击者一个把会话凭证送到任意主机的原语。
func TestRedirectStartIgnoresSpoofedHost(t *testing.T) {
	for _, c := range redirectCases() {
		t.Run(c.channel.source, func(t *testing.T) {
			srv, _ := redirectFlowHarness(t, testPublicBaseURL, c.channel, c.enabled(verifierFor("1001")))

			req, err := http.NewRequest(http.MethodGet, srv.URL+c.channel.startPath, nil)
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
		})
	}
}

// 凭据缺失或不符一律**不签发任何会话**。
//
// 若不在这里拦住，攻击者可以把自己账号的授权码塞进受害者的浏览器，让服务端
// 为攻击者的主体签发会话——受害者此后的一切操作都落在攻击者能登录的账号上。
func TestRedirectCallbackRejectsBadState(t *testing.T) {
	for _, c := range redirectCases() {
		t.Run(c.channel.source, func(t *testing.T) {
			srv, fixture := redirectFlowHarness(t, testPublicBaseURL, c.channel, c.enabled(verifierFor("1001")))

			_, cookie := startFlow(t, c, srv.URL, "")

			cases := map[string]struct {
				state string
				jar   bool
			}{
				"凭据不符":     {"a-different-state", true},
				"缺凭据参数":    {"", true},
				"缺 cookie": {"whatever", false},
			}
			for name, bad := range cases {
				t.Run(name, func(t *testing.T) {
					location := callback(t, c, srv.URL, cookieFor(bad.jar, cookie), "the-code", bad.state)

					if got := fragmentValue(t, location, frontendErrorFragment); got != c.channel.loginFailed() {
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
		})
	}
}

// 校验不通过（渠道说这份凭证不成立）同样不签发会话。
func TestRedirectCallbackRejectsInvalidCredential(t *testing.T) {
	for _, c := range redirectCases() {
		t.Run(c.channel.source, func(t *testing.T) {
			srv, fixture := redirectFlowHarness(t, testPublicBaseURL, c.channel,
				c.enabled(fakeVerifier{err: identity.ErrInvalidToken}))

			_, cookie := startFlow(t, c, srv.URL, "")
			location := callback(t, c, srv.URL, cookie, "the-code", cookie.Value)

			if fragmentValue(t, location, frontendTokenFragment) != "" {
				t.Error("校验失败不得交付会话凭证")
			}
			if n := fixture.issuedSessions(); n != 0 {
				t.Errorf("签发了 %d 条会话，期望 0 条", n)
			}
		})
	}
}

// 同一份凭据用第二次**不签发会话**。
//
// 这条由服务端自己的记录保证，而不是靠浏览器在回调后删掉 cookie——cookie 由
// 浏览器保管，重放它对我们不是"做不到"的事。
func TestRedirectCallbackRejectsReplayedState(t *testing.T) {
	for _, c := range redirectCases() {
		t.Run(c.channel.source, func(t *testing.T) {
			srv, fixture := redirectFlowHarness(t, testPublicBaseURL, c.channel, c.enabled(verifierFor("1001")))

			_, cookie := startFlow(t, c, srv.URL, "")

			first := callback(t, c, srv.URL, cookie, "the-code", cookie.Value)
			if fragmentValue(t, first, frontendTokenFragment) == "" {
				t.Fatalf("第一次回调没有交付会话凭证: %q", first)
			}

			second := callback(t, c, srv.URL, cookie, "the-code", cookie.Value)
			if fragmentValue(t, second, frontendTokenFragment) != "" {
				t.Error("同一份凭据的第二次回调仍然交付了会话凭证")
			}
			if got := fragmentValue(t, second, frontendErrorFragment); got != c.channel.loginFailed() {
				t.Errorf("第二次回调的跳转目标 = %q，期望带一个固定的失败标记", second)
			}
			if n := fixture.issuedSessions(); n != 1 {
				t.Errorf("签发了 %d 条会话，期望 1 条", n)
			}
		})
	}
}

// cookie 与地址一致、但服务端从没发出过这份凭据时，同样不签发会话。
//
// 这把"凭据是否成立"的判据收回服务端：cookie 的取值可以被别的来源写进来
// （子域、脚本注入），但只有服务端发出过的取值才有对应记录。
func TestRedirectCallbackRejectsUnissuedState(t *testing.T) {
	for _, c := range redirectCases() {
		t.Run(c.channel.source, func(t *testing.T) {
			srv, fixture := redirectFlowHarness(t, testPublicBaseURL, c.channel, c.enabled(verifierFor("1001")))

			forged := &http.Cookie{Name: c.channel.stateCookie(), Value: "forged-state"}
			location := callback(t, c, srv.URL, forged, "the-code", forged.Value)

			if fragmentValue(t, location, frontendTokenFragment) != "" {
				t.Error("服务端没发出过的凭据不得交付会话凭证")
			}
			if n := fixture.issuedSessions(); n != 0 {
				t.Errorf("签发了 %d 条会话，期望 0 条", n)
			}
		})
	}
}

// 协议名大小写不敏感：写成 `HTTPS://` 时 cookie 仍须要求加密传输。
//
// 按原始字符串的前缀比较会把这一种判成"不要求"，与取值校验的结论相反。
func TestRedirectStartCookieSecureForUppercaseScheme(t *testing.T) {
	for _, c := range redirectCases() {
		t.Run(c.channel.source, func(t *testing.T) {
			srv, _ := redirectFlowHarness(t, "HTTPS://aladdin.example.com", c.channel, c.enabled(verifierFor("1001")))

			_, cookie := startFlow(t, c, srv.URL, "")
			if !cookie.Secure {
				t.Error("对外源是 https（只是大小写不同）时 cookie 应当要求加密传输")
			}
		})
	}
}

// 未启用该渠道时两个端点都不存在。
func TestRedirectEndpointsAbsentWhenChannelDisabled(t *testing.T) {
	for _, c := range redirectCases() {
		t.Run(c.channel.source, func(t *testing.T) {
			// 只装**另一条**渠道：本渠道这条路整体缺席。
			other := otherChannel(c)
			srv, _ := redirectFlowHarness(t, testPublicBaseURL, c.channel, other.enabled(verifierFor("1001")))
			client := noRedirectClient()

			for _, path := range []string{c.channel.startPath, c.channel.callbackPath} {
				resp, err := client.Get(srv.URL + path)
				if err != nil {
					t.Fatalf("访问 %s 失败: %v", path, err)
				}
				_ = resp.Body.Close()
				if resp.StatusCode != http.StatusNotFound {
					t.Errorf("%s 状态码 = %d，期望 404", path, resp.StatusCode)
				}
			}
		})
	}
}

// otherChannel 返回清单里**另一条**渠道。
func otherChannel(c redirectCase) redirectCase {
	for _, other := range redirectCases() {
		if other.channel.source != c.channel.source {
			return other
		}
	}
	panic("渠道清单里只有一条渠道")
}

// callbackResult 是一次回调响应里调用方需要的东西；响应体已经在 helper 里读完并关闭。
type callbackResult struct {
	location string
	cookies  []*http.Cookie
}

// callback 走一次回调端点，返回跳转目标。cookie 为 nil 表示不携带凭据 cookie。
func callback(t *testing.T, c redirectCase, baseURL string, cookie *http.Cookie, code, state string) string {
	t.Helper()
	return callbackResponse(t, c, baseURL, cookie, code, state).location
}

// callbackResponse 走一次回调端点，返回跳转目标与响应写下的 cookie。
func callbackResponse(t *testing.T, c redirectCase, baseURL string, cookie *http.Cookie, code, state string) callbackResult {
	t.Helper()

	target, err := url.Parse(baseURL + c.channel.callbackPath)
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
	return callbackResult{location: resp.Header.Get("Location"), cookies: resp.Cookies()}
}

// cookieFor 按需返回 cookie 指针，使"不携带 cookie"这一情形能表达出来。
func cookieFor(present bool, cookie *http.Cookie) *http.Cookie {
	if !present {
		return nil
	}
	return cookie
}

// 绑定起点走完回调后不签发会话，而是把已校验身份记成一份 HttpOnly 待绑定凭据。
func TestRedirectBindCallbackIssuesPendingBinding(t *testing.T) {
	for _, c := range redirectCases() {
		t.Run(c.channel.source, func(t *testing.T) {
			srv, fixture := redirectFlowHarness(t, testPublicBaseURL, c.channel, c.enabled(verifierFor("2001")))

			_, stateCookie := startFlow(t, c, srv.URL, string(flowPurposeBind))
			resp := callbackResponse(t, c, srv.URL, stateCookie, "the-code", stateCookie.Value)
			location := resp.location

			if got := fragmentValue(t, location, frontendBindingFragment); got != c.channel.source {
				t.Fatalf("binding fragment = %q，期望 %q（location=%q）", got, c.channel.source, location)
			}
			if fragmentValue(t, location, frontendTokenFragment) != "" {
				t.Error("绑定回调不得交付会话凭证")
			}
			if n := fixture.issuedSessions(); n != 0 {
				t.Errorf("绑定回调签发了 %d 条会话，期望 0", n)
			}
			if _, err := fixture.identityStore.Lookup(context.Background(), c.channel.source, "2001"); !errors.Is(err, identity.ErrIdentityNotFound) {
				t.Errorf("绑定回调登记了身份（err=%v），期望未登记", err)
			}

			var pending *http.Cookie
			for _, cookie := range resp.cookies {
				if cookie.Name == pendingBindingCookie {
					pending = cookie
				}
			}
			if pending == nil {
				t.Fatal("绑定回调没有写下待绑定凭据 cookie")
			}
			if pending.Value == "" {
				t.Error("待绑定凭据 cookie 是空值")
			}
			if !pending.HttpOnly {
				t.Error("待绑定凭据 cookie 必须不可被脚本读取")
			}
			if pending.Path != pendingBindingCookiePath {
				t.Errorf("待绑定凭据 cookie 作用路径 = %q，期望 %q", pending.Path, pendingBindingCookiePath)
			}
			if pending.SameSite != http.SameSiteStrictMode {
				t.Errorf("SameSite = %v，期望 Strict（兑换是同源 RPC，不需要 Lax）", pending.SameSite)
			}
			if !pending.Secure {
				t.Error("对外源是 https 时待绑定凭据 cookie 应当要求加密传输")
			}
		})
	}
}

// 绑定路径的失败也带固定标记，但不含任何内部细节，也绝不签发会话。
func TestRedirectBindCallbackFailureUsesBindMarker(t *testing.T) {
	for _, c := range redirectCases() {
		t.Run(c.channel.source, func(t *testing.T) {
			srv, fixture := redirectFlowHarness(t, testPublicBaseURL, c.channel,
				c.enabled(fakeVerifier{err: identity.ErrInvalidToken}))

			_, stateCookie := startFlow(t, c, srv.URL, string(flowPurposeBind))
			location := callback(t, c, srv.URL, stateCookie, "the-code", stateCookie.Value)

			if got := fragmentValue(t, location, frontendErrorFragment); got != c.channel.bindFailed() {
				t.Fatalf("error fragment = %q，期望 %q（location=%q）", got, c.channel.bindFailed(), location)
			}
			if fragmentValue(t, location, frontendTokenFragment) != "" {
				t.Error("绑定失败不得交付会话凭证")
			}
			if n := fixture.issuedSessions(); n != 0 {
				t.Errorf("绑定失败签发了 %d 条会话，期望 0", n)
			}
		})
	}
}

// 凭据不符时也要按用途选标记：用户明明登录着、正在做绑定，不该被告知
// "登录未完成"。用途取自服务端记下的那条记录，因此这里能给出正确的措辞。
func TestRedirectBindCallbackBadStateUsesBindMarker(t *testing.T) {
	for _, c := range redirectCases() {
		t.Run(c.channel.source, func(t *testing.T) {
			srv, fixture := redirectFlowHarness(t, testPublicBaseURL, c.channel, c.enabled(verifierFor("2001")))

			_, stateCookie := startFlow(t, c, srv.URL, string(flowPurposeBind))
			location := callback(t, c, srv.URL, stateCookie, "the-code", "not-the-issued-state")

			if got := fragmentValue(t, location, frontendErrorFragment); got != c.channel.bindFailed() {
				t.Fatalf("error fragment = %q，期望 %q（location=%q）", got, c.channel.bindFailed(), location)
			}
			if fragmentValue(t, location, frontendTokenFragment) != "" {
				t.Error("绑定失败不得交付会话凭证")
			}
			if n := fixture.issuedSessions(); n != 0 {
				t.Errorf("凭据不符却签发了 %d 条会话，期望 0", n)
			}
		})
	}
}

// 每条渠道的派生取值（cookie 名、作用路径、失败标记）都必须与它自己的端点
// 对得上：写错一处会表现为"登录莫名其妙失败"，而不是任何一条报错。
func TestRedirectChannelDerivedValues(t *testing.T) {
	seen := map[string]string{}
	for _, c := range redirectChannels() {
		if !strings.HasPrefix(c.startPath, "/auth/"+c.source+"/") {
			t.Errorf("渠道 %s 的起点路径 = %q，期望落在 /auth/%s/ 下", c.source, c.startPath, c.source)
		}
		if c.stateCookie() != "aladdin_"+c.source+"_state" {
			t.Errorf("渠道 %s 的 cookie 名 = %q", c.source, c.stateCookie())
		}
		if got, want := c.stateCookiePath(), "/auth/"+c.source; got != want {
			t.Errorf("渠道 %s 的 cookie 作用路径 = %q，期望 %q", c.source, got, want)
		}
		if got, want := c.loginFailed(), c.source+"_login_failed"; got != want {
			t.Errorf("渠道 %s 的登录失败标记 = %q，期望 %q", c.source, got, want)
		}
		if got, want := c.bindFailed(), c.source+"_bind_failed"; got != want {
			t.Errorf("渠道 %s 的绑定失败标记 = %q，期望 %q", c.source, got, want)
		}
		if prev, ok := seen[c.stateCookie()]; ok {
			t.Errorf("渠道 %s 与 %s 共用了 cookie 名 %q", c.source, prev, c.stateCookie())
		}
		seen[c.stateCookie()] = c.source
	}
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
