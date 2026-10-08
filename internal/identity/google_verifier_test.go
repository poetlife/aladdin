package identity

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

// 这条路径的凭证是**授权码**：校验过程是先换令牌、再校验换回来的身份令牌。
// 两层都要覆盖——换取的每一类回答怎么归类，以及身份令牌的每一条声明都必须
// 生效（缺一条的后果不对称：多一条只会多一种把合法用户挡在门外的失败方式，
// 少一条则可能是"任何 Google 应用的令牌都能登录本服务"）。
//
// **整套用例不联网**：假的提供方跑在 httptest 上，签名密钥由用例自己生成，
// 令牌端点与身份提供方的地址都由用例指过去。

const (
	testClientID     = "1234567890.apps.googleusercontent.com"
	testClientSecret = "the-client-secret"
	testRedirectURI  = "https://aladdin.example.com/auth/google/callback"
	testCode         = "the-authorization-code"
)

// fakeGoogle 是一个本地的身份提供方：它公布发现文档与公钥、签发令牌，
// 并按用例的要求回答令牌端点。
type fakeGoogle struct {
	server *httptest.Server
	key    *rsa.PrivateKey
	keyID  string

	mu sync.Mutex
	// reply 是令牌端点当前给出的回答。
	reply tokenReply
	// lastForm 是令牌端点最后一次收到的表单，用于断言换取时给出的取值。
	lastForm url.Values
}

// tokenReply 是一次令牌端点的回答：状态码与回答内容。
//
// `raw` 非空时原样写出，用来模拟"读得出来、但解析不出东西"。
type tokenReply struct {
	status int
	raw    string
	body   map[string]any
}

func newFakeGoogle(t *testing.T) *fakeGoogle {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("生成测试密钥失败: %v", err)
	}
	fake := &fakeGoogle{key: key, keyID: "test-key"}

	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, map[string]any{
			"issuer":   fake.server.URL,
			"jwks_uri": fake.server.URL + "/keys",
		})
	})
	mux.HandleFunc("/keys", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{
			Key:       key.Public(),
			KeyID:     fake.keyID,
			Algorithm: string(jose.RS256),
			Use:       "sig",
		}}})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("解析表单失败: %v", err)
		}
		fake.mu.Lock()
		fake.lastForm = r.PostForm
		answer := fake.reply
		fake.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(answer.status)
		if answer.raw != "" {
			if _, err := w.Write([]byte(answer.raw)); err != nil {
				t.Errorf("写出响应失败: %v", err)
			}
			return
		}
		if err := json.NewEncoder(w).Encode(answer.body); err != nil {
			t.Errorf("写出响应失败: %v", err)
		}
	})
	fake.server = httptest.NewServer(mux)
	t.Cleanup(fake.server.Close)
	// 默认回答一份合法的身份令牌：用例只想改一项时不必把整套回答写一遍。
	// 它在服务起来之后才设定——签发要用服务地址作为签发方。
	fake.replySignedToken(t, nil)
	return fake
}

// verifier 用这个假提供方构造一个校验器：发现、公钥与令牌端点都指过去。
func (f *fakeGoogle) verifier() *GoogleVerifier {
	return newGoogleVerifier(
		f.server.URL, testClientID, testClientSecret, testRedirectURI,
		f.server.URL+"/token", f.server.Client(),
	)
}

// replySignedToken 让令牌端点回答一份用本提供方密钥签发的身份令牌。
func (f *fakeGoogle) replySignedToken(t *testing.T, claims map[string]any) {
	t.Helper()
	f.reply = tokenReply{status: http.StatusOK, body: map[string]any{
		"id_token":     f.sign(t, claims),
		"access_token": "ya29.not-used",
	}}
}

// replyError 让令牌端点回答一个错误码与状态码。
func (f *fakeGoogle) replyError(status int, errorCode string) {
	f.reply = tokenReply{status: status, body: map[string]any{"error": errorCode}}
}

// form 返回令牌端点最后一次收到的表单。
func (f *fakeGoogle) form() url.Values {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastForm
}

// sign 用这个假提供方的密钥签发一份身份令牌。
//
// claims 由调用方给出，缺省的三项（签发方、受众、有效期）在这里补上——
// 用例只想改一项时不必把整份声明写一遍。
func (f *fakeGoogle) sign(t *testing.T, claims map[string]any) string {
	t.Helper()
	return f.signWith(t, claims, f.key, f.keyID)
}

