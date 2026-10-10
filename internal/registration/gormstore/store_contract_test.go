package gormstore

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/poetlife/aladdin/internal/config"
	"github.com/poetlife/aladdin/internal/database"
	"github.com/poetlife/aladdin/internal/database/migrate"
	"github.com/poetlife/aladdin/internal/registration"
)

// 两个存储实现共用同一套用例。
//
// 这是本仓库里"同一件事只有一个实现"之外的另一半：**同一份契约只能有一套用例**。
// 给关系库实现单独写一套，两套就会各自漂移，而漂移的表现形式是"换后端之后行为
// 变了"——最难在事后归因的一类问题。
//
// 用例放在 gormstore 包内而不是 internal/registration：后者若导入 gormstore 会
// 形成导入环（gormstore 依赖 registration）。

var testNow = time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)

// storeCase 是一个存储实现的构造方式。
type storeCase struct {
	name string
	open func(t *testing.T) registration.Store
}

func storeCases(t *testing.T) []storeCase {
	t.Helper()
	return []storeCase{
		{
			name: "内存实现",
			open: func(*testing.T) registration.Store { return registration.NewMemoryStore() },
		},
		{
			// 走真实的连接与迁移：它同时覆盖"表建出来了吗、条件更新在这个方言上
			// 成立吗"这两件事，而内存实现把它们挡在测试之外。
			name: "关系库实现",
			open: func(t *testing.T) registration.Store { return newTestStore(t) },
		},
	}
}

// forEachStore 把同一段用例跑在两个实现上。
func forEachStore(t *testing.T, run func(t *testing.T, store registration.Store)) {
	t.Helper()
	for _, sc := range storeCases(t) {
		t.Run(sc.name, func(t *testing.T) {
			run(t, sc.open(t))
		})
	}
}

// 没有记录时返回零值：**归一化只有领域层那一处**，两个实现不各补一次。
func TestContractMissingPolicyIsZeroValue(t *testing.T) {
	forEachStore(t, func(t *testing.T, store registration.Store) {
		policy, err := store.Policy(context.Background())
		if err != nil {
			t.Fatalf("读取策略失败: %v", err)
		}
		if policy.Mode != "" {
			t.Errorf("没有记录时应当是零值（空模式），实际 %q", policy.Mode)
		}
	})
}

// 写入后读回一致；重复写入是覆盖，不是新增一条。
func TestContractPolicyRoundTrip(t *testing.T) {
	forEachStore(t, func(t *testing.T, store registration.Store) {
		ctx := context.Background()
		want := registration.Policy{
			Mode:               registration.ModeInvite,
			DefaultRoleID:      "viewer",
			DefaultScope:       "tenant/acme",
			UpdatedBySubjectID: "usr_admin",
			UpdatedAt:          testNow,
		}
		if err := store.PutPolicy(ctx, want); err != nil {
			t.Fatalf("写入策略失败: %v", err)
		}
		got, err := store.Policy(ctx)
		if err != nil {
			t.Fatalf("读取策略失败: %v", err)
		}
		if got.Mode != want.Mode || got.DefaultRoleID != want.DefaultRoleID ||
			got.DefaultScope != want.DefaultScope || got.UpdatedBySubjectID != want.UpdatedBySubjectID {
			t.Fatalf("读回与写入不一致: %+v", got)
		}

		// 再写一次：这张表只有一行。
		want.Mode = registration.ModeClosed
		if err := store.PutPolicy(ctx, want); err != nil {
			t.Fatalf("覆盖策略失败: %v", err)
		}
		got, err = store.Policy(ctx)
		if err != nil {
			t.Fatalf("读取策略失败: %v", err)
		}
		if got.Mode != registration.ModeClosed {
			t.Fatalf("覆盖没生效，实际 %q", got.Mode)
		}
	})
}

