package server

import (
	"context"
	"errors"
	"fmt"

	"connectrpc.com/connect"

	rbacv1 "github.com/poetlife/aladdin/api/gen/aladdin/rbac/v1"
	"github.com/poetlife/aladdin/internal/rbac"
)

// RBACService 实现权限体系的管理面。
//
// 它只负责**管理**角色与授权关系；判定由鉴权拦截器统一执行，
// 因此这里的方法本身也受 proto 注解约束。
//
// 方法签名是 Connect 风格的，不是 gRPC 风格。刻意不做"保留 gRPC 签名 +
// 写一层适配器"（memo 的做法）：我们的服务实现本来就只是转发给
// rbac.Engine 与 store，多一层适配只是多一份需要同步维护的样板。
type RBACService struct {
	store  rbac.MutableStore
	engine *rbac.Engine
	// gate 与空主体认领共用：授角色与"这个主体有没有角色"的检查必须串行。
	gate *subjectLifecycleGate
}

// NewRBACService 构造管理面服务。
func NewRBACService(store rbac.MutableStore, engine *rbac.Engine, gate *subjectLifecycleGate) *RBACService {
	return &RBACService{store: store, engine: engine, gate: gate}
}

// GetRole 实现 RBACService。
func (s *RBACService) GetRole(ctx context.Context, req *connect.Request[rbacv1.GetRoleRequest]) (*connect.Response[rbacv1.GetRoleResponse], error) {
	role, err := s.store.Role(ctx, req.Msg.GetRoleId())
	if err != nil {
		return nil, toConnectError(err)
	}
	return connect.NewResponse(&rbacv1.GetRoleResponse{Role: toProtoRole(role)}), nil
}

// ListRoles 实现 RBACService。
func (s *RBACService) ListRoles(ctx context.Context, _ *connect.Request[rbacv1.ListRolesRequest]) (*connect.Response[rbacv1.ListRolesResponse], error) {
	roles, err := s.store.Roles(ctx)
	if err != nil {
		return nil, toConnectError(err)
	}
	out := make([]*rbacv1.Role, 0, len(roles))
	for _, r := range roles {
		out = append(out, toProtoRole(r))
	}
	return connect.NewResponse(&rbacv1.ListRolesResponse{Roles: out}), nil
}

// PutRole 实现 RBACService。对应"变更受理 → 约束校验 → 持久化"三个阶段。
func (s *RBACService) PutRole(ctx context.Context, req *connect.Request[rbacv1.PutRoleRequest]) (*connect.Response[rbacv1.PutRoleResponse], error) {
	if req.Msg.GetRole() == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("role 不能为空"))
	}
	candidate, err := fromProtoRole(req.Msg.GetRole())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}

	roles, err := s.roleIndex(ctx)
	if err != nil {
		return nil, toConnectError(err)
	}
	if existing, ok := roles[candidate.ID]; ok {
		if err := rbac.ValidateImmutable(existing, candidate); err != nil {
			return nil, toConnectError(err)
		}
	}
	if err := rbac.ValidateInheritance(roles, candidate); err != nil {
		return nil, toConnectError(err)
	}
	if err := s.store.PutRole(ctx, candidate); err != nil {
		return nil, toConnectError(err)
	}

	return connect.NewResponse(&rbacv1.PutRoleResponse{
		Role:     toProtoRole(candidate),
		ChangeId: changeID("role", candidate.ID),
	}), nil
}

// DeleteRole 实现 RBACService。
//
// 校验用的是**库里的真实绑定**而不是请求里带来的信息：角色是否还被主体
// 持有只有存储知道，而调用方（CLI、前端）看到的是可能已经过期的视图。
func (s *RBACService) DeleteRole(ctx context.Context, req *connect.Request[rbacv1.DeleteRoleRequest]) (*connect.Response[rbacv1.DeleteRoleResponse], error) {
	roles, err := s.roleIndex(ctx)
	if err != nil {
		return nil, toConnectError(err)
	}
	bindings, err := s.store.BindingsOfRole(ctx, req.Msg.GetRoleId())
	if err != nil {
		return nil, toConnectError(err)
	}
	if err := rbac.ValidateRoleDeletion(roles, bindings, req.Msg.GetRoleId()); err != nil {
		return nil, toConnectError(err)
	}
	if err := s.store.DeleteRole(ctx, req.Msg.GetRoleId()); err != nil {
		return nil, toConnectError(err)
	}
	return connect.NewResponse(&rbacv1.DeleteRoleResponse{
		ChangeId: changeID("role", req.Msg.GetRoleId()),
	}), nil
}

