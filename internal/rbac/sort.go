package rbac

import "sort"

// 本文件是**列表顺序的唯一实现**。
//
// 顺序属于可观察行为：它决定接口返回什么、测试能断言什么、前端做差分时
// 看到什么。因此它不能由"存储返回的顺序"决定——内存实现返回 map 的遍历
// 顺序，数据库实现返回执行计划决定的顺序，两者都会变。
//
// 排序放在领域侧而不是各存储实现里：给存储实现各写一份，就是同一件事的
// 第二个实现（见 CLAUDE.md 的 SSOT 原则），而两份排序迟早会有一份忘记更新。

// sortPermissions 对权限码做稳定排序，使展开结果对同一输入始终一致。
//
// 展开结果的顺序会影响日志与测试断言，因此不依赖 map 的随机遍历顺序。
func sortPermissions(codes []PermissionCode) {
	sort.Slice(codes, func(i, j int) bool { return codes[i] < codes[j] })
}

// SortRoles 使角色列表按角色标识有序，便于测试断言与前端差分。
//
// 就地排序并返回同一个切片，便于在存储实现的返回处直接串联调用。
func SortRoles(roles []RoleDefinition) []RoleDefinition {
	sort.Slice(roles, func(i, j int) bool { return roles[i].ID < roles[j].ID })
	return roles
}

// SortBindings 使绑定列表按（主体, 角色, 作用域）有序。
//
// 一条规则覆盖两种查询：按主体反查时主体相同，退化为主体内部的（角色,
// 作用域）有序；按角色反查时角色相同，退化为主体有序。不为每种查询各定
// 一套顺序，是因为它们的差别只体现在调用方拿到的排列上，而调用方要的是
// **稳定**，不是某一种特定排列。
func SortBindings(bindings []RoleBinding) []RoleBinding {
	sort.Slice(bindings, func(i, j int) bool {
		a, b := bindings[i], bindings[j]
		if a.SubjectID != b.SubjectID {
			return a.SubjectID < b.SubjectID
		}
		if a.RoleID != b.RoleID {
			return a.RoleID < b.RoleID
		}
		return a.Scope < b.Scope
	})
	return bindings
}
