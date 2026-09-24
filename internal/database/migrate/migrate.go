// Package migrate 是**库结构演进的唯一入口**：全仓库只有这里定义迁移、
// 只有这里执行迁移。业务代码不得自行执行 DDL（见 docs/ssot-registry.md）。
//
// 它独立成包而不是并进 internal/database：表结构定义是全仓库长期不动的一
// 份，迁移清单却会随着每次结构变更不断变长。两者放在同一个目录里，几个月
// 之后"这个目录里哪个文件是稳定的、哪个是只增不减的历史"就看不出来了。
//
// 依赖方向是单向的：本包依赖 internal/database（要用它定义的记录类型来
// 建表），反过来不成立。因此运行器由本包导出，由启动路径调用，而不是挂在
// database 上——挂上去就成环了。
package migrate

import (
	"context"
	"fmt"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/go-gormigrate/gormigrate/v2"

	"github.com/poetlife/aladdin/internal/database"
)

// migrationTableName 是记录"库结构已经演进到哪一步"的表的表名。
//
// 不叫 `migrations`：这个名字放在一个多模块共用的库里太容易被误认成
// 业务表。业务代码不读写它，它只属于迁移机制。
const migrationTableName = "schema_migrations"

// Run 把库结构推进到代码里定义的最新版本。
//
// 约定见 docs/design/persistence/README.md：在监听端口打开之前执行、
// 幂等、失败即拒绝启动、库里存在未知版本时拒绝启动。
func Run(ctx context.Context, db *gorm.DB, logger *zap.Logger) error {
	pending := migrations()

	// 迁移库只认 `db`，没有独立的日志接口可接，因此数据库日志由 gorm 的
	// 日志实现统一承接（见 internal/database/sql_logger.go）——它已经接到
	// 本进程的 zap 上。
	//
	// 带上 ctx：gormigrate 的所有读写都走这个 *gorm.DB，包括它自己开的
	// 事务，因此取消信号能一路传到驱动层。
	m := gormigrate.New(db.WithContext(ctx), migrationOptions(), pending)

	// 调用前记目标与当前版本、调用后记结果与耗时：迁移属于"涉及外部依赖
	// 调用"的关键路径，而它的失败会直接表现为服务起不来（见 docs/observability.md）。
	applied, err := appliedMigrationIDs(db)
	if err != nil {
		return fmt.Errorf("读取已应用的库结构版本失败: %w", err)
	}
	logger.Info("开始执行库结构迁移",
		zap.String("table", migrationTableName),
		zap.Int("declared", len(pending)),
		zap.Strings("applied", applied),
	)
	started := time.Now()

	if err := m.Migrate(); err != nil {
		// 失败时上面的 applied 就是库停在哪儿；出错的那条是声明清单里
		// 第一条不在 applied 中的迁移——迁移库不回传这个信息，所以靠
		// 这两条日志对照着看。这也是为什么失败必须留 Error 级日志。
		logger.Error("库结构迁移失败",
			zap.Error(err),
			zap.Strings("applied", applied),
			zap.Duration("elapsed", time.Since(started)),
		)
		return fmt.Errorf("库结构迁移失败: %w", err)
	}

	// 记的是迁移之后的版本表，不是"这次跑了哪几条"——后者要靠推断得出
	// （声明清单减去迁移前的已应用集合），而推断出来的东西一旦与迁移库
	// 的判断不一致，日志就成了第二个会漂移的实现。前后两份事实摆在一起，
	// 该跑哪几条一目了然。
	after, err := appliedMigrationIDs(db)
	if err != nil {
		// 迁移本身已经成功，读不到版本表不该让启动失败；但要说出来，
		// 否则少的那一行"迁移完成"会被当成"迁移没跑"。
		logger.Warn("库结构迁移已完成，但读取版本表失败", zap.Error(err))
		return nil
	}
	logger.Info("库结构迁移完成",
		zap.Strings("applied", after),
		zap.Duration("elapsed", time.Since(started)),
	)
	return nil
}

// appliedMigrationIDs 读出版本表里已记录的迁移 ID。
//
// **只用于日志**，不参与"该跑哪几条"的判断——那个判断的唯一入口是
// gormigrate 自己。库还是空的（表都还没建）时返回空列表，不是错误。
func appliedMigrationIDs(db *gorm.DB) ([]string, error) {
	if !db.Migrator().HasTable(migrationTableName) {
		return nil, nil
	}
	column := migrationOptions().IDColumnName
	var ids []string
	if err := db.Table(migrationTableName).Order(column).Pluck(column, &ids).Error; err != nil {
		return nil, err
	}
	return ids, nil
}

// migrationOptions 是迁移机制的固定约定。
func migrationOptions() *gormigrate.Options {
	return &gormigrate.Options{
		TableName:    migrationTableName,
		IDColumnName: "id",
		// 与业务表标识类列同一个长度上限：版本标识也是标识。
		IDColumnSize: database.IDSize,
		// 每个迁移在自己的事务里执行：失败则整体回滚，不留下半成品结构。
		UseTransaction: true,
		// 库里存在代码中没有的版本时拒绝启动——通常意味着二进制被回滚而库没有。
		// 带着一个自己不认识的库结构继续服务，比停机危险得多。
		ValidateUnknownMigrations: true,
	}
}