// 可空时间列：不过期 / 未撤销落成 NULL，不落成年份 1。
//
// 这不是洁癖：MySQL 的 datetime 下界是 1000 年，零值时间在那上面根本插不进去。
func TestContractInviteNullableTimes(t *testing.T) {
	forEachStore(t, func(t *testing.T, store registration.Store) {
		ctx := context.Background()
		invite := registration.Invite{
			ID:                 "inv_a",
			CodeHash:           "hash-a",
			CreatedBySubjectID: "usr_admin",
			CreatedAt:          testNow,
		}
		if err := store.PutInvite(ctx, invite); err != nil {
			t.Fatalf("写入邀请码失败: %v", err)
		}
		list, err := store.Invites(ctx)
		if err != nil {
			t.Fatalf("读取邀请码失败: %v", err)
		}
		if len(list) != 1 {
			t.Fatalf("应有 1 条，实际 %d", len(list))
		}
		if !list[0].ExpiresAt.IsZero() || !list[0].RevokedAt.IsZero() {
			t.Errorf("两个可空时间都应当是零值，实际 %+v", list[0])
		}

		// 带下次的往返同样要对得上。
		expires := testNow.Add(24 * time.Hour)
		invite.ID, invite.CodeHash, invite.ExpiresAt = "inv_b", "hash-b", expires
		if err := store.PutInvite(ctx, invite); err != nil {
			t.Fatalf("写入邀请码失败: %v", err)
		}
		list, err = store.Invites(ctx)
		if err != nil {
			t.Fatalf("读取邀请码失败: %v", err)
		}
		var found bool
		for _, got := range list {
			if got.ID == "inv_b" {
				found = true
				if !got.ExpiresAt.Equal(expires) {
					t.Errorf("有效期应为 %v，实际 %v", expires, got.ExpiresAt)
				}
			}
		}
		if !found {
			t.Error("没读到刚写下的那条")
		}
	})
}

// 兑换：成功后用量加一；不可兑换时**不产生任何写入**。
func TestContractRedeem(t *testing.T) {
	forEachStore(t, func(t *testing.T, store registration.Store) {
		ctx := context.Background()
		if err := store.PutInvite(ctx, registration.Invite{
			ID: "inv_a", CodeHash: "hash-a", CreatedBySubjectID: "usr_admin",
			CreatedAt: testNow, MaxUses: 1,
		}); err != nil {
			t.Fatalf("写入邀请码失败: %v", err)
		}

		got, err := store.Redeem(ctx, "hash-a", testNow)
		if err != nil {
			t.Fatalf("第一次兑换应当成功: %v", err)
		}
		if got.UsedCount != 1 {
			t.Errorf("用量应为 1，实际 %d", got.UsedCount)
		}

		if _, err := store.Redeem(ctx, "hash-a", testNow); !errors.Is(err, registration.ErrInviteUnusable) {
			t.Fatalf("第二次兑换应当失败，实际 %v", err)
		}
		if _, err := store.Redeem(ctx, "hash-unknown", testNow); !errors.Is(err, registration.ErrInviteUnusable) {
			t.Fatalf("不存在的码应当失败，实际 %v", err)
		}
	})
}

