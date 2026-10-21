package interceptor

import (
	"errors"

	"connectrpc.com/connect"

	errorsv1 "github.com/poetlife/aladdin/api/gen/aladdin/errors/v1"
	rbacv1 "github.com/poetlife/aladdin/api/gen/aladdin/rbac/v1"
	"github.com/poetlife/aladdin/internal/errs"
	"github.com/poetlife/aladdin/internal/rbac"
)

// Reject 把判定结论转换为 Connect 错误。
//
// 它是"拒绝原因 → RPC 错误"的**唯一实现**：鉴权拦截器用它，事件通道的逐主题
// 判定也用它（见 internal/server/events_service.go 的 Watch）。多写一份映射就会
// 出现"同一个拒绝原因，两条路径给出不同的码"。
//
// 状态码的选取对应 docs/design/rbac/server-permissions.md 的拒绝语义表。
// **最关键的一条：不得把"无权限"与"未认证"混为一谈。**
// 混用会导致客户端在权限不足时反复尝试刷新凭证，把一次权限配置错误
// 放大成登录风暴。
//
// 迁移期同时携带两种详情：新的 FailureDetail 是后续唯一契约，旧的
// DenialDetail 是已经发布出去的形状，供尚未升级的客户端读取。删除条件写在
// docs/design/errors/README.md 的「与 DenialDetail 的迁移关系」。
func Reject(reason rbac.Reason) error {
	encoded := errs.ToConnect(failureFor(reason))
	// 详情构造失败时退回不带详情的错误：拒绝语义本身比详情更重要。
	if detail, detailErr := connect.NewErrorDetail(&rbacv1.DenialDetail{Reason: reason}); detailErr == nil {
		encoded.AddDetail(detail)
	}
	return encoded
}

// failureFor 把鉴权判定原因映射为失败目录里的分类。
//
// 映射只有这一处：客户端拿到的传输码由目录里的原因决定，不再由这里
// 逐条挑码。
func failureFor(reason rbac.Reason) *errs.Error {
	switch reason {
	case rbac.ReasonNoMatchingGrant:
		return errs.ErrPermissionDenied
	case rbac.ReasonScopeMismatch:
		return errs.ErrScopeMismatch
	case rbac.ReasonSessionExpired:
		return errs.ErrSessionExpired
	case rbac.ReasonSubjectNotFound:
		return errs.ErrSessionSubjectNotFound
	case rbac.ReasonStoreUnavailable:
		return errs.ErrStoreUnavailable
	case rbac.ReasonAnnotationMissing:
		return errs.ErrAnnotationMissing
	default:
		// 理论上不可达：判定只会产出上面几种原因。走到这里说明判定层与
		// 拒绝映射之间多了一个取值，按"鉴权失败"处理而不是编一个拒绝理由。
		return errs.ErrPermissionCheckFailed
	}
}

// DenyByAnnotation 用于方法注解缺失或非法时的拒绝。
//
// 它与 Reject 分开，是因为这一类属于**服务端配置缺陷**而非调用方的权限问题：
// 返回 Internal 而不是 PermissionDenied，避免让调用方误以为是自己的问题。
// metadata 里带上方法名，使排障能直接定位到漏写注解的那个方法。
func DenyByAnnotation(procedure, reason string) error {
	failure := errs.ErrAnnotationMissing.
		WithMetadata("method", procedure).
		WithMetadata("detail", reason)
	encoded := errs.ToConnect(failure)

	// 迁移期保留旧详情，形状与字段含义不变。
	detail := &rbacv1.DenialDetail{
		Reason: rbacv1.DenialReason_DENIAL_REASON_ANNOTATION_MISSING,
		Detail: reason,
		Metadata: map[string]string{
			"method": procedure,
		},
	}
	if connectDetail, detailErr := connect.NewErrorDetail(detail); detailErr == nil {
		encoded.AddDetail(connectDetail)
	}
	return encoded
}

// RejectUnknownProcedure 用于"这个路径根本不是一个 RPC 方法"。
//
// 它与 DenyByAnnotation 是两类事：那一个是**服务端漏写注解**（服务端缺陷，
// 返回 Internal，并带上方法名让排障的人直接找到漏写的那一处）；这一个是
// **调用方把地址打错了**——或者有人直接访问了服务端 / 发布域的根路径。
// 后者返回未实现，与框架自己遇到未知过程名时的结论一致。
//
// 混成同一个码的代价很具体：一次地址打错（或一次扫描）会在面板上呈现为
// 服务端故障，而排障的人会去找一个并不存在的漏写注解（见
// docs/design/rbac/server-permissions.md 的"拒绝语义"）。
//
// 它不携带 DenialDetail：这里没有"哪一条权限规则拒绝了它"可讲。它仍然是一个
// 已分类的失败，因此带 FailureDetail——过程名在文案里。
func RejectUnknownProcedure(procedure string) error {
	failure := errs.Newf(
		errorsv1.FailureReason_FAILURE_REASON_UNKNOWN_PROCEDURE,
		"这个地址不是一个 RPC 方法：%s", procedure)
	return errs.ToConnect(failure)
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
		return Reject(rbac.ReasonStoreUnavailable)
	}
	if errors.Is(err, ErrNoCredential) || errors.Is(err, ErrInvalidCredential) {
		return Reject(rbac.ReasonSessionExpired)
	}
	return Reject(rbac.ReasonSubjectNotFound)
}
