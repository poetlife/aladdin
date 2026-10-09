// Package galaxy 是"用户放下一组具名文件与素材、再把它发布成一个站点"这一能力
// 的唯一实现。
//
// 边界：它不回答"你是谁"（认证，见 internal/identity），也不回答"你能做什么"
// （RBAC，见 internal/rbac）。判定发生在服务端的鉴权拦截器里，本包消费的是
// 它的结论与一个**已确认的主体标识**。
//
// 本包内部有两把闸门，各管一件事，不可互相替代：
//
//   - RBAC 权限码决定这个主体**有没有资格创作**（在 proto 的方法注解上声明）；
//   - 归属决定这个工程**是不是他的**（见 ownership.go）。
//
// 归属不由 RBAC 表达，也因此**接口面上不存在"指定拥有者"的形状**：所有操作
// 的目标工程都必须是调用者自己的，而调用者由凭证决定。
//
// 四条贯穿本包的不变量：
//
//   - **版本不可变**：一个版本保存后逐字不变，且不因别处变化而变化（version.go）。
//   - **库只存清单，不存字节**：文件组落到库里是一组「路径 → 内容摘要或资产
//     标识」，字节一律在对象存储、按内容摘要共享（content_set.go）。
//   - **清单是引用集合的唯一信源**：一个版本引用了哪些资产由它的资产条目直接
//     读出，不解析任何文本（content_set.go 的 Manifest.Assets）。
//   - **发布物是一组文本产物**：只引用本文件组里的条目，交付时附带内容安全策略
//     响应头（publish.go、csp.go）。
package galaxy

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"sort"
	"time"

	"go.uber.org/zap"

	"github.com/poetlife/aladdin/internal/idgen"
	"github.com/poetlife/aladdin/internal/objectstore"
	"github.com/poetlife/aladdin/internal/watch"
)

// 字段长度上限，按**字符数**而不是字节数计：用户感知的长度是字数，按字节算
// 会让"一个中文名称写到十几个字就被拒"成为一条需要解释的规则。
const (
	// ProjectNameMaxRunes 是工程名称的长度上限。
	ProjectNameMaxRunes = 64
	// ProjectDescriptionMaxRunes 是工程简介的长度上限。
	ProjectDescriptionMaxRunes = 280
)

var (
	// ErrProjectNotFound 表示工程不存在**或不属于调用者**。
	//
	// 两者刻意共用一个结论：区分它们等于提供一个工程枚举接口——拿着别人的
	// 工程标识反复调用，"不存在"与"无权访问"的差集就能把一个不可猜标识试
	// 出来，而工程标识是发布地址的一部分（见 ownership.go）。
	ErrProjectNotFound = errors.New("工程不存在")

	// ErrDraftNotFound 表示这个工程还没有草稿行。
	//
	// **它不是故障**：草稿行是惰性创建的，本人第一次推送之前就没有这一行，
	// 而"没有这一行"与"有一行但清单为空"在编辑与展示上完全等价。
	ErrDraftNotFound = errors.New("草稿不存在")

	// ErrVersionNotFound 表示这个工程下没有这个版本。
	ErrVersionNotFound = errors.New("版本不存在")

	// ErrPublicationNotFound 表示这个工程当前没有发布物。
	//
	// 未发布、已撤回、工程不存在、工程标识没被猜中——四者返回同一个结论。
	// 区分它们等于告诉一个猜地址的人"这个标识是真的，只是还没发布"。
	ErrPublicationNotFound = errors.New("发布物不存在")

	// ErrPreviewGrantNotFound 表示库里没有这条预览凭证（或它已经过期被清掉）。
	//
	// 它是**存储层的否定结论**，与"这次预览请求该不该给东西"不是一回事：后者
	// 由 galaxy.ErrPreviewNotFound 回答（见 preview.go）。
	ErrPreviewGrantNotFound = errors.New("预览凭证不存在")

	// ErrProjectNameTooLong 表示工程名称超过长度上限。
	ErrProjectNameTooLong = errors.New("工程名称过长")
	// ErrProjectDescriptionTooLong 表示工程简介超过长度上限。
	ErrProjectDescriptionTooLong = errors.New("工程简介过长")

	// ErrStoreUnavailable 表示 galaxy 的持久化存储不可用。
	//
	// 与"工程不存在"必须分开：前者是故障，后者是正常状态。混为一谈会把一次
	// 数据库抖动表现成"我的工程全没了"。
	ErrStoreUnavailable = errors.New("galaxy 存储不可用")
)