// AssignRole 实现 RBACService。
//
// 授予与回收走同一条路径（proto 上由 grant 区分），但走的校验不同：
// 回收不可能引入新的互斥冲突，因此不做互斥校验——这正是
// ValidateAssignment 只在授予侧调用的原因。两边共同的前提是角色存在：
// 回收一个不存在的角色多半意味着调用方写错了角色标识，静默成功会把它藏起来。
func (s *RBACService) AssignRole(ctx context.Context, req *connect.Request[rbacv1.AssignRoleRequest]) (*connect.Response[rbacv1.AssignRoleResponse], error) {
	roles, err := s.roleIndex(ctx)
	if err != nil {
		return nil, toConnectError(err)
	}
	if _, ok := roles[req.Msg.GetRoleId()]; !ok {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("角色不存在"))
	}
	// 目标范围必须已登记（全局除外，它是模型的根）。这条与上面那条判据同构：
	// 角色要有定义才能授，范围要有登记才能绑（见 docs/design/rbac/scopes.md）。
	//
	// **只在授予侧校验**：下面回收分支不查——撤收窄永远要能做，哪怕那条绑定
	// 落在的范围已经不在目录里（历史数据）。与"互斥只在授予时校验"同理。
	if !req.Msg.GetGrant() {
		// 撤销是幂等的：绑定本来就不存在时 Unbind 不报错（见 MutableStore）。
		binding := rbac.RoleBinding{
			SubjectID: req.Msg.GetSubjectId(),
			RoleID:    req.Msg.GetRoleId(),
			Scope:     rbac.Scope(req.Msg.GetScope()),
		}
		if err := s.store.Unbind(ctx, binding); err != nil {
			return nil, toConnectError(err)
		}
		return connect.NewResponse(&rbacv1.AssignRoleResponse{
			ChangeId: changeID("binding", binding.SubjectID),
		}), nil
	}

	if err := s.requireRegisteredScope(ctx, req.Msg.GetScope()); err != nil {
		return nil, err
	}

	binding := rbac.RoleBinding{
		SubjectID: req.Msg.GetSubjectId(),
		RoleID:    req.Msg.GetRoleId(),
		Scope:     rbac.Scope(req.Msg.GetScope()),
	}

	// 授予角色与空主体认领串行：认领要确认"这个主体没有角色绑定"，
	// 若两者之间刚好插进一次授予，就会出现身份移走、角色留下的搁浅。
	unlock := s.gate.lock()
	defer unlock()

	existing, err := s.store.SubjectBindings(ctx, binding.SubjectID)
	if err != nil && !errors.Is(err, rbac.ErrSubjectNotFound) {
		return nil, toConnectError(err)
	}
	if err := rbac.ValidateAssignment(roles, existing, binding, binding.Scope); err != nil {
		return nil, toConnectError(err)
	}
	if err := s.store.Bind(ctx, binding); err != nil {
		return nil, toConnectError(err)
	}
	return connect.NewResponse(&rbacv1.AssignRoleResponse{
		ChangeId: changeID("binding", binding.SubjectID),
	}), nil
}

