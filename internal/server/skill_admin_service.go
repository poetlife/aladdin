package server

import (
	"context"

	"connectrpc.com/connect"
	"go.uber.org/zap"

	skillv1 "github.com/poetlife/aladdin/api/gen/aladdin/skill/v1"
	"github.com/poetlife/aladdin/internal/skill"
)

// SkillAdminService 是技能目录**维护面**的 RPC 实现。
//
// 它与 SkillService 是同一件事的两侧，因此是两个服务：读要 `skill.catalog.read`，
// 写要 `skill.catalog.write`。写在同一份接口里会让"这个服务要不要认证"变成一个
// 需要逐方法去看的问题（见 skill.proto）。
//
// 同样是薄壳：五步纳管、同步的比对、指针的切换都在 internal/skill，本包只做协议层
// 的取参与回填。
type SkillAdminService struct {
	skills *skill.Service
	logger *zap.Logger
}

// NewSkillAdminService 构造技能目录维护面。
func NewSkillAdminService(service *skill.Service, logger *zap.Logger) *SkillAdminService {
	return &SkillAdminService{skills: service, logger: logger}
}

// ImportSkill 实现 SkillAdminService。
func (s *SkillAdminService) ImportSkill(ctx context.Context, req *connect.Request[skillv1.ImportSkillRequest]) (*connect.Response[skillv1.ImportSkillResponse], error) {
	if _, err := callerSubject(ctx); err != nil {
		return nil, err
	}
	item, err := s.skills.Import(ctx, skill.ImportParams{
		RepositoryURL: req.Msg.GetRepositoryUrl(),
		Ref:           req.Msg.GetRef(),
		SubPath:       req.Msg.GetSubPath(),
		Title:         req.Msg.GetTitle(),
		Summary:       req.Msg.GetSummary(),
		Tags:          req.Msg.GetTags(),
		ImagePaths:    req.Msg.GetImagePaths(),
	})
	if err != nil {
		return nil, toSkillConnectError(err)
	}
	return connect.NewResponse(&skillv1.ImportSkillResponse{
		Skill: toProtoSkill(s.skills.ViewOf(ctx, item, true), true),
	}), nil
}

// ResyncSkill 实现 SkillAdminService。
func (s *SkillAdminService) ResyncSkill(ctx context.Context, req *connect.Request[skillv1.ResyncSkillRequest]) (*connect.Response[skillv1.ResyncSkillResponse], error) {
	if _, err := callerSubject(ctx); err != nil {
		return nil, err
	}
	item, changed, err := s.skills.Resync(ctx, req.Msg.GetSkillId())
	if err != nil {
		return nil, toSkillConnectError(err)
	}
	return connect.NewResponse(&skillv1.ResyncSkillResponse{
		Skill:   toProtoSkill(s.skills.ViewOf(ctx, item, true), true),
		Changed: changed,
	}), nil
}

// SetCurrentSkillVersion 实现 SkillAdminService。
func (s *SkillAdminService) SetCurrentSkillVersion(ctx context.Context, req *connect.Request[skillv1.SetCurrentSkillVersionRequest]) (*connect.Response[skillv1.SetCurrentSkillVersionResponse], error) {
	if _, err := callerSubject(ctx); err != nil {
		return nil, err
	}
	item, err := s.skills.SetCurrentVersion(ctx, req.Msg.GetSkillId(), req.Msg.GetVersionId())
	if err != nil {
		return nil, toSkillConnectError(err)
	}
	return connect.NewResponse(&skillv1.SetCurrentSkillVersionResponse{
		Skill: toProtoSkill(s.skills.ViewOf(ctx, item, true), true),
	}), nil
}

// UpdateSkillMetadata 实现 SkillAdminService。
func (s *SkillAdminService) UpdateSkillMetadata(ctx context.Context, req *connect.Request[skillv1.UpdateSkillMetadataRequest]) (*connect.Response[skillv1.UpdateSkillMetadataResponse], error) {
	if _, err := callerSubject(ctx); err != nil {
		return nil, err
	}
	item, err := s.skills.UpdateMetadata(ctx, req.Msg.GetSkillId(),
		req.Msg.GetTitle(), req.Msg.GetSummary(), req.Msg.GetTags())
	if err != nil {
		return nil, toSkillConnectError(err)
	}
	return connect.NewResponse(&skillv1.UpdateSkillMetadataResponse{
		Skill: toProtoSkill(s.skills.ViewOf(ctx, item, true), true),
	}), nil
}

