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

// GalaxyService 是创作面（"用户放下一组具名文件，再把它发布成一个站点"）的
// RPC 实现。
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
	form := fromProtoSiteForm(req.Msg.GetForm())
	project, err := s.galaxy.CreateProject(ctx, subject.ID, req.Msg.GetName(), req.Msg.GetDescription(), form)
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
		zap.String("subject_id", subject.ID),
		zap.String("form", string(form)))
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

// GetDraft 实现 GalaxyService。没有草稿行时返回一份空清单，而不是错误。
func (s *GalaxyService) GetDraft(ctx context.Context, req *connect.Request[galaxyv1.GetDraftRequest]) (*connect.Response[galaxyv1.GetDraftResponse], error) {
	subject, err := callerSubject(ctx)
	if err != nil {
		return nil, err
	}
	draft, entries, err := s.galaxy.GetDraft(ctx, subject.ID, req.Msg.GetProjectId())
	if err != nil {
		return nil, toGalaxyConnectError(err)
	}
	return connect.NewResponse(&galaxyv1.GetDraftResponse{Draft: toProtoDraft(draft, entries)}), nil
}

// PushDraft 实现 GalaxyService：以给定的清单整组替换草稿。
//
// 请求里只有引用（内容摘要与资产标识），**不带字节**：字节由客户端在调用本方法
// 之前直传进对象存储。因此本方法没有大请求体，也不做任何上传。
func (s *GalaxyService) PushDraft(ctx context.Context, req *connect.Request[galaxyv1.PushDraftRequest]) (*connect.Response[galaxyv1.PushDraftResponse], error) {
	subject, err := callerSubject(ctx)
	if err != nil {
		return nil, err
	}
	entries, err := fromProtoEntries(req.Msg.GetEntries())
	if err != nil {
		return nil, err
	}
	draft, err := s.galaxy.PushDraft(ctx, subject.ID, req.Msg.GetProjectId(), entries)
	if err != nil {
		return nil, toGalaxyConnectError(err)
	}
	// 只记文件数，**不记路径清单**：清单是用户内容的一部分，全文进日志既无
	// 必要也可能含隐私。
	s.logger.Info("已整组替换草稿",
		zap.String("project_id", req.Msg.GetProjectId()),
		zap.String("subject_id", subject.ID),
		zap.Int("files", len(draft.Manifest)))
	// 返回的清单不再附地址：客户端刚给的就是它，再签一遍地址只是多一批会过期
	// 的字符串。
	return connect.NewResponse(&galaxyv1.PushDraftResponse{Draft: toProtoDraft(draft, nil)}), nil
}

// SaveVersion 实现 GalaxyService：把草稿的当前清单冻结成一个版本。
func (s *GalaxyService) SaveVersion(ctx context.Context, req *connect.Request[galaxyv1.SaveVersionRequest]) (*connect.Response[galaxyv1.SaveVersionResponse], error) {
	subject, err := callerSubject(ctx)
	if err != nil {
		return nil, err
	}
	version, err := s.galaxy.SaveVersion(ctx, subject.ID, req.Msg.GetProjectId())
	if err != nil {
		return nil, toGalaxyConnectError(err)
	}
	return connect.NewResponse(&galaxyv1.SaveVersionResponse{Version: toProtoVersion(version, nil)}), nil
}

// ListVersions 实现 GalaxyService（清单随行，不带读取地址）。
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
		resp.Versions = append(resp.Versions, toProtoVersion(version, nil))
	}
	return connect.NewResponse(resp), nil
}

