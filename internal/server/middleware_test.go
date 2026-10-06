package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"

	"github.com/poetlife/aladdin/internal/observability"
	"github.com/poetlife/aladdin/internal/rbac"
	"github.com/poetlife/aladdin/internal/server/interceptor"
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

// 放行清单里**只能**是浏览器直连的非 RPC 入口。
//
// 它与 infraProcedurePrefixes 的区别是类别：那份是 Connect 官方组件提供的
// 框架服务（确实是 RPC，只是没有 aladdin 的权限注解），这份根本不是 RPC。
// 两者的边界一旦混掉，就会出现"某个业务 RPC 被静默放行"。
func TestBrowserEntryPathsAreNotRPCProcedures(t *testing.T) {
	if len(browserEntryPaths) == 0 {
		t.Fatal("放行清单是空的——若确实没有任何浏览器直连入口，应当连同这条用例一起删掉")
	}
	for path := range browserEntryPaths {
		if isInfraProcedure(path) {
			t.Errorf("%q 同时在基础设施前缀清单里：两份清单的类别不同，不能重叠", path)
		}
		// RPC 过程名形如 /<包名>.<领域>.<版本>.<Service>/<Method>，
		// 第一段必带点号。浏览器直连入口不该长成这个样子。
		first := strings.SplitN(strings.TrimPrefix(path, "/"), "/", 2)[0]
		if strings.Contains(first, ".") {
			t.Errorf("%q 看起来是一个 RPC 过程名，不该出现在浏览器直连清单里", path)
		}
	}
}

// 基础设施前缀也**不得**落进浏览器直连清单。
func TestInfraProceduresAreNotBrowserEntries(t *testing.T) {
	for _, prefix := range infraProcedurePrefixes {
		if isBrowserEntry(prefix) {
			t.Errorf("%q 同时在浏览器直连清单里", prefix)
		}
	}
}

// 这些路径若不走放行清单，会被 rbac.Resolve 判成"未声明注解"而拒绝。
// 这正是放行清单存在的理由——这条用例把这个理由固定下来：一旦某条路径变得
// 能被 rbac.Resolve 解析，说明它其实是（或变成了）RPC，清单该重新审视。
func TestBrowserEntriesWouldOtherwiseBeDenied(t *testing.T) {
	for path := range browserEntryPaths {
		if _, err := rbac.Resolve(path); err == nil {
			t.Errorf("路径 %q 能被 rbac.Resolve 解析出注解，说明它可能其实是 RPC", path)
		}
	}
}

// **"地址不对应任何 RPC 方法"不是服务端故障。** 拼错的路径、扫描器、直接访问
// 根路径都会落到这里；它们若以"服务端方法注解缺失"的名义回 500，一次地址打错
// 就会在面板上呈现为服务端坏了，排障的人还会去找一个并不存在的漏写注解
// （见 docs/design/rbac/server-permissions.md 的"拒绝语义"）。
//
// 另一半——"方法存在、注解漏写"仍须是服务端内部错误——由 catalog_test 的全量
// 注解校验守着：真实方法不可能走到这一档。
func TestUnknownPathIsNotAServerFault(t *testing.T) {
	m := &authMiddleware{
		authn:       interceptor.NewTokenAuthenticator(),
		errorWriter: connect.NewErrorWriter(),
		logger:      zap.NewNop(),
	}

	for _, path := range []string{"/", "/nope", "/aladdin.nope.v1.Missing/Do"} {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, path, nil)
			// 指明 Connect 协议，错误体的形状才是确定的。
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()

			reached := false
			m.wrap(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true })).
				ServeHTTP(rec, req)

			if reached {
				t.Fatal("未知路径被放行到了下一个 handler")
			}
			if rec.Code == http.StatusInternalServerError {
				t.Errorf("未知路径回了 500（服务端故障），应当是未实现")
			}
			if body := rec.Body.String(); !strings.Contains(body, "unimplemented") {
				t.Errorf("响应体里没有 unimplemented：%s", body)
			}
		})
	}
}

