package gormstore

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/poetlife/aladdin/internal/config"
	"github.com/poetlife/aladdin/internal/database"
	"github.com/poetlife/aladdin/internal/database/migrate"
	"github.com/poetlife/aladdin/internal/identity"
	"github.com/poetlife/aladdin/internal/rbac"
)

// 两个会话存储实现共用同一套用例。
//
// 这是本仓库"同一件事只有一个实现"之外的另一半：**同一份契约只能有一套
// 用例**。给 gorm 实现单独写一套，两套就会各自漂移，而漂移的表现形式是
// "换后端之后行为变了"——最难在事后归因的一类问题。
//
// 用例放在 gormstore 包内而不是 internal/identity：后者若导入 gormstore
// 会形成导入环（gormstore 依赖 identity）。

// testNow 是所有用例的时间基准。
//
// 时间取整秒：不同的后端对时间列的精度与存储格式各有做法，用例要冻结的是
// **语义**，不是某个驱动的小数位。真的需要亚秒精度时那是一条独立的用例，
// 而不是让每条用例都跟着驱动的实现细节走。
var testNow = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// testSession 造一份会话，过期时间由调用方给出。
func testSession(expiresAt time.Time) identity.Session {
	return identity.Session{
		SubjectID:    "google:109876543210987654321",
		SubjectType:  rbac.SubjectTypeUser,
		DefaultScope: "tenant/acme",
		IssuedAt:     testNow,
		ExpiresAt:    expiresAt,
	}
}

// storeCase 是一个会话存储实现的构造方式。
type storeCase struct {
	name string
	open func(t *testing.T) identity.SessionStore
}

func storeCases(t *testing.T) []storeCase {
	t.Helper()
	return []storeCase{
		{
			name: "内存实现",
			open: func(*testing.T) identity.SessionStore { return identity.NewMemoryStore() },
		},
		{
			name: "关系库实现",
			open: func(t *testing.T) identity.SessionStore { return New(newTestDB(t, newTestDBPath(t))) },
		},
	}
}

// forEachStore 把同一段用例跑在两个实现上。
func forEachStore(t *testing.T, run func(t *testing.T, store identity.SessionStore)) {
	t.Helper()
	for _, sc := range storeCases(t) {
		t.Run(sc.name, func(t *testing.T) {
			run(t, sc.open(t))
		})
	}
}

// 找不到会话返回 ErrSessionNotFound：它同时代表"不存在"、"已撤销"与"已过期"。
func TestContractMissingSession(t *testing.T) {
	forEachStore(t, func(t *testing.T, store identity.SessionStore) {
		_, err := store.Get(context.Background(), identity.HashToken("从不存在的凭证"))
		if !errors.Is(err, identity.ErrSessionNotFound) {
			t.Errorf("err = %v，期望 ErrSessionNotFound", err)
		}
	})
}

// 会话必须整份原样往返：少一个字段，认证拿到的主体就少一项属性，
// 而现象是"登录之后界面不对"，与存储层看起来毫无关系。
func TestContractSessionRoundTrip(t *testing.T) {
	forEachStore(t, func(t *testing.T, store identity.SessionStore) {
		ctx := context.Background()
		want := testSession(testNow.Add(24 * time.Hour))
		hash := identity.HashToken("一份凭证")

		if err := store.Put(ctx, hash, want); err != nil {
			t.Fatalf("写入会话失败: %v", err)
		}
		got, err := store.Get(ctx, hash)
		if err != nil {
			t.Fatalf("读取会话失败: %v", err)
		}
		assertSameSession(t, got, want)
	})
}

// 撤销是删除，且立即生效：撤销之后同一个摘要再也读不到。
func TestContractDeleteRemovesSession(t *testing.T) {
	forEachStore(t, func(t *testing.T, store identity.SessionStore) {
		ctx := context.Background()
		hash := identity.HashToken("一份凭证")
		if err := store.Put(ctx, hash, testSession(testNow.Add(time.Hour))); err != nil {
			t.Fatalf("写入会话失败: %v", err)
		}
		if err := store.Delete(ctx, hash); err != nil {
			t.Fatalf("撤销会话失败: %v", err)
		}
		if _, err := store.Get(ctx, hash); !errors.Is(err, identity.ErrSessionNotFound) {
			t.Errorf("撤销后 err = %v，期望 ErrSessionNotFound", err)
		}
	})
}

// 撤销是幂等的：重复撤销与撤销一份本来就没有的凭证，结果一致。
func TestContractDeleteIsIdempotent(t *testing.T) {
	forEachStore(t, func(t *testing.T, store identity.SessionStore) {
		ctx := context.Background()
		hash := identity.HashToken("一份凭证")
		if err := store.Put(ctx, hash, testSession(testNow.Add(time.Hour))); err != nil {
			t.Fatalf("写入会话失败: %v", err)
		}

		for range 2 {
			if err := store.Delete(ctx, hash); err != nil {
				t.Fatalf("撤销失败: %v", err)
			}
		}
		if err := store.Delete(ctx, identity.HashToken("从不存在的凭证")); err != nil {
			t.Errorf("撤销不存在的凭证 err = %v，期望 nil", err)
		}
	})
}

