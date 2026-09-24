package observability

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// 一个合法的入站 traceparent：32 位 trace-id、16 位 span-id、flags 01（已采样）。
const incomingTraceparent = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
const incomingTraceID = "4bf92f3577b34da6a3ce929d0e0e4736"
const incomingSpanID = "00f067aa0ba902b7"

// newTestProvider 构建一个**不上报**的 provider，并注册为全局实现。
//
// 每个用例都重建：全局 provider 是进程级状态，上一条用例留下的采样器会
// 影响下一条。这里刻意不配 Endpoint——本文件的所有断言都建立在
// "没有导出端点时链路标识照常工作"这个前提上。
func newTestProvider(t *testing.T) *Provider {
	t.Helper()
	provider, err := NewProvider(context.Background(), ProviderOptions{
		ServiceName: "test",
		SampleRatio: 1,
	})
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
	return provider
}

// 服务端必须继承入站的 trace-id、生成**自己**的 span-id，并把两者回写到响应头。
//
// 这个用例同时覆盖了"没有 Collector 也能工作"：provider 没有配端点，
// 但 trace_id / span_id 依然合法，传播与回写照常。
func TestServerSpanInheritsAndEchoesTraceparent(t *testing.T) {
	newTestProvider(t)

	header := http.Header{}
	header.Set(TraceparentHeader, incomingTraceparent)

	ctx, span := StartServerSpan(context.Background(), header, "aladdin.rbac.v1.RBACService/ListRoles")
	defer span.End()

	sc := trace.SpanContextFromContext(ctx)
	if got := sc.TraceID().String(); got != incomingTraceID {
		t.Errorf("trace_id = %q，期望继承入站值 %q", got, incomingTraceID)
	}
	if !sc.IsSampled() {
		t.Error("入站 flags 为 01，采样标记应当被继承")
	}
	if got := sc.SpanID().String(); got == incomingSpanID {
		t.Error("span_id 不应等于入站值：服务端必须有自己的 span")
	}
	if len(sc.SpanID().String()) != 16 {
		t.Errorf("span_id = %q，期望 16 位十六进制", sc.SpanID())
	}

	out := http.Header{}
	WriteTraceHeaders(ctx, out)

	echoed := out.Get(TraceparentHeader)
	parts := strings.Split(echoed, "-")
	if len(parts) != 4 {
		t.Fatalf("回写的 traceparent 形状不对: %q", echoed)
	}
	if parts[1] != incomingTraceID {
		t.Errorf("回写的 trace-id = %q，期望与请求一致", parts[1])
	}
	if parts[2] != sc.SpanID().String() {
		t.Errorf("回写的 span-id = %q，期望是服务端自己的 %q", parts[2], sc.SpanID())
	}

	// 给人看的那个头直接给 trace-id：复制它就能搜日志，不必从 traceparent
	// 里手工剥出中段。
	if got := out.Get(ResponseTraceIDHeader); got != incomingTraceID {
		t.Errorf("%s = %q，期望 %q", ResponseTraceIDHeader, got, incomingTraceID)
	}
}

// 两个响应头必须同源。
//
// 不一致比只有一个更糟：排障的人会拿两个 ID 互相搜，两边都搜不到，
// 然后开始怀疑日志系统丢了数据。它们只能由 WriteTraceHeaders 一处写出，
// 就是为了让"不一致"这件事没有发生的路径。
func TestResponseTraceHeadersAgree(t *testing.T) {
	newTestProvider(t)

	header := http.Header{}
	header.Set(TraceparentHeader, incomingTraceparent)

	ctx, span := StartServerSpan(context.Background(), header, "Proc")
	defer span.End()

	out := http.Header{}
	WriteTraceHeaders(ctx, out)

	fromTraceparent := strings.Split(out.Get(TraceparentHeader), "-")[1]
	if fromTraceparent != out.Get(ResponseTraceIDHeader) {
		t.Errorf("traceparent 里的 trace-id (%q) 与 %s (%q) 不一致",
			fromTraceparent, ResponseTraceIDHeader, out.Get(ResponseTraceIDHeader))
	}
}

// x-trace-id 是响应方向给人看的头，**入站的它必须被忽略**。
//
// 接受它就等于把自定义头重新变成传播机制——那正是这次迁移要去掉的东西，
// 而它的危害是标准组件（Collector、service mesh）再也接不上链路。
func TestIncomingResponseTraceIDHeaderIsIgnored(t *testing.T) {
	newTestProvider(t)

	header := http.Header{}
	header.Set(ResponseTraceIDHeader, "aaaabbbbccccddddeeeeffff00001111")

	ctx, span := StartServerSpan(context.Background(), header, "Proc")
	defer span.End()

	sc := trace.SpanContextFromContext(ctx)
	if got := sc.TraceID().String(); got == "aaaabbbbccccddddeeeeffff00001111" {
		t.Error("入站的 x-trace-id 被当成了链路标识")
	}
}

