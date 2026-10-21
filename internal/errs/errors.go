// Package errs 是 aladdin 里"一次失败"的唯一表示。
//
// 它把三件事绑在一起：客户端可分支的失败原因、这次失败对应的传输码、
// 以及一句可展示的文案。三者由 proto 里的失败目录派生（见
// docs/design/errors/README.md），因此同一个原因在所有方法里只有一个码。
//
// 领域代码返回这里的错误；把它编码成 Connect 错误只发生在 RPC 边界的一处
// （见 ToConnect）。业务方法不再各自决定传输码与文案。
package errs

import (
	"errors"
	"fmt"

	"connectrpc.com/connect"

	errorsv1 "github.com/poetlife/aladdin/api/gen/aladdin/errors/v1"
)

// failureValue 是生成目录里的一条记录：一个原因对应的传输码与默认文案。
type failureValue struct {
	code connect.Code
	text string
}

// Error 是一次带分类的失败。
//
// 它的零值没有意义：必须经由 New / Newf / Wrap / Wrapf 构造，或用生成好的
// ErrXxx 值。code 由 reason 决定，调用方不能单独指定——允许"同一个原因在
// 两个方法里给两个码"正是这套设计要消灭的东西。
type Error struct {
	code     connect.Code
	reason   errorsv1.FailureReason
	message  string
	metadata map[string]string
	cause    error
}

// New 按失败原因构造一个错误，传输码与文案取自生成目录。
//
// reason 必须是 proto 里声明过的非零取值。未登记的原因属于构建期缺陷
// （改 proto 后忘了生成），在初始化或测试里直接暴露，而不是静默变成
// 另一个原因。
func New(reason errorsv1.FailureReason) *Error {
	value, ok := failureValues[reason]
	if !ok {
		panic(fmt.Sprintf("errs.New: 未登记的失败原因 %v，请改 proto 并运行 make gen", reason))
	}
	return &Error{code: value.code, reason: reason, message: value.text}
}

// Newf 与 New 相同，但用给定文案替换默认文案。
//
// 只用于补充上下文（哪一项超限、被哪个版本引用）。文案只用于展示，
// 客户端不得据此分支；需要分支的信息进 reason 或 metadata。
func Newf(reason errorsv1.FailureReason, format string, args ...any) *Error {
	err := New(reason)
	err.message = fmt.Sprintf(format, args...)
	return err
}

// Wrap 把原因与因果链绑在一起。
//
// cause 只进日志，不上线：编码成对外错误时用的是 Error.Message()。
// cause 为 nil 时等价于 New。
func Wrap(cause error, reason errorsv1.FailureReason) *Error {
	err := New(reason)
	err.cause = cause
	return err
}

// Wrapf 与 Wrap 相同，但用给定文案替换默认文案。
func Wrapf(cause error, reason errorsv1.FailureReason, format string, args ...any) *Error {
	err := Wrap(cause, reason)
	err.message = fmt.Sprintf(format, args...)
	return err
}

// Internal 把任意错误归为"服务端内部失败"。
//
// 对外固定文案，原始错误只进日志。未分类的错误走这里，因此内部细节
// （数据库语句、对象存储地址、路径）不会随错误消息泄漏给调用方。
func Internal(cause error) *Error {
	return Wrap(cause, errorsv1.FailureReason_FAILURE_REASON_INTERNAL_UNEXPECTED)
}

// From 从错误链里取回最外层的 *Error。
//
// 返回 false 表示这个错误没有经过分类（例如传输层产生的错误）。
func From(err error) (*Error, bool) {
	var target *Error
	if errors.As(err, &target) {
		return target, true
	}
	return nil, false
}

// Is 报告错误链里最外层的原因是不是给定原因。
//
// 与 errors.Is 的分工：那个比较错误值，这个比较失败原因。调用方不需要
// 知道是哪个 ErrXxx 值被返回，只需要知道"这是不是'版本不存在'"。
func Is(err error, reason errorsv1.FailureReason) bool {
	target, ok := From(err)
	return ok && target.reason == reason
}

// Code 返回这次失败的传输码。
func (e *Error) Code() connect.Code { return e.code }

// Reason 返回这次失败的分类。
func (e *Error) Reason() errorsv1.FailureReason { return e.reason }

// Message 返回可以发给调用方的文案，不含因果链。
func (e *Error) Message() string { return e.message }

// Metadata 返回结构化补充信息的一份副本。
func (e *Error) Metadata() map[string]string {
	if len(e.metadata) == 0 {
		return nil
	}
	out := make(map[string]string, len(e.metadata))
	for k, v := range e.metadata {
		out[k] = v
	}
	return out
}

// WithMetadata 返回带一条补充信息的新错误。
//
// 它不改动接收者：生成好的 ErrXxx 是共享值，原地修改会让一次请求的
// 上下文出现在另一次请求里。
func (e *Error) WithMetadata(key, value string) *Error {
	clone := *e
	clone.metadata = make(map[string]string, len(e.metadata)+1)
	for k, v := range e.metadata {
		clone.metadata[k] = v
	}
	clone.metadata[key] = value
	return &clone
}

// Is 让 errors.Is 按**失败原因**匹配，而不是按错误值指针。
//
// 领域代码返回的可能是包装过的错误（带上下文或因果链），调用方仍然可以写
// errors.Is(err, errs.ErrProjectNotFound)：同一原因就是同一类失败，不需要
// 知道具体是哪个包装层。
func (e *Error) Is(target error) bool {
	other, ok := target.(*Error)
	return ok && other != nil && e.reason == other.reason
}

// Unwrap 让 errors.As / errors.Is 能穿过包装找到因果链。
func (e *Error) Unwrap() error { return e.cause }

// Error 是给日志看的字符串：文案加因果链。
//
// 对外编码不使用它（用 Message），因此因果链不会随错误消息上线。
func (e *Error) Error() string {
	if e.cause == nil {
		return e.message
	}
	return e.message + ": " + e.cause.Error()
}
