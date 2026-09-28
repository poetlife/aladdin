// Package galaxy 是"用户写一份 HTML 并把它发布出去"这一能力的唯一实现。
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
// 三条贯穿本包的不变量：
//
//   - **版本不可变**：一个版本保存后逐字不变，且不因别处变化而变化（version.go）。
//   - **正文是引用集合的唯一信源**：一个版本引用了哪些资产由它的正文决定，
//     不另存一份清单（placeholder.go 是识别入口）。
//   - **发布物恒为一个 HTML 文档**，只引用本工程资产，且交付时附带内容安全
//     策略响应头（publish.go、csp.go）。
package galaxy

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"go.uber.org/zap"

	"github.com/poetlife/aladdin/internal/objectstore"
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
	// **它不是故障**：草稿行是惰性创建的，本人第一次保存之前就没有这一行，
	// 而"没有这一行"与"有一行但正文为空"在编辑与展示上完全等价。
	ErrDraftNotFound = errors.New("草稿不存在")

	// ErrVersionNotFound 表示这个工程下没有这个版本。
	ErrVersionNotFound = errors.New("版本不存在")

	// ErrPublicationNotFound 表示这个工程当前没有发布物。
	//
	// 未发布、已撤回、工程不存在、工程标识没被猜中——四者返回同一个结论。
	// 区分它们等于告诉一个猜地址的人"这个标识是真的，只是还没发布"。
	ErrPublicationNotFound = errors.New("发布物不存在")

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
	CreatedAt      time.Time
	UpdatedAt      time.Time
	// CurrentPublicationID 是**可空的发布指针**：空表示未发布。
	//
	// 它指向 publication 表里的一条记录，对外地址返回的是那条记录里的产物。
	// 指针放在工程行上（而不是让发布记录反过来标记"我是当前的"），因为
	// "当前发布的是哪一个"是工程的一个属性，只有一个写者。
	CurrentPublicationID string
}

// Draft 是工程当前正在编辑的正文。
//
// 它不是版本：随时可改，改它不产生版本，不参与发布，也不保证可退回。
type Draft struct {
	ProjectID string
	Content   Document
	// UpdatedAt 为零值表示这个工程还没有草稿行（惰性创建）。
	UpdatedAt time.Time
}

// Version 是正文的一次不可变快照。
type Version struct {
	// ID 由 aladdin 分配。
	ID string
	// ProjectID 是所属工程：一个版本属于唯一一个工程。
	ProjectID string
	// Seq 是工程内递增的序号，**仅用于展示与排序，不是标识**。
	//
	// 删除一个版本会让后续序号出现空洞，这是可接受的：把序号当标识意味着
	// 删除会波及所有更大的序号，而标识别名化正是这类 bug 的来源。
	Seq int64
	// Content 是保存那一刻草稿的内容（写入后不再修改）。
	Content Document
	SavedAt time.Time
}

// Publication 是一次发布产生的对外产物。
type Publication struct {
	// ID 由 aladdin 分配，且**只由工程与版本决定**：同一次发布重试到落库
	// 这一步不会产生第二条记录，也不会换一个标识（见 publish.go 的检查点）。
	ID string
	// ProjectID 与 VersionID 记录这次发布的是哪一个工程的哪一个版本。
	ProjectID string
	VersionID string
	// Content 是**改写后的 HTML**。这是对外地址真正返回的东西。
	//
	// 落库而不是每次请求现算：库里存着"当前发布的是什么"这一事实，取用者
	// 只读它，不必重新解析正文、重新查资产。
	Content Document
	// PublishedBySubjectID 与 PublishedAt 是留痕。
	PublishedBySubjectID string
	PublishedAt          time.Time
}

