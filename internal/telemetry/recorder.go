package telemetry

import (
	"context"
	"sort"

	"go.uber.org/zap"

	telemetryv1 "github.com/poetlife/aladdin/api/gen/aladdin/telemetry/v1"
	"github.com/poetlife/aladdin/internal/observability"
)

// Request 是一次上报的协议层上下文。
//
// 它把"协议层知道的事"与"遥测语义"分开：主体标识来自会话，来源地址只用于限流，
// 两者都不由请求体提供。RPC handler 负责把它们填进来（见 internal/server）。
type Request struct {
	// HeaderClient 是请求头声明的上报端（observability.ClientWeb / ClientCLI）。
	// 未携带时为空，此时不校验事件自报的 client。
	HeaderClient string
	// SubjectID 是已识别的主体标识；空表示匿名。
	SubjectID string
	// Source 是来源地址，**只**用于匿名限流的键。
	//
	// 它**不进日志**：地址不在 docs/observability.md 允许写入的字段里，而它对本
	// 服务要回答的问题（谁做了什么、结果如何）没有增量信息。
	Source string
	// Events 是本批事件。
	Events []*telemetryv1.Event
}

// Recorder 把通过校验的事件落成结构化日志。
//
// 它是遥测落盘的**唯一入口**：校验、脱敏、限流都在它内部完成，调用方（RPC
// handler）只负责把协议层的事实递进来。这样"什么会被写进日志"是一个可以单独
// 测试的问题，不必起一个服务端。
type Recorder struct {
	logger  *zap.Logger
	limiter *Limiter
}

// NewRecorder 构造记录器。limiter 为 nil 表示不限流（只应在测试里这么用）。
func NewRecorder(logger *zap.Logger, limiter *Limiter) *Recorder {
	return &Recorder{logger: logger, limiter: limiter}
}

// Report 处理一批上报，返回被接受的事件条数。
//
// 返回值只服务于测试与自检——RPC 的响应体刻意是空的，客户端不据此分支
// （见 telemetry.proto 的 ReportEventsResponse）。
func (r *Recorder) Report(ctx context.Context, req Request) int {
	if r == nil {
		return 0
	}

	if !r.limiter.Allow(limitKey(req), len(req.Events)) {
		r.debug(ctx, "客户端事件上报超出上限，本批丢弃",
			zap.Int("events", len(req.Events)))
		return 0
	}

	logger := observability.SpanLogger(ctx, r.logger)
	if logger == nil {
		logger = zap.NewNop()
	}

	accepted := 0
	for _, ev := range req.Events {
		rec, reason, ok := normalize(ev, req.SubjectID, req.HeaderClient)
		if !ok {
			// 丢弃只记 DEBUG：未知动作、不允许匿名都是**上报端的清单不一致**，
			// 不是业务错误。记到 WARN 会让一次客户端发版把服务端日志刷满。
			r.debug(ctx, "客户端事件被丢弃", zap.String("reason", reason))
			continue
		}
		accepted++
		logger.Info("客户端事件", recordFields(rec)...)
	}
	return accepted
}

// limitKey 取本次上报的限流键：已识别主体用它，匿名用来源地址。
//
// 匿名时键是来源地址而不是"全部匿名调用方共用一个键"：后者会让一个滥用者把
// 所有匿名上报者一起拖进限流。代价是"来源地址"在反向代理后面是代理自身的
// 地址（见 docs/observability.md）——宁可限得糙一点，也不去信任可伪造的
// X-Forwarded-For。
func limitKey(req Request) string {
	if req.SubjectID != "" {
		return "subject:" + req.SubjectID
	}
	return "source:" + req.Source
}

// debug 记一行带链路标识的 DEBUG 日志；没有 logger 时什么也不做。
func (r *Recorder) debug(ctx context.Context, msg string, fields ...zap.Field) {
	logger := observability.SpanLogger(ctx, r.logger)
	if logger == nil {
		return
	}
	logger.Debug(msg, fields...)
}

// recordFields 把一条记录摊平成日志字段。
//
// 属性加 `attr_` 前缀：它与标准字段（client / surface / action / result）同处
// 顶层，前缀是它们唯一的区分——没有它，一个叫 result 的属性就会覆盖结局。
//
// trace_id 用 client_trace_id 这个名字：服务端本次请求自己的 trace_id 由
// SpanLogger 写入，与本字段**不是同一条链路**——它记录的是客户端当时所在的那条。
func recordFields(rec Record) []zap.Field {
	fields := []zap.Field{
		zap.String("client", rec.Client),
		zap.String("surface", rec.Surface),
		zap.String("action", rec.Action),
		zap.String("result", rec.Result),
	}
	if rec.DurationMS > 0 {
		fields = append(fields, zap.Uint32("duration_ms", rec.DurationMS))
	}
	if rec.TraceID != "" {
		fields = append(fields, zap.String("client_trace_id", rec.TraceID))
	}
	if rec.SubjectID != "" {
		fields = append(fields, zap.String("subject_id", rec.SubjectID))
	}
	// 按键排序，让同一条事件每次的字段顺序一致：日志是给人 grep 的，
	// map 的随机顺序会让"看起来不同"变成一种假信号。
	keys := make([]string, 0, len(rec.Attrs))
	for key := range rec.Attrs {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		fields = append(fields, zap.String("attr_"+key, rec.Attrs[key]))
	}
	return fields
}
