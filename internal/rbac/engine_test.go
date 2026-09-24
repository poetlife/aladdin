package rbac

import (
	"context"
	"testing"
)

// newTestEngine 构造一套可复用的判定夹具。
//
// 主体 u1 在 tenant/acme 上持有 viewer（只读角色）。
func newTestEngine(t *testing.T) (*Engine, *MemoryStore) {
	t.Helper()
	store := NewMemoryStore()
	store.RegisterSubject(Subject{ID: "u1", Type: SubjectTypeUser, DefaultScope: "tenant/acme"})
	store.RegisterSubject(Subject{ID: "admin", Type: SubjectTypeUser, DefaultScope: GlobalScope})
	store.RegisterSubject(Subject{ID: "nobody", Type: SubjectTypeUser, DefaultScope: "tenant/acme"})
	if err := store.Bind(context.Background(), RoleBinding{
		SubjectID: "u1", RoleID: RoleViewer, Scope: "tenant/acme",
	}); err != nil {
		t.Fatalf("绑定失败: %v", err)
	}
	if err := store.Bind(context.Background(), RoleBinding{
		SubjectID: "admin", RoleID: RoleSystemAdmin, Scope: GlobalScope,
	}); err != nil {
		t.Fatalf("绑定失败: %v", err)
	}
	// nobody 已登记但没有任何绑定：用于区分"无匹配授权"与"主体不存在"。
	return NewEngine(store, nil, nil), store
}

func TestEngineCheck(t *testing.T) {
	tests := []struct {
		name       string
		subjectID  string
		permission PermissionCode
		scope      Scope
		wantAllow  bool
		wantReason Reason
	}{
		{
			name: "子作用域继承父作用域的授权", subjectID: "u1",
			permission: PermissionRbacRoleRead, scope: "tenant/acme/project/web",
			wantAllow: true, wantReason: ReasonAllow,
		},
		{
			name: "授予在自身作用域上生效", subjectID: "u1",
			permission: PermissionRbacRoleRead, scope: "tenant/acme",
			wantAllow: true, wantReason: ReasonAllow,
		},
		{
			name: "子作用域的授权不能用于父作用域", subjectID: "u1",
			permission: PermissionRbacRoleRead, scope: "tenant",
			wantAllow: false, wantReason: ReasonScopeMismatch,
		},
		{
			name: "角色未授予该权限", subjectID: "u1",
			permission: PermissionRbacRoleWrite, scope: "tenant/acme",
			wantAllow: false, wantReason: ReasonNoMatchingGrant,
		},
		{
			name: "系统管理员通配全部权限", subjectID: "admin",
			permission: PermissionRbacSubjectAssign, scope: "tenant/other/deep",
			wantAllow: true, wantReason: ReasonAllow,
		},
		{
			name: "已登记但无绑定的主体", subjectID: "nobody",
			permission: PermissionRbacRoleRead, scope: "tenant/acme",
			wantAllow: false, wantReason: ReasonNoMatchingGrant,
		},
		{
			name: "未登记的主体", subjectID: "ghost",
			permission: PermissionRbacRoleRead, scope: "tenant/acme",
			wantAllow: false, wantReason: ReasonSubjectNotFound,
		},
	}

	engine, _ := newTestEngine(t)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := engine.Check(context.Background(),
				Subject{ID: tt.subjectID}, tt.permission, tt.scope)
			if got.Allowed != tt.wantAllow {
				t.Errorf("Allowed = %v, want %v（reason=%s）", got.Allowed, tt.wantAllow, got.Reason)
			}
			if got.Reason != tt.wantReason {
				t.Errorf("Reason = %s, want %s", got.Reason, tt.wantReason)
			}
		})
	}
}

func TestEngineCheckSessionExpired(t *testing.T) {
	engine, _ := newTestEngine(t)
	got := engine.Check(context.Background(), Subject{}, PermissionRbacRoleRead, "tenant/acme")
	if got.Allowed {
		t.Error("空主体不应被允许")
	}
	if got.Reason != ReasonSessionExpired {
		t.Errorf("Reason = %s, want %s", got.Reason, ReasonSessionExpired)
	}
}

