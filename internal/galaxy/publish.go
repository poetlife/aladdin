package galaxy

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/poetlife/aladdin/internal/objectstore"
	"github.com/poetlife/aladdin/internal/observability"
)

var (
	// ErrPublishUnavailable 表示这个部署没有配置发布所需的存储。
	//
	// **它不是故障**，而是"这个能力没开"：工程与内容照常可用，前端据此不
	// 渲染发布入口。
	ErrPublishUnavailable = errors.New("发布功能未启用")

	// ErrInvalidContent 表示这个版本的内容不能发布。
	//
	// 包装的消息里含具体位置，因此用户拿到的不是一句"发布失败"。
	ErrInvalidContent = errors.New("内容不能发布")
)

// 发布的四个阶段。名字与 docs/design/galaxy/publication.md 的阶段划分一致：
// 留痕里的 stage 字段直接取这些值，排障时按它筛选。
const (
	stageValidate = "受理与校验"
	stagePromote  = "媒体上架"
	stageRecord   = "产物落库"
	stageSwitch   = "切换与生效"
)

// spanLogger 取带链路标识的 logger，并容忍"没有 logger"这一情形（测试）。
func spanLogger(ctx context.Context, logger *zap.Logger) *zap.Logger {
	return observability.SpanLogger(ctx, logger)
}

// logStage 为发布的一个阶段留一行痕（四阶段留痕的唯一入口）。
//
// 每个阶段各留一行是 spec 的要求：无需重跑就能回答"现在到第几步、上一步结果
// 如何"。**不含文本全文**——需要"关键入参"时记的是标识与数量。
func (s *Service) logStage(ctx context.Context, level, stage string, fields ...zap.Field) {
	logger := spanLogger(ctx, s.logger)
	if logger == nil {
		return
	}
	fields = append(fields, zap.String("stage", stage))
	switch level {
	case "warn":
		logger.Warn("发布阶段", fields...)
	case "error":
		logger.Error("发布阶段", fields...)
	default:
		logger.Info("发布阶段", fields...)
	}
}

