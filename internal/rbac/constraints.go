package rbac

import (
	"context"
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
	// ErrBuiltinRoleUndeletable 表示内置角色不可删除。
	ErrBuiltinRoleUndeletable = errors.New("内置角色不可删除")
	// ErrBuiltinRoleImmutable 表示内置角色的权限范围不可修改。
	ErrBuiltinRoleImmutable = errors.New("内置角色的权限范围不可修改")
	// ErrScopeInUse 表示范围仍被角色绑定引用（含其后代上的绑定），不可删除。
	ErrScopeInUse = errors.New("范围仍被使用")
	// ErrRegistrationRoleUnfit 表示这个角色不能当"默认注册角色"：它持有（或经
	// 继承、通配而覆盖）一个能授予角色或改写注册策略的权限码。
	ErrRegistrationRoleUnfit = errors.New("该角色不能作为默认注册角色")
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
//
// 已持有同一角色**不是**互斥：那是同一条事实的重复授予，存储侧本就按幂等
// 处理（见 MutableStore.Bind）。把它算成互斥的错误在于，互斥说的是"两个
// 角色不能同时持有"，而这里是同一个角色——按前者报错会让"再点一次保存"
// 变成一个失败，而调用方没有任何办法区分它与真正的互斥冲突。
func ValidateAssignment(roles map[string]RoleDefinition, existing []RoleBinding, candidate RoleBinding, scope Scope) error {
	if role, ok := roles[candidate.RoleID]; ok {
		for _, other := range existing {
			if other.Scope != scope {
				continue
			}
			if other.RoleID == candidate.RoleID {
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

// ValidateRoleDeletion 校验角色可以删除：内置角色不可删除，且角色未被继承、
// 未被任何主体持有。
//
// 它是"这个角色能不能删"的**唯一入口**。存储实现的 DeleteRole 只执行不判定
// （见 MutableStore），因此调用方必须先过这里——否则内置角色会随后端不同而
// 有不同的命运。
func ValidateRoleDeletion(roles map[string]RoleDefinition, bindings []RoleBinding, roleID string) error {
	if role, ok := roles[roleID]; ok && role.Builtin {
		return fmt.Errorf("%w: %q", ErrBuiltinRoleUndeletable, roleID)
	}
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

// ValidateScopeDeletion 校验范围可以删除：范围内（**含其后代**）没有被任何主体持有。
//
// 它是"这个范围能不能删"的**唯一入口**。存储实现的 DeleteScope 只执行不判定
// （见 MutableStore），因此调用方必须先过这里。
//
// 判据与删除角色同构：引用还在，就不允许把被引用的东西抽走。绑定落在后代上也算
// 被引用——层级由路径前缀表达，`tenant/acme` 之内本来就包含 `tenant/acme/project`。
//
// 传入的绑定由调用方先按范围取（BindingsUnderScope），这里只做判断，不再查一次。
func ValidateScopeDeletion(path string, bindings []RoleBinding) error {
	if len(bindings) == 0 {
		return nil
	}
	// 报出条数与一个例子：管理员据此知道去哪儿把引用摘掉（见
	// docs/design/rbac/scopes.md 的"删除要干净"）。
	example := bindings[0]
	return fmt.Errorf("%w: %q 仍被 %d 条绑定引用（例如主体 %q 的角色 %q 在 %q 上）",
		ErrScopeInUse, path, len(bindings), example.SubjectID, example.RoleID, string(example.Scope))
}

// registrationUnfitPermissions 是"默认注册角色"不得覆盖的权限码。
//
// 它们各自对应一条路径：授予角色、发布策略、改写注册策略本身。任何一个落进
// 默认角色，都会让"开放注册"或"发一个邀请码"变成一次无痕提权——而这两件事
// 的输入分别是"打开一个开关"与"把一串字符给出去"，都不是管理员在授权界面前
// 做的动作。`*` 无需单列：它覆盖全部权限，因此会被 covers 一致地判为不合格。
var registrationUnfitPermissions = []PermissionCode{
	PermissionRbacSubjectAssign,
	PermissionRbacPolicyPublish,
	PermissionIdentityRegistrationWrite,
}

// ValidateRegistrationDefaultRole 校验一个角色能不能当"默认注册角色"。
//
// 它是这条判断的**唯一入口**，与互斥约束同处：同属"授予前的校验"，而不是运行时
// 护栏（见 docs/design/rbac/role-model.md 的约束一节）。理由见
// docs/design/identity/registration.md。
//
// 判据是角色**展开之后**的权限集合，不是它直接声明的那几条：一个继承了系统管理
// 员的角色同样能自我放大，只比较直接声明会把这条缝留着。展开复用唯一的继承实现
// （expand），不另写一份。
//
// 空标识表示"不给默认角色"，那是合法取值，直接通过。
func ValidateRegistrationDefaultRole(ctx context.Context, store Store, roleID string) error {
	if roleID == "" {
		return nil
	}
	_, permissions, err := expand(ctx, store, []string{roleID})
	if err != nil {
		return err
	}
	for _, unfit := range registrationUnfitPermissions {
		if covers(permissions, unfit) {
			return fmt.Errorf("%w: %q 持有 %s（或一个覆盖它的通配），它能让新来的人自己提权",
				ErrRegistrationRoleUnfit, roleID, unfit)
		}
	}
	return nil
}
