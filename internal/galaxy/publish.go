package galaxy

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/poetlife/aladdin/internal/observability"
)

var (
	// ErrPublishUnavailable 表示这个部署没有配置发布所需的存储。
	//
	// **它不是故障**，而是"这个能力没开"：工程与资产照常可用，前端据此不
	// 渲染发布入口。
	ErrPublishUnavailable = errors.New("发布功能未启用")

	// ErrInvalidContent 表示这个版本的内容不能发布。
	//
	// 包装的消息里含具体位置，因此用户拿到的不是一句"发布失败"。
	ErrInvalidContent = errors.New("正文不能发布")
)

// 发布的四个阶段。名字与 docs/design/galaxy/publication.md 的阶段划分一致：
// 留痕里的 stage 字段直接取这些值，排障时按它筛选。
const (
	stageValidate = "受理与校验"
	stagePromote  = "资产上架"
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
// 如何"。**不含正文全文**——需要"关键入参"时记的是标识，而不是原始内容。
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
//     地址返回的还是上一次的产物。重试即从受理开始，且因为上架按内容摘要幂等，
//     重试不需要任何清理。
//   - 落库**之后**、切换之前的中断：从**切换**继续即可，不需要重新上架、不需要
//     重新落库——库里的发布记录已经是完整的（发布标识由工程与版本确定，
//     重试落在同一条记录上）。
//
// 准入由两把闸门共同决定：`galaxy.project.publish` 权限码（在 proto 的方法
// 注解上声明，由鉴权拦截器执行）与工程归属（OwnedProject）。
func (s *Service) Publish(ctx context.Context, subjectID, projectID, versionID string) (Publication, error) {
	if !s.publishEnabled() {
		return Publication{}, ErrPublishUnavailable
	}
	// 归属：工程必须是调用者自己的。
	if _, err := OwnedProject(ctx, s.store, projectID, subjectID); err != nil {
		return Publication{}, err
	}
	// **只能发布版本，不能发布草稿**：草稿是可变的，"发布一个可变的东西"没有
	// 意义。因此这里读的是版本表，草稿内容根本不会被读到。
	version, err := s.store.GetVersion(ctx, projectID, versionID)
	if err != nil {
		return Publication{}, err
	}

	// 发布标识在受理阶段就确定，因此一次被拒的发布也能被留痕指认。
	publicationID := PublicationID(projectID, versionID)

	// 阶段一：受理与校验。不能发布则终止，**不留任何痕迹**（指针未动、
	// 公开区无新对象、发布表无新行）。
	artifact, report, err := s.validateDocument(ctx, projectID, version.Content)
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

	// 阶段二：资产上架。只上架这个版本引用到的资产，且按内容摘要幂等。
	referenced := ReferencedAssetIDs(version.Content)
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

	// 阶段三：产物落库。落库是检查点：此后无论发生什么，产物都已经在库里，
	// 续跑只需从切换继续。
	publication := Publication{
		ID:                   publicationID,
		ProjectID:            projectID,
		VersionID:            versionID,
		Content:              artifact,
		PublishedBySubjectID: subjectID,
		PublishedAt:          s.now(),
	}
	if err := s.store.PutPublication(ctx, publication); err != nil {
		return Publication{}, err
	}
	s.logStage(ctx, "info", stageRecord,
		zap.String("publication_id", publicationID),
		zap.String("project_id", projectID),
		zap.Int("artifact_bytes", len(artifact)))

	// 阶段四：切换与生效。指针切换是幂等的，且**只在这里**改变对外可见的结果。
	if err := s.store.SetCurrentPublication(ctx, projectID, publicationID, s.now()); err != nil {
		return Publication{}, err
	}
	s.logStage(ctx, "info", stageSwitch,
		zap.String("publication_id", publicationID),
		zap.String("project_id", projectID),
		zap.String("effective_at", publication.PublishedAt.Format(time.RFC3339)))
	return publication, nil
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
		// 已经未发布：撤回是幂等的。
		return nil
	}
	return s.store.SetCurrentPublication(ctx, projectID, "", s.now())
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

// Origin 返回发布态地址的派生入口。
//
// 交付层（发布地址那条浏览器直连入口）用它派生内容安全策略里的允许来源：那个
// 取值与改写生成的地址来自**同一个配置值**，两处各写一份的表现是"地址指向 A、
// 策略允许 B"，而它表现为"发布成功了但什么都显示不出来"。
func (s *Service) Origin() PublicOrigin { return s.origin }

// CurrentArtifact 返回一个工程当前的发布产物，供**未认证**的公开入口使用。
//
// **它刻意不做归属校验**：发布态是公开匿名的，"地址即凭据"——拿到地址的人能
// 看，猜不出地址的人看不到。因此本函数不能有"调用者"这个参数。
//
// 未发布、已撤回、工程不存在、标识没被猜中——四者返回同一个结论
// （ErrPublicationNotFound），区分它们等于告诉一个猜地址的人"这个标识是真的，
// 只是还没发布"。
func (s *Service) CurrentArtifact(ctx context.Context, projectID string) (Publication, error) {
	if !s.publishEnabled() {
		return Publication{}, ErrPublicationNotFound
	}
	project, err := s.store.GetProject(ctx, projectID)
	if err != nil {
		if errors.Is(err, ErrProjectNotFound) {
			return Publication{}, ErrPublicationNotFound
		}
		return Publication{}, err
	}
	if project.CurrentPublicationID == "" {
		return Publication{}, ErrPublicationNotFound
	}
	publication, err := s.store.GetPublication(ctx, project.CurrentPublicationID)
	if err != nil {
		if errors.Is(err, ErrPublicationNotFound) {
			return Publication{}, ErrPublicationNotFound
		}
		return Publication{}, err
	}
	return publication, nil
}
