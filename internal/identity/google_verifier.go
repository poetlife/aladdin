package identity

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
)

// GoogleIssuer 是 Google 的签发方标识。
const GoogleIssuer = "https://accounts.google.com"

// Google 的端点与两个浏览器直连路径。
//
// 端点是**渠道侧的事实**，不是本服务的配置项。
const (
	googleAuthorizeEndpoint = "https://accounts.google.com/o/oauth2/v2/auth"
	// gosec 按路径里的 "token" 误判成硬编码凭证了。这里是端点地址。
	googleTokenEndpoint = "https://oauth2.googleapis.com/token" //nolint:gosec // 端点地址，不是凭证

	// GoogleStartPath 是发起登录的地址，它把浏览器交给 Google。
	GoogleStartPath = "/auth/google/start"
	// GoogleCallbackPath 是 Google 把浏览器送回来的地址。
	//
	// **它是这个渠道对外契约的一部分**，因此定义在这里而不是服务端：
	// 它要与 Google 控制台里登记的"已获授权的重定向 URI"一致，也要与换令牌时
	// 给出的取值逐字一致。
	GoogleCallbackPath = "/auth/google/callback"

	// googleScope 是授权请求携带的范围。
	//
	// 只要两项：`openid` 是"换回一份能自证身份的令牌"的必要条件；`email` 是渠道
	// 给出的可读标识，用于展示与排障（也是引导按邮箱认领的依据）。**不申请
	// profile 之类的范围**：昵称与头像由本仓库的档案模块管（见
	// docs/design/profile/README.md），多要一项就多一次用户被授权页吓退的机会。
	googleScope = "openid email"
)

// googleRequestTimeout 是单次与 Google 交互的超时。
//
// 它必须存在：登录是一个同步请求，没有超时意味着 Google 慢下来时本服务的
// 请求会一直挂在那里，最终拖垮的是本服务而不是 Google。
const googleRequestTimeout = 10 * time.Second

// GoogleVerifier 是 TokenVerifier 的 Google 实现。
//
// 它与其他渠道的形状一致：**凭证不是一个自证身份的令牌，而是一个授权码**。
// 校验过程是"拿授权码与客户端密钥去换令牌"，换回来的**身份令牌**再走本模块
// 原有的四项声明校验（签名、签发方、受众、有效期）。这个差别留在本文件内部，
// 不上升到 TokenVerifier 的语义——上层只关心"给我一份凭证，我还你一个身份
// 或者一个拒绝理由"。
type GoogleVerifier struct {
	issuer       string
	clientID     string
	clientSecret string
	redirectURI  string

	// tokenEndpoint 与 client 允许测试指向本机的假 Google。
	tokenEndpoint string
	client        *http.Client

	// mu 保护 verifier。它同时起到"发现只做一次"与"并发登录不会各发现一次"
	// 两个作用。
	mu       sync.Mutex
	verifier *oidc.IDTokenVerifier
}

// NewGoogleVerifier 构造校验 Google 授权码的校验器。
//
// 三项任一为空时返回**接口层面的 nil**，而不是一个装空指针的具体类型：
// 未启用 Google 登录的部署不该有一个校验器。三项必须齐全——只有客户端标识
// 而没有密钥的校验器一次也换不到令牌，它的"已启用"是假的。
func NewGoogleVerifier(clientID, clientSecret, redirectURI string) TokenVerifier {
	if clientID == "" || clientSecret == "" || redirectURI == "" {
		return nil
	}
	return newGoogleVerifier(
		GoogleIssuer, clientID, clientSecret, redirectURI,
		googleTokenEndpoint, &http.Client{Timeout: googleRequestTimeout},
	)
}

