package migrate

import (
	"gorm.io/gorm"

	"github.com/go-gormigrate/gormigrate/v2"

	"github.com/poetlife/aladdin/internal/database"
)

// migration0003IdentitiesTable 建立认证模块的身份别名表。
//
// **已发布，不可修改。** 需要改结构时追加新的迁移，见 migrations.go。
//
// 这张表让"一个人多个登录渠道"成为同一个主体：登录时按 (来源, 身份标识)
// 查一次，未命中才登记新主体。缺了它，同一个人换个渠道登进来就会变成
// 另一个主体，管理员授的权在他那里"看不见"（见
// docs/design/identity/identity-linking.md）。
//
// 表由认证模块拥有，但建表入口在这里、不在 internal/identity ——
// 表结构与迁移机制是全库共用的基础设施，各模块只拥有自己的表
// （见 docs/design/persistence/README.md 的约束）。
var migration0003IdentitiesTable = &gormigrate.Migration{
	ID: "0003_identities_table",
	Migrate: func(tx *gorm.DB) error {
		return tx.AutoMigrate(&database.IdentityRecord{})
	},
}