// ListSubjectBindings 实现 RBACService。
//
// 返回的 effective_permissions 是展开后的最终集合，
// 前端会话权限即来源于此，前端不需要（也不允许）自行展开。
func (s *RBACService) ListSubjectBindings(ctx context.Context, req *connect.Request[rbacv1.ListSubjectBindingsRequest]) (*connect.Response[rbacv1.ListSubjectBindingsResponse], error) {
	bindings, err := s.store.SubjectBindings(ctx, req.Msg.GetSubjectId())
	if err != nil {
		return nil, toConnectError(err)
	}
	out := make([]*rbacv1.RoleBinding, 0, len(bindings))
	for _, b := range bindings {
		out = append(out, &rbacv1.RoleBinding{
			SubjectId: b.SubjectID,
			RoleId:    b.RoleID,
			Scope:     string(b.Scope),
		})
	}

	permissions, _, err := s.engine.EffectivePermissions(ctx, rbac.Subject{
		ID:           req.Msg.GetSubjectId(),
		Type:         rbac.SubjectTypeUser,
		DefaultScope: rbac.Scope(req.Msg.GetScope()),
	}, rbac.Scope(req.Msg.GetScope()))
	if err != nil {
		return nil, toConnectError(err)
	}
	codes := make([]string, 0, len(permissions))
	for _, p := range permissions {
		codes = append(codes, p.String())
	}
	return connect.NewResponse(&rbacv1.ListSubjectBindingsResponse{
		Bindings:             out,
		EffectivePermissions: codes,
	}), nil
}

// ListScopes 实现 RBACService。
//
// 返回登记过的范围，**不含全局**：全局是模型的根，不是一条登记记录，因此它既
// 没得登记也删不掉（见 docs/design/rbac/scopes.md）。
func (s *RBACService) ListScopes(ctx context.Context, _ *connect.Request[rbacv1.ListScopesRequest]) (*connect.Response[rbacv1.ListScopesResponse], error) {
	scopes, err := s.store.Scopes(ctx)
	if err != nil {
		return nil, toConnectError(err)
	}
	out := make([]*rbacv1.Scope, 0, len(scopes))
	for _, sc := range scopes {
		out = append(out, toProtoScope(sc))
	}
	return connect.NewResponse(&rbacv1.ListScopesResponse{Scopes: out}), nil
}

// PutScope 实现 RBACService。
//
// 路径是标识：登记一个已存在的路径等同于改显示名（存储侧按主键覆盖），
// **不存在"改路径"**——那是"删掉旧范围 + 新建一个"，与角色标识同构。
func (s *RBACService) PutScope(ctx context.Context, req *connect.Request[rbacv1.PutScopeRequest]) (*connect.Response[rbacv1.PutScopeResponse], error) {
	path := req.Msg.GetPath()
	if rbac.Scope(path) == rbac.GlobalScope {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("全局不需要登记：它永远可用，但不是目录里的一条"))
	}
	candidate := rbac.ScopeDefinition{Path: path, DisplayName: req.Msg.GetDisplayName()}
	if err := s.store.PutScope(ctx, candidate); err != nil {
		return nil, toConnectError(err)
	}
	return connect.NewResponse(&rbacv1.PutScopeResponse{Scope: toProtoScope(candidate)}), nil
}

// DeleteScope 实现 RBACService。
//
// 引用校验用的是**库里的真实绑定**而不是请求里带来的信息：范围内还有没有绑定
// 只有存储知道，而调用方（CLI、前端）看到的是可能已经过期的视图——与 DeleteRole
// 同一个判据。
func (s *RBACService) DeleteScope(ctx context.Context, req *connect.Request[rbacv1.DeleteScopeRequest]) (*connect.Response[rbacv1.DeleteScopeResponse], error) {
	path := req.Msg.GetPath()
	if rbac.Scope(path) == rbac.GlobalScope {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("全局不可删除"))
	}
	bindings, err := s.store.BindingsUnderScope(ctx, rbac.Scope(path))
	if err != nil {
		return nil, toConnectError(err)
	}
	if err := rbac.ValidateScopeDeletion(path, bindings); err != nil {
		return nil, toConnectError(err)
	}
	if err := s.store.DeleteScope(ctx, path); err != nil {
		return nil, toConnectError(err)
	}
	return connect.NewResponse(&rbacv1.DeleteScopeResponse{}), nil
}