// GetVersion 实现 GalaxyService（它带每一项的短时读取地址）。
func (s *GalaxyService) GetVersion(ctx context.Context, req *connect.Request[galaxyv1.GetVersionRequest]) (*connect.Response[galaxyv1.GetVersionResponse], error) {
	subject, err := callerSubject(ctx)
	if err != nil {
		return nil, err
	}
	version, entries, err := s.galaxy.GetVersion(ctx, subject.ID, req.Msg.GetProjectId(), req.Msg.GetVersionId())
	if err != nil {
		return nil, toGalaxyConnectError(err)
	}
	return connect.NewResponse(&galaxyv1.GetVersionResponse{Version: toProtoVersion(version, entries)}), nil
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

// ValidateDraft 实现 GalaxyService。
//
// 它是**界面提示与发布前置校验共用的那一个入口**（见 docs/ssot-registry.md）：
// 返回的是问题清单而不是单个错误，因为界面要的是"哪几处有问题"。存储故障仍以
// RPC 错误返回，与"内容有问题"分开。
//
// 它校验的是**已保存的草稿**：写入只有一条路径（命令行整组推送），因此"校验
// 一份还没保存的内容"这个形状不存在。
func (s *GalaxyService) ValidateDraft(ctx context.Context, req *connect.Request[galaxyv1.ValidateDraftRequest]) (*connect.Response[galaxyv1.ValidateDraftResponse], error) {
	subject, err := callerSubject(ctx)
	if err != nil {
		return nil, err
	}
	report, err := s.galaxy.ValidateDraft(ctx, subject.ID, req.Msg.GetProjectId())
	if err != nil {
		return nil, toGalaxyConnectError(err)
	}
	resp := &galaxyv1.ValidateDraftResponse{Problems: make([]*galaxyv1.ValidationProblem, 0, len(report.Problems))}
	for _, problem := range report.Problems {
		resp.Problems = append(resp.Problems, &galaxyv1.ValidationProblem{
			Message: problem.Message,
			Path:    problem.Path,
			Line:    fitInt32(problem.Line),
		})
	}
	return connect.NewResponse(resp), nil
}

// PreviewDraft 实现 GalaxyService：把草稿渲染成一份能给沙箱 iframe 的 HTML。
func (s *GalaxyService) PreviewDraft(ctx context.Context, req *connect.Request[galaxyv1.PreviewDraftRequest]) (*connect.Response[galaxyv1.PreviewDraftResponse], error) {
	subject, err := callerSubject(ctx)
	if err != nil {
		return nil, err
	}
	html, err := s.galaxy.PreviewDraft(ctx, subject.ID, req.Msg.GetProjectId(), req.Msg.GetPath())
	if err != nil {
		return nil, toGalaxyConnectError(err)
	}
	return connect.NewResponse(&galaxyv1.PreviewDraftResponse{Html: string(html)}), nil
}

// BeginContentUpload 实现 GalaxyService：签发一份内容对象的直传凭证。
//
// **字节不经过本服务端**（见 docs/design/objectstore/README.md）。凭证**不进
// 日志**：把它写下来等于把一次写入的能力留在了日志文件里。
func (s *GalaxyService) BeginContentUpload(ctx context.Context, req *connect.Request[galaxyv1.BeginContentUploadRequest]) (*connect.Response[galaxyv1.BeginContentUploadResponse], error) {
	subject, err := callerSubject(ctx)
	if err != nil {
		return nil, err
	}
	declaredSize, err := declaredBytes(req.Msg.GetSizeBytes())
	if err != nil {
		return nil, err
	}
	exists, credential, err := s.galaxy.BeginContentUpload(ctx, subject.ID, req.Msg.GetProjectId(),
		req.Msg.GetDigest(), declaredSize)
	if err != nil {
		return nil, toGalaxyConnectError(err)
	}
	if exists {
		return connect.NewResponse(&galaxyv1.BeginContentUploadResponse{AlreadyExists: true}), nil
	}
	s.logger.Info("已签发内容对象直传凭证",
		zap.String("project_id", req.Msg.GetProjectId()),
		zap.String("subject_id", subject.ID),
		zap.String("digest", req.Msg.GetDigest()),
		zap.Uint64("declared_bytes", req.Msg.GetSizeBytes()))
	return connect.NewResponse(&galaxyv1.BeginContentUploadResponse{Upload: toProtoUpload(credential)}), nil
}

// CommitContentUpload 实现 GalaxyService：读回对象、核对摘要。
func (s *GalaxyService) CommitContentUpload(ctx context.Context, req *connect.Request[galaxyv1.CommitContentUploadRequest]) (*connect.Response[galaxyv1.CommitContentUploadResponse], error) {
	subject, err := callerSubject(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.galaxy.CommitContentUpload(ctx, subject.ID, req.Msg.GetProjectId(), req.Msg.GetDigest()); err != nil {
		return nil, toGalaxyConnectError(err)
	}
	return connect.NewResponse(&galaxyv1.CommitContentUploadResponse{}), nil
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

// DeleteAsset 实现 GalaxyService。被任一版本的文件清单引用时拒绝。
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

// Publish 实现 GalaxyService：受理与校验、媒体上架、产物落库、切换与生效。
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
		MaxTextBytes:       fitUint32(capabilities.MaxTextBytes),
		MaxFileSetBytes:    fitUint32(capabilities.MaxFileSetBytes),
		MaxFiles:           fitUint32(capabilities.MaxFiles),
		AssetLimits:        limits,
	}
}

// fromProtoSiteForm 把接口枚举翻译成领域取值。
//
// **UNSPECIFIED 落到零值**，而零值不是一种合法形态——创建工程时因此会收到
// "形态不合法"，而不是被静默地当成 static。
func fromProtoSiteForm(form galaxyv1.SiteForm) galaxy.SiteForm {
	switch form {
	case galaxyv1.SiteForm_SITE_FORM_STATIC:
		return galaxy.SiteFormStatic
	case galaxyv1.SiteForm_SITE_FORM_DOCS:
		return galaxy.SiteFormDocs
	default:
		return ""
	}
}

// toProtoSiteForm 把领域取值翻译成接口枚举。
func toProtoSiteForm(form galaxy.SiteForm) galaxyv1.SiteForm {
	switch form {
	case galaxy.SiteFormStatic:
		return galaxyv1.SiteForm_SITE_FORM_STATIC
	case galaxy.SiteFormDocs:
		return galaxyv1.SiteForm_SITE_FORM_DOCS
	default:
		return galaxyv1.SiteForm_SITE_FORM_UNSPECIFIED
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
	case galaxy.MediaKindFont:
		return galaxyv1.MediaKind_MEDIA_KIND_FONT
	default:
		return galaxyv1.MediaKind_MEDIA_KIND_UNSPECIFIED
	}
}

// toProtoProject 把工程视图翻译成接口类型。
//
// published_url 只在发布态给出：未发布时它是空串，而**不是**一个"以后会可用"
// 的地址——把地址提前显示出来会让人以为站点已经能打开了。base_url 相反：它是
// 构建要用的，与"有没有发出去"无关。
func toProtoProject(view galaxy.ProjectView) *galaxyv1.Project {
	project := &galaxyv1.Project{
		Id:          view.Project.ID,
		Name:        view.Project.Name,
		Description: view.Project.Description,
		Form:        toProtoSiteForm(view.Project.Form),
		CreatedAt:   view.Project.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:   view.Project.UpdatedAt.UTC().Format(time.RFC3339),
		Published:   view.Published,
		BaseUrl:     view.BaseURL,
	}
	if view.Published {
		project.PublishedVersionId = view.Publication.VersionID
		project.PublishedAt = view.Publication.PublishedAt.UTC().Format(time.RFC3339)
		project.PublishedUrl = view.PublishedURL
	}
	return project
}

// toProtoDraft 把草稿翻译成接口类型。
func toProtoDraft(draft galaxy.Draft, entries []galaxy.EntryView) *galaxyv1.Draft {
	out := &galaxyv1.Draft{Entries: toProtoEntries(entries, draft.Manifest)}
	if !draft.UpdatedAt.IsZero() {
		out.UpdatedAt = draft.UpdatedAt.UTC().Format(time.RFC3339)
	}
	return out
}

// toProtoVersion 把版本翻译成接口类型。
//
// entries 为空时按清单原样给出（不带地址）：版本列表要的是"这一版有哪些文件"，
// 而地址是一份会过期的凭证，列表不该下发一批。
func toProtoVersion(version galaxy.Version, entries []galaxy.EntryView) *galaxyv1.Version {
	return &galaxyv1.Version{
		Id:                 version.ID,
		Seq:                version.Seq,
		SavedAt:            version.SavedAt.UTC().Format(time.RFC3339),
		Entries:            toProtoEntries(entries, version.Manifest),
		RenderRulesVersion: fitInt32(version.RenderRulesVersion),
	}
}

// toProtoEntries 把清单（可带地址）翻译成接口类型（唯一入口）。
//
// entries 为 nil 时按 manifest 原样给出，url 留空。
func toProtoEntries(entries []galaxy.EntryView, manifest galaxy.Manifest) []*galaxyv1.FileEntry {
	if entries == nil {
		entries = make([]galaxy.EntryView, 0, len(manifest))
		for _, entry := range manifest {
			entries = append(entries, galaxy.EntryView{Entry: entry})
		}
	}
	out := make([]*galaxyv1.FileEntry, 0, len(entries))
	for _, view := range entries {
		out = append(out, toProtoEntry(view))
	}
	return out
}

// toProtoEntry 把一条条目翻译成接口类型。
func toProtoEntry(view galaxy.EntryView) *galaxyv1.FileEntry {
	entry := &galaxyv1.FileEntry{Path: view.Entry.Path, Url: view.URL}
	switch view.Entry.Kind {
	case galaxy.EntryKindAsset:
		entry.Source = &galaxyv1.FileEntry_AssetId{AssetId: view.Entry.AssetID}
	default:
		entry.Source = &galaxyv1.FileEntry_Digest{Digest: view.Entry.Digest}
	}
	return entry
}

// fromProtoEntries 把接口类型翻译成清单条目（唯一入口）。
//
// 类别由 oneof 里**实际填了哪一个**决定：两者都空或都填是用法错误，而不是
// 一个可以猜的缺省——猜错的后果是一份内容被当成另一种字节来取。
func fromProtoEntries(entries []*galaxyv1.FileEntry) ([]galaxy.Entry, error) {
	out := make([]galaxy.Entry, 0, len(entries))
	for _, entry := range entries {
		converted := galaxy.Entry{Path: entry.GetPath()}
		switch source := entry.GetSource().(type) {
		case *galaxyv1.FileEntry_Digest:
			if source.Digest == "" {
				return nil, connect.NewError(connect.CodeInvalidArgument,
					fmt.Errorf("条目 %q 的内容摘要为空", entry.GetPath()))
			}
			converted.Kind = galaxy.EntryKindText
			converted.Digest = source.Digest
		case *galaxyv1.FileEntry_AssetId:
			if source.AssetId == "" {
				return nil, connect.NewError(connect.CodeInvalidArgument,
					fmt.Errorf("条目 %q 的资产标识为空", entry.GetPath()))
			}
			converted.Kind = galaxy.EntryKindAsset
			converted.AssetID = source.AssetId
		default:
			return nil, connect.NewError(connect.CodeInvalidArgument,
				fmt.Errorf("条目 %q 既没有内容摘要也没有资产标识", entry.GetPath()))
		}
		out = append(out, converted)
	}
	return out, nil
}

// toProtoAsset 把资产视图翻译成接口类型。
func toProtoAsset(view galaxy.AssetView) *galaxyv1.Asset {
	return &galaxyv1.Asset{
		Id:         view.Asset.ID,
		MediaType:  view.Asset.MediaType,
		Kind:       toProtoMediaKind(view.Asset.MediaKind),
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
//   - 取值不合法（名称过长、路径不合法、类型不在白名单、摘要形状不对、清单
//     不成型）→ InvalidArgument（改了再试，重试没用）；
//   - 内容不能发布 → InvalidArgument（消息里带具体位置）；
//   - 本部署未启用对象存储或发布 → FailedPrecondition（换一种做法）；
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
	case errors.Is(err, galaxy.ErrSiteFormInvalid):
		return connect.NewError(connect.CodeInvalidArgument, errors.New("工程形态必须是 static 或 docs"))
	case errors.Is(err, galaxy.ErrEntryPathInvalid):
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("文件路径不合法：%s", err.Error()))
	case errors.Is(err, galaxy.ErrEntrySetInvalid):
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("文件清单不合法：%s", err.Error()))
	case errors.Is(err, galaxy.ErrDigestInvalid):
		return connect.NewError(connect.CodeInvalidArgument, errors.New("内容摘要的形状不合法"))
	case errors.Is(err, galaxy.ErrTextTooLarge):
		return connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("单份文本不能超过 %d KiB", galaxy.MaxTextBytes/1024))
	case errors.Is(err, galaxy.ErrFileSetTooLarge):
		return connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("整组内容不能超过 %d KiB", galaxy.MaxFileSetBytes/1024))
	case errors.Is(err, galaxy.ErrTooManyFiles):
		return connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("文件数不能超过 %d 个", galaxy.MaxFiles))
	case errors.Is(err, galaxy.ErrDigestMismatch):
		return connect.NewError(connect.CodeFailedPrecondition,
			errors.New("字节与声明的摘要不符，上传已中止"))
	case errors.Is(err, galaxy.ErrAssetTypeNotAllowed):
		return connect.NewError(connect.CodeInvalidArgument, errors.New("这个类型不在允许的范围内"))
	case errors.Is(err, galaxy.ErrAssetTooLarge):
		return connect.NewError(connect.CodeInvalidArgument, errors.New("文件超过该类别的上限"))
	case errors.Is(err, galaxy.ErrInvalidContent):
		// 消息里带具体位置（哪一份文件、哪一处），因此原样透出。
		return connect.NewError(connect.CodeInvalidArgument, errors.New(err.Error()))
	case errors.Is(err, galaxy.ErrAssetUnavailable):
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("本部署未配置对象存储"))
	case errors.Is(err, galaxy.ErrPublishUnavailable):
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("本部署未启用发布功能"))
	case errors.Is(err, galaxy.ErrVersionPublished):
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("这个版本正在被发布，先撤回或改发布别的版本"))
	case errors.Is(err, galaxy.ErrAssetReferenced):
		return connect.NewError(connect.CodeFailedPrecondition,
			fmt.Errorf("资产仍被版本引用，不能删除：%s", err.Error()))
	case errors.Is(err, galaxy.ErrAssetObjectMissing), errors.Is(err, galaxy.ErrContentObjectMissing):
		return connect.NewError(connect.CodeFailedPrecondition,
			errors.New("上传没有完成：对象不存在，请重试"))
	case errors.Is(err, rbac.ErrSubjectNotFound):
		return connect.NewError(connect.CodeNotFound, errors.New("主体不存在或已停用"))
	default:
		return connect.NewError(connect.CodeInternal, errors.New("创作服务内部错误"))
	}
}
