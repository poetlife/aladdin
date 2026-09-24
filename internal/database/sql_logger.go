package database

import (
	"context"
	"errors"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// slowQueryThreshold 是判定一次查询"慢"的阈值。
//
// 本仓库的查询都是单表、按主键或唯一键取数，超过这个值说明出了问题，
// 而不是说明负载高。
const slowQueryThreshold = 200 * time.Millisecond

// newSQLogger 把 gorm 的 SQL 日志接到仓库统一的日志实现上。
//
// 不接不行：gorm 自带的 logger 会直接往标准输出打，那就成了仓库里第二个
// 日志实现，绕开了 observability 的唯一出口（见 docs/ssot-registry.md）。
func newSQLogger(l *zap.Logger) logger.Interface {
	// SQL 明细只在 debug 级别打开：它是排障用的，不该在默认级别刷屏。
	// 判断的是 logger 实际启用的级别，因此 `log_level: debug` 就能看到 SQL，
	// 不需要为它单开一个配置项。
	level := logger.Warn
	if l.Core().Enabled(zapcore.DebugLevel) {
		level = logger.Info
	}
	return &sqlLogger{zap: l, level: level}
}

// sqlLogger 是 gorm 的日志接口在 zap 上的实现。
type sqlLogger struct {
	zap   *zap.Logger
	level logger.LogLevel
}

// LogMode 实现 logger.Interface。
func (l *sqlLogger) LogMode(level logger.LogLevel) logger.Interface {
	return &sqlLogger{zap: l.zap, level: level}
}

// Info 实现 logger.Interface。gorm 用它报告常规过程信息。
func (l *sqlLogger) Info(_ context.Context, msg string, args ...any) {
	if l.level < logger.Info {
		return
	}
	l.zap.Sugar().Debugf(msg, args...)
}

// Warn 实现 logger.Interface。
func (l *sqlLogger) Warn(_ context.Context, msg string, args ...any) {
	if l.level < logger.Warn {
		return
	}
	l.zap.Sugar().Warnf(msg, args...)
}

// Error 实现 logger.Interface。
func (l *sqlLogger) Error(_ context.Context, msg string, args ...any) {
	if l.level < logger.Error {
		return
	}
	l.zap.Sugar().Errorf(msg, args...)
}

// Trace 实现 logger.Interface：一次查询执行完后按结果与耗时选级别。
//
// "没查到"不算错误：本仓库大量依赖"角色不存在"这种正常结论，
// 把它记成 error 会让日志里充满假故障。
func (l *sqlLogger) Trace(_ context.Context, begin time.Time, fc func() (string, int64), err error) {
	if l.level <= logger.Silent {
		return
	}
	elapsed := time.Since(begin)
	sql, rows := fc()
	fields := []zap.Field{
		zap.String("sql", sql),
		zap.Int64("rows", rows),
		zap.Duration("elapsed", elapsed),
	}

	switch {
	case err != nil && !errors.Is(err, gorm.ErrRecordNotFound):
		l.zap.Error("SQL 执行失败", append(fields, zap.Error(err))...)
	case l.level >= logger.Warn && elapsed > slowQueryThreshold:
		l.zap.Warn("SQL 执行缓慢", fields...)
	case l.level >= logger.Info:
		l.zap.Debug("SQL", fields...)
	}
}