// Store 是 galaxy 的持久化抽象（只读部分）。
//
// 它保持"哑"：只读写事实，**不做校验**。体积上限、类型白名单、归属、引用
// 完整性、删除拦阻的唯一入口都在本包的上层。把校验放进存储，内存实现与 SQL
// 实现就会各写一遍，而两份判断迟早会有一份漏掉。
type Store interface {
	// GetProject 返回该工程，不存在时返回 ErrProjectNotFound。
	GetProject(ctx context.Context, projectID string) (Project, error)

	// ListProjectsByOwner 返回该主体创建的工程，按更新时间倒序。
	//
	// 这是**唯一**的工程列表入口，范围由拥有者而不是由请求给定：不存在
	// "列出所有工程"的形状。
	ListProjectsByOwner(ctx context.Context, ownerSubjectID string) ([]Project, error)

	// GetDraft 返回该工程的草稿，没有草稿行时返回 ErrDraftNotFound。
	GetDraft(ctx context.Context, projectID string) (Draft, error)

	// GetVersion 返回该工程下的一个版本（含正文），不存在时返回
	// ErrVersionNotFound。
	GetVersion(ctx context.Context, projectID, versionID string) (Version, error)

	// ListVersions 返回该工程的版本元数据，按序号升序。**不带正文**：
	// 版本正文可能有几百 KB，列表接口不该把它一起读上来。
	ListVersions(ctx context.Context, projectID string) ([]Version, error)

	// ListVersionContents 返回该工程所有版本的正文。
	//
	// 它与 ListVersions 分开是刻意的：只有"哪些版本引用了这个资产"这一个
	// 判断需要扫全部正文（见 version.go），而它一口气把大字段读出来的代价
	// 应当由需要它的人承担。
	ListVersionContents(ctx context.Context, projectID string) ([]Version, error)

	// GetAsset 返回该工程下的一个资产，不存在时返回 ErrAssetNotFound。
	GetAsset(ctx context.Context, projectID, assetID string) (Asset, error)

	// ListAssets 返回该工程的资产，按上传时间倒序。
	ListAssets(ctx context.Context, projectID string) ([]Asset, error)

	// GetPublication 按发布标识读回发布记录（含产物正文）。
	GetPublication(ctx context.Context, publicationID string) (Publication, error)
}

// MutableStore 是 Store 的写能力。
//
// 与判定路径的只读约束同源：写能力单独成一个接口，从类型上排除"读取过程中
// 顺手改数据"。
type MutableStore interface {
	Store

	// CreateProject 写入一个新工程（标识与拥有者由调用方给定）。
	CreateProject(ctx context.Context, project Project) error

	// PutProjectMeta 覆盖工程的名称与简介，并更新更新时间。
	// 它**不动**发布指针：一次改名不该影响发布态。
	PutProjectMeta(ctx context.Context, projectID, name, description string, at time.Time) error

	// SetCurrentPublication 写入或清除发布指针。publicationID 为空表示撤回。
	//
	// 切换是幂等的：把指针指向同一条发布记录不改变任何对外可见的结果。
	SetCurrentPublication(ctx context.Context, projectID, publicationID string, at time.Time) error

	// DeleteProject 删除工程**连同它的全部版本、资产与发布记录**。
	//
	// 它只删库内的行。私有区与公开区的对象由上层分别处理：私有的要删（否则
	// 成为无从被引用的孤儿），公开的不回收（见 publish.go）。
	DeleteProject(ctx context.Context, projectID string) error

	// PutDraft 写入或覆盖草稿，行不存在时创建。
	PutDraft(ctx context.Context, projectID string, content Document, at time.Time) error

	// CreateVersion 写入一个版本，并**在工程内分配序号**后返回落库的版本。
	//
	// 序号由存储分配而不是由调用方计算：先查最大值再写入在并发下会得到两个
	// 相同的序号，而"查到了什么"与"写进去了什么"必须在同一处发生。
	CreateVersion(ctx context.Context, version Version) (Version, error)

	// DeleteVersion 删除一个版本。
	DeleteVersion(ctx context.Context, projectID, versionID string) error

	// CreateAsset 写入一个资产（标识、摘要与媒体类型由调用方给定）。
	CreateAsset(ctx context.Context, asset Asset) error

	// DeleteAsset 删除一个资产的元数据行。
	DeleteAsset(ctx context.Context, projectID, assetID string) error

	// PutPublication 写入一条发布记录；同一条记录重复写入不产生第二条。
	PutPublication(ctx context.Context, publication Publication) error
}

