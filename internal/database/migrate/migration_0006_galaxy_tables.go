package migrate

import (
	"gorm.io/gorm"

	"github.com/go-gormigrate/gormigrate/v2"

	"github.com/poetlife/aladdin/internal/database"
)

// migration0006GalaxyTables 建立 galaxy 的五张表。
//
// **已发布，不可修改。** 需要改结构时追加新的迁移，见 migrations.go。
//
// 五张表一起建，而不是按子模块拆成五条迁移：它们由同一次功能落地引入，之间
// 的引用（版本与发布都指向工程）只有在同一版结构里才成立。拆开只会让"迁移到
// 一半"变成一个需要解释的中间状态。
//
// 草稿与版本**分表**，不在工程行上放一列：草稿可覆盖写，版本不可变，而两张表
// 让不可变性由表的存在方式表达。工程行上只有一个可空的发布指针，正文一律不在
// 它上面（见 docs/design/persistence/schema.md）。
//
// 所有行都是**惰性创建**的：建工程不写草稿行、不上传就不写资产行。因此本迁移
// 不涉及任何回填。
//
// 表由 galaxy 模块拥有，但建表入口在这里、不在 internal/galaxy —— 表结构与
// 迁移机制是全库共用的基础设施，各模块只拥有自己的表（见
// docs/design/persistence/README.md 的约束）。
var migration0006GalaxyTables = &gormigrate.Migration{
	ID: "0006_galaxy_tables",
	Migrate: func(tx *gorm.DB) error {
		return tx.AutoMigrate(
			&database.GalaxyProjectRecord{},
			&database.GalaxyDraftRecord{},
			&database.GalaxyVersionRecord{},
			&database.GalaxyAssetRecord{},
			&database.GalaxyPublicationRecord{},
		)
	},
}
