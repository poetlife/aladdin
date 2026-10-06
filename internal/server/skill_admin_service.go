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
	})
	if err != nil {
		return nil, toSkillConnectError(err)
	}
	return connect.NewResponse(&skillv1.ImportSkillResponse{
		Skill: toProtoSkill(skill.View{Skill: item}, true),
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
		Skill:   toProtoSkill(skill.View{Skill: item}, true),
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
		Skill: toProtoSkill(skill.View{Skill: item}, true),
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
		Skill: toProtoSkill(skill.View{Skill: item}, true),
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