// BeginSkillImageUpload 实现 SkillAdminService：签发一份展示图上传播的直传凭证。
//
// **字节不经过本服务端**（见 docs/design/objectstore/README.md）：服务端只校验
// **声明的**类型与大小，真正的边界由存储侧按策略执行。
func (s *SkillAdminService) BeginSkillImageUpload(ctx context.Context, req *connect.Request[skillv1.BeginSkillImageUploadRequest]) (*connect.Response[skillv1.BeginSkillImageUploadResponse], error) {
	if _, err := callerSubject(ctx); err != nil {
		return nil, err
	}
	imageID, credential, err := s.skills.BeginImageUpload(ctx, req.Msg.GetSkillId(),
		req.Msg.GetImageId(), req.Msg.GetContentType(),
		int64(req.Msg.GetSizeBytes())) //nolint:gosec // proto 是 uint64，领域层要 int64；上面限了上限
	if err != nil {
		return nil, toSkillConnectError(err)
	}
	// 只记声明的类型与大小，**不记凭证本身**：把凭证写进日志等于把一次写入的能力
	// 留在了日志文件里（与头像那一处同一条）。
	s.logger.Info("已签发技能展示图直传凭证",
		zap.String("skill_id", req.Msg.GetSkillId()),
		zap.String("image_id", imageID),
		zap.Bool("replace", req.Msg.GetImageId() != ""),
		zap.String("content_type", req.Msg.GetContentType()),
		zap.Uint64("declared_bytes", req.Msg.GetSizeBytes()))
	return connect.NewResponse(&skillv1.BeginSkillImageUploadResponse{
		ImageId: imageID,
		Upload:  toProtoUpload(credential),
	}), nil
}

// CommitSkillImageUpload 实现 SkillAdminService：核对对象确实到了，把这一张记进图集。
func (s *SkillAdminService) CommitSkillImageUpload(ctx context.Context, req *connect.Request[skillv1.CommitSkillImageUploadRequest]) (*connect.Response[skillv1.CommitSkillImageUploadResponse], error) {
	if _, err := callerSubject(ctx); err != nil {
		return nil, err
	}
	item, err := s.skills.CommitImageUpload(ctx, req.Msg.GetSkillId(), req.Msg.GetImageId())
	if err != nil {
		return nil, toSkillConnectError(err)
	}
	return connect.NewResponse(&skillv1.CommitSkillImageUploadResponse{
		Skill: toProtoSkill(s.skills.ViewOf(ctx, item, true), true),
	}), nil
}

// DeleteSkillImage 实现 SkillAdminService。标识不在图集里时失败（**不是幂等**）。
func (s *SkillAdminService) DeleteSkillImage(ctx context.Context, req *connect.Request[skillv1.DeleteSkillImageRequest]) (*connect.Response[skillv1.DeleteSkillImageResponse], error) {
	if _, err := callerSubject(ctx); err != nil {
		return nil, err
	}
	item, err := s.skills.DeleteImage(ctx, req.Msg.GetSkillId(), req.Msg.GetImageId())
	if err != nil {
		return nil, toSkillConnectError(err)
	}
	return connect.NewResponse(&skillv1.DeleteSkillImageResponse{
		Skill: toProtoSkill(s.skills.ViewOf(ctx, item, true), true),
	}), nil
}

// ReorderSkillImages 实现 SkillAdminService：请求给出的是期望的完整顺序。
func (s *SkillAdminService) ReorderSkillImages(ctx context.Context, req *connect.Request[skillv1.ReorderSkillImagesRequest]) (*connect.Response[skillv1.ReorderSkillImagesResponse], error) {
	if _, err := callerSubject(ctx); err != nil {
		return nil, err
	}
	item, err := s.skills.ReorderImages(ctx, req.Msg.GetSkillId(), req.Msg.GetImageIds())
	if err != nil {
		return nil, toSkillConnectError(err)
	}
	return connect.NewResponse(&skillv1.ReorderSkillImagesResponse{
		Skill: toProtoSkill(s.skills.ViewOf(ctx, item, true), true),
	}), nil
}

// DeleteSkill 实现 SkillAdminService。
func (s *SkillAdminService) DeleteSkill(ctx context.Context, req *connect.Request[skillv1.DeleteSkillRequest]) (*connect.Response[skillv1.DeleteSkillResponse], error) {
	if _, err := callerSubject(ctx); err != nil {
		return nil, err
	}
	if err := s.skills.Delete(ctx, req.Msg.GetSkillId()); err != nil {
		return nil, toSkillConnectError(err)
	}
	return connect.NewResponse(&skillv1.DeleteSkillResponse{}), nil
}