// 过期与撤销都在条件里判：不成立时那一行原封不动。
func TestContractRedeemRespectsExpiryAndRevocation(t *testing.T) {
	forEachStore(t, func(t *testing.T, store registration.Store) {
		ctx := context.Background()
		expired := testNow.Add(-time.Hour)
		if err := store.PutInvite(ctx, registration.Invite{
			ID: "inv_expired", CodeHash: "hash-expired", CreatedBySubjectID: "usr_admin",
			CreatedAt: testNow, ExpiresAt: expired,
		}); err != nil {
			t.Fatalf("写入邀请码失败: %v", err)
		}
		if _, err := store.Redeem(ctx, "hash-expired", testNow); !errors.Is(err, registration.ErrInviteUnusable) {
			t.Fatalf("过期的码应当失败，实际 %v", err)
		}

		if err := store.PutInvite(ctx, registration.Invite{
			ID: "inv_revoked", CodeHash: "hash-revoked", CreatedBySubjectID: "usr_admin",
			CreatedAt: testNow,
		}); err != nil {
			t.Fatalf("写入邀请码失败: %v", err)
		}
		if _, err := store.RevokeInvite(ctx, "inv_revoked", testNow); err != nil {
			t.Fatalf("撤销失败: %v", err)
		}
		if _, err := store.Redeem(ctx, "hash-revoked", testNow); !errors.Is(err, registration.ErrInviteUnusable) {
			t.Fatalf("已撤销的码应当失败，实际 %v", err)
		}

		list, err := store.Invites(ctx)
		if err != nil {
			t.Fatalf("读取邀请码失败: %v", err)
		}
		for _, invite := range list {
			if invite.UsedCount != 0 {
				t.Errorf("%s 的用量不该被推进，实际 %d", invite.ID, invite.UsedCount)
			}
		}
	})
}

// **不超发**：同一份上限为 1 的码被并发兑换，恰好成功一次。
//
// 它在关系库实现上钉住的是那条条件更新（而不是"先查后写"），在并发下这是唯一
// 站得住的写法。
func TestContractRedeemDoesNotOversell(t *testing.T) {
	forEachStore(t, func(t *testing.T, store registration.Store) {
		ctx := context.Background()
		if err := store.PutInvite(ctx, registration.Invite{
			ID: "inv_a", CodeHash: "hash-a", CreatedBySubjectID: "usr_admin",
			CreatedAt: testNow, MaxUses: 1,
		}); err != nil {
			t.Fatalf("写入邀请码失败: %v", err)
		}

		const racers = 16
		var wg sync.WaitGroup
		var mu sync.Mutex
		succeeded := 0
		for range racers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if _, err := store.Redeem(ctx, "hash-a", testNow); err == nil {
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
	})
}

// 撤销不存在与重复撤销：前者是找不到，后者是幂等的成功。
func TestContractRevoke(t *testing.T) {
	forEachStore(t, func(t *testing.T, store registration.Store) {
		ctx := context.Background()
		if _, err := store.RevokeInvite(ctx, "inv_nope", testNow); !errors.Is(err, registration.ErrInviteNotFound) {
			t.Fatalf("不存在的标识应报找不到，实际 %v", err)
		}

		if err := store.PutInvite(ctx, registration.Invite{
			ID: "inv_a", CodeHash: "hash-a", CreatedBySubjectID: "usr_admin", CreatedAt: testNow,
		}); err != nil {
			t.Fatalf("写入邀请码失败: %v", err)
		}
		first, err := store.RevokeInvite(ctx, "inv_a", testNow)
		if err != nil {
			t.Fatalf("撤销失败: %v", err)
		}
		later := testNow.Add(time.Hour)
		second, err := store.RevokeInvite(ctx, "inv_a", later)
		if err != nil {
			t.Fatalf("重复撤销应当是幂等的成功，实际 %v", err)
		}
		if !second.RevokedAt.Equal(first.RevokedAt) {
			t.Errorf("重复撤销不该改写时间戳：%v → %v", first.RevokedAt, second.RevokedAt)
		}
	})
}

// newTestStore 打开一个临时目录里的库，并跑完迁移。
//
// 它走的是服务端启动时的同一条路径（连接 → 迁移），因此"表建出来了吗"这件事
// 在测试里是**真的**被验证的。
func newTestStore(t *testing.T) *Store {
	t.Helper()
	db, err := database.Open(config.DatabaseConfig{
		Driver: string(database.DialectSQLite),
		DSN:    filepath.Join(t.TempDir(), "registration.db"),
	}, zap.NewNop())
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	if err := migrate.Run(context.Background(), db, zap.NewNop()); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	return New(db)
}
