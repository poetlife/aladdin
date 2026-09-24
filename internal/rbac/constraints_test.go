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
		{
			// 同一条事实的重复授予是幂等的，不是互斥——把它算成互斥会让
			// "再点一次保存"变成一个调用方无法与真冲突区分开的失败。
			name:     "已持有同一角色不算互斥",
			existing: []RoleBinding{{SubjectID: "u1", RoleID: "plain", Scope: scope}},
			roleID:   "plain",
		},
		{
			// 即使该角色自己出现在互斥名单里，重复授予它本身也不构成冲突：
			// 互斥说的是"两个角色不能同时持有"。
			name:     "已持有互斥名单中的角色本身不算互斥",
			existing: []RoleBinding{{SubjectID: "u1", RoleID: "executor", Scope: scope}},
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

// TestBuiltinRoleIsUndeletable 覆盖内置角色的不可删除性。
//
// 这条规则住在 ValidateRoleDeletion 里，而不是各存储实现的 DeleteRole 里：
// 存储实现只执行不判定（见 MutableStore），否则内存实现与数据库实现会
// 各自答一遍，而其中一份迟早会漏。
func TestBuiltinRoleIsUndeletable(t *testing.T) {
	roles := roleIndex(RoleDefinition{ID: RoleSystemAdmin, Builtin: true}, RoleDefinition{ID: "custom"})

	if err := ValidateRoleDeletion(roles, nil, RoleSystemAdmin); !errors.Is(err, ErrBuiltinRoleUndeletable) {
		t.Errorf("删除内置角色应报 ErrBuiltinRoleUndeletable，实际 %v", err)
	}
	if err := ValidateRoleDeletion(roles, nil, "custom"); err != nil {
		t.Errorf("删除自定义角色不应报错，实际 %v", err)
	}
}

// TestMemoryStoreDeleteRole 覆盖存储侧的删除语义：只执行、不判定。
func TestMemoryStoreDeleteRole(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()

	if err := store.PutRole(ctx, RoleDefinition{ID: "custom"}); err != nil {
		t.Fatalf("写入自定义角色失败: %v", err)
	}
	if err := store.DeleteRole(ctx, "custom"); err != nil {
		t.Errorf("自定义角色应可删除，实际 %v", err)
	}
	// 删除不存在的角色必须报错：静默成功会把"拼错了角色标识"藏起来。
	if err := store.DeleteRole(ctx, "custom"); !errors.Is(err, ErrRoleNotFound) {
		t.Errorf("删除不存在的角色应报 ErrRoleNotFound，实际 %v", err)
	}
}

// TestMemoryStoreBindIsIdempotent 覆盖"重复授予不产生重复绑定"。
func TestMemoryStoreBindIsIdempotent(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	putSubjects(t, store, Subject{ID: "u1"})
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
