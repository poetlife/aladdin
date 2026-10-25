package migrate

import (
	"gorm.io/gorm"

	"github.com/go-gormigrate/gormigrate/v2"

	"github.com/poetlife/aladdin/internal/database/entity"
)

// migration0019GalaxyWatermarkVisits 新增水印登记表：一枚码对回一次访问。
//
// **已发布，不可修改。** 需要改结构时追加新的迁移，见 migrations.go。
//
// 它是纯新增——这张表此前不存在，没有任何存量行要搬，因此是"建表，结束"，
// 与 0002、0012、0013、0016、0018 同形。
//
// **不写入任何行。** "没有登记"就是这张表出现之前的行为：水印签发了但没有一处能
// 对回人——而那正是这一版要补上的东西，不该由迁移预置一条假记录来表达。
var migration0019GalaxyWatermarkVisits = &gormigrate.Migration{
	ID: "0019_galaxy_watermark_visits",
	Migrate: func(tx *gorm.DB) error {
		return tx.AutoMigrate(&entity.GalaxyWatermarkVisitRecord{})
	},
}
