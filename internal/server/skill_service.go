package server

import (
	"context"
	"errors"
	"fmt"
	"time"

	"connectrpc.com/connect"
	"go.uber.org/zap"

	skillv1 "github.com/poetlife/aladdin/api/gen/aladdin/skill/v1"
	"github.com/poetlife/aladdin/internal/objectstore"
	"github.com/poetlife/aladdin/internal/skill"
	"github.com/poetlife/aladdin/internal/tagging"
)

// SkillService 是技能目录**读面**的 RPC 实现。
//
// 它是一层薄壳：检索语义、回退折算、使用量的口径都在 internal/skill，本包只做
// 协议层的取数与回填。这样"什么算命中""有效标题是什么"可以脱离 RPC 单独测试。
//
// 权限与作用域由方法注解声明（见 skill.proto），拦截器统一执行，这里不重复判定。
//
// **本层不写调用方的文件系统**：取正文只把字节放进响应里，没有任何一条路径会
// 落盘（见 docs/design/skill/agent-access.md）。
type SkillService struct {
	skills *skill.Service
	logger *zap.Logger
}

// NewSkillService 构造技能目录读面。
func NewSkillService(service *skill.Service, logger *zap.Logger) *SkillService {
	return &SkillService{skills: service, logger: logger}
}

// GetCapabilities 实现 SkillService：下发这个部署下技能目录的边界。
func (s *SkillService) GetCapabilities(ctx context.Context, _ *connect.Request[skillv1.GetCapabilitiesRequest]) (*connect.Response[skillv1.GetCapabilitiesResponse], error) {
	if _, err := callerSubject(ctx); err != nil {
		return nil, err
	}
	capabilities := s.skills.Capabilities()
	return connect.NewResponse(&skillv1.GetCapabilitiesResponse{
		Capabilities: &skillv1.SkillCapabilities{
			CatalogEnabled:  capabilities.CatalogEnabled,
			ImportEnabled:   capabilities.ImportEnabled,
			MaxFiles:        fitUint32(int64(capabilities.MaxFiles)),
			MaxFileBytes:    fitUint32(int64(capabilities.MaxFileBytes)),
			MaxPackageBytes: fitUint32(int64(capabilities.MaxPackageBytes)),
		},
	}), nil
}

// ListSkills 实现 SkillService。
func (s *SkillService) ListSkills(ctx context.Context, req *connect.Request[skillv1.ListSkillsRequest]) (*connect.Response[skillv1.ListSkillsResponse], error) {
	subject, err := callerSubject(ctx)
	if err != nil {
		return nil, err
	}
	result, err := s.skills.ListSkills(ctx, subject.ID, skill.Filter{
		Query:         req.Msg.GetQuery(),
		Tags:          req.Msg.GetTags(),
		FavoritedOnly: req.Msg.GetFavoritedOnly(),
	})
	if err != nil {
		return nil, toSkillConnectError(err)
	}
	resp := &skillv1.ListSkillsResponse{
		Skills:        make([]*skillv1.Skill, 0, len(result.Skills)),
		AvailableTags: result.AvailableTags,
		Truncated:     result.Truncated,
	}
	for _, item := range result.Skills {
		resp.Skills = append(resp.Skills, toProtoSkill(item, false))
	}
	return connect.NewResponse(resp), nil
}

// GetSkill 实现 SkillService。
func (s *SkillService) GetSkill(ctx context.Context, req *connect.Request[skillv1.GetSkillRequest]) (*connect.Response[skillv1.GetSkillResponse], error) {
	subject, err := callerSubject(ctx)
	if err != nil {
		return nil, err
	}
	view, err := s.skills.GetSkill(ctx, subject.ID, req.Msg.GetSkillId())
	if err != nil {
		return nil, toSkillConnectError(err)
	}
	return connect.NewResponse(&skillv1.GetSkillResponse{
		Skill: toProtoSkill(view, true),
	}), nil
}

