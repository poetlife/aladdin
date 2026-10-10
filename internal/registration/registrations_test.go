package registration

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/poetlife/aladdin/internal/rbac"
)

// clock 是一枚可以由用例拨动的时钟。
type clock struct{ at time.Time }

func (c *clock) now() time.Time          { return c.at }
func (c *clock) advance(d time.Duration) { c.at = c.at.Add(d) }

func newTestRegistrations() (*Registrations, *clock) {
	c := &clock{at: time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)}
	regs := New(NewMemoryStore())
	regs.now = c.now
	return regs, c
}

// 没有记录时的取值就是**缺省姿态**：开放注册、无默认角色、全局范围。
//
// 这一条同时满足两件事：新部署开箱是开放注册，既有部署升级后行为逐字不变。
// 它是"迁移只建表、不写行"那条约定的行为面。
func TestPolicyDefaultsToOpenWithoutDefaultRole(t *testing.T) {
	regs, _ := newTestRegistrations()

	policy, err := regs.Policy(context.Background())
	if err != nil {
		t.Fatalf("读取策略失败: %v", err)
	}
	if policy.Mode != ModeOpen {
		t.Errorf("缺省模式应为 %q，实际 %q", ModeOpen, policy.Mode)
	}
	if policy.GrantsDefaultRole() {
		t.Errorf("缺省不该有默认角色，实际 %q", policy.DefaultRoleID)
	}
	if policy.DefaultScope != rbac.GlobalScope {
		t.Errorf("缺省范围应为全局，实际 %q", policy.DefaultScope)
	}
}

// 模式是三选一：未定义的取值（含空串）不得被写进去。
//
// 空串是"这条记录还没有被写过"，它由 Effective 归一成开放注册，而不是一个
// 可以被调用方传进来的姿态。
func TestPutPolicyRejectsUndefinedMode(t *testing.T) {
	regs, _ := newTestRegistrations()

	for _, mode := range []Mode{"", "public", "OPEN"} {
		_, err := regs.PutPolicy(context.Background(), Policy{Mode: mode}, "usr_admin")
		if !errors.Is(err, ErrPolicyInvalid) {
			t.Errorf("模式 %q 应被拒绝，实际 %v", mode, err)
		}
	}
}

// 默认角色与默认范围同进同退：只给一半是笔误，当场拒绝比在库里留一条半成品好。
func TestPutPolicyRequiresRoleAndScopeTogether(t *testing.T) {
	regs, _ := newTestRegistrations()

	_, err := regs.PutPolicy(context.Background(), Policy{
		Mode:         ModeOpen,
		DefaultScope: rbac.Scope("tenant/acme"),
	}, "usr_admin")
	if !errors.Is(err, ErrPolicyInvalid) {
		t.Fatalf("只给范围不给角色应被拒绝，实际 %v", err)
	}

	// 只给角色、范围留空是合法的：空范围就是全局。
	if _, err := regs.PutPolicy(context.Background(), Policy{
		Mode:          ModeInvite,
		DefaultRoleID: "viewer",
	}, "usr_admin"); err != nil {
		t.Fatalf("只给角色应当合法（范围留空即全局），实际 %v", err)
	}
}

// 改动人与改动时刻由入口写下，不由调用方给：它回答"这个姿态是谁定的"。
func TestPutPolicyRecordsActor(t *testing.T) {
	regs, c := newTestRegistrations()

	policy, err := regs.PutPolicy(context.Background(), Policy{Mode: ModeClosed}, "usr_admin")
	if err != nil {
		t.Fatalf("写入策略失败: %v", err)
	}
	if policy.UpdatedBySubjectID != "usr_admin" {
		t.Errorf("改动人应为 usr_admin，实际 %q", policy.UpdatedBySubjectID)
	}
	if !policy.UpdatedAt.Equal(c.now()) {
		t.Errorf("改动时刻应为当前时刻，实际 %v", policy.UpdatedAt)
	}
}