// signWith 允许换密钥与密钥标识，用于构造"签名对不上"的令牌。
func (f *fakeGoogle) signWith(t *testing.T, claims map[string]any, key *rsa.PrivateKey, keyID string) string {
	t.Helper()

	now := time.Now()
	full := map[string]any{
		"iss": f.server.URL,
		"aud": testClientID,
		"sub": "109876543210987654321",
		"iat": now.Unix(),
		"exp": now.Add(time.Hour).Unix(),
	}
	for k, v := range claims {
		full[k] = v
	}

	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.RS256, Key: jose.JSONWebKey{Key: key, KeyID: keyID}},
		nil,
	)
	if err != nil {
		t.Fatalf("构造签名器失败: %v", err)
	}
	token, err := jwt.Signed(signer).Claims(full).Serialize()
	if err != nil {
		t.Fatalf("签发令牌失败: %v", err)
	}
	return token
}

func writeJSON(t *testing.T, w http.ResponseWriter, body any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(body); err != nil {
		t.Errorf("写出响应失败: %v", err)
	}
}

// 一次正常的换来带回身份标识与展示信息，且**与邮箱无关**。
func TestGoogleVerifierExchangesCodeForIdentity(t *testing.T) {
	fake := newFakeGoogle(t)
	fake.replySignedToken(t, map[string]any{"email": "zhang@example.com"})

	got, err := fake.verifier().Verify(context.Background(), testCode)
	if err != nil {
		t.Fatalf("合法授权码被拒: %v", err)
	}
	if got.ExternalID != "109876543210987654321" {
		t.Errorf("身份标识 = %q", got.ExternalID)
	}
	if got.Display != "zhang@example.com" {
		t.Errorf("展示信息 = %q，期望记录邮箱用于展示", got.Display)
	}
}

// 换取时必须给出与授权时**逐字一致**的回调地址，否则 Google 会拒绝这次换取；
// 客户端标识与密钥同理。
func TestGoogleVerifierExchangesWithMatchingRedirectURI(t *testing.T) {
	fake := newFakeGoogle(t)

	if _, err := fake.verifier().Verify(context.Background(), testCode); err != nil {
		t.Fatalf("合法授权码被拒: %v", err)
	}

	form := fake.form()
	if form.Get("grant_type") != "authorization_code" {
		t.Errorf("grant_type = %q", form.Get("grant_type"))
	}
	if form.Get("code") != testCode {
		t.Errorf("code = %q", form.Get("code"))
	}
	if form.Get("client_id") != testClientID {
		t.Errorf("client_id = %q", form.Get("client_id"))
	}
	if form.Get("client_secret") != testClientSecret {
		t.Errorf("client_secret 没有按配置给出")
	}
	if form.Get("redirect_uri") != testRedirectURI {
		t.Errorf("redirect_uri = %q，期望与授权时一致", form.Get("redirect_uri"))
	}
}

// 受众不匹配必须拒绝，且这一类错误最容易漏。
//
// 漏掉它意味着**任何** Google 应用的令牌都能登录本服务——包括攻击者自己
// 注册的那个应用：签名是真的、签发方是 Google、也没过期，唯独不是签给我们的。
func TestGoogleVerifierRejectsForeignAudience(t *testing.T) {
	fake := newFakeGoogle(t)
	fake.replySignedToken(t, map[string]any{"aud": "someone-else.apps.googleusercontent.com"})

	assertInvalidToken(t, fake.verifier(), testCode)
}

// 过期的令牌不是身份证明。
func TestGoogleVerifierRejectsExpired(t *testing.T) {
	fake := newFakeGoogle(t)
	fake.replySignedToken(t, map[string]any{"exp": time.Now().Add(-time.Minute).Unix()})

	assertInvalidToken(t, fake.verifier(), testCode)
}

// 签发方不符：格式相同、签名也可能是真的，但不是我们的身份来源。
func TestGoogleVerifierRejectsForeignIssuer(t *testing.T) {
	fake := newFakeGoogle(t)
	fake.replySignedToken(t, map[string]any{"iss": "https://accounts.example.com"})

	assertInvalidToken(t, fake.verifier(), testCode)
}

