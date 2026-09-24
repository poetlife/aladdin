// Package database 提供数据库连接、后端方言与结构迁移。
//
// 它是持久化基础设施的**唯一入口**：全仓库只有这里建立数据库连接、
// 只有这里定义表结构、只有这里执行迁移。业务代码不得自行 gorm.Open
// 或执行 DDL（见 docs/ssot-registry.md）。
//
// 后端取值的合法集合定义在本包（dialect.go）：方言知识只应有一处，
// 配置模块只负责把取值按分层规则读出来。
package database

import (
	"fmt"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/poetlife/aladdin/internal/config"
)

// Open 按配置建立数据库连接。
//
// 它完成三件事：解析并校验后端、归一连接串、建立连接并把连接池配成
// 该后端该有的样子。失败时返回的错误里带着**脱敏后的定位信息**——
// 连接串可能含口令，不能原样回显（见 docs/design/persistence/README.md）。
//
// 这里不做 Ping：连接是惰性的，拿不到连接这件事由第一次查询或迁移暴露，
// 而迁移必然发生在监听端口打开之前，因此"连不上就拒绝启动"依然成立。
// 少一次多余的往返。
func Open(cfg config.DatabaseConfig, logger *zap.Logger) (*gorm.DB, error) {
	dialect, err := ParseDialect(cfg.Driver)
	if err != nil {
		return nil, err
	}
	dialector, err := dialect.Dialector(cfg.DSN)
	if err != nil {
		return nil, err
	}

	db, err := gorm.Open(dialector, &gorm.Config{
		Logger: newSQLogger(logger),
		// 表间不加外键是刻意的：角色是否存在、是否违反互斥都是领域约束，
		// 唯一入口是授权面的校验（见 docs/design/persistence/schema.md）。
		// 显式关掉，免得将来有人加了个关联字段，gorm 就顺手把外键建出来，
		// 于是同一份判定多出了第二个实现。
		DisableForeignKeyConstraintWhenMigrating: true,
	})
	if err != nil {
		return nil, fmt.Errorf("连接数据库失败（%s）: %w", Describe(cfg), err)
	}

	if err := dialect.applyPool(db); err != nil {
		return nil, fmt.Errorf("配置 %s 的连接池失败: %w", dialect, err)
	}
	return db, nil
}
