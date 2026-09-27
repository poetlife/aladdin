package identity

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

// GitHub 校验器的行为用例。
//
// **不联网**：假 GitHub 是一个 httptest 服务，校验器指向它。覆盖的是"授权码
// 怎么变成身份、什么情况下拒绝"——真实 GitHub 的响应形状由上表逐条固定。

const (
	testGithubClientID     = "Iv1.0123456789abcdef"
	testGithubClientSecret = "the-client-secret"
	testGithubRedirectURI  = "https://aladdin.example.com/auth/github/callback"
	testGithubCode         = "the-authorization-code"
)

// fakeGithub 是一个可控的 GitHub：换令牌与取用户信息两个端点。
type fakeGithub struct {
	server *httptest.Server

	mu            sync.Mutex
	tokenStatus   int
	tokenBody     string
	userStatus    int
	userBody      string
	lastTokenForm url.Values
	lastUserHead  http.Header
}

func newFakeGithub(t *testing.T) *fakeGithub {
	t.Helper()
	f := &fakeGithub{
		tokenStatus: http.StatusOK,
		tokenBody:   `{"access_token":"gho_token"}`,
		userStatus:  http.StatusOK,
		userBody:    `{"id":12345,"login":"octocat"}`,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/login/oauth/access_token", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		f.lastTokenForm = r.PostForm
		status, body := f.tokenStatus, f.tokenBody
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	})
	mux.HandleFunc("/user", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.lastUserHead = r.Header.Clone()
		status, body := f.userStatus, f.userBody
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	})

	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeGithub) verifier() *GithubVerifier {
	return newGithubVerifier(
		testGithubClientID, testGithubClientSecret, testGithubRedirectURI,
		f.server.URL+"/login/oauth/access_token", f.server.URL+"/user",
		f.server.Client(),
	)
}

func (f *fakeGithub) tokenForm() url.Values {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastTokenForm
}

func (f *fakeGithub) userHeader() http.Header {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastUserHead
}

// 成功：授权码换出访问令牌，再由它取回不可变标识与展示名。
func TestGithubVerifierResolvesIdentity(t *testing.T) {
	fake := newFakeGithub(t)

	got, err := fake.verifier().Verify(context.Background(), testGithubCode)
	if err != nil {
		t.Fatalf("校验失败: %v", err)
	}
	if got.ExternalID != "12345" {
		t.Errorf("身份标识 = %q，期望用户编号的十进制形式", got.ExternalID)
	}
	if got.Display != "octocat" {
		t.Errorf("展示信息 = %q，期望登录名", got.Display)
	}
}

// 换令牌的请求必须带齐四项，且回落地址与授权时给出的逐字一致。
func TestGithubVerifierExchangesWithMatchingRedirectURI(t *testing.T) {
	fake := newFakeGithub(t)
	if _, err := fake.verifier().Verify(context.Background(), testGithubCode); err != nil {
		t.Fatalf("校验失败: %v", err)
	}

	form := fake.tokenForm()
	for key, want := range map[string]string{
		"client_id":     testGithubClientID,
		"client_secret": testGithubClientSecret,
		"code":          testGithubCode,
		"redirect_uri":  testGithubRedirectURI,
	} {
		if got := form.Get(key); got != want {
			t.Errorf("换令牌的 %s = %q，期望 %q", key, got, want)
		}
	}
}

// 取用户信息必须带 User-Agent：GitHub 拒绝没有它的请求。
func TestGithubVerifierSendsRequiredHeaders(t *testing.T) {
	fake := newFakeGithub(t)
	if _, err := fake.verifier().Verify(context.Background(), testGithubCode); err != nil {
		t.Fatalf("校验失败: %v", err)
	}

	header := fake.userHeader()
	if header.Get("User-Agent") == "" {
		t.Error("取用户信息时没有带 User-Agent，GitHub 会拒绝")
	}
	if got := header.Get("Authorization"); got != "Bearer gho_token" {
		t.Errorf("Authorization = %q", got)
	}
}

// 大编号不丢精度：GitHub 的编号早已超出 int32。
func TestGithubVerifierKeepsLargeID(t *testing.T) {
	fake := newFakeGithub(t)
	fake.userBody = `{"id":9007199254740993,"login":"big"}`

	got, err := fake.verifier().Verify(context.Background(), testGithubCode)
	if err != nil {
		t.Fatalf("校验失败: %v", err)
	}
	if got.ExternalID != "9007199254740993" {
		t.Errorf("身份标识 = %q，期望原值（不得被截断）", got.ExternalID)
	}
}

// **失败时 GitHub 仍返回 200**：错误写在响应体里，必须解析出来。
//
// 只看状态码会把每一次失败的登录都当成成功，然后在下一步以一次空令牌的请求
// 失败告终——其表象与真正的原因（授权码已用过、已过期）相距很远。
func TestGithubVerifierRejectsErrorBodyDespiteOKStatus(t *testing.T) {
	fake := newFakeGithub(t)
	fake.tokenBody = `{"error":"bad_verification_code","error_description":"已经用过"}`

	_, err := fake.verifier().Verify(context.Background(), testGithubCode)
	if !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("err = %v，期望 ErrInvalidToken", err)
	}
}

func TestGithubVerifierRejectsMissingAccessToken(t *testing.T) {
	fake := newFakeGithub(t)
	fake.tokenBody = `{}`

	if _, err := fake.verifier().Verify(context.Background(), testGithubCode); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("err = %v，期望 ErrInvalidToken", err)
	}
}