// Publish 把**某一个槽的一个版本**发布成对外可达的产物。
//
// 四个阶段的顺序不可调换，且**检查点设在"产物落库"完成之后**：
//
//   - 落库**之前**的中断（校验中、上架中）：对外**不留任何影响**——指针没动，
//     地址返回的还是上一次的产物。重试即从受理开始，且因为上架与内容对象都按
//     内容摘要幂等，重试不需要任何清理。
//   - 落库**之后**、切换之前的中断：从**切换**继续即可，不需要重新上架、不需要
//     重新落库——库里的发布记录已经是完整的（发布标识由工程与版本确定，
//     重试落在同一条记录上）。**整套一次性生效就落在这里**：切换是一个单点，
//     因此不存在"入口是新的、而某一页还是旧的"。
//
// **它只碰这一个槽**：另一个槽的发布指针与地址不受影响。
//
// 准入由两把闸门共同决定：`galaxy.project.publish` 权限码（在 proto 的方法
// 注解上声明，由鉴权拦截器执行）与工程归属（OwnedProject）。
func (s *Service) Publish(ctx context.Context, subjectID, projectID string, slot ContentSlot, versionID string) (Publication, error) {
	if !s.publishEnabled() {
		return Publication{}, ErrPublishUnavailable
	}
	project, err := s.ownedProjectSlot(ctx, subjectID, projectID, slot)
	if err != nil {
		return Publication{}, err
	}
	// **只能发布版本，不能发布草稿**：草稿是可变的，"发布一个可变的东西"
	// 没有意义。因此这里读的是版本表，草稿清单根本不会被读到。
	version, err := s.store.GetVersion(ctx, projectID, slot, versionID)
	if err != nil {
		return Publication{}, err
	}

	// 发布标识在受理阶段就确定，因此一次被拒的发布也能被留痕指认。
	publicationID := PublicationID(projectID, versionID)

	// 阶段一：受理与校验。不能发布则终止，**不留任何痕迹**（指针未动、
	// 公开区无新对象、发布表无新行）。
	artifacts, report, err := s.buildArtifacts(ctx, project, slot, version.Manifest, false)
	if err != nil {
		return Publication{}, err
	}
	if !report.OK() {
		reason := strings.Join(report.Messages(), "; ")
		s.logStage(ctx, "warn", stageValidate,
			zap.String("publication_id", publicationID),
			zap.String("project_id", projectID),
			zap.String("slot", string(slot)),
			zap.String("version_id", versionID),
			zap.String("subject_id", subjectID),
			zap.String("decision", "reject"),
			zap.String("reason", reason))
		return Publication{}, fmt.Errorf("%w: %s", ErrInvalidContent, reason)
	}
	s.logStage(ctx, "info", stageValidate,
		zap.String("publication_id", publicationID),
		zap.String("project_id", projectID),
		zap.String("slot", string(slot)),
		zap.String("version_id", versionID),
		zap.String("subject_id", subjectID),
		zap.String("decision", "accept"))

	// 阶段二：媒体上架。只上架这个版本引用的资产，且按（内容摘要，类型）幂等。
	// **文本不上架**——发布态由服务端从私有区读它。
	referenced := version.Manifest.AssetIDs()
	assets := make([]Asset, 0, len(referenced))
	for _, assetID := range referenced {
		asset, err := s.assetOfProject(ctx, projectID, assetID)
		if err != nil {
			return Publication{}, err
		}
		assets = append(assets, asset)
	}
	promoteStarted := time.Now()
	outcome, err := s.promoteAssets(ctx, assets)
	if err != nil {
		return Publication{}, err
	}
	s.logStage(ctx, "info", stagePromote,
		zap.String("publication_id", publicationID),
		zap.String("project_id", projectID),
		zap.Int("referenced", outcome.Referenced),
		zap.Int("promoted", outcome.Promoted),
		zap.Int("skipped", outcome.Skipped),
		zap.Int64("bytes", outcome.Bytes),
		zap.Float64("duration_ms", time.Since(promoteStarted).Seconds()*1000))

	// 阶段三：产物落库。渲染/改写产生的文本写成内容对象（已存在的按内容摘要
	// 跳过），产物清单存成一条发布记录。落库是检查点：此后无论发生什么，产物
	// 都已经在库里，续跑只需从切换继续。
	manifest, newObjects, err := s.writeArtifacts(ctx, project, version.Manifest, artifacts)
	if err != nil {
		return Publication{}, err
	}
	publication := Publication{
		ID:                   publicationID,
		ProjectID:            projectID,
		VersionID:            versionID,
		Slot:                 slot,
		Manifest:             manifest,
		PublishedBySubjectID: subjectID,
		PublishedAt:          s.now(),
	}
	if err := s.store.PutPublication(ctx, publication); err != nil {
		return Publication{}, err
	}
	s.logStage(ctx, "info", stageRecord,
		zap.String("publication_id", publicationID),
		zap.String("project_id", projectID),
		zap.Int("paths", len(manifest)),
		zap.Int("new_objects", newObjects))

	// 阶段四：切换与生效。指针切换是幂等的，且**只在这里**改变对外可见的结果。
	if err := s.store.SetCurrentPublication(ctx, projectID, slot, publicationID, s.now()); err != nil {
		return Publication{}, err
	}
	s.publish(projectID)
	s.logStage(ctx, "info", stageSwitch,
		zap.String("publication_id", publicationID),
		zap.String("project_id", projectID),
		zap.String("slot", string(slot)),
		zap.String("effective_at", publication.PublishedAt.Format(time.RFC3339)))
	return publication, nil
}

// writeArtifacts 把产物逐份写成内容对象，并给出产物清单。
//
// 它有两件事必须一起做：**产物清单里的每一条都对应一个真实存在的内容对象**，
// 因此清单不是在写对象之前凭空拼出来的。
//
// 文本条目换成产物路径与**产物摘要**（渲染/改写后的字节）；资产条目原样带上，
// 好让发布态知道这个路径要重定向而不是返回文本。
func (s *Service) writeArtifacts(ctx context.Context, project Project, versionManifest Manifest, artifacts map[string][]byte) (Manifest, int, error) {
	if s.assets == nil {
		return nil, 0, ErrAssetUnavailable
	}
	manifest := make(Manifest, 0, len(artifacts)+len(versionManifest))
	newObjects := 0

	paths := make([]string, 0, len(artifacts))
	for artifactPath := range artifacts {
		paths = append(paths, artifactPath)
	}
	sortStrings(paths)

	for _, artifactPath := range paths {
		data := artifacts[artifactPath]
		digest := ContentDigest(data)
		created, err := s.writeContentObject(ctx, project.ID, digest, data)
		if err != nil {
			return nil, 0, err
		}
		if created {
			newObjects++
		}
		manifest = append(manifest, Entry{Path: artifactPath, Kind: EntryKindText, Digest: digest})
	}
	// 资产条目原样进产物清单：发布态据此把那条路径重定向到公开区。它们不产生
	// 内容对象，也不改路径。
	for _, entry := range versionManifest.Assets() {
		manifest = append(manifest, entry)
	}
	return manifest, newObjects, nil
}

