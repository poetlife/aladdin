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

// Publish 把一个版本发布成对外可达的产物。
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
// 准入由两把闸门共同决定：`galaxy.project.publish` 权限码（在 proto 的方法
// 注解上声明，由鉴权拦截器执行）与工程归属（OwnedProject）。
func (s *Service) Publish(ctx context.Context, subjectID, projectID, versionID string) (Publication, error) {
	if !s.publishEnabled() {
		return Publication{}, ErrPublishUnavailable
	}
	project, err := OwnedProject(ctx, s.store, projectID, subjectID)
	if err != nil {
		return Publication{}, err
	}
	// **只能发布版本，不能发布草稿**：草稿是可变的，"发布一个可变的东西"
	// 没有意义。因此这里读的是版本表，草稿清单根本不会被读到。
	version, err := s.store.GetVersion(ctx, projectID, versionID)
	if err != nil {
		return Publication{}, err
	}

	// 发布标识在受理阶段就确定，因此一次被拒的发布也能被留痕指认。
	publicationID := PublicationID(projectID, versionID)

	// 阶段一：受理与校验。不能发布则终止，**不留任何痕迹**（指针未动、
	// 公开区无新对象、发布表无新行）。
	artifacts, report, err := s.buildArtifacts(ctx, project, version.Manifest)
	if err != nil {
		return Publication{}, err
	}
	if !report.OK() {
		reason := strings.Join(report.Messages(), "; ")
		s.logStage(ctx, "warn", stageValidate,
			zap.String("publication_id", publicationID),
			zap.String("project_id", projectID),
			zap.String("version_id", versionID),
			zap.String("subject_id", subjectID),
			zap.String("decision", "reject"),
			zap.String("reason", reason))
		return Publication{}, fmt.Errorf("%w: %s", ErrInvalidContent, reason)
	}
	s.logStage(ctx, "info", stageValidate,
		zap.String("publication_id", publicationID),
		zap.String("project_id", projectID),
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
	if err := s.store.SetCurrentPublication(ctx, projectID, publicationID, s.now()); err != nil {
		return Publication{}, err
	}
	s.publish(projectID)
	s.logStage(ctx, "info", stageSwitch,
		zap.String("publication_id", publicationID),
		zap.String("project_id", projectID),
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

// Unpublish 撤回发布：把发布指针置空，地址**立刻**不可达（不依赖缓存过期）。
//
// 发布记录**保留**，因此可以重新发布同一个版本。公开区的副本不因撤回而删除：
// 它是一份独立对象，召回它需要一次对账。
func (s *Service) Unpublish(ctx context.Context, subjectID, projectID string) error {
	project, err := OwnedProject(ctx, s.store, projectID, subjectID)
	if err != nil {
		return err
	}
	if project.CurrentPublicationID == "" {
		// 已经未发布：撤回是幂等的。**什么都没变就不发事件**——订阅者为一次
		// 没有发生的变更重拉一遍是白费的。
		return nil
	}
	if err := s.store.SetCurrentPublication(ctx, projectID, "", s.now()); err != nil {
		return err
	}
	s.publish(projectID)
	return nil
}

// ProjectView 是工程元数据加上它的发布状态。
//
// 对外地址由服务端算好（见 public_origin.go 的派生入口），客户端不拼——拼一份
// 就是第三个地址来源。
type ProjectView struct {
	Project Project
	// Published 为真表示指针非空。它与"地址可用"是两件事：发布域没配置时
	// 指针可能还在，而地址算不出来。
	Published bool
	// Publication 是当前发布记录，未发布时为零值。
	Publication Publication
	// PublishedURL 是发布地址，未发布或发布域未配置时为空。
	PublishedURL string
	// BaseURL 是**发布根**，未配置发布域时为空。
	//
	// 它与是否已发布无关：构建命令用它（见 cli.md 的 `project base`），而"产物里
	// 的绝对路径要成立"这件事不取决于页面发没发出去。
	BaseURL string
}

// View 读出一个工程的对外状态。
func (s *Service) View(ctx context.Context, project Project) (ProjectView, error) {
	publication, published, err := s.CurrentPublication(ctx, project)
	if err != nil {
		return ProjectView{}, err
	}
	return ProjectView{
		Project:      project,
		Published:    published,
		Publication:  publication,
		PublishedURL: s.PageURL(project.ID),
		BaseURL:      s.BaseURL(project.ID),
	}, nil
}

// CurrentPublication 返回工程当前发布的那条记录与它的对外地址。
//
// 第二个返回值为假表示未发布（指针为空）。地址为空的可能有两种：发布域未配置
// （此时发布本身也不可用），或这条记录是在配置还在时发布的——两种情况都如实
// 反映"现在这个地址打不开"，而不是编一个出来。
func (s *Service) CurrentPublication(ctx context.Context, project Project) (Publication, bool, error) {
	if project.CurrentPublicationID == "" {
		return Publication{}, false, nil
	}
	publication, err := s.store.GetPublication(ctx, project.CurrentPublicationID)
	if err != nil {
		return Publication{}, false, err
	}
	return publication, true, nil
}

// PageURL 返回一个工程的发布地址，未配置发布域时为空。
func (s *Service) PageURL(projectID string) string {
	return s.origin.PageURL(projectID)
}

// BaseURL 返回一个工程的**发布根**，未配置发布域时为空。
//
// 它是构建命令要的那个值（见 cli.md 的 `project base`）：产物里的绝对路径靠它
// 成立，与"页面有没有发出去"无关。
func (s *Service) BaseURL(projectID string) string {
	if s.origin.IsZero() {
		return ""
	}
	return s.origin.SiteRoot(projectID)
}

// Origin 返回发布态地址的派生入口。
//
// 交付层（发布地址那条浏览器直连入口）用它派生内容安全策略里的允许来源：那个
// 取值与重定向生成的地址来自**同一处派生**，两处各写一份的表现是"地址指向 A、
// 策略允许 B"，而它表现为"发布成功了但什么都显示不出来"。
func (s *Service) Origin() PublicOrigin { return s.origin }

// PublishedEntry 查一条发布态的请求路径命中哪一条条目（集合成员测试的唯一入口）。
//
// **它不做归属校验**：发布态是公开匿名的，"地址即凭据"——拿到地址的人能看，
// 猜不出地址的人看不到。因此本方法不能有"调用者"这个参数。
//
// **它不读对象存储**：这条路径要能在校验命中（`ETag` 相符）时只读库里的清单。
//
// 未发布、已撤回、工程不存在、标识没被猜中、路径不在集合里——五者返回同一个
// 结论（ErrPublicationNotFound）。区分它们等于告诉一个猜地址的人"这个标识是
// 真的"或"这一页是真的，只是还没发布"。
//
// 请求路径为空表示入口页（`/g/<标识>` 与 `/g/<标识>/index.html` 是同一页）。
func (s *Service) PublishedEntry(ctx context.Context, projectID, requestPath string) (SiteForm, Entry, error) {
	if !s.publishEnabled() {
		return "", Entry{}, ErrPublicationNotFound
	}
	project, err := s.store.GetProject(ctx, projectID)
	if err != nil {
		if errors.Is(err, ErrProjectNotFound) {
			return "", Entry{}, ErrPublicationNotFound
		}
		return "", Entry{}, err
	}
	if project.CurrentPublicationID == "" {
		return "", Entry{}, ErrPublicationNotFound
	}
	publication, err := s.store.GetPublication(ctx, project.CurrentPublicationID)
	if err != nil {
		if errors.Is(err, ErrPublicationNotFound) {
			return "", Entry{}, ErrPublicationNotFound
		}
		return "", Entry{}, err
	}
	if requestPath == "" {
		requestPath = project.Form.EntryPath()
	}
	entry, ok := publication.Manifest.Find(requestPath)
	if !ok {
		return "", Entry{}, ErrPublicationNotFound
	}
	return project.Form, entry, nil
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
	return s.origin.AssetURL(asset.Digest, asset.MediaType), nil
}

// PreviewDraft 把当前草稿渲染成一份可以放进沙箱 iframe 的 HTML。
//
// **渲染在服务端，与发布共用同一段实现**：`docs` 形态的 markdown → HTML 只有
// 一处实现，网页端不再引第二个渲染器——两份实现迟早漂移，而用户看到的是
// "预览好好的、发布出来不一样"。
//
// 它复用**编辑态**的那套地址：`asset://` 记号被换成短时预签名地址，因此预览里
// 的图会随地址过期而显示不出来，刷新即得到新地址。
//
// **它不做审查、不做裁剪**：内容写什么就渲染什么，引用坏了就显示坏的。这处
// 有问题由校验入口单独给出——把两者混起来会让用户以为预览看起来对就等于发布
// 能成功。
func (s *Service) PreviewDraft(ctx context.Context, subjectID, projectID, entryPath string) ([]byte, error) {
	project, err := OwnedProject(ctx, s.store, projectID, subjectID)
	if err != nil {
		return nil, err
	}
	draft, err := s.store.GetDraft(ctx, projectID)
	if errors.Is(err, ErrDraftNotFound) {
		return []byte(emptyPreviewDocument), nil
	} else if err != nil {
		return nil, err
	}
	if len(draft.Manifest) == 0 {
		return []byte(emptyPreviewDocument), nil
	}

	urlOf := func(entry Entry) (string, error) { return s.presignEntry(ctx, projectID, entry) }

	if project.Form != SiteFormDocs {
		// `static` 只能**逐页预览**：页内的站内导航在预览里不可用（沙箱文档
		// 没有自己的源），而记号照常被换成短时地址。
		target := entryPath
		if target == "" {
			target = project.Form.EntryPath()
		}
		entry, ok := draft.Manifest.Find(target)
		if !ok {
			return nil, fmt.Errorf("%w: %s", ErrEntrySetInvalid, target)
		}
		data, err := s.assets.Read(ctx, ContentObjectKey(projectID, entry.Digest))
		if err != nil {
			return nil, err
		}
		return SubstituteAssetMarkers(data, func(assetID string) (string, error) {
			asset, found := assetEntryByID(draft.Manifest, assetID)
			if !found {
				return PlaceholderScheme + assetID, nil
			}
			url, err := urlOf(asset)
			if err != nil || url == "" {
				return PlaceholderScheme + assetID, nil
			}
			return url, nil
		})
	}

	// `docs`：整站渲染一遍、拼成一份、用文内锚点导航。
	sources := make([]DocSource, 0, len(draft.Manifest))
	skeleton := make([]Doc, 0, len(draft.Manifest))
	for _, entry := range draft.Manifest {
		if !IsMarkdownPath(entry.Path) {
			continue
		}
		data, err := s.assets.Read(ctx, ContentObjectKey(projectID, entry.Digest))
		if err != nil {
			return nil, err
		}
		sources = append(sources, DocSource{Path: entry.Path, Body: data})
		skeleton = append(skeleton, Doc{SourcePath: entry.Path, ArtifactPath: ArtifactPath(project.Form, entry.Path)})
	}
	sortDocs(skeleton)
	linker := func(from string) LinkResolver {
		return PreviewLinker{
			Form:     project.Form,
			Manifest: draft.Manifest,
			Docs:     skeleton,
			URLOf:    urlOf,
		}
	}
	docs, err := RenderDocs(sources, linker)
	if err != nil {
		return nil, err
	}
	return RenderPreviewDocument(docs, s.origin.SiteRoot(projectID)), nil
}

// emptyPreviewDocument 是草稿为空时的预览页。它不是错误页：新工程打开编辑器
// 时本来就还没有内容。
const emptyPreviewDocument = "<!doctype html><meta charset=\"utf-8\"><p>这份草稿还是空的。</p>"

// sortDocs 按源路径升序，与 RenderDocs 的顺序一致——锚点在渲染之前就要定下来，
// 因此这里必须用同一个序。
func sortDocs(docs []Doc) {
	for i := 1; i < len(docs); i++ {
		for j := i; j > 0 && docs[j].SourcePath < docs[j-1].SourcePath; j-- {
			docs[j], docs[j-1] = docs[j-1], docs[j]
		}
	}
}
