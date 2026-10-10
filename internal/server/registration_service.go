package server

import (
	"context"
	"errors"
	"fmt"
	"time"

	"connectrpc.com/connect"
	"go.uber.org/zap"

	identityv1 "github.com/poetlife/aladdin/api/gen/aladdin/identity/v1"
	"github.com/poetlife/aladdin/internal/rbac"
	"github.com/poetlife/aladdin/internal/registration"
	"github.com/poetlife/aladdin/internal/server/interceptor"
)

// RegistrationService 实现注册面：谁进得来、进来拿什么。
//
// 它同时是**管理面**（策略与邀请码）与**公开面**（完成一次因邀请码而暂停的注册）。
// 两者放在一个服务里，是因为它们操作的是同一份事实；分成两个服务只会让"这份策略
// 当前是什么"在两个地方各读一次。
//
// 它**不自己实现登记**：那一件事只有一段实现（见 IdentityService.register），
// 开放注册的回调与这里的"完成注册"都走它。各写一份迟早会出现"走邀请码进来的
// 没授默认角色"这类断裂。
type RegistrationService struct {
	identity      *IdentityService
	registrations *registration.Registrations
	store         rbac.MutableStore
	logger        *zap.Logger
}

// NewRegistrationService 构造注册面服务。
//
// identity 必须是**同一个** IdentityService 实例：那一段登记实现是两个入口共用
// 的那一份，另建一个会让两处的存储与签发入口悄悄分叉。
func NewRegistrationService(identity *IdentityService, registrations *registration.Registrations, store rbac.MutableStore, logger *zap.Logger) *RegistrationService {
	return &RegistrationService{
		identity:      identity,
		registrations: registrations,
		store:         store,
		logger:        logger,
	}
}

// GetRegistrationPolicy 实现 RegistrationService。
func (s *RegistrationService) GetRegistrationPolicy(ctx context.Context, _ *connect.Request[identityv1.GetRegistrationPolicyRequest]) (*connect.Response[identityv1.GetRegistrationPolicyResponse], error) {
	policy, err := s.registrations.Policy(ctx)
	if err != nil {
		return nil, toRegistrationConnectError(err)
	}
	return connect.NewResponse(&identityv1.GetRegistrationPolicyResponse{
		Policy: toProtoRegistrationPolicy(policy),
	}), nil
}

// PutRegistrationPolicy 实现 RegistrationService。
//
// 它**只改这一条记录、不授予任何权限**：默认角色是在登记那一刻写进绑定表的，
// 因此改策略不追溯已登记的主体，也不改变任何一次判定（见
// docs/design/identity/registration.md）。
//
// 三条语义约束在这一侧校验，因为它们都要读授权面：角色必须存在、范围必须已登记、
// 角色必须过"不能自我放大"那条判据。第三条的唯一入口是 internal/rbac 的约束校验。
func (s *RegistrationService) PutRegistrationPolicy(ctx context.Context, req *connect.Request[identityv1.PutRegistrationPolicyRequest]) (*connect.Response[identityv1.PutRegistrationPolicyResponse], error) {
	actor, ok := interceptor.SubjectFromContext(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("未认证"))
	}
	mode, err := registrationModeFromProto(req.Msg.GetMode())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}

	policy := registration.Policy{
		Mode:          mode,
		DefaultRoleID: req.Msg.GetDefaultRoleId(),
		DefaultScope:  rbac.Scope(req.Msg.GetDefaultScope()),
	}
	if policy.GrantsDefaultRole() {
		if _, err := s.store.Role(ctx, policy.DefaultRoleID); err != nil {
			return nil, toRegistrationConnectError(err)
		}
		if err := requireRegisteredScope(ctx, s.store, string(policy.DefaultScope)); err != nil {
			return nil, err
		}
		if err := rbac.ValidateRegistrationDefaultRole(ctx, s.store, policy.DefaultRoleID); err != nil {
			return nil, toRegistrationConnectError(err)
		}
	}

	updated, err := s.registrations.PutPolicy(ctx, policy, actor.ID)
	if err != nil {
		return nil, toRegistrationConnectError(err)
	}
	// 留痕含改动者与被设定的姿态，**不含任何凭证**（这里本来也没有）。
	s.logger.Info("已修改注册策略",
		zap.String("subject_id", actor.ID),
		zap.String("mode", string(updated.Mode)),
		zap.String("default_role_id", updated.DefaultRoleID),
		zap.String("default_scope", string(updated.DefaultScope)),
	)
	return connect.NewResponse(&identityv1.PutRegistrationPolicyResponse{
		Policy: toProtoRegistrationPolicy(updated),
	}), nil
}