// writeContentObject **仅当对象不存在时**写入一个内容对象（唯一入口）。
//
// 第二个返回值表示这次是否真的写了。摘要是寻址键，因此"这一份产物是不是新的"
// 是一个只看键就能回答的问题——重复发布同一个版本不产生任何新字节。
func (s *Service) writeContentObject(ctx context.Context, projectID, digest string, data []byte) (bool, error) {
	key := ContentObjectKey(projectID, digest)
	if _, err := s.assets.Head(ctx, key); err == nil {
		return false, nil
	} else if !errors.Is(err, objectstore.ErrObjectNotFound) {
		return false, err
	}
	if err := s.assets.Put(ctx, key, objectstore.NeutralContentType, data); err != nil {
		return false, err
	}
	return true, nil
}

// sortStrings 是一个不含依赖的字典序排序（产物路径的顺序要固定，好让同样的
// 输入得到同样的清单与留痕）。
func sortStrings(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}

// Unpublish 撤回**某一个槽**的发布：把那个槽的发布指针置空，它的地址**立刻**
// 不可达（不依赖缓存过期）。**另一个槽不受任何影响。**
//
// 发布记录**保留**，因此可以重新发布同一个版本。公开区的副本不因撤回而删除：
// 它是一份独立对象，召回它需要一次对账。
func (s *Service) Unpublish(ctx context.Context, subjectID, projectID string, slot ContentSlot) error {
	project, err := s.ownedProjectSlot(ctx, subjectID, projectID, slot)
	if err != nil {
		return err
	}
	enabled, _ := project.FindSlot(slot)
	if enabled.CurrentPublicationID == "" {
		// 已经未发布：撤回是幂等的。**什么都没变就不发事件**——订阅者为一次
		// 没有发生的变更重拉一遍是白费的。
		return nil
	}
	if err := s.store.SetCurrentPublication(ctx, projectID, slot, "", s.now()); err != nil {
		return err
	}
	s.publish(projectID)
	return nil
}

// SlotView 是**一个内容槽**的对外状态。
type SlotView struct {
	Slot ContentSlot
	// Published 为真表示这个槽的指针非空。它与"地址可用"是两件事：发布域
	// 没配置时指针可能还在，而地址算不出来。
	Published bool
	// Publication 是这个槽的当前发布记录，未发布时为零值。
	Publication Publication
	// PublishedURL 是这个槽的发布地址，未发布或发布域未配置时为空。
	PublishedURL string
	// BaseURL 是这个槽的**发布根**，未配置发布域时为空。
	//
	// 它与是否已发布无关：构建命令用它（见 cli.md 的 `project base`），而
	// "产物里的绝对路径要成立"这件事不取决于页面发没发出去。
	BaseURL string
}

// ProjectView 是工程元数据加上**它每个槽**的发布状态。
//
// 每个槽一条，顺序与工程上的槽一致——**状态按槽分开**，因此没有"这个工程发布了
// 没有"这样一个问题（站点发了、文档没发是完全正常的一档）。
type ProjectView struct {
	Project Project
	Slots   []SlotView
}

// FindSlot 取一个槽的对外状态，未启用时第二个返回值为假。
func (v ProjectView) FindSlot(slot ContentSlot) (SlotView, bool) {
	for _, candidate := range v.Slots {
		if candidate.Slot == slot {
			return candidate, true
		}
	}
	return SlotView{}, false
}

// View 读出一个工程**每个槽**的对外状态。
func (s *Service) View(ctx context.Context, project Project) (ProjectView, error) {
	views := make([]SlotView, 0, len(project.Slots))
	for _, enabled := range project.Slots {
		publication, published, err := s.CurrentPublication(ctx, project, enabled.Slot)
		if err != nil {
			return ProjectView{}, err
		}
		views = append(views, SlotView{
			Slot:         enabled.Slot,
			Published:    published,
			Publication:  publication,
			PublishedURL: s.origin.ShareURL(project.ID, enabled.Slot),
			BaseURL:      s.BaseURL(project.ID, enabled.Slot),
		})
	}
	return ProjectView{Project: project, Slots: views}, nil
}

// CurrentPublication 返回**某一个槽**当前发布的那条记录。
//
// 第二个返回值为假表示这个槽未发布（指针为空）。地址为空的可能有两种：发布域
// 未配置（此时发布本身也不可用），或这条记录是在配置还在时发布的——两种情况都
// 如实反映"现在这个地址打不开"，而不是编一个出来。
func (s *Service) CurrentPublication(ctx context.Context, project Project, slot ContentSlot) (Publication, bool, error) {
	enabled, ok := project.FindSlot(slot)
	if !ok || enabled.CurrentPublicationID == "" {
		return Publication{}, false, nil
	}
	publication, err := s.store.GetPublication(ctx, enabled.CurrentPublicationID)
	if err != nil {
		return Publication{}, false, err
	}
	return publication, true, nil
}

// 内容地址与分享地址都由 Service.Origin() 上那一处派生给出（见
// PublicOrigin.ContentURL / ShareURL）——不在这一层再包一遍，免得出现第二个
// "发布地址从哪来"。