// 签发的码：明文只返回一次，库里只有摘要，列表里读不回明文。
func TestIssueReturnsPlaintextExactlyOnce(t *testing.T) {
	regs, _ := newTestRegistrations()
	ctx := context.Background()

	invite, code, err := regs.Issue(ctx, IssueParams{Label: "给张三", MaxUses: 1}, "usr_admin")
	if err != nil {
		t.Fatalf("签发失败: %v", err)
	}
	if code == "" {
		t.Fatal("签发必须返回明文")
	}
	if invite.CodeHash == code || strings.Contains(invite.CodeHash, code) {
		t.Error("库里存的必须是摘要，不是明文")
	}
	if invite.CodeHash == "" {
		t.Error("摘要不能为空")
	}

	list, err := regs.List(ctx)
	if err != nil {
		t.Fatalf("列表失败: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("应有一条邀请码，实际 %d", len(list))
	}
	// 列表里没有明文字段——这一点由类型保证；这里钉住的是"摘要也不给出去"，
	// 让读侧无从离线比对一份猜出来的码。
	entry := list[0]
	if entry.Label != "给张三" {
		t.Errorf("说明应原样保留，实际 %q", entry.Label)
	}
	if entry.ID == "" || entry.ID == code {
		t.Errorf("定位键应当是独立的标识，实际 %q", entry.ID)
	}
}

// 形状不像码的输入在做一次查询之前就被挡掉。
func TestRedeemRejectsMalformedCode(t *testing.T) {
	regs, _ := newTestRegistrations()

	for _, code := range []string{"", "短", strings.Repeat("A", inviteCodeLength-1)} {
		if _, err := regs.Redeem(context.Background(), code); !errors.Is(err, ErrInviteUnusable) {
			t.Errorf("码 %q 应被拒绝，实际 %v", code, err)
		}
	}
}

// 归一只应有一处：大小写、连字符、易混字符的写法都折到同一份码上。
func TestRedeemNormalizesInput(t *testing.T) {
	regs, _ := newTestRegistrations()
	ctx := context.Background()

	_, code, err := regs.Issue(ctx, IssueParams{}, "usr_admin")
	if err != nil {
		t.Fatalf("签发失败: %v", err)
	}

	// 造一个含易混字符的写法：把 1 写成 I、0 写成 O，并加上连字符与小写。
	noisy := strings.ToLower(FormatInviteCode(code))
	noisy = strings.NewReplacer("1", "i", "0", "o").Replace(noisy)
	noisy = strings.ToUpper(noisy) + " "

	if _, err := regs.Redeem(ctx, noisy); err != nil {
		t.Fatalf("折算后应当命中同一份码，实际 %v", err)
	}
}

// 次数上限：一份一次性的码只能兑换一次，第二次给同一个结论。
func TestRedeemHonoursMaxUses(t *testing.T) {
	regs, _ := newTestRegistrations()
	ctx := context.Background()

	_, code, err := regs.Issue(ctx, IssueParams{MaxUses: 1}, "usr_admin")
	if err != nil {
		t.Fatalf("签发失败: %v", err)
	}
	if _, err := regs.Redeem(ctx, code); err != nil {
		t.Fatalf("第一次兑换应当成功: %v", err)
	}
	if _, err := regs.Redeem(ctx, code); !errors.Is(err, ErrInviteUnusable) {
		t.Fatalf("第二次兑换应当失败，实际 %v", err)
	}

	// 0 表示不限次。
	_, unlimited, err := regs.Issue(ctx, IssueParams{}, "usr_admin")
	if err != nil {
		t.Fatalf("签发失败: %v", err)
	}
	for i := range 5 {
		if _, err := regs.Redeem(ctx, unlimited); err != nil {
			t.Fatalf("第 %d 次兑换不应受限: %v", i+1, err)
		}
	}
}

// **不超发**：同一份上限为 1 的码被并发兑换 N 次，恰好成功一次。
//
// 这是"判断与写入是一次操作"的可验证形式：先查后写在并发下会各读到一次
// "还剩一次"。
func TestRedeemDoesNotOversellUnderConcurrency(t *testing.T) {
	regs, _ := newTestRegistrations()
	ctx := context.Background()

	_, code, err := regs.Issue(ctx, IssueParams{MaxUses: 1}, "usr_admin")
	if err != nil {
		t.Fatalf("签发失败: %v", err)
	}

	const racers = 16
	var wg sync.WaitGroup
	var mu sync.Mutex
	succeeded := 0
	for range racers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := regs.Redeem(ctx, code); err == nil {
				mu.Lock()
				succeeded++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if succeeded != 1 {
		t.Fatalf("上限为 1 的码并发兑换应恰好成功一次，实际 %d 次", succeeded)
	}
	list, err := regs.List(ctx)
	if err != nil {
		t.Fatalf("列表失败: %v", err)
	}
	if list[0].UsedCount != 1 {
		t.Errorf("用量应为 1，实际 %d", list[0].UsedCount)
	}
}

// 过期：到点即不可兑换。
func TestRedeemHonoursExpiry(t *testing.T) {
	regs, c := newTestRegistrations()
	ctx := context.Background()

	_, code, err := regs.Issue(ctx, IssueParams{ExpiresAt: c.now().Add(time.Hour)}, "usr_admin")
	if err != nil {
		t.Fatalf("签发失败: %v", err)
	}
	c.advance(2 * time.Hour)
	if _, err := regs.Redeem(ctx, code); !errors.Is(err, ErrInviteUnusable) {
		t.Fatalf("过期的码应不可兑换，实际 %v", err)
	}
}

// 有效期已经过去的码当场拒绝：在库里留一份换不动的死码，表现为"我明明发了码，
// 它却说无效"。
func TestIssueRejectsPastExpiry(t *testing.T) {
	regs, c := newTestRegistrations()

	_, _, err := regs.Issue(context.Background(),
		IssueParams{ExpiresAt: c.now().Add(-time.Minute)}, "usr_admin")
	if !errors.Is(err, ErrInviteInvalid) {
		t.Fatalf("过去的有效期应被拒绝，实际 %v", err)
	}
}

// 撤销立即生效，且**只撤销、不删除**：行还在，用量与留痕留着。
func TestRevokeIsImmediateAndKeepsTheRow(t *testing.T) {
	regs, _ := newTestRegistrations()
	ctx := context.Background()

	invite, code, err := regs.Issue(ctx, IssueParams{MaxUses: 3}, "usr_admin")
	if err != nil {
		t.Fatalf("签发失败: %v", err)
	}
	if _, err := regs.Redeem(ctx, code); err != nil {
		t.Fatalf("撤销前应当可以兑换: %v", err)
	}

	revoked, err := regs.Revoke(ctx, invite.ID)
	if err != nil {
		t.Fatalf("撤销失败: %v", err)
	}
	if revoked.RevokedAt.IsZero() {
		t.Error("撤销后应当有时间戳")
	}
	if _, err := regs.Redeem(ctx, code); !errors.Is(err, ErrInviteUnusable) {
		t.Fatalf("撤销后应不可兑换，实际 %v", err)
	}

	list, err := regs.List(ctx)
	if err != nil {
		t.Fatalf("列表失败: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("撤销不该删除这一行，实际剩 %d 条", len(list))
	}
	if list[0].UsedCount != 1 {
		t.Errorf("用量应当留着，实际 %d", list[0].UsedCount)
	}
}

// 重复撤销是**幂等的成功**，且不覆盖第一次的时间戳：第一次撤销的时刻才是
// "这个码什么时候失效的"这个问题的答案。
func TestRevokeIsIdempotent(t *testing.T) {
	regs, c := newTestRegistrations()
	ctx := context.Background()

	invite, _, err := regs.Issue(ctx, IssueParams{}, "usr_admin")
	if err != nil {
		t.Fatalf("签发失败: %v", err)
	}
	first, err := regs.Revoke(ctx, invite.ID)
	if err != nil {
		t.Fatalf("第一次撤销失败: %v", err)
	}
	c.advance(time.Hour)
	second, err := regs.Revoke(ctx, invite.ID)
	if err != nil {
		t.Fatalf("重复撤销应当是幂等的成功，实际 %v", err)
	}
	if !second.RevokedAt.Equal(first.RevokedAt) {
		t.Errorf("重复撤销不该改写时间戳：%v → %v", first.RevokedAt, second.RevokedAt)
	}
}

// 撤销一个不存在的标识是"找不到"，不是幂等成功：它与"已经撤销过"不同。
func TestRevokeUnknownInvite(t *testing.T) {
	regs, _ := newTestRegistrations()

	if _, err := regs.Revoke(context.Background(), "inv_nope"); !errors.Is(err, ErrInviteNotFound) {
		t.Fatalf("不存在的标识应报找不到，实际 %v", err)
	}
}

// 列表顺序稳定：签发时间倒序。
func TestListIsNewestFirst(t *testing.T) {
	regs, c := newTestRegistrations()
	ctx := context.Background()

	var ids []string
	for range 3 {
		invite, _, err := regs.Issue(ctx, IssueParams{}, "usr_admin")
		if err != nil {
			t.Fatalf("签发失败: %v", err)
		}
		ids = append(ids, invite.ID)
		c.advance(time.Second)
	}

	list, err := regs.List(ctx)
	if err != nil {
		t.Fatalf("列表失败: %v", err)
	}
	for i, invite := range list {
		want := ids[len(ids)-1-i]
		if invite.ID != want {
			t.Fatalf("第 %d 条应为最新签发的 %q，实际 %q", i, want, invite.ID)
		}
	}
}