// GetSkillFile 实现 SkillService：取用一条文件的字节。
//
// 它是使用量的计量点（在领域层），因此这一层的职责只有"把字节放进响应"。
func (s *SkillService) GetSkillFile(ctx context.Context, req *connect.Request[skillv1.GetSkillFileRequest]) (*connect.Response[skillv1.GetSkillFileResponse], error) {
	subject, err := callerSubject(ctx)
	if err != nil {
		return nil, err
	}
	content, err := s.skills.GetFile(ctx, subject.ID, req.Msg.GetSkillId(), req.Msg.GetPath())
	if err != nil {
		return nil, toSkillConnectError(err)
	}
	return connect.NewResponse(&skillv1.GetSkillFileResponse{
		Content: content.Data,
		Path:    content.Path,
		Digest:  content.Digest,
	}), nil
}

// ListSkillVersions 实现 SkillService。
func (s *SkillService) ListSkillVersions(ctx context.Context, req *connect.Request[skillv1.ListSkillVersionsRequest]) (*connect.Response[skillv1.ListSkillVersionsResponse], error) {
	subject, err := callerSubject(ctx)
	if err != nil {
		return nil, err
	}
	// 先读一次技能：它同时给出"技能在不在"与"当前指针指向哪一份"，省掉一次
	// 单独的查询，也让下面那个 `current` 标记不需要另一条判据。
	view, err := s.skills.GetSkill(ctx, subject.ID, req.Msg.GetSkillId())
	if err != nil {
		return nil, toSkillConnectError(err)
	}
	versions, err := s.skills.ListVersions(ctx, req.Msg.GetSkillId())
	if err != nil {
		return nil, toSkillConnectError(err)
	}
	resp := &skillv1.ListSkillVersionsResponse{Versions: make([]*skillv1.SkillVersion, 0, len(versions))}
	for _, version := range versions {
		resp.Versions = append(resp.Versions, &skillv1.SkillVersion{
			Id:           version.ID,
			Commit:       version.Commit,
			FileCount:    fitUint32(int64(len(version.Files))),
			TotalBytes:   fitUint64(version.TotalBytes()),
			CreatedAt:    formatSkillTime(version.CreatedAt),
			Current:      version.ID == view.Current.ID,
			SkippedFiles: fitUint32(int64(version.SkippedFiles)),
		})
	}
	return connect.NewResponse(resp), nil
}

