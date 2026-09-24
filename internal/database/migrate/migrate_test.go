package migrate

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/poetlife/aladdin/internal/config"
	"github.com/poetlife/aladdin/internal/database"
)

// openTestDB 开一个临时 sqlite 库，测试结束自动关闭。
//
// 用文件而不是内存库：迁移要验证的正是"落盘之后还在不在"，
// 用内存库等于把待验证的性质从夹具里拿掉了。
func openTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	// t.TempDir 在测试结束时被删除，Cleanup 按后进先出执行，
	// 因此这里的关闭先于目录删除——否则 WAL 文件会挡住删除。
	db, err := database.Open(config.DatabaseConfig{
		Driver: string(database.DialectSQLite),
		DSN:    filepath.Join(t.TempDir(), "migrate.db"),
	}, zap.NewNop())
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	return db
}

func TestRunCreatesSchema(t *testing.T) {
	db := openTestDB(t)

	if err := Run(context.Background(), db, zap.NewNop()); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}

	for _, table := range []string{"roles", "subjects", "role_bindings", migrationTableName} {
		if !db.Migrator().HasTable(table) {
			t.Errorf("迁移后表 %q 仍不存在", table)
		}
	}
}

// 迁移必须幂等：服务端每次启动都会跑一遍，而"第二次启动就报错"等于
// 服务只能起一次。运行两次之后，版本表里也不该出现重复行。
func TestRunIsIdempotent(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	if err := Run(ctx, db, zap.NewNop()); err != nil {
		t.Fatalf("首次迁移失败: %v", err)
	}
	first, err := appliedMigrationIDs(db)
	if err != nil {
		t.Fatalf("读取版本表失败: %v", err)
	}

	if err := Run(ctx, db, zap.NewNop()); err != nil {
		t.Fatalf("二次迁移失败: %v", err)
	}
	second, err := appliedMigrationIDs(db)
	if err != nil {
		t.Fatalf("读取版本表失败: %v", err)
	}

	if strings.Join(first, ",") != strings.Join(second, ",") {
		t.Errorf("两次迁移后的版本记录不一致：%v → %v", first, second)
	}
	if len(second) != len(migrations()) {
		t.Errorf("版本记录 %v 与声明清单 %d 条不符", second, len(migrations()))
	}
}

// 库里存在代码里没有的版本时拒绝启动：通常意味着二进制被回滚而库没有。
// 带着一个自己不认识的库结构继续服务，比停机危险得多。
func TestRunRejectsUnknownVersion(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	if err := Run(ctx, db, zap.NewNop()); err != nil {
		t.Fatalf("首次迁移失败: %v", err)
	}
	// 直接往版本表里塞一条本代码不认识的记录，模拟"库比二进制新"。
	if err := db.Exec(fmt.Sprintf(
		"INSERT INTO %s (id) VALUES (?)", migrationTableName), "9999_from_the_future").Error; err != nil {
		t.Fatalf("写入未知版本失败: %v", err)
	}

	if err := Run(ctx, db, zap.NewNop()); err == nil {
		t.Error("库里存在未知版本时应拒绝迁移")
	}
}

// 迁移清单本身的两条不变式。gormigrate 在运行时会检查重复与保留 ID，
// 但那时错误发生在启动路径上；在这里提前失败，问题就落在改动它的那个人身上。
func TestMigrationListIsWellFormed(t *testing.T) {
	seen := map[string]bool{}
	for i, m := range migrations() {
		if m.ID == "" {
			t.Errorf("第 %d 条迁移没有 ID", i)
			continue
		}
		if seen[m.ID] {
			t.Errorf("迁移 ID %q 重复", m.ID)
		}
		seen[m.ID] = true
		if m.Migrate == nil {
			t.Errorf("迁移 %q 没有迁移体", m.ID)
		}
	}
	if len(migrations()) == 0 {
		t.Fatal("迁移清单为空——一个空清单会让版本表永远建不出来")
	}
}
