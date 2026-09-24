package server

import (
	"context"
	"errors"
	"time"

	"connectrpc.com/connect"

	identityv1 "github.com/poetlife/aladdin/api/gen/aladdin/identity/v1"
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
	authn  *interceptor.TokenAuthenticator
}

// NewIdentityService 构造认证面服务。
func NewIdentityService(store rbac.MutableStore, engine *rbac.Engine) *IdentityService {
	return &IdentityService{store: store, engine: engine}
}

// SetAuthenticator 注入认证器，使 Login 可以签发开发用 token。
//
// 分离成 setter 是为了避免构造顺序上的循环依赖：
// Server 先构造服务再构造认证器。
func (s *IdentityService) SetAuthenticator(authn *interceptor.TokenAuthenticator) {
	s.authn = authn
}

// Login 实现 IdentityService。
//
// 它是公开方法（proto 上标记 public），因此不经过认证中间件。
//
// 骨架自带的实现只接受 token 形式的凭证，把 token 直接作为访问凭证返回。
// 生产环境应替换为口令校验或外部身份服务——替换点只有本方法。
func (s *IdentityService) Login(_ context.Context, req *connect.Request[identityv1.LoginRequest]) (*connect.Response[identityv1.LoginResponse], error) {
	token := ""
	switch cred := req.Msg.GetCredential().(type) {
	case *identityv1.LoginRequest_Token:
		token = cred.Token.GetToken()
	case *identityv1.LoginRequest_Password:
		// 口令认证尚未实现。返回 Unimplemented 而不是伪造成功，
		// 避免开发环境误以为认证已经生效。
		return nil, connect.NewError(connect.CodeUnimplemented,
			errors.New("口令认证尚未实现，请使用 token 凭证"))
	default:
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("缺少凭证"))
	}
	if token == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("凭证不能为空"))
	}
	if _, err := s.authenticateToken(token); err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("凭证无效"))
	}
	return connect.NewResponse(&identityv1.LoginResponse{
		AccessToken: token,
		ExpiresAt:   time.Now().UTC().Add(12 * time.Hour).Format(time.RFC3339),
	}), nil
}

// Refresh 实现 IdentityService。
//
// 骨架阶段访问凭证不设独立有效期，因此 Refresh 是幂等的原样返回。
// 接入真实凭证后，此处改为签发新凭证并作废旧凭证。
func (s *IdentityService) Refresh(_ context.Context, req *connect.Request[identityv1.RefreshRequest]) (*connect.Response[identityv1.RefreshResponse], error) {
	if req.Msg.GetAccessToken() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			errors.New("access_token 不能为空"))
	}
	if _, err := s.authenticateToken(req.Msg.GetAccessToken()); err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated,
			errors.New("凭证已失效，请重新登录"))
	}
	return connect.NewResponse(&identityv1.RefreshResponse{
		AccessToken: req.Msg.GetAccessToken(),
		ExpiresAt:   time.Now().UTC().Add(12 * time.Hour).Format(time.RFC3339),
	}), nil
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

func (s *IdentityService) authenticateToken(token string) (rbac.Subject, error) {
	if s.authn == nil {
		return rbac.Subject{}, errors.New("认证器未注入")
	}
	subject, ok := s.authn.Tokens[token]
	if !ok {
		return rbac.Subject{}, errors.New("凭证无效")
	}
	return subject, nil
}
