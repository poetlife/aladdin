package entity

import "time"

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
	// BlindWatermark 表示这个工程**要不要在发布出来的站点上铺盲水印**。
	//
	// **默认关闭。** 盲水印要在截图上留下可提取的痕迹，而它必然改变渲染结果的
	// 像素——在深色主题上，那点改动是看得见的（见 docs/design/galaxy/watermark.md
	// 的"可见性"）。因此它不是一个"平台替所有人决定"的能力，而是一个**创作者对
	// 自己作品的选择**：需要溯源的人打开它，能接受那层纹理；不需要的人不受影响。
	//
	// 存在工程行上而不是内容槽行上：它是"这个作品要不要"的问题，两个槽属于同一个
	// 作品，不该各答一半。
	BlindWatermark bool
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

// GalaxyWatermarkVisitRecord 是**一次被水印标记过的访问**在库里的一行。
//
// 水印本身叠在主站壳上，像素里只有一枚随机码（见
// docs/design/galaxy/watermark.md）；这张表就是"那枚码对回谁"的那一处。因此它是
// 这套设计里唯一装着身份的一层，取舍写在下面几列上：
//
//   - **主体可以为空**。发布域是匿名可取用的，没有会话时身份退到来源地址与浏览器
//     标识的摘要。那一档仍然是同一件事的一次留痕，只是它指向"这一次访问"而不是一个人。
//   - **浏览器标识只存摘要**（见 galaxy.DigestUserAgent）：原值是一串能指纹化这台
//     设备的字符串，而溯源只需要"是不是同一台"。
//   - **来源地址只在这里**：不进日志、不进明水印、不进给访客的响应。发布物内不得
//     承载任何秘密那条约束在这里的具体形态就是它。
//
// **它不含内容本身**，也不随发布指针变化：撤回发布不删它——登记回答的是"这一份图
// 是谁看的"，与"现在还能不能打开"是两件事。
type GalaxyWatermarkVisitRecord struct {
	// Code 是水印码，主键。它就是像素里编着的那一串字符。
	Code string `gorm:"primaryKey;size:32"`
	// ProjectID 与 Slot 是这次访问落在哪个工程的哪个槽。
	ProjectID string `gorm:"size:191;index"`
	Slot      string `gorm:"size:16"`
	// SubjectID 是访客的主体标识。**匿名为空。**
	SubjectID string `gorm:"size:191"`
	// IdentityKey 是"同一访客"这个判据的取值（见 galaxy.WatermarkViewer.IdentityKey）。
	//
	// 它单独成列而不是每次由主体与来源现算：匿名那一档的取值包含来源地址，而
	// 来源地址是会变的**输入**，判据不能跟着它漂；存下来也让"窗口内复用同一枚码"
	// 是一次带索引的查找，而不是把表扫一遍再比较。
	IdentityKey string `gorm:"size:255;index:idx_galaxy_watermark_reuse"`
	// SourceAddress 是访客的来源地址。**只在这里。**
	SourceAddress string `gorm:"size:128"`
	// UserAgentDigest 是浏览器标识的摘要。
	UserAgentDigest string `gorm:"size:32"`
	// VisitedAt 是这枚码第一次发出去的时刻。明水印上印的日期就是它。
	VisitedAt time.Time `gorm:"index:idx_galaxy_watermark_reuse"`
}

// TableName 实现 gorm 的表名解析。
func (GalaxyWatermarkVisitRecord) TableName() string { return "galaxy_watermark_visits" }