// 签名对不上：换一把密钥签，公钥集合里没有它。
func TestGoogleVerifierRejectsBadSignature(t *testing.T) {
	fake := newFakeGoogle(t)
	other, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("生成密钥失败: %v", err)
	}
	fake.reply = tokenReply{status: http.StatusOK, body: map[string]any{
		"id_token": fake.signWith(t, nil, other, fake.keyID),
	}}

	assertInvalidToken(t, fake.verifier(), testCode)
}

// 缺少不可变标识：它是这个渠道上唯一的身份键，缺了它就无从确定这是谁。
//
// **不得退化成"用邮箱顶上"**——那正是本模块最硬的一条禁止。
func TestGoogleVerifierRejectsMissingSubject(t *testing.T) {
	fake := newFakeGoogle(t)
	fake.replySignedToken(t, map[string]any{"sub": "", "email": "zhang@example.com"})

	assertInvalidToken(t, fake.verifier(), testCode)
}

// 空授权码不必走到网络。
func TestGoogleVerifierRejectsEmptyCredential(t *testing.T) {
	fake := newFakeGoogle(t)

	assertInvalidToken(t, fake.verifier(), "")
}

// 授权码本身不成立（用过、过期、不是签给本应用的）判为**未认证**。
//
// 判据是响应体里的错误码，不是状态码：Google 对两类问题都用 4xx 回答。
func TestGoogleVerifierInvalidGrantIsUnauthenticated(t *testing.T) {
	cases := map[string]tokenReply{
		"400 带错误码": {status: http.StatusBadRequest, body: map[string]any{"error": "invalid_grant"}},
		// 状态码正常、错误写在 body 里：只看状态码会把这次失败当成成功。
		"200 带错误码": {status: http.StatusOK, body: map[string]any{"error": "invalid_grant"}},
	}
	for name, reply := range cases {
		t.Run(name, func(t *testing.T) {
			fake := newFakeGoogle(t)
			fake.reply = reply

			assertInvalidToken(t, fake.verifier(), testCode)
		})
	}
}

// **本服务的配置不成立**时判为渠道不可用，而不是"你的凭证无效"：它一次也换不到
// 令牌，表现为"所有人都登不进来"，报成凭证问题会把排障引向客户端。
func TestGoogleVerifierConfigFailuresAreUnavailable(t *testing.T) {
	for _, errorCode := range []string{"invalid_client", "redirect_uri_mismatch", "invalid_request"} {
		t.Run(errorCode, func(t *testing.T) {
			fake := newFakeGoogle(t)
			fake.replyError(http.StatusBadRequest, errorCode)

			assertUnavailable(t, fake.verifier(), testCode)
		})
	}
}

// 上游故障（5xx、限流）同样是渠道不可用：一次上游抖动不该表现成"所有人的
// 凭证都失效了"。
func TestGoogleVerifierProviderFailuresAreUnavailable(t *testing.T) {
	cases := map[string]tokenReply{
		"500":    {status: http.StatusInternalServerError, body: map[string]any{}},
		"503":    {status: http.StatusServiceUnavailable, body: map[string]any{}},
		"429":    {status: http.StatusTooManyRequests, body: map[string]any{"error": "rate_limit_exceeded"}},
		"响应无法解析": {status: http.StatusOK, raw: "<html>这不是 JSON</html>"},
		"没有身份令牌": {status: http.StatusOK, body: map[string]any{"access_token": "ya29.x"}},
	}
	for name, reply := range cases {
		t.Run(name, func(t *testing.T) {
			fake := newFakeGoogle(t)
			fake.reply = reply

			assertUnavailable(t, fake.verifier(), testCode)
		})
	}
}

// 令牌端点够不着是**另一类**失败：那是我们这边的问题，不是凭证不成立。
func TestGoogleVerifierUnreachableTokenEndpointIsUnavailable(t *testing.T) {
	fake := newFakeGoogle(t)
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	dead.Close()

	verifier := newGoogleVerifier(
		fake.server.URL, testClientID, testClientSecret, testRedirectURI,
		dead.URL+"/token", fake.server.Client(),
	)

	assertUnavailable(t, verifier, testCode)
}

