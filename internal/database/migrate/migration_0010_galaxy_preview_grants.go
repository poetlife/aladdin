package migrate

import (
	"gorm.io/gorm"

	"github.com/go-gormigrate/gormigrate/v2"

	"github.com/poetlife/aladdin/internal/database"
)

// migration0010GalaxyPreviewGrants 新增预览凭证表：一条随预览地址走的短时凭证。
//
// **它是可加的、也不动已有行**：既有工程一条凭证都没有，与"没人要求过预览"
// 完全等价（见 docs/design/galaxy/authoring.md 的"预览"）。
//
// 表里只有凭证本身、它授权的工程与失效时刻——没有清单、没有字节，也没有
// "这条凭证能做什么"的字段：它能做的事只有一件（读这一个工程的草稿）。
//
// **已经发布过的迁移不得修改。** 需要再改结构时追加新的。
var migration0010GalaxyPreviewGrants = &gormigrate.Migration{
	ID: "0010_galaxy_preview_grants",
	Migrate: func(tx *gorm.DB) error {
		return tx.AutoMigrate(&database.GalaxyPreviewGrantRecord{})
	},
}