// **流式响应对包装类型有一条硬要求：它必须自己实现 http.Flusher。**
//
// connect-go 取 Flusher 时做的是**直接的类型断言**（`w.(http.Flusher)`，见它
// protocol.go 的 newStreamingResponseWriter），不是 http.ResponseController——
// 因此 statusRecorder 上那个 Unwrap 帮不上忙。少了这个方法的表现为**流式方法
// 一律 Internal**（"*statusRecorder does not implement http.Flusher"），而同一个
// 服务上的 unary 方法完全看不出来。这条断言把它钉在构建期。
func TestStatusRecorderExposesFlusher(t *testing.T) {
	flushed := false
	var writer http.ResponseWriter = &statusRecorder{
		ResponseWriter: flushingRecorder{flushed: &flushed},
	}

	flusher, ok := writer.(http.Flusher)
	if !ok {
		t.Fatal("statusRecorder 没有实现 http.Flusher：流式方法会在 connect-go 的类型断言处失败")
	}
	flusher.Flush()
	if !flushed {
		t.Error("Flush 没有透传到底层的 ResponseWriter")
	}

	// 底层不是 Flusher 时它不能炸：那种 writer 上本来就没有可刷的东西，而一次
	// panic 会把"包了一层"变成"这个服务不可用"。
	(&statusRecorder{ResponseWriter: plainRecorder{}}).Flush()
}

// 请求留痕带 client：它回答"这次 RPC 是谁发的"，浏览器与命令行的失败模式与
// 排障入口不同，只按过程名分组时分不出来。
func TestRequestLogCarriesClient(t *testing.T) {
	m, logs := newTestTelemetry(t, []string{"/aladdin.rbac.v1.RBACService/"})
	req := httptest.NewRequest(http.MethodPost, "/aladdin.rbac.v1.RBACService/ListRoles", nil)
	req.Header.Set(observability.HeaderClient, observability.ClientWeb)
	m.wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(httptest.NewRecorder(), req)

	fields := logs.All()[0].ContextMap()
	if fields["client"] != observability.ClientWeb {
		t.Errorf("client = %v，期望 %q", fields["client"], observability.ClientWeb)
	}
}

// 不在白名单里的取值**不写这个字段**：写一个未校验的原值会让"按端检索"
// 失去上界（每次扫描器都能造一个新取值）。
func TestRequestLogOmitsUnknownClient(t *testing.T) {
	m, logs := newTestTelemetry(t, []string{"/aladdin.rbac.v1.RBACService/"})
	req := httptest.NewRequest(http.MethodPost, "/aladdin.rbac.v1.RBACService/ListRoles", nil)
	req.Header.Set(observability.HeaderClient, "curl/8")
	m.wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(httptest.NewRecorder(), req)

	if _, ok := logs.All()[0].ContextMap()["client"]; ok {
		t.Error("未知上报端不应写进日志")
	}
}

// 公开路径上带了有效凭证时，主体仍应被识别。
//
// 需要它的只有遥测上报：它既要允许匿名（登录页失败），又要在已登录时把事件归到
// 主体上。识别**不改变任何判定**——公开方法本就放行，因此失败也一律忽略。
func TestPublicPathBestEffortIdentifiesSubject(t *testing.T) {
	const procedure = "/aladdin.telemetry.v1.TelemetryService/ReportEvents"

	machine := interceptor.NewTokenAuthenticator()
	machine.Add("tok", rbac.Subject{ID: "sub_1"})
	m := &authMiddleware{authn: machine, errorWriter: connect.NewErrorWriter(), logger: zap.NewNop()}

	cases := []struct {
		name, auth string
		want       bool
	}{
		{"带有效凭证", "Bearer tok", true},
		{"匿名", "", false},
		{"凭证无效", "Bearer nope", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, procedure, nil)
			if tc.auth != "" {
				req.Header.Set("Authorization", tc.auth)
			}
			var got bool
			m.wrap(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				_, got = interceptor.SubjectFromContext(r.Context())
			})).ServeHTTP(httptest.NewRecorder(), req)
			if got != tc.want {
				t.Errorf("主体识别 = %v，期望 %v", got, tc.want)
			}
		})
	}
}

// flushingRecorder 是一个会记下"刷过没有"的 ResponseWriter。
type flushingRecorder struct {
	http.ResponseWriter
	flushed *bool
}

func (f flushingRecorder) Flush() { *f.flushed = true }

// plainRecorder 实现了 http.ResponseWriter，但**不**实现 http.Flusher。
type plainRecorder struct {
	http.ResponseWriter
}
