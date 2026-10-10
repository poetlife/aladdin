// Package registration 是**注册面**的领域入口：谁进得来、进来拿什么。
//
// 它坐在"渠道凭证已校验"之后（见 docs/design/identity/channel-login.md），
// 回答两个问题：
//
//   - 一个**未登记**的渠道身份能不能被登记成新主体；
//   - 登记时要不要给它一条默认角色。
//
// 它**不判定权限**：默认角色是以一条真实的角色绑定落库的，判定路径
// （internal/rbac）只读那张表，从不读这里的策略。改策略不改变任何一次既有的
// 放行结果，删掉策略记录也不撤销任何一条已写下的绑定——这正是它区别于"配置项"
// 的判据（见 docs/design/identity/registration.md 的「与 AGENTS.md 第 7 条的关系」）。
package registration

import (
	"fmt"
	"time"

	"github.com/poetlife/aladdin/internal/rbac"
)

// Mode 是站点的准入姿态。
//
// **三选一，互斥且穷尽。** 拆成"允许注册"与"需要邀请码"两个布尔量会造出四种
// 状态，其中两种没有意义，而实现必须为它们各写一个分支。
type Mode string

const (
	// ModeOpen 表示未登记的渠道身份直接登记成新主体。
	ModeOpen Mode = "open"
	// ModeInvite 表示未登记的渠道身份要先给一份有效邀请码。
	ModeInvite Mode = "invite"
	// ModeClosed 表示不接受新主体。**只拦新主体**——已经登记过的身份照常登录。
	ModeClosed Mode = "closed"
)

// Valid 报告这个取值是不是一个已定义的姿态。
//
// 空串**不合法**：它是"这条记录还没有被写过"，由 Policy.Effective 归一成
// ModeOpen，而不是一个可以传进来的姿态。
func (m Mode) Valid() bool {
	switch m {
	case ModeOpen, ModeInvite, ModeClosed:
		return true
	default:
		return false
	}
}

// Policy 是站点级的注册策略。
//
// 站点级一份，对全部渠道生效。**没有"按渠道分别设"**：要做的话是策略多一维，
// 不是新机制（见 docs/design/identity/registration.md）。
type Policy struct {
	// Mode 是准入姿态。
	Mode Mode
	// DefaultRoleID 是新主体的默认角色。空表示不给默认角色。
	DefaultRoleID string
	// DefaultScope 是默认角色的作用范围，空表示全局。它同时成为新主体的
	// **默认作用域**——主体的默认作用域只能来自绑定关系，因此它取自这条绑定，
	// 而不是另有一个配置项。
	//
	// 与 DefaultRoleID **同进同退**：要么都给、要么都不给。
	DefaultScope rbac.Scope
	// UpdatedBySubjectID 与 UpdatedAt 是留痕。它们**不参与判定**，只回答
	// "这个姿态是谁、什么时候定的"。
	UpdatedBySubjectID string
	UpdatedAt          time.Time
}

// GrantsDefaultRole 报告这份策略会不会给新主体一条默认角色绑定。
func (p Policy) GrantsDefaultRole() bool { return p.DefaultRoleID != "" }

// Effective 返回策略里**真正生效**的取值：一条记录还没有被写过时（模式为空），
// 等价于开放注册、无默认角色、全局范围。
//
// 这条归一化只有这一处。它同时满足两件事：新部署开箱是开放注册，既有部署
// 升级后行为逐字不变——收紧准入是一个由管理员显式做出的动作，不是一个会突然
// 生效的默认值。
func (p Policy) Effective() Policy {
	if p.Mode == "" {
		p.Mode = ModeOpen
	}
	return p
}

// Validate 校验一份策略的形状是否自洽。
//
// 它只管**形状**：模式取值合法、默认角色与默认范围同进同退。三个语义约束
// ——角色是否存在、范围是否已登记、这个角色能不能当默认角色——都属于授权面，
// 唯一入口是 internal/rbac 的约束校验（见 constraints.go 的
// ValidateRegistrationDefaultRole）。在这里再判一遍就是第二份实现。
func (p Policy) Validate() error {
	if !p.Mode.Valid() {
		return fmt.Errorf("%w: 注册模式 %q 未定义", ErrPolicyInvalid, string(p.Mode))
	}
	if p.DefaultRoleID == "" && p.DefaultScope != rbac.GlobalScope {
		return fmt.Errorf("%w: 没给默认角色，就不该给默认范围", ErrPolicyInvalid)
	}
	return nil
}
