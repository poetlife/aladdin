package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"

	"github.com/poetlife/aladdin/internal/observability"
)

const testTraceID = "11112222333344445555666677778888"

// newTestTelemetry 装配一个只记日志的遥测中间件。
func newTestTelemetry(t *testing.T, registeredPaths []string) (*telemetryMiddleware, *observer.ObservedLogs) {
	t.Helper()

	// 遥测实现必须真的建起来：没有 provider 时 span 无效，trace_id 就不会出现在
	// 日志里，测试会失败在一个与被测逻辑无关的地方。
	provider, err := observability.NewProvider(context.Background(), observability.ProviderOptions{
		ServiceName: "test",
		SampleRatio: 1,
	})
	if err != nil {
		t.Fatalf("构建遥测失败: %v", err)
	}
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })

	core, logs := observer.New(zapcore.DebugLevel)
	return &telemetryMiddleware{logger: zap.New(core), registeredPaths: registeredPaths}, logs
}

// serve 让一个什么都不做的 handler 走一遍中间件。
//
// handler 刻意不碰 RBAC 引擎：这样断言的就是"中间件自己留了痕"，
// 而不是"引擎留了痕"。
func serve(m *telemetryMiddleware, path, traceparent string) {
	req := httptest.NewRequest(http.MethodPost, path, nil)
	if traceparent != "" {
		req.Header.Set(observability.TraceparentHeader, traceparent)
	}
	handler := m.wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	handler.ServeHTTP(httptest.NewRecorder(), req)
}

// 每个请求都要留下一行可检索的日志。
//
// 补上它之前，服务端唯一会写 trace_id 的地方是 RBAC 判定留痕，于是
// Login / Refresh / WhoAmI / GetSessionPermissions 这些不走判定的请求虽然起了
// span、回写了 trace_id 响应头，日志里却一行都没有——拿着那个 id 去搜日志会
// 一无所获，而浏览器打开页面时最先发的就是这几个方法。
func TestEveryRequestLeavesSearchableTrace(t *testing.T) {
	const procedure = "/aladdin.rbac.v1.RBACService/ListRoles"
	m, logs := newTestTelemetry(t, []string{"/aladdin.rbac.v1.RBACService/"})

	serve(m, procedure, "00-"+testTraceID+"-00f067aa0ba902b7-01")

	entries := logs.All()
	if len(entries) != 1 {
		t.Fatalf("日志条数 = %d，期望每个请求恰好一条", len(entries))
	}
	entry := entries[0]
	if entry.Level != zapcore.InfoLevel {
		t.Errorf("业务请求应记 INFO，实际 %s", entry.Level)
	}

	fields := entry.ContextMap()
	if fields["trace_id"] != testTraceID {
		t.Errorf("trace_id = %v，期望 %q", fields["trace_id"], testTraceID)
	}
	if fields["procedure"] != procedure {
		t.Errorf("procedure = %v，期望 %q", fields["procedure"], procedure)
	}
	if fields["status"] != int64(http.StatusOK) {
		t.Errorf("status = %v，期望 200", fields["status"])
	}
	if _, ok := fields["duration_ms"]; !ok {
		t.Error("缺少耗时字段 duration_ms")
	}
}

// 探针与未匹配路径降到 DEBUG：它们高频，记 INFO 会把日志淹成心跳与噪声。
// 但**仍然要记**——否则"服务是否健康"这件事本身不可追溯。
func TestProbeAndUnmatchedLoggedAtDebug(t *testing.T) {
	cases := []struct {
		name  string
		path  string
		paths []string
	}{
		{"健康探针", "/grpc.health.v1.Health/Check", []string{"/grpc.health.v1.Health/"}},
		{"未匹配路径", "/random/scanner", []string{"/aladdin.rbac.v1.RBACService/"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, logs := newTestTelemetry(t, tc.paths)
			serve(m, tc.path, "")

			entries := logs.All()
			if len(entries) != 1 {
				t.Fatalf("日志条数 = %d，期望 1", len(entries))
			}
			if entries[0].Level != zapcore.DebugLevel {
				t.Errorf("等级 = %s，期望 DEBUG", entries[0].Level)
			}
			if entries[0].ContextMap()["trace_id"] == nil {
				t.Error("降到 DEBUG 也应当带 trace_id，否则排障时依然搜不到")
			}
		})
	}
}