// ListInvites 实现 RegistrationService。
//
// 返回值里**没有任何字段能还原出码的明文**：库里存的是摘要，明文只在签发那一次
// 返回过。要收回一份只能用 RevokeInvite。
func (s *RegistrationService) ListInvites(ctx context.Context, _ *connect.Request[identityv1.ListInvitesRequest]) (*connect.Response[identityv1.ListInvitesResponse], error) {
	list, err := s.registrations.List(ctx)
	if err != nil {
		return nil, toRegistrationConnectError(err)
	}
	out := make([]*identityv1.Invite, 0, len(list))
	for _, invite := range list {
		out = append(out, toProtoInvite(invite))
	}
	return connect.NewResponse(&identityv1.ListInvitesResponse{Invites: out}), nil
}

// CreateInvite 实现 RegistrationService。
func (s *RegistrationService) CreateInvite(ctx context.Context, req *connect.Request[identityv1.CreateInviteRequest]) (*connect.Response[identityv1.CreateInviteResponse], error) {
	actor, ok := interceptor.SubjectFromContext(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("未认证"))
	}
	expiresAt, err := parseInviteExpiry(req.Msg.GetExpiresAt())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}

	invite, code, err := s.registrations.Issue(ctx, registration.IssueParams{
		Label:     req.Msg.GetLabel(),
		MaxUses:   req.Msg.GetMaxUses(),
		ExpiresAt: expiresAt,
	}, actor.ID)
	if err != nil {
		return nil, toRegistrationConnectError(err)
	}
	// 留痕**不含明文**：它与口令同级——一份能换来一次注册的凭据。
	s.logger.Info("已签发邀请码",
		zap.String("subject_id", actor.ID),
		zap.String("invite_id", invite.ID),
		zap.Int32("max_uses", invite.MaxUses),
	)
	return connect.NewResponse(&identityv1.CreateInviteResponse{
		Invite: toProtoInvite(invite),
		// **只在这一次返回**。此后再也读不回来。
		Code: code,
	}), nil
}

// RevokeInvite 实现 RegistrationService。
//
// 只撤销，不删除：用量与留痕留在库里，"这个码被谁用过、用过几次"才答得上来。
func (s *RegistrationService) RevokeInvite(ctx context.Context, req *connect.Request[identityv1.RevokeInviteRequest]) (*connect.Response[identityv1.RevokeInviteResponse], error) {
	actor, ok := interceptor.SubjectFromContext(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("未认证"))
	}
	invite, err := s.registrations.Revoke(ctx, req.Msg.GetId())
	if err != nil {
		return nil, toRegistrationConnectError(err)
	}
	s.logger.Info("已撤销邀请码",
		zap.String("subject_id", actor.ID),
		zap.String("invite_id", invite.ID),
	)
	return connect.NewResponse(&identityv1.RevokeInviteResponse{Invite: toProtoInvite(invite)}), nil
}

