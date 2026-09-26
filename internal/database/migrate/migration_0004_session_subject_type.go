package migrate

import (
	"gorm.io/gorm"

	"github.com/go-gormigrate/gormigrate/v2"

	"github.com/poetlife/aladdin/internal/database"
)

// migration0004SessionSubjectType 给会话表补上签发时的主体类型。
//
// **为什么它会单独存在。** 迁移体用的是 AutoMigrate，它读的是**当前**的
// 结构定义。因此往 SessionRecord 上加一个字段，只会让"从头建库"的库多出
// 这一列；已经跑过 0002 的库不会有——那条迁移早就被记成已应用，不会再执行。
// 这正是结构快照测试要拦下的那类偏差，修法是补一条迁移，而不是改 0002。
//
// **已发布，不可修改。** 需要改结构时追加新的迁移，见 migrations.go。
var migration0004SessionSubjectType = &gormigrate.Migration{
	ID: "0004_session_subject_type",
	Migrate: func(tx *gorm.DB) error {
		return tx.AutoMigrate(&database.SessionRecord{})
	},
}