// Project 是一个创作单元的元数据。
//
// 三条与判定无关的约定，都写在类型上：
//
//   - 名称**不是查找键**：不按名称查工程、不加唯一约束、不进发布地址。一旦
//     名称成为查找键，它就成了一条可以被改名或抢注改写的路径。
//   - 元数据**不参与任何判定**：名称与简介都是可改的自由文本，把判定建在
//     它上面等于把一次改名变成一次授权变更。
//   - 拥有者**不可转移**：转移是"把一整个工程连同它的发布地址交给另一个人"，
//     属于资源所有权的变更，首版不做。
type Project struct {
	// ID 由 aladdin 分配，不可猜、不可改、不复用——与主体标识同一条约定。
	ID string
	// OwnerSubjectID 是创建它的主体。归属校验只读这一个字段。
	OwnerSubjectID string
	Name           string
	Description    string
	// Slots 是启用的内容槽，**至少一个**。
	//
	// **槽只增不删**（见 Service.AddProjectSlot）：让历史版本无法复现的从来不是
	// "多了一个槽"，而是"同一个槽换了语义"，因此槽的语义不可变而槽可加。每个槽
	// 各有自己的草稿、版本、发布指针与地址，**两槽互不影响**。
	Slots     []ProjectSlot
	CreatedAt time.Time
	UpdatedAt time.Time
}

// ProjectSlot 是工程的一个内容槽，以及它当前的发布状态。
//
// **它存在即这个槽启用**——"这个工程有哪些内容"只有这一处回答。每个槽的指针
// 各归各的：发布或撤回一个槽不改变另一个槽的任何字段。
type ProjectSlot struct {
	Slot ContentSlot
	// CurrentPublicationID 是**可空的发布指针**：空表示这个槽未发布。
	//
	// 它指向 publication 表里的一条记录，该槽的对外地址按那条记录里的**产物
	// 清单**分派。指针放在槽上（而不是让发布记录反过来标记"我是当前的"），
	// 因为"这个槽当前发布的是哪一个"是槽的一个属性，只有一个写者。
	CurrentPublicationID string
}

// EnabledSlots 按固定顺序返回启用的内容槽。
func (p Project) EnabledSlots() []ContentSlot {
	slots := make([]ContentSlot, 0, len(p.Slots))
	for _, slot := range p.Slots {
		slots = append(slots, slot.Slot)
	}
	sort.Slice(slots, func(i, j int) bool { return slots[i].Order() < slots[j].Order() })
	return slots
}

// FindSlot 取一个槽及其发布状态，未启用时第二个返回值为假。
//
// **"这个槽没启用"与"这个槽未发布"是两件事**，但对外都表现为"这条地址什么都不
// 给"（见 publish.go 的否定结论）。
func (p Project) FindSlot(slot ContentSlot) (ProjectSlot, bool) {
	for _, candidate := range p.Slots {
		if candidate.Slot == slot {
			return candidate, true
		}
	}
	return ProjectSlot{}, false
}

// Draft 是**某个内容槽**当前正在编辑的文件清单。
//
// 它不是版本：随时可改，改它不产生版本，不参与发布，也不保证可退回。
type Draft struct {
	ProjectID string
	Slot      ContentSlot
	// Manifest 只在 GetDraft 里非空。**库里只有清单，没有字节。**
	Manifest Manifest
	// UpdatedAt 为零值表示这个槽还没有草稿行（惰性创建）。
	UpdatedAt time.Time
}

// Version 是文件清单的一次不可变快照。
type Version struct {
	// ID 由 aladdin 分配。
	ID string
	// ProjectID 是所属工程：一个版本属于唯一一个工程。
	ProjectID string
	// Slot 是所属内容槽。**版本按槽隔离**：它只在自己的槽里被列出、发布与撤回。
	Slot ContentSlot
	// Seq 是**槽内**递增的序号，**仅用于展示与排序，不是标识**。
	//
	// 删除一个版本会让后续序号出现空洞，这是可接受的：把序号当标识意味着
	// 删除会波及所有更大的序号，而标识别名化正是这类 bug 的来源。
	Seq int64
	// Manifest 是保存那一刻草稿的清单。**写入后不再修改。**
	//
	// **字节不随清单走**：它们是按内容摘要寻址的不可变对象，由多个版本共享。
	Manifest Manifest
	// Description 是可选的一句说明（"这一版加了什么"）。空表示没有。
	//
	// **它是元数据层，与"版本不可变"不冲突**：不可变说的是 Manifest 与
	// RenderRulesVersion，而说明不进产物、不参与任何判定，因此**可以事后改**
	// （见 UpdateVersionDescription）。写错一句话与"把这一版的内容改掉"是两件事，
	// 而前者若只能靠再存一版来修，人会宁可不写。
	Description string
	// RenderRulesVersion 是保存时所处的渲染规则版本。只有 `docs` 槽使用它，
	// 重新发布时按它渲染而不是按当前最新的（见 doc_render.go）。
	RenderRulesVersion int
	SavedAt            time.Time
}

// Publication 是一次发布产生的对外产物。
type Publication struct {
	// ID 由 aladdin 分配，且**只由工程与版本决定**：同一次发布重试到落库
	// 这一步不会产生第二条记录，也不会换一个标识（见 publish.go 的检查点）。
	ID string
	// ProjectID 与 VersionID 记录这次发布的是哪一个工程的哪一个版本。
	ProjectID string
	VersionID string
	// Slot 是这次发布属于哪个内容槽。
	Slot ContentSlot
	// Manifest 是**产物清单**：路径 → 内容摘要。该槽的对外地址按它分派。
	//
	// 落库而不是每次请求现算：库里存着"当前发布的是什么"这一事实，取用者
	// 只读它，不必重新读文件组、重新查资产、重新渲染。**而清单只有路径与
	// 摘要，没有内容本身**——库因此不随内容增长。
	Manifest Manifest
	// PublishedBySubjectID 与 PublishedAt 是留痕。
	PublishedBySubjectID string
	PublishedAt          time.Time
}