// 非法的入站 traceparent 绝不能被继承，也不能被原样回显。
//
// 不校验的后果有两层：日志里出现格式非法的 trace_id 会让所有基于它的检索失效；
// 而入站值优先意味着调用方可以用一个固定值把不同请求的日志并成一条链路。
//
// 同时断言它**不导致请求失败**：观测数据的格式问题不能升级成业务故障。
func TestInvalidTraceparentIsNotInherited(t *testing.T) {
	newTestProvider(t)

	cases := []struct {
		name     string
		value    string
		bogusTID string
	}{
		{"长度不对", "00-4bf92f-00f067aa0ba902b7-01", ""},
		{"大写十六进制", "00-4BF92F3577B34DA6A3CE929D0E0E4736-00F067AA0BA902B7-01", ""},
		{"trace-id 全零", "00-00000000000000000000000000000000-00f067aa0ba902b7-01", "00000000000000000000000000000000"},
		{"span-id 全零", "00-4bf92f3577b34da6a3ce929d0e0e4736-0000000000000000-01", ""},
		{"版本非法", "ff-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01", ""},
		{"根本不是这个格式", "abc", ""},
		{"空串", "", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			header := http.Header{}
			header.Set(TraceparentHeader, tc.value)

			ctx, span := StartServerSpan(context.Background(), header, "Proc")
			defer span.End()

			sc := trace.SpanContextFromContext(ctx)
			if !sc.IsValid() {
				t.Fatal("入站值非法时也必须有一个合法的 span")
			}
			if tc.bogusTID != "" && sc.TraceID().String() == tc.bogusTID {
				t.Errorf("继承了非法的 trace-id %q", tc.bogusTID)
			}

			out := http.Header{}
			WriteTraceHeaders(ctx, out)
			if tc.value != "" && out.Get(TraceparentHeader) == tc.value {
				t.Errorf("非法入站值被原样回显: %q", tc.value)
			}
			if echoed := out.Get(TraceparentHeader); !strings.HasPrefix(echoed, "00-") {
				t.Errorf("回写的 traceparent 形状不对: %q", echoed)
			}
			// 给人看的头也不能把非法值带出去
			if tc.bogusTID != "" && out.Get(ResponseTraceIDHeader) == tc.bogusTID {
				t.Errorf("%s 回写了非法入站值 %q", ResponseTraceIDHeader, tc.bogusTID)
			}
		})
	}
}

// 没有 span 的日志不带这两个字段，有 span 的日志带，且与 span 上下文一致。
func TestSpanLoggerCarriesSpanContext(t *testing.T) {
	newTestProvider(t)

	t.Run("有 span 时带 trace_id 与 span_id", func(t *testing.T) {
		core, logs := observer.New(zapcore.DebugLevel)
		logger := zap.New(core)

		ctx, span := StartServerSpan(context.Background(), http.Header{}, "Proc")
		defer span.End()

		SpanLogger(ctx, logger).Info("hello")

		entries := logs.All()
		if len(entries) != 1 {
			t.Fatalf("日志条数 = %d，期望 1", len(entries))
		}
		fields := entries[0].ContextMap()
		sc := trace.SpanContextFromContext(ctx)
		if fields["trace_id"] != sc.TraceID().String() {
			t.Errorf("trace_id 字段 = %v，期望 %q", fields["trace_id"], sc.TraceID())
		}
		if fields["span_id"] != sc.SpanID().String() {
			t.Errorf("span_id 字段 = %v，期望 %q", fields["span_id"], sc.SpanID())
		}
	})

	// 进程启动、配置加载、优雅退出这类日志没有请求上下文。它们是**缺字段**，
	// 不是伪造一个值——假的 trace_id 会把不相干的日志并成一条链路。
	t.Run("没有 span 时不伪造字段", func(t *testing.T) {
		core, logs := observer.New(zapcore.DebugLevel)
		logger := zap.New(core)

		SpanLogger(context.Background(), logger).Info("hello")

		fields := logs.All()[0].ContextMap()
		if _, ok := fields["trace_id"]; ok {
			t.Errorf("没有 span 时不应写入 trace_id，实际为 %v", fields["trace_id"])
		}
		if _, ok := fields["span_id"]; ok {
			t.Errorf("没有 span 时不应写入 span_id，实际为 %v", fields["span_id"])
		}
	})

	t.Run("logger 为 nil 时返回 nil", func(t *testing.T) {
		if got := SpanLogger(context.Background(), nil); got != nil {
			t.Error("nil logger 应当原样返回 nil")
		}
	})
}

// 出站调用把 traceparent 写进载体，下游据此继承同一条链路。
func TestInjectTraceparent(t *testing.T) {
	newTestProvider(t)

	ctx, span := StartClientSpan(context.Background(), "aladdin.rbac.v1.RBACService/ListRoles")
	defer span.End()

	carrier := &mapCarrier{values: map[string]string{}}
	InjectTraceparent(ctx, carrier)

	sc := trace.SpanContextFromContext(ctx)
	if got := carrier.values[TraceparentHeader]; !strings.Contains(got, sc.TraceID().String()) {
		t.Errorf("注入的 traceparent = %q，未包含 trace_id %q", got, sc.TraceID())
	}
}

// 没有 span 时不注入：一个空洞的头比不注入更容易误导下游。
func TestInjectTraceparentWithoutSpan(t *testing.T) {
	carrier := &mapCarrier{values: map[string]string{}}
	InjectTraceparent(context.Background(), carrier)
	if len(carrier.values) != 0 {
		t.Errorf("没有 span 时不应写入任何传播头，实际 %v", carrier.values)
	}
}

// mapCarrier 是测试用的最小传播载体。
type mapCarrier struct{ values map[string]string }

func (c *mapCarrier) Get(key string) string { return c.values[key] }
func (c *mapCarrier) Set(key, value string) { c.values[key] = value }
func (c *mapCarrier) Keys() []string {
	keys := make([]string, 0, len(c.values))
	for key := range c.values {
		keys = append(keys, key)
	}
	return keys
}
