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
// 工程是创作单元：元数据 + 一组内容槽 + 一个资产库，但它自己这一行只有元数据。
// **工程表上不放清单**（草稿、版本与发布的清单都不在这张表上），也**不放发布指针
// 与形态**（那两样住在 galaxy_project_slots 上）：工程列表接口会读这一行，把每个
// 工程的清单一起读上来是一笔与列表无关的代价。整张表也不含任何字节。
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
	// CreatedAt 与 UpdatedAt 是展示与排障用，不参与判定。
	//
	// **这张表上没有形态，也没有发布指针**：一个工程有哪几种内容由
	// galaxy_project_slots 回答，发布指针也住在那一张表上（每个槽一个）。
	CreatedAt time.Time
	UpdatedAt time.Time
}

// TableName 实现 gorm 的表名解析。
func (GalaxyProjectRecord) TableName() string { return "galaxy_projects" }

// GalaxySlotRecord 是工程启用的一个内容槽在库里的一行。一个槽一条。
//
// **行存在即槽启用**："这个工程有哪些内容"只有这一处定义，工程表上不重复存
// 一份——那会是同一事实的第二个来源，两者不一致时无从判断谁对。**槽只增不删**
// （见 docs/design/galaxy/site-model.md），因此这张表不会出现"某一行曾经存在
// 过"的历史。
//
// **发布指针按槽分开是这一层最要紧的一条**：两个槽各有各的指针，发布与撤回都
// 只碰自己那一行——站点发出去不影响文档，撤回一个也不影响另一个。
type GalaxySlotRecord struct {
	// ProjectID 与 Slot 是联合主键。槽取 `site` 或 `docs`。
	ProjectID string `gorm:"primaryKey;size:191"`
	Slot      string `gorm:"primaryKey;size:16"`
	// CurrentPublicationID 是**可空的发布指针**：空表示这个槽未发布，非空指向
	// galaxy_publications 里的一条记录。
	CurrentPublicationID string `gorm:"size:191"`
}

// TableName 实现 gorm 的表名解析。
func (GalaxySlotRecord) TableName() string { return "galaxy_project_slots" }

