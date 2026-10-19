package migrate

import (
	"gorm.io/gorm"

	"github.com/go-gormigrate/gormigrate/v2"

	"github.com/poetlife/aladdin/internal/database"
)

// migration0020GalaxyBlindWatermark 给工程表加一列「要不要铺盲水印」。
//
// **已发布，不可修改。** 需要改结构时追加新的迁移，见 migrations.go。
//
// 加的是**布尔列、默认 false**：存量工程因此全部落在"不铺"那一档，与这一列出现
// 之前的行为逐字相同——那些站点上原本就没有盲水印（见
// docs/design/galaxy/watermark.md 的"默认关闭"）。这一点不能反过来：若默认给
// true，一次升级就会让所有已发布站点在无人要求的情况下多出一层看得见的纹理。
var migration0020GalaxyBlindWatermark = &gormigrate.Migration{
	ID: "0020_galaxy_blind_watermark",
	Migrate: func(tx *gorm.DB) error {
		return tx.AutoMigrate(&database.GalaxyProjectRecord{})
	},
}
