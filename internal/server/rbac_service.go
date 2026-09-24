package server

import (
	"context"
	"errors"

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
}

// NewRBACService 构造管理面服务。
func NewRBACService(store rbac.MutableStore, engine *rbac.Engine) *RBACService {
	return &RBACService{store: store, engine: engine}
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

	binding := rbac.RoleBinding{
		SubjectID: req.Msg.GetSubjectId(),
		RoleID:    req.Msg.GetRoleId(),
		Scope:     rbac.Scope(req.Msg.GetScope()),
	}
	if !req.Msg.GetGrant() {
		// 撤销是幂等的：绑定本来就不存在时 Unbind 不报错（见 MutableStore）。
		if err := s.store.Unbind(ctx, binding); err != nil {
			return nil, toConnectError(err)
		}
		return connect.NewResponse(&rbacv1.AssignRoleResponse{
			ChangeId: changeID("binding", binding.SubjectID),
		}), nil
	}

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
	case errors.Is(err, rbac.ErrRoleNotFound), errors.Is(err, rbac.ErrSubjectNotFound):
		return connect.NewError(connect.CodeNotFound, err)
	case errors.Is(err, rbac.ErrStoreUnavailable):
		return connect.NewError(connect.CodeUnavailable, err)
	default:
		// 约束类错误（互斥、成环、被占用）都是调用方的输入问题。
		return connect.NewError(connect.CodeFailedPrecondition, err)
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