// CompleteRegistration 实现 RegistrationService。
//
// 公开方法：调用方正是那个还没登录、也还没有账号的人。
//
// **归属只由服务端在回调时记下的那份一次性凭据决定**：请求里只有邀请码，没有
// 主体、也没有渠道身份。若存在"替某个身份注册"的形状，任何拿到一个码的人都能
// 在库里造出一个属于别人的主体（与 CompleteIdentityBinding 同一条理由）。
//
// 注册模式在**这一刻重读**，而不是沿用回调时的结论：管理员把站点改成"不接受
// 新账号"之后，一个停在填码页上的流程不应该还能把账号建出来。
//
// **三步的顺序是刻意的：先看一眼凭据 → 兑换邀请码 → 再取走凭据 → 登记。**
//
// 凭据是单次使用的，而"码打错了"是最常见的一种失败。先取走凭据再兑换，会让每一次
// 打错都把人打回登录页重来一遍——那是使用者在这一页上唯一要做的事，也是最不该被
// 惩罚的地方。反过来先兑换，打错时凭据原封不动，改一个码再提交就行（Redeem 失败
// 时不产生任何写入）。
//
// 代价是"凭据已失效"这一支会白烧掉一次码的可用次数。它只在两次提交同时到达、
// 或中途过期时才发生，比"每次打错都重新登录"罕见得多，损失也小得多。
func (s *RegistrationService) CompleteRegistration(ctx context.Context, req *connect.Request[identityv1.CompleteRegistrationRequest]) (*connect.Response[identityv1.CompleteRegistrationResponse], error) {
	token, ok := cookieValue(req.Header(), registrationCookie)
	if !ok {
		return nil, registrationFlowExpired()
	}
	// 只看一眼，**不取走**：取走是最后一步（见上）。
	pending, ok := s.identity.pendingRegistrations.peek(token)
	if !ok {
		// 不区分"没发出过""过期""已用过"：对调用方是同一件事——这次注册已经不能
		// 完成了，重新发起一次登录。
		return nil, registrationFlowExpired()
	}

	policy, err := s.registrations.Policy(ctx)
	if err != nil {
		return nil, toRegistrationConnectError(err)
	}
	switch policy.Mode {
	case registration.ModeClosed:
		s.logger.Warn("注册没有完成",
			zap.String("reason", "站点不接受新账号"),
			zap.String("source", pending.source))
		return nil, registrationClosed()
	case registration.ModeInvite:
		// 消费邀请码。**它只回答"这次兑换成不成立"**——返回值里的记录不带角色、
		// 不带范围，拿不到"该给他什么"，那只能来自上面的策略。
		if _, err := s.registrations.Redeem(ctx, req.Msg.GetInviteCode()); err != nil {
			// 留痕**不含码**：它与口令同级。四种"兑换不动"的原因在存储层已经合并
			// 成同一个结论，因此这里只有一个说法——排障的人于是去重新发一个码，
			// 而不是去琢磨这个码是不是曾经有效过。
			s.logger.Warn("注册没有完成",
				zap.String("reason", "邀请码不可兑换"),
				zap.String("source", pending.source))
			return nil, toRegistrationConnectError(err)
		}
	}
	// 模式在这一刻是"开放"时**不消费码**：这个人的码留着，下次仍可用，没有理由
	// 白白作废一份管理员发出去的东西。

	// 到这里才取走凭据：单次使用由这一次取走保证，两次并发的提交只有一个能走到
	// 下面那一步。取不到意味着它在这几步之间被另一个请求取走、或刚好过期。
	if _, ok := s.identity.pendingRegistrations.consume(token); !ok {
		return nil, registrationFlowExpired()
	}

	// 登记与授予走的是与开放注册**同一段实现**。
	issued, err := s.identity.register(ctx, pending.source, pending.verified, policy)
	if err != nil {
		return nil, toRegistrationConnectError(err)
	}

	resp := connect.NewResponse(&identityv1.CompleteRegistrationResponse{
		AccessToken: issued.Token,
		ExpiresAt:   issued.Session.ExpiresAt.Format(time.RFC3339),
	})
	// 服务端已经消费了凭据；顺手让浏览器侧也把它删掉。
	resp.Header().Add("Set-Cookie", registrationClearCookie(s.identity.pendingRegistrations.secure).String())
	return resp, nil
}

// registrationModeFromProto 把接口上的模式翻成内部取值。
//
// UNSPECIFIED **不是**一个可以写进来的姿态：它是"没填"，而"没填"落成开放注册会
// 让一次漏传把站点悄悄打开。空范围那一侧的惯例（空 = 全局）在这里不适用——那是
// 一个有明确含义的根，而"没填模式"没有。
func registrationModeFromProto(mode identityv1.RegistrationMode) (registration.Mode, error) {
	switch mode {
	case identityv1.RegistrationMode_REGISTRATION_MODE_OPEN:
		return registration.ModeOpen, nil
	case identityv1.RegistrationMode_REGISTRATION_MODE_INVITE:
		return registration.ModeInvite, nil
	case identityv1.RegistrationMode_REGISTRATION_MODE_CLOSED:
		return registration.ModeClosed, nil
	default:
		return "", errors.New("注册模式必须显式指定")
	}
}

// toProtoRegistrationMode 把内部取值翻成接口上的模式。
func toProtoRegistrationMode(mode registration.Mode) identityv1.RegistrationMode {
	switch mode {
	case registration.ModeInvite:
		return identityv1.RegistrationMode_REGISTRATION_MODE_INVITE
	case registration.ModeClosed:
		return identityv1.RegistrationMode_REGISTRATION_MODE_CLOSED
	default:
		return identityv1.RegistrationMode_REGISTRATION_MODE_OPEN
	}
}

// toProtoRegistrationPolicy 把策略翻成接口类型。
//
// 两个时间与改动者都是留痕，**不参与判定**；界面上它们回答"这个姿态是谁定的"。
func toProtoRegistrationPolicy(policy registration.Policy) *identityv1.RegistrationPolicy {
	out := &identityv1.RegistrationPolicy{
		Mode:               toProtoRegistrationMode(policy.Mode),
		DefaultRoleId:      policy.DefaultRoleID,
		DefaultScope:       string(policy.DefaultScope),
		UpdatedBySubjectId: policy.UpdatedBySubjectID,
	}
	if !policy.UpdatedAt.IsZero() {
		out.UpdatedAt = policy.UpdatedAt.UTC().Format(time.RFC3339)
	}
	return out
}