// EntryView 是一条条目加上它在这一刻的短时读取地址。
//
// 地址与条目分开表达：地址每次读取都不同（它是一份会过期的凭证），而条目
// （路径与摘要）是内容自己的属性。
type EntryView struct {
	Entry Entry
	// URL 为空表示取不到地址（桶未配置或签发失败）。此时条目仍然列出。
	URL string
}

// Store 是 galaxy 的持久化抽象（只读部分）。
//
// 它保持"哑"：只读写事实，**不做校验**。体积上限、类型白名单、归属、引用
// 完整性、删除拦阻的唯一入口都在本包的上层。把校验放进存储，内存实现与 SQL
// 实现就会各写一遍，而两份判断迟早会有一份漏掉。
type Store interface {
	// GetProject 返回该工程（含它启用的内容槽），不存在时返回 ErrProjectNotFound。
	GetProject(ctx context.Context, projectID string) (Project, error)

	// ListProjectsByOwner 返回该主体创建的工程（含各自的启用槽），按更新时间
	// 倒序。
	//
	// 这是**唯一**的工程列表入口，范围由拥有者而不是由请求给定：不存在
	// "列出所有工程"的形状。**槽随工程一起返回**，列表因此是一次查询的事，
	// 不是每个工程再查一次。
	ListProjectsByOwner(ctx context.Context, ownerSubjectID string) ([]Project, error)

	// GetDraft 返回该槽的草稿清单，没有草稿行时返回 ErrDraftNotFound。
	GetDraft(ctx context.Context, projectID string, slot ContentSlot) (Draft, error)

	// GetVersion 返回该工程该槽下的一个版本（含清单），不存在时返回
	// ErrVersionNotFound。
	GetVersion(ctx context.Context, projectID string, slot ContentSlot, versionID string) (Version, error)

	// ListVersions 返回该工程**该槽**的版本，按序号升序。**清单随行返回**：
	// 它只有路径与摘要，几 KB 量级，而"哪些版本引用了这个资产"正是靠它回答的。
	ListVersions(ctx context.Context, projectID string, slot ContentSlot) ([]Version, error)

	// GetAsset 返回该工程下的一个资产，不存在时返回 ErrAssetNotFound。
	GetAsset(ctx context.Context, projectID, assetID string) (Asset, error)

	// ListAssets 返回该工程的资产，按上传时间倒序。
	//
	// tags 非空时按**交集**筛选：只返回同时带这些标签的资产。取值是归一化之后
	// 的形式（见 NormalizeTags）。
	ListAssets(ctx context.Context, projectID string, tags []string) ([]Asset, error)

	// ListProjectTags 返回该工程已有的全部标签，升序。
	//
	// 它是筛选界面的候选来源，因此**不受任何筛选影响**：筛出来的那几个不该
	// 把候选集缩小。
	ListProjectTags(ctx context.Context, projectID string) ([]string, error)

	// ListDraftSnapshots 返回该工程**该槽**的草稿历史，最近的在前。
	//
	// 清单随行返回：它只有路径与摘要，而"这一份与上一份差在哪"要靠它答。
	ListDraftSnapshots(ctx context.Context, projectID string, slot ContentSlot) ([]DraftSnapshot, error)

	// GetDraftSnapshot 按标识读回一条草稿快照，不存在（或已过期被清掉）时返回
	// ErrDraftSnapshotNotFound。
	GetDraftSnapshot(ctx context.Context, projectID string, slot ContentSlot, snapshotID string) (DraftSnapshot, error)

	// GetPublication 按发布标识读回发布记录（含产物清单）。
	GetPublication(ctx context.Context, publicationID string) (Publication, error)

	// GetPreviewGrant 按凭证本体读回一条预览凭证，没有时返回
	// ErrPreviewGrantNotFound。
	//
	// **过期不由它判定**：它只回答"库里有没有这一条"，过期与否由调用方比较
	// 时间（见 PreviewGrant.Expired）。把判定放在读里会让"这条凭证此刻算不算
	// 数"多出第二种说法。
	GetPreviewGrant(ctx context.Context, token string) (PreviewGrant, error)
}

