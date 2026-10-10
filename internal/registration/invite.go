package registration

import (
	"crypto/rand"
	"fmt"
	"math/big"
	"strings"
	"time"
)

// inviteAlphabet 是邀请码的字母表：Crockford base32 的 32 个字符。
//
// 它选的是"人抄写时不认错"这个取值集：剔掉 I、L、O、U 四个字母，于是
// "1 与 I 与 l""0 与 O"这类混淆根本无从发生（U 被剔掉是为了让它拼不出成词的
// 东西）。这与设备码短码的字母表是同一条取向（见 internal/server 的
// userCodeAlphabet），但两处的取值集**不同**：短码要被人从终端上读出来念一遍，
// 因此只用 20 个辅音字母；邀请码要被粘贴、也可能被抄，因此还需要数字。
const inviteAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// inviteCodeLength 是邀请码的字符数。
//
// 32 个字符 × 16 位 = 80 位熵。它不是"看起来够长"定的：邀请码是一份能换来
// 一次注册的 bearer 凭证，强度要落在"猜中不可行"上，与口令同级（见
// docs/design/identity/registration.md）。
const inviteCodeLength = 16

// inviteGroupSize 是给人看时每组的字符数。
const inviteGroupSize = 4

// Invite 是一份已签发的邀请码。
//
// 它**不带角色、不带范围、不绑定收件人**：只回答"这个未登记的身份可不可以被
// 登记"。进来之后拿什么，由站点策略那一份决定——合成两处意味着同一个码既是
// 准入凭证又是权限凭证，泄漏一个码就泄漏了它所带的那份权限。
type Invite struct {
	// ID 是邀请码标识，撤销时的定位键。
	//
	// 它与 CodeHash 分开，是为了让**摘要不出库**：拿到列表的人看到的是 ID 与
	// 用量，看不到摘要。摘要虽然还原不出明文，但它是一个可以离线比对的量——
	// 低熵的码会因此变得可枚举。本服务的码有 80 位熵、枚举不可行，但"把可以
	// 不暴露的东西暴露出去"没有理由。
	ID string
	// CodeHash 是码明文的摘要。**原文不入库**，与凭证同一条性质、同一个入口。
	CodeHash string
	// Label 是管理员自己看的标签。**不参与任何判断。**
	Label string
	// CreatedBySubjectID 与 CreatedAt 是留痕。
	CreatedBySubjectID string
	CreatedAt          time.Time
	// MaxUses 是次数上限，0 表示不限次。
	MaxUses int32
	// UsedCount 是已经成功兑换的次数，由兑换时的一次原子扣减推进。
	UsedCount int32
	// ExpiresAt 是失效时刻。**零值表示不过期。**
	ExpiresAt time.Time
	// RevokedAt 是撤销时刻。**零值表示未撤销**，非零即不可兑换。
	RevokedAt time.Time
}

// Redeemable 报告这份码在 at 这一刻还换不换得动。
//
// 它只用于**读侧的展示**（列表里那一列状态）与失败时的措辞。真正的判据是
// 存储里那次原子扣减——先读后写在并发下会超发，因此这里的结论不得当作放行依据。
func (i Invite) Redeemable(at time.Time) bool {
	if !i.RevokedAt.IsZero() {
		return false
	}
	if !i.ExpiresAt.IsZero() && !at.Before(i.ExpiresAt) {
		return false
	}
	return i.MaxUses == 0 || i.UsedCount < i.MaxUses
}

// NewInviteCode 生成一份邀请码，返回**规范形态**（不带连字符）。
//
// 存与比都用规范形态；给人看时那道连字符由 FormatInviteCode 加，不进来——
// 存放形态只有一种，输入的折算才有唯一的目标（见 NormalizeInviteCode）。
func NewInviteCode() (string, error) {
	limit := big.NewInt(int64(len(inviteAlphabet)))
	code := make([]byte, inviteCodeLength)
	for i := range code {
		n, err := rand.Int(rand.Reader, limit)
		if err != nil {
			return "", fmt.Errorf("生成邀请码失败: %w", err)
		}
		code[i] = inviteAlphabet[n.Int64()]
	}
	return string(code), nil
}

// NormalizeInviteCode 把使用者输入的邀请码折成存放时的规范形态。
//
// 邀请码是给人抄或粘的：大小写、连字符、顺手敲进的空格都随人怎么写。折算
// 只应有这一处——散在各调用方各折一遍，迟早会有一条路径折得不一致，而那表现
// 为"管理员发的码明明是对的，页面却说无效"（见 docs/ssot-registry.md）。
//
// 易混字符按 Crockford base32 的约定折算：I / L 折成 1，O 折成 0。本服务
// **从不签发**含这四个字母的码，因此折算只会帮到人，不会把两个不同的码折成
// 同一个——U 不在字母表里，也不折算，它就是一个不成立的字符。
func NormalizeInviteCode(s string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(s) {
		switch r {
		case '-', ' ', '\t', '\n', '\r':
			continue
		case 'I', 'L':
			b.WriteByte('1')
		case 'O':
			b.WriteByte('0')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// FormatInviteCode 把规范形态折成给人看的形式：每 inviteGroupSize 个字符一组，
// 中间一个连字符。
//
// 分组只为可读与可核对，不改变码本身——NormalizeInviteCode 会把连字符去掉。
func FormatInviteCode(code string) string {
	if code == "" {
		return ""
	}
	var b strings.Builder
	for i, r := range code {
		if i > 0 && i%inviteGroupSize == 0 {
			b.WriteByte('-')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// LooksLikeInviteCode 报告一个折好的字符串**形状上**像不像本服务签发过的码。
//
// 它只用来在做一次查询之前挡掉明显不是码的输入（空串、少打一位、粘进了别的东西）。
// 它**不是**判定：一份形状完全正确但从未签发过的码照样要查到库里才知道不成立。
// 因此调用方不得拿它的结论当"这份码有效"。
func LooksLikeInviteCode(normalized string) bool {
	if len(normalized) != inviteCodeLength {
		return false
	}
	for _, r := range normalized {
		if !strings.ContainsRune(inviteAlphabet, r) {
			return false
		}
	}
	return true
}
