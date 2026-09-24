package rbac

import (
	"errors"
	"fmt"
)

// 约束校验失败的原因。它们必须是可区分的：授权失败时用户需要知道
// 究竟违反了哪一条规则。
var (
	// ErrInheritanceCycle 表示角色继承成环。
	ErrInheritanceCycle = errors.New("角色继承成环")
	// ErrMutuallyExclusive 表示两个互斥角色被同时授予同一主体。
	ErrMutuallyExclusive = errors.New("角色互斥，不可同时授予")
	// ErrRoleInUse 表示角色仍被其他角色继承或仍被主体持有，不可删除。
	ErrRoleInUse = errors.New("角色仍被使用")
	// ErrBuiltinRoleImmutable 表示内置角色的权限范围不可修改。
	ErrBuiltinRoleImmutable = errors.New("内置角色的权限范围不可修改")
)

// ValidateInheritance 校验候选角色的继承链不成环，且被继承的角色都存在。
//
// 约束在**授权时**即校验，而不是等到使用时才拒绝——
// 不合法的角色组合不应该被存进系统（见 docs/design/rbac/role-model.md）。
func ValidateInheritance(roles map[string]RoleDefinition, candidate RoleDefinition) error {
	for _, parent := range candidate.Inherits {
		if _, ok := roles[parent]; !ok {
			return fmt.Errorf("%w: 被继承的角色 %q 不存在", ErrRoleNotFound, parent)
		}
	}

	// 从候选角色的每个父角色出发沿继承边走，若能回到候选角色自身即成环。
	// 起点是父角色而非候选角色本身——否则候选角色会被误判为自身的祖先。
	//
	// visited 是按起点独立重置的：不同分支共用一份 visited 会把
	// "这个角色已经查过"和"这个角色在本次路径上"混为一谈，从而漏判环。
	var walk func(roleID string, depth int, path map[string]bool) error
	walk = func(roleID string, depth int, path map[string]bool) error {
		if depth > maxInheritanceDepth {
			return fmt.Errorf("%w: 继承深度超过上限 %d", ErrInheritanceCycle, maxInheritanceDepth)
		}
		if roleID == candidate.ID {
			return fmt.Errorf("%w: %q 出现在自身的继承链上", ErrInheritanceCycle, candidate.ID)
		}
		if path[roleID] {
			return nil
		}
		path[roleID] = true
		def, ok := roles[roleID]
		if !ok {
			return fmt.Errorf("%w: 被继承的角色 %q 不存在", ErrRoleNotFound, roleID)
		}
		for _, parent := range def.Inherits {
			if err := walk(parent, depth+1, path); err != nil {
				return err
			}
		}
		return nil
	}

	for _, parent := range candidate.Inherits {
		if err := walk(parent, 2, map[string]bool{}); err != nil {
			return err
		}
	}
	return nil
}

// ValidateImmutable 校验对内置角色的修改没有触碰不可变部分。
//
// 内置角色的**存在**是模型的一部分；其权限绑定在部署时可调整，
// 但角色标识与内置标记不可更改。
func ValidateImmutable(existing RoleDefinition, candidate RoleDefinition) error {
	if !existing.Builtin {
		return nil
	}
	if existing.ID != candidate.ID || !candidate.Builtin {
		return fmt.Errorf("%w: 不能更改内置角色的标识或内置标记", ErrBuiltinRoleImmutable)
	}
	return nil
}

// ValidateAssignment 校验一次角色授予是否违反静态互斥（SSD）。
//
// 互斥是对称的：任一方声明了互斥即生效，不需要两边都写。
// 只在授予（grant）时校验；回收不可能引入新的互斥冲突。
func ValidateAssignment(roles map[string]RoleDefinition, existing []RoleBinding, candidate RoleBinding, scope Scope) error {
	if role, ok := roles[candidate.RoleID]; ok {
		for _, other := range existing {
			if other.Scope != scope {
				continue
			}
			peers := append([]string{other.RoleID}, roles[other.RoleID].MutuallyExclusiveWith...)
			for _, peer := range peers {
				if peer == role.ID {
					return fmt.Errorf("%w: %q 与 %q", ErrMutuallyExclusive, role.ID, other.RoleID)
				}
			}
			for _, declared := range role.MutuallyExclusiveWith {
				if declared == other.RoleID {
					return fmt.Errorf("%w: %q 与 %q", ErrMutuallyExclusive, role.ID, other.RoleID)
				}
			}
		}
	}
	return nil
}

// ValidateRoleDeletion 校验角色未被继承且未被持有。
func ValidateRoleDeletion(roles map[string]RoleDefinition, bindings []RoleBinding, roleID string) error {
	for _, r := range roles {
		for _, parent := range r.Inherits {
			if parent == roleID {
				return fmt.Errorf("%w: 仍被角色 %q 继承", ErrRoleInUse, r.ID)
			}
		}
	}
	for _, b := range bindings {
		if b.RoleID == roleID {
			return fmt.Errorf("%w: 仍被主体 %q 持有", ErrRoleInUse, b.SubjectID)
		}
	}
	return nil
}