// newGoogleVerifier 允许指定签发方、令牌端点与 HTTP 客户端，使测试能指向本地的假提供方。
func newGoogleVerifier(issuer, clientID, clientSecret, redirectURI, tokenEndpoint string, client *http.Client) *GoogleVerifier {
	return &GoogleVerifier{
		issuer:        issuer,
		clientID:      clientID,
		clientSecret:  clientSecret,
		redirectURI:   redirectURI,
		tokenEndpoint: tokenEndpoint,
		client:        client,
	}
}

// GoogleAuthorizeURL 构造交给浏览器的授权地址。
//
// redirectURI 必须与换令牌时给出的完全一致，否则 Google 会拒绝这次换取。
func GoogleAuthorizeURL(clientID, redirectURI, state string) string {
	q := url.Values{
		"client_id":     {clientID},
		"redirect_uri":  {redirectURI},
		"response_type": {"code"},
		"scope":         {googleScope},
		"state":         {state},
	}
	return googleAuthorizeEndpoint + "?" + q.Encode()
}

// Verify 实现 TokenVerifier。
//
// 凭证是 Google 在回调里交回的**授权码**。
func (v *GoogleVerifier) Verify(ctx context.Context, credential string) (VerifiedIdentity, error) {
	if credential == "" {
		return VerifiedIdentity{}, fmt.Errorf("%w: 授权码为空", ErrInvalidToken)
	}

	idToken, err := v.exchangeCode(ctx, credential)
	if err != nil {
		return VerifiedIdentity{}, err
	}
	return v.verifyIDToken(ctx, idToken)
}

// googleTokenResponse 是换取令牌的响应。
//
// 只需要身份令牌与错误码：访问令牌我们不消费（取回身份所需的一切都在身份
// 令牌的声明里），因此**不把它读进来**——多一个字段就多一处可能被顺手用上、
// 或者被顺手写进日志的东西。
type googleTokenResponse struct {
	IDToken string `json:"id_token"`
	Error   string `json:"error"`
}

