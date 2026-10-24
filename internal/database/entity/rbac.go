package entity

// RoleRecord 是角色定义在库里的一行。
type RoleRecord struct {
	// ID 是角色标识，主键。发布后不可更改。
	ID string `gorm:"primaryKey;size:191"`
	// DisplayName 是显示名，可改。
	DisplayName string
	// Builtin 为真时该角色由权限目录派生，不可删除。
	Builtin bool
	// Permissions 是该角色直接持有的权限码。
	//
	// 存成 JSON 而不是拆一张角色-权限关联表：角色定义从来是**整份读写**的，
	// 判定需要的是完整的定义，没有"按权限码反查持有它的角色"这类查询。
	// 拆表会引入一次 join 与一套额外的写入顺序，换不到任何当前需要的能力。
	// 真出现反查需求时再追加一条迁移，那正是版本化迁移存在的意义。
	Permissions []string `gorm:"serializer:json;type:text"`
	// Inherits 是被继承的角色标识。
	Inherits []string `gorm:"serializer:json;type:text"`
	// MutuallyExclusiveWith 是静态互斥的角色标识。
	MutuallyExclusiveWith []string `gorm:"serializer:json;type:text"`
}

// TableName 实现 gorm 的表名解析。
func (RoleRecord) TableName() string { return "roles" }

// SubjectRecord 是主体在库里的一行。
type SubjectRecord struct {
	// ID 是主体标识，主键。
	ID string `gorm:"primaryKey;size:191"`
	// Type 是主体类型（人类用户 / 服务账号 / 机器凭证）。
	Type string
	// DefaultScope 是未显式指定作用域时使用的值。
	DefaultScope string
}

// TableName 实现 gorm 的表名解析。
func (SubjectRecord) TableName() string { return "subjects" }

// RoleBindingRecord 是"主体在某个作用域上持有某个角色"这一事实在库里的一行。
//
// 三列构成复合主键，因此**同一事实只能存在一条**。唯一性由库保证而不是
// 由"写入前先查一下"保证：后者在并发下不成立，而重复授予同一角色是
// 最常见的重复写入来源。
//
// 与角色表、主体表之间**不建外键**：角色是否存在、主体是否存在、是否违反
// 互斥都是领域约束，唯一入口是授权面的校验（见 constraints.go）。
// 把同一份判定再写一遍到库约束里，等于让它有两个会漂移的实现。
type RoleBindingRecord struct {
	// SubjectID 是该事实的主体方。
	SubjectID string `gorm:"primaryKey;size:191"`
	// RoleID 是该事实的角色方。
	RoleID string `gorm:"primaryKey;size:191"`
	// Scope 是该事实生效的范围。
	Scope string `gorm:"primaryKey;size:191"`
}

// TableName 实现 gorm 的表名解析。
func (RoleBindingRecord) TableName() string { return "role_bindings" }

// ScopeRecord 是一个**已登记**的范围在库里的一行（见 docs/design/rbac/scopes.md）。
//
// 路径是主键，因此同一个路径只有一条记录。它是标识，**一经登记不可更改**——
// "改路径"不是改名，而是"删掉旧范围 + 新建一个"，与角色标识同构。显示名只用于
// 展示，不参与判定，也不参与匹配，因此两个范围可以重名。
//
// 全局（根）不在这里：它永远可用、不可创建也不可删除，不是一条记录。
//
// 与绑定表之间**不建外键**：绑定必须指向已登记范围，这条是领域约束，唯一入口
// 是授权面的校验（见 internal/rbac/constraints.go）。写成外键等于让同一份判定
// 有两个会漂移的实现，也把"范围路径是自由文本"这一性质交给库约束接管。
type ScopeRecord struct {
	// Path 是范围路径，主键。
	Path string `gorm:"primaryKey;size:191"`
	// DisplayName 是展示名，可改。
	DisplayName string
}

// TableName 实现 gorm 的表名解析。
func (ScopeRecord) TableName() string { return "scopes" }
