package registration

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/poetlife/aladdin/internal/identity"
	"github.com/poetlife/aladdin/internal/idgen"
)

// inviteIDPrefix 是邀请码标识的前缀，让日志与库里一眼看出它是哪一类标识。
const inviteIDPrefix = "inv_"

// maxInviteLabelLength 是邀请码标签的长度上限。
//
// 标签只给管理员自己看，但它是**调用方可控的输入**，因此要有界：一份没有上界的
// 说明会让列表页与库里各多出一块大小由调用方决定的东西。
const maxInviteLabelLength = 200

// ErrInviteInvalid 表示签发一份邀请码时给的取值不成立（次数为负、有效期已经
// 过去）。它是**调用方的输入问题**，与"这份码兑换不动"是两回事。
var ErrInviteInvalid = errors.New("邀请码参数不合法")

// Registrations 是注册面的**唯一入口**。
//
// 它同时承担三件事，而这三件事必须在一起：读策略、管邀请码、把"这个身份能不能
// 被登记"这句话说成一个结论。分散开的话，"邀请码只作准入闸门"这条约束会在
// 某一条路径上悄悄失效，而它失效的表现是"某条登录路径不需要码"。
//
// 它**不写角色绑定**：授予是服务端拿着策略去调授权面的那条既有路径（见
// internal/server 的注册流程），因此本包一行都不碰 rbac 的写能力。
type Registrations struct {
	store Store
	// now 允许测试拨动时间；生产上就是 time.Now。
	now func() time.Time
}

// New 构造注册面的入口。
func New(store Store) *Registrations {
	return &Registrations{store: store, now: time.Now}
}

// Policy 返回当前生效的注册策略。
//
// **取值缺失等价于开放注册、无默认角色、全局范围**，这条归一化只有这一处
// （见 Policy.Effective）。
func (r *Registrations) Policy(ctx context.Context) (Policy, error) {
	policy, err := r.store.Policy(ctx)
	if err != nil {
		return Policy{}, err
	}
	return policy.Effective(), nil
}

// PutPolicy 写入注册策略，并留下改动人与改动时刻。
//
// 它**只改这一条记录、不授予任何权限**：新主体的默认角色是在登记那一刻写进
// 绑定表的，因此改策略不追溯已登记的主体，也不改变任何一次判定。
//
// actor 是改动者本身，不由调用方之外的任何地方推断——"这个姿态是谁定的"要能
// 答得上来。
func (r *Registrations) PutPolicy(ctx context.Context, policy Policy, actor string) (Policy, error) {
	if err := policy.Validate(); err != nil {
		return Policy{}, err
	}
	policy.UpdatedBySubjectID = actor
	policy.UpdatedAt = r.now()
	if err := r.store.PutPolicy(ctx, policy); err != nil {
		return Policy{}, err
	}
	return policy, nil
}

// List 返回全部邀请码，顺序稳定：签发时间倒序（最近的在前）。
//
// **返回值里没有明文**，也读不回来——库里存的是摘要，明文只在 Issue 那一次
// 出现过。要收回一份只能用 Revoke。
func (r *Registrations) List(ctx context.Context) ([]Invite, error) {
	list, err := r.store.Invites(ctx)
	if err != nil {
		return nil, err
	}
	sortInvites(list)
	return list, nil
}

// IssueParams 是签发一份邀请码的取值。
type IssueParams struct {
	// Label 是给管理员自己看的标签。**不参与任何判断。**
	Label string
	// MaxUses 是次数上限，0 表示不限次。
	MaxUses int32
	// ExpiresAt 是失效时刻，零值表示不过期。
	ExpiresAt time.Time
}

// Issue 签发一份邀请码，返回记录与**码的明文**。
//
// 明文只在这里返回一次：库里存的是它的摘要（与凭证同一个入口），此后再也读不
// 回来。返回值里的那份明文是管理员唯一的持有机会，界面必须把这句话说出来。
func (r *Registrations) Issue(ctx context.Context, params IssueParams, actor string) (Invite, string, error) {
	label := strings.TrimSpace(params.Label)
	if len(label) > maxInviteLabelLength {
		return Invite{}, "", fmt.Errorf("%w: 说明最多 %d 个字符", ErrInviteInvalid, maxInviteLabelLength)
	}
	if params.MaxUses < 0 {
		return Invite{}, "", fmt.Errorf("%w: 次数上限不能为负", ErrInviteInvalid)
	}
	now := r.now()
	if !params.ExpiresAt.IsZero() && !params.ExpiresAt.After(now) {
		// 有效期已经过去的码换不动，当场拒绝比在库里留一份死码好：后者表现为
		// "我明明发了码，它却说无效"。
		return Invite{}, "", fmt.Errorf("%w: 有效期必须晚于现在", ErrInviteInvalid)
	}

	code, err := NewInviteCode()
	if err != nil {
		return Invite{}, "", err
	}
	id, err := idgen.New(inviteIDPrefix)
	if err != nil {
		return Invite{}, "", err
	}

	invite := Invite{
		ID:                 id,
		CodeHash:           identity.HashToken(code),
		Label:              label,
		CreatedBySubjectID: actor,
		CreatedAt:          now,
		MaxUses:            params.MaxUses,
		ExpiresAt:          params.ExpiresAt,
	}
	if err := r.store.PutInvite(ctx, invite); err != nil {
		return Invite{}, "", err
	}
	return invite, code, nil
}

// Revoke 撤销一份邀请码。
//
// 已撤销的再撤一次是幂等的成功：重复点击不该变成一个调用方无法与真失败区分开
// 的错误（与"重复授予同一角色是幂等的重复写入"同一条取向）。
func (r *Registrations) Revoke(ctx context.Context, id string) (Invite, error) {
	if id == "" {
		return Invite{}, fmt.Errorf("%w: 邀请码标识不能为空", ErrInviteNotFound)
	}
	return r.store.RevokeInvite(ctx, id, r.now())
}

// Redeem 消费一次邀请码。
//
// **这是"邀请码只作准入闸门"的落点**：它只回答"这次兑换成不成立"，返回值里的
// 记录不含角色、不含范围，因此调用方拿不到"该给他什么"——那只能来自站点策略。
//
// 形状不像码的输入在这里就挡掉，不查库：一份少打一位的输入本来就不可能命中，
// 让它去查一次只是给库多一次无意义的负载。
func (r *Registrations) Redeem(ctx context.Context, code string) (Invite, error) {
	normalized := NormalizeInviteCode(code)
	if !LooksLikeInviteCode(normalized) {
		return Invite{}, ErrInviteUnusable
	}
	return r.store.Redeem(ctx, identity.HashToken(normalized), r.now())
}
