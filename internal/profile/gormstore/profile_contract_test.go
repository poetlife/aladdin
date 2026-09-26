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
	"github.com/poetlife/aladdin/internal/profile"
)

// 两个档案存储实现共用同一套用例。
//
// 理由与会话、身份别名的契约测试相同：**同一份契约只能有一套用例**。给 gorm
// 实现单独写一套，两套就会各自漂移，而漂移的表现形式是"换后端之后，改一次
// 昵称把头像弄没了"。

// profileCase 是一个档案存储实现的构造方式。
type profileCase struct {
	name string
	open func(t *testing.T) profile.Store
}

func profileCases(t *testing.T) []profileCase {
	t.Helper()
	return []profileCase{
		{
			name: "内存实现",
			open: func(*testing.T) profile.Store { return profile.NewMemoryStore() },
		},
		{
			name: "关系库实现",
			open: func(t *testing.T) profile.Store {
				return New(newProfileTestDB(t, filepath.Join(t.TempDir(), "profile.db")))
			},
		},
	}
}

// forEachProfileStore 把同一段用例跑在两个实现上。
func forEachProfileStore(t *testing.T, run func(t *testing.T, store profile.Store)) {
	t.Helper()
	for _, pc := range profileCases(t) {
		t.Run(pc.name, func(t *testing.T) {
			run(t, pc.open(t))
		})
	}
}

func testTime() time.Time { return time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC) }

// 没有档案行时返回 ErrProfileNotFound，而不是一个空档案。
//
// 它与"存储不可用"必须可区分：前者是正常状态（档案行是惰性创建的），后者
// 必须让请求快速失败。
func TestProfileContractMissing(t *testing.T) {
	forEachProfileStore(t, func(t *testing.T, store profile.Store) {
		_, err := store.Get(context.Background(), "从不存在的主体")
		if !errors.Is(err, profile.ErrProfileNotFound) {
			t.Errorf("err = %v，期望 ErrProfileNotFound", err)
		}
	})
}

// 写入之后再读回，字段一字不差。
func TestProfileContractRoundTrip(t *testing.T) {
	forEachProfileStore(t, func(t *testing.T, store profile.Store) {
		ctx := context.Background()
		at := testTime()
		if err := store.PutText(ctx, "usr_a", "阿拉丁", "一盏灯", at); err != nil {
			t.Fatalf("写入失败: %v", err)
		}

		got, err := store.Get(ctx, "usr_a")
		if err != nil {
			t.Fatalf("读取失败: %v", err)
		}
		if got.SubjectID != "usr_a" || got.Nickname != "阿拉丁" || got.Bio != "一盏灯" {
			t.Errorf("读回 = %+v，与写入的不一致", got)
		}
		if !got.UpdatedAt.Equal(at) {
			t.Errorf("UpdatedAt = %v，期望 %v", got.UpdatedAt, at)
		}
	})
}

// 档案行是惰性创建的：只有写入才产生行，且一个主体只有一行。
func TestProfileContractLazyRow(t *testing.T) {
	forEachProfileStore(t, func(t *testing.T, store profile.Store) {
		ctx := context.Background()

		if _, err := store.Get(ctx, "usr_a"); !errors.Is(err, profile.ErrProfileNotFound) {
			t.Fatalf("读一个没写过的主体 = %v，期望 ErrProfileNotFound", err)
		}

		at := testTime()
		if err := store.PutText(ctx, "usr_a", "第一次", "", at); err != nil {
			t.Fatalf("写入失败: %v", err)
		}
		// 再写一次同一主体：应当是覆盖，不是新增。
		if err := store.PutText(ctx, "usr_a", "第二次", "", at); err != nil {
			t.Fatalf("二次写入失败: %v", err)
		}

		got, err := store.Get(ctx, "usr_a")
		if err != nil {
			t.Fatalf("读取失败: %v", err)
		}
		if got.Nickname != "第二次" {
			t.Errorf("Nickname = %q，期望覆盖为 %q", got.Nickname, "第二次")
		}
	})
}

// **改名不清头像。** 这是 Store 按字段分成两个方法所保护的那条性质：
// 一个覆盖整行的接口会让一次改名把头像对象键擦掉。
func TestProfileContractPutTextKeepsAvatarKey(t *testing.T) {
	forEachProfileStore(t, func(t *testing.T, store profile.Store) {
		ctx := context.Background()
		at := testTime()

		if err := store.PutAvatarKey(ctx, "usr_a", "avatars/usr_a", at); err != nil {
			t.Fatalf("写入头像键失败: %v", err)
		}
		if err := store.PutText(ctx, "usr_a", "改了名", "", at.Add(time.Minute)); err != nil {
			t.Fatalf("写入昵称失败: %v", err)
		}

		got, err := store.Get(ctx, "usr_a")
		if err != nil {
			t.Fatalf("读取失败: %v", err)
		}
		if got.AvatarKey != "avatars/usr_a" {
			t.Errorf("AvatarKey = %q，期望改名后仍是 %q", got.AvatarKey, "avatars/usr_a")
		}
		if got.Nickname != "改了名" {
			t.Errorf("Nickname = %q，期望 %q", got.Nickname, "改了名")
		}
	})
}

