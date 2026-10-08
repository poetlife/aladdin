package migrate

import (
	"context"
	"testing"
	"time"

	"go.uber.org/zap"
)

// 存量库上的回填：**旧列还在、里面有值**时，那一张要变成图集里的第一张，而桶上的
// 对象一个都不搬——回填行留的仍然是旧键。
//
// 这一条只能这样造：迁移体用的是 AutoMigrate，建表用的是**当前的**模型，而当前模型
// 里已经没有 `cover_key` 了，因此一个全新的库上那一列从来不曾存在（见
// migration_0015_skill_images.go）。要走到回填那条路，就得手工把那一列与一条存量行
// 补出来，再把版本表里的 0015 抹掉让它重跑一遍。
func TestMigration0015BackfillsLegacyCover(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	if err := Run(ctx, db, zap.NewNop()); err != nil {
		t.Fatalf("首次迁移失败: %v", err)
	}

	// 造一个"封面时代"的库：那一列、以及一条带封面的存量技能行。
	if err := db.Exec("ALTER TABLE skills ADD COLUMN cover_key text").Error; err != nil {
		t.Fatalf("补出旧列失败: %v", err)
	}
	now := time.Now()
	if err := db.Exec(
		"INSERT INTO skills (id, cover_key, created_at, updated_at) VALUES (?, ?, ?, ?)",
		"skl_legacy", "skills/cover/skl_legacy", now, now,
	).Error; err != nil {
		t.Fatalf("插入存量技能失败: %v", err)
	}
	if err := db.Exec("DELETE FROM schema_migrations WHERE id = ?", "0015_skill_images").Error; err != nil {
		t.Fatalf("抹掉迁移记录失败: %v", err)
	}

	if err := Run(ctx, db, zap.NewNop()); err != nil {
		t.Fatalf("重跑迁移失败: %v", err)
	}

	var row struct {
		ID        string
		SkillID   string
		Position  int
		ObjectKey string
	}
	if err := db.Table("skill_images").Where("skill_id = ?", "skl_legacy").Take(&row).Error; err != nil {
		t.Fatalf("存量封面没有变成图集里的一张: %v", err)
	}
	if row.ID == "" {
		t.Error("回填行没有图标识")
	}
	// **键照搬**：迁移碰不到对象存储，搬键就等于把存量封面弄丢。
	if row.ObjectKey != "skills/cover/skl_legacy" {
		t.Errorf("回填行的对象键 = %q，期望照搬旧键", row.ObjectKey)
	}
	if row.Position != 0 {
		t.Errorf("回填行的位置 = %d，期望 0（它即首图）", row.Position)
	}
	if hasColumn(db, "skills", "cover_key") {
		t.Error("旧列还在：图集与旧列并存就是第二份事实")
	}
}

// 空值不产生图集行：存量技能里没有封面的那些，迁移之后仍然是"没有图"。
func TestMigration0015SkipsSkillsWithoutCover(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	if err := Run(ctx, db, zap.NewNop()); err != nil {
		t.Fatalf("首次迁移失败: %v", err)
	}
	if err := db.Exec("ALTER TABLE skills ADD COLUMN cover_key text").Error; err != nil {
		t.Fatalf("补出旧列失败: %v", err)
	}
	now := time.Now()
	for _, id := range []string{"skl_null", "skl_empty"} {
		if err := db.Exec(
			"INSERT INTO skills (id, created_at, updated_at) VALUES (?, ?, ?)", id, now, now,
		).Error; err != nil {
			t.Fatalf("插入存量技能失败: %v", err)
		}
	}
	if err := db.Exec("UPDATE skills SET cover_key = '' WHERE id = ?", "skl_empty").Error; err != nil {
		t.Fatalf("写入空封面失败: %v", err)
	}
	if err := db.Exec("DELETE FROM schema_migrations WHERE id = ?", "0015_skill_images").Error; err != nil {
		t.Fatalf("抹掉迁移记录失败: %v", err)
	}
	if err := Run(ctx, db, zap.NewNop()); err != nil {
		t.Fatalf("重跑迁移失败: %v", err)
	}

	var count int64
	if err := db.Table("skill_images").Count(&count).Error; err != nil {
		t.Fatalf("读取图集失败: %v", err)
	}
	if count != 0 {
		t.Errorf("没有封面的技能产生了 %d 行图集", count)
	}
}
