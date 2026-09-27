package identity

import (
	"context"
	"fmt"
	"sync"

	"github.com/coreos/go-oidc/v3/oidc"
)

// GoogleIssuer 是 Google 的签发方标识。
const GoogleIssuer = "https://accounts.google.com"

// GoogleVerifier 是 TokenVerifier 的 Google 实现。
//
// 它把签名、签发方、受众、有效期四项声明校验交给 go-oidc 完成，
// **不自行解析令牌**：自研实现漏掉一条声明的概率远高于它带来的收益，
// 而漏掉的那一条通常正是受众校验。
type GoogleVerifier struct {
	issuer   string
	clientID string

	// mu 保护 verifier。它同时起到"发现只做一次"与"并发登录不会各发现一次"
	// 两个作用。
	mu       sync.Mutex
	verifier *oidc.IDTokenVerifier
}

// NewGoogleVerifier 构造校验 Google 身份令牌的校验器。
//
// clientID 为空时返回**接口层面的 nil**，而不是一个装空指针的具体类型：
// 未启用 Google 登录的部署不该有一个校验器，而"空受众"在校验里意味着
// "任何应用签发的令牌都算数"——那正是最危险的一种配置。返回接口类型而非
// 具体类型，调用方用 `== nil` 就能判断这条路有没有开；返回具体类型的 nil
// 装进接口后 `== nil` 为假，接下去就是一次空指针解引用。
func NewGoogleVerifier(clientID string) TokenVerifier {
	if clientID == "" {
		return nil
	}
	return newGoogleVerifier(GoogleIssuer, clientID)
}

// newGoogleVerifier 允许指定签发方，使测试能指向本地的假提供方。
func newGoogleVerifier(issuer, clientID string) *GoogleVerifier {
	return &GoogleVerifier{issuer: issuer, clientID: clientID}
}

// Verify 实现 TokenVerifier。
//
// 凭证是 Google Identity Services 在浏览器内返回的身份令牌（ID token）。
func (v *GoogleVerifier) Verify(ctx context.Context, credential string) (VerifiedIdentity, error) {
	if credential == "" {
		return VerifiedIdentity{}, fmt.Errorf("%w: 令牌为空", ErrInvalidToken)
	}

	verifier, err := v.idTokenVerifier(ctx)
	if err != nil {
		return VerifiedIdentity{}, err
	}

	token, err := verifier.Verify(ctx, credential)
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