// 身份提供方够不着（发现失败）同样是渠道不可用，而且它发生在换取成功之后。
func TestGoogleVerifierUnreachableIssuerIsUnavailable(t *testing.T) {
	fake := newFakeGoogle(t)
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	dead.Close()

	verifier := newGoogleVerifier(
		dead.URL, testClientID, testClientSecret, testRedirectURI,
		fake.server.URL+"/token", fake.server.Client(),
	)

	assertUnavailable(t, verifier, testCode)
}

// **错误信息里不得出现客户端密钥、授权码或任何令牌。** 错误会进日志，
// 而这些值与口令同级。
func TestGoogleVerifierErrorsCarryNoSecrets(t *testing.T) {
	fake := newFakeGoogle(t)
	fake.replyError(http.StatusBadRequest, "invalid_grant")

	_, err := fake.verifier().Verify(context.Background(), testCode)
	if err == nil {
		t.Fatal("期望失败")
	}
	for _, secret := range []string{testClientSecret, testCode, fake.sign(t, nil)} {
		if strings.Contains(err.Error(), secret) {
			t.Errorf("错误信息里出现了不该出现的取值: %q", err)
		}
	}
}

// 三项缺一即没有校验器。
//
// 返回的必须是**接口层面的 nil**：一个装空指针的接口不等于 nil，
// 调用方用它做 `== nil` 判断会落空，接下去就是一次空指针解引用。
func TestNewGoogleVerifierDisabledWhenIncomplete(t *testing.T) {
	cases := map[string]struct {
		clientID     string
		clientSecret string
		redirectURI  string
	}{
		"全空":    {"", "", ""},
		"缺标识":   {"", testClientSecret, testRedirectURI},
		"缺密钥":   {testClientID, "", testRedirectURI},
		"缺回调地址": {testClientID, testClientSecret, ""},
	}
	for name, c := range cases {
		if v := NewGoogleVerifier(c.clientID, c.clientSecret, c.redirectURI); v != nil {
			t.Errorf("%s：构造出了校验器，期望接口层面的 nil", name)
		}
	}

	if NewGoogleVerifier(testClientID, testClientSecret, testRedirectURI) == nil {
		t.Error("三项齐全却没有构造出校验器")
	}
}

// 授权地址只带两项范围：openid 是换回可校验令牌的必要条件，email 是可读标识。
// **不申请 profile 之类的范围**——多要一项就多一次用户被授权页吓退的机会。
func TestGoogleAuthorizeURL(t *testing.T) {
	got := GoogleAuthorizeURL(testClientID, testRedirectURI, "the-state")

	parsed, err := url.Parse(got)
	if err != nil {
		t.Fatalf("授权地址无法解析: %v", err)
	}
	if parsed.Host != "accounts.google.com" {
		t.Errorf("授权地址的宿主 = %q", parsed.Host)
	}
	q := parsed.Query()
	for key, want := range map[string]string{
		"client_id":     testClientID,
		"redirect_uri":  testRedirectURI,
		"response_type": "code",
		"state":         "the-state",
	} {
		if q.Get(key) != want {
			t.Errorf("%s = %q，期望 %q", key, q.Get(key), want)
		}
	}
	scope := strings.Fields(q.Get("scope"))
	if len(scope) != 2 || scope[0] != "openid" || scope[1] != "email" {
		t.Errorf("scope = %q，期望只有 openid 与 email", q.Get("scope"))
	}
}

// assertInvalidToken 断言一份凭证被拒，且拒绝类别是"未认证"。
//
// 绝不是"无权限"：把两者混为一谈会让客户端把一次凭证问题表现成权限问题。
func assertInvalidToken(t *testing.T, verifier *GoogleVerifier, credential string) {
	t.Helper()
	_, err := verifier.Verify(context.Background(), credential)
	if !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("err = %v，期望 ErrInvalidToken", err)
	}
}

// assertUnavailable 断言一次失败被归成"渠道不可用"，而不是"凭证无效"。
func assertUnavailable(t *testing.T, verifier *GoogleVerifier, credential string) {
	t.Helper()
	_, err := verifier.Verify(context.Background(), credential)
	if !errors.Is(err, ErrProviderUnavailable) {
		t.Fatalf("err = %v，期望 ErrProviderUnavailable", err)
	}
	if errors.Is(err, ErrInvalidToken) {
		t.Error("渠道不可用被当成了凭证无效")
	}
}
