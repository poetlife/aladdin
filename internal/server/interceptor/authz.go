package interceptor

import (
	"context"
	"errors"
	"net/http"

	"connectrpc.com/connect"
	"go.uber.org/zap"
	"google.golang.org/protobuf/proto"

	rbacv1 "github.com/poetlife/aladdin/api/gen/aladdin/rbac/v1"
	"github.com/poetlife/aladdin/internal/rbac"
)

// scopeSource 是作用域来源的短别名，避免每处都写完整的包名前缀。
// 注解的解析在 rbac 包；这里只保留"如何按来源取到作用域"这一层。
type scopeSource = rbacv1.ScopeSource

const (
	scopeSourceRequestField = rbacv1.ScopeSource_SCOPE_SOURCE_REQUEST_FIELD
	scopeSourceMetadata     = rbacv1.ScopeSource_SCOPE_SOURCE_METADATA
	scopeSourceCredential   = rbacv1.ScopeSource_SCOPE_SOURCE_CREDENTIAL
)

// Authorizer 持有鉴权所需的依赖。
//
// 它只做鉴权（你能做什么）。认证（你是谁）由 HTTP 中间件完成并把主体
// 放进 context，这里只读取。
type Authorizer struct {
	Engine *rbac.Engine
	Logger *zap.Logger
}

// Interceptor 返回 Connect 的鉴权拦截器。
//
// 它同时适用于三种协议（Connect / gRPC / gRPC-Web），因为 Connect 在协议层
// 之下把请求统一成一个形状，注解解析与判定逻辑只写一份。
//
// **它也同时覆盖 unary 与 server stream。** 两者的差别只有"过程名与请求头从哪
// 取"，判定本身是同一段代码；拆成两个拦截器之后，"判定逻辑只有一处"就得靠人
// 记住去同步两个文件——而漏同步的表现是一次静默放行。
func (a *Authorizer) Interceptor() connect.Interceptor {
	return authorizerInterceptor{authorizer: a}
}

// authorizerInterceptor 把 decide 接到 Connect 的两个入口上。
type authorizerInterceptor struct {
	authorizer *Authorizer
}

// WrapUnary 实现 connect.Interceptor：unary 调用。
func (i authorizerInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		if err := i.authorizer.decide(ctx, req.Spec().Procedure, req.Header(), req.Any()); err != nil {
			return nil, err
		}
		return next(ctx, req)
	}
}

// WrapStreamingHandler 实现 connect.Interceptor：流式调用。
//
// 过程名与请求头在连接上就有，判定因此与 unary 完全共用。**请求消息拿不到**：
// 框架在调用被包装的这个函数之后才去读流上那一条消息（读走它就轮到 handler
// 读不到了）。因此流式方法只能从凭证或请求头取作用域——"从请求字段取"在流上
// 不可表达。这里传 nil 是**兜底**：真有人这么声明时，解析会失败并以"作用域不符"
// 拒绝，而不是放行；构建期则由 internal/rbac 的不变量测试直接挡住（见
// docs/design/events/README.md 的边界一节）。
func (i authorizerInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		if err := i.authorizer.decide(ctx, conn.Spec().Procedure, conn.RequestHeader(), nil); err != nil {
			return err
		}
		return next(ctx, conn)
	}
}

// WrapStreamingClient 是空实现：鉴权只发生在服务端，客户端那一侧没有可判的东西。
func (i authorizerInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

// decide 是鉴权判定的**唯一实现**，unary 与流式两条入口共用它。
//
// requestMessage 只在作用域声明为"从请求字段取"时用到；流式调用传 nil（它拿不到
// 那条消息，见 WrapStreamingHandler）。
func (a *Authorizer) decide(ctx context.Context, procedure string, header http.Header, requestMessage any) error {
	rule, err := rbac.Resolve(procedure)
	if err != nil {
		// "这个地址不是 RPC 方法"与"方法漏写注解"是两回事，错误码也不同
		// （见 docs/design/rbac/server-permissions.md 的"拒绝语义"）。
		if errors.Is(err, rbac.ErrUnknownProcedure) {
			return RejectUnknownProcedure(procedure)
		}
		return DenyByAnnotation(procedure, err.Error())
	}
	if rule.Kind == rbac.KindDenied {
		return DenyByAnnotation(procedure, rule.Reason)
	}
	if rule.Kind == rbac.KindPublic {
		return nil
	}

	// 主体由 HTTP 中间件放入；缺失说明中间件链路配错了，
	// 而不是调用方的问题——按未认证处理并留痕。
	subject, ok := SubjectFromContext(ctx)
	if !ok {
		return Reject(rbac.ReasonSessionExpired)
	}
	if rule.Kind == rbac.KindAuthenticatedOnly {
		return nil
	}

	scope, err := resolveScope(rule.ScopeFrom, subject, header, requestMessage)
	if err != nil {
		// 解析失败**不降级为全局作用域**——降级会让一次配置错误
		// 变成一次越权。失败即以"作用域不符"拒绝。
		return Reject(rbac.ReasonScopeMismatch)
	}

	decision := a.Engine.Check(ctx, subject, rule.Permission, scope)
	if !decision.Allowed {
		return Reject(decision.Reason)
	}
	return nil
}

// resolveScope 按注解声明的作用域来源解析本次调用的作用域。
//
// requestMessage 只在来源是请求字段时用到；流式调用传 nil（见
// WrapStreamingHandler）。
func resolveScope(from scopeSource, subject rbac.Subject, header http.Header, requestMessage any) (rbac.Scope, error) {
	switch from {
	case scopeSourceCredential:
		return subject.DefaultScope, nil
	case scopeSourceMetadata:
		scope, ok := ScopeFromHeader(header)
		if !ok {
			return "", errors.New("请求头中缺少作用域")
		}
		return scope, nil
	case scopeSourceRequestField:
		return scopeFromRequestField(requestMessage)
	default:
		return "", errors.New("未声明作用域来源")
	}
}

// scopeFromRequestField 从请求消息的 scope 字段读取作用域。
//
// 用反射而非为每个消息写一遍，是为了让新增接口不必同步新增解析代码。
func scopeFromRequestField(req any) (rbac.Scope, error) {
	msg, ok := req.(proto.Message)
	if !ok {
		return "", errors.New("请求不是 protobuf 消息")
	}
	fields := msg.ProtoReflect().Descriptor().Fields()
	field := fields.ByName("scope")
	if field == nil {
		return "", errors.New("请求消息没有 scope 字段")
	}
	return rbac.Scope(msg.ProtoReflect().Get(field).String()), nil
}
