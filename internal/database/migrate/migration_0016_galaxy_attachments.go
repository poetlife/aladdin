package migrate

import (
	"gorm.io/gorm"

	"github.com/go-gormigrate/gormigrate/v2"

	"github.com/poetlife/aladdin/internal/database"
)

// migration0016GalaxyAttachments 建立工程附件表。
//
// **已发布，不可修改。** 需要改结构时追加新的迁移，见 migrations.go。
//
// 它是纯新增——附件此前不存在，没有任何存量行要搬。因此这条迁移是"建表，结束"，
// 与 0002、0012、0013 同形。
//
// **一张表，且不含任何字节**：附件的字节在对象存储的私有区（键为
// `galaxy/<工程标识>/attachments/<附件标识>`），库里只有元数据。它**永不进公开区**，
// 因此也不像资产那样有"公开区的第二份"（见 docs/design/galaxy/attachments.md）。
var migration0016GalaxyAttachments = &gormigrate.Migration{
	ID: "0016_galaxy_attachments",
	Migrate: func(tx *gorm.DB) error {
		return tx.AutoMigrate(&database.GalaxyAttachmentRecord{})
	},
}