// toProtoInvite 把邀请码翻成接口类型。
//
// **没有摘要、也没有明文。** 摘要不出库是刻意的：读侧拿不到它，也就无从离线
// 比对一份猜出来的码。
func toProtoInvite(invite registration.Invite) *identityv1.Invite {
	out := &identityv1.Invite{
		Id:                 invite.ID,
		Label:              invite.Label,
		CreatedBySubjectId: invite.CreatedBySubjectID,
		CreatedAt:          invite.CreatedAt.UTC().Format(time.RFC3339),
		MaxUses:            invite.MaxUses,
		UsedCount:          invite.UsedCount,
	}
	if !invite.ExpiresAt.IsZero() {
		out.ExpiresAt = invite.ExpiresAt.UTC().Format(time.RFC3339)
	}
	if !invite.RevokedAt.IsZero() {
		out.RevokedAt = invite.RevokedAt.UTC().Format(time.RFC3339)
	}
	return out
}

// parseInviteExpiry 解析签发请求里的有效期。空表示不过期。
func parseInviteExpiry(raw string) (time.Time, error) {
	if raw == "" {
		return time.Time{}, nil
	}
	at, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}, fmt.Errorf("有效期必须是 ISO 8601 时间: %w", err)
	}
	return at, nil
}

// registrationFlowExpired 是"这次注册没走完"的统一拒绝。
//
// 它说的是重新登录，而不是重新要一个码：凭据不成立时，码对不对根本还没轮到判。
// 与"码无效"分开，是因为使用者的下一步完全不同。
func registrationFlowExpired() error {
	return connect.NewError(connect.CodeFailedPrecondition,
		errors.New("这次注册已经过期或已经完成，请重新登录一次"))
}

// registrationClosed 是"站点不接受新账号"的统一拒绝。
//
// 它与 registrationFlowExpired **必须是两个结论**：一个说"重新来一次就行"，
// 另一个说"再来多少次都不行"。合并会让使用者陷在重试里。
func registrationClosed() error {
	return connect.NewError(connect.CodeFailedPrecondition,
		errors.New("本站不接受新账号，请联系管理员"))
}

// inviteUnusable 是"这份邀请码兑换不动"的统一拒绝。
//
// 不存在、已过期、次数用尽、已撤销——**四种情形共用这一句**。区分它们只会把一次
// 失败的注册变成对"这个码是否曾经有效"的探测（见 docs/design/identity/registration.md）。
func inviteUnusable() error {
	return connect.NewError(connect.CodeFailedPrecondition,
		errors.New("邀请码无效或已用尽，请向管理员索取一个新的"))
}

// toRegistrationConnectError 把注册面的错误映射为 Connect 错误。
//
// 分类的依据是"调用方该做什么"，而不是错误来自哪一层：
//
//   - 存储故障 → Unavailable（退避重试），**不是**权限或输入问题；
//   - 形状不对、角色不合资格 → InvalidArgument（改输入，别重试）；
//   - 码兑换不动 → 统一的那一句，不区分原因；
//   - 记录找不到 → NotFound（界面刷新一下）。
func toRegistrationConnectError(err error) error {
	switch {
	case errors.Is(err, registration.ErrStoreUnavailable), errors.Is(err, rbac.ErrStoreUnavailable):
		return connect.NewError(connect.CodeUnavailable, errors.New("服务暂时不可用，请稍后重试"))
	case errors.Is(err, registration.ErrPolicyInvalid), errors.Is(err, registration.ErrInviteInvalid):
		return connect.NewError(connect.CodeInvalidArgument, err)
	case errors.Is(err, rbac.ErrRegistrationRoleUnfit):
		return connect.NewError(connect.CodeInvalidArgument, err)
	case errors.Is(err, registration.ErrInviteNotFound):
		return connect.NewError(connect.CodeNotFound, errors.New("邀请码不存在"))
	case errors.Is(err, registration.ErrInviteUnusable):
		return inviteUnusable()
	case errors.Is(err, rbac.ErrRoleNotFound):
		return connect.NewError(connect.CodeNotFound, errors.New("角色不存在"))
	case errors.Is(err, rbac.ErrScopeNotFound):
		return connect.NewError(connect.CodeNotFound, errors.New("范围未登记"))
	default:
		return connect.NewError(connect.CodeInternal, errors.New("注册服务内部错误"))
	}
}
