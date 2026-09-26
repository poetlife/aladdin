package migrate

import (
	"gorm.io/gorm"

	"github.com/go-gormigrate/gormigrate/v2"

	"github.com/poetlife/aladdin/internal/database"
)

// migration0002SessionTable 建立认证模块的会话表。
//
// **已发布，不可修改。** 需要改结构时追加新的迁移，见 migrations.go。
//
// 会话表由认证模块拥有，但它的建表入口在这里、不在 internal/identity ——
// 表结构与迁移机制是全库共用的基础设施，各模块只拥有自己的表
// （见 docs/design/persistence/README.md 的约束）。
var migration0002SessionTable = &gormigrate.Migration{
	ID: "0002_session_table",
	Migrate: func(tx *gorm.DB) error {
		return tx.AutoMigrate(&database.SessionRecord{})
	},
}
