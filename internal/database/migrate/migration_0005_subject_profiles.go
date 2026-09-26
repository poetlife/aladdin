package migrate

import (
	"gorm.io/gorm"

	"github.com/go-gormigrate/gormigrate/v2"

	"github.com/poetlife/aladdin/internal/database"
)

// migration0005SubjectProfiles 建立个人档案表。
//
// **已发布，不可修改。** 需要改结构时追加新的迁移，见 migrations.go。
//
// 它是**独立于主体表**的一张表，而不是主体表上多出来的几列：主体表是整行
// 覆盖写入的（登录登记、引导、开发种子都走同一条路径），昵称落在那一行上
// 会被任何一次主体写入顺手擦成空值；而档案是纯展示数据，判定路径一个字节
// 都不需要它（见 docs/design/profile/README.md）。
//
// 行是惰性创建的：主体登记时不写这一行，本人第一次保存档案才写入。因此本
// 迁移不涉及任何回填。
//
// 表由个人档案模块拥有，但建表入口在这里、不在 internal/profile ——
// 表结构与迁移机制是全库共用的基础设施，各模块只拥有自己的表
// （见 docs/design/persistence/README.md 的约束）。
var migration0005SubjectProfiles = &gormigrate.Migration{
	ID: "0005_subject_profiles",
	Migrate: func(tx *gorm.DB) error {
		return tx.AutoMigrate(&database.SubjectProfileRecord{})
	},
}
