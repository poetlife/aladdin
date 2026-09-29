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

// SubjectProfileRecord 是一个主体的展示信息在库里的一行。
//
// 它是一张**独立于主体表**的表，而不是主体表上多出来的几列。两个理由：
//
//   - 主体表是判定路径要读的数据（"这个主体登记了没有"），而档案是纯展示
//     数据，任何一次判定都不需要它的一个字节；
//   - 主体表是**整行覆盖**写入的（登录登记、引导、开发种子都走同一条路径），
//     昵称若落在那一行上，任何一次主体写入都会把它顺手擦成空值。
//
// 行是**惰性创建**的：主体登记时不写这一行，本人第一次保存档案才写入。
// 认证流程因此完全不依赖本表，依赖方向单向（见
// docs/design/profile/README.md）。
//
// 与主体表之间**不建外键**，理由与绑定表、会话表相同：主体是否存在是领域
// 约束，唯一入口是认证流程的登记动作，不复制成库约束。
type SubjectProfileRecord struct {
	// SubjectID 是主体标识，主键。一个主体最多一条档案。
	SubjectID string `gorm:"primaryKey;size:191"`
	// Nickname 是主体自己设置的昵称。空表示未设置，展示时回退。
	//
	// **刻意不加唯一索引。** 唯一约束会把它变成一个查找键，也就是第二条身份
	// 路径；一旦有了唯一性，"按昵称查人"的需求就会跟着出现——而那正是邮箱
	// 这条路已经踩过的坑。本表也因此不建按昵称的索引：没有任何一条读取路径
	// 按昵称查。
	Nickname string
	// Bio 是简介。空表示未填写。
	Bio string
	// AvatarKey 是头像在对象存储里的对象键。空表示没有头像。
	//
	// **这里存的是键，不是字节。** 字节在对象存储（见
	// docs/design/profile/avatar-storage.md）；本列是"这个主体有没有头像"的
	// 权威，对象存储上可能留下无从被引用的孤儿对象。
	AvatarKey string
	// UpdatedAt 是最后一次变更的时间。只用于展示与排障，不参与判定。
	UpdatedAt time.Time
}

// TableName 实现 gorm 的表名解析。
func (SubjectProfileRecord) TableName() string { return "subject_profiles" }

// GalaxyProjectRecord 是 galaxy 工程在库里的一行。
//
// 工程是创作单元：元数据 + 一种形态 + 一份草稿 + 若干版本 + 一个资产库，但它
// 自己这一行只有元数据。**工程表上不放清单**（草稿、版本与发布的清单都不在这张
// 表上）：工程列表接口会读这一行，把每个工程的清单一起读上来是一笔与列表无关
// 的代价。整张表也不含任何字节。
type GalaxyProjectRecord struct {
	// ID 是工程标识，主键。由 aladdin 分配，**不可猜、不可改、不复用**——
	// 它是发布地址的一部分，而发布态是公开匿名的。
	ID string `gorm:"primaryKey;size:191"`
	// OwnerSubjectID 是创建它的主体。不可转移。
	//
	// 这一列建索引是因为**唯一的列表路径按它查**（"我的工程"）。它与名称那一列
	// 的取向相反，两者的区别是"有没有一条读取路径按它查"，不是"要不要加索引"。
	OwnerSubjectID string `gorm:"size:191;index"`
	// Name 是工程名称。可改。
	//
	// **刻意不加唯一约束、不建索引。** 一旦它成为查找键，它就成了一条可以被
	// 改名或抢注改写的路径；而发布地址用的是不可猜标识，用不着名字。
	Name string
	// Description 是简介。空表示未填写。
	Description string
	// Form 是站点形态（static / docs）。**创建时定下，此后不可改**：它决定已
	// 保存版本的发布语义，允许改形态等于让历史版本的产物无法复现。
	Form string
	// CurrentPublicationID 是**可空的发布指针**：空表示未发布，非空指向
	// galaxy_publications 里的一条记录。
	//
	// 指针放在工程行上，而不是让发布记录反过来标记"我是当前的"：这是工程的
	// 一个属性，只有一个写者。
	CurrentPublicationID string `gorm:"size:191"`
	// CreatedAt 与 UpdatedAt 是展示与排障用，不参与判定。
	CreatedAt time.Time
	UpdatedAt time.Time
}

// TableName 实现 gorm 的表名解析。
func (GalaxyProjectRecord) TableName() string { return "galaxy_projects" }

