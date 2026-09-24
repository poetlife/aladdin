package rbac

import (
	"context"
	"errors"
	"time"

	"go.uber.org/zap"

	rbacv1 "github.com/poetlife/aladdin/api/gen/aladdin/rbac/v1"
	"github.com/poetlife/aladdin/internal/observability"
)

// Reason 是判定结论的固定原因枚举。
//
// 它刻意使用有限枚举而非自由文本：客户端需要据此区分处理
// （见 docs/design/rbac/enforcement.md 的原因表），
// 自由文本会迫使客户端做字符串匹配。
//
// 取值定义在 api/proto/aladdin/rbac/v1/errors.proto —— 拒绝原因是对外契约，
// Go、TypeScript 与 CLI 三端都从同一份定义生成。下面这些常量只是给生成
// 枚举起的短名字，**不构成第二份定义**（见 docs/ssot-registry.md）。
type Reason = rbacv1.DenialReason

const (
	// ReasonAllow 表示允许。
	ReasonAllow = rbacv1.DenialReason_DENIAL_REASON_ALLOW
	// ReasonNoMatchingGrant 表示主体确实没有这条权限。
	ReasonNoMatchingGrant = rbacv1.DenialReason_DENIAL_REASON_NO_MATCHING_GRANT
	// ReasonScopeMismatch 表示主体有此权限，但被授予的作用域不覆盖本次请求。
	ReasonScopeMismatch = rbacv1.DenialReason_DENIAL_REASON_SCOPE_MISMATCH
	// ReasonSessionExpired 表示凭证失效或未携带。
	ReasonSessionExpired = rbacv1.DenialReason_DENIAL_REASON_SESSION_EXPIRED
	// ReasonSubjectNotFound 表示主体已被删除或停用。
	ReasonSubjectNotFound = rbacv1.DenialReason_DENIAL_REASON_SUBJECT_NOT_FOUND
	// ReasonStoreUnavailable 表示存储不可用，无法得出结论。
	//
	// 它**不是**拒绝：上层应据此返回"服务不可用"并允许客户端退避重试，
	// 而不是让客户端以为权限不足。
	ReasonStoreUnavailable = rbacv1.DenialReason_DENIAL_REASON_STORE_UNAVAILABLE
	// ReasonAnnotationMissing 表示服务端方法漏写鉴权注解。
	//
	// 这是**服务端配置缺陷**而非调用方的权限问题。
	ReasonAnnotationMissing = rbacv1.DenialReason_DENIAL_REASON_ANNOTATION_MISSING
)

// Decision 是一次鉴权判定的完整结论。
type Decision struct {
	Allowed     bool
	Reason      Reason
	SubjectID   string
	Permission  PermissionCode
	Scope       Scope
	Roles       []string
	Permissions []PermissionCode
}

// Engine 是鉴权决策的唯一实现。
//
// 三端中只有服务端持有 Engine；前端与 CLI 的本地判断只做权限码集合的
// 成员测试，不重复实现本引擎的逻辑。
type Engine struct {
	store   Store
	logger  *zap.Logger
	metrics *observability.Metrics
}

// NewEngine 构造决策引擎。
//
// logger 为 nil 时判定不留痕、metrics 为 nil 时不计数，两者互不影响
// （仅测试场景会用到 nil）。
func NewEngine(store Store, logger *zap.Logger, metrics *observability.Metrics) *Engine {
	return &Engine{store: store, logger: logger, metrics: metrics}
}

// Check 判定主体在给定作用域下是否持有指定权限。
//
// 判定顺序是行为的一部分（见 docs/design/rbac/enforcement.md）：
// 主体有效性 → 取绑定 → 作用域过滤 → 继承展开 → 通配匹配 → 默认拒绝。
//
// 无论允许还是拒绝都会留痕；拒绝的留痕不可采样。
func (e *Engine) Check(ctx context.Context, subject Subject, permission PermissionCode, scope Scope) Decision {
	started := time.Now()
	decision := e.check(ctx, subject, permission, scope)
	e.observe(ctx, decision, time.Since(started))
	return decision
}

// observe 把一次判定的结论写到链路与指标上。
//
// 日志在各条返回路径上就近输出（见 log / logWithError），而链路属性与指标
// 在这里统一记录：计时必须包住整次判定，只能在最外层做。集中在一处也保证了
// "每条结论都被计数"——散落到各条返回路径上，迟早会出现某种拒绝没有指标。
func (e *Engine) observe(ctx context.Context, d Decision, elapsed time.Duration) {
	label := decisionLabel(d.Allowed)
	observability.RecordDecision(ctx, d.SubjectID, d.Permission.String(), label, d.Reason.String())
	e.metrics.RBACDecision(ctx, d.Permission.String(), label, elapsed)
}

// decisionLabel 把结论转成指标属性与链路属性用的取值。
//
// 取值必须有界：属性上出现自由文本会让时序数量随流量增长。
func decisionLabel(allowed bool) string {
	if allowed {
		return "allow"
	}
	return "deny"
}

