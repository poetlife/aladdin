package observability

import (
	"context"
	"net/http"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
)

// TraceparentHeader 是 W3C Trace Context 的传播头名。
//
// 它是**传播**的头名唯一来源：服务端提取、响应回写、CLI 注入都用它。
// 前端有一份等价的常量（TypeScript 无法引用 Go 常量），改动时必须同步，
// 见 docs/observability.md 的传播约定。
const TraceparentHeader = "traceparent"

// ResponseTraceIDHeader 是**响应方向**给人看的链路标识头。
//
// 它存在的唯一理由是可用性：traceparent 里的 trace-id 要从 `00-` 与 span-id
// 之间手工剥出来，人复制它去搜日志很容易抄错。这个头直接给那 32 位。
//
// 两条边界，名字里的 Response 就是为提醒第一条：
//
//   - **只在响应方向出现，不是传播机制**。请求方向的链路传播只认
//     traceparent；服务端不读入站的这个头。把它当成可入站的值，就等于
//     退回"自定义头当传播协议"，标准组件再也接不上。
//   - **必须与 traceparent 同源**。两者都由 WriteTraceHeaders 从同一个 span
//     上下文写出，不允许任何一方被单独生成——两个 ID 不一致比只有一个更糟。
const ResponseTraceIDHeader = "x-trace-id"

// StartServerSpan 为一次进入的 RPC 请求起 server span。
//
// 它从入站头里提取并校验 `traceparent`：不合法时 OTel 的传播器原样返回入参
// context，于是这里新起一条链路。**非法值绝不被继承**——否则日志里会出现格式
// 非法的 trace_id，而且调用方可以用一个固定值把不同请求的日志并成一条链路。
//
// 只看 `traceparent`。`x-trace-id` 是响应方向给人看的头，**入站的它一律忽略**：
// 接受它就等于把自定义头重新变成传播机制。见 ResponseTraceIDHeader 的说明。
//
// 校验失败不会让请求失败：观测数据的格式问题不能升级成业务故障，否则一条畸形
// 的传播头就足以拒绝服务。
func StartServerSpan(ctx context.Context, header http.Header, procedure string) (context.Context, trace.Span) {
	parent := otel.GetTextMapPropagator().Extract(ctx, propagation.HeaderCarrier(header))
	return otel.Tracer(scopeName).Start(parent, procedure, trace.WithSpanKind(trace.SpanKindServer))
}

// WriteTraceHeaders 把本次请求的链路标识写进响应头。
//
// 写两个头，二者取自**同一个** span 上下文：
//
//   - `traceparent`：W3C 标准，给 OTel Collector、Tempo/Jaeger 与 service mesh 用。
//     trace-id 与请求一致，span-id 是服务端**自己这个 span** 的——回显请求头做不到
//     这一点，客户端因此能定位到服务端处理它的那一步。
//   - `x-trace-id`：给人看的，直接可复制去搜日志。
//
// 没有有效 span 时两个都不写：空洞的头比不写更容易误导人。
func WriteTraceHeaders(ctx context.Context, header http.Header) {
	spanContext := trace.SpanContextFromContext(ctx)
	if !spanContext.IsValid() {
		return
	}
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(header))
	header.Set(ResponseTraceIDHeader, spanContext.TraceID().String())
}

// EndServerSpan 结束 ctx 上的 server span，并按 HTTP 状态码置状态。
//
// 只有 5xx 记为 Error，4xx 不算——这是 OTel 的 HTTP 语义约定：客户端错误是
// 服务端正常履职的结果。鉴权拒绝另由 RecordDecision 记在属性上，不靠 span
// 状态表达，否则"权限不足"和"服务端崩了"在 trace 里会变成同一件事。
func EndServerSpan(ctx context.Context, status int) {
	span := trace.SpanFromContext(ctx)
	if !span.IsRecording() {
		return
	}
	span.SetAttributes(attribute.Int("http.response.status_code", status))
	if status >= http.StatusInternalServerError {
		span.SetStatus(codes.Error, http.StatusText(status))
	}
	span.End()
}

// TextMapCarrier 是传播头读写的载体。
//
// 调用方用自己的传输层类型实现它（如 gRPC metadata 的适配器），
// 从而不必依赖 OTel 的类型——OTel 的 API 只允许出现在本包内。
type TextMapCarrier interface {
	Get(key string) string
	Set(key, value string)
	Keys() []string
}

// carrierAdapter 把本包的载体适配成 OTel 的载体类型。
//
// 用嵌入而不是逐个转发方法：两者的方法集完全一致，
// 逐个转发只会在 OTel 增删方法时变成一处静默的错误。
type carrierAdapter struct{ TextMapCarrier }

// InjectTraceparent 把当前 span 的 traceparent 写进载体。
//
// 出站调用用它把链路标识交给下游。没有有效 span 时不写：
// 一个空洞的头比不写更容易误导人。
func InjectTraceparent(ctx context.Context, carrier TextMapCarrier) {
	if !trace.SpanContextFromContext(ctx).IsValid() {
		return
	}
	otel.GetTextMapPropagator().Inject(ctx, carrierAdapter{carrier})
}

// StartClientSpan 为一次出站 RPC 起 client span。
//
// 下游通过 traceparent 继承本 span 的 trace_id，于是"CLI 的一次调用"
// 与"服务端处理它的那几步"在同一条链路上。
func StartClientSpan(ctx context.Context, method string) (context.Context, trace.Span) {
	return otel.Tracer(scopeName).Start(ctx, method, trace.WithSpanKind(trace.SpanKindClient))
}

// SpanLogger 返回带 trace_id 与 span_id 字段的 logger。
//
// 这是日志与链路关联的**唯一入口**：任何模块不得自行读取传播头、不得自行
// 生成 ID。没有 span 的日志（进程启动、配置加载、优雅退出）不带这两个字段
// ——它们不是必填项，凑一个假值比缺字段更糟。
func SpanLogger(ctx context.Context, logger *zap.Logger) *zap.Logger {
	if logger == nil {
		return nil
	}
	sc := trace.SpanContextFromContext(ctx)
	if !sc.IsValid() {
		return logger
	}
	return logger.With(
		zap.String("trace_id", sc.TraceID().String()),
		zap.String("span_id", sc.SpanID().String()),
	)
}

// RecordDecision 把一次鉴权结论写进当前 span。
//
// 目的是让 trace 自解释：翻开任意一条链路就能看到"这次请求被判定为什么、
// 依据是什么"，不必再按 trace_id 去日志里捞。主体标识写在 span 属性上而不进
// 指标属性——span 不做聚合，高基数在这里是安全的。
func RecordDecision(ctx context.Context, subjectID, permission, decision, reason string) {
	span := trace.SpanFromContext(ctx)
	if !span.IsRecording() {
		return
	}
	span.SetAttributes(
		attribute.String("aladdin.rbac.subject_id", subjectID),
		attribute.String("aladdin.rbac.permission", permission),
		attribute.String("aladdin.rbac.decision", decision),
		attribute.String("aladdin.rbac.reason", reason),
	)
}
