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

// ScopeDefinition 是一个**已登记**的范围（见 docs/design/rbac/scopes.md）。
//
// 路径是标识，一经登记不可更改——与 RoleDefinition.ID 同构；"改路径"不是改名，
// 而是"删掉旧范围 + 新建一个"。显示名只用于展示，**不参与判定、不参与匹配**，
// 因此两个范围可以重名。
//
// 全局（根）不是一条登记记录：它永远可用、不可创建也不可删除，因而不出现在这里。
type ScopeDefinition struct {
	Path        string
	DisplayName string
}

// RoleBinding 是"主体在某个作用域上持有某个角色"这一事实。
type RoleBinding struct {
	SubjectID string
	RoleID    string
	Scope     Scope
}
