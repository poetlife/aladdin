//go:build e2e

package e2e

import (
	"context"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	rbacv1 "github.com/poetlife/aladdin/api/gen/aladdin/rbac/v1"
	"github.com/poetlife/aladdin/internal/rbac"
)

// 本文件覆盖管理面的两条写路径，它们此前因"没有持久化"而留空：
// 角色回收（grant=false）与删除角色前的占用校验。
//
// 走真实 gRPC 连接而不是直接调服务方法：这两条都在 proto 注解的权限约束
// 之下，绕过拦截器就等于没验证它们在真实链路上能不能用。

// adminClient 用一个持有全部权限的会话连上服务端。
//
// system.admin 的权限是通配的 `*`，因此它既能读也能写；
// 用它起夹具，被测的才是管理面本身而不是权限不足。
func adminClient(t *testing.T) (context.Context, rbacv1.RBACServiceClient, func()) {
	t.Helper()
	h := startServer(t, rbac.RoleSystemAdmin, testScope)
	c := h.dial(t, testToken, testScope)
	ctx, cancel := c.Context()
	return ctx, rbacv1.NewRBACServiceClient(c.Conn()), cancel
}

func listBindings(t *testing.T, ctx context.Context, svc rbacv1.RBACServiceClient, subject string) []*rbacv1.RoleBinding {
	t.Helper()
	resp, err := svc.ListSubjectBindings(ctx, &rbacv1.ListSubjectBindingsRequest{
		SubjectId: subject,
		Scope:     testScope,
	})
	if err != nil {
		t.Fatalf("ListSubjectBindings 失败: %v", err)
	}
	return resp.GetBindings()
}

func assign(t *testing.T, ctx context.Context, svc rbacv1.RBACServiceClient, roleID string, grant bool) error {
	t.Helper()
	_, err := svc.AssignRole(ctx, &rbacv1.AssignRoleRequest{
		SubjectId: testSubject,
		RoleId:    roleID,
		Scope:     testScope,
		Grant:     grant,
	})
	return err
}

// putRole 在测试作用域上写入一个角色定义。
//
// scope 必须显式带上：这几个方法的 scope_source 是请求字段，不带就是
// 全局作用域，而夹具的绑定在 testScope 上——那会得到 PermissionDenied，
// 看起来像"权限不够"，实际是漏了一个字段。
func putRole(t *testing.T, ctx context.Context, svc rbacv1.RBACServiceClient, role *rbacv1.Role) {
	t.Helper()
	if _, err := svc.PutRole(ctx, &rbacv1.PutRoleRequest{Scope: testScope, Role: role}); err != nil {
		t.Fatalf("创建角色 %s 失败: %v", role.GetId(), err)
	}
}

func deleteRole(ctx context.Context, svc rbacv1.RBACServiceClient, roleID string) error {
	_, err := svc.DeleteRole(ctx, &rbacv1.DeleteRoleRequest{Scope: testScope, RoleId: roleID})
	return err
}

// TestAssignAndRevoke 覆盖授予与回收在同一条链路上的往返。
//
// 两边都必须幂等：重复授予不该产生第二条绑定，重复回收也不该报错——
// 调用方不该为了"再点一次"去写判断。
func TestAssignAndRevoke(t *testing.T) {
	ctx, svc, cancel := adminClient(t)
	defer cancel()

	base := len(listBindings(t, ctx, svc, testSubject))

	for range 2 {
		if err := assign(t, ctx, svc, rbac.RoleAuditor, true); err != nil {
			t.Fatalf("授予失败: %v", err)
		}
	}
	if got := len(listBindings(t, ctx, svc, testSubject)); got != base+1 {
		t.Fatalf("重复授予后绑定数 = %d，期望 %d", got, base+1)
	}

	for range 2 {
		if err := assign(t, ctx, svc, rbac.RoleAuditor, false); err != nil {
			t.Fatalf("回收失败: %v", err)
		}
	}
	if got := len(listBindings(t, ctx, svc, testSubject)); got != base {
		t.Fatalf("回收后绑定数 = %d，期望回到 %d", got, base)
	}
}

// 回收一个不存在的角色标识要报错：静默成功会把"写错了角色名"藏起来。
func TestRevokeUnknownRoleIsNotFound(t *testing.T) {
	ctx, svc, cancel := adminClient(t)
	defer cancel()

	err := assign(t, ctx, svc, "no.such.role", false)
	if got := status.Code(err); got != codes.NotFound {
		t.Fatalf("状态码 = %s, want NotFound (err=%v)", got, err)
	}
}

// TestRoleDeletionRespectsOccupancy 覆盖删除角色前的占用校验。
//
// 校验用的是**库里的真实绑定**而不是请求带来的信息：角色是否还被持有
// 只有存储知道，调用方看到的是可能已经过期的视图。
func TestRoleDeletionRespectsOccupancy(t *testing.T) {
	ctx, svc, cancel := adminClient(t)
	defer cancel()

	const custom = "e2e.custom"
	putRole(t, ctx, svc, &rbacv1.Role{
		Id: custom, DisplayName: "端到端自定义角色",
		Permissions: []string{rbac.PermissionAuditLogRead.String()},
	})

	if err := assign(t, ctx, svc, custom, true); err != nil {
		t.Fatalf("授予失败: %v", err)
	}
	err := deleteRole(ctx, svc, custom)
	if got := status.Code(err); got != codes.FailedPrecondition {
		t.Fatalf("仍被持有时删除的状态码 = %s, want FailedPrecondition (err=%v)", got, err)
	}

	if err := assign(t, ctx, svc, custom, false); err != nil {
		t.Fatalf("回收失败: %v", err)
	}
	if err := deleteRole(ctx, svc, custom); err != nil {
		t.Fatalf("无人持有后应可删除，实际 %v", err)
	}
	// 删掉之后再删一次必须报"不存在"，而不是静默成功。
	err = deleteRole(ctx, svc, custom)
	if got := status.Code(err); got != codes.NotFound {
		t.Fatalf("重复删除的状态码 = %s, want NotFound (err=%v)", got, err)
	}
}

// 仍被其他角色继承时同样不可删除——占用校验的另一半。
func TestRoleDeletionRespectsInheritance(t *testing.T) {
	ctx, svc, cancel := adminClient(t)
	defer cancel()

	const (
		parent = "e2e.parent"
		child  = "e2e.child"
	)
	for _, role := range []*rbacv1.Role{
		{Id: parent, DisplayName: "父角色", Permissions: []string{rbac.PermissionAuditLogRead.String()}},
		{Id: child, DisplayName: "子角色", Inherits: []string{parent}},
	} {
		putRole(t, ctx, svc, role)
	}

	err := deleteRole(ctx, svc, parent)
	if got := status.Code(err); got != codes.FailedPrecondition {
		t.Fatalf("仍被继承时删除的状态码 = %s, want FailedPrecondition (err=%v)", got, err)
	}
}

// 内置角色不可删除。这条规则住在领域校验里，因此对内存实现与库实现都成立——
// 它在真实链路上的表现就是这里断言的状态码。
func TestBuiltinRoleIsUndeletableOverWire(t *testing.T) {
	ctx, svc, cancel := adminClient(t)
	defer cancel()

	err := deleteRole(ctx, svc, rbac.RoleViewer)
	if got := status.Code(err); got != codes.FailedPrecondition {
		t.Fatalf("状态码 = %s, want FailedPrecondition (err=%v)", got, err)
	}
}
