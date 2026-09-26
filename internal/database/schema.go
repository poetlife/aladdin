package database

import "time"

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

// IdentityRecord 是"某个渠道上的某个身份属于哪个主体"在库里的一行。
//
// (Source, ExternalID) 构成复合主键，因此**同一个来源的同一个身份只能属于
// 一个主体**。这条唯一性不是防重复写入的优化，而是多渠道归一的全部地基：
// 它同时给出"登录时该用哪个主体"与"这个身份不能被别人再认领"两个结论
// （见 docs/design/identity/identity-linking.md）。
//
// 一个主体可以有多个身份，一个身份只能属于一个主体。因此主体标识是规范标识，
// 不包含任何渠道取值——把渠道编进主体标识，等于让"一个人两个渠道"这件事
// 在模型上无法表达。
//
// 与主体表之间**不建外键**：主体是否存在是领域约束，唯一入口是认证流程的
// 登记动作，不复制成库约束。
type IdentityRecord struct {
	// Source 是这个身份由谁签发（如 google）。
	//
	// 长度按来源名的量级定：它是一个枚举式的短标识，不是渠道那边的任意字符串。
	Source string `gorm:"primaryKey;size:64"`
	// ExternalID 是该来源签发的不可变标识，即这个渠道上的身份键。
	//
	// 长度取与其它标识列相同的上限（见 IDSize）：渠道标识有多长由渠道决定，
	// 套用仓库里已经定下的那一个上限，比自己估一个更不容易出错。
	ExternalID string `gorm:"primaryKey;size:191"`
	// SubjectID 是这个身份属于谁。
	//
	// 建索引是因为"这个人已经绑了哪些渠道"是绑定与解绑界面的常规读取路径。
	// 这与绑定表刻意不加的索引不同类：那是一个尚未出现的查询，这是一个
	// 确定存在的。
	SubjectID string `gorm:"size:191;index"`
	// Display 是该渠道给出的可读标识（Google 是邮箱），仅供展示与排障。
	//
	// **不参与任何判定，也不参与身份的确定。** 按它归并意味着渠道侧改名或
	// 回收邮箱的那天，另一个人直接接管这个主体的全部角色绑定。
	//
	// 记在身份这一层而不是主体上：同一个人在两个渠道上可能有两个邮箱，
	// 记在主体上等于要求回答"哪个才是他的"。
	Display string
}

// TableName 实现 gorm 的表名解析。
func (IdentityRecord) TableName() string { return "identities" }

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

// SessionRecord 是一份已签发的访问凭证在库里的一行。
//
// **这里存的是凭证的摘要，不是凭证本身。** 拿到库的人拿到的是摘要，而摘要
// 构造不出原凭证，因此一次数据库泄露不等于一次全量会话接管。这条是
// docs/design/persistence/schema.md 里"不存放任何凭证的原文"的落点，
// 也是认证模块唯一被允许落库的东西。
//
// 与主体表之间**不建外键**，理由与绑定表相同：主体是否存在是领域约束，
// 唯一入口是认证流程的登记动作，不复制成库约束。
type SessionRecord struct {
	// TokenHash 是凭证本身的摘要，主键——它同时是唯一的查找入口。
	//
	// 长度按十六进制摘要固定：摘要是定长的，给它一个"够大"的上限只会掩盖
	// "这里其实不该放别的东西"这件事。换摘要算法时随迁移一起调整。
	TokenHash string `gorm:"primaryKey;size:64"`
	// SubjectID 是这个会话代表谁。
	//
	// 不建索引：当前没有任何按主体查会话的路径（撤销以凭证为单位）。等真
	// 出现"一次撤销某主体的全部会话"时再随迁移补上——索引也是结构，不为
	// 一个想象中的查询先付写入代价。
	SubjectID string `gorm:"size:191"`
	// SubjectType 是签发时该主体的类型。
	//
	// 这里存的是**签发那一刻的快照**，与 DefaultScope 同理：凭证一旦签发，
	// 它代表的那一方就固定下来。这样校验路径只读这一张表，不必为了一个
	// 展示字段回头再查一次主体表——而主体表是判定面的数据，认证路径不该
	// 依赖它（见 docs/design/persistence/schema.md）。
	SubjectType string
	// DefaultScope 是签发时绑定在凭证上的作用域。
	//
	// 它不是权限：请求携带的作用域最终仍要与主体的绑定关系比对。因此
	// 签发后主体被收窄了绑定，不会让这个较宽的作用域换来任何实际权限。
	DefaultScope string
	// IssuedAt 是签发时间。只用于排障与审计，不参与判定。
	IssuedAt time.Time
	// ExpiresAt 是过期时间。到点即失效，判定与校验都不依赖清理动作。
	//
	// 建索引是因为启动时的过期行回收要按它筛选：没有它，那一次清理会退化成
	// 对一张只增不减的表做全表扫描。代价只在签发（登录）时付一次，而校验
	// 走的主键路径不受影响。
	ExpiresAt time.Time `gorm:"index"`
}

// TableName 实现 gorm 的表名解析。
func (SessionRecord) TableName() string { return "sessions" }
