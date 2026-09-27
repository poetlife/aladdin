package identity

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// GitHub 的三个端点。
//
// 它们与客户端标识、密钥一样，是**渠道侧的事实**，不是本服务的配置项。
const (
	githubAuthorizeEndpoint = "https://github.com/login/oauth/authorize"
	// gosec 按路径里的 "access_token" 误判成硬编码凭证了。这里是端点地址。
	githubTokenEndpoint = "https://github.com/login/oauth/access_token" //nolint:gosec // 端点地址，不是凭证
	githubUserEndpoint  = "https://api.github.com/user"
	// GithubStartPath 是发起登录的地址，它把浏览器交给 GitHub。
	GithubStartPath = "/auth/github/start"
	// GithubCallbackPath 是 GitHub 把浏览器送回来的地址。
	//
	// **它是这个渠道对外契约的一部分**，因此定义在这里而不是服务端：
	// 它要与 GitHub 控制台里登记的地址一致，也要与换令牌时给出的取值逐字一致，
	// 而这三处（授权地址、换令牌、反向代理规则）必须引用同一份定义。
	GithubCallbackPath = "/auth/github/callback"
	// githubAPIVersion 固定 GitHub REST API 的版本。
	//
	// 不固定时 GitHub 会按它认为合适的方式演进响应，而"某个字段某天没了"
	// 会表现为"一部分人突然登不进来"。固定之后，变化需要我们先改这一行。
	githubAPIVersion = "2022-11-28"
	// githubUserAgent 是 GitHub 要求的请求头，缺了会被直接拒绝。
	githubUserAgent = "aladdin"
)

// githubRequestTimeout 是单次与 GitHub 交互的超时。
//
// 它必须存在：登录是一个同步请求，没有超时意味着 GitHub 慢下来时本服务的
// 请求会一直挂在那里，最终拖垮的是本服务而不是 GitHub。
const githubRequestTimeout = 10 * time.Second

// GithubVerifier 是 TokenVerifier 的 GitHub 实现。
//
// 它与其他渠道的实现形状不同：**凭证不是一个自证身份的令牌，而是一个授权码**。
// 因此校验过程不是"验证签名与声明"，而是"拿授权码去换访问令牌，再用令牌取回
// 用户信息"。这个差别留在本文件内部，不上升到 TokenVerifier 的语义——上层
// 只关心"给我一份凭证，我还你一个身份或者一个拒绝理由"。
type GithubVerifier struct {
	clientID     string
	clientSecret string
	redirectURI  string

	// tokenEndpoint 与 userEndpoint 允许测试指向本机的假 GitHub。
	tokenEndpoint string
	userEndpoint  string
	client        *http.Client
}

// NewGithubVerifier 构造校验 GitHub 授权码的校验器。
//
// 三项任一为空时返回**接口层面的 nil**，而不是一个装空指针的具体类型：
// 未启用 GitHub 登录的部署不该有一个校验器。三项必须齐全——只有客户端标识
// 而没有密钥的校验器一次也换不到令牌，它的"已启用"是假的（见
// docs/design/config/server-config.md）。
func NewGithubVerifier(clientID, clientSecret, redirectURI string) TokenVerifier {
	if clientID == "" || clientSecret == "" || redirectURI == "" {
		return nil
	}
	return newGithubVerifier(
		clientID, clientSecret, redirectURI,
		githubTokenEndpoint, githubUserEndpoint,
		&http.Client{Timeout: githubRequestTimeout},
	)
}

// newGithubVerifier 允许指定端点与 HTTP 客户端，使测试能指向本机的假 GitHub。
func newGithubVerifier(clientID, clientSecret, redirectURI, tokenEndpoint, userEndpoint string, client *http.Client) *GithubVerifier {
	return &GithubVerifier{
		clientID:      clientID,
		clientSecret:  clientSecret,
		redirectURI:   redirectURI,
		tokenEndpoint: tokenEndpoint,
		userEndpoint:  userEndpoint,
		client:        client,
	}
}

// GithubAuthorizeURL 构造交给浏览器的授权地址。
//
// **不带 scope**：取回不可变标识与登录名不需要任何授权范围，多要一项就多一次
// 用户被授权页吓退的机会。redirectURI 必须与换令牌时给出的完全一致，否则
// GitHub 会拒绝这次换取。
func GithubAuthorizeURL(clientID, redirectURI, state string) string {
	q := url.Values{
		"client_id":    {clientID},
		"redirect_uri": {redirectURI},
		"state":        {state},
	}
	return githubAuthorizeEndpoint + "?" + q.Encode()
}

// Verify 实现 TokenVerifier。
//
// 凭证是 GitHub 在回调里交回的**授权码**。
func (v *GithubVerifier) Verify(ctx context.Context, credential string) (VerifiedIdentity, error) {
	if credential == "" {
		return VerifiedIdentity{}, fmt.Errorf("%w: 授权码为空", ErrInvalidToken)
	}

	token, err := v.exchangeCode(ctx, credential)
	if err != nil {
		return VerifiedIdentity{}, err
	}
	return v.fetchIdentity(ctx, token)
}

