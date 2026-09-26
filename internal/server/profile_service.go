package server

import (
	"context"
	"errors"
	"fmt"

	"connectrpc.com/connect"
	"go.uber.org/zap"

	profilev1 "github.com/poetlife/aladdin/api/gen/aladdin/profile/v1"
	"github.com/poetlife/aladdin/internal/profile"
	"github.com/poetlife/aladdin/internal/rbac"
	"github.com/poetlife/aladdin/internal/server/interceptor"
)

// ProfileService 是个人档案面（"这个主体叫什么、长什么样"）的 RPC 实现。
//
// **每个方法都只作用于调用者自己。** 接口面上刻意不存在任何带目标主体标识的
// 形状：目标由凭证决定，不由请求里的字段决定。由此得到的结论是准入门槛为
// "已认证"而非某个权限码——不存在以他人为目标的形状，也就没有可被滥用的
// 授权面（见 docs/design/profile/README.md）。
//
// 它不读角色、不参与判定：档案是展示信息。
type ProfileService struct {
	profiles *profile.Profiles
	logger   *zap.Logger
}

// NewProfileService 构造档案服务。
func NewProfileService(profiles *profile.Profiles, logger *zap.Logger) *ProfileService {
	return &ProfileService{profiles: profiles, logger: logger}
}

// GetProfile 实现 ProfileService。
func (s *ProfileService) GetProfile(ctx context.Context, _ *connect.Request[profilev1.GetProfileRequest]) (*connect.Response[profilev1.GetProfileResponse], error) {
	subject, err := callerSubject(ctx)
	if err != nil {
		return nil, err
	}
	view, err := s.profiles.Get(ctx, subject.ID)
	if err != nil {
		return nil, toProfileConnectError(err)
	}
	return connect.NewResponse(&profilev1.GetProfileResponse{Profile: s.toProtoProfile(view)}), nil
}

// UpdateProfile 实现 ProfileService。
//
// 请求表达的是**期望的完整状态**，不是增量修改：空串表示清空该项。
func (s *ProfileService) UpdateProfile(ctx context.Context, req *connect.Request[profilev1.UpdateProfileRequest]) (*connect.Response[profilev1.UpdateProfileResponse], error) {
	subject, err := callerSubject(ctx)
	if err != nil {
		return nil, err
	}
	view, err := s.profiles.Update(ctx, subject.ID, req.Msg.GetNickname(), req.Msg.GetBio())
	if err != nil {
		return nil, toProfileConnectError(err)
	}

	// 留痕只记"哪几项现在是有值的"，**不记内容**：简介是自由文本，全文进日志
	// 既无必要也可能含隐私（见 docs/ssot-registry.md 的副作用表）。
	s.logger.Info("已更新档案",
		zap.String("subject_id", subject.ID),
		zap.Bool("nickname_set", view.Nickname != ""),
		zap.Bool("bio_set", view.Bio != ""),
	)
	return connect.NewResponse(&profilev1.UpdateProfileResponse{Profile: s.toProtoProfile(view)}), nil
}

// UpdateAvatar 实现 ProfileService。
//
// 类型由字节本身判定，请求里没有"声明类型"这个字段——上传方不自证安全属性
// （见 profile.SniffAvatarType）。
func (s *ProfileService) UpdateAvatar(ctx context.Context, req *connect.Request[profilev1.UpdateAvatarRequest]) (*connect.Response[profilev1.UpdateAvatarResponse], error) {
	subject, err := callerSubject(ctx)
	if err != nil {
		return nil, err
	}
	image := req.Msg.GetImage()
	view, err := s.profiles.SetAvatar(ctx, subject.ID, image)
	if err != nil {
		return nil, toProfileConnectError(err)
	}

	// 只记类型与字节数，**不记字节本身**。
	s.logger.Info("已更新头像",
		zap.String("subject_id", subject.ID),
		zap.Int("bytes", len(image)),
	)
	return connect.NewResponse(&profilev1.UpdateAvatarResponse{Profile: s.toProtoProfile(view)}), nil
}

