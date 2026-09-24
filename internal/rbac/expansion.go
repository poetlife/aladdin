package rbac

import (
	"context"
	"fmt"
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

	out := make([]PermissionCode, 0, len(permissions))
	for _, p := range AllPermissionCodes {
		if permissions[p] {
			out = append(out, p)
			delete(permissions, p)
		}
	}
	// 目录之外的权限码（例如存储中被手工写入的脏数据）仍应参与判定，
	// 但排在目录顺序之后，保证输出对同一输入稳定可比较。
	rest := make([]PermissionCode, 0, len(permissions))
	for p := range permissions {
		rest = append(rest, p)
	}
	sortPermissions(rest)
	return ordered, append(out, rest...), nil
}