func (v *GoogleVerifier) exchangeCode(ctx context.Context, code string) (string, error) {
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"client_id":     {v.clientID},
		"client_secret": {v.clientSecret},
		"redirect_uri":  {v.redirectURI},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, v.tokenEndpoint,
		strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrProviderUnavailable, err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := v.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("%w: 请求 %s 失败: %w", ErrProviderUnavailable, req.URL.Host, err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("%w: 读取 %s 的响应失败: %w", ErrProviderUnavailable, req.URL.Host, err)
	}

	// 状态码与错误码都要看：Google 对"这份授权码不成立"与"本服务的配置不成立"
	// 都用 4xx 回答，区别在响应体的 error 上。
	var parsed googleTokenResponse
	if json.Unmarshal(body, &parsed) != nil {
		// 解析不出来时按渠道不可用处理：我们不知道发生了什么，而"未认证"是
		// 一个关于**用户凭证**的明确结论，不能靠猜。
		return "", fmt.Errorf("%w: %s 的响应无法解析（HTTP %d）", ErrProviderUnavailable, req.URL.Host, resp.StatusCode)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || parsed.Error != "" {
		return "", googleTokenFailure(req.URL.Host, resp.StatusCode, parsed.Error)
	}
	if parsed.IDToken == "" {
		// 换回来了、却没有可校验的身份令牌：这一次登录确定不了身份，而原因在
		// 我们这一侧（例如授权范围里漏了 openid），不是用户凭证的问题。
		return "", fmt.Errorf("%w: %s 的响应里没有身份令牌", ErrProviderUnavailable, req.URL.Host)
	}
	return parsed.IDToken, nil
}

// googleTokenFailure 把令牌端点的否定回答归一成两类结论。
//
// **判据是响应体里的错误码，不是状态码**：
//
//   - `invalid_grant`：授权码已用过、已过期、不是签给这个应用的——这是"这次
//     登录不成立"，用户重新点一次即可，归 ErrInvalidToken；
//   - 其余错误码（`invalid_client`、`redirect_uri_mismatch`、…）与一切 5xx、
//     429、传输失败：本服务或上游的问题——它一次也换不到令牌，表现为"所有人都
//     登不进来"。把它报成"你的凭证无效"会把排障引向客户端，归 ErrProviderUnavailable。
//
// **错误原文与响应体绝不带出去**：响应体里装着身份令牌与访问令牌，而错误信息
// 会进日志。这里只带主机名、状态码与那个固定的错误码枚举。
func googleTokenFailure(host string, status int, errorCode string) error {
	if errorCode == "invalid_grant" {
		return fmt.Errorf("%w: %s 拒绝了这份授权码（HTTP %d）", ErrInvalidToken, host, status)
	}
	if errorCode != "" {
		return fmt.Errorf("%w: %s 拒绝了这次换取（HTTP %d，%s）", ErrProviderUnavailable, host, status, errorCode)
	}
	return fmt.Errorf("%w: %s 返回 HTTP %d", ErrProviderUnavailable, host, status)
}

// verifyIDToken 校验换回来的身份令牌。
//
// 它把签名、签发方、受众、有效期四项声明校验交给 go-oidc 完成，**不自行解析
// 令牌**：自研实现漏掉一条声明的概率远高于它带来的收益，而漏掉的那一条通常
// 正是受众校验。这份清单与"令牌由浏览器搬运"的形态**逐字相同**——凭证怎么
// 到达服务端，不改变它该被怎么校验。
func (v *GoogleVerifier) verifyIDToken(ctx context.Context, idToken string) (VerifiedIdentity, error) {
	if idToken == "" {
		return VerifiedIdentity{}, fmt.Errorf("%w: 令牌为空", ErrInvalidToken)
	}

	verifier, err := v.idTokenVerifier(ctx)
	if err != nil {
		return VerifiedIdentity{}, err
	}

	token, err := verifier.Verify(ctx, idToken)
	if err != nil {
		// 令牌本身绝不进入错误信息：它与口令同级，而错误信息会进日志。
		// 需要的是拒绝原因（哪条声明不符），不是令牌。
		return VerifiedIdentity{}, fmt.Errorf("%w: %w", ErrInvalidToken, err)
	}

	var claims struct {
		Email string `json:"email"`
	}
	if err := token.Claims(&claims); err != nil {
		return VerifiedIdentity{}, fmt.Errorf("%w: 声明无法解析: %w", ErrInvalidToken, err)
	}
	if token.Subject == "" {
		// 它是这个渠道上唯一的身份键。缺了它就查不到对应主体，
		// 也就无从确定这是谁——不能退化成"用邮箱顶上"，
		// 那正是本模块最硬的一条禁止。
		return VerifiedIdentity{}, fmt.Errorf("%w: 缺少不可变标识", ErrInvalidToken)
	}

	return VerifiedIdentity{ExternalID: token.Subject, Display: claims.Email}, nil
}

// idTokenVerifier 返回校验器，首次调用时完成提供方发现。
//
// **发现是懒的，不是在启动时做的。** 启动时发现意味着 Google 不可达就
// 起不来服务，而这是一个同时服务 CLI 与机器凭证的权限平台——一个可选登录
// 方式的上游故障不该让它整体不可用。
//
// 失败不缓存：一次网络抖动不该让这个进程在剩余的生命周期里再也登不进人。
// 成功则缓存，签名公钥由 go-oidc 自己按需刷新。
func (v *GoogleVerifier) idTokenVerifier(ctx context.Context) (*oidc.IDTokenVerifier, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.verifier != nil {
		return v.verifier, nil
	}

	provider, err := oidc.NewProvider(ctx, v.issuer)
	if err != nil {
		return nil, fmt.Errorf("%w: 发现 %s 失败: %w", ErrProviderUnavailable, v.issuer, err)
	}
	v.verifier = provider.Verifier(&oidc.Config{ClientID: v.clientID})
	return v.verifier, nil
}

var _ TokenVerifier = (*GoogleVerifier)(nil)