// check 是判定本身。
func (e *Engine) check(ctx context.Context, subject Subject, permission PermissionCode, scope Scope) Decision {
	decision := Decision{
		SubjectID:  subject.ID,
		Permission: permission,
		Scope:      scope,
	}

	if subject.ID == "" {
		decision.Reason = ReasonSessionExpired
		e.log(ctx, decision)
		return decision
	}

	inScope, outOfScope, err := e.collectRoles(ctx, subject.ID, scope)
	if err != nil {
		decision.Reason = reasonForStoreError(err)
		e.log(ctx, decision)
		return decision
	}

	roles, permissions, err := expand(ctx, e.store, inScope)
	if err != nil {
		// 角色数据损坏（如继承成环）属于需要立即处理的故障，
		// 但对外仍表现为"无法得出结论"，避免把内部细节暴露给客户端。
		decision.Reason = ReasonStoreUnavailable
		e.logWithError(ctx, decision, err)
		return decision
	}
	decision.Roles = roles
	decision.Permissions = permissions

	for _, held := range permissions {
		if Matches(held, permission) {
			decision.Allowed = true
			decision.Reason = ReasonAllow
			e.log(ctx, decision)
			return decision
		}
	}

	// 未命中。区分"确实没有"与"有但作用域不够"——
	// 这两种情况对用户的提示完全不同（见 enforcement.md 的原因表）。
	// 该检查只在拒绝路径上执行，不增加放行请求的代价。
	if e.grantedOutsideScope(ctx, outOfScope, permission) {
		decision.Reason = ReasonScopeMismatch
	} else {
		decision.Reason = ReasonNoMatchingGrant
	}
	e.log(ctx, decision)
	return decision
}

// EffectivePermissions 返回主体在指定作用域下展开后的最终权限码集合。
//
// 这是前端会话权限的唯一来源：前端拿到的是已展开的集合，
// 因此不需要（也不允许）自行实现继承、通配与作用域包含。
func (e *Engine) EffectivePermissions(ctx context.Context, subject Subject, scope Scope) ([]PermissionCode, []string, error) {
	if subject.ID == "" {
		return nil, nil, ErrSubjectNotFound
	}
	inScope, _, err := e.collectRoles(ctx, subject.ID, scope)
	if err != nil {
		return nil, nil, err
	}
	roles, permissions, err := expand(ctx, e.store, inScope)
	if err != nil {
		return nil, nil, err
	}
	return permissions, roles, nil
}

// collectRoles 把主体的绑定按作用域拆成两组：本次请求作用域内生效的，
// 以及作用域外因而不生效的。后者只用于在拒绝时给出准确原因。
func (e *Engine) collectRoles(ctx context.Context, subjectID string, scope Scope) (inScope, outOfScope []string, err error) {
	bindings, err := e.store.SubjectBindings(ctx, subjectID)
	if err != nil {
		return nil, nil, err
	}
	for _, b := range bindings {
		if b.Scope.Contains(scope) {
			inScope = append(inScope, b.RoleID)
			continue
		}
		outOfScope = append(outOfScope, b.RoleID)
	}
	return inScope, outOfScope, nil
}

// grantedOutsideScope 报告作用域外的绑定中是否存在授予目标权限的角色。
func (e *Engine) grantedOutsideScope(ctx context.Context, roleIDs []string, permission PermissionCode) bool {
	if len(roleIDs) == 0 {
		return false
	}
	_, permissions, err := expand(ctx, e.store, roleIDs)
	if err != nil {
		return false
	}
	for _, held := range permissions {
		if Matches(held, permission) {
			return true
		}
	}
	return false
}

func reasonForStoreError(err error) Reason {
	switch {
	case errors.Is(err, ErrSubjectNotFound):
		return ReasonSubjectNotFound
	case errors.Is(err, ErrRoleNotFound):
		return ReasonStoreUnavailable
	default:
		return ReasonStoreUnavailable
	}
}

// log 输出一条判定留痕。
//
// 字段集合是 docs/observability.md 中"鉴权相关的强制字段"的落点；
// 缺失任一字段视为不合格埋点。
func (e *Engine) log(ctx context.Context, d Decision) {
	e.logWithError(ctx, d, nil)
}

func (e *Engine) logWithError(ctx context.Context, d Decision, err error) {
	if e.logger == nil {
		return
	}
	fields := []zap.Field{
		zap.String("subject_id", d.SubjectID),
		zap.String("permission", d.Permission.String()),
		zap.String("scope", d.Scope.String()),
		zap.String("decision", decisionLabel(d.Allowed)),
		zap.String("reason", d.Reason.String()),
	}
	if len(d.Roles) > 0 {
		fields = append(fields, zap.Strings("roles", d.Roles))
	}
	if err != nil {
		fields = append(fields, zap.Error(err))
	}

	logger := observability.SpanLogger(ctx, e.logger)
	// 拒绝必须留痕且不可采样，因此用 WARN/ERROR 而非 DEBUG。
	// 允许可以降级到 DEBUG，避免高频放行把日志淹没。
	switch {
	case err != nil:
		logger.Error("鉴权判定失败", fields...)
	case d.Allowed:
		logger.Debug("鉴权通过", fields...)
	default:
		logger.Warn("鉴权拒绝", fields...)
	}
}
