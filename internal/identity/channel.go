package identity

import (
	"context"
	"errors"
	"sort"
)

// 校验渠道凭证时的两类否定结论。
//
// 它们必须分开：ErrInvalidToken 是"这份凭证不成立"（调用方的问题），
// ErrProviderUnavailable 是"我们够不着渠道"（我们的问题）。返回同一个错误
// 会让一次外部依赖故障表现成"所有人都登录失败了"。
var (
	// ErrInvalidToken 表示渠道凭证未通过校验。
	//
	// 它对应"未认证"，**不是**"无权限"。把两者混为一谈会让客户端把一次
	// 凭证问题表现成权限问题（见 docs/design/rbac/server-permissions.md）。
	ErrInvalidToken = errors.New("渠道凭证无效")

	// ErrProviderUnavailable 表示取不到校验凭证所需的渠道设施。
	ErrProviderUnavailable = errors.New("身份提供方不可用")

	// ErrChannelDisabled 表示请求的登录渠道未启用。
	//
	// 它与 ErrInvalidToken 必须分开：那是"这份凭证不成立"，这是"这条路没开"。
	// 混为一谈会把一次配置缺失表现成一次凭证故障（见
	// docs/design/identity/channel-login.md）。
	ErrChannelDisabled = errors.New("登录渠道未启用")
)

// VerifiedIdentity 是一份**校验通过**的渠道凭证里我们使用到的部分。
//
// 只有两项，因为只有这两项有消费者。**这份结构刻意不长**：每多一个字段，
// 就多一种"顺手拿它做个判断"的可能，而每一个这样的判断都是一条新的、
// 需要被记住的准入条件。
//
// 它属于所有渠道，不属于某一个：Google 从 ID token 的声明里取，GitHub 从
// 用户接口的响应里取，取值方式不同，形状相同。
type VerifiedIdentity struct {
	// ExternalID 是该渠道签发的不可变标识，即这个渠道上的身份键。
	//
	// 它是**渠道身份**，不是 aladdin 的主体标识：主体由身份别名表按
	// (来源, 这个值) 解析得到（见 docs/design/identity/identity-linking.md）。
	ExternalID string
	// Display 是该渠道给出的可读标识（Google 是邮箱，GitHub 是登录名），
	// 仅供展示与排障。
	//
	// **不参与任何判定，也不参与身份的确定。** 按它归并意味着渠道侧改名或
	// 回收标识的那天，另一个人直接接管这个主体的全部角色绑定。
	Display string
}

// TokenVerifier 校验一份渠道凭证并给出它声明的身份。
//
// credential 的含义由渠道决定：Google 是一份 ID token，GitHub 是一个授权码。
// 这个差别留在**实现内部**，不上升到接口语义——上层只关心"给我一份凭证，
// 我还你一个身份或者一个拒绝理由"。
//
// 校验器藏在接口后面，使服务端与测试都不必知道校验是怎么做的，
// 也使测试能注入一个自己签发凭证的假实现——整套测试因此不联网。
type TokenVerifier interface {
	Verify(ctx context.Context, credential string) (VerifiedIdentity, error)
}

// Channel 是一个已启用的登录渠道。
type Channel struct {
	// Source 是渠道来源标识，取值见 SourceGoogle / SourceGithub。
	Source string
	// ClientID 是该渠道的公开客户端标识。
	//
	// 它**不是秘密**：这个值本来就明文出现在浏览器里，这是这类登录方式的
	// 设计前提，因此可以下发（见 docs/design/identity/channel-login.md）。
	ClientID string
	// Verifier 校验该渠道签发的凭证。为 nil 表示这个渠道未启用。
	Verifier TokenVerifier
}

// Registry 是**已启用**渠道的集合，是"当前有哪些登录方式"的唯一来源。
//
// 未启用的渠道不进这个集合，而不是留一个 Verifier 为 nil 的位置：一个
// "装在那里的空实现"迟早会被某条路径直接调用，而它的行为是拒绝还是放行
// 取决于写的人当时怎么想——那正是未启用的登录方式变成后门的方式。
type Registry struct {
	// ordered 按 Source 排序，使下发给客户端的渠道清单顺序稳定。
	ordered  []Channel
	bySource map[string]Channel
}

// NewRegistry 由一组渠道构造注册表，**丢弃 Verifier 为 nil 的项**。
func NewRegistry(channels ...Channel) *Registry {
	r := &Registry{bySource: make(map[string]Channel, len(channels))}
	for _, ch := range channels {
		if ch.Verifier == nil {
			continue
		}
		r.bySource[ch.Source] = ch
	}
	r.ordered = make([]Channel, 0, len(r.bySource))
	for _, ch := range r.bySource {
		r.ordered = append(r.ordered, ch)
	}
	sort.Slice(r.ordered, func(i, j int) bool { return r.ordered[i].Source < r.ordered[j].Source })
	return r
}

// Get 取一个已启用的渠道。
func (r *Registry) Get(source string) (Channel, bool) {
	if r == nil {
		return Channel{}, false
	}
	ch, ok := r.bySource[source]
	return ch, ok
}

// Methods 返回已启用的渠道，按 Source 排序。顺序稳定，便于下发给客户端。
func (r *Registry) Methods() []Channel {
	if r == nil {
		return nil
	}
	return r.ordered
}

// Sources 返回已启用的渠道来源，顺序与 Methods 一致。
//
// 给"把已启用的登录方式写进启动日志"用：那里只需要来源，不需要客户端标识。
func (r *Registry) Sources() []string {
	methods := r.Methods()
	sources := make([]string, 0, len(methods))
	for _, ch := range methods {
		sources = append(sources, ch.Source)
	}
	return sources
}