// DeleteAvatar 实现 ProfileService。没有头像时也成功（幂等）。
func (s *ProfileService) DeleteAvatar(ctx context.Context, _ *connect.Request[profilev1.DeleteAvatarRequest]) (*connect.Response[profilev1.DeleteAvatarResponse], error) {
	subject, err := callerSubject(ctx)
	if err != nil {
		return nil, err
	}
	view, err := s.profiles.ClearAvatar(ctx, subject.ID)
	if err != nil {
		return nil, toProfileConnectError(err)
	}
	s.logger.Info("已删除头像", zap.String("subject_id", subject.ID))
	return connect.NewResponse(&profilev1.DeleteAvatarResponse{Profile: s.toProtoProfile(view)}), nil
}

// callerSubject 取回当前请求的主体。
//
// 认证是**中间件层**完成的，走到 handler 时主体已在 context 上；取不到只可能
// 是注解漏配（被 authMiddleware 放行却没经过认证）。那时返回"未认证"而不是
// "无权限"：后者会把一次配置缺陷放大成一次登录风暴。
func callerSubject(ctx context.Context) (rbac.Subject, error) {
	subject, ok := interceptor.SubjectFromContext(ctx)
	if !ok {
		return rbac.Subject{}, connect.NewError(connect.CodeUnauthenticated, errors.New("未认证"))
	}
	return subject, nil
}

// toProtoProfile 把档案视图翻译成接口类型。
//
// display_name 与 avatar_url 都是服务端算好的**派生值**，客户端不得自行拼接：
// 回退规则与地址签发各只有一处实现（见 docs/ssot-registry.md）。
func (s *ProfileService) toProtoProfile(view profile.View) *profilev1.Profile {
	return &profilev1.Profile{
		Nickname:            view.Nickname,
		DisplayName:         view.DisplayName,
		Bio:                 view.Bio,
		AvatarUrl:           view.AvatarURL,
		AvatarUploadEnabled: s.profiles.AvatarUploadEnabled(),
		// 上限由服务端下发，客户端不自己写一份（见 proto 里这个字段的说明）。
		AvatarMaxBytes: profile.AvatarMaxBytes,
	}
}

// toProfileConnectError 把档案相关的错误映射为 Connect 错误。
//
// 分类依据是"调用方该做什么"，而不是错误来自哪一层：
//
//   - 存储故障 → Unavailable（退避重试），**不是**权限或认证问题；
//   - 长度超限、类型不符、超过上限 → InvalidArgument（改了再试，重试没用）；
//   - 本部署未启用头像 → FailedPrecondition（换一种做法：不传头像）；
//   - 主体不存在 → NotFound（会话已失效，重新登录）。
func toProfileConnectError(err error) error {
	switch {
	case errors.Is(err, profile.ErrStoreUnavailable), errors.Is(err, rbac.ErrStoreUnavailable):
		return connect.NewError(connect.CodeUnavailable, errors.New("服务暂时不可用，请稍后重试"))
	case errors.Is(err, profile.ErrNicknameTooLong):
		return connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("昵称不能超过 %d 个字", profile.NicknameMaxRunes))
	case errors.Is(err, profile.ErrBioTooLong):
		return connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("简介不能超过 %d 个字", profile.BioMaxRunes))
	case errors.Is(err, profile.ErrAvatarUnavailable):
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("本部署未启用头像"))
	case errors.Is(err, profile.ErrAvatarTooLarge):
		return connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("头像不能超过 %d MiB", profile.AvatarMaxBytes/(1024*1024)))
	case errors.Is(err, profile.ErrAvatarTypeNotAllowed):
		return connect.NewError(connect.CodeInvalidArgument, errors.New("头像必须是 PNG、JPEG 或 GIF"))
	case errors.Is(err, rbac.ErrSubjectNotFound):
		return connect.NewError(connect.CodeNotFound, errors.New("主体不存在或已停用"))
	default:
		return connect.NewError(connect.CodeInternal, errors.New("档案服务内部错误"))
	}
}
