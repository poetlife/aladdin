package migrate

import (
	"gorm.io/gorm"

	"github.com/go-gormigrate/gormigrate/v2"

	"github.com/poetlife/aladdin/internal/database"
)

// migration0009GalaxyAssetMetadata 给资产加"说明层"元数据：资产表增展示标题与
// 备注两列，并新增一张标签表。
//
// **它是可加的，不动已有行**：存量资产的标题与备注为空、没有标签行，与"这个
// 资产还没有这些信息"完全等价（见 docs/design/galaxy/asset-library.md）。
//
// 与 0007 不同，它**不删表**：字节层（摘要、媒体类型、类别、字节数）在这一版
// 没有变化，而说明层的新字段可以就地补空值。
//
// **序号是 0009 而不是 0008**：它写出来时 main 上已经有一条 0008（scopes 表，
// 见 migration_0008_scopes_table.go）。两边各自占了 0008，而那条已经发布出去了，
// 因此让位的是这一条——序号与执行顺序一致，且已发布的迁移一个字都不动。
//
// **已经发布过的迁移不得修改。** 需要再改结构时追加新的。
var migration0009GalaxyAssetMetadata = &gormigrate.Migration{
	ID: "0009_galaxy_asset_metadata",
	Migrate: func(tx *gorm.DB) error {
		return tx.AutoMigrate(
			&database.GalaxyAssetRecord{},
			&database.GalaxyAssetTagRecord{},
		)
	},
}
