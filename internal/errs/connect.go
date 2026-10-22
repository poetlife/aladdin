package errs

import (
	"errors"

	"connectrpc.com/connect"

	errorsv1 "github.com/poetlife/aladdin/api/gen/aladdin/errors/v1"
)

// ToConnect 把任意错误编码成 Connect 错误。
//
// 它是"失败 → 对外错误"的**唯一转换点**：服务方法返回领域错误，RPC 边界
// 调用它一次。业务代码不再各自决定传输码与文案。
//
// 三种错误原样透传，不二次包装：
//
//   - 已经带分类的错误（最外层是 *Error）；
//   - 传输层已经编码好的错误（*connect.Error）；
//   - 包在别的错误里的 *connect.Error（例如中间件或框架产生的取消、超时）。
//
// 包装它们会改变协议语义——把一次"取消"变成"内部错误"——也会让同一个失败
// 出现两个原因。
//
// 没有分类的错误按"服务端内部失败"处理：对外固定文案，原始错误只进日志。
func ToConnect(err error) *connect.Error {
	if err == nil {
		return nil
	}

	// 先认领我们自己的分类：如果调用方显式给了一次失败贴上原因，
	// 这个原因比它内部包着的传输错误更权威。
	target, ok := From(err)
	if !ok {
		var ce *connect.Error
		if errors.As(err, &ce) {
			return ce
		}
		target = Internal(err)
	}

	out := connect.NewError(target.Code(), errors.New(target.Message()))
	detail := &errorsv1.FailureDetail{
		Reason:  target.Reason(),
		Message: target.Message(),
	}
	if metadata := target.Metadata(); len(metadata) > 0 {
		detail.Metadata = metadata
	}
	if encoded, detailErr := connect.NewErrorDetail(detail); detailErr == nil {
		out.AddDetail(encoded)
	}
	// 详情构造失败只可能是消息形状不合法（本包不会产出这种消息）。
	// 此时仍返回错误本身：失败语义比详情更重要，调用方至少拿到正确的码。
	return out
}