// 标识前缀。**唯一入口**是本文件的分配函数：工程标识与资产标识由创建入口
// 分配，分配即冻结、永不复用——与主体标识同一条约定。
const (
	projectIDPrefix     = "prj_"
	versionIDPrefix     = "ver_"
	assetIDPrefix       = "ast_"
	publicationIDPrefix = "pub_"

	// idEntropyBytes 是标识里随机部分的字节数。
	//
	// 128 位不可猜：工程标识是发布地址的一部分，而发布态是公开匿名的——
	// "地址即凭据"的全部强度都落在这几个字节上。
	idEntropyBytes = 16
)

// NewProjectID 分配一个工程标识（唯一入口）。
func NewProjectID() (string, error) { return newID(projectIDPrefix) }

// newVersionID 分配一个版本标识（唯一入口）。
func newVersionID() (string, error) { return newID(versionIDPrefix) }

// newAssetID 分配一个资产标识（唯一入口）。
func newAssetID() (string, error) { return newID(assetIDPrefix) }

// newID 从密码学随机源取一段熵并编码。
//
// 用 base64url 而不是十六进制：同样的字节数下它更短，而标识会出现在发布地址
// 里，短一点对所有人可读。
func newID(prefix string) (string, error) {
	buf := make([]byte, idEntropyBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("分配标识失败: %w", err)
	}
	return prefix + base64.RawURLEncoding.EncodeToString(buf), nil
}

// PublicationID 返回（工程，版本）确定的那条发布记录的标识（唯一入口）。
//
// 它由两个不可猜的标识派生，因此同样不可猜；而"确定"是检查点的要求——
// 落库之后、切换之前中断的那一次发布重试时，必须落在**同一条**记录上，
// 否则同一次发布会产生第二条记录（见 publish.go 的"检查点与恢复"）。
func PublicationID(projectID, versionID string) string {
	sum := sha256.Sum256([]byte(projectID + "\x00" + versionID))
	return publicationIDPrefix + base64.RawURLEncoding.EncodeToString(sum[:idEntropyBytes])
}

// Capabilities 是当前部署下创作能力的边界。
//
// 它是**能力下发点**：能力由服务端说，客户端不猜。两个布尔项是部署形态的
// 公开事实，不因调用者而异。
type Capabilities struct {
	// AssetUploadEnabled 为假时不渲染上传入口与资产区。
	AssetUploadEnabled bool
	// PublishEnabled 为假时不渲染发布入口。
	PublishEnabled bool
	// MaxDocumentBytes 与 MaxArtifactBytes 是正文与产物的字节上限。
	MaxDocumentBytes int64
	MaxArtifactBytes int64
	AssetLimits      []AssetKindLimit
}

// Deps 是构造 Service 所需的取值。
type Deps struct {
	// Store 是持久化存储。必填。
	Store MutableStore
	// Assets 是私有区对象存储的**直传**入口（见 internal/objectstore）。
	//
	// **为 nil 表示这个部署没有配置私有桶**：资产功能整体缺席，而工程与版本
	// 照常可用。缺席由 nil 表达，而不是由一个"什么都存不下"的实现表达——
	// 后者会让一次配置缺失在运行时表现成一次存储故障。
	Assets objectstore.Store
	// Public 是公开区的写入口。**为 nil 表示没有配置发布**：发布不可用。
	// 它写的是与 Assets 同一个桶，区别在写下去的对象权限（公开读）。
	Public PublicStore
	// Origin 是发布态地址的派生入口。零值表示没有配置发布域。
	Origin PublicOrigin
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
		logger: deps.Logger,
		now:    now,
	}
}

