package identity

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/coreos/go-oidc/v3/oidc"
)

// GoogleIssuer 是 Google 的签发方标识。
const GoogleIssuer = "https://accounts.google.com"

var (
	// ErrInvalidToken 表示身份令牌未通过校验。
	//
	// 它对应"未认证"，**不是**"无权限"。把两者混为一谈会让客户端把一次
	// 凭证问题表现成权限问题（见 docs/design/rbac/server-permissions.md）。
	ErrInvalidToken = errors.New("身份令牌无效")

	// ErrProviderUnavailable 表示取不到校验令牌所需的公钥。
	//
	// 它与 ErrInvalidToken 必须分开：那是"这份令牌不成立"，这是"我们这边
	// 够不着 Google"。前者是客户的错，后者是我们的，返回同一个错误会让
	// 一次外部依赖故障表现成"所有人都登录失败了"。
	ErrProviderUnavailable = errors.New("身份提供方不可用")
)

// GoogleIdentity 是一份校验通过的身份令牌里我们使用到的部分。
//
// 只有两项，因为只有这两项有消费者。**这份结构刻意不长**：
// 每多一个字段，就多一种"顺手拿它做个判断"的可能，而每一个这样的判断
// 都是一条新的、需要被记住的准入条件。
type GoogleIdentity struct {
	// Subject 是 Google 侧不可变的用户标识，即这个渠道上的身份键。
	//
	// 它是**身份**，不是 aladdin 的主体标识：主体由身份别名表按
	// (来源, 这个值) 解析得到（见 docs/design/identity/identity-linking.md）。
	Subject string
	// Email 只用于展示与排障（"这个主体是谁"）。
	//
	// **它不参与任何判定，也不参与身份的确定。** 它的验证状态同理：
	// 既然没有一次判定读它，它是否"已验证"就不构成准入条件。
	Email string
}

// TokenVerifier 校验一份身份令牌并给出它声明的主体。
//
// 校验器藏在这个接口后面，使服务端与测试都不必知道校验是怎么做的，
// 也使测试能注入一个自己签发令牌的假实现——整套测试因此不联网。
type TokenVerifier interface {
	Verify(ctx context.Context, idToken string) (GoogleIdentity, error)
}

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
func (v *GoogleVerifier) Verify(ctx context.Context, idToken string) (GoogleIdentity, error) {
	if idToken == "" {
		return GoogleIdentity{}, fmt.Errorf("%w: 令牌为空", ErrInvalidToken)
	}

	verifier, err := v.idTokenVerifier(ctx)
	if err != nil {
		return GoogleIdentity{}, err
	}

	token, err := verifier.Verify(ctx, idToken)
	if err != nil {
		// 令牌本身绝不进入错误信息：它与口令同级，而错误信息会进日志。
		// 需要的是拒绝原因（哪条声明不符），不是令牌。
		return GoogleIdentity{}, fmt.Errorf("%w: %w", ErrInvalidToken, err)
	}

	var claims struct {
		Email string `json:"email"`
	}
	if err := token.Claims(&claims); err != nil {
		return GoogleIdentity{}, fmt.Errorf("%w: 声明无法解析: %w", ErrInvalidToken, err)
	}
	if token.Subject == "" {
		// 它是这个渠道上唯一的身份键。缺了它就查不到对应主体，
		// 也就无从确定这是谁——不能退化成"用邮箱顶上"，
		// 那正是本模块最硬的一条禁止。
		return GoogleIdentity{}, fmt.Errorf("%w: 缺少不可变标识", ErrInvalidToken)
	}

	return GoogleIdentity{Subject: token.Subject, Email: claims.Email}, nil
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
