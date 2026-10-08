package migrate

import (
	"time"

	"github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"

	"github.com/poetlife/aladdin/internal/database"
	"github.com/poetlife/aladdin/internal/idgen"
)

// legacyCoverImageIDPrefix 是回填存量的那一张封面时给它分配的图标识前缀。
//
// 这里写死一个字面量，而不是引用 internal/skill 的常量：**迁移是冻结的历史**，
// 它应当永远产生它当时产生的那种标识，而不是跟着之后的重命名走。
const legacyCoverImageIDPrefix = "ski_"

// migration0015SkillImages 把技能的单张封面换成**有序的展示图集**。
//
// 三件事，顺序不能换：
//
//  1. 建 `skill_images` 表；
//  2. **回填**：存量技能上那一张封面变成图集里的第一张。对象键原样留在新行上
//     （旧键是 `skills/cover/<技能标识>`，新键按图标识派生）——**桶上的对象一个都
//     不搬**：迁移碰不到对象存储，而"搬家"的那条路要在桶上复制一遍，失败一次就是
//     一张再也取不到的图；
//  3. **删掉 `skills.cover_key` 那一列**：图集成为"这个技能有哪些展示图"的唯一
//     答案，留着旧列就是第二份事实，而两份事实迟早会不一致。
//
// **第 2、3 步以"那一列在不在"为前提。** 迁移体用的是 AutoMigrate，建表用的是
// **当前的**模型——而当前模型里已经没有 `cover_key` 了，因此一个全新的库上那一列
// 从来不曾存在（见 schema_snapshot_test.go 关于这件事的说明）。存量库上它还在，
// 那才是要回填与删除的那一种。少了这一道判断，新库会在建表之后的回填那一步报
// "no such column"。
//
// 判在不在、以及删列，都走 0011 立下的那两条规矩（**不用 gorm 的
// `Migrator().HasColumn` / `DropColumn`**）：前者在 sqlite 上答不出列清单，后者的
// 行为依赖驱动——字段已经不在模型里时它会**静默不删**。这一处正是那种字段，
// 因此删列是一句显式的 DDL，列在不在由 `hasColumn` 回答。
//
// 回填行的 `size_bytes` 记 0：旧列里没有字节数，而迁移看不见对象存储。它的后果只是
// 这一张暂时不参与合计上限的计算，换一次图就会被纠正为真实值。
//
// **已发布，不可修改。** 需要改结构时追加新的迁移，见 migrations.go。
var migration0015SkillImages = &gormigrate.Migration{
	ID: "0015_skill_images",
	Migrate: func(tx *gorm.DB) error {
		if err := tx.AutoMigrate(&database.SkillImageRecord{}); err != nil {
			return err
		}
		if !hasColumn(tx, (database.SkillRecord{}).TableName(), "cover_key") {
			return nil
		}
		var covers []struct {
			ID       string
			CoverKey string
		}
		if err := tx.Table((database.SkillRecord{}).TableName()).
			Select("id, cover_key").
			Where("cover_key IS NOT NULL AND cover_key <> ''").
			Scan(&covers).Error; err != nil {
			return err
		}
		now := time.Now()
		for _, cover := range covers {
			imageID, err := idgen.New(legacyCoverImageIDPrefix)
			if err != nil {
				return err
			}
			record := database.SkillImageRecord{
				ID:        imageID,
				SkillID:   cover.ID,
				Position:  0,
				ObjectKey: cover.CoverKey,
				CreatedAt: now,
			}
			if err := tx.Create(&record).Error; err != nil {
				return err
			}
		}
		return tx.Exec("ALTER TABLE " + (database.SkillRecord{}).TableName() + " DROP COLUMN cover_key").Error
	},
}