// SetSkillFavorite 实现 SkillService。
func (s *SkillService) SetSkillFavorite(ctx context.Context, req *connect.Request[skillv1.SetSkillFavoriteRequest]) (*connect.Response[skillv1.SetSkillFavoriteResponse], error) {
	subject, err := callerSubject(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.skills.SetFavorite(ctx, subject.ID, req.Msg.GetSkillId(), req.Msg.GetFavorited()); err != nil {
		return nil, toSkillConnectError(err)
	}
	return connect.NewResponse(&skillv1.SetSkillFavoriteResponse{}), nil
}

// toProtoSkill 把一条技能回填成协议类型。
//
// detail 为假时**只填列表要用的那几项**：文件清单与来源只在详情里给出，列表会读
// 很多行，把清单挂上来是一笔与列表无关的代价（见 skill.proto 的 Skill 说明）。
func toProtoSkill(view skill.View, detail bool) *skillv1.Skill {
	item := view.Skill
	out := &skillv1.Skill{
		Id:         item.ID,
		Title:      item.EffectiveTitle(),
		Summary:    item.EffectiveSummary(),
		Tags:       item.Tags,
		Favorited:  view.Favorited,
		FileCount:  fitUint32(int64(len(item.Current.Files))),
		TotalBytes: fitUint64(item.Current.TotalBytes()),
		Usage: &skillv1.SkillUsage{
			UseDays:    fitUint32(int64(view.Usage.UseDays)),
			UserCount:  fitUint32(int64(view.Usage.Users)),
			LastUsedAt: formatSkillTime(view.Usage.LastUsedAt),
		},
	}
	if !detail {
		return out
	}
	out.TitleOverride = item.Title
	out.SummaryOverride = item.Summary
	out.Name = item.Current.Name
	out.Description = item.Current.Description
	out.CurrentVersionId = item.Current.ID
	out.CurrentVersionCreatedAt = formatSkillTime(item.Current.CreatedAt)
	out.Source = &skillv1.SkillSource{
		RepositoryUrl: item.Source.URL(),
		Ref:           item.Source.Ref,
		SubPath:       item.Source.SubPath,
		Commit:        item.Source.Commit,
	}
	out.Files = make([]*skillv1.SkillFile, 0, len(item.Current.Files))
	for _, file := range item.Current.Files {
		out.Files = append(out.Files, &skillv1.SkillFile{
			Path:      file.Path,
			SizeBytes: fitUint64(file.SizeBytes),
			Digest:    file.Digest,
		})
	}
	return out
}

// formatSkillTime 把时刻折成仓库既有的对外形状（RFC3339，UTC）。
//
// 零值返回空串：使用量里的"从未被取用"与"取用于公元 1 年"是两件事。
func formatSkillTime(at time.Time) string {
	if at.IsZero() {
		return ""
	}
	return at.UTC().Format(time.RFC3339)
}

// toSkillConnectError 把领域错误映射为 Connect 错误码。
//
// 三类必须分开（见 docs/design/skill/onboarding.md 的"超时与失败"）：**形状不合法**
// 是用法错误、**远端侧不可得**要么可重试要么改引用、**内容不通过**只能改远端仓库。
// 把它们混成一句"纳管失败"的表现是管理员完全不知道该动哪一头。
func toSkillConnectError(err error) error {
	switch {
	case errors.Is(err, skill.ErrStoreUnavailable),
		errors.Is(err, objectstore.ErrStoreUnavailable):
		return connect.NewError(connect.CodeUnavailable, errors.New("服务暂时不可用，请稍后重试"))
	case errors.Is(err, skill.ErrObjectStoreUnavailable),
		errors.Is(err, skill.ErrRemoteNotConfigured):
		// **不是故障，是"这条路在这个部署上不成立"**：没有配置对象存储时技能目录
		// 整体不可用，前端据此不渲染入口（见 GetCapabilities）。
		return connect.NewError(connect.CodeFailedPrecondition,
			errors.New("这个部署没有配置对象存储，技能目录不可用"))
	case errors.Is(err, skill.ErrSkillNotFound):
		return connect.NewError(connect.CodeNotFound, errors.New("技能不存在"))
	case errors.Is(err, skill.ErrVersionNotFound):
		return connect.NewError(connect.CodeNotFound, errors.New("技能版本不存在"))
	case errors.Is(err, skill.ErrFileNotFound):
		// **与"技能不存在"必须分开**：技能在，是那条路径不在。
		return connect.NewError(connect.CodeNotFound, errors.New("这个技能里没有这条路径"))
	case errors.Is(err, skill.ErrRepositoryInvalid):
		return connect.NewError(connect.CodeInvalidArgument, errors.New(err.Error()))
	case errors.Is(err, skill.ErrRepositoryNotFound):
		return connect.NewError(connect.CodeNotFound, errors.New("远端仓库或引用不存在"))
	case errors.Is(err, skill.ErrRemoteUnavailable):
		return connect.NewError(connect.CodeUnavailable, errors.New(err.Error()))
	case errors.Is(err, skill.ErrPackageInvalid):
		// 消息里点名了那一处（哪一个路径、违反了哪一条），因此原样透出。
		return connect.NewError(connect.CodeInvalidArgument, errors.New(err.Error()))
	case errors.Is(err, skill.ErrTitleTooLong):
		return connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("技能标题不能超过 %d 个字", skill.MaxTitleRunes))
	case errors.Is(err, skill.ErrSummaryTooLong):
		return connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("技能简介不能超过 %d 个字", skill.MaxSummaryRunes))
	case errors.Is(err, tagging.ErrInvalid), errors.Is(err, tagging.ErrTooMany):
		return connect.NewError(connect.CodeInvalidArgument, errors.New(err.Error()))
	default:
		return connect.NewError(connect.CodeInternal, errors.New("服务端处理失败"))
	}
}
