package rbac

import "strings"

// Scope 是权限生效的范围，形如 "tenant/acme/project/web"。
//
// 空字符串表示全局作用域（根），只有系统管理员角色持有。
type Scope string

// GlobalScope 是唯一能覆盖所有其他作用域的值。
const GlobalScope Scope = ""

// Contains 报告 s 是否包含 other，即 other 是否处于 s 的作用域之内。
//
// 规则对应 docs/design/rbac/role-model.md：
// 父作用域天然包含子作用域，且包含自身。全局作用域包含一切。
func (s Scope) Contains(other Scope) bool {
	if s == GlobalScope {
		return true
	}
	if s == other {
		return true
	}
	return strings.HasPrefix(string(other), string(s)+"/")
}

// Segments 返回作用域的层级路径。
func (s Scope) Segments() []string {
	if s == GlobalScope {
		return nil
	}
	return strings.Split(string(s), "/")
}

// String 实现 fmt.Stringer。全局作用域显示为根标记，避免日志里出现空串。
func (s Scope) String() string {
	if s == GlobalScope {
		return "<global>"
	}
	return string(s)
}