// MutableStore 是 Store 的写能力。
//
// 与判定路径的只读约束同源：写能力单独成一个接口，从类型上排除"读取过程中
// 顺手改数据"。
type MutableStore interface {
	Store

	// CreateProject 写入一个新工程，**连同它启用的内容槽**（标识、拥有者与槽
	// 由调用方给定）。
	//
	// 工程行与槽行在同一个事务里落库：先写工程再补槽会留下一个"没有内容的工程"
	// 的中间状态，而"至少一个槽"是工程能成立的前提。
	CreateProject(ctx context.Context, project Project) error

	// PutProjectMeta 覆盖工程的名称与简介，并更新更新时间。
	// 它**不动**发布指针，也**不动内容槽**：一次改名不该影响发布态。
	PutProjectMeta(ctx context.Context, projectID, name, description string, at time.Time) error

	// AddProjectSlot 给工程加一个槽。**槽已经启用时返回 ErrSlotEnabled**。
	//
	// 它只写槽行，不碰另一个槽的任何东西（也没有"删槽"的对应方法）。
	AddProjectSlot(ctx context.Context, projectID string, slot ContentSlot) error

	// SetCurrentPublication 写入或清除**某一个槽**的发布指针。publicationID
	// 为空表示撤回。
	//
	// 切换是幂等的：把指针指向同一条发布记录不改变任何对外可见的结果。
	// **它只碰这一个槽**，另一个槽的指针原样。
	SetCurrentPublication(ctx context.Context, projectID string, slot ContentSlot, publicationID string, at time.Time) error

	// DeleteProject 删除工程**连同它的全部内容槽、版本、资产与发布记录**。
	//
	// 它只删库内的行。私有区与公开区的对象由上层分别处理：私有的要删（否则
	// 成为无从被引用的孤儿），公开的不回收（见 publish.go）。
	DeleteProject(ctx context.Context, projectID string) error

	// PutDraft 整组替换**某一个槽**的草稿，行不存在时创建，并**在同一步**把被
	// 替换掉的那份清单记成一条草稿快照。
	//
	// **它整组读写**：保存草稿表达的是完整状态，不是增量。
	//
	// 两件事必须落在同一步：先读旧清单、再写新清单，中间隔着一次返回——中途失败
	// 留下的是"新草稿已经生效、旧的那份没有任何记录"，而那正是快照要消掉的东西。
	//
	// snapshot 为**零值**（ID 为空）表示这次不留快照（这个槽还没有草稿行，或新旧
	// 清单完全相同，见 Service.recordReplacedDraft）。retention 给出保留策略，
	// 清理与写入在同一个事务里——表的大小因此只由"还有多少条活的快照"决定。
	PutDraft(ctx context.Context, projectID string, slot ContentSlot, manifest Manifest, at time.Time,
		snapshot DraftSnapshot, retention DraftSnapshotRetention) error

	// CreateVersion 写入一个版本，并**在该槽内分配序号**后返回落库的版本。
	//
	// 序号由存储分配而不是由调用方计算：先查最大值再写入在并发下会得到两个
	// 相同的序号，而"查到了什么"与"写进去了什么"必须在同一处发生。**序号在
	// 槽内递增**：两个槽各自的第一个版本序号都是 1。
	CreateVersion(ctx context.Context, version Version) (Version, error)

	// UpdateVersionDescription 覆盖一个版本的说明。
	//
	// **它只动说明那一列**：清单、序号、保存时间与渲染规则版本逐字不变，因此
	// "版本不可变"（说的是内容）不受影响，已发布页面也不受影响。版本不存在时
	// 返回 ErrVersionNotFound。
	UpdateVersionDescription(ctx context.Context, projectID string, slot ContentSlot, versionID, description string) error

	// DeleteVersion 删除**某一个槽**的一个版本。
	DeleteVersion(ctx context.Context, projectID string, slot ContentSlot, versionID string) error

	// CreateAsset 写入一个资产（标识、摘要与媒体类型由调用方给定）。
	//
	// 它连同这个资产的标签一起落库（一个事务）：标签是资产的一部分，先写行
	// 再补标签会留下一个"标签还没到"的中间状态。
	CreateAsset(ctx context.Context, asset Asset) error

	// UpdateAssetMeta 覆盖一个资产的说明层元数据（标题、备注与标签）。
	//
	// **它只动说明层**：内容摘要、媒体类型、类别与字节数逐字不变（见
	// docs/design/galaxy/asset-library.md）。标签是**整体替换**——不在给定
	// 集合里的就是删掉。资产不存在时返回 ErrAssetNotFound。
	UpdateAssetMeta(ctx context.Context, projectID, assetID, title, notes string, tags []string) error

	// DeleteAsset 删除一个资产的元数据行**连同它的标签**。
	DeleteAsset(ctx context.Context, projectID, assetID string) error

	// PutPublication 写入一条发布记录；同一条记录重复写入不产生第二条。
	PutPublication(ctx context.Context, publication Publication) error

	// PutPreviewGrant 写入一条预览凭证，并**在同一个事务里**清掉该工程下在
	// cleanupBefore 之前失效的凭证。
	//
	// 清理挂在写入这一处而不是单开一个接口：表的增长于是只由"还有多少条活着的
	// 凭证"决定，而预览请求（读的那条路）是只读的；挂在读上会让一次取图顺带写库。
	//
	// 失效时刻由调用方给出，不由存储自己取当前时间：判断留在上层，存储只读写
	// 事实（与"存储保持哑"同源），这条清理因此可测。
	PutPreviewGrant(ctx context.Context, grant PreviewGrant, cleanupBefore time.Time) error
}

