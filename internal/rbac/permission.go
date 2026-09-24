package rbac

import (
	"fmt"
	"strings"
)

// PermissionCode 是权限码，形如 "rbac.role.read"。
//
// 取值必须来自 api/permissions/catalog.yaml 派生出的常量（见 catalog_gen.go），
// 代码中不得出现权限码字符串字面量。唯一例外是本包内部与权限目录的比对。
type PermissionCode string

// IsWildcard 报告该权限码是否为通配写法。
func (p PermissionCode) IsWildcard() bool {
	return p == PermissionAll || strings.HasSuffix(string(p), ".*")
}

// Valid 报告权限码形状是否合法：全小写、点分、下划线分词；通配只能在末段或独占。
func (p PermissionCode) Valid() bool {
	s := string(p)
	if s == "" {
		return false
	}
	for _, seg := range strings.Split(s, ".") {
		if seg == "*" {
			continue
		}
		if seg == "" {
			return false
		}
		for i, r := range seg {
			switch {
			case r >= 'a' && r <= 'z':
			case r == '_':
			case r >= '0' && r <= '9' && i > 0:
			default:
				return false
			}
		}
	}
	return true
}

// Matches 报告持有权限 held 是否覆盖请求权限 requested。
//
// 以下是本仓库唯一的权限码匹配实现（见 docs/ssot-registry.md）。
// 匹配规则对应 docs/design/rbac/role-model.md 的通配约定：
// 通配只能出现在末段或独占整个权限码，因此这里不做任意位置的前缀匹配。
func Matches(held, requested PermissionCode) bool {
	if held == PermissionAll {
		return true
	}
	if held == requested {
		return true
	}
	prefix, ok := strings.CutSuffix(string(held), ".*")
	if !ok {
		return false
	}
	// 末段通配：held 的父路径必须是 requested 的前缀。
	// 例如 "rbac.role.*" 覆盖 "rbac.role.read"，但不覆盖 "rbac.subject.read"。
	return strings.HasPrefix(string(requested), prefix+".")
}

// String 实现 fmt.Stringer，便于日志输出。
func (p PermissionCode) String() string { return string(p) }

// ParsePermissionCode 把外部输入的字符串解析为权限码，并校验形状。
// 用于权限目录的加载与测试；业务代码不应使用它替代常量。
func ParsePermissionCode(s string) (PermissionCode, error) {
	p := PermissionCode(s)
	if !p.Valid() {
		return "", fmt.Errorf("非法的权限码 %q：应形如 domain.resource.action", s)
	}
	return p, nil
}
