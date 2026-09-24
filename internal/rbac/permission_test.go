package rbac

import "testing"

func TestMatches(t *testing.T) {
	tests := []struct {
		name      string
		held      PermissionCode
		requested PermissionCode
		want      bool
	}{
		{"精确匹配", PermissionRbacRoleRead, PermissionRbacRoleRead, true},
		{"不同动作不匹配", PermissionRbacRoleRead, PermissionRbacRoleWrite, false},
		{"不同资源不匹配", PermissionRbacRoleRead, PermissionRbacSubjectRead, false},
		{"全部权限匹配任意", PermissionAll, PermissionRbacSubjectAssign, true},
		{"末段通配覆盖同资源", "rbac.role.*", PermissionRbacRoleRead, true},
		{"末段通配不跨越资源", "rbac.role.*", PermissionRbacSubjectRead, false},
		{"领域通配覆盖整个领域", "rbac.*", PermissionRbacSubjectAssign, true},
		{"领域通配不跨越领域", "rbac.*", PermissionAuditLogRead, false},
		{"请求通配不因持有而匹配", PermissionRbacRoleRead, "rbac.role.*", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Matches(tt.held, tt.requested); got != tt.want {
				t.Errorf("Matches(%q, %q) = %v, want %v", tt.held, tt.requested, got, tt.want)
			}
		})
	}
}

func TestPermissionCodeValid(t *testing.T) {
	tests := []struct {
		name string
		code PermissionCode
		want bool
	}{
		{"三段式", "rbac.role.read", true},
		{"两段式", "rbac.read", true},
		{"带下划线分词", "audit.log.read", true},
		{"末段通配", "rbac.role.*", true},
		{"整体通配", "*", true},
		{"大写非法", "RBAC.role.read", false},
		{"空串非法", "", false},
		{"前导点非法", ".rbac.role", false},
		{"尾随点非法", "rbac.role.", false},
		{"连续点非法", "rbac..role", false},
		{"中段通配形状合法", "rbac.*.read", true}, // 通配位置约束由 Matches 保证，不在形状校验里
		{"连字符非法", "rbac.role-read", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.code.Valid(); got != tt.want {
				t.Errorf("PermissionCode(%q).Valid() = %v, want %v", tt.code, got, tt.want)
			}
		})
	}
}

func TestParsePermissionCode(t *testing.T) {
	if _, err := ParsePermissionCode("rbac.role.read"); err != nil {
		t.Errorf("合法权限码被拒绝: %v", err)
	}
	if _, err := ParsePermissionCode("Not A Code"); err == nil {
		t.Error("非法权限码未被拒绝")
	}
}
