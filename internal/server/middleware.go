package server

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"
	"go.uber.org/zap"

	"github.com/poetlife/aladdin/internal/observability"
	"github.com/poetlife/aladdin/internal/server/interceptor"
)

// infraProcedurePrefixes 是不参与业务鉴权的基础设施服务。
//
// 它们由 Connect 官方组件提供（健康检查、反射），没有 aladdin 的权限注解，
// 因此必须显式放行。这与"未声明注解即默认拒绝"并不矛盾：那条规则针对的是
// **业务方法**，而这两个是框架服务，由编排系统与调试工具调用。
//
// 这份清单应当保持极短。任何业务方法进入它都是缺陷。
var infraProcedurePrefixes = []string{
	"/grpc.health.v1.Health/",
	"/grpc.reflection.v1.",
	"/grpc.reflection.v1alpha.",
}

func isInfraProcedure(path string) bool {
	for _, prefix := range infraProcedurePrefixes {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}

// procedureUnmatched 是未落在任何已注册服务下的路径共用的指标属性值。
//
// 直接拿 r.URL.Path 当指标属性会让任意路径（扫描器、拼错的地址）各占一个
// 时序，时序数量随流量增长——这正是"指标属性必须有界"要防的事。
const procedureUnmatched = "unmatched"

// telemetryMiddleware 在 HTTP 层起 server span、回写链路标识并记录请求指标。
//
// 它必须排在最外层：被拒绝的请求同样需要留痕，否则无法回答
// "是谁在反复尝试越权"。
//
// 三种 RPC 协议（Connect / gRPC / gRPC-Web）的元数据都落在 HTTP 头里，
// 因此**只有这一处提取点**，一次实现同时覆盖三种协议。
type telemetryMiddleware struct {
	metrics *observability.Metrics
	logger  *zap.Logger
	// registeredPaths 是注册到 mux 的全部路径，用于把请求路径归一成有界取值。
	registeredPaths []string
}

func (m *telemetryMiddleware) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		procedure := m.procedureOf(r.URL.Path)
		ctx, _ := observability.StartServerSpan(r.Context(), r.Header, procedure)

		// 响应头必须在写出任何响应字节之前设置，否则会被丢弃。
		// 跨域部署时还需要配合 Access-Control-Expose-Headers 把它们暴露给浏览器
		// （见 docs/observability.md）。
		observability.WriteTraceHeaders(ctx, w.Header())

		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		started := time.Now()
		defer func() {
			elapsed := time.Since(started)
			observability.EndServerSpan(ctx, rec.status)
			m.metrics.ServerRequest(ctx, procedure, strconv.Itoa(rec.status), elapsed)
			m.logRequest(ctx, procedure, rec.status, elapsed)
		}()

		next.ServeHTTP(rec, r.WithContext(ctx))
	})
}

// logRequest 为每个完成的请求留一行。
//
// 这是"拿着 trace_id 就能搜到东西"的前提。在这之前，服务端唯一会写 trace_id
// 的地方是 RBAC 判定留痕，于是 Login / Refresh / WhoAmI / GetSessionPermissions
// 这些不走判定的请求虽然起了 span、也回写了响应头，却一行日志都没有——
// 拿着它们返回的 trace_id 去搜日志会一无所获，而浏览器打开页面时最先发的
// 恰恰就是这几个方法。
//
// 只记过程名、结果码、耗时与主体标识，**不记请求体**：Login 的请求体里装的
// 就是凭证，而凭证绝不允许进日志（见 docs/design/config/credentials.md）。
func (m *telemetryMiddleware) logRequest(ctx context.Context, procedure string, status int, elapsed time.Duration) {
	logger := observability.SpanLogger(ctx, m.logger)
	if logger == nil {
		return
	}
	fields := []zap.Field{
		zap.String("procedure", procedure),
		zap.Int("status", status),
		// 毫秒而不是 zap.Duration 的秒：秒的浮点数（0.000617208）要数零才读得出，
		// 而毫秒既可读、又能直接用查询语句比较。键名带单位，避免读者去猜。
		zap.Float64("duration_ms", elapsed.Seconds()*1000),
	}
	if subject, ok := interceptor.SubjectFromContext(ctx); ok {
		fields = append(fields, zap.String("subject_id", subject.ID))
	}

	// 高频探针与未匹配到任何服务的路径（扫描器、拼错的地址）降到 DEBUG：
	// 它们同样需要可追溯，但不该把日志淹成心跳与噪声。
	if isInfraProcedure(procedure) || procedure == procedureUnmatched {
		logger.Debug("请求完成", fields...)
		return
	}
	logger.Info("请求完成", fields...)
}

