package rbac

// SubjectType 是主体的类型。
type SubjectType string

const (
	// SubjectTypeUser 是人类用户。
	SubjectTypeUser SubjectType = "user"
	// SubjectTypeServiceAccount 是服务账号。
	SubjectTypeServiceAccount SubjectType = "service_account"
	// SubjectTypeMachine 是机器凭证，从属于某个用户或服务账号，CLI 使用。
	SubjectTypeMachine SubjectType = "machine"
)

// Subject 是发起操作的一方。本包只消费已确认的主体，不负责认证。
type Subject struct {
	ID           string
	Type         SubjectType
	DefaultScope Scope
}

// RoleDefinition 是角色的定义。内置角色由权限目录派生（见 catalog_gen.go）。
type RoleDefinition struct {
	ID          string
	DisplayName string
	// Builtin 为真时角色不可删除，且权限范围不可修改。
	Builtin bool
	// Permissions 是该角色直接持有的权限码。
	Permissions []PermissionCode
	// Inherits 是被继承的角色标识。继承是传递的，不允许循环。
	Inherits []string
	// MutuallyExclusiveWith 是静态互斥（SSD）的角色标识。
	MutuallyExclusiveWith []string
}

// RoleBinding 是"主体在某个作用域上持有某个角色"这一事实。
type RoleBinding struct {
	SubjectID string
	RoleID    string
	Scope     Scope
}
