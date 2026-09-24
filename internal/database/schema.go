package database

// 本文件是**表结构的唯一信源**：库里的每一张表、每一列都在这里定义，
// 迁移只负责把这里的定义推到库里（见 docs/design/persistence/schema.md）。
//
// 记录类型与领域对象是分开的：领域对象（rbac.RoleDefinition 等）表达
// "权限模型里有什么"，记录类型表达"它在库里长什么样"。两者由
// internal/rbac/gormstore 转换，转换是唯一一处——记录里加一列不会
// 悄悄改变判定看到的东西。
//
// 库里长什么样与怎么建表是两件事：本文件只描述前者。建表由
// internal/database/migrate 里的迁移完成，本文件不依赖它，也不被它修改。

// IDSize 是所有标识类列的长度上限。
//
// 取 191 而不是更大的值：utf8mb4 下它对应 764 字节，三列复合主键
// （见 RoleBindingRecord）合计仍在 InnoDB 的索引长度限制之内。
// 本仓库的标识都是 `system.admin`、`tenant/acme` 这种形状，
// 这个上限远超实际需要。
//
// 导出是因为它不止约束这三张业务表：迁移版本表的标识列同样要落在
// 同一个上限内（见 internal/database/migrate）。取值只有一个来源。
const IDSize = 191

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
