package gormstore

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"go.uber.org/zap"

	"github.com/poetlife/aladdin/internal/config"
	"github.com/poetlife/aladdin/internal/database"
	"github.com/poetlife/aladdin/internal/rbac"
)

// 两个存储实现共用同一套用例。
//
// 这是本仓库里"同一件事只有一个实现"之外的另一半：**同一份契约只能有
// 一套用例**。给 gorm 实现单独写一套，两套就会各自漂移，而漂移的表现
// 形式是"换后端之后行为变了"——最难在事后归因的一类问题。
//
// 用例放在 gormstore 包内而不是 internal/rbac：后者若导入 gormstore
// 会形成导入环（gormstore 依赖 rbac）。

// storeCase 是一个存储实现的构造方式。
type storeCase struct {
	name string
	open func(t *testing.T) rbac.MutableStore
}

func storeCases(t *testing.T) []storeCase {
	t.Helper()
	return []storeCase{
		{
			name: "内存实现",
			open: func(*testing.T) rbac.MutableStore { return rbac.NewMemoryStore() },
		},
		{
			// 走 Open 而不是 New：它同时覆盖"迁移 + 补内置角色"这两步，
			// 而内存实现的构造里也带着内置角色，两边起点才算一致。
			name: "关系库实现",
			open: func(t *testing.T) rbac.MutableStore { return newTestStore(t) },
		},
	}
}

// forEachStore 把同一段用例跑在两个实现上。
func forEachStore(t *testing.T, run func(t *testing.T, store rbac.MutableStore)) {
	t.Helper()
	for _, sc := range storeCases(t) {
		t.Run(sc.name, func(t *testing.T) {
			run(t, sc.open(t))
		})
	}
}

// 找不到角色与"库用不了"必须可区分：前者是正常结论，后者必须快速失败。
func TestContractMissingRole(t *testing.T) {
	forEachStore(t, func(t *testing.T, store rbac.MutableStore) {
		_, err := store.Role(context.Background(), "no.such.role")
		if !errors.Is(err, rbac.ErrRoleNotFound) {
			t.Errorf("err = %v，期望 ErrRoleNotFound", err)
		}
	})
}

// 未登记的主体与"登记了但没有角色"是两回事：
// 前者是 ErrSubjectNotFound，后者是一个空列表。
func TestContractSubjectDistinction(t *testing.T) {
	forEachStore(t, func(t *testing.T, store rbac.MutableStore) {
		ctx := context.Background()

		if _, err := store.SubjectBindings(ctx, "nobody"); !errors.Is(err, rbac.ErrSubjectNotFound) {
			t.Errorf("未登记主体 err = %v，期望 ErrSubjectNotFound", err)
		}

		if err := store.PutSubject(ctx, rbac.Subject{
			ID: "u1", Type: rbac.SubjectTypeUser, DefaultScope: "tenant/acme",
		}); err != nil {
			t.Fatalf("登记主体失败: %v", err)
		}
		got, err := store.SubjectBindings(ctx, "u1")
		if err != nil {
			t.Fatalf("已登记主体不应报错: %v", err)
		}
		if len(got) != 0 {
			t.Errorf("尚无绑定时应返回空列表，得到 %v", got)
		}
	})
}

// 重复授予同一角色不产生第二条记录。库侧靠复合主键保证，内存侧靠写入前
// 比对——两者都必须成立，因为并发下的"先查再写"不成立。
func TestContractBindIsIdempotent(t *testing.T) {
	forEachStore(t, func(t *testing.T, store rbac.MutableStore) {
		ctx := context.Background()
		if err := store.PutSubject(ctx, rbac.Subject{ID: "u1"}); err != nil {
			t.Fatalf("登记主体失败: %v", err)
		}
		binding := rbac.RoleBinding{SubjectID: "u1", RoleID: rbac.RoleViewer, Scope: "tenant/acme"}

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
			t.Errorf("绑定数 = %d，期望 1", len(got))
		}
	})
}

// 作用域是绑定的一部分：同一角色在不同作用域上各算一条。
func TestContractScopeIsPartOfBinding(t *testing.T) {
	forEachStore(t, func(t *testing.T, store rbac.MutableStore) {
		ctx := context.Background()
		if err := store.PutSubject(ctx, rbac.Subject{ID: "u1"}); err != nil {
			t.Fatalf("登记主体失败: %v", err)
		}
		for _, scope := range []rbac.Scope{"tenant/acme", "tenant/acme/project/web", rbac.GlobalScope} {
			if err := store.Bind(ctx, rbac.RoleBinding{
				SubjectID: "u1", RoleID: rbac.RoleViewer, Scope: scope,
			}); err != nil {
				t.Fatalf("绑定失败: %v", err)
			}
		}

		got, err := store.SubjectBindings(ctx, "u1")
		if err != nil {
			t.Fatalf("读取绑定失败: %v", err)
		}
		if len(got) != 3 {
			t.Errorf("绑定数 = %d，期望 3——作用域不同的绑定是不同的事实", len(got))
		}
	})
}

