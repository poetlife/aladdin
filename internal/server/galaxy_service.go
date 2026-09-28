package server

import (
	"context"
	"errors"
	"fmt"
	"time"

	"connectrpc.com/connect"
	"go.uber.org/zap"

	galaxyv1 "github.com/poetlife/aladdin/api/gen/aladdin/galaxy/v1"
	"github.com/poetlife/aladdin/internal/galaxy"
	"github.com/poetlife/aladdin/internal/objectstore"
	"github.com/poetlife/aladdin/internal/rbac"
)

// GalaxyService 是创作面（"用户写一份 HTML 并把它发布出去"）的 RPC 实现。
//
// **两把闸门各管一件事**：准入（`galaxy.*` 权限码）由 proto 上的方法注解声明、
// 由鉴权拦截器执行；归属（这个工程是不是他的）由本层调用的领域入口校验。它们
// 不可互相替代，也不重复实现。
//
// **接口面上没有"指定拥有者"的形状**：目标工程由请求给出，"它属于谁"由凭证
// 决定。两者对不上时返回的结论与"这个工程不存在"完全相同（见
// galaxy.OwnedProject 的说明）。
type GalaxyService struct {
	galaxy *galaxy.Service
	logger *zap.Logger
}

// NewGalaxyService 构造创作服务。
func NewGalaxyService(service *galaxy.Service, logger *zap.Logger) *GalaxyService {
	return &GalaxyService{galaxy: service, logger: logger}
}

// GetCapabilities 实现 GalaxyService：下发这个部署下创作能力的边界。
func (s *GalaxyService) GetCapabilities(ctx context.Context, _ *connect.Request[galaxyv1.GetCapabilitiesRequest]) (*connect.Response[galaxyv1.GetCapabilitiesResponse], error) {
	if _, err := callerSubject(ctx); err != nil {
		return nil, err
	}
	return connect.NewResponse(&galaxyv1.GetCapabilitiesResponse{
		Capabilities: toProtoCapabilities(s.galaxy.Capabilities()),
	}), nil
}

// ListProjects 实现 GalaxyService。
func (s *GalaxyService) ListProjects(ctx context.Context, _ *connect.Request[galaxyv1.ListProjectsRequest]) (*connect.Response[galaxyv1.ListProjectsResponse], error) {
	subject, err := callerSubject(ctx)
	if err != nil {
		return nil, err
	}
	projects, err := s.galaxy.ListProjects(ctx, subject.ID)
	if err != nil {
		return nil, toGalaxyConnectError(err)
	}
	resp := &galaxyv1.ListProjectsResponse{Projects: make([]*galaxyv1.Project, 0, len(projects))}
	for _, project := range projects {
		view, err := s.galaxy.View(ctx, project)
		if err != nil {
			return nil, toGalaxyConnectError(err)
		}
		resp.Projects = append(resp.Projects, toProtoProject(view))
	}
	return connect.NewResponse(resp), nil
}

// CreateProject 实现 GalaxyService。
func (s *GalaxyService) CreateProject(ctx context.Context, req *connect.Request[galaxyv1.CreateProjectRequest]) (*connect.Response[galaxyv1.CreateProjectResponse], error) {
	subject, err := callerSubject(ctx)
	if err != nil {
		return nil, err
	}
	project, err := s.galaxy.CreateProject(ctx, subject.ID, req.Msg.GetName(), req.Msg.GetDescription())
	if err != nil {
		return nil, toGalaxyConnectError(err)
	}
	view, err := s.galaxy.View(ctx, project)
	if err != nil {
		return nil, toGalaxyConnectError(err)
	}
	// 名称与简介是自由文本，**不进日志**：需要关键入参时记的是标识。
	s.logger.Info("已创建工程",
		zap.String("project_id", project.ID),
		zap.String("subject_id", subject.ID))
	return connect.NewResponse(&galaxyv1.CreateProjectResponse{Project: toProtoProject(view)}), nil
}