// GalaxyDraftRecord 是**一个内容槽**当前草稿在库里的一行。一个槽最多一条。
//
// **与版本表分开，而不是在工程行上放一列。** 草稿是可变的、随时被覆盖写的；
// 版本是不可变的。放在一起会让"这一行到底是不是历史"取决于一个额外的标志位，
// 而两张表让不可变性由表的存在方式表达。
type GalaxyDraftRecord struct {
	// ProjectID 与 Slot 是联合主键。一个工程**每个槽**最多一份草稿。
	ProjectID string `gorm:"primaryKey;size:191"`
	Slot      string `gorm:"primaryKey;size:16"`
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

// GalaxyDraftSnapshotRecord 是**被替换掉的**那一份草稿清单在库里的一行。
//
// 它不是版本：没有序号、不能发布、按保留策略过期（见
// docs/design/galaxy/project-versioning.md）。它存在的理由是"只推草稿不存版本"
// 这条最常见的误用——那种用法下，中间过程在改动发生的那一刻就没有任何记录。
//
// **清单里没有字节**：与草稿、版本一样，条目指向的是按内容摘要寻址的对象。
type GalaxyDraftSnapshotRecord struct {
	// ID 是快照标识，主键。由 aladdin 分配。**前缀与版本不同**（`snp_` 对 `ver_`）：
	// 拿一条快照的标识去发布必须失败，而标识一眼看得出拿错了哪一种。
	ID string `gorm:"primaryKey;size:191"`
	// ProjectID 与 Slot 是它所属的（工程，槽）。一个槽一行。
	//
	// 建复合索引是因为读取路径只有一条：按（工程，槽）取最近的那几条。
	ProjectID string `gorm:"size:191;index:idx_galaxy_draft_snapshots_slot,priority:1"`
	Slot      string `gorm:"size:16;index:idx_galaxy_draft_snapshots_slot,priority:2"`
	// Seq 是在**槽内**递增的序号，**仅用于排序，不是标识**。
	//
	// 不靠时间排序：两次替换可能落在同一个时间刻上（库的时间列精度有限，而一次
	// 密集的构建循环本来就可能在同几秒里推好几次），那时"哪一条是最近的"就成了
	// 一个由标识的随机性决定的答案。序号在写入它的那个事务里分配，因此"最新的
	// 那一条"永远是一个确定的答案。删除与清理会让序号出现空洞，这是可接受的
	// （与版本表同一条）。
	Seq int64 `gorm:"index:idx_galaxy_draft_snapshots_slot,priority:3"`
	// Manifest 是被替换掉的那份清单的序列化形式。**写入后不再修改。**
	Manifest string
	// Source 是替换它的那一端（`web` / `cli`）。没带上报端标识时为空。
	Source string `gorm:"size:16"`
	// ReplacedBySubjectID 是执行那次替换的主体。留痕用。
	ReplacedBySubjectID string `gorm:"size:191"`
	// CreatedAt 是它被替换掉的时刻。**保留策略按它判定**，因此它参与排序。
	CreatedAt time.Time
}

// TableName 实现 gorm 的表名解析。
func (GalaxyDraftSnapshotRecord) TableName() string { return "galaxy_draft_snapshots" }

// GalaxyVersionRecord 是文件清单的一次不可变快照在库里的一行。
type GalaxyVersionRecord struct {
	// ID 是版本标识，主键。由 aladdin 分配。
	ID string `gorm:"primaryKey;size:191"`
	// ProjectID 是该版本所属工程。建索引是因为唯一的读取路径按它查。
	ProjectID string `gorm:"size:191;index"`
	// Slot 是该版本所属内容槽。**版本按槽隔离**：它只在自己的槽里被列出、
	// 发布与撤回，序号也在槽内递增。
	Slot string `gorm:"size:16;index"`
	// Seq 是在**槽内**递增的序号。**仅用于展示与排序，不是标识。**
	//
	// 刻意不加 (project_id, slot, seq) 的唯一约束：那会把序号变成一个必须被
	// 维护的结构，而"删除一个版本会让序号出现空洞"是可接受的。序号可空洞，
	// 标识不可。
	Seq int64
	// Manifest 是保存那一刻草稿的清单。**写入后不再修改。**
	//
	// **版本引用了哪些资产由这一列直接读出**：清单的每一条写明它是文本条目
	// 还是资产条目，因此这件事不必解析任何文本，也没有第二处集合。
	Manifest string
	// Description 是可选的一句说明（"这一版加了什么"）。空表示没有。
	//
	// **它是元数据层，与"版本不可变"不冲突**：不可变说的是 Manifest 与
	// RenderRulesVersion，而说明不进产物、不参与任何判定，因此**可以事后改**。
	// 它不设列长：上限按**字符数**在领域层卡（见 galaxy.VersionDescriptionMaxRunes），
	// 而一个字符占几个字节取决于内容，在这里写死一个字节数会与那一条对不上。
	Description string
	// RenderRulesVersion 是保存时所处的渲染规则版本。只有 `docs` 槽使用它，
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
	// Title 是展示标题，与 Filename 分离。空表示没有标题。
	Title string
	// Notes 是自由文本备注。**不进对象键、不进发布产物、不进日志原文。**
	//
	// 它不设列长：上限按**字符数**在领域层卡（见 galaxy.AssetNotesMaxRunes），
	// 而一个字符占几个字节取决于内容，在这里写死一个字节数会与那一条对不上。
	Notes string
	// UploadedAt 是上传时间。
	UploadedAt time.Time
}

// TableName 实现 gorm 的表名解析。
func (GalaxyAssetRecord) TableName() string { return "galaxy_assets" }

// GalaxyAssetTagRecord 是一个资产的一个标签在库里的一行。
//
// **标签独立成表，而不是资产行上的一列**：它要支持"按标签列出工程里的资产"，
// 而那在资产行的一列里只能靠模糊匹配（见 docs/design/persistence/schema.md）。
type GalaxyAssetTagRecord struct {
	// ProjectID 与 AssetID 合起来指向资产。工程标识也带上，是为了让"这个工程
	// 有哪些标签"能只查这张表，不必回连资产行。
	ProjectID string `gorm:"primaryKey;size:191;index:idx_galaxy_asset_tags_tag,priority:1"`
	AssetID   string `gorm:"primaryKey;size:191"`
	// Tag 是**归一化之后**的标签串（小写）。原始写法不保留。
	//
	// (ProjectID, AssetID, Tag) 是复合主键，因此同一个资产上的同一个标签不会
	// 产生第二行；另建的 (project_id, tag) 索引服务按标签筛选。
	Tag string `gorm:"primaryKey;size:64;index:idx_galaxy_asset_tags_tag,priority:2"`
}

// TableName 实现 gorm 的表名解析。
func (GalaxyAssetTagRecord) TableName() string { return "galaxy_asset_tags" }

// GalaxyAttachmentRecord 是工程里的一份**发布物**（二进制、zip、导出文件）在库里的一行。
//
// 它与资产表**并列而不合并**（见 docs/design/galaxy/attachments.md）：资产会被网页
// 引用、由浏览器渲染，因此类型必须过白名单、发布后进公开区；附件只给工程成员下载，
// 类型不限，且**永不进公开区**。合成一张表会让"这个工程有哪些东西"变成一句需要
// 按列分辨的话，而它直接决定了下发那一侧走哪条路径。
//
// **字节不在这张表里**，键为 `galaxy/<工程标识>/attachments/<附件标识>`。
type GalaxyAttachmentRecord struct {
	// ID 是附件标识，主键。由 aladdin 分配，不可猜、不复用。
	ID string `gorm:"primaryKey;size:191"`
	// ProjectID 是该附件所属工程。建索引的理由与资产表相同。
	ProjectID string `gorm:"size:191;index"`
	// VersionID 是**标注**的版本，可空。它不是外键、也没有级联：删除一个版本时
	// 指向它的标注被清空（见 MutableStore.DeleteVersion），而标注**不拦阻**版本
	// 删除——它不参与发布（见 attachments.md）。
	VersionID string `gorm:"size:191;index"`
	// Filename 是原始文件名。**仅供展示与排障**：它不进对象键，也不参与任何判断。
	// 它另外会进下载响应头，而那一处会先滤掉会破坏响应头的字符。
	Filename string
	// SizeBytes 是字节数。它取自提交时对对象的核对，不是声明值；工程的附件总量
	// 配额也是按这一列求和得出的。
	SizeBytes int64
	// Digest 是字节的 SHA-256。**不是寻址键**（附件按标识寻址），只用于核对与展示。
	Digest string `gorm:"size:64"`
	// Description 是说明。可改，可空。
	Description string
	// UploadedBySubjectID 与 UploadedAt 是留痕。
	UploadedBySubjectID string `gorm:"size:191"`
	UploadedAt          time.Time
}

// TableName 实现 gorm 的表名解析。
func (GalaxyAttachmentRecord) TableName() string { return "galaxy_attachments" }

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
	// Slot 是这次发布属于哪个内容槽。地址按槽分派，因此指针切换时也要对上它。
	Slot string `gorm:"size:16"`
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

// GalaxyPreviewGrantRecord 是一条**预览凭证**在库里的一行。
//
// 凭证是预览地址的一部分，拿到它的人能看到这个工程的草稿（见
// docs/design/galaxy/authoring.md）。因此它**只按工程授权、只短时有效**，且不
// 承载任何别的能力：读不了别的工程，写不了，也发不了布。
//
// **不放在工程行上**：工程行会被列表与详情接口读取，把一条凭证挂上去等于让它
// 随每一次工程读取被带出来；凭证只在两处被读写——签发的那一刻，与一次预览请求。
//
// **过期由这一列的时间判定，不由"行还在不在"判定。** 删除只是清理：判定只比
// 较时间，因此一条没被清掉的行也不会多给一秒钟的访问权。
type GalaxyPreviewGrantRecord struct {
	// Token 是凭证本体，主键。随机、不可猜——它是预览地址里唯一保密的那一段。
	Token string `gorm:"primaryKey;size:191"`
	// ProjectID 是它授权的工程。建索引是为了"清理这个工程的过期凭证"能按它查。
	ProjectID string `gorm:"size:191;index"`
	// Slot 是它授权的**内容槽**。**凭证绑在（工程，槽）上**：拿站点槽的票去取
	// 文档槽的路径，与"这一页不存在"没有区别。
	Slot string `gorm:"size:16"`
	// SubjectID 是签发时的主体。**留痕用**：判定不看它，因为工程本身只有拥有者
	// 读得到（见 OwnedProject）。
	SubjectID string `gorm:"size:191"`
	// ExpiresAt 是失效时刻。
	ExpiresAt time.Time
	// CreatedAt 是签发时刻。留痕用。
	CreatedAt time.Time
}

// TableName 实现 gorm 的表名解析。
func (GalaxyPreviewGrantRecord) TableName() string { return "galaxy_preview_grants" }

// SkillRecord 是平台技能目录里的一个技能在库里的一行（见
// docs/design/skill/README.md）。
//
// **它不含任何字节**：内容是一组「路径 → 内容摘要 + 字节数」，随版本行整份读写；
// 字节按内容摘要存放在对象存储、且**跨技能共享**（见 internal/skill/package.go 的
// ContentObjectKey）。因此这一行不会有"大字段随列表被带出来"的问题。
//
// **没有"拥有者"这一列。** 目录是部署级的：技能不属于任何主体，谁能看见由权限码
// 表达、不由归属表达。纳管是谁做的只进留痕，不进数据模型。
type SkillRecord struct {
	// ID 是技能标识，主键。由 aladdin 分配、不可猜、不改、不复用。
	ID string `gorm:"primaryKey;size:191"`
	// Title 与 Summary 是**说明层的原值**。**空表示没有覆盖**——对外的有效取值是
	// 它回退到当前版本的 name / description 之后的结果，而那一次折算在领域层做
	// （空是一个判据，不是"内容是一个空字符串"）。
	Title   string
	Summary string
	// 来源：两段名字、引用、子路径，加上上次同步到的提交。
	//
	// **两段名字分开存，而不是存一个地址串**：地址串的形状规则将来若变，存量行不
	// 会因此解析不出来；而取字节的地址由这两段现拼，不存在"存进去的地址与用出去
	// 的地址不是一回事"这条缝。
	SourceOwner   string
	SourceName    string
	SourceRef     string
	SourceSubPath string
	// SourceCommit 是上次同步到的提交。它是"这个技能更新到哪了"的唯一答案，也是
	// 同步时比对"远端有没有变化"的对象。
	SourceCommit string `gorm:"size:64"`
	// CurrentVersionID 是当前对外服务的那一份快照。
	//
	// **它永远非空**：技能是随第一个版本一起产生的，没有"建好了还没有版本"这个
	// 状态。回滚只改这一列，不动任何版本行。
	CurrentVersionID string `gorm:"size:191"`
	CreatedAt        time.Time
	// UpdatedAt 是说明层最后一次改动的时间。展示与排障用。
	UpdatedAt time.Time
}

// TableName 实现 gorm 的表名解析。
func (SkillRecord) TableName() string { return "skills" }

// SkillVersionRecord 是一个技能的一份**不可变快照**在库里的一行。
//
// 与 galaxy 的版本行同理：写入之后不再修改，因此"这份内容当时是什么"永远答得
// 上来。回滚只切技能行上的指针。
type SkillVersionRecord struct {
	// ID 是版本标识，主键。由 aladdin 分配。
	ID string `gorm:"primaryKey;size:191"`
	// SkillID 是该版本所属技能。建索引是因为唯一的读取路径按它查。
	SkillID string `gorm:"size:191;index"`
	// Commit 是这一份取自远端的哪个提交。
	Commit string `gorm:"size:64"`
	// Name 与 Description 是这一份 SKILL.md 里声明的两项，**在纳管时解析一次**。
	//
	// 它们既是版本快照的一部分（内容不变它们就不变），也是说明层留空时的回退
	// 目标。每次读取都回到对象里取，会把一次目录列表变成几百次对象读取。
	// Description 是**触发说明**：读者靠它决定要不要用这个技能。
	Name        string
	Description string
	// Files 是清单的序列化形式：一组「路径 → 内容摘要 + 字节数」。
	//
	// **它是整份读写的**（与 RoleRecord.Permissions 同一条理由）：没有"按某一条
	// 路径反查版本"的查询，而读取永远要整棵树（校验、取用、界面上的文件列表）。
	// 拆成关联表只会引入一次 join 与一套写入顺序。
	Files []SkillFileRecord `gorm:"serializer:json;type:text"`
	// SkippedFiles 是**上游有、而平台没收**的条目数。
	//
	// 平台只分发文本，因此真实仓库里那些示例图一类的东西不进包（见
	// internal/skill/repo_tree.go）。这个计数让"平台里的包比上游少几个文件"
	// 成为一件看得见的事——静默地少才是问题。
	SkippedFiles int
	// CreatedAt 是这一份从远端纳管进来的时刻。
	CreatedAt time.Time
}

// TableName 实现 gorm 的表名解析。
func (SkillVersionRecord) TableName() string { return "skill_versions" }

// SkillFileRecord 是技能版本清单里的一条。
//
// **它不是一张表**：它是 SkillVersionRecord.Files 那一列里 JSON 的元素，因此没有
// 表名。放在这里是因为它是"库里的形状"的一部分，而表结构的唯一信源是本文件。
type SkillFileRecord struct {
	// Path 是包内相对路径。
	Path string `json:"path"`
	// Digest 是内容摘要，也是私有区对象的键。
	Digest string `json:"digest"`
	// SizeBytes 是字节数。
	SizeBytes int64 `json:"size_bytes"`
}

// SkillTagRecord 是一个技能的一个标签在库里的一行。
//
// **标签独立成表，而不是技能行上的一列**：它要支持"按标签列出技能"，而那在技能行
// 的一列里只能靠模糊匹配（与 galaxy 资产标签同一条理由）。
type SkillTagRecord struct {
	// (SkillID, Tag) 是复合主键：同一个技能上的同一个标签不会产生第二行。
	// 另建的 (tag, skill_id) 索引服务按标签筛选——那是这一列唯一的另一种查法。
	SkillID string `gorm:"primaryKey;size:191;index:idx_skill_tags_tag,priority:2"`
	// Tag 是**归一化之后**的标签串（小写）。原始写法不保留。
	Tag string `gorm:"primaryKey;size:64;index:idx_skill_tags_tag,priority:1"`
}

// TableName 实现 gorm 的表名解析。
func (SkillTagRecord) TableName() string { return "skill_tags" }

// SkillImageRecord 是一个技能的一张**展示图**在库里的一行（见
// docs/design/skill/catalog.md 的"展示图集"）。
//
// **图集独立成表，而顺序是它自己的一列**：顺序会变（重排、设为首图），而"哪一张"
// 不随顺序变。把顺序编进对象键（`<技能标识>/<序号>`）会让一次重排变成"把每一张都
// 搬家"，而删中间一张会让后面每一张的地址都作废。
//
// 它**只存清单，不存字节**：字节在对象存储，一张一个键，由这一个技能独占（不像内容
// 对象那样按摘要共享）。
type SkillImageRecord struct {
	// ID 是图标识，主键。由 aladdin 分配、不可猜、不改、不复用。
	//
	// **不复用是有代价的**：对象键由它派生，因此复用一个标识就等于让新图去覆盖旧图
	// 的对象。
	ID string `gorm:"primaryKey;size:191"`
	// SkillID 是这一张所属的技能。这个索引与 Position 合起来服务唯一的读取路径：
	// "按顺序取一个技能的整组图"。
	SkillID string `gorm:"size:191;index:idx_skill_images_order,priority:1"`
	// Position 是它在图集里的位置，**越小越靠前，最小的那个就是首图**。
	//
	// 它不稠密也成立（删除会留下空档）：顺序只按大小比，不按稠密性判。
	Position int `gorm:"index:idx_skill_images_order,priority:2"`
	// ObjectKey 是这一张在对象存储里的键。**存下来而不是每次现拼**，是为了那些
	// 不按当前规则派生的存量行（早期的单张封面用的是另一个前缀）。
	ObjectKey string
	// SizeBytes 是字节数。图集的合计上限按它算，因此它必须与桶上的对象一致。
	SizeBytes int64
	CreatedAt time.Time
}

// TableName 实现 gorm 的表名解析。
func (SkillImageRecord) TableName() string { return "skill_images" }

// SkillFavoriteRecord 是一个主体收藏一个技能在库里的一行。
//
// **它是主体与技能之间的关系**，既不是技能的属性也不是主体的属性。它不改变目录
// 内容、不影响别人，因此不需要写权限（见 docs/design/skill/catalog.md）。
type SkillFavoriteRecord struct {
	SkillID string `gorm:"primaryKey;size:191"`
	// SubjectID 是主体标识。`subject_id` 上建索引是因为唯一的另一种查法是
	// "这个主体收藏了哪些"。
	SubjectID string `gorm:"primaryKey;size:191;index"`
	CreatedAt time.Time
}

// TableName 实现 gorm 的表名解析。
func (SkillFavoriteRecord) TableName() string { return "skill_favorites" }

// SkillUsageDailyRecord 是一次取用按（技能，主体，日期）归并之后的一行。
//
// **主键就是那三项**：一次取用连着取好几个文件，按次记的数字主要由"这个包有几个
// 文件"决定，与"有没有人在用"无关。按人日归并之后，行数有天然上界（技能数 ×
// 主体数 × 天数），而"最近 30 天有多少人在用"是一次分组计数。
//
// 它**只增不减，靠保留期回收**（与客户端事件同一条），回收在服务端启动路径上做。
type SkillUsageDailyRecord struct {
	SkillID   string `gorm:"primaryKey;size:191"`
	SubjectID string `gorm:"primaryKey;size:191"`
	// Day 是**日期串**（`2006-01-02`）而不是时间戳。
	//
	// 时间戳在两种后端之间的时区处理并不一致（一种带时区存储、一种不带），而"哪一
	// 天"这个判据不该取决于库里怎么存。日期串是它实际的含义，也顺带让"早于某天"
	// 成为一次字典序比较。
	Day string `gorm:"primaryKey;size:10;index"`
	// UsedAt 是那一天里最后一次取用的时刻。
	UsedAt time.Time
}

// TableName 实现 gorm 的表名解析。
func (SkillUsageDailyRecord) TableName() string { return "skill_usage_daily" }

// ClientEventRecord 是一条**客户端事件**在库里的一行（见
// docs/observability.md 的「客户端事件」）。
//
// 它是写侧落库的读模型，只服务于站内的只读管理页。存**原始事件**而不是按分钟
// 汇总，取的是"一张表同时回答计数与明细"：客户端事件是低频批量上报（只有出站前
// 被拦下的那一档），30 天的行数可控，没有双写与 rollup 一致性的成本。量级真涨到
// 需要汇总表时再追加一条迁移——那正是版本化迁移存在的意义。
//
// 与其它表不同，这一张**只增不减，靠保留期回收**：行本身没有"被撤销"的语义，
// 超出窗口的行由启动时的回收删掉（见 internal/telemetry 的保留期常量）。
//
// 与主体表之间**不建外键**，理由与绑定表、会话表相同：主体是否存在是领域约束，
// 唯一入口是认证流程的登记动作，不复制成库约束。匿名事件根本没有主体，加外键
// 还会把"合法的空值"变成一个结构错误。
type ClientEventRecord struct {
	// ID 是自增主键。
	//
	// 事件**没有自然主键**：同一毫秒内的两条相同事件是两条合法记录，用
	// (时间, 动作) 之类的组合当键会把它们合并成一条。自增列因此不是图省事，而是
	// 承认"每一条都是独立事实"。
	ID uint64 `gorm:"primaryKey;autoIncrement"`
	// OccurredAt 是服务端收到并落库的时刻——**不由上报端提供**。
	//
	// 建索引是因为两条常规路径都按它筛选：时间窗内的计数与明细，以及保留期回收。
	// 没有它，这两者都会退化成对一张只增不减的表做全表扫描。
	OccurredAt time.Time `gorm:"index"`
	// Client 是上报端（`web` / `cli`）。取值经服务端白名单折算，不是上报端的原值。
	Client string
	// Surface 是事件发生在哪个界面/入口。
	Surface string
	// Action 是动作名，取值来自服务端的动作允许清单。**它不是枚举列**：动作清单随
	// 业务扩展，而这里是历史数据——用枚举会让"昨天记下的动作"在清单变更后无法表示。
	Action string
	// Result 是结局（`ok` / `fail` / `cancel` / `blocked`）。
	Result string
	// DurationMS 是上报端声明的耗时；0 表示未提供。
	DurationMS uint32
	// ClientTraceID 是上报端当时所在链路的 trace_id；空表示未提供或不合法。
	//
	// 注意它**不是**本行所属请求的 trace_id，而是上报端当时那条链路的，因此读侧
	// 与日志里的字段名一致，都叫 client_trace_id。
	ClientTraceID string `gorm:"size:64"`
	// SubjectID 是事件所属主体；空表示匿名。由服务端从会话得出，不由请求提供。
	SubjectID string `gorm:"size:191"`
	// Attrs 是白名单属性，与主体权限集合同样**整份读写**——读侧要么整行取出展示，
	// 要么按 (client, action, result) 计数，没有任何"按某个属性值反查"的查询。
	// 拆成关联表会引入一次 join 与一套写入顺序，换不到当前需要的能力。
	Attrs map[string]string `gorm:"serializer:json;type:text"`
}

// TableName 实现 gorm 的表名解析。
func (ClientEventRecord) TableName() string { return "client_events" }

// RegistrationPolicyRecord 是站点级注册策略在库里的一行（见
// docs/design/identity/registration.md）。
//
// **一行，主键是一个固定取值。** 它是站点级的一份策略，不是一张按维度索引的
// 表；把主键钉死成一行，比"取第一行"少一个"到底以哪一行为准"的疑问。将来若真
// 要做"按渠道分别设"，那时是一个维度列进主键，而不是换一套读法。
//
// **没有记录时的取值就是缺省姿态**（开放注册、无默认角色、全局范围），因此迁移
// 只建表、不写行：初值写进迁移是把同一件事说两遍，而两处迟早会有一处被改。这一条
// 同时保证了既有部署升级后行为逐字不变。
//
// 它**不参与任何判定**：判定路径读的是绑定表，这张表只在"登记一个新主体"那一刻
// 被读一次。与绑定表之间因此不建外键——角色是否存在、范围是否已登记都是领域约束，
// 唯一入口是管理面的校验。
type RegistrationPolicyRecord struct {
	// ID 是固定主键，恒为"site"。
	ID string `gorm:"primaryKey;size:16"`
	// Mode 是准入姿态：open / invite / closed。
	Mode string `gorm:"size:16"`
	// DefaultRoleID 与 DefaultScope **同进同退**：要么都有，要么都没有。
	//
	// 后者空表示全局。它同时成为新主体的默认作用域——主体的默认作用域只能来自
	// 绑定关系，因此它取自那条绑定，不另有一个配置项。
	DefaultRoleID string `gorm:"size:191"`
	DefaultScope  string `gorm:"size:191"`
	// UpdatedBySubjectID 与 UpdatedAt 是留痕，**不参与判定**：它们回答"这个姿态
	// 是谁、什么时候定的"，而答案不影响任何一次放行。
	UpdatedBySubjectID string `gorm:"size:191"`
	UpdatedAt          time.Time
}

// TableName 实现 gorm 的表名解析。
func (RegistrationPolicyRecord) TableName() string { return "registration_policy" }

// RegistrationInviteRecord 是一份已签发的邀请码在库里的一行（见
// docs/design/identity/registration.md）。
//
// **码原文不入库。** 这里存的是它的摘要，与访问凭证同一条性质、同一个入口：
// 一次数据库泄露交出的是摘要，而摘要构造不出原文。
//
// CodeHash 而不是 ID 作查找键，但**ID 才是撤销与列表的定位键**：读侧拿不到摘要，
// 也就无从离线比对一份猜出来的码。唯一索引落在摘要上，是因为兑换只按摘要进。
//
// **只撤销，不删除**：RevokedAt 非空即不可兑换，行仍在——删掉会连用量与留痕
// 一起丢掉，"这个码被谁用过、用过几次"就再也答不上来。
type RegistrationInviteRecord struct {
	// ID 是邀请码标识，主键，由 aladdin 分配、不可猜。
	ID string `gorm:"primaryKey;size:191"`
	// CodeHash 是码明文的 SHA-256。建唯一索引是因为兑换只按它进。
	CodeHash string `gorm:"size:64;uniqueIndex"`
	// Label 是管理员自己看的标签，**不参与任何判断**。
	Label string
	// CreatedBySubjectID 与 CreatedAt 是留痕。
	CreatedBySubjectID string `gorm:"size:191"`
	CreatedAt          time.Time
	// MaxUses 是次数上限，0 表示不限次；UsedCount 由兑换时的一次条件更新推进。
	MaxUses   int32
	UsedCount int32
	// ExpiresAt 为 NULL 表示不过期。
	//
	// 用可空列而不是零值：MySQL 的 datetime 下界是 1000 年，零值时间在那上面
	// 根本插不进去，而在 SQLite 上会变成一串看起来像数据的 '0001-01-01'。
	ExpiresAt *time.Time
	// RevokedAt 为 NULL 表示未撤销。
	RevokedAt *time.Time
}

// TableName 实现 gorm 的表名解析。
func (RegistrationInviteRecord) TableName() string { return "registration_invites" }