// PublishPolicy 实现 RBACService。
//
// 它对应 docs/design/rbac/role-model.md 中"缓存失效"与"生效确认"两个阶段。
// 骨架阶段判定不走缓存，因此失效条目数恒为 0；接入缓存后此处即失效入口。
func (s *RBACService) PublishPolicy(_ context.Context, req *connect.Request[rbacv1.PublishPolicyRequest]) (*connect.Response[rbacv1.PublishPolicyResponse], error) {
	if req.Msg.GetChangeId() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("change_id 不能为空"))
	}
	return connect.NewResponse(&rbacv1.PublishPolicyResponse{
		ChangeId:           req.Msg.GetChangeId(),
		InvalidatedEntries: 0,
	}), nil
}

// requireRegisteredScope 校验目标范围已登记。
//
// 全局不需要登记：它是模型的根，不是目录里的一条。**存储故障必须原样上报**——
// 把它当成"未登记"会把一次数据库故障表现成一次输入错误，于是管理员去改范围
// 配置，而真正的故障被掩盖了。
func (s *RBACService) requireRegisteredScope(ctx context.Context, path string) error {
	if rbac.Scope(path) == rbac.GlobalScope {
		return nil
	}
	if _, err := s.store.Scope(ctx, path); err != nil {
		if errors.Is(err, rbac.ErrScopeNotFound) {
			return connect.NewError(connect.CodeNotFound,
				fmt.Errorf("范围未登记: %s。请先登记这个范围，再授予角色", path))
		}
		return toConnectError(err)
	}
	return nil
}

func (s *RBACService) roleIndex(ctx context.Context) (map[string]rbac.RoleDefinition, error) {
	list, err := s.store.Roles(ctx)
	if err != nil {
		return nil, err
	}
	index := make(map[string]rbac.RoleDefinition, len(list))
	for _, r := range list {
		index[r.ID] = r
	}
	return index, nil
}

func changeID(kind, target string) string {
	return kind + ":" + target
}

// toConnectError 把领域错误映射为 Connect 错误码。
func toConnectError(err error) error {
	switch {
	case errors.Is(err, rbac.ErrRoleNotFound), errors.Is(err, rbac.ErrSubjectNotFound), errors.Is(err, rbac.ErrScopeNotFound):
		return connect.NewError(connect.CodeNotFound, err)
	case errors.Is(err, rbac.ErrStoreUnavailable):
		return connect.NewError(connect.CodeUnavailable, err)
	default:
		// 约束类错误（互斥、成环、被占用）都是调用方的输入问题。
		return connect.NewError(connect.CodeFailedPrecondition, err)
	}
}

func toProtoScope(scope rbac.ScopeDefinition) *rbacv1.Scope {
	return &rbacv1.Scope{
		Path:        scope.Path,
		DisplayName: scope.DisplayName,
	}
}

func toProtoRole(r rbac.RoleDefinition) *rbacv1.Role {
	perms := make([]string, 0, len(r.Permissions))
	for _, p := range r.Permissions {
		perms = append(perms, p.String())
	}
	return &rbacv1.Role{
		Id:                    r.ID,
		DisplayName:           r.DisplayName,
		Permissions:           perms,
		Inherits:              r.Inherits,
		MutuallyExclusiveWith: r.MutuallyExclusiveWith,
		Builtin:               r.Builtin,
	}
}

func fromProtoRole(r *rbacv1.Role) (rbac.RoleDefinition, error) {
	if r.GetId() == "" {
		return rbac.RoleDefinition{}, errors.New("role.id 不能为空")
	}
	perms := make([]rbac.PermissionCode, 0, len(r.GetPermissions()))
	for _, raw := range r.GetPermissions() {
		p, err := rbac.ParsePermissionCode(raw)
		if err != nil {
			return rbac.RoleDefinition{}, err
		}
		perms = append(perms, p)
	}
	return rbac.RoleDefinition{
		ID:                    r.GetId(),
		DisplayName:           r.GetDisplayName(),
		Builtin:               r.GetBuiltin(),
		Permissions:           perms,
		Inherits:              r.GetInherits(),
		MutuallyExclusiveWith: r.GetMutuallyExclusiveWith(),
	}, nil
}
