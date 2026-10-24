package entity

import "time"

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