// 缺不可变标识即拒绝，**不得退化成用登录名顶上**。
func TestGithubVerifierRejectsMissingID(t *testing.T) {
	for _, body := range []string{
		`{"login":"octocat"}`,
		`{"id":0,"login":"octocat"}`,
	} {
		fake := newFakeGithub(t)
		fake.userBody = body

		_, err := fake.verifier().Verify(context.Background(), testGithubCode)
		if !errors.Is(err, ErrInvalidToken) {
			t.Errorf("用户信息 %s 得到 err = %v，期望 ErrInvalidToken", body, err)
		}
	}
}

// 用户接口明确拒绝令牌是"未认证"，不是"服务不可用"。
func TestGithubVerifierUserUnauthorizedIsInvalidToken(t *testing.T) {
	fake := newFakeGithub(t)
	fake.userStatus = http.StatusUnauthorized
	fake.userBody = `{"message":"Bad credentials"}`

	if _, err := fake.verifier().Verify(context.Background(), testGithubCode); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("err = %v，期望 ErrInvalidToken", err)
	}
}

// 渠道侧的故障是"服务不可用"，不是"凭证无效"：混为一谈会让一次 GitHub 故障
// 表现成"所有人的凭证都失效了"。
func TestGithubVerifierProviderFailuresAreUnavailable(t *testing.T) {
	cases := map[string]func(*fakeGithub){
		"换令牌 5xx":  func(f *fakeGithub) { f.tokenStatus = http.StatusBadGateway },
		"换令牌坏响应体":  func(f *fakeGithub) { f.tokenBody = `not json` },
		"用户接口 5xx": func(f *fakeGithub) { f.userStatus = http.StatusInternalServerError },
		"用户接口坏响应体": func(f *fakeGithub) { f.userBody = `not json` },
	}
	for name, breakIt := range cases {
		t.Run(name, func(t *testing.T) {
			fake := newFakeGithub(t)
			breakIt(fake)

			if _, err := fake.verifier().Verify(context.Background(), testGithubCode); !errors.Is(err, ErrProviderUnavailable) {
				t.Fatalf("err = %v，期望 ErrProviderUnavailable", err)
			}
		})
	}
}

// 够不着 GitHub 也是"服务不可用"。
func TestGithubVerifierUnreachableIsUnavailable(t *testing.T) {
	fake := newFakeGithub(t)
	fake.server.Close()

	if _, err := fake.verifier().Verify(context.Background(), testGithubCode); !errors.Is(err, ErrProviderUnavailable) {
		t.Fatalf("err = %v，期望 ErrProviderUnavailable", err)
	}
}

func TestGithubVerifierRejectsEmptyCredential(t *testing.T) {
	fake := newFakeGithub(t)

	if _, err := fake.verifier().Verify(context.Background(), ""); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("err = %v，期望 ErrInvalidToken", err)
	}
}

// **密钥与访问令牌绝不进入错误信息**：错误信息会进日志。
func TestGithubVerifierErrorsCarryNoSecrets(t *testing.T) {
	cases := map[string]func(*fakeGithub){
		"换取被拒":  func(f *fakeGithub) { f.tokenBody = `{"error":"bad_verification_code"}` },
		"渠道不可用": func(f *fakeGithub) { f.tokenStatus = http.StatusBadGateway },
	}
	for name, breakIt := range cases {
		t.Run(name, func(t *testing.T) {
			fake := newFakeGithub(t)
			breakIt(fake)

			_, err := fake.verifier().Verify(context.Background(), testGithubCode)
			if err == nil {
				t.Fatal("期望失败")
			}
			for _, secret := range []string{testGithubClientSecret, "gho_token", testGithubCode} {
				if strings.Contains(err.Error(), secret) {
					t.Errorf("错误信息 %q 含 %q", err, secret)
				}
			}
		})
	}
}

// 三项缺任一项都视为未启用：只有客户端标识而没有密钥的校验器一次也换不到
// 令牌，它的"已启用"是假的。
func TestNewGithubVerifierDisabledWhenIncomplete(t *testing.T) {
	cases := map[string][3]string{
		"全空":    {"", "", ""},
		"缺密钥":   {testGithubClientID, "", testGithubRedirectURI},
		"缺标识":   {"", testGithubClientSecret, testGithubRedirectURI},
		"缺回调地址": {testGithubClientID, testGithubClientSecret, ""},
	}
	for name, args := range cases {
		if v := NewGithubVerifier(args[0], args[1], args[2]); v != nil {
			t.Errorf("%s：期望接口层真正的 nil，得到 %#v", name, v)
		}
	}
	if v := NewGithubVerifier(testGithubClientID, testGithubClientSecret, testGithubRedirectURI); v == nil {
		t.Error("三项齐全时期望返回一个可用的校验器")
	}
}

// 授权地址必须带客户端标识、回调地址与凭据，且**不带任何 scope**：
// 取回不可变标识与登录名不需要任何授权范围。
func TestGithubAuthorizeURL(t *testing.T) {
	raw := GithubAuthorizeURL(testGithubClientID, testGithubRedirectURI, "the-state")

	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("授权地址无法解析: %v", err)
	}
	if !strings.HasPrefix(raw, githubAuthorizeEndpoint) {
		t.Errorf("授权地址 = %q，期望指向 %s", raw, githubAuthorizeEndpoint)
	}
	q := u.Query()
	for key, want := range map[string]string{
		"client_id":    testGithubClientID,
		"redirect_uri": testGithubRedirectURI,
		"state":        "the-state",
	} {
		if got := q.Get(key); got != want {
			t.Errorf("授权地址的 %s = %q，期望 %q", key, got, want)
		}
	}
	if _, hasScope := q["scope"]; hasScope {
		t.Errorf("授权地址带了 scope：%q。取身份不需要任何授权范围", raw)
	}
}