// GetProject 实现 GalaxyService。
func (s *GalaxyService) GetProject(ctx context.Context, req *connect.Request[galaxyv1.GetProjectRequest]) (*connect.Response[galaxyv1.GetProjectResponse], error) {
	subject, err := callerSubject(ctx)
	if err != nil {
		return nil, err
	}
	project, err := s.galaxy.GetProject(ctx, subject.ID, req.Msg.GetProjectId())
	if err != nil {
		return nil, toGalaxyConnectError(err)
	}
	view, err := s.galaxy.View(ctx, project)
	if err != nil {
		return nil, toGalaxyConnectError(err)
	}
	return connect.NewResponse(&galaxyv1.GetProjectResponse{Project: toProtoProject(view)}), nil
}

// UpdateProject 实现 GalaxyService。
func (s *GalaxyService) UpdateProject(ctx context.Context, req *connect.Request[galaxyv1.UpdateProjectRequest]) (*connect.Response[galaxyv1.UpdateProjectResponse], error) {
	subject, err := callerSubject(ctx)
	if err != nil {
		return nil, err
	}
	project, err := s.galaxy.UpdateProject(ctx, subject.ID, req.Msg.GetProjectId(), req.Msg.GetName(), req.Msg.GetDescription())
	if err != nil {
		return nil, toGalaxyConnectError(err)
	}
	view, err := s.galaxy.View(ctx, project)
	if err != nil {
		return nil, toGalaxyConnectError(err)
	}
	s.logger.Info("已更新工程元数据",
		zap.String("project_id", project.ID),
		zap.String("subject_id", subject.ID))
	return connect.NewResponse(&galaxyv1.UpdateProjectResponse{Project: toProtoProject(view)}), nil
}