// 撤销只影响指定的那一条，不误伤其他会话。
func TestContractDeleteIsNarrow(t *testing.T) {
	forEachStore(t, func(t *testing.T, store identity.SessionStore) {
		ctx := context.Background()
		target, other := identity.HashToken("要撤销的"), identity.HashToken("要留下的")
		for hash, session := range map[string]identity.Session{
			target:                     testSession(testNow.Add(time.Hour)),
			other:                      testSession(testNow.Add(2 * time.Hour)),
			identity.HashToken("已过期的"): testSession(testNow.Add(-time.Hour)),
		} {
			if err := store.Put(ctx, hash, session); err != nil {
				t.Fatalf("写入会话失败: %v", err)
			}
		}

		if err := store.Delete(ctx, target); err != nil {
			t.Fatalf("撤销失败: %v", err)
		}
		if _, err := store.Get(ctx, target); !errors.Is(err, identity.ErrSessionNotFound) {
			t.Errorf("被撤销的会话 err = %v，期望 ErrSessionNotFound", err)
		}
		if _, err := store.Get(ctx, other); err != nil {
			t.Errorf("其他会话不应受影响，err = %v", err)
		}
	})
}

// 回收只删已过期的行，且在**恰好到点**这一刻就删。
//
// 边界与 identity.Session.Expired 必须一致：那边是"到点即失效"，
// 这边就不能留一个"还差一毫秒"的缝。
func TestContractDeleteExpiredBoundary(t *testing.T) {
	forEachStore(t, func(t *testing.T, store identity.SessionStore) {
		ctx := context.Background()
		cases := map[string]struct {
			expiresAt time.Time
			expired   bool
		}{
			"早已过期": {testNow.Add(-time.Hour), true},
			"恰好到点": {testNow, true},
			"还差一秒": {testNow.Add(time.Second), false},
			"很久以后": {testNow.Add(24 * time.Hour), false},
		}
		for name, c := range cases {
			if err := store.Put(ctx, identity.HashToken(name), testSession(c.expiresAt)); err != nil {
				t.Fatalf("写入会话失败: %v", err)
			}
		}

		removed, err := store.DeleteExpired(ctx, testNow)
		if err != nil {
			t.Fatalf("回收失败: %v", err)
		}
		if removed != 2 {
			t.Errorf("回收条数 = %d，期望 2（早已过期 + 恰好到点）", removed)
		}
		for name, c := range cases {
			_, err := store.Get(ctx, identity.HashToken(name))
			switch {
			case c.expired && !errors.Is(err, identity.ErrSessionNotFound):
				t.Errorf("%s 应被回收，err = %v", name, err)
			case !c.expired && err != nil:
				t.Errorf("%s 不应被回收，err = %v", name, err)
			}
		}
	})
}

// 新库上的回收是空操作，而不是错误——服务端每次启动都会调它。
func TestContractDeleteExpiredOnEmptyStore(t *testing.T) {
	forEachStore(t, func(t *testing.T, store identity.SessionStore) {
		removed, err := store.DeleteExpired(context.Background(), testNow)
		if err != nil {
			t.Fatalf("空存储回收失败: %v", err)
		}
		if removed != 0 {
			t.Errorf("回收条数 = %d，期望 0", removed)
		}
	})
}

// assertSameSession 逐字段比对会话。
//
// 不用 reflect.DeepEqual：time.Time 的相等还包含单调时钟与位置信息，
// 而那两样是**驱动的实现细节**，不是这份契约的一部分——拿它做断言，
// 换一个后端就会失败，失败的原因却与"会话存得对不对"无关。
func assertSameSession(t *testing.T, got, want identity.Session) {
	t.Helper()
	if got.SubjectID != want.SubjectID {
		t.Errorf("SubjectID = %q，期望 %q", got.SubjectID, want.SubjectID)
	}
	if got.SubjectType != want.SubjectType {
		t.Errorf("SubjectType = %q，期望 %q", got.SubjectType, want.SubjectType)
	}
	if got.DefaultScope != want.DefaultScope {
		t.Errorf("DefaultScope = %q，期望 %q", got.DefaultScope, want.DefaultScope)
	}
	if !got.IssuedAt.Equal(want.IssuedAt) {
		t.Errorf("IssuedAt = %v，期望 %v", got.IssuedAt, want.IssuedAt)
	}
	if !got.ExpiresAt.Equal(want.ExpiresAt) {
		t.Errorf("ExpiresAt = %v，期望 %v", got.ExpiresAt, want.ExpiresAt)
	}
}

// newTestDBPath 给出一个临时目录里的库文件路径。
//
// 用文件而不是内存库：内存库只在单连接内有效，而"重启后仍然有效"这条
// 性质必须以一个**能重新打开的文件**为前提（见下一条用例）。
func newTestDBPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "identity.db")
}

// newTestDB 打开库并完成迁移，返回可直接构造存储的连接。
func newTestDB(t *testing.T, path string) *gorm.DB {
	t.Helper()
	db, err := database.Open(config.DatabaseConfig{
		Driver: string(database.DialectSQLite),
		DSN:    path,
	}, zap.NewNop())
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	if err := migrate.Run(context.Background(), db, zap.NewNop()); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	t.Cleanup(func() { closeTestDB(t, db) })
	return db
}

// closeTestDB 释放连接池。
//
// 必须真的关掉：sqlite 的写锁是文件级的，上一个连接不关，下一个连接
// 打开同一个文件时会在写入上卡住——而现象是测试"偶尔超时"。
func closeTestDB(t *testing.T, db *gorm.DB) {
	t.Helper()
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("获取连接池失败: %v", err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatalf("关闭连接池失败: %v", err)
	}
}
