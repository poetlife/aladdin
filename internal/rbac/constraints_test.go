package rbac

import (
	"context"
	"errors"
	"testing"
)

func roleIndex(roles ...RoleDefinition) map[string]RoleDefinition {
	index := make(map[string]RoleDefinition, len(roles))
	for _, r := range roles {
		index[r.ID] = r
	}
	return index
}

func TestValidateInheritance(t *testing.T) {
	base := roleIndex(
		RoleDefinition{ID: "a"},
		RoleDefinition{ID: "b", Inherits: []string{"a"}},
		RoleDefinition{ID: "c", Inherits: []string{"b"}},
	)

	tests := []struct {
		name      string
		candidate RoleDefinition
		wantErr   error
	}{
		{
			name:      "合法继承",
			candidate: RoleDefinition{ID: "d", Inherits: []string{"c"}},
		},
		{
			name:      "自环",
			candidate: RoleDefinition{ID: "a", Inherits: []string{"a"}},
			wantErr:   ErrInheritanceCycle,
		},
		{
			name:      "间接成环",
			candidate: RoleDefinition{ID: "a", Inherits: []string{"c"}},
			wantErr:   ErrInheritanceCycle,
		},
		{
			name:      "继承不存在的角色",
			candidate: RoleDefinition{ID: "e", Inherits: []string{"missing"}},
			wantErr:   ErrRoleNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateInheritance(base, tt.candidate)
			if tt.wantErr == nil {
				if err != nil {
					t.Fatalf("不应报错，实际 %v", err)
				}
				return
			}
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("错误 = %v, want 包裹 %v", err, tt.wantErr)
			}
		})
	}
}

func TestValidateImmutable(t *testing.T) {
	builtin := RoleDefinition{ID: RoleSystemAdmin, Builtin: true}

	if err := ValidateImmutable(builtin, builtin); err != nil {
		t.Errorf("未做修改时不应报错，实际 %v", err)
	}
	if err := ValidateImmutable(builtin, RoleDefinition{ID: RoleSystemAdmin}); !errors.Is(err, ErrBuiltinRoleImmutable) {
		t.Errorf("去掉内置标记应被拒绝，实际 %v", err)
	}
	custom := RoleDefinition{ID: "custom"}
	if err := ValidateImmutable(custom, RoleDefinition{ID: "custom", Builtin: true}); err != nil {
		t.Errorf("非内置角色不受该约束，实际 %v", err)
	}
}

func TestValidateAssignment(t *testing.T) {
	roles := roleIndex(
		RoleDefinition{ID: "approver"},
		RoleDefinition{ID: "executor", MutuallyExclusiveWith: []string{"approver"}},
		RoleDefinition{ID: "plain"},
	)
	scope := Scope("tenant/acme")

	tests := []struct {
		name     string
		existing []RoleBinding
		roleID   string
		wantErr  error
	}{
		{
			name:   "无冲突的授予",
			roleID: "plain",
		},
		{
			name:     "单向声明互斥即生效",
			existing: []RoleBinding{{SubjectID: "u1", RoleID: "approver", Scope: scope}},
			roleID:   "executor",
			wantErr:  ErrMutuallyExclusive,
		},
		{
			name:     "反向授予同样被拒",
			existing: []RoleBinding{{SubjectID: "u1", RoleID: "executor", Scope: scope}},
			roleID:   "approver",
			wantErr:  ErrMutuallyExclusive,
		},
		{
			name:     "不同作用域不构成冲突",
			existing: []RoleBinding{{SubjectID: "u1", RoleID: "approver", Scope: "tenant/other"}},
			roleID:   "executor",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateAssignment(roles, tt.existing,
				RoleBinding{SubjectID: "u1", RoleID: tt.roleID, Scope: scope}, scope)
			if tt.wantErr == nil {
				if err != nil {
					t.Fatalf("不应报错，实际 %v", err)
				}
				return
			}
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("错误 = %v, want 包裹 %v", err, tt.wantErr)
			}
		})
	}
}

func TestValidateRoleDeletion(t *testing.T) {
	roles := roleIndex(
		RoleDefinition{ID: "child", Inherits: []string{"parent"}},
		RoleDefinition{ID: "parent"},
		RoleDefinition{ID: "orphan"},
	)
	bindings := []RoleBinding{{SubjectID: "u1", RoleID: "held", Scope: "root"}}

	tests := []struct {
		name    string
		roleID  string
		wantErr error
	}{
		{"仍被继承", "parent", ErrRoleInUse},
		{"仍被持有", "held", ErrRoleInUse},
		{"无人使用", "orphan", nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateRoleDeletion(roles, bindings, tt.roleID)
			if tt.wantErr == nil {
				if err != nil {
					t.Fatalf("不应报错，实际 %v", err)
				}
				return
			}
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("错误 = %v, want 包裹 %v", err, tt.wantErr)
			}
		})
	}
}

// TestMemoryStoreBuiltinProtection 覆盖内置角色的不可删除性。
func TestMemoryStoreBuiltinProtection(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()

	if err := store.DeleteRole(ctx, RoleSystemAdmin); err == nil {
		t.Error("内置角色应不可删除")
	}
	if err := store.PutRole(ctx, RoleDefinition{ID: "custom"}); err != nil {
		t.Fatalf("写入自定义角色失败: %v", err)
	}
	if err := store.DeleteRole(ctx, "custom"); err != nil {
		t.Errorf("自定义角色应可删除，实际 %v", err)
	}
}

// TestMemoryStoreBindIsIdempotent 覆盖"重复授予不产生重复绑定"。
func TestMemoryStoreBindIsIdempotent(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	store.RegisterSubject(Subject{ID: "u1"})
	binding := RoleBinding{SubjectID: "u1", RoleID: RoleViewer, Scope: "root"}

	for range 3 {
		if err := store.Bind(ctx, binding); err != nil {
			t.Fatalf("绑定失败: %v", err)
		}
	}
	got, err := store.SubjectBindings(ctx, "u1")
	if err != nil {
		t.Fatalf("读取绑定失败: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("绑定数 = %d, want 1", len(got))
	}
}
