package server

import (
	"context"
	"errors"
	"fmt"
	"time"

	"connectrpc.com/connect"
	"go.uber.org/zap"

	identityv1 "github.com/poetlife/aladdin/api/gen/aladdin/identity/v1"
	"github.com/poetlife/aladdin/internal/identity"
	"github.com/poetlife/aladdin/internal/rbac"
	"github.com/poetlife/aladdin/internal/server/interceptor"
)

// IdentityService 实现身份认证面。
//
// 边界：本服务只做认证（你是谁）。判定（你能做什么）由 RBAC 负责。
// 认证失败与鉴权失败返回不同的错误码，见 docs/design/rbac/server-permissions.md。
type IdentityService struct {
	store  rbac.MutableStore
	engine *rbac.Engine
	logger *zap.Logger

	// channels 是**已启用**的登录渠道，是"当前有哪些登录方式"的唯一来源。
	//
	// 未启用的渠道根本不在这里，而不是留一个 Verifier 为 nil 的位置——
	// 一个"装在那里的空实现"迟早会被某条路径直接调用（见 internal/identity
	// 的 Registry）。
	channels *identity.Registry
	// identities 是"一个渠道身份属于哪个主体"的唯一入口。
	identities *identity.Identities
	// sessions 是会话凭证的签发与失效入口。
	sessions *identity.Sessions
	// machine 是机器凭证的查表认证器：token 形式的登录仍走它。
	machine *interceptor.TokenAuthenticator
}

// IdentityDeps 是认证面所需的依赖，由 server.New 装配后注入。
type IdentityDeps struct {
	Machine    *interceptor.TokenAuthenticator
	Identities *identity.Identities
	Sessions   *identity.Sessions
	Channels   *identity.Registry
	Logger     *zap.Logger
}

// NewIdentityService 构造认证面服务。
func NewIdentityService(store rbac.MutableStore, engine *rbac.Engine, deps IdentityDeps) *IdentityService {
	return &IdentityService{
		store:      store,
		engine:     engine,
		logger:     deps.Logger,
		channels:   deps.Channels,
		identities: deps.Identities,
		sessions:   deps.Sessions,
		machine:    deps.Machine,
	}
}

// Login 实现 IdentityService。
//
// 它是公开方法（proto 上标记 public），因此不经过认证中间件。
func (s *IdentityService) Login(ctx context.Context, req *connect.Request[identityv1.LoginRequest]) (*connect.Response[identityv1.LoginResponse], error) {
	switch cred := req.Msg.GetCredential().(type) {
	case *identityv1.LoginRequest_Google:
		return s.loginWithChannel(ctx, identity.SourceGoogle, cred.Google.GetIdToken())
	case *identityv1.LoginRequest_Token:
		return s.loginWithMachineToken(cred.Token.GetToken())
	case *identityv1.LoginRequest_Password:
		// 口令认证尚未实现。返回 Unimplemented 而不是伪造成功，
		// 避免开发环境误以为认证已经生效。
		return nil, connect.NewError(connect.CodeUnimplemented,
			errors.New("口令认证尚未实现，请使用 Google 登录或 token 凭证"))
	default:
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("缺少凭证"))
	}
}

// loginWithChannel 校验一份渠道凭证，解析出主体，签发会话。
func (s *IdentityService) loginWithChannel(ctx context.Context, source, credential string) (*connect.Response[identityv1.LoginResponse], error) {
	verified, err := s.verify(ctx, source, credential)
	if err != nil {
		return nil, toLoginConnectError(err)
	}
	issued, err := s.resolveAndIssue(ctx, source, verified)
	if err != nil {
		return nil, toIdentityConnectError(err)
	}
	return connect.NewResponse(&identityv1.LoginResponse{
		AccessToken: issued.Token,
		ExpiresAt:   issued.Session.ExpiresAt.Format(time.RFC3339),
	}), nil
}

