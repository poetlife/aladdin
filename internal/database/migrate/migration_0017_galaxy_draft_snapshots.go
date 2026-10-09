package migrate

import (
	"gorm.io/gorm"

	"github.com/go-gormigrate/gormigrate/v2"

	"github.com/poetlife/aladdin/internal/database"
)

// migration0017GalaxyDraftSnapshots 建立草稿快照表，并给版本表加一列说明。
//
// **已发布，不可修改。** 需要改结构时追加新的迁移，见 migrations.go。
//
// 它做两件事：
//
//   - 新增一张表：**被替换掉的**那一份草稿清单。它不是版本（没有序号、不能发布、
//     按保留策略过期），存在的理由是"只推草稿不存版本"那条最常见的误用——那种
//     用法下中间过程在改动发生的那一刻就没有任何记录（见
//     docs/design/galaxy/project-versioning.md）。
//   - 给版本表加一列说明。它是元数据层，可事后改，与"版本不可变"不冲突：不可变
//     说的是清单与渲染规则版本。存量版本这一列为空——那是"还没有说明"，是这一列
//     的正常取值，不需要回填。
//
// 两张表都不含任何字节：清单的条目指向的是按内容摘要寻址的对象。
var migration0017GalaxyDraftSnapshots = &gormigrate.Migration{
	ID: "0017_galaxy_draft_snapshots",
	Migrate: func(tx *gorm.DB) error {
		return tx.AutoMigrate(
			&database.GalaxyDraftSnapshotRecord{},
			&database.GalaxyVersionRecord{},
		)
	},
}