// **换头像不动昵称与简介。** 与上一条对称。
func TestProfileContractPutAvatarKeepsText(t *testing.T) {
	forEachProfileStore(t, func(t *testing.T, store profile.Store) {
		ctx := context.Background()
		at := testTime()

		if err := store.PutText(ctx, "usr_a", "阿拉丁", "一盏灯", at); err != nil {
			t.Fatalf("写入昵称失败: %v", err)
		}
		if err := store.PutAvatarKey(ctx, "usr_a", "avatars/usr_a", at.Add(time.Minute)); err != nil {
			t.Fatalf("写入头像键失败: %v", err)
		}

		got, err := store.Get(ctx, "usr_a")
		if err != nil {
			t.Fatalf("读取失败: %v", err)
		}
		if got.Nickname != "阿拉丁" || got.Bio != "一盏灯" {
			t.Errorf("换头像后 = %+v，昵称与简介不该变", got)
		}
	})
}

// 空键表示清除头像，昵称与简介不受影响。
func TestProfileContractClearAvatarKey(t *testing.T) {
	forEachProfileStore(t, func(t *testing.T, store profile.Store) {
		ctx := context.Background()
		at := testTime()

		if err := store.PutText(ctx, "usr_a", "阿拉丁", "", at); err != nil {
			t.Fatalf("写入昵称失败: %v", err)
		}
		if err := store.PutAvatarKey(ctx, "usr_a", "avatars/usr_a", at); err != nil {
			t.Fatalf("写入头像键失败: %v", err)
		}
		if err := store.PutAvatarKey(ctx, "usr_a", "", at.Add(time.Minute)); err != nil {
			t.Fatalf("清除头像键失败: %v", err)
		}

		got, err := store.Get(ctx, "usr_a")
		if err != nil {
			t.Fatalf("读取失败: %v", err)
		}
		if got.AvatarKey != "" {
			t.Errorf("AvatarKey = %q，期望为空", got.AvatarKey)
		}
		if got.Nickname != "阿拉丁" {
			t.Errorf("Nickname = %q，期望清除头像不影响昵称", got.Nickname)
		}
	})
}

// 昵称没有唯一约束：两个主体可以同名。
//
// 这条不是"顺便测一下"，而是**昵称不是身份**在存储层的落点：一旦这里出现
// 唯一性，"按昵称查人"就会成为一个可实现的查询（见
// docs/design/profile/README.md 的"边界与约束"）。
func TestProfileContractNicknameIsNotUnique(t *testing.T) {
	forEachProfileStore(t, func(t *testing.T, store profile.Store) {
		ctx := context.Background()
		at := testTime()

		if err := store.PutText(ctx, "usr_a", "同名", "", at); err != nil {
			t.Fatalf("写入 usr_a 失败: %v", err)
		}
		if err := store.PutText(ctx, "usr_b", "同名", "", at); err != nil {
			t.Fatalf("第二个主体用同一昵称写入失败: %v", err)
		}

		for _, id := range []string{"usr_a", "usr_b"} {
			got, err := store.Get(ctx, id)
			if err != nil {
				t.Fatalf("读取 %s 失败: %v", id, err)
			}
			if got.Nickname != "同名" {
				t.Errorf("%s 的 Nickname = %q，期望 %q", id, got.Nickname, "同名")
			}
		}
	})
}

// 清空昵称与简介：空值就是"未设置"，写进去之后读回来仍是空。
func TestProfileContractClearText(t *testing.T) {
	forEachProfileStore(t, func(t *testing.T, store profile.Store) {
		ctx := context.Background()
		at := testTime()

		if err := store.PutText(ctx, "usr_a", "阿拉丁", "一盏灯", at); err != nil {
			t.Fatalf("写入失败: %v", err)
		}
		if err := store.PutText(ctx, "usr_a", "", "", at.Add(time.Minute)); err != nil {
			t.Fatalf("清空失败: %v", err)
		}

		got, err := store.Get(ctx, "usr_a")
		if err != nil {
			t.Fatalf("读取失败: %v", err)
		}
		if got.Nickname != "" || got.Bio != "" {
			t.Errorf("清空后 = %+v，期望两个字段都为空", got)
		}
	})
}

// newProfileTestDB 打开库并完成迁移，返回可直接构造存储的连接。
//
// 用文件而不是内存库：内存库只在单连接内有效，而契约测试要比对的是"写进去
// 的东西还在不在"，那需要一个真实的库文件。
func newProfileTestDB(t *testing.T, path string) *gorm.DB {
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
	t.Cleanup(func() {
		sqlDB, err := db.DB()
		if err != nil {
			t.Fatalf("获取连接池失败: %v", err)
		}
		// 必须真的关掉：sqlite 的写锁是文件级的，上一个连接不关，下一个连接
		// 打开同一个文件时会在写入上卡住——而现象是测试"偶尔超时"。
		if err := sqlDB.Close(); err != nil {
			t.Fatalf("关闭连接池失败: %v", err)
		}
	})
	return db
}
