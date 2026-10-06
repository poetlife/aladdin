package migrate

import (
	"gorm.io/gorm"

	"github.com/go-gormigrate/gormigrate/v2"

	"github.com/poetlife/aladdin/internal/database"
)

// migration0013SkillCatalog 建立平台技能目录的五张表。
//
// **已发布，不可修改。** 需要改结构时追加新的迁移，见 migrations.go。
//
// 它是纯新增——目录此前不存在，没有任何存量行要搬。因此这条迁移是"建表，结束"，
// 与 0002、0012 同形。
//
// 五张表由 skill 模块拥有，但建表入口在这里、不在 internal/skill —— 表结构与迁移
// 机制是全库共用的基础设施，各模块只拥有自己的表
// （见 docs/design/persistence/README.md 的约束）。
//
// **字节不在其中任何一张表里**：内容是一组「路径 → 内容摘要 + 字节数」，随版本行
// 整份读写；字节按内容摘要存放在对象存储，且跨技能共享。
var migration0013SkillCatalog = &gormigrate.Migration{
	ID: "0013_skill_catalog",
	Migrate: func(tx *gorm.DB) error {
		return tx.AutoMigrate(
			&database.SkillRecord{},
			&database.SkillVersionRecord{},
			&database.SkillTagRecord{},
			&database.SkillFavoriteRecord{},
			&database.SkillUsageDailyRecord{},
		)
	},
}
