package interceptor

import (
	"context"
	"errors"

	"connectrpc.com/connect"
	"go.uber.org/zap"

	errorsv1 "github.com/poetlife/aladdin/api/gen/aladdin/errors/v1"
	"github.com/poetlife/aladdin/internal/errs"
)

// FailureInterceptor 把业务方法返回的领域错误编码成 Connect 错误。
//
// 它是"失败 → 对外错误"的**唯一转换点**。有了它，服务方法只返回领域错误，
// 不再各自维护一份"错误映射表"；也正因为只有这一处，同一个原因不可能在
// 两个方法里给出两个传输码。
//
// 它同时覆盖 unary 与 server stream：两条入口的差别只有"过程名从哪取"，
// 转换本身是同一段代码。
type FailureInterceptor struct {
	// Logger 用于记录"内部失败"的因果链。未分类的错误也在这里留痕，
	// 否则它对外只剩一句固定文案，排障时无从下手。
	Logger *zap.Logger
}

// WrapUnary 实现 connect.Interceptor：unary 调用。
func (i *FailureInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		resp, err := next(ctx, req)
		return resp, i.convert(req.Spec().Procedure, err)
	}
}

// WrapStreamingHandler 实现 connect.Interceptor：服务端流。
func (i *FailureInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		return i.convert(conn.Spec().Procedure, next(ctx, conn))
	}
}

// WrapStreamingClient 是空实现：转换只发生在服务端。
func (i *FailureInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

// failureReasonOf 从已编码的错误详情里取回失败原因。
//
// 它只用于日志：已编码错误的分类在详情里，不在错误类型上。
func failureReasonOf(ce *connect.Error) string {
	for _, d := range ce.Details() {
		msg, err := d.Value()
		if err != nil {
			continue
		}
		if detail, ok := msg.(*errorsv1.FailureDetail); ok {
			return detail.GetReason().String()
		}
	}
	return ""
}

// convert 是这一层唯一的判断：已经编码的原样透传，已分类的补详情，
// 其余按内部失败处理并留痕。
//
// 透传的对象包括鉴权拦截器产出的拒绝、传输层的取消与超时。把它们再包一层
// 会改变协议语义，也会让同一个失败出现两个原因（见 docs/design/errors/README.md
// 的「唯一的转换点」）。
func (i *FailureInterceptor) convert(procedure string, err error) error {
	if err == nil {
		return nil
	}

	target, classified := errs.From(err)
	if classified {
		// 内部失败按错误级别记录：它要么是注解缺失这类配置缺陷，要么是
		// 未预料的实现问题，都属于"该有人来看一眼"的那一档。
		if target.Code() == connect.CodeInternal && i.Logger != nil {
			i.Logger.Error("服务端内部失败",
				zap.String("procedure", procedure),
				zap.String("reason", target.Reason().String()),
				zap.Error(err),
			)
		}
		return errs.ToConnect(target)
	}

	var connectErr *connect.Error
	if errors.As(err, &connectErr) {
		// 鉴权拦截器在更内层就把拒绝编码好了，因此它不会经过上面的分类分支。
		// 内部失败仍要留痕——注解缺失正是这一档，排障的人需要过程名与原因。
		if connectErr.Code() == connect.CodeInternal && i.Logger != nil {
			i.Logger.Error("服务端内部失败",
				zap.String("procedure", procedure),
				zap.String("reason", failureReasonOf(connectErr)),
				zap.Error(connectErr),
			)
		}
		// 取出里面的 Connect 错误而不是退回外层包装：connect-go 只认
		// *connect.Error，交给它一个包装过的值会被当成 Unknown。
		return connectErr
	}

	if i.Logger != nil {
		i.Logger.Error("未分类的失败，按内部错误对外",
			zap.String("procedure", procedure),
			zap.String("reason", errorsv1.FailureReason_FAILURE_REASON_INTERNAL_UNEXPECTED.String()),
			zap.Error(err),
		)
	}
	return errs.ToConnect(err)
}
