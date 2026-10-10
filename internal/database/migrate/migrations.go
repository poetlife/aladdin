package migrate

import (
	"github.com/go-gormigrate/gormigrate/v2"
)

// migrations 返回按顺序排列的迁移清单，**这是库结构版本演进的唯一入口**。
//
// 新增一条迁移的做法：
//
//  1. 在本目录新建 `migration_<序号>_<做了什么>.go`，里面定义一个
//     `*gormigrate.Migration`；
//  2. 在本列表末尾追加它；
//  3. 运行本包的结构快照测试，确认它的效果就是你想改的东西。
//
// **已经发布过的迁移不得修改。** 它已经在别的库上执行过，改它不会改变那些库，
// 只会让新库与旧库长成两个样子——而这类偏差要等到某个库上出现"这一列怎么没有"
// 才会被发现。要改结构就追加一条新的。
//
// **序号可以跳号。** 两个分支各加一条时谁先合谁就是前一个序号，而"清单里少一个
// 序号"不会有任何后果：执行的是这个列表里**尚未执行过**的那些，与序号连不连续
// 无关。缺号只在一种情况下要紧——两个未合并的分支用了同一个序号，那时它们各自的
// 文件与变量名会撞在一起。
func migrations() []*gormigrate.Migration {
	return []*gormigrate.Migration{
		migration0001RBACTables,
		migration0002SessionTable,
		migration0003IdentitiesTable,
		migration0004SessionSubjectType,
		migration0005SubjectProfiles,
		migration0006GalaxyTables,
		migration0007GalaxyFileSet,
		migration0008ScopesTable,
		migration0009GalaxyAssetMetadata,
		migration0010GalaxyPreviewGrants,
		migration0011GalaxyContentSlots,
		migration0012ClientEvents,
		migration0013SkillCatalog,
		migration0014SkillCover,
		migration0015SkillImages,
		migration0016GalaxyAttachments,
		migration0017GalaxyDraftSnapshots,
	}
}