// 撤销是幂等的：重复撤销与撤销一个本来就没有的绑定，结果一致。
func TestContractUnbindIsIdempotent(t *testing.T) {
	forEachStore(t, func(t *testing.T, store rbac.MutableStore) {
		ctx := context.Background()
		if err := store.PutSubject(ctx, rbac.Subject{ID: "u1"}); err != nil {
			t.Fatalf("登记主体失败: %v", err)
		}
		binding := rbac.RoleBinding{SubjectID: "u1", RoleID: rbac.RoleViewer, Scope: "tenant/acme"}
		if err := store.Bind(ctx, binding); err != nil {
			t.Fatalf("绑定失败: %v", err)
		}

		for range 2 {
			if err := store.Unbind(ctx, binding); err != nil {
				t.Fatalf("撤销失败: %v", err)
			}
		}
		got, err := store.SubjectBindings(ctx, "u1")
		if err != nil {
			t.Fatalf("读取绑定失败: %v", err)
		}
		if len(got) != 0 {
			t.Errorf("撤销后仍剩 %v", got)
		}
	})
}

// 撤销只影响指定的那一条，不误伤同主体或同角色的其他绑定。
func TestContractUnbindIsNarrow(t *testing.T) {
	forEachStore(t, func(t *testing.T, store rbac.MutableStore) {
		ctx := context.Background()
		if err := store.PutSubject(ctx, rbac.Subject{ID: "u1"}); err != nil {
			t.Fatalf("登记主体失败: %v", err)
		}
		target := rbac.RoleBinding{SubjectID: "u1", RoleID: rbac.RoleViewer, Scope: "tenant/acme"}
		others := []rbac.RoleBinding{
			{SubjectID: "u1", RoleID: rbac.RoleViewer, Scope: "tenant/other"},
			{SubjectID: "u1", RoleID: rbac.RoleAuditor, Scope: "tenant/acme"},
		}
		for _, b := range append([]rbac.RoleBinding{target}, others...) {
			if err := store.Bind(ctx, b); err != nil {
				t.Fatalf("绑定失败: %v", err)
			}
		}

		if err := store.Unbind(ctx, target); err != nil {
			t.Fatalf("撤销失败: %v", err)
		}
		got, err := store.SubjectBindings(ctx, "u1")
		if err != nil {
			t.Fatalf("读取绑定失败: %v", err)
		}
		if !reflect.DeepEqual(got, rbac.SortBindings(others)) {
			t.Errorf("撤销后剩余 = %v，期望 %v", got, rbac.SortBindings(others))
		}
	})
}

// 按角色反查只返回该角色的绑定——删除角色前的占用校验靠它。
func TestContractBindingsOfRole(t *testing.T) {
	forEachStore(t, func(t *testing.T, store rbac.MutableStore) {
		ctx := context.Background()
		for _, id := range []string{"u1", "u2"} {
			if err := store.PutSubject(ctx, rbac.Subject{ID: id}); err != nil {
				t.Fatalf("登记主体失败: %v", err)
			}
		}
		want := rbac.RoleBinding{SubjectID: "u2", RoleID: rbac.RoleAuditor, Scope: "tenant/acme"}
		for _, b := range []rbac.RoleBinding{
			{SubjectID: "u1", RoleID: rbac.RoleViewer, Scope: "tenant/acme"},
			want,
			{SubjectID: "u1", RoleID: rbac.RoleAuditor, Scope: "tenant/other"},
		} {
			if err := store.Bind(ctx, b); err != nil {
				t.Fatalf("绑定失败: %v", err)
			}
		}

		got, err := store.BindingsOfRole(ctx, rbac.RoleViewer)
		if err != nil {
			t.Fatalf("按角色反查失败: %v", err)
		}
		if len(got) != 1 || got[0].RoleID != rbac.RoleViewer {
			t.Errorf("反查结果 = %v，期望只含 %s 的绑定", got, rbac.RoleViewer)
		}

		empty, err := store.BindingsOfRole(ctx, "no.such.role")
		if err != nil {
			t.Fatalf("反查不存在的角色不应报错: %v", err)
		}
		if len(empty) != 0 {
			t.Errorf("不存在的角色应返回空列表，得到 %v", empty)
		}
	})
}

