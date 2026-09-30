package migrate

import (
	"gorm.io/gorm"

	"github.com/go-gormigrate/gormigrate/v2"

	"github.com/poetlife/aladdin/internal/database"
)

// migration0012ClientEvents 建立遥测模块的客户端事件表。
//
// **已发布，不可修改。** 需要改结构时追加新的迁移，见 migrations.go。
//
// 它只有一张表，且是纯新增——没有任何存量行要搬：事件在此之前只落成日志，库里
// 没有对应数据。因此这条迁移是"建表，结束"，与 0002 同形。
//
// 事件表由遥测模块拥有，但建表入口在这里、不在 internal/telemetry —— 表结构与
// 迁移机制是全库共用的基础设施，各模块只拥有自己的表
// （见 docs/design/persistence/README.md 的约束）。
var migration0012ClientEvents = &gormigrate.Migration{
	ID: "0012_client_events",
	Migrate: func(tx *gorm.DB) error {
		return tx.AutoMigrate(&database.ClientEventRecord{})
	},
}