func TestEngineEffectivePermissions(t *testing.T) {
	engine, _ := newTestEngine(t)
	permissions, roles, err := engine.EffectivePermissions(context.Background(),
		Subject{ID: "u1"}, "tenant/acme/project")
	if err != nil {
		t.Fatalf("展开失败: %v", err)
	}
	if len(roles) != 1 || roles[0] != RoleViewer {
		t.Errorf("角色 = %v, want [%s]", roles, RoleViewer)
	}
	want := map[PermissionCode]bool{
		PermissionRbacRoleRead:    true,
		PermissionRbacSubjectRead: true,
	}
	if len(permissions) != len(want) {
		t.Fatalf("权限数 = %d (%v), want %d", len(permissions), permissions, len(want))
	}
	for _, p := range permissions {
		if !want[p] {
			t.Errorf("出现非预期权限 %q", p)
		}
	}
}

func TestEngineEffectivePermissionsUnknownSubject(t *testing.T) {
	engine, _ := newTestEngine(t)
	if _, _, err := engine.EffectivePermissions(context.Background(),
		Subject{ID: "ghost"}, "tenant/acme"); err == nil {
		t.Error("未登记的主体应返回错误")
	}
}

// TestEngineInheritance 覆盖角色继承的传递性。
func TestEngineInheritance(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	store.RegisterSubject(Subject{ID: "u1", Type: SubjectTypeUser, DefaultScope: "root"})
	putRole(t, store, ctx, RoleDefinition{
		ID: "leaf", DisplayName: "叶子",
		Permissions: []PermissionCode{PermissionAuditLogRead},
		Inherits:    []string{"middle"},
	})
	putRole(t, store, ctx, RoleDefinition{
		ID: "middle", DisplayName: "中间",
		Permissions: []PermissionCode{PermissionAuditLogExport},
		Inherits:    []string{"top"},
	})
	putRole(t, store, ctx, RoleDefinition{
		ID: "top", DisplayName: "顶层",
		Permissions: []PermissionCode{PermissionRbacRoleRead},
	})
	if err := store.Bind(ctx, RoleBinding{SubjectID: "u1", RoleID: "leaf", Scope: "root"}); err != nil {
		t.Fatalf("绑定失败: %v", err)
	}

	engine := NewEngine(store, nil, nil)
	for _, want := range []PermissionCode{
		PermissionAuditLogRead, PermissionAuditLogExport, PermissionRbacRoleRead,
	} {
		if got := engine.Check(ctx, Subject{ID: "u1"}, want, "root"); !got.Allowed {
			t.Errorf("继承链上的权限 %q 应被允许，实际 %s", want, got.Reason)
		}
	}
	if got := engine.Check(ctx, Subject{ID: "u1"}, PermissionRbacRoleWrite, "root"); got.Allowed {
		t.Error("未在继承链上的权限不应被允许")
	}
}

// TestEngineStoreUnavailable 覆盖存储故障时的判定语义。
//
// 关键约定：存储不可用**不是拒绝**。上层需要据此返回"服务不可用"，
// 让客户端退避重试，而不是以为权限不足。
func TestEngineStoreUnavailable(t *testing.T) {
	engine := NewEngine(failingStore{}, nil, nil)
	got := engine.Check(context.Background(), Subject{ID: "u1"}, PermissionRbacRoleRead, "root")
	if got.Allowed {
		t.Error("存储故障时不应允许")
	}
	if got.Reason != ReasonStoreUnavailable {
		t.Errorf("Reason = %s, want %s", got.Reason, ReasonStoreUnavailable)
	}
}

type failingStore struct{}

func (failingStore) Role(context.Context, string) (RoleDefinition, error) {
	return RoleDefinition{}, ErrStoreUnavailable
}

func (failingStore) Roles(context.Context) ([]RoleDefinition, error) {
	return nil, ErrStoreUnavailable
}

func (failingStore) SubjectBindings(context.Context, string) ([]RoleBinding, error) {
	return nil, ErrStoreUnavailable
}

func putRole(t *testing.T, store *MemoryStore, ctx context.Context, role RoleDefinition) {
	t.Helper()
	if err := store.PutRole(ctx, role); err != nil {
		t.Fatalf("写入角色 %q 失败: %v", role.ID, err)
	}
}
