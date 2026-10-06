package migrate

import (
	"gorm.io/gorm"

	"github.com/go-gormigrate/gormigrate/v2"

	"github.com/poetlife/aladdin/internal/database"
)

// migration0014SkillCover 给技能表加上封面那一列。
//
// **已发布，不可修改。** 需要改结构时追加新的迁移，见 migrations.go。
//
// 它是纯新增、且只有一个可空列：存量技能没有封面，空值就是它们的正确状态。**不
// 追加到 0013 里**——那一条已经在别人的库上应用过了，改它不会改变那些库，只会让
// 新库与旧库长成两个样子。
var migration0014SkillCover = &gormigrate.Migration{
	ID: "0014_skill_cover",
	Migrate: func(tx *gorm.DB) error {
		return tx.AutoMigrate(&database.SkillRecord{})
	},
}
