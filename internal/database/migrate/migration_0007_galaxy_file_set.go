package migrate

import (
	"gorm.io/gorm"

	"github.com/go-gormigrate/gormigrate/v2"

	"github.com/poetlife/aladdin/internal/database"
)

// migration0007GalaxyFileSet 把 galaxy 的内容形态从「一份 HTML」改成「一组具名
// 文件」：草稿、版本与发布的那一列从"正文"变成"清单"，工程加形态，版本加渲染
// 规则版本。资产表的结构不变（类别本来就是一列，新增的 font 只是它的一个取值）。
//
// **它是破坏性的：旧行一律丢弃。** 五张表被删掉重建，因此已有的工程、草稿、
// 版本、资产元数据与发布记录都不会保留。这是刻意的，理由有两条：
//
//   - galaxy 是**同一天上线的新模块**，还没有承载真实内容；
//   - 转换本身做不了。旧的"正文"要变成 `{index.html: 摘要}` 的清单，就得把每
//     一行正文写成一个对象存储对象——而**迁移只碰数据库，不碰对象存储**（见
//     docs/design/persistence/README.md）。一份"读得到库里的话、写不进桶里的
//     字节"的迁移会把库与桶永久地对不上，比直接丢掉更糟。
//
// **已经发布过的迁移不得修改。** 需要再改结构时追加新的。
var migration0007GalaxyFileSet = &gormigrate.Migration{
	ID: "0007_galaxy_file_set",
	Migrate: func(tx *gorm.DB) error {
		// 先删后建。按依赖顺序删（子表在前），免得外键或后续的 AutoMigrate
		// 在半途上遇到旧结构。
		for _, table := range []string{
			"galaxy_publications",
			"galaxy_versions",
			"galaxy_drafts",
			"galaxy_assets",
			"galaxy_projects",
		} {
			if err := tx.Migrator().DropTable(table); err != nil {
				return err
			}
		}
		return tx.AutoMigrate(
			&database.GalaxyProjectRecord{},
			&database.GalaxyDraftRecord{},
			&database.GalaxyVersionRecord{},
			&database.GalaxyAssetRecord{},
			&database.GalaxyPublicationRecord{},
		)
	},
}
