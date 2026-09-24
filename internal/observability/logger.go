// Package observability 提供日志与链路标识的统一入口。
//
// 本包是"同类副作用只能有一个实现入口"的落点：任何模块需要写结构化日志，
// 都必须使用这里构建的 logger，不得自行 new 一个 zap logger。
package observability

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// TraceIDHeader 是链路标识在 gRPC metadata 与 HTTP Header 中的键名。
//
// 三端（服务端、CLI、前端）共用同一个键，见 docs/observability.md。
const TraceIDHeader = "x-trace-id"

type contextKey struct{}

var traceIDCtxKey = contextKey{}

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
// 控制台输出人类可读的纯文本；文件输出 JSON Lines，字段定义见 docs/observability.md。
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

// WithTraceID 把链路标识放入 context。
//
// 拦截器在认证之前调用它，确保被拒绝的请求同样留痕。
func WithTraceID(ctx context.Context, traceID string) context.Context {
	return context.WithValue(ctx, traceIDCtxKey, traceID)
}

// EnsureTraceID 在 context 中已有链路标识时原样返回，否则生成一个新的。
//
// 这是链路标识生成的唯一入口；任何模块不得自行构造 trace_id。
func EnsureTraceID(ctx context.Context) context.Context {
	if TraceIDFromContext(ctx) != "" {
		return ctx
	}
	return WithTraceID(ctx, NewTraceID())
}

// NewTraceID 生成一个新的链路标识。
func NewTraceID() string {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		// crypto/rand 失败在实践中不可恢复；退化为固定前缀，
		// 宁可失去唯一性也不能让日志路径 panic。
		return "trace-unavailable"
	}
	return hex.EncodeToString(buf[:])
}

// TraceIDFromContext 返回 context 中的链路标识，不存在时返回空串。
func TraceIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(traceIDCtxKey).(string)
	return id
}

// WithTraceIDField 返回一个已附带 trace_id 字段的 logger。
//
// 所有业务日志都应经由此函数派生 logger，避免出现"日志断点"。
func WithTraceIDField(logger *zap.Logger, ctx context.Context) *zap.Logger {
	if logger == nil {
		return nil
	}
	if id := TraceIDFromContext(ctx); id != "" {
		return logger.With(zap.String("trace_id", id))
	}
	return logger
}