// resolveAndIssue 是登录与重定向回调**共用**的那段核心：解析主体、签发会话、留痕。
//
// 它接受一份**已校验**的身份，自己不做任何校验。调用方必须在校验通过之后
// 才走到这里——解析会登记新主体，因此绝不能在校验之前发生，否则任何字符串
// 都能在库里造出一个主体。
//
// 两条登录路径（RPC 与浏览器重定向回调）共用它，是为了让"登录成功后要做什么"
// 只有一份：分开实现迟早会出现"RPC 生效、回调没生效"这类断裂。
func (s *IdentityService) resolveAndIssue(ctx context.Context, source string, verified identity.VerifiedIdentity) (identity.Issued, error) {
	subject, err := s.identities.ResolveOrRegister(ctx, source, verified.ExternalID, verified.Display)
	if err != nil {
		return identity.Issued{}, err
	}
	issued, err := s.sessions.Issue(ctx, subject)
	if err != nil {
		return identity.Issued{}, err
	}

	// 登录留痕含主体标识：它是"这个人现在叫什么"的唯一可检索来源，
	// 也是建立第一个管理员时人工搬运的那个值。**不含凭证**。
	s.logger.Info("登录成功",
		zap.String("source", source),
		zap.String("subject_id", subject.ID),
	)
	return issued, nil
}

// loginWithMachineToken 用一份机器凭证换访问凭证。
//
// 当前形态是**原样返回**：机器凭证本身就可以直接用作请求凭证。把它改成
// "只能用于换取会话凭证"（长期密钥只在登录那一次出现在网络上）是一个更
// 干净的终局形态，但那会打断所有既有 CLI 使用者，因此作为独立决策来做，
// 不搭在接入 Google 登录里顺手改掉（见 docs/design/identity/session-token.md）。
func (s *IdentityService) loginWithMachineToken(token string) (*connect.Response[identityv1.LoginResponse], error) {
	if token == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("凭证不能为空"))
	}
	if _, ok := s.machine.Lookup(token); !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("凭证无效"))
	}
	return connect.NewResponse(&identityv1.LoginResponse{
		AccessToken: token,
		ExpiresAt:   time.Now().UTC().Add(12 * time.Hour).Format(time.RFC3339),
	}), nil
}

// verify 按 source 取校验器并校验渠道凭证。
//
// 它只做校验，不登记主体、不签发会话——那两件事在 resolveAndIssue 里，
// 由调用方在校验通过之后触发。
//
// 失败一律返回领域错误，由各入口各自映射成自己的错误形状：登录 RPC 映射成
// Connect 错误码，浏览器回调映射成一次带错误信息的重定向。
func (s *IdentityService) verify(ctx context.Context, source, credential string) (identity.VerifiedIdentity, error) {
	channel, ok := s.channels.Get(source)
	if !ok {
		return identity.VerifiedIdentity{}, fmt.Errorf("%w: %s", identity.ErrChannelDisabled, source)
	}
	return channel.Verifier.Verify(ctx, credential)
}

// toLoginConnectError 把登录路径上的失败映射为 Connect 错误码。
//
// 分类的依据是"调用方该做什么"，而不是错误来自哪一层：
//
//   - 渠道未启用 → Unimplemented（这条路没开，换一条）；
//   - 凭证不成立 → Unauthenticated（**不是**无权限，见 docs/design/rbac）；
//   - 够不着渠道 → Unavailable（退避重试）。
func toLoginConnectError(err error) error {
	switch {
	case errors.Is(err, identity.ErrChannelDisabled):
		return connect.NewError(connect.CodeUnimplemented, errors.New("未启用该登录方式"))
	case errors.Is(err, identity.ErrProviderUnavailable):
		return connect.NewError(connect.CodeUnavailable,
			errors.New("身份提供方暂时不可用，请稍后重试"))
	case errors.Is(err, identity.ErrInvalidToken):
		return connect.NewError(connect.CodeUnauthenticated, errors.New("身份凭证无效"))
	default:
		return toIdentityConnectError(err)
	}
}

// Refresh 实现 IdentityService。
//
// 骨架阶段机器凭证不设独立有效期，因此 Refresh 是幂等的原样返回。
// 会话凭证走各自的刷新路径（见 docs/design/identity/session-token.md）。
func (s *IdentityService) Refresh(_ context.Context, req *connect.Request[identityv1.RefreshRequest]) (*connect.Response[identityv1.RefreshResponse], error) {
	token := req.Msg.GetAccessToken()
	if token == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			errors.New("access_token 不能为空"))
	}
	if _, ok := s.machine.Lookup(token); !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated,
			errors.New("凭证已失效，请重新登录"))
	}
	return connect.NewResponse(&identityv1.RefreshResponse{
		AccessToken: token,
		ExpiresAt:   time.Now().UTC().Add(12 * time.Hour).Format(time.RFC3339),
	}), nil
}

