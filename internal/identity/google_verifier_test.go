package identity

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

// 校验清单的每一条都必须生效，缺一条的后果不对称：多一条只会多一种把
// 合法用户挡在门外的失败方式，少一条则可能是"任何 Google 应用的令牌都能
// 登录本服务"。
//
// **整套用例不联网**：假的提供方跑在 httptest 上，签名密钥由用例自己生成。

const testClientID = "1234567890.apps.googleusercontent.com"

// fakeGoogle 是一个本地的身份提供方：它公布发现文档与公钥，并签发令牌。
type fakeGoogle struct {
	server *httptest.Server
	key    *rsa.PrivateKey
	keyID  string
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
	fake.server = httptest.NewServer(mux)
	t.Cleanup(fake.server.Close)
	return fake
}

// verify 用这个假提供方构造一个校验器。
func (f *fakeGoogle) verifier() *GoogleVerifier {
	return newGoogleVerifier(f.server.URL, testClientID)
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

// 一份合法的令牌解出身份标识与展示信息，且**与邮箱无关**。
func TestGoogleVerifierAcceptsValidToken(t *testing.T) {
	fake := newFakeGoogle(t)
	token := fake.sign(t, map[string]any{"email": "zhang@example.com"})

	got, err := fake.verifier().Verify(context.Background(), token)
	if err != nil {
		t.Fatalf("合法令牌被拒: %v", err)
	}
	if got.Subject != "109876543210987654321" {
		t.Errorf("身份标识 = %q", got.Subject)
	}
	if got.Email != "zhang@example.com" {
		t.Errorf("展示信息 = %q，期望记录邮箱用于展示", got.Email)
	}
}

// 受众不匹配必须拒绝，且这一类错误最容易漏。
//
// 漏掉它意味着**任何** Google 应用的令牌都能登录本服务——包括攻击者自己
// 注册的那个应用：签名是真的、签发方是 Google、也没过期，唯独不是签给我们的。
func TestGoogleVerifierRejectsForeignAudience(t *testing.T) {
	fake := newFakeGoogle(t)
	token := fake.sign(t, map[string]any{"aud": "someone-else.apps.googleusercontent.com"})

	assertInvalidToken(t, fake.verifier(), token)
}

// 过期的令牌不是身份证明。
func TestGoogleVerifierRejectsExpired(t *testing.T) {
	fake := newFakeGoogle(t)
	token := fake.sign(t, map[string]any{
		"exp": time.Now().Add(-time.Minute).Unix(),
	})

	assertInvalidToken(t, fake.verifier(), token)
}

// 签发方不符：格式相同、签名也可能是真的，但不是我们的身份来源。
func TestGoogleVerifierRejectsForeignIssuer(t *testing.T) {
	fake := newFakeGoogle(t)
	token := fake.sign(t, map[string]any{"iss": "https://accounts.example.com"})

	assertInvalidToken(t, fake.verifier(), token)
}

// 签名对不上：换一把密钥签，公钥集合里没有它。
func TestGoogleVerifierRejectsBadSignature(t *testing.T) {
	fake := newFakeGoogle(t)
	other, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("生成密钥失败: %v", err)
	}
	token := fake.signWith(t, nil, other, fake.keyID)

	assertInvalidToken(t, fake.verifier(), token)
}

// 缺少不可变标识：它是这个渠道上唯一的身份键，缺了它就无从确定这是谁。
//
// **不得退化成"用邮箱顶上"**——那正是本模块最硬的一条禁止。
func TestGoogleVerifierRejectsMissingSubject(t *testing.T) {
	fake := newFakeGoogle(t)
	// 声明里显式把它置空：jwt 的 Claims 序列化会带上这个键。
	token := fake.sign(t, map[string]any{"sub": "", "email": "zhang@example.com"})

	assertInvalidToken(t, fake.verifier(), token)
}

// 空令牌不必走到网络。
func TestGoogleVerifierRejectsEmptyToken(t *testing.T) {
	fake := newFakeGoogle(t)

	assertInvalidToken(t, fake.verifier(), "")
}

// 提供方够不着是**另一类**失败：那是我们这边的问题，不是令牌不成立。
//
// 混为一谈会让一次外部依赖故障表现成"所有人都登录失败了"。
func TestGoogleVerifierDistinguishesProviderFailure(t *testing.T) {
	fake := newFakeGoogle(t)
	// 用一份**合法**的令牌：这样失败只可能来自"够不着提供方"，
	// 而不是"这份令牌本身有问题"。
	token := fake.sign(t, nil)

	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	dead.Close()

	_, err := newGoogleVerifier(dead.URL, testClientID).Verify(context.Background(), token)
	if !errors.Is(err, ErrProviderUnavailable) {
		t.Fatalf("err = %v，期望 ErrProviderUnavailable", err)
	}
	if errors.Is(err, ErrInvalidToken) {
		t.Error("提供方不可达被当成了令牌无效")
	}
}

// 未配置客户端标识时没有校验器。
//
// 返回的必须是**接口层面的 nil**：一个装空指针的接口不等于 nil，
// 调用方用它做 `== nil` 判断会落空，接下去就是一次空指针解引用。
func TestGoogleVerifierAbsentWhenNotConfigured(t *testing.T) {
	if NewGoogleVerifier("") != nil {
		t.Error("未配置客户端标识却构造出了校验器")
	}
	if NewGoogleVerifier(testClientID) == nil {
		t.Error("配置了客户端标识却没有构造出校验器")
	}
}

// assertInvalidToken 断言一份令牌被拒，且拒绝类别是"未认证"。
//
// 绝不是"无权限"：把两者混为一谈会让客户端把一次凭证问题表现成权限问题。
func assertInvalidToken(t *testing.T, verifier *GoogleVerifier, token string) {
	t.Helper()
	_, err := verifier.Verify(context.Background(), token)
	if !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("err = %v，期望 ErrInvalidToken", err)
	}
}
