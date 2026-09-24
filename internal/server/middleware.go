package server

import (
	"errors"
	"net/http"
	"strings"

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

// withTraceID 生成或继承链路标识。
//
// 它必须排在最外层：被拒绝的请求同样需要留痕，否则无法回答
// "是谁在反复尝试越权"。
func withTraceID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := observability.EnsureTraceID(r.Context())
		if incoming := r.Header.Get(observability.TraceIDHeader); incoming != "" {
			ctx = observability.WithTraceID(ctx, incoming)
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

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
