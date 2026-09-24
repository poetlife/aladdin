package rbac

import (
	"context"
	"reflect"
	"testing"
)

// newEmptyStore 造一个"什么角色都没有"的存储，模拟一个全新的库。
//
// 不能用 NewMemoryStore：它自带内置角色，而"补缺失"这件事恰恰要求
// 起点是缺的。
func newEmptyStore() *MemoryStore {
	return &MemoryStore{
		roles:    map[string]RoleDefinition{},
		subjects: map[string]Subject{},
	}
}

func TestEnsureBuiltinRolesFillsMissing(t *testing.T) {
	ctx := context.Background()
	store := newEmptyStore()

	created, err := EnsureBuiltinRoles(ctx, store)
	if err != nil {
		t.Fatalf("初始化失败: %v", err)
	}
	if created != len(BuiltinRoles) {
		t.Errorf("新增 %d 个角色，期望 %d 个", created, len(BuiltinRoles))
	}

	roles, err := store.Roles(ctx)
	if err != nil {
		t.Fatalf("读取角色失败: %v", err)
	}
	if len(roles) != len(BuiltinRoles) {
		t.Errorf("库里有 %d 个角色，期望 %d 个", len(roles), len(BuiltinRoles))
	}
}

// 服务端每次启动都会跑一遍，因此它必须是幂等的：第二次不新增任何东西。
func TestEnsureBuiltinRolesIsIdempotent(t *testing.T) {
	ctx := context.Background()
	store := newEmptyStore()

	if _, err := EnsureBuiltinRoles(ctx, store); err != nil {
		t.Fatalf("首次初始化失败: %v", err)
	}
	created, err := EnsureBuiltinRoles(ctx, store)
	if err != nil {
		t.Fatalf("二次初始化失败: %v", err)
	}
	if created != 0 {
		t.Errorf("二次初始化新增了 %d 个角色，期望 0", created)
	}
}

// 只补缺失的，不动已存在的。
//
// 内置角色的权限绑定在部署时**可以**调整（见 docs/design/rbac/role-model.md），
// 每次启动都按代码里的定义覆盖一遍，等于把这种调整静默撤销——
// 而那正是"部署时可调整"这句承诺反悔的样子。
func TestEnsureBuiltinRolesKeepsDeploymentAdjustments(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()

	var target RoleDefinition
	for _, builtin := range BuiltinRoles {
		if builtin.ID == RoleAuditor {
			target = builtin
		}
	}
	if target.ID == "" {
		t.Fatalf("内置角色 %q 不存在，用例需要换一个", RoleAuditor)
	}

	adjusted := target
	adjusted.Permissions = append(append([]PermissionCode{}, target.Permissions...),
		PermissionAuditLogExport)
	adjusted.DisplayName = "部署时改过的显示名"
	if err := store.PutRole(ctx, adjusted); err != nil {
		t.Fatalf("写入调整后的角色失败: %v", err)
	}

	created, err := EnsureBuiltinRoles(ctx, store)
	if err != nil {
		t.Fatalf("初始化失败: %v", err)
	}
	if created != 0 {
		t.Errorf("新增了 %d 个角色，期望 0——已有角色不该被重写", created)
	}

	got, err := store.Role(ctx, RoleAuditor)
	if err != nil {
		t.Fatalf("读取角色失败: %v", err)
	}
	if !reflect.DeepEqual(got, adjusted) {
		t.Errorf("部署时的调整被启动流程覆盖了：\n实际 %+v\n期望 %+v", got, adjusted)
	}
}

// 只缺一部分时，补的也只能是缺的那部分。
func TestEnsureBuiltinRolesFillsOnlyMissing(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()

	// 模拟"某个内置角色在这个库里被删掉了"。
	if err := store.DeleteRole(ctx, RoleViewer); err != nil {
		t.Fatalf("删除角色失败: %v", err)
	}

	created, err := EnsureBuiltinRoles(ctx, store)
	if err != nil {
		t.Fatalf("初始化失败: %v", err)
	}
	if created != 1 {
		t.Errorf("新增 %d 个角色，期望 1", created)
	}
	if _, err := store.Role(ctx, RoleViewer); err != nil {
		t.Errorf("被删掉的内置角色应被补回，实际 %v", err)
	}
}

// 存储坏了必须报出来，而不是当成"角色缺失"去补写一遍。
// 混淆的后果是：一次读故障被翻译成一串覆盖写，把好数据也改掉。
func TestEnsureBuiltinRolesPropagatesStoreErrors(t *testing.T) {
	store := &unavailableStore{MemoryStore: newEmptyStore()}

	if _, err := EnsureBuiltinRoles(context.Background(), store); err == nil {
		t.Fatal("存储出错时应返回错误")
	}
}

// unavailableStore 是在可写存储上模拟"库连不上"：读角色一律失败。
//
// 不复用 engine_test.go 里的 failingStore——那个只实现只读的 Store，
// 而本用例要走的是"补写内置角色"这条写路径。
type unavailableStore struct {
	*MemoryStore
}

func (s *unavailableStore) Role(context.Context, string) (RoleDefinition, error) {
	return RoleDefinition{}, ErrStoreUnavailable
}