// GetAuthMethods 实现 IdentityService。
//
// 公开方法：调用方尚未认证，而这正是它要回答的问题的前提。它之所以能公开，
// 是因为返回的内容本来就会出现在浏览器里——一个不公开的"有哪些登录方式"
// 不保护任何东西，只会迫使前端硬编码一份会漂移的副本。
//
// 清单**由注册表派生**，不在这里逐渠道列举：列举一份就会与"实际装配了哪些
// 渠道"有两个来源，而它们迟早会漂移，表现为"登录页渲染了一个点了报错的入口"。
// 未启用的渠道不在注册表里，因此也就不在下发清单里——前端据此不渲染入口。
func (s *IdentityService) GetAuthMethods(_ context.Context, _ *connect.Request[identityv1.GetAuthMethodsRequest]) (*connect.Response[identityv1.GetAuthMethodsResponse], error) {
	channels := s.channels.Methods()
	methods := make([]*identityv1.AuthMethod, 0, len(channels))
	for _, ch := range channels {
		methods = append(methods, &identityv1.AuthMethod{
			Source:   ch.Source,
			ClientId: ch.ClientID,
		})
	}
	return connect.NewResponse(&identityv1.GetAuthMethodsResponse{Methods: methods}), nil
}

// BindIdentity 实现 IdentityService。
//
// **归属由发起者决定，不由令牌决定。** 令牌只证明"发起者控制着这个身份"，
// 因此这里只可能绑到当前凭证代表的主体上——不存在"把身份绑到指定主体"的
// 形状。如果存在，任何持有他人令牌的人都能把身份挂到他人名下。
func (s *IdentityService) BindIdentity(ctx context.Context, req *connect.Request[identityv1.BindIdentityRequest]) (*connect.Response[identityv1.BindIdentityResponse], error) {
	subject, ok := interceptor.SubjectFromContext(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("未认证"))
	}

	google, ok := req.Msg.GetCredential().(*identityv1.BindIdentityRequest_Google)
	if !ok {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("缺少凭证"))
	}
	// 与登录**同一个**校验器、同一份校验清单：为绑定另写一套会让
	// "哪条路径校验得更松"只能靠比对代码来回答。
	verified, err := s.verify(ctx, identity.SourceGoogle, google.Google.GetIdToken())
	if err != nil {
		return nil, toLoginConnectError(err)
	}

	if err := s.identities.Bind(ctx, subject.ID, identity.SourceGoogle, verified.ExternalID, verified.Display); err != nil {
		return nil, toIdentityConnectError(err)
	}
	s.logger.Info("已绑定登录渠道",
		zap.String("subject_id", subject.ID),
		zap.String("source", identity.SourceGoogle),
		zap.String("identity_id", verified.ExternalID),
	)
	list, err := s.identityList(ctx, subject.ID)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&identityv1.BindIdentityResponse{Identities: list}), nil
}

// UnbindIdentity 实现 IdentityService。
//
// 只作用于当前主体。摘掉最后一个身份会被拒绝：那会让这个主体再也没有
// 任何进入方式，而它的角色绑定还在，没有人能进来清理。
func (s *IdentityService) UnbindIdentity(ctx context.Context, req *connect.Request[identityv1.UnbindIdentityRequest]) (*connect.Response[identityv1.UnbindIdentityResponse], error) {
	subject, ok := interceptor.SubjectFromContext(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("未认证"))
	}
	source, externalID := req.Msg.GetSource(), req.Msg.GetExternalId()
	if source == "" || externalID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			errors.New("source 与 external_id 都不能为空"))
	}

	if err := s.identities.Unbind(ctx, subject.ID, source, externalID); err != nil {
		return nil, toIdentityConnectError(err)
	}
	s.logger.Info("已解绑登录渠道",
		zap.String("subject_id", subject.ID),
		zap.String("source", source),
		zap.String("identity_id", externalID),
	)
	list, err := s.identityList(ctx, subject.ID)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&identityv1.UnbindIdentityResponse{Identities: list}), nil
}

// ListIdentities 实现 IdentityService。
func (s *IdentityService) ListIdentities(ctx context.Context, _ *connect.Request[identityv1.ListIdentitiesRequest]) (*connect.Response[identityv1.ListIdentitiesResponse], error) {
	subject, ok := interceptor.SubjectFromContext(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("未认证"))
	}
	list, err := s.identityList(ctx, subject.ID)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&identityv1.ListIdentitiesResponse{Identities: list}), nil
}