// 标识前缀。**唯一入口**是本文件的分配函数：工程标识与资产标识由创建入口
// 分配，分配即冻结、永不复用——与主体标识同一条约定。
//
// 随机部分从哪来、取多少熵由 `idgen` 决定（那里是全部标识分配的公共入口），
// 这里只声明"哪一类标识长什么前缀"。
const (
	projectIDPrefix     = "prj_"
	versionIDPrefix     = "ver_"
	assetIDPrefix       = "ast_"
	publicationIDPrefix = "pub_"
)

// NewProjectID 分配一个工程标识（唯一入口）。
func NewProjectID() (string, error) { return idgen.New(projectIDPrefix) }

// newVersionID 分配一个版本标识（唯一入口）。
func newVersionID() (string, error) { return idgen.New(versionIDPrefix) }

// newAssetID 分配一个资产标识（唯一入口）。
func newAssetID() (string, error) { return idgen.New(assetIDPrefix) }

// PublicationID 返回（工程，版本）确定的那条发布记录的标识（唯一入口）。
//
// 它由两个不可猜的标识派生，因此同样不可猜；而"确定"是检查点的要求——
// 落库之后、切换之前中断的那一次发布重试时，必须落在**同一条**记录上，
// 否则同一次发布会产生第二条记录（见 publish.go 的"检查点与恢复"）。
func PublicationID(projectID, versionID string) string {
	sum := sha256.Sum256([]byte(projectID + "\x00" + versionID))
	return publicationIDPrefix + base64.RawURLEncoding.EncodeToString(sum[:idgen.EntropyBytes])
}

// Capabilities 是当前部署下创作能力的边界。
//
// 它是**能力下发点**：能力由服务端说，客户端不猜。两个布尔项是部署形态的
// 公开事实，不因调用者而异。
type Capabilities struct {
	// AssetUploadEnabled 为假时不渲染上传入口与资产区。**它同时是内容的
	// 前提**：桶是字节唯一能放的地方，没有桶就没有草稿、没有版本。
	AssetUploadEnabled bool
	// PublishEnabled 为假时不渲染发布入口。
	PublishEnabled bool
	// PreviewEnabled 为假时不渲染预览：预览走发布域上的一条通道，没有发布域的
	// 部署就没有预览（见 preview.go 的 previewEnabled）。
	PreviewEnabled bool
	// MaxTextBytes / MaxFileSetBytes / MaxFiles 是文本的单份、整组与数量上限。
	MaxTextBytes    int64
	MaxFileSetBytes int64
	MaxFiles        int64
	AssetLimits     []AssetKindLimit
}

// Deps 是构造 Service 所需的取值。
type Deps struct {
	// Store 是持久化存储。必填。
	Store MutableStore
	// Assets 是私有区对象存储的**直传**入口（见 internal/objectstore）。
	//
	// **为 nil 表示这个部署没有配置桶**：资产、**内容（草稿与版本）**与发布
	// 整体缺席，而工程元数据照常可用。缺席由 nil 表达，而不是由一个"什么都
	// 存不下"的实现表达——后者会让一次配置缺失在运行时表现成一次存储故障。
	Assets objectstore.Store
	// Public 是公开区的写入口。**为 nil 表示没有配置发布**：发布不可用。
	// 它写的是与 Assets 同一个桶，区别在写下去的对象权限（公开读）。
	Public PublicStore
	// Origin 是发布态地址的派生入口。零值表示没有配置发布域。
	Origin PublicOrigin
	// Events 是工程状态变化的总线：每一次成功的写入往里发一条小事件，供已经
	// 打开的工作台看见别处（命令行、另一个标签页）的改动（见
	// docs/design/events/README.md）。
	//
	// **为 nil 表示不推送**。它不影响任何写入路径的正确性——事件是提示不是事实，
	// 订阅方收到之后自己去读现状。
	Events *watch.Hub
	// Logger 可以为 nil（测试），此时不产出留痕。
	Logger *zap.Logger
	// Now 可以为 nil，默认 time.Now。
	Now func() time.Time
}

// Service 是本模块的用例层：工程、草稿、版本、资产、校验与发布。
type Service struct {
	store  MutableStore
	assets objectstore.Store
	public PublicStore
	origin PublicOrigin
	events *watch.Hub
	logger *zap.Logger
	now    func() time.Time
}

// NewService 构造用例层。
func NewService(deps Deps) *Service {
	now := deps.Now
	if now == nil {
		now = time.Now
	}
	return &Service{
		store:  deps.Store,
		assets: deps.Assets,
		public: deps.Public,
		origin: deps.Origin,
		events: deps.Events,
		logger: deps.Logger,
		now:    now,
	}
}

// Capabilities 汇报部署形态的边界。
func (s *Service) Capabilities() Capabilities {
	return Capabilities{
		AssetUploadEnabled: s.assets != nil,
		PublishEnabled:     s.publishEnabled(),
		PreviewEnabled:     s.previewEnabled(),
		MaxTextBytes:       MaxTextBytes,
		MaxFileSetBytes:    MaxFileSetBytes,
		MaxFiles:           MaxFiles,
		AssetLimits:        AssetKindLimits(),
	}
}

