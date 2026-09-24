package server

import (
	"context"
	"fmt"
	"os"

	"go.uber.org/zap"

	"github.com/poetlife/aladdin/internal/rbac"
)

// 开发用种子数据的环境变量。
//
// 它们只在 ALADDIN_DEV_SEED=1 时被读取，且必须四项齐全——
// 半套配置比没有配置更危险，会让开发者以为权限已经生效。
const (
	EnvDevSeed = "ALADDIN_DEV_SEED"
	// 以下四个是环境变量**名**，不是凭证本身；gosec G101 在此为误报。
	EnvDevToken   = "ALADDIN_DEV_TOKEN" //nolint:gosec // 常量值是变量名，不是密钥
	EnvDevSubject = "ALADDIN_DEV_SUBJECT"
	EnvDevRole    = "ALADDIN_DEV_ROLE"
	EnvDevScope   = "ALADDIN_DEV_SCOPE"
)

// ApplyDevSeed 在显式开启时注入一个开发用主体与令牌。
//
// 这是**仅供本地开发**的旁路：它把 token 到主体的映射直接写进内存，
// 绕过了真实的凭证签发流程。生产部署必须确保 ALADDIN_DEV_SEED 未被设置。
//
// 之所以提供它，是因为骨架阶段尚无认证后端，没有它整个链路无法端到端跑通——
// 而那正是骨架存在的意义。接入真实认证后应删除本文件。
func ApplyDevSeed(s *Server, logger *zap.Logger) error {
	if os.Getenv(EnvDevSeed) != "1" {
		return nil
	}

	token := os.Getenv(EnvDevToken)
	subjectID := os.Getenv(EnvDevSubject)
	roleID := os.Getenv(EnvDevRole)
	scope := rbac.Scope(os.Getenv(EnvDevScope))

	for name, value := range map[string]string{
		EnvDevToken:   token,
		EnvDevSubject: subjectID,
		EnvDevRole:    roleID,
		EnvDevScope:   string(scope),
	} {
		if value == "" {
			return fmt.Errorf("%s 已开启，但 %s 未设置", EnvDevSeed, name)
		}
	}

	ctx := context.Background()
	subject := rbac.Subject{
		ID:           subjectID,
		Type:         rbac.SubjectTypeUser,
		DefaultScope: scope,
	}
	s.store.RegisterSubject(subject)
	if err := s.store.Bind(ctx, rbac.RoleBinding{
		SubjectID: subjectID,
		RoleID:    roleID,
		Scope:     scope,
	}); err != nil {
		return fmt.Errorf("注入开发绑定失败: %w", err)
	}
	if auth := s.Authenticator(); auth != nil {
		auth.Add(token, subject)
	}

	// 大声留痕：生产环境里出现这条日志就说明配置错了。
	logger.Warn("已注入开发用凭证，切勿用于生产环境",
		zap.String("subject_id", subjectID),
		zap.String("role_id", roleID),
		zap.String("scope", scope.String()),
	)
	return nil
}
