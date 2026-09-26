package server

import (
	"context"
	"errors"
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

	// googleClientID 为 Google 登录的客户端标识；为空表示未启用该登录方式。
	//
	// 它不是秘密：这个值本来就明文出现在浏览器里，这是这类登录方式的设计
	// 前提（见 docs/design/identity/google-login.md）。
	googleClientID string
	// verifier 校验渠道签发的身份令牌。未启用时为空——**不装一个"什么都通过"
	// 的实现**，那会让未启用的登录方式变成一个可用的后门。
	verifier identity.TokenVerifier
	// identities 是"一个渠道身份属于哪个主体"的唯一入口。
	identities *identity.Identities
	// sessions 是会话凭证的签发与失效入口。
	sessions *identity.Sessions
	// machine 是机器凭证的查表认证器：token 形式的登录仍走它。
	machine *interceptor.TokenAuthenticator
}

// IdentityDeps 是认证面所需的依赖，由 server.New 装配后注入。
type IdentityDeps struct {
	Machine        *interceptor.TokenAuthenticator
	Identities     *identity.Identities
	Sessions       *identity.Sessions
	Verifier       identity.TokenVerifier
	GoogleClientID string
	Logger         *zap.Logger
}

// NewIdentityService 构造认证面服务。
func NewIdentityService(store rbac.MutableStore, engine *rbac.Engine, deps IdentityDeps) *IdentityService {
	return &IdentityService{
		store:          store,
		engine:         engine,
		logger:         deps.Logger,
		googleClientID: deps.GoogleClientID,
		verifier:       deps.Verifier,
		identities:     deps.Identities,
		sessions:       deps.Sessions,
		machine:        deps.Machine,
	}
}

// Login 实现 IdentityService。
//
// 它是公开方法（proto 上标记 public），因此不经过认证中间件。
func (s *IdentityService) Login(ctx context.Context, req *connect.Request[identityv1.LoginRequest]) (*connect.Response[identityv1.LoginResponse], error) {
	switch cred := req.Msg.GetCredential().(type) {
	case *identityv1.LoginRequest_Google:
		return s.loginWithGoogle(ctx, cred.Google.GetIdToken())
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

// loginWithGoogle 校验一份 Google 身份令牌，解析出主体，签发会话。
//
// 三步的顺序是有意的：**先校验令牌，再解析主体，最后才签发**。
// 解析会登记新主体，因此绝不能在校验通过之前发生——否则任何字符串都能
// 在库里造出一个主体。
func (s *IdentityService) loginWithGoogle(ctx context.Context, idToken string) (*connect.Response[identityv1.LoginResponse], error) {
	verified, err := s.verifyGoogle(ctx, idToken)
	if err != nil {
		return nil, err
	}

	subject, err := s.identities.ResolveOrRegister(ctx, identity.SourceGoogle, verified.Subject, verified.Email)
	if err != nil {
		return nil, toIdentityConnectError(err)
	}

	issued, err := s.sessions.Issue(ctx, subject)
	if err != nil {
		return nil, toIdentityConnectError(err)
	}

	// 登录留痕含主体标识：它是"这个人现在叫什么"的唯一可检索来源，
	// 也是建立第一个管理员时人工搬运的那个值。**不含令牌**。
	s.logger.Info("登录成功",
		zap.String("source", identity.SourceGoogle),
		zap.String("subject_id", subject.ID),
	)
	return connect.NewResponse(&identityv1.LoginResponse{
		AccessToken: issued.Token,
		ExpiresAt:   issued.Session.ExpiresAt.Format(time.RFC3339),
	}), nil
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

// verifyGoogle 校验一份 Google 身份令牌。
//
// 校验器缺失、令牌不成立、提供方不可达三者分别映射到不同的错误码：
// 前两者是"这条路走不通"，后者是"我们够不着 Google"，返回同一个错误
// 会让一次外部依赖故障表现成"所有人都登录失败了"。
//
// **任何失败都不返回"无权限"**，只返回"未认证"。
func (s *IdentityService) verifyGoogle(ctx context.Context, idToken string) (identity.GoogleIdentity, error) {
	if s.verifier == nil {
		// 未启用时报"未实现"，而不是"凭证无效"：前者说的是这条路没开，
		// 后者会说成是调用方拿错了令牌。
		return identity.GoogleIdentity{}, connect.NewError(connect.CodeUnimplemented,
			errors.New("未启用 Google 登录"))
	}
	verified, err := s.verifier.Verify(ctx, idToken)
	switch {
	case err == nil:
		return verified, nil
	case errors.Is(err, identity.ErrProviderUnavailable):
		return identity.GoogleIdentity{}, connect.NewError(connect.CodeUnavailable,
			errors.New("身份提供方暂时不可用，请稍后重试"))
	default:
		return identity.GoogleIdentity{}, connect.NewError(connect.CodeUnauthenticated,
			errors.New("身份令牌无效"))
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
// **未启用时返回空标识，由前端决定不渲染入口**，而不是渲染了再报错。
func (s *IdentityService) GetAuthMethods(_ context.Context, _ *connect.Request[identityv1.GetAuthMethodsRequest]) (*connect.Response[identityv1.GetAuthMethodsResponse], error) {
	return connect.NewResponse(&identityv1.GetAuthMethodsResponse{
		GoogleClientId: s.googleClientID,
	}), nil
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
	verified, err := s.verifyGoogle(ctx, google.Google.GetIdToken())
	if err != nil {
		return nil, err
	}

	if err := s.identities.Bind(ctx, subject.ID, identity.SourceGoogle, verified.Subject, verified.Email); err != nil {
		return nil, toIdentityConnectError(err)
	}
	s.logger.Info("已绑定登录渠道",
		zap.String("subject_id", subject.ID),
		zap.String("source", identity.SourceGoogle),
		zap.String("identity_id", verified.Subject),
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