// GalaxyDraftRecord 是工程当前草稿在库里的一行。一个工程最多一条。
//
// **与版本表分开，而不是在工程行上放一列。** 草稿是可变的、随时被覆盖写的；
// 版本是不可变的。放在一起会让"这一行到底是不是历史"取决于一个额外的标志位，
// 而两张表让不可变性由表的存在方式表达。
type GalaxyDraftRecord struct {
	// ProjectID 是工程标识，主键。一个工程最多一份草稿。
	ProjectID string `gorm:"primaryKey;size:191"`
	// Manifest 是文件清单的序列化形式：一组「路径 → 内容摘要或资产标识」。
	//
	// **它是整组读写的**：保存草稿表达的是完整状态，不是增量。**清单里没有
	// 字节**——文本条目的字节是按内容摘要寻址的对象存储对象。
	Manifest string
	// UpdatedAt 是最后一次保存的时间。展示与排障用。
	UpdatedAt time.Time
}

// TableName 实现 gorm 的表名解析。
func (GalaxyDraftRecord) TableName() string { return "galaxy_drafts" }

// GalaxyVersionRecord 是文件清单的一次不可变快照在库里的一行。
type GalaxyVersionRecord struct {
	// ID 是版本标识，主键。由 aladdin 分配。
	ID string `gorm:"primaryKey;size:191"`
	// ProjectID 是该版本所属工程。建索引是因为唯一的读取路径按它查。
	ProjectID string `gorm:"size:191;index"`
	// Seq 是在工程内递增的序号。**仅用于展示与排序，不是标识。**
	//
	// 刻意不加 (project_id, seq) 的唯一约束：那会把序号变成一个必须被维护的
	// 结构，而"删除一个版本会让序号出现空洞"是可接受的。序号可空洞，标识不可。
	Seq int64
	// Manifest 是保存那一刻草稿的清单。**写入后不再修改。**
	//
	// **版本引用了哪些资产由这一列直接读出**：清单的每一条写明它是文本条目
	// 还是资产条目，因此这件事不必解析任何文本，也没有第二处集合。
	Manifest string
	// RenderRulesVersion 是保存时所处的渲染规则版本。只有 `docs` 形态使用它，
	// 重新发布时按它渲染而不是按当前最新的（见 doc_render.go）。
	RenderRulesVersion int
	// SavedAt 是保存时间。排障与展示用。
	SavedAt time.Time
}

// TableName 实现 gorm 的表名解析。
func (GalaxyVersionRecord) TableName() string { return "galaxy_versions" }

// GalaxyAssetRecord 是工程资产库里的一个媒体文件在库里的一行。
type GalaxyAssetRecord struct {
	// ID 是资产标识，主键。由 aladdin 分配。
	ID string `gorm:"primaryKey;size:191"`
	// ProjectID 是该资产所属工程，建索引的理由与版本表相同。
	ProjectID string `gorm:"size:191;index"`
	// Digest 是字节的密码学摘要（SHA-256 的十六进制），**是"同一份字节"的
	// 标识**。公开区按**（内容摘要，媒体类型）**寻址，因此同一份字节以两种
	// 类型出现时是两个对象。
	Digest string `gorm:"size:64"`
	// MediaKind 是类别（图片 / 视频 / 音频 / 字体），决定大小上限，也是私有区
	// 对象键里的一段。
	MediaKind string
	// MediaType 是**上传方声明的**类型，服务端只校验它在白名单内。它同时决定
	// 此后下发时回给浏览器的内容类型——两者只有一个来源。
	MediaType string
	// SizeBytes 是字节数。它取自提交时对对象的核对，不是声明值。
	SizeBytes int64
	// Filename 是原始文件名。**仅供展示与排障**：它不作为对象键，也不参与
	// 任何判断。它可能含路径分隔符、控制字符以及别人的名字。
	Filename string
	// UploadedAt 是上传时间。
	UploadedAt time.Time
}

// TableName 实现 gorm 的表名解析。
func (GalaxyAssetRecord) TableName() string { return "galaxy_assets" }

// GalaxyPublicationRecord 是每次发布产生的产物在库里的一行。
//
// **产物放在独立的发布表上，不放在工程行上**：工程行会被列表接口读取，往它
// 上面挂一组记录正是档案模块那一课要避免的事。工程行上只有一个可空的指针。
type GalaxyPublicationRecord struct {
	// ID 是发布标识，主键。由 aladdin 分配，且**只由工程与版本决定**，因此
	// 同一次发布重试到落库这一步不会产生第二条记录。
	ID string `gorm:"primaryKey;size:191"`
	// ProjectID 建索引的理由与版本表相同。
	ProjectID string `gorm:"size:191"`
	// VersionID 是这次发布的是哪个版本。
	VersionID string `gorm:"size:191"`
	// Manifest 是**产物清单**：一组「路径 → 内容摘要」或资产条目。对外地址按
	// 它分派；**字节不在库里**。
	Manifest string
	// PublishedBySubjectID 与 PublishedAt 是留痕。
	PublishedBySubjectID string `gorm:"size:191"`
	PublishedAt          time.Time
}

// TableName 实现 gorm 的表名解析。
func (GalaxyPublicationRecord) TableName() string { return "galaxy_publications" }
