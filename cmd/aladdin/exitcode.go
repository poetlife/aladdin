package main

import (
	"errors"
	"fmt"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/poetlife/aladdin/internal/auth"
)

// CLI 退出码。四类失败必须互不相同，脚本才能据此分支处理
// （见 docs/design/rbac/cli-permissions.md 的退出码约定）。
//
// 这些数值是**脚本契约**：变更即破坏性变更，需同步更新文档与测试。
const (
	exitOK               = 0
	exitFailure          = 1 // 未分类的失败
	exitUsage            = 2 // 用法错误（参数不合法）
	exitUnauthenticated  = 3 // 未认证或凭证过期，脚本应触发重新登录
	exitPermissionDenied = 4 // 权限不足，脚本不应重试
	exitUnavailable      = 5 // 服务端不可用，脚本可退避重试
)

// usageError 标记调用方的用法错误。
//
// 单独成类型而不是靠错误文本匹配，是因为退出码是脚本契约：
// 它必须由一个可被编译器检查的信号驱动，而不是一句措辞。
type usageError struct{ msg string }

// Error 实现 error。
func (e *usageError) Error() string { return e.msg }

// usageErrorf 构造一个用法错误。
func usageErrorf(format string, args ...any) error {
	return &usageError{msg: fmt.Sprintf(format, args...)}
}

// exitCodeFor 把错误映射为退出码。
func exitCodeFor(err error) int {
	if err == nil {
		return exitOK
	}
	switch {
	case errors.Is(err, auth.ErrNoCredential), errors.Is(err, auth.ErrCredentialFileInsecure):
		return exitUnauthenticated
	}

	var ue *usageError
	if errors.As(err, &ue) {
		return exitUsage
	}

	st, ok := status.FromError(err)
	if !ok {
		return exitFailure
	}
	switch st.Code() {
	case codes.OK:
		return exitOK
	case codes.InvalidArgument, codes.FailedPrecondition, codes.OutOfRange, codes.NotFound:
		return exitUsage
	case codes.Unauthenticated:
		return exitUnauthenticated
	case codes.PermissionDenied:
		return exitPermissionDenied
	case codes.Unavailable, codes.DeadlineExceeded:
		return exitUnavailable
	default:
		return exitFailure
	}
}
