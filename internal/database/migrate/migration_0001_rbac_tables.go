package migrate

import (
	"gorm.io/gorm"

	"github.com/go-gormigrate/gormigrate/v2"

	"github.com/poetlife/aladdin/internal/database"
)

// migration0001RBACTables 建立 RBAC 的三张业务表。
//
// **已发布，不可修改。** 需要改结构时追加新的迁移，见 migrations.go。
//
// 迁移体用 AutoMigrate 而不是手写 SQL：同一份迁移要能同时在 sqlite 与
// mysql 上跑，把 DDL 交给 gorm 按方言生成是唯一不必维护两套 SQL 的做法。
// 代价是旧迁移的冻结只能靠纪律与结构快照测试来保证——后者会在"改了
// internal/database/schema.go 却没追加迁移"时失败。
var migration0001RBACTables = &gormigrate.Migration{
	ID: "0001_rbac_tables",
	Migrate: func(tx *gorm.DB) error {
		return tx.AutoMigrate(
			&database.RoleRecord{},
			&database.SubjectRecord{},
			&database.RoleBindingRecord{},
		)
	},
}