// procedureOf 把请求路径归一成有界的指标属性值。
func (m *telemetryMiddleware) procedureOf(path string) string {
	for _, registered := range m.registeredPaths {
		if strings.HasPrefix(path, registered) {
			return path
		}
	}
	return procedureUnmatched
}

// statusRecorder 记录响应状态码，供 span 与请求指标使用。
type statusRecorder struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (r *statusRecorder) WriteHeader(code int) {
	if !r.wrote {
		r.status = code
		r.wrote = true
	}
	r.ResponseWriter.WriteHeader(code)
}

// Write 记录"已经写过响应"，避免后续的 WriteHeader 把状态码改掉。
//
// 不写响应头直接写体时，net/http 隐含 200，这里必须与之保持一致。
func (r *statusRecorder) Write(b []byte) (int, error) {
	r.wrote = true
	return r.ResponseWriter.Write(b)
}

// Unwrap 让 http.ResponseController 能找到被包装的 ResponseWriter，
// 从而保住 Flusher 等可选接口。
//
// 少了它会直接坏掉流式响应——反射服务就是流式的，而它的失败方式
// （连接挂住而不是报错）极难归因。
func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

// authMiddleware 在 HTTP 层完成认证。
//
// 为什么认证不放在 Connect 拦截器里：拦截器在请求体读取并解压**之后**才执行，
// 把认证放那里意味着未认证的请求也会先被完整读一遍。放在 HTTP 层可以在
// 读取任何字节之前拒绝，connect 官方也这样建议。
//
// 拒绝时用 connect.ErrorWriter 写出错误：它按请求的 Content-Type 识别协议，
// 对 Connect / gRPC / gRPC-Web 分别编码（gRPC 走 trailer）。
// 没有它，中间件写出的 JSON 会被 grpc-go 客户端当成传输层故障，
// CLI 就拿不到正确的退出码了。
type authMiddleware struct {
	authn       interceptor.Authenticator
	errorWriter *connect.ErrorWriter
	logger      *zap.Logger
}

func (m *authMiddleware) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if isInfraProcedure(path) {
			next.ServeHTTP(w, r)
			return
		}

		rule, err := interceptor.Resolve(path)
		if err != nil {
			m.reject(w, r, interceptor.DenyByAnnotation(path, err.Error()))
			return
		}
		switch rule.Kind {
		case interceptor.KindDenied:
			m.reject(w, r, interceptor.DenyByAnnotation(path, rule.Reason))
			return
		case interceptor.KindPublic:
			// 公开方法不需要认证，直接交给 handler。
			next.ServeHTTP(w, r)
			return
		}

		subject, err := m.authn.Authenticate(r.Context(), r.Header)
		if err != nil {
			m.reject(w, r, interceptor.RejectAuthFailure(err))
			return
		}
		// 主体放入 context 后交给 Connect handler，
		// 鉴权拦截器在同一 context 上读取它。
		next.ServeHTTP(w, r.WithContext(interceptor.WithSubject(r.Context(), subject)))
	})
}

// httpStatusFor 把 Connect 错误码映射为普通 HTTP 状态码。
//
// 只用于**非 RPC 请求**的兜底响应；RPC 客户端看到的错误形状由
// ErrorWriter 决定，不经过这里。
func httpStatusFor(code connect.Code) int {
	switch code {
	case connect.CodeUnauthenticated:
		return http.StatusUnauthorized
	case connect.CodePermissionDenied:
		return http.StatusForbidden
	case connect.CodeNotFound:
		return http.StatusNotFound
	case connect.CodeUnavailable:
		return http.StatusServiceUnavailable
	case connect.CodeInvalidArgument:
		return http.StatusBadRequest
	default:
		return http.StatusInternalServerError
	}
}

// reject 按客户端所用协议写出错误。
//
// ErrorWriter 处理不了非 RPC 请求（IsSupported 返回 false）时，
// 退回普通 HTTP 错误——这类请求本来就不是 RPC 调用方发的。
func (m *authMiddleware) reject(w http.ResponseWriter, r *http.Request, err error) {
	if m.errorWriter.IsSupported(r) {
		if writeErr := m.errorWriter.Write(w, r, err); writeErr != nil {
			m.logger.Error("写出拒绝响应失败",
				zap.String("path", r.URL.Path), zap.Error(writeErr))
		}
		return
	}
	// 非 RPC 请求（浏览器直连路径、探针等）没有协议可依，退回普通 HTTP 错误。
	code := connect.CodeOf(err)
	message := err.Error()
	var connectErr *connect.Error
	if errors.As(err, &connectErr) {
		message = connectErr.Message()
	}
	http.Error(w, message, httpStatusFor(code))
}