// publishEnabled 是"发布这条路在不在"的**唯一判据**。
//
// 两个前提合起来才成立：有公开区的写入口，且知道发布态的地址从哪派生。缺任
// 一条时这条路径整体缺席，而不是发起得了、交付不了。
func (s *Service) publishEnabled() bool {
	return s.public != nil && !s.origin.IsZero()
}

// CreateProject 创建一个工程（工程标识的分配入口）。
//
// slots 是创建时要启用的内容槽，**至少一个、不重复**。
func (s *Service) CreateProject(ctx context.Context, ownerSubjectID, name, description string, slots []ContentSlot) (Project, error) {
	if err := validateProjectMeta(name, description); err != nil {
		return Project{}, err
	}
	enabled, err := normalizeSlots(slots)
	if err != nil {
		return Project{}, err
	}
	id, err := NewProjectID()
	if err != nil {
		return Project{}, err
	}
	now := s.now()
	project := Project{
		ID:             id,
		OwnerSubjectID: ownerSubjectID,
		Name:           name,
		Description:    description,
		Slots:          enabled,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := s.store.CreateProject(ctx, project); err != nil {
		return Project{}, err
	}
	// 建立工程**不发事件**：订阅要给出一个工程标识，而这个标识刚刚才存在，
	// 没有人可能订着它。给它发一条是无人可收的死代码。
	if s.logger != nil {
		s.logger.Info("已创建工程",
			zap.String("project_id", project.ID),
			zap.String("subject_id", ownerSubjectID),
			zap.Strings("slots", slotStrings(project.EnabledSlots())))
	}
	return project, nil
}

// AddProjectSlot 给一个已有工程加一个内容槽。
//
// **单向**：没有"删掉一个槽"的对应方法——槽的语义与地址是固定的，已有的版本与
// 地址都挂在它上面（见 docs/design/galaxy/site-model.md）。
//
// **加一个槽不改变另一个槽**：那个槽的草稿、版本与发布指针都原样。但有一条要
// 先挡住——文档槽占住 `docs` 这个首段，因此**站点槽里已经存在这类路径时不允许
// 加文档槽**：否则那些路径会从"site 槽里的一份文件"变成"文档槽的地址"，而它们
// 已经发布出去了。这里如实拒绝并指出是哪一条，而不是静默让它们失效。
func (s *Service) AddProjectSlot(ctx context.Context, subjectID, projectID string, slot ContentSlot) (Project, error) {
	if !slot.IsValid() {
		return Project{}, fmt.Errorf("%w: %q", ErrContentSlotInvalid, slot)
	}
	project, err := OwnedProject(ctx, s.store, projectID, subjectID)
	if err != nil {
		return Project{}, err
	}
	if _, ok := project.FindSlot(slot); ok {
		return Project{}, fmt.Errorf("%w: %q", ErrSlotEnabled, slot)
	}
	if slot == SlotDocs {
		if err := s.ensureNoReservedPaths(ctx, projectID); err != nil {
			return Project{}, err
		}
	}
	if err := s.store.AddProjectSlot(ctx, projectID, slot); err != nil {
		return Project{}, err
	}
	s.publish(projectID)
	if s.logger != nil {
		s.logger.Info("已加入内容槽",
			zap.String("project_id", projectID),
			zap.String("subject_id", subjectID),
			zap.String("slot", string(slot)))
	}
	return s.store.GetProject(ctx, projectID)
}

// ensureNoReservedPaths 检查站点槽的草稿与全部版本里有没有占用保留段的路径。
//
// 它是"加文档槽"的前置：站点槽里的一份 `docs/...` 一旦发布过，加上文档槽就会
// 让那条地址改指别处。查的范围是**草稿与每一个版本**——版本是不可变的历史，
// 它同样挂着地址。
func (s *Service) ensureNoReservedPaths(ctx context.Context, projectID string) error {
	manifests := make([]Manifest, 0, 1)
	if draft, err := s.store.GetDraft(ctx, projectID, SlotSite); err == nil {
		manifests = append(manifests, draft.Manifest)
	} else if !errors.Is(err, ErrDraftNotFound) {
		return err
	}
	versions, err := s.store.ListVersions(ctx, projectID, SlotSite)
	if err != nil {
		return err
	}
	for _, version := range versions {
		manifests = append(manifests, version.Manifest)
	}
	for _, manifest := range manifests {
		for _, entry := range manifest {
			if err := ValidateSlotPath(SlotSite, entry.Path); err != nil {
				return fmt.Errorf("%w: 站点槽里已有 %q，它会让文档槽的地址改指别处",
					ErrReservedPath, entry.Path)
			}
		}
	}
	return nil
}

// normalizeSlots 校验并规范化创建时给出的内容槽（唯一入口）。
func normalizeSlots(slots []ContentSlot) ([]ProjectSlot, error) {
	if len(slots) == 0 {
		return nil, fmt.Errorf("%w: 至少选一个内容槽", ErrContentSlotInvalid)
	}
	seen := make(map[ContentSlot]bool, len(slots))
	enabled := make([]ProjectSlot, 0, len(slots))
	for _, slot := range slots {
		if !slot.IsValid() {
			return nil, fmt.Errorf("%w: %q", ErrContentSlotInvalid, slot)
		}
		if seen[slot] {
			return nil, fmt.Errorf("%w: %q 重复", ErrContentSlotInvalid, slot)
		}
		seen[slot] = true
		enabled = append(enabled, ProjectSlot{Slot: slot})
	}
	sort.Slice(enabled, func(i, j int) bool { return enabled[i].Slot.Order() < enabled[j].Slot.Order() })
	return enabled, nil
}

// slotStrings 把内容槽转成留痕用的字符串。
func slotStrings(slots []ContentSlot) []string {
	out := make([]string, 0, len(slots))
	for _, slot := range slots {
		out = append(out, string(slot))
	}
	return out
}

// ownedProjectSlot 取一个工程，要求调用者是拥有者、**且该槽已启用**（唯一入口）。
//
// **"这个槽没启用"与"这个工程不是你的"给同一个结论**：区分它们等于告诉调用者
// "这个工程是真的，只是没有这个槽"——与"不是你的与不存在同结论"是同一条取向
// （见 ownership.go）。
func (s *Service) ownedProjectSlot(ctx context.Context, subjectID, projectID string, slot ContentSlot) (Project, error) {
	project, err := OwnedProject(ctx, s.store, projectID, subjectID)
	if err != nil {
		return Project{}, err
	}
	if _, ok := project.FindSlot(slot); !ok {
		return Project{}, ErrProjectNotFound
	}
	return project, nil
}

// GetProject 读取一个工程，要求调用者是它的拥有者。
func (s *Service) GetProject(ctx context.Context, subjectID, projectID string) (Project, error) {
	return OwnedProject(ctx, s.store, projectID, subjectID)
}

// ListProjects 列出调用者自己的工程。
func (s *Service) ListProjects(ctx context.Context, subjectID string) ([]Project, error) {
	return s.store.ListProjectsByOwner(ctx, subjectID)
}

// UpdateProject 覆盖工程的名称与简介。
func (s *Service) UpdateProject(ctx context.Context, subjectID, projectID, name, description string) (Project, error) {
	if err := validateProjectMeta(name, description); err != nil {
		return Project{}, err
	}
	if _, err := OwnedProject(ctx, s.store, projectID, subjectID); err != nil {
		return Project{}, err
	}
	if err := s.store.PutProjectMeta(ctx, projectID, name, description, s.now()); err != nil {
		return Project{}, err
	}
	// 写入已经成功，事件就发出去：后面那次读取失败不改变"元数据已变"这个事实，
	// 而少发一条事件会让订阅者停在旧内容上。
	s.publish(projectID)
	return s.store.GetProject(ctx, projectID)
}

// DeleteProject 删除一个工程，并把它的私有区对象（资产与内容）一并清掉。
//
// 已发布的地址立刻变成"不存在"（发布记录随工程一起删）；公开区的副本不回收
// ——它是一份独立对象，召回它需要一次对账，属于另一个职责。
func (s *Service) DeleteProject(ctx context.Context, subjectID, projectID string) error {
	project, err := OwnedProject(ctx, s.store, projectID, subjectID)
	if err != nil {
		return err
	}
	assets, err := s.store.ListAssets(ctx, projectID, nil)
	if err != nil {
		return err
	}
	if err := s.store.DeleteProject(ctx, projectID); err != nil {
		return err
	}
	// 工程不存在了：发一条事件并结束它的全部订阅（见 events.go 的顺序说明）。
	s.publishDeleted(projectID)
	// 对象删除失败不影响"工程已删除"这一结论：库内是权威，桶上可能因此留下
	// 无从被引用的孤儿对象，而它没有功能影响（与头像同源）。
	s.deleteAssetObjects(ctx, projectID, assets, "删除工程")
	if s.logger != nil {
		s.logger.Info("已删除工程",
			zap.String("project_id", projectID),
			zap.String("subject_id", subjectID),
			zap.Strings("slots", slotStrings(project.EnabledSlots())),
			zap.Int("assets", len(assets)))
	}
	return nil
}

// GetDraft 读取**某一个槽**的当前草稿清单，并给每一项附上短时读取地址。
//
// 没有草稿行时返回一份空清单，而不是错误：惰性创建的行与"推送过一次空清单"
// 在编辑上完全等价，而把"还没推过"表现成一个错误会让编辑器在每个新工程上先
// 显示一次失败。
func (s *Service) GetDraft(ctx context.Context, subjectID, projectID string, slot ContentSlot) (Draft, []EntryView, error) {
	if _, err := s.ownedProjectSlot(ctx, subjectID, projectID, slot); err != nil {
		return Draft{}, nil, err
	}
	draft, err := s.store.GetDraft(ctx, projectID, slot)
	if errors.Is(err, ErrDraftNotFound) {
		return Draft{ProjectID: projectID, Slot: slot}, nil, nil
	}
	if err != nil {
		return Draft{}, nil, err
	}
	return draft, s.attachURLs(ctx, projectID, draft.Manifest), nil
}

// PushDraft 以给定的清单**整组替换某一个槽的草稿**（它不产生版本）。
//
// 请求表达的是完整状态而不是增量：清单里没有的路径就是"删掉"。因此写入形状
// 只有"整组"一种，两个入口并存会引出的那类覆盖冲突（网页上刚改的一句被一次
// push 静默盖掉）连同它需要的基线校验一起不存在。
//
// **它只碰这一个槽**：另一个槽的草稿与版本不受影响。
//
// **被换掉的那份清单会留成一条草稿快照**（见 docs/design/galaxy/project-versioning.md）：
// "只推不存版本"这条最常见的用法因此不会让中间过程消失。source 是发起这一端的
// 标识（`web` / `cli`），由服务端从上报端标识那一个入口读出。
func (s *Service) PushDraft(ctx context.Context, subjectID, projectID string, slot ContentSlot, entries []Entry, source string) (Draft, error) {
	if _, err := s.ownedProjectSlot(ctx, subjectID, projectID, slot); err != nil {
		return Draft{}, err
	}
	manifest, err := NormalizeManifest(entries)
	if err != nil {
		return Draft{}, err
	}
	if err := ValidateManifestForSlot(slot, manifest); err != nil {
		return Draft{}, err
	}
	draft, err := s.replaceDraft(ctx, subjectID, projectID, slot, manifest, source)
	if err != nil {
		return Draft{}, err
	}
	s.publish(projectID)
	if s.logger != nil {
		s.logger.Info("已整组替换草稿",
			zap.String("project_id", projectID),
			zap.String("subject_id", subjectID),
			zap.String("slot", string(slot)),
			zap.Int("files", len(manifest)))
	}
	return draft, nil
}

// attachURLs 给清单的每一项附上编辑态的短时读取地址。
//
// 签发失败只留痕、不返错：清单本身仍然有意义，而一个取不到地址的条目的表现
// 形式是"这一份暂时打不开"，与头像过期后的表现同源。
func (s *Service) attachURLs(ctx context.Context, projectID string, manifest Manifest) []EntryView {
	if s.assets == nil {
		return nil
	}
	views := make([]EntryView, 0, len(manifest))
	for _, entry := range manifest {
		url, err := s.presignEntry(ctx, projectID, entry)
		if err != nil {
			if s.logger != nil {
				s.logger.Warn("签发条目读取地址失败",
					zap.String("project_id", projectID),
					zap.String("path", entry.Path),
					zap.Error(err))
			}
			views = append(views, EntryView{Entry: entry})
			continue
		}
		views = append(views, EntryView{Entry: entry, URL: url})
	}
	return views
}

// presignEntry 为一个条目签发短时读取地址（唯一入口）。
func (s *Service) presignEntry(ctx context.Context, projectID string, entry Entry) (string, error) {
	if s.assets == nil {
		return "", ErrAssetUnavailable
	}
	if entry.Kind == EntryKindAsset {
		asset, err := s.assetOfProject(ctx, projectID, entry.AssetID)
		if err != nil {
			return "", err
		}
		return s.presignAsset(ctx, asset)
	}
	return s.assets.PresignGet(ctx, ContentObjectKey(projectID, entry.Digest), AssetURLTTL)
}

// validateProjectMeta 是名称与简介长度的唯一入口。
func validateProjectMeta(name, description string) error {
	if len([]rune(name)) > ProjectNameMaxRunes {
		return fmt.Errorf("%w: 上限 %d 个字符", ErrProjectNameTooLong, ProjectNameMaxRunes)
	}
	if len([]rune(description)) > ProjectDescriptionMaxRunes {
		return fmt.Errorf("%w: 上限 %d 个字符", ErrProjectDescriptionTooLong, ProjectDescriptionMaxRunes)
	}
	return nil
}

// deleteAssetObjects 尽力删除一批资产的私有区对象。
//
// 它刻意不返回错误：库内的元数据行是"存在哪些资产"的权威，对象删除失败只会
// 留下一个无从被引用的孤儿对象。把这件事升级成一次失败，会让"工程已经删不掉
// 了"变成一个由存储抖动决定的结果。
//
// **一次交下来而不是逐个删**：一个工程可能有几十上百个资产，逐个删在跨境链路
// 上就是每次都付一次往返（见 issue #33 的实测）。批量是存储实现的事，这里只管
// 把键凑齐。
func (s *Service) deleteAssetObjects(ctx context.Context, projectID string, assets []Asset, action string) {
	if s.assets == nil || len(assets) == 0 {
		return
	}
	keys := make([]string, 0, len(assets))
	for _, asset := range assets {
		keys = append(keys, AssetObjectKey(projectID, asset.MediaKind, asset.ID))
	}
	if err := s.assets.DeleteMany(ctx, keys); err != nil && s.logger != nil {
		s.logger.Warn("删除资产对象失败，元数据已清空",
			zap.String("action", action),
			zap.String("project_id", projectID),
			zap.Int("assets", len(keys)),
			zap.Error(err))
	}
}