// Capabilities 汇报部署形态的边界。
func (s *Service) Capabilities() Capabilities {
	return Capabilities{
		AssetUploadEnabled: s.assets != nil,
		PublishEnabled:     s.publishEnabled(),
		MaxDocumentBytes:   MaxDocumentBytes,
		MaxArtifactBytes:   MaxArtifactBytes,
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

// CreateProject 创建一个工程（工程标识与版本/资产标识的分配入口）。
func (s *Service) CreateProject(ctx context.Context, ownerSubjectID, name, description string) (Project, error) {
	if err := validateProjectMeta(name, description); err != nil {
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
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := s.store.CreateProject(ctx, project); err != nil {
		return Project{}, err
	}
	if s.logger != nil {
		s.logger.Info("已创建工程",
			zap.String("project_id", project.ID),
			zap.String("subject_id", ownerSubjectID))
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
	return s.store.GetProject(ctx, projectID)
}

// DeleteProject 删除一个工程，并把它的私有区资产字节一并清掉。
//
// 已发布的地址立刻变成"不存在"（发布记录随工程一起删）；公开区的副本不回收。
// ——它是一份独立对象，召回它需要一次对账，属于另一个职责。
func (s *Service) DeleteProject(ctx context.Context, subjectID, projectID string) error {
	if _, err := OwnedProject(ctx, s.store, projectID, subjectID); err != nil {
		return err
	}
	assets, err := s.store.ListAssets(ctx, projectID)
	if err != nil {
		return err
	}
	if err := s.store.DeleteProject(ctx, projectID); err != nil {
		return err
	}
	// 对象删除失败不影响"工程已删除"这一结论：库内是权威，桶上可能因此留下
	// 无从被引用的孤儿对象，而它没有功能影响（与头像同源）。
	s.deleteAssetObjects(ctx, projectID, assets, "删除工程")
	if s.logger != nil {
		s.logger.Info("已删除工程",
			zap.String("project_id", projectID),
			zap.String("subject_id", subjectID),
			zap.Int("assets", len(assets)))
	}
	return nil
}

// GetDraft 读取当前草稿。没有草稿行时返回一份空草稿，而不是错误。
//
// 惰性创建的行与"保存过一次空内容"在编辑上完全等价，因此这里把
// ErrDraftNotFound 折成零值——把"还没写过"表现成一个错误，会让编辑器在
// 每个新工程上先显示一次失败。
func (s *Service) GetDraft(ctx context.Context, subjectID, projectID string) (Draft, error) {
	if _, err := OwnedProject(ctx, s.store, projectID, subjectID); err != nil {
		return Draft{}, err
	}
	draft, err := s.store.GetDraft(ctx, projectID)
	if errors.Is(err, ErrDraftNotFound) {
		return Draft{ProjectID: projectID}, nil
	}
	if err != nil {
		return Draft{}, err
	}
	return draft, nil
}

// SaveDraft 覆盖当前草稿（它不产生版本）。
func (s *Service) SaveDraft(ctx context.Context, subjectID, projectID string, content Document) (Draft, error) {
	if _, err := OwnedProject(ctx, s.store, projectID, subjectID); err != nil {
		return Draft{}, err
	}
	if err := CheckDocumentSize(content); err != nil {
		return Draft{}, err
	}
	now := s.now()
	if err := s.store.PutDraft(ctx, projectID, content, now); err != nil {
		return Draft{}, err
	}
	return Draft{ProjectID: projectID, Content: content, UpdatedAt: now}, nil
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
func (s *Service) deleteAssetObjects(ctx context.Context, projectID string, assets []Asset, action string) {
	if s.assets == nil {
		return
	}
	for _, asset := range assets {
		key := AssetObjectKey(projectID, asset.ID)
		if err := s.assets.Delete(ctx, key); err != nil && s.logger != nil {
			s.logger.Warn("删除资产对象失败，元数据已清空",
				zap.String("action", action),
				zap.String("project_id", projectID),
				zap.String("asset_id", asset.ID),
				zap.Error(err))
		}
	}
}