// githubTokenResponse 是换令牌的响应。
//
// 失败时 GitHub 仍然返回 **HTTP 200**，错误写在 body 里，因此**必须解析 body
// 判断**，不能只看状态码——只看状态码会把每一次失败的登录都当成成功，然后在
// 下一步以一次空令牌的请求失败告终，其表象与真正的原因相距很远。
type githubTokenResponse struct {
	AccessToken      string `json:"access_token"`
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

func (v *GithubVerifier) exchangeCode(ctx context.Context, code string) (string, error) {
	form := url.Values{
		"client_id":     {v.clientID},
		"client_secret": {v.clientSecret},
		"code":          {code},
		"redirect_uri":  {v.redirectURI},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, v.tokenEndpoint,
		strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrProviderUnavailable, err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	// 不要求 JSON 时 GitHub 返回表单编码，解析会失败。
	req.Header.Set("Accept", "application/json")

	body, err := v.do(req)
	if err != nil {
		return "", err
	}

	var parsed githubTokenResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("%w: 换令牌的响应无法解析", ErrProviderUnavailable)
	}
	if parsed.Error != "" || parsed.AccessToken == "" {
		// 授权码已用过、已过期、不是签给这个应用的——都是"这次登录不成立"。
		// **错误原文与响应体绝不带出去**：它们可能含访问令牌或密钥。
		return "", fmt.Errorf("%w: 换取访问令牌被拒绝", ErrInvalidToken)
	}
	return parsed.AccessToken, nil
}

// githubUser 是用户信息里我们使用到的部分。
//
// 只有两项，理由与 Google 那边相同：每多一个字段，就多一种"顺手拿它做个判断"
// 的可能，而每一个这样的判断都是一条新的需要被记住的准入条件。
type githubUser struct {
	// ID 是 GitHub 的用户编号，**永不复用**，改用户名、改邮箱都不变。
	//
	// 用 int64：GitHub 的编号已经超出 int32 的范围，用 int32 会在某一天
	// 静默截断，而截断的表现是两个不同的人撞成同一个身份。
	ID int64 `json:"id"`
	// Login 是登录名。它**可被改名、可被回收**，因此只用于展示与排障，
	// **绝不参与身份的确定**。
	Login string `json:"login"`
}

func (v *GithubVerifier) fetchIdentity(ctx context.Context, token string) (VerifiedIdentity, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.userEndpoint, nil)
	if err != nil {
		return VerifiedIdentity{}, fmt.Errorf("%w: %w", ErrProviderUnavailable, err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", githubAPIVersion)
	// GitHub 拒绝没有 User-Agent 的请求。
	req.Header.Set("User-Agent", githubUserAgent)

	body, err := v.do(req)
	if err != nil {
		return VerifiedIdentity{}, err
	}

	var user githubUser
	if err := json.Unmarshal(body, &user); err != nil {
		return VerifiedIdentity{}, fmt.Errorf("%w: 用户信息的响应无法解析", ErrProviderUnavailable)
	}
	if user.ID == 0 {
		// 它是这个渠道上唯一的身份键。缺了它就查不到对应主体，也就无从确定
		// 这是谁——不能退化成"用登录名顶上"，那正是本模块最硬的一条禁止。
		return VerifiedIdentity{}, fmt.Errorf("%w: 缺少不可变标识", ErrInvalidToken)
	}

	return VerifiedIdentity{
		ExternalID: strconv.FormatInt(user.ID, 10),
		Display:    user.Login,
	}, nil
}

// do 发一次请求并读出响应体，把失败归一成两类结论。
//
// "我们够不着他"（传输错误、5xx、响应读不出来）归 ErrProviderUnavailable，
// "他明确说这个凭证不行"（4xx）归 ErrInvalidToken。这两类必须分开：混在一起
// 会让一次 GitHub 故障表现成"所有人的凭证都失效了"。
//
// **响应体绝不进入错误信息**：换令牌的响应体里就装着访问令牌。
func (v *GithubVerifier) do(req *http.Request) ([]byte, error) {
	resp, err := v.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: 请求 %s 失败: %w", ErrProviderUnavailable, req.URL.Host, err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("%w: 读取 %s 的响应失败: %w", ErrProviderUnavailable, req.URL.Host, err)
	}

	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return nil, fmt.Errorf("%w: %s 拒绝了这份凭证（HTTP %d）", ErrInvalidToken, req.URL.Host, resp.StatusCode)
	case resp.StatusCode < 200 || resp.StatusCode >= 300:
		return nil, fmt.Errorf("%w: %s 返回 HTTP %d", ErrProviderUnavailable, req.URL.Host, resp.StatusCode)
	}
	return body, nil
}

var _ TokenVerifier = (*GithubVerifier)(nil)