// identityList 读回主体的渠道清单，组装成接口类型。
//
// 绑定与解绑都返回**整份现状**而不是一个成功标志：客户端刚做过一次改变
// 现状的操作，让它在同一次往返里拿到新现状，比再发一次查询更省事，
// 也不会读到两次请求之间的中间态。
func (s *IdentityService) identityList(ctx context.Context, subjectID string) ([]*identityv1.Identity, error) {
	list, err := s.identities.List(ctx, subjectID)
	if err != nil {
		return nil, toIdentityConnectError(err)
	}
	return toProtoIdentities(list), nil
}

// WhoAmI 实现 IdentityService。
func (s *IdentityService) WhoAmI(ctx context.Context, _ *connect.Request[identityv1.WhoAmIRequest]) (*connect.Response[identityv1.WhoAmIResponse], error) {
	subject, ok := interceptor.SubjectFromContext(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("未认证"))
	}
	return connect.NewResponse(&identityv1.WhoAmIResponse{
		SubjectId:    subject.ID,
		SubjectType:  string(subject.Type),
		DefaultScope: string(subject.DefaultScope),
	}), nil
}

// GetSessionPermissions 实现 IdentityService。
//
// 这是前端会话权限的唯一来源：返回的是**展开后的最终权限码集合**，
// 前端据此做展示裁剪，不需要也不允许自行实现继承、通配与作用域包含。
func (s *IdentityService) GetSessionPermissions(ctx context.Context, req *connect.Request[identityv1.GetSessionPermissionsRequest]) (*connect.Response[identityv1.GetSessionPermissionsResponse], error) {
	subject, ok := interceptor.SubjectFromContext(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("未认证"))
	}
	scope := rbac.Scope(req.Msg.GetScope())
	if scope == rbac.GlobalScope {
		// 未显式指定时使用凭证绑定的默认作用域；
		// 不降级为全局作用域——那会把一次调用缺陷变成一次越权。
		scope = subject.DefaultScope
	}

	permissions, _, err := s.engine.EffectivePermissions(ctx, subject, scope)
	if err != nil {
		return nil, toConnectError(err)
	}
	codes := make([]string, 0, len(permissions))
	for _, p := range permissions {
		codes = append(codes, p.String())
	}
	return connect.NewResponse(&identityv1.GetSessionPermissionsResponse{
		Scope:       string(scope),
		Permissions: codes,
	}), nil
}

// toProtoIdentities 把渠道身份翻译成接口类型。
//
// 只有展示与定位所需的三个字段，**没有一个是判定输入**。
func toProtoIdentities(list []identity.Identity) []*identityv1.Identity {
	out := make([]*identityv1.Identity, 0, len(list))
	for _, ident := range list {
		out = append(out, &identityv1.Identity{
			Source:     ident.Source,
			ExternalId: ident.ExternalID,
			Display:    ident.Display,
		})
	}
	return out
}

// toIdentityConnectError 把身份与主体相关的错误映射为 Connect 错误。
//
// 分类的依据是"调用方该做什么"，而不是错误来自哪一层：
//
//   - 存储故障 → Unavailable（退避重试），**不是**权限或认证问题；
//   - 身份已被别人占用 → AlreadyExists（换一个渠道，不要重试）；
//   - 摘掉最后一个身份 → FailedPrecondition（调用方的输入问题）；
//   - 身份未登记 → NotFound（界面刷新一下）。
func toIdentityConnectError(err error) error {
	switch {
	case errors.Is(err, identity.ErrStoreUnavailable), errors.Is(err, rbac.ErrStoreUnavailable):
		return connect.NewError(connect.CodeUnavailable, errors.New("服务暂时不可用，请稍后重试"))
	case errors.Is(err, identity.ErrIdentityTaken):
		// 信息里**不含占用者**：透露它等于把一次失败的绑定变成一次
		// "这个渠道对应哪个主体"的探测。
		return connect.NewError(connect.CodeAlreadyExists, errors.New("该登录渠道已被其他账号绑定"))
	case errors.Is(err, identity.ErrLastIdentity):
		return connect.NewError(connect.CodeFailedPrecondition,
			errors.New("不能解绑最后一个登录渠道，否则这个账号将无法登录"))
	case errors.Is(err, identity.ErrIdentityNotFound):
		return connect.NewError(connect.CodeNotFound, errors.New("该登录渠道未绑定到当前账号"))
	case errors.Is(err, rbac.ErrSubjectNotFound):
		return connect.NewError(connect.CodeNotFound, errors.New("主体不存在或已停用"))
	default:
		return connect.NewError(connect.CodeInternal, errors.New("认证服务内部错误"))
	}
}
