package interceptor

import (
	"context"
	"errors"
	"net/http"

	"connectrpc.com/connect"
	"go.uber.org/zap"
	"google.golang.org/protobuf/proto"

	"github.com/poetlife/aladdin/internal/rbac"
)

// Authorizer 持有鉴权所需的依赖。
//
// 它只做鉴权（你能做什么）。认证（你是谁）由 HTTP 中间件完成并把主体
// 放进 context，这里只读取。
type Authorizer struct {
	Engine *rbac.Engine
	Logger *zap.Logger
}

// Interceptor 返回 Connect 的 unary 鉴权拦截器。
//
// 它同时适用于三种协议（Connect / gRPC / gRPC-Web），因为 Connect 在
// 协议层之下把请求统一成 AnyRequest，注解解析与判定逻辑只写一份。
func (a *Authorizer) Interceptor() connect.UnaryInterceptorFunc {
	return func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			procedure := req.Spec().Procedure

			rule, err := Resolve(procedure)
			if err != nil {
				return nil, DenyByAnnotation(procedure, err.Error())
			}
			if rule.Kind == KindDenied {
				return nil, DenyByAnnotation(procedure, rule.Reason)
			}
			if rule.Kind == KindPublic {
				return next(ctx, req)
			}

			// 主体由 HTTP 中间件放入；缺失说明中间件链路配错了，
			// 而不是调用方的问题——按未认证处理并留痕。
			subject, ok := SubjectFromContext(ctx)
			if !ok {
				return nil, reject(rbac.ReasonSessionExpired)
			}
			if rule.Kind == KindAuthenticatedOnly {
				return next(ctx, req)
			}

			scope, err := resolveScope(rule.ScopeFrom, subject, req.Header(), req.Any())
			if err != nil {
				// 解析失败**不降级为全局作用域**——降级会让一次配置错误
				// 变成一次越权。失败即以"作用域不符"拒绝。
				return nil, reject(rbac.ReasonScopeMismatch)
			}

			decision := a.Engine.Check(ctx, subject, rule.Permission, scope)
			if !decision.Allowed {
				return nil, reject(decision.Reason)
			}
			return next(ctx, req)
		}
	}
}

// resolveScope 按注解声明的作用域来源解析本次调用的作用域。
func resolveScope(from scopeSource, subject rbac.Subject, header http.Header, req any) (rbac.Scope, error) {
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
		return scopeFromRequestField(req)
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