// BaseURL 返回一个槽的**发布根**，未配置发布域时为空。
//
// 它是构建命令要的那个值（见 cli.md 的 `project base`）：产物里的绝对路径靠它
// 成立，与"页面有没有发出去"无关。
func (s *Service) BaseURL(projectID string, slot ContentSlot) string {
	if s.origin.IsZero() {
		return ""
	}
	return s.origin.SiteRoot(projectID, slot)
}

// Origin 返回发布态地址的派生入口。
//
// 交付层（发布地址那条浏览器直连入口）用它派生内容安全策略里的允许来源：那个
// 取值与重定向生成的地址来自**同一处派生**，两处各写一份的表现是"地址指向 A、
// 策略允许 B"，而它表现为"发布成功了但什么都显示不出来"。
func (s *Service) Origin() PublicOrigin { return s.origin }

// PublishedEntry 查一条发布态的请求路径命中**某一个槽**的哪一条条目
// （集合成员测试的唯一入口）。
//
// **它不做归属校验**：发布态是公开匿名的，"地址即凭据"——拿到地址的人能看，
// 猜不出地址的人看不到。因此本方法不能有"调用者"这个参数。
//
// **它不读对象存储**：这条路径要能在校验命中（`ETag` 相符）时只读库里的清单。
//
// **槽没有启用**、未发布、已撤回、工程不存在、标识没被猜中、路径不在集合里
// ——六者返回同一个结论（ErrPublicationNotFound）。区分它们等于告诉一个猜地址
// 的人"这个标识是真的"或"这一页是真的，只是还没发布"。
//
// 请求路径为空表示入口页（`/g/<标识>` 与 `/g/<标识>/index.html` 是同一页；
// docs 槽同理，入口在它自己的根下）。
func (s *Service) PublishedEntry(ctx context.Context, projectID string, slot ContentSlot, requestPath string) (Entry, error) {
	if !s.publishEnabled() {
		return Entry{}, ErrPublicationNotFound
	}
	project, err := s.store.GetProject(ctx, projectID)
	if err != nil {
		if errors.Is(err, ErrProjectNotFound) {
			return Entry{}, ErrPublicationNotFound
		}
		return Entry{}, err
	}
	if _, ok := project.FindSlot(slot); !ok {
		return Entry{}, ErrPublicationNotFound
	}
	publication, published, err := s.CurrentPublication(ctx, project, slot)
	if err != nil {
		if errors.Is(err, ErrPublicationNotFound) {
			return Entry{}, ErrPublicationNotFound
		}
		return Entry{}, err
	}
	if !published {
		return Entry{}, ErrPublicationNotFound
	}
	if requestPath == "" {
		// **入口取的是产物路径，不是文件组里的路径。** 两者在 `docs` 槽上不同：
		// 入口源是 `index.md`，而产物里那一页是 `index.html`。拿源路径去查清单会
		// 查不到——表现是"文档槽的发布地址（`/g/<标识>/docs`）打不开"。
		requestPath = ArtifactPath(slot, slot.EntryPath())
	}
	entry, ok := publication.Manifest.Find(requestPath)
	if !ok {
		return Entry{}, ErrPublicationNotFound
	}
	return entry, nil
}

// ReadPublishedText 读出一份发布态文本的字节。
//
// **文本不能重定向到公开区**：内容寻址会抹掉路径，文档落在摘要地址上之后它内部
// 的 `/assets/index.js` 与相对链接就都指不到地方了。因此这一步由服务端在它自己
// 的路径上给出。
func (s *Service) ReadPublishedText(ctx context.Context, projectID, digest string) ([]byte, error) {
	if s.assets == nil {
		return nil, ErrPublicationNotFound
	}
	data, err := s.assets.Read(ctx, ContentObjectKey(projectID, digest))
	if err != nil {
		if errors.Is(err, objectstore.ErrObjectNotFound) {
			return nil, ErrPublicationNotFound
		}
		return nil, err
	}
	return data, nil
}

// PublishedAssetURL 返回一条发布态资产条目的公开区地址。
//
// 公开区地址按内容摘要与类型寻址、只在重定向的那一刻给出——**它不进产物**，
// 因此构建产物一个字节都不用改。
func (s *Service) PublishedAssetURL(ctx context.Context, projectID, assetID string) (string, error) {
	if s.origin.IsZero() {
		return "", ErrPublicationNotFound
	}
	asset, err := s.store.GetAsset(ctx, projectID, assetID)
	if err != nil {
		if errors.Is(err, ErrAssetNotFound) {
			// 被发布引用的资产不许删，因此这条链不该断；断了就与"这一页不存在"
			// 同结论，而不是抛一次 500。
			return "", ErrPublicationNotFound
		}
		return "", err
	}
	return s.origin.AssetURL(projectID, asset.Digest, asset.MediaType), nil
}