// DeleteProject 实现 GalaxyService：连同版本、资产与发布记录一并删除。
func (s *GalaxyService) DeleteProject(ctx context.Context, req *connect.Request[galaxyv1.DeleteProjectRequest]) (*connect.Response[galaxyv1.DeleteProjectResponse], error) {
	subject, err := callerSubject(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.galaxy.DeleteProject(ctx, subject.ID, req.Msg.GetProjectId()); err != nil {
		return nil, toGalaxyConnectError(err)
	}
	s.logger.Info("已删除工程",
		zap.String("project_id", req.Msg.GetProjectId()),
		zap.String("subject_id", subject.ID))
	return connect.NewResponse(&galaxyv1.DeleteProjectResponse{}), nil
}

// GetDraft 实现 GalaxyService。没有草稿行时返回一份空草稿，而不是错误。
func (s *GalaxyService) GetDraft(ctx context.Context, req *connect.Request[galaxyv1.GetDraftRequest]) (*connect.Response[galaxyv1.GetDraftResponse], error) {
	subject, err := callerSubject(ctx)
	if err != nil {
		return nil, err
	}
	draft, err := s.galaxy.GetDraft(ctx, subject.ID, req.Msg.GetProjectId())
	if err != nil {
		return nil, toGalaxyConnectError(err)
	}
	return connect.NewResponse(&galaxyv1.GetDraftResponse{Draft: toProtoDraft(draft)}), nil
}

// SaveDraft 实现 GalaxyService。
func (s *GalaxyService) SaveDraft(ctx context.Context, req *connect.Request[galaxyv1.SaveDraftRequest]) (*connect.Response[galaxyv1.SaveDraftResponse], error) {
	subject, err := callerSubject(ctx)
	if err != nil {
		return nil, err
	}
	draft, err := s.galaxy.SaveDraft(ctx, subject.ID, req.Msg.GetProjectId(), galaxy.Document(req.Msg.GetContent()))
	if err != nil {
		return nil, toGalaxyConnectError(err)
	}
	// 只记字节数，**不记正文**：正文是用户内容，全文进日志既无必要也可能含隐私。
	s.logger.Info("已保存草稿",
		zap.String("project_id", req.Msg.GetProjectId()),
		zap.String("subject_id", subject.ID),
		zap.Int("bytes", len(req.Msg.GetContent())))
	return connect.NewResponse(&galaxyv1.SaveDraftResponse{Draft: toProtoDraft(draft)}), nil
}

// SaveVersion 实现 GalaxyService：把草稿的当前内容冻结成一个版本。
func (s *GalaxyService) SaveVersion(ctx context.Context, req *connect.Request[galaxyv1.SaveVersionRequest]) (*connect.Response[galaxyv1.SaveVersionResponse], error) {
	subject, err := callerSubject(ctx)
	if err != nil {
		return nil, err
	}
	version, err := s.galaxy.SaveVersion(ctx, subject.ID, req.Msg.GetProjectId())
	if err != nil {
		return nil, toGalaxyConnectError(err)
	}
	return connect.NewResponse(&galaxyv1.SaveVersionResponse{Version: toProtoVersion(version, false)}), nil
}

// ListVersions 实现 GalaxyService（列表不带正文）。
func (s *GalaxyService) ListVersions(ctx context.Context, req *connect.Request[galaxyv1.ListVersionsRequest]) (*connect.Response[galaxyv1.ListVersionsResponse], error) {
	subject, err := callerSubject(ctx)
	if err != nil {
		return nil, err
	}
	versions, err := s.galaxy.ListVersions(ctx, subject.ID, req.Msg.GetProjectId())
	if err != nil {
		return nil, toGalaxyConnectError(err)
	}
	resp := &galaxyv1.ListVersionsResponse{Versions: make([]*galaxyv1.Version, 0, len(versions))}
	for _, version := range versions {
		resp.Versions = append(resp.Versions, toProtoVersion(version, false))
	}
	return connect.NewResponse(resp), nil
}

// GetVersion 实现 GalaxyService（它带正文）。
func (s *GalaxyService) GetVersion(ctx context.Context, req *connect.Request[galaxyv1.GetVersionRequest]) (*connect.Response[galaxyv1.GetVersionResponse], error) {
	subject, err := callerSubject(ctx)
	if err != nil {
		return nil, err
	}
	version, err := s.galaxy.GetVersion(ctx, subject.ID, req.Msg.GetProjectId(), req.Msg.GetVersionId())
	if err != nil {
		return nil, toGalaxyConnectError(err)
	}
	return connect.NewResponse(&galaxyv1.GetVersionResponse{Version: toProtoVersion(version, true)}), nil
}

// DeleteVersion 实现 GalaxyService。被当前发布指向的版本不可删。
func (s *GalaxyService) DeleteVersion(ctx context.Context, req *connect.Request[galaxyv1.DeleteVersionRequest]) (*connect.Response[galaxyv1.DeleteVersionResponse], error) {
	subject, err := callerSubject(ctx)
	if err != nil {
		return nil, err
	}
	err = s.galaxy.DeleteVersion(ctx, subject.ID, req.Msg.GetProjectId(), req.Msg.GetVersionId())
	if err != nil {
		return nil, toGalaxyConnectError(err)
	}
	s.logger.Info("已删除版本",
		zap.String("project_id", req.Msg.GetProjectId()),
		zap.String("version_id", req.Msg.GetVersionId()),
		zap.String("subject_id", subject.ID))
	return connect.NewResponse(&galaxyv1.DeleteVersionResponse{}), nil
}

// ValidateContent 实现 GalaxyService。
//
// 它是**编辑器提示与发布前置校验共用的那一个入口**（见 docs/ssot-registry.md）：
// 返回的是问题清单而不是单个错误，因为编辑器要的是"哪几处有问题"。存储故障
// 仍以 RPC 错误返回，与"正文有问题"分开。
func (s *GalaxyService) ValidateContent(ctx context.Context, req *connect.Request[galaxyv1.ValidateContentRequest]) (*connect.Response[galaxyv1.ValidateContentResponse], error) {
	subject, err := callerSubject(ctx)
	if err != nil {
		return nil, err
	}
	report, err := s.galaxy.ValidateContent(ctx, subject.ID, req.Msg.GetProjectId(), galaxy.Document(req.Msg.GetContent()))
	if err != nil {
		return nil, toGalaxyConnectError(err)
	}
	resp := &galaxyv1.ValidateContentResponse{Problems: make([]*galaxyv1.ValidationProblem, 0, len(report.Problems))}
	for _, problem := range report.Problems {
		resp.Problems = append(resp.Problems, &galaxyv1.ValidationProblem{Message: problem.Message})
	}
	return connect.NewResponse(resp), nil
}

// ListAssets 实现 GalaxyService。
func (s *GalaxyService) ListAssets(ctx context.Context, req *connect.Request[galaxyv1.ListAssetsRequest]) (*connect.Response[galaxyv1.ListAssetsResponse], error) {
	subject, err := callerSubject(ctx)
	if err != nil {
		return nil, err
	}
	assets, err := s.galaxy.ListAssets(ctx, subject.ID, req.Msg.GetProjectId())
	if err != nil {
		return nil, toGalaxyConnectError(err)
	}
	resp := &galaxyv1.ListAssetsResponse{Assets: make([]*galaxyv1.Asset, 0, len(assets))}
	for _, asset := range assets {
		resp.Assets = append(resp.Assets, toProtoAsset(asset))
	}
	return connect.NewResponse(resp), nil
}

// BeginAssetUpload 实现 GalaxyService：分配资产标识并签发直传凭证。
//
// **字节不经过本服务端**（见 docs/design/objectstore/README.md）。凭证**不进日志**：
// 把它写下来等于把一次写入的能力留在了日志文件里。
func (s *GalaxyService) BeginAssetUpload(ctx context.Context, req *connect.Request[galaxyv1.BeginAssetUploadRequest]) (*connect.Response[galaxyv1.BeginAssetUploadResponse], error) {
	subject, err := callerSubject(ctx)
	if err != nil {
		return nil, err
	}
	declaredSize, err := declaredBytes(req.Msg.GetSizeBytes())
	if err != nil {
		return nil, err
	}
	assetID, credential, err := s.galaxy.BeginAssetUpload(ctx, subject.ID, req.Msg.GetProjectId(),
		req.Msg.GetContentType(), declaredSize)
	if err != nil {
		return nil, toGalaxyConnectError(err)
	}
	s.logger.Info("已签发资产直传凭证",
		zap.String("project_id", req.Msg.GetProjectId()),
		zap.String("asset_id", assetID),
		zap.String("subject_id", subject.ID),
		zap.String("content_type", req.Msg.GetContentType()),
		zap.Uint64("declared_bytes", req.Msg.GetSizeBytes()))
	return connect.NewResponse(&galaxyv1.BeginAssetUploadResponse{
		AssetId: assetID,
		Upload:  toProtoUpload(credential),
	}), nil
}

// CommitAssetUpload 实现 GalaxyService：核对对象确实到了，写入资产元数据。
func (s *GalaxyService) CommitAssetUpload(ctx context.Context, req *connect.Request[galaxyv1.CommitAssetUploadRequest]) (*connect.Response[galaxyv1.CommitAssetUploadResponse], error) {
	subject, err := callerSubject(ctx)
	if err != nil {
		return nil, err
	}
	asset, err := s.galaxy.CommitAssetUpload(ctx, subject.ID, req.Msg.GetProjectId(), req.Msg.GetAssetId(),
		req.Msg.GetContentType(), req.Msg.GetDigest(), req.Msg.GetFilename())
	if err != nil {
		return nil, toGalaxyConnectError(err)
	}
	url, err := s.galaxy.AssetURL(ctx, subject.ID, asset.ProjectID, asset.ID)
	if err != nil {
		return nil, toGalaxyConnectError(err)
	}
	return connect.NewResponse(&galaxyv1.CommitAssetUploadResponse{
		Asset: toProtoAsset(galaxy.AssetView{Asset: asset, URL: url}),
	}), nil
}

// DeleteAsset 实现 GalaxyService。被任一版本引用时拒绝。
func (s *GalaxyService) DeleteAsset(ctx context.Context, req *connect.Request[galaxyv1.DeleteAssetRequest]) (*connect.Response[galaxyv1.DeleteAssetResponse], error) {
	subject, err := callerSubject(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.galaxy.DeleteAsset(ctx, subject.ID, req.Msg.GetProjectId(), req.Msg.GetAssetId()); err != nil {
		return nil, toGalaxyConnectError(err)
	}
	s.logger.Info("已删除资产",
		zap.String("project_id", req.Msg.GetProjectId()),
		zap.String("asset_id", req.Msg.GetAssetId()),
		zap.String("subject_id", subject.ID))
	return connect.NewResponse(&galaxyv1.DeleteAssetResponse{}), nil
}

// Publish 实现 GalaxyService：受理与校验、资产上架、产物落库、切换与生效。
func (s *GalaxyService) Publish(ctx context.Context, req *connect.Request[galaxyv1.PublishRequest]) (*connect.Response[galaxyv1.PublishResponse], error) {
	subject, err := callerSubject(ctx)
	if err != nil {
		return nil, err
	}
	publication, err := s.galaxy.Publish(ctx, subject.ID, req.Msg.GetProjectId(), req.Msg.GetVersionId())
	if err != nil {
		return nil, toGalaxyConnectError(err)
	}
	project, err := s.galaxy.GetProject(ctx, subject.ID, req.Msg.GetProjectId())
	if err != nil {
		return nil, toGalaxyConnectError(err)
	}
	view, err := s.galaxy.View(ctx, project)
	if err != nil {
		return nil, toGalaxyConnectError(err)
	}
	return connect.NewResponse(&galaxyv1.PublishResponse{
		Publication: toProtoPublication(publication, view.PublishedURL),
		Project:     toProtoProject(view),
	}), nil
}

// Unpublish 实现 GalaxyService：把发布指针置空，地址立刻不可达。
func (s *GalaxyService) Unpublish(ctx context.Context, req *connect.Request[galaxyv1.UnpublishRequest]) (*connect.Response[galaxyv1.UnpublishResponse], error) {
	subject, err := callerSubject(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.galaxy.Unpublish(ctx, subject.ID, req.Msg.GetProjectId()); err != nil {
		return nil, toGalaxyConnectError(err)
	}
	project, err := s.galaxy.GetProject(ctx, subject.ID, req.Msg.GetProjectId())
	if err != nil {
		return nil, toGalaxyConnectError(err)
	}
	view, err := s.galaxy.View(ctx, project)
	if err != nil {
		return nil, toGalaxyConnectError(err)
	}
	s.logger.Info("已撤回发布",
		zap.String("project_id", req.Msg.GetProjectId()),
		zap.String("subject_id", subject.ID))
	return connect.NewResponse(&galaxyv1.UnpublishResponse{Project: toProtoProject(view)}), nil
}

// toProtoCapabilities 把能力边界翻译成接口类型。
func toProtoCapabilities(capabilities galaxy.Capabilities) *galaxyv1.Capabilities {
	limits := make([]*galaxyv1.AssetKindLimit, 0, len(capabilities.AssetLimits))
	for _, limit := range capabilities.AssetLimits {
		limits = append(limits, &galaxyv1.AssetKindLimit{
			Kind:     toProtoMediaKind(limit.Kind),
			MaxBytes: fitUint32(limit.MaxBytes),
		})
	}
	return &galaxyv1.Capabilities{
		AssetUploadEnabled: capabilities.AssetUploadEnabled,
		PublishEnabled:     capabilities.PublishEnabled,
		MaxDocumentBytes:   fitUint32(capabilities.MaxDocumentBytes),
		MaxArtifactBytes:   fitUint32(capabilities.MaxArtifactBytes),
		AssetLimits:        limits,
	}
}

// toProtoMediaKind 把类别翻译成接口枚举。
//
// 默认分支返回 UNSPECIFIED 而不是 IMAGE：把"未知"表现成一个合法取值，会让
// 客户端拿到一个看起来正常、实际表示别的东西的枚举。
func toProtoMediaKind(kind galaxy.MediaKind) galaxyv1.MediaKind {
	switch kind {
	case galaxy.MediaKindImage:
		return galaxyv1.MediaKind_MEDIA_KIND_IMAGE
	case galaxy.MediaKindVideo:
		return galaxyv1.MediaKind_MEDIA_KIND_VIDEO
	case galaxy.MediaKindAudio:
		return galaxyv1.MediaKind_MEDIA_KIND_AUDIO
	default:
		return galaxyv1.MediaKind_MEDIA_KIND_UNSPECIFIED
	}
}

// toProtoProject 把工程视图翻译成接口类型。
//
// published_url 只在发布态给出：未发布时它是空串，而**不是**一个"以后会可用"
// 的地址——把地址提前显示出来会让人以为页面已经能打开了。
func toProtoProject(view galaxy.ProjectView) *galaxyv1.Project {
	project := &galaxyv1.Project{
		Id:          view.Project.ID,
		Name:        view.Project.Name,
		Description: view.Project.Description,
		CreatedAt:   view.Project.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:   view.Project.UpdatedAt.UTC().Format(time.RFC3339),
		Published:   view.Published,
	}
	if view.Published {
		project.PublishedVersionId = view.Publication.VersionID
		project.PublishedAt = view.Publication.PublishedAt.UTC().Format(time.RFC3339)
		project.PublishedUrl = view.PublishedURL
	}
	return project
}

// toProtoDraft 把草稿翻译成接口类型。更新时间只在保存过之后才有值。
func toProtoDraft(draft galaxy.Draft) *galaxyv1.Draft {
	out := &galaxyv1.Draft{Content: string(draft.Content)}
	if !draft.UpdatedAt.IsZero() {
		out.UpdatedAt = draft.UpdatedAt.UTC().Format(time.RFC3339)
	}
	return out
}

// toProtoVersion 把版本翻译成接口类型。
//
// withContent 为假时**不带正文**：版本列表会把每个版本的正文一起读上来，而列表
// 根本不显示它们（见 ListVersions 的 proto 说明）。
func toProtoVersion(version galaxy.Version, withContent bool) *galaxyv1.Version {
	out := &galaxyv1.Version{
		Id:      version.ID,
		Seq:     version.Seq,
		SavedAt: version.SavedAt.UTC().Format(time.RFC3339),
	}
	if withContent {
		out.Content = string(version.Content)
	}
	return out
}

// toProtoAsset 把资产视图翻译成接口类型。
func toProtoAsset(view galaxy.AssetView) *galaxyv1.Asset {
	return &galaxyv1.Asset{
		Id:         view.Asset.ID,
		MediaType:  view.Asset.MediaType,
		SizeBytes:  fitUint64(view.Asset.SizeBytes),
		Filename:   view.Asset.Filename,
		UploadedAt: view.Asset.UploadedAt.UTC().Format(time.RFC3339),
		Url:        view.URL,
	}
}

// toProtoPublication 把发布记录翻译成接口类型。
func toProtoPublication(publication galaxy.Publication, pageURL string) *galaxyv1.Publication {
	return &galaxyv1.Publication{
		Id:          publication.ID,
		ProjectId:   publication.ProjectID,
		VersionId:   publication.VersionID,
		PublishedAt: publication.PublishedAt.UTC().Format(time.RFC3339),
		Url:         pageURL,
	}
}

// toGalaxyConnectError 把创作相关的错误映射为 Connect 错误。
//
// 分类依据是"调用方该做什么"，而不是错误来自哪一层：
//
//   - 存储故障 → Unavailable（退避重试）；
//   - 取值不合法（名称过长、正文超限、类型不在白名单、摘要形状不对）→
//     InvalidArgument（改了再试，重试没用）；
//   - 正文不能发布 → InvalidArgument（消息里带具体位置）；
//   - 本部署未启用资产或发布 → FailedPrecondition（换一种做法）；
//   - 状态不允许（版本正被发布、资产被引用、上传没完成）→ FailedPrecondition；
//   - 目标不存在**或不属于调用者** → NotFound（同一个结论，见领域层）。
func toGalaxyConnectError(err error) error {
	switch {
	case errors.Is(err, galaxy.ErrStoreUnavailable),
		errors.Is(err, objectstore.ErrStoreUnavailable),
		errors.Is(err, galaxy.ErrPublicStoreUnavailable),
		errors.Is(err, rbac.ErrStoreUnavailable):
		return connect.NewError(connect.CodeUnavailable, errors.New("服务暂时不可用，请稍后重试"))
	case errors.Is(err, galaxy.ErrProjectNotFound):
		return connect.NewError(connect.CodeNotFound, errors.New("工程不存在"))
	case errors.Is(err, galaxy.ErrVersionNotFound):
		return connect.NewError(connect.CodeNotFound, errors.New("版本不存在"))
	case errors.Is(err, galaxy.ErrAssetNotFound):
		return connect.NewError(connect.CodeNotFound, errors.New("资产不存在"))
	case errors.Is(err, galaxy.ErrProjectNameTooLong):
		return connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("工程名称不能超过 %d 个字", galaxy.ProjectNameMaxRunes))
	case errors.Is(err, galaxy.ErrProjectDescriptionTooLong):
		return connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("工程简介不能超过 %d 个字", galaxy.ProjectDescriptionMaxRunes))
	case errors.Is(err, galaxy.ErrDocumentTooLarge):
		return connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("正文不能超过 %d KiB", galaxy.MaxDocumentBytes/1024))
	case errors.Is(err, galaxy.ErrArtifactTooLarge):
		return connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("发布产物不能超过 %d KiB", galaxy.MaxArtifactBytes/1024))
	case errors.Is(err, galaxy.ErrAssetTypeNotAllowed):
		return connect.NewError(connect.CodeInvalidArgument, errors.New("这个类型不在允许的范围内"))
	case errors.Is(err, galaxy.ErrAssetDigestInvalid):
		return connect.NewError(connect.CodeInvalidArgument, errors.New("内容摘要的形状不合法"))
	case errors.Is(err, galaxy.ErrAssetTooLarge):
		return connect.NewError(connect.CodeInvalidArgument, errors.New("文件超过该类别的上限"))
	case errors.Is(err, galaxy.ErrInvalidContent):
		// 消息里带具体位置（第几行、哪一处），因此原样透出。
		return connect.NewError(connect.CodeInvalidArgument, errors.New(err.Error()))
	case errors.Is(err, galaxy.ErrAssetUnavailable):
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("本部署未启用资产功能"))
	case errors.Is(err, galaxy.ErrPublishUnavailable):
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("本部署未启用发布功能"))
	case errors.Is(err, galaxy.ErrVersionPublished):
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("这个版本正在被发布，先撤回或改发布别的版本"))
	case errors.Is(err, galaxy.ErrAssetReferenced):
		return connect.NewError(connect.CodeFailedPrecondition,
			fmt.Errorf("资产仍被版本引用，不能删除：%s", err.Error()))
	case errors.Is(err, galaxy.ErrAssetObjectMissing):
		return connect.NewError(connect.CodeFailedPrecondition,
			errors.New("上传没有完成：对象不存在，请重试"))
	case errors.Is(err, galaxy.ErrAssetDigestMismatch):
		return connect.NewError(connect.CodeFailedPrecondition,
			errors.New("资产字节与声明的摘要不符，发布已中止"))
	case errors.Is(err, rbac.ErrSubjectNotFound):
		return connect.NewError(connect.CodeNotFound, errors.New("主体不存在或已停用"))
	default:
		return connect.NewError(connect.CodeInternal, errors.New("创作服务内部错误"))
	}
}
