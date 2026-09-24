package gormstore

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"go.uber.org/zap"

	"github.com/poetlife/aladdin/internal/config"
	"github.com/poetlife/aladdin/internal/database"
	"github.com/poetlife/aladdin/internal/rbac"
)

// sqliteConfig 指向一个临时目录里的库文件。
func sqliteConfig(t *testing.T, name string) config.DatabaseConfig {
	t.Helper()
	return config.DatabaseConfig{
		Driver: string(database.DialectSQLite),
		DSN:    filepath.Join(t.TempDir(), name),
	}
}

// Open 必须在返回可用的存储之前把内置角色补齐：判定路径拿不到角色，
// 就等于对所有人拒绝，而现象看起来像一次权限配置错误。
func TestOpenSeedsBuiltinRoles(t *testing.T) {
	store, err := Open(context.Background(), sqliteConfig(t, "seed.db"), zap.NewNop())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	for _, builtin := range rbac.BuiltinRoles {
		if _, err := store.Role(context.Background(), builtin.ID); err != nil {
			t.Errorf("内置角色 %q 未落库: %v", builtin.ID, err)
		}
	}
}

// 持久化的直接判据：进程退出再进来，数据还在。
//
// 这条用例是整次改动的验收点——在此之前所有数据都活在内存里，
// "重启即清空"是设计的一部分。
func TestDataSurvivesReopen(t *testing.T) {
	ctx := context.Background()
	cfg := sqliteConfig(t, "persist.db")

	role := rbac.RoleDefinition{
		ID:          "custom.role",
		DisplayName: "自定义角色",
		Permissions: []rbac.PermissionCode{rbac.PermissionAuditLogRead},
	}
	binding := rbac.RoleBinding{SubjectID: "u1", RoleID: "custom.role", Scope: "tenant/acme"}

	first, err := Open(ctx, cfg, zap.NewNop())
	if err != nil {
		t.Fatalf("首次 Open: %v", err)
	}
	if err := first.PutRole(ctx, role); err != nil {
		t.Fatalf("写入角色失败: %v", err)
	}
	if err := first.PutSubject(ctx, rbac.Subject{
		ID: "u1", Type: rbac.SubjectTypeUser, DefaultScope: "tenant/acme",
	}); err != nil {
		t.Fatalf("写入主体失败: %v", err)
	}
	if err := first.Bind(ctx, binding); err != nil {
		t.Fatalf("写入绑定失败: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("关闭失败: %v", err)
	}

	// 第二次 Open 走完整的"连接 → 迁移 → 补内置角色"路径：已有的库上
	// 这三步都必须是无副作用的，否则第二次启动就会改变数据。
	second, err := Open(ctx, cfg, zap.NewNop())
	if err != nil {
		t.Fatalf("二次 Open: %v", err)
	}
	t.Cleanup(func() { _ = second.Close() })

	got, err := second.Role(ctx, role.ID)
	if err != nil {
		t.Fatalf("重开后读不到角色: %v", err)
	}
	if got.DisplayName != role.DisplayName || len(got.Permissions) != len(role.Permissions) {
		t.Errorf("重开后角色 = %+v，期望 %+v", got, role)
	}

	bindings, err := second.SubjectBindings(ctx, "u1")
	if err != nil {
		t.Fatalf("重开后读不到绑定: %v", err)
	}
	if len(bindings) != 1 || bindings[0] != binding {
		t.Errorf("重开后绑定 = %v，期望 [%v]", bindings, binding)
	}
}

// 配置不可用时必须拒绝启动，而不是带着一个空库继续跑。
func TestOpenRejectsUnusableConfig(t *testing.T) {
	cases := []struct {
		name string
		cfg  config.DatabaseConfig
	}{
		{"未登记的后端", config.DatabaseConfig{Driver: "postgres", DSN: "aladdin.db"}},
		{"后端为空", config.DatabaseConfig{Driver: "", DSN: "aladdin.db"}},
		{"连接串为空", config.DatabaseConfig{Driver: "sqlite", DSN: ""}},
		{"目录不存在", config.DatabaseConfig{
			Driver: "sqlite",
			DSN:    filepath.Join(t.TempDir(), "not-there", "aladdin.db"),
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, err := Open(context.Background(), tc.cfg, zap.NewNop())
			if err == nil {
				_ = store.Close()
				t.Fatal("期望拒绝启动")
			}
		})
	}
}

// "库用不了"与"没有这条数据"必须分开翻译。
//
// 混为一谈的后果是：一次数据库故障表现成一次权限问题，运维看到的是
// "用户突然没权限了"，于是去改权限配置——而真正的原因在别处。
func TestStoreFailureIsNotMistakenForMissingData(t *testing.T) {
	store := newTestStore(t)
	if err := store.Close(); err != nil {
		t.Fatalf("关闭连接池失败: %v", err)
	}
	ctx := context.Background()

	t.Run("读角色", func(t *testing.T) {
		_, err := store.Role(ctx, rbac.RoleSystemAdmin)
		assertUnavailable(t, err)
	})

	t.Run("读角色列表", func(t *testing.T) {
		if _, err := store.Roles(ctx); !errors.Is(err, rbac.ErrStoreUnavailable) {
			t.Errorf("err = %v，期望 ErrStoreUnavailable", err)
		}
	})

	t.Run("读主体绑定", func(t *testing.T) {
		_, err := store.SubjectBindings(ctx, "u1")
		assertUnavailable(t, err)
	})

	t.Run("写角色", func(t *testing.T) {
		err := store.PutRole(ctx, rbac.RoleDefinition{ID: "custom.role"})
		if !errors.Is(err, rbac.ErrStoreUnavailable) {
			t.Errorf("err = %v，期望 ErrStoreUnavailable", err)
		}
	})

	t.Run("写绑定", func(t *testing.T) {
		err := store.Bind(ctx, rbac.RoleBinding{SubjectID: "u1", RoleID: rbac.RoleViewer})
		if !errors.Is(err, rbac.ErrStoreUnavailable) {
			t.Errorf("err = %v，期望 ErrStoreUnavailable", err)
		}
	})
}

func assertUnavailable(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, rbac.ErrStoreUnavailable) {
		t.Errorf("err = %v，期望包装 ErrStoreUnavailable", err)
	}
	// 同时确认它没有被误报成"不存在"：上层按后者会回一个 404，
	// 而正确的回应是"服务不可用，请重试"。
	if errors.Is(err, rbac.ErrRoleNotFound) || errors.Is(err, rbac.ErrSubjectNotFound) {
		t.Errorf("存储故障被误报成了数据不存在: %v", err)
	}
}
