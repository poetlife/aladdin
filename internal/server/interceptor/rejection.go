package interceptor

import (
	"errors"

	"connectrpc.com/connect"

	rbacv1 "github.com/poetlife/aladdin/api/gen/aladdin/rbac/v1"
	"github.com/poetlife/aladdin/internal/rbac"
)

// reject 把判定结论转换为 Connect 错误。
//
// 状态码的选取对应 docs/design/rbac/server-permissions.md 的拒绝语义表。
// **最关键的一条：不得把"无权限"与"未认证"混为一谈。**
// 混用会导致客户端在权限不足时反复尝试刷新凭证，把一次权限配置错误
// 放大成登录风暴。
//
// 拒绝原因以结构化的 DenialDetail 挂在错误详情里，客户端三端
// （Go / TypeScript / CLI）都从同一个生成类型读取，不做文本匹配。
func reject(reason rbac.Reason) error {
	code, message := codeAndMessage(reason)
	err := connect.NewError(code, errors.New(message))
	// 详情构造失败时退回不带详情的错误：拒绝语义本身比详情更重要。
	if detail, detailErr := connect.NewErrorDetail(&rbacv1.DenialDetail{Reason: reason}); detailErr == nil {
		err.AddDetail(detail)
	}
	return err
}

func codeAndMessage(reason rbac.Reason) (connect.Code, string) {
	switch reason {
	case rbac.ReasonNoMatchingGrant:
		return connect.CodePermissionDenied, "权限不足"
	case rbac.ReasonScopeMismatch:
		// 同样是"权限不足"，但语义上是作用域不够，客户端据此提示
		// "需要更高层级授权"而不是"联系管理员"。
		return connect.CodePermissionDenied, "当前作用域下权限不足，需要更高层级授权"
	case rbac.ReasonSessionExpired:
		return connect.CodeUnauthenticated, "凭证已失效，请重新登录"
	case rbac.ReasonSubjectNotFound:
		return connect.CodeUnauthenticated, "主体不存在或已停用"
	case rbac.ReasonStoreUnavailable:
		return connect.CodeUnavailable, "权限服务暂时不可用，请稍后重试"
	default:
		return connect.CodeInternal, "鉴权失败"
	}
}

// DenyByAnnotation 用于方法注解缺失或非法时的拒绝。
//
// 它与 reject 分开，是因为这一类属于**服务端配置缺陷**而非调用方的权限问题：
// 返回 Internal 而不是 PermissionDenied，避免让调用方误以为是自己的问题。
// metadata 里带上方法名，使排障能直接定位到漏写注解的那个方法。
func DenyByAnnotation(procedure, reason string) error {
	err := connect.NewError(connect.CodeInternal, errors.New("服务端方法注解缺失，已默认拒绝"))
	detail := &rbacv1.DenialDetail{
		Reason: rbacv1.DenialReason_DENIAL_REASON_ANNOTATION_MISSING,
		Detail: reason,
		Metadata: map[string]string{
			"method": procedure,
		},
	}
	if connectDetail, detailErr := connect.NewErrorDetail(detail); detailErr == nil {
		err.AddDetail(connectDetail)
	}
	return err
}

// RejectAuthFailure 把认证失败转换为 Connect 错误。
//
// 认证失败统一映射为 Unauthenticated：无论"没带凭证"还是"凭证无效"，
// 客户端要做的都是引导用户重新登录，区分这两者对用户没有价值。
//
// **但存储故障不是认证失败。** 把它映射成 Unauthenticated，会让所有客户端
// 同时被引导重新登录，而重新登录同样失败——一次数据库抖动于是被放大成
// 一次全站登录风暴。它必须单独成类，让客户端退避重试。
func RejectAuthFailure(err error) error {
	if errors.Is(err, ErrStoreUnavailable) {
		return reject(rbac.ReasonStoreUnavailable)
	}
	if errors.Is(err, ErrNoCredential) || errors.Is(err, ErrInvalidCredential) {
		return reject(rbac.ReasonSessionExpired)
	}
	return reject(rbac.ReasonSubjectNotFound)
}