// 角色定义必须整份原样往返：多一个字段被丢掉，判定就会少一条权限，
// 而现象是"配了却没生效"。
func TestContractRoleRoundTrip(t *testing.T) {
	forEachStore(t, func(t *testing.T, store rbac.MutableStore) {
		ctx := context.Background()
		role := rbac.RoleDefinition{
			ID:                    "custom.role",
			DisplayName:           "自定义角色",
			Permissions:           []rbac.PermissionCode{rbac.PermissionAuditLogRead, rbac.PermissionRbacRoleRead},
			Inherits:              []string{rbac.RoleViewer},
			MutuallyExclusiveWith: []string{rbac.RoleAuditor},
		}

		if err := store.PutRole(ctx, role); err != nil {
			t.Fatalf("写入角色失败: %v", err)
		}
		got, err := store.Role(ctx, role.ID)
		if err != nil {
			t.Fatalf("读取角色失败: %v", err)
		}
		if !reflect.DeepEqual(got, role) {
			t.Errorf("往返后不一致：\n实际 %+v\n期望 %+v", got, role)
		}

		// 覆盖写：同一标识再写一次，取到的是后写的那份。
		updated := role
		updated.DisplayName = "改过的名字"
		updated.Permissions = nil
		if err := store.PutRole(ctx, updated); err != nil {
			t.Fatalf("覆盖写入失败: %v", err)
		}
		got, err = store.Role(ctx, role.ID)
		if err != nil {
			t.Fatalf("读取角色失败: %v", err)
		}
		if !reflect.DeepEqual(got, updated) {
			t.Errorf("覆盖写后不一致：\n实际 %+v\n期望 %+v", got, updated)
		}
	})
}

// 列表顺序由 rbac.SortRoles / SortBindings 定义，两个实现必须给出同一个次序。
// 这条断言是"顺序只有一处实现"的守门人。
func TestContractOrderingIsShared(t *testing.T) {
	ctx := context.Background()
	var snapshots [][]rbac.RoleDefinition

	for _, sc := range storeCases(t) {
		store := sc.open(t)
		// 按一个与排序规则无关的次序写入，确保结果不是"恰好按写入顺序返回"。
		for _, id := range []string{"zeta", "alpha", "mid"} {
			if err := store.PutRole(ctx, rbac.RoleDefinition{ID: id, DisplayName: id}); err != nil {
				t.Fatalf("写入角色失败: %v", err)
			}
		}
		roles, err := store.Roles(ctx)
		if err != nil {
			t.Fatalf("读取角色失败: %v", err)
		}
		snapshots = append(snapshots, roles)
	}

	if !reflect.DeepEqual(snapshots[0], snapshots[1]) {
		t.Fatalf("两个实现的角色次序不同：\n%s = %v\n%s = %v",
			storeCases(t)[0].name, snapshots[0], storeCases(t)[1].name, snapshots[1])
	}
	// 顺带确认它确实是排过序的，而不是两边都碰巧返回了写入顺序。
	for i := 1; i < len(snapshots[0]); i++ {
		if snapshots[0][i-1].ID >= snapshots[0][i].ID {
			t.Fatalf("角色列表未按标识排序: %v", snapshots[0])
		}
	}
}

// 删除不存在的角色必须报错：静默成功会把"拼错了角色标识"藏起来。
func TestContractDeleteMissingRole(t *testing.T) {
	forEachStore(t, func(t *testing.T, store rbac.MutableStore) {
		ctx := context.Background()
		if err := store.DeleteRole(ctx, "no.such.role"); !errors.Is(err, rbac.ErrRoleNotFound) {
			t.Errorf("err = %v，期望 ErrRoleNotFound", err)
		}

		if err := store.PutRole(ctx, rbac.RoleDefinition{ID: "custom.role"}); err != nil {
			t.Fatalf("写入角色失败: %v", err)
		}
		if err := store.DeleteRole(ctx, "custom.role"); err != nil {
			t.Fatalf("删除角色失败: %v", err)
		}
		if _, err := store.Role(ctx, "custom.role"); !errors.Is(err, rbac.ErrRoleNotFound) {
			t.Errorf("删除后仍能读到角色，err = %v", err)
		}
	})
}

// newTestStore 打开一个临时目录里的库，走的是服务端启动时的同一条路径。
func newTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(context.Background(), config.DatabaseConfig{
		Driver: string(database.DialectSQLite),
		DSN:    filepath.Join(t.TempDir(), "gormstore.db"),
	}, zap.NewNop())
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}
