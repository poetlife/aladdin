package rbac

import "strings"

// Scope 是权限生效的范围，形如 "tenant/acme/project/web"。
//
// 空字符串表示全局作用域（根），只有系统管理员角色持有。
type Scope string

// GlobalScope 是唯一能覆盖所有其他作用域的值。
const GlobalScope Scope = ""

// GlobalScopeLiteral 是全局作用域在**文本形式**下的写法。
//
// 全局作用域的内部值是空串，而空串在配置文件里表示"这一项没写"。两者不能
// 共用一种写法：一次手滑漏填就会静默变成一次全局授权，而那种错误在启动时
// 不会说任何话。所以"我确实要全局"必须有一个显式的字面量。
//
// 取值与 String() 的输出一致，前端也用同一个字面量渲染（见 web/src/pages/HomePage.tsx）。
const GlobalScopeLiteral = "<global>"

// ParseScope 把文本形式的作用域转成内部值。
//
// 只认 GlobalScopeLiteral 这一个特殊写法，其余**原样使用**：作用域名是自由
// 文本，规范化（去空白、去首尾斜杠……）会让"我改的这一行到底生效没有"变成
// 一个需要推理的问题，而它本该是一眼可答的。
//
// 空串会得到全局作用域——这是"空即全局"的直接结果。配置层之所以另设一个
// 哨兵，是因为在**那里**空串表示"这一项没写"，两件事不能共用一种写法。
// 换句话说：空与哨兵都指向全局，但配置层只接受后者作为"我想要全局"的表示。
func ParseScope(s string) Scope {
	if s == GlobalScopeLiteral {
		return GlobalScope
	}
	return Scope(s)
}

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
		return GlobalScopeLiteral
	}
	return string(s)
}
