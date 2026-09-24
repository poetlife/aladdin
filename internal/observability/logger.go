// Package observability 提供日志、链路追踪与指标的统一入口。
//
// 本包是"同类副作用只能有一个实现入口"的落点：
//
//   - 任何模块需要写结构化日志，都必须使用这里构建的 logger；
//   - 任何模块需要记录指标或给链路打点，都必须使用这里暴露的助手；
//   - **OTel 的 SDK 与 API 只在本包内出现**，其他模块不直接依赖它们，
//     因此传播头名、字段名、指标名都只有一处定义。
//
// 三者的行为约定见 docs/observability.md。
package observability

import (
	"fmt"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// Level 是日志级别。
type Level string

// 受支持的日志级别。取值与 docs/observability.md 的约定一致。
const (
	LevelDebug Level = "debug"
	LevelInfo  Level = "info"
	LevelWarn  Level = "warn"
	LevelError Level = "error"
)

// Options 是日志构建参数。
type Options struct {
	// Level 是最低输出级别。
	Level Level
	// FilePath 非空时，除控制台外额外输出 JSON Lines 到该文件。
	// CLI 不应设置它：CLI 只在 stderr 输出，避免污染管道数据。
	FilePath string
	// Service 是写入每条日志的 service 字段。
	Service string
}

// NewLogger 按 Options 构建 logger。
//
// 控制台输出人类可读的纯文本；文件输出 JSON Lines。注意 logger 本身**不会**
// 自动带上 trace_id / span_id——那需要由 SpanLogger 从请求上下文派生，
// 因为只有调用点才知道当前在不在某个请求里。
func NewLogger(opts Options) (*zap.Logger, error) {
	level, err := zapcore.ParseLevel(string(opts.Level))
	if err != nil {
		return nil, fmt.Errorf("非法的日志级别 %q: %w", opts.Level, err)
	}

	encoderConfig := zap.NewProductionEncoderConfig()
	encoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder
	encoderConfig.EncodeLevel = zapcore.LowercaseLevelEncoder

	consoleCore := zapcore.NewCore(
		zapcore.NewConsoleEncoder(encoderConfig),
		zapcore.Lock(zapcore.AddSync(stderrWriteSyncer{})),
		level,
	)

	cores := []zapcore.Core{consoleCore}
	if opts.FilePath != "" {
		sink, _, err := zap.Open(opts.FilePath)
		if err != nil {
			return nil, fmt.Errorf("打开日志文件失败: %w", err)
		}
		fileEncoderConfig := zap.NewProductionEncoderConfig()
		fileEncoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder
		fileEncoderConfig.EncodeLevel = zapcore.CapitalLevelEncoder
		cores = append(cores, zapcore.NewCore(
			zapcore.NewJSONEncoder(fileEncoderConfig),
			sink,
			level,
		))
	}

	fields := []zap.Option{}
	if opts.Service != "" {
		fields = append(fields, zap.Fields(zap.String("logger", opts.Service)))
	}
	return zap.New(zapcore.NewTee(cores...), fields...), nil
}
