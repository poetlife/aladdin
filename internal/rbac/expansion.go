package rbac

import (
	"context"
	"fmt"
	"slices"
)

// maxInheritanceDepth 是角色继承链的深度上限。
//
// 它同时承担两个职责：限制判定代价随继承深度增长，以及在数据出现环时
// 快速失败而不是无限递归。环本身应被授权时的校验拒绝（见
// docs/design/rbac/role-model.md），这里是纵深防御。
const maxInheritanceDepth = 32

// expand 把一组角色标识展开为最终生效的角色集合与权限码集合。
//
// 展开过程即"继承是传递的"这一行为的实现：从每个起点角色出发做深度优先
// 遍历，收集沿途的全部权限码。返回值中的角色标识是去重后的闭包。
//
// 本函数是角色继承展开的唯一实现（见 docs/ssot-registry.md）。
func expand(ctx context.Context, store Store, rootRoleIDs []string) ([]string, []PermissionCode, error) {
	visited := make(map[string]bool, len(rootRoleIDs))
	permissions := map[PermissionCode]bool{}
	ordered := make([]string, 0, len(rootRoleIDs))

	var walk func(roleID string, depth int) error
	walk = func(roleID string, depth int) error {
		if depth > maxInheritanceDepth {
			return fmt.Errorf("角色继承深度超过上限 %d（起点 %q）：可能存在环", maxInheritanceDepth, roleID)
		}
		if visited[roleID] {
			return nil
		}
		visited[roleID] = true
		ordered = append(ordered, roleID)

		def, err := store.Role(ctx, roleID)
		if err != nil {
			return fmt.Errorf("读取角色 %q: %w", roleID, err)
		}
		for _, p := range def.Permissions {
			permissions[p] = true
		}
		for _, parent := range def.Inherits {
			if err := walk(parent, depth+1); err != nil {
				return err
			}
		}
		return nil
	}

	for _, id := range rootRoleIDs {
		if err := walk(id, 1); err != nil {
			return nil, nil, err
		}
	}

	held := make([]PermissionCode, 0, len(permissions))
	for p := range permissions {
		held = append(held, p)
	}
	sortPermissions(held)

	// 先按目录顺序输出**已展开**的具体权限码：持有项里的通配（"*"、"rbac.role.*"）
	// 在这里落成它覆盖的每一条。
	//
	// 展开不是为了让判定好写——判定有 Matches。它是为了让这份集合能**原样下发**
	// 给前端做展示裁剪，而前端只做集合成员判断、不解释通配
	// （见 docs/design/rbac/frontend-permissions.md）。不展开的话，系统管理员
	// 到手的是一串孤零零的 "*"，界面上任何一个入口都进不去。
	out := make([]PermissionCode, 0, len(AllPermissionCodes)+len(held))
	for _, p := range AllPermissionCodes {
		if covers(held, p) {
			out = append(out, p)
		}
	}

	// 目录之外的持有项原样保留，排在具体权限码之后。
	//
	// 一是存储里可能有目录尚未登记的权限码（脏数据），判定仍要能匹配到它；
	// 二是通配自身也在其中，保留它使"目录之外的权限码"对持有 "*" 的主体继续成立。
	// 输出因此是原持有集合的**超集**——只增不减，判定的结论一字不变。
	rest := make([]PermissionCode, 0, len(held))
	for _, p := range held {
		if !slices.Contains(AllPermissionCodes, p) {
			rest = append(rest, p)
		}
	}
	return ordered, append(out, rest...), nil
}

// covers 报告持有集合中是否存在覆盖 requested 的权限码。
//
// 复用 Matches 而不是另写一遍前缀比较：通配匹配只有一处实现
// （见 docs/ssot-registry.md）。
func covers(held []PermissionCode, requested PermissionCode) bool {
	for _, h := range held {
		if Matches(h, requested) {
			return true
		}
	}
	return false
}
