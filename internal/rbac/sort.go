package rbac

import "sort"

// sortPermissions 对权限码做稳定排序，使展开结果对同一输入始终一致。
//
// 展开结果的顺序会影响日志与测试断言，因此不依赖 map 的随机遍历顺序。
func sortPermissions(codes []PermissionCode) {
	sort.Slice(codes, func(i, j int) bool { return codes[i] < codes[j] })
}
