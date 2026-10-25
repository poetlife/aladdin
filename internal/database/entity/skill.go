package entity

import "time"

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
