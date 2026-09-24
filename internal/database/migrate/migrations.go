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
func migrations() []*gormigrate.Migration {
	return []*gormigrate.Migration{
		migration0001RBACTables,
	}
}
