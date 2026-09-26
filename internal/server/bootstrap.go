package server

import (
	"context"
	"errors"
	"fmt"

	"go.uber.org/zap"

	"github.com/poetlife/aladdin/internal/config"
	"github.com/poetlife/aladdin/internal/rbac"
)

// ApplyBootstrap 按引导配置建立系统里的第一个管理员。
//
// 它解决的是一个真实的问题：存储里还没有任何角色绑定时，没有任何人有权
// 授予自己角色，于是"第一个管理员从哪来"没有答案。
//
// 它是**初始化**的输入，不是判定的输入：写入的是一条真实的角色绑定，
// 判定路径只读存储、从不读配置。因此生效之后删掉这两个键，**不会**撤销
// 已经建立的绑定；改这两个键也不改变任何一次判定结果（见 CLAUDE.md 第 7 条）。
//
// 四条边界在下面逐条落实：
//   - 物化：写的是真实绑定，不是一条"信任这个来源"的规则；
//   - 一次性：只在存储里一条绑定都没有时生效；
//   - 留痕：生效时以 warn 级记录被授予的主体与作用域；
//   - 按不可复用的标识：取值原样使用，不推导、不规范——尤其不按邮箱。
func ApplyBootstrap(ctx context.Context, store rbac.MutableStore, cfg config.BootstrapConfig, logger *zap.Logger) error {
	if cfg.Empty() {
		return nil
	}

	empty, err := hasNoBindings(ctx, store)
	if err != nil {
		return err
	}
	if !empty {
		// 存储里已经有绑定了，引导配置**不生效**：它是一次性的初始化动作，
		// 不是每次启动都重新施加一遍的规则。
		logger.Info("存储中已有角色绑定，引导配置不生效",
			zap.String("subject_id", cfg.Subject))
		return nil
	}

	if err := ensureBootstrapSubject(ctx, store, cfg.Subject, rbac.Scope(cfg.Scope)); err != nil {
		return err
	}
	if err := store.Bind(ctx, rbac.RoleBinding{
		SubjectID: cfg.Subject,
		RoleID:    rbac.RoleSystemAdmin,
		Scope:     rbac.Scope(cfg.Scope),
	}); err != nil {
		return fmt.Errorf("建立引导绑定失败: %w", err)
	}

	// 大声留痕：与开发种子旁路同级。这条在正常部署里只会出现一次，
	// 因此"它出现了"本身就是可检索、可告警的事件。
	logger.Warn("已按引导配置建立第一个管理员",
		zap.String("subject_id", cfg.Subject),
		zap.String("role_id", rbac.RoleSystemAdmin),
		zap.String("scope", cfg.Scope),
	)
	return nil
}

// ensureBootstrapSubject 保证引导主体存在，并把它默认作用域设为引导作用域。
//
// **默认作用域这一项是必要的，不是顺手为之。** 按文档的操作步骤，运维是
// 先登录一次、再把日志里读出的主体标识填进配置——那时主体已经存在，
// 而它的默认作用域是新主体的空值（全局）。会话签发的默认作用域取自这里，
// 于是这个管理员登录后声明的默认作用域是全局，而他的绑定在更窄的作用域上：
// 全局请求找不到覆盖它的绑定，界面上什么都看不到。刚建立的管理员登录后
// 一片空白，是最容易被当成"功能坏了"的一种表现。
//
// 类型从已有主体上保留：引导只决定"授予什么"，不重写主体的类型。
func ensureBootstrapSubject(ctx context.Context, store rbac.MutableStore, subjectID string, scope rbac.Scope) error {
	existing, err := store.Subject(ctx, subjectID)
	switch {
	case err == nil:
		existing.DefaultScope = scope
		if err := store.PutSubject(ctx, existing); err != nil {
			return fmt.Errorf("更新引导主体的默认作用域失败: %w", err)
		}
		return nil
	case errors.Is(err, rbac.ErrSubjectNotFound):
		if err := store.PutSubject(ctx, rbac.Subject{
			ID:           subjectID,
			Type:         rbac.SubjectTypeUser,
			DefaultScope: scope,
		}); err != nil {
			return fmt.Errorf("登记引导主体失败: %w", err)
		}
		return nil
	default:
		return fmt.Errorf("读取引导主体失败: %w", err)
	}
}

// hasNoBindings 报告存储里是不是一条角色绑定都没有。
//
// 逐角色查，而不是"读一张全局的绑定表"：存储的读接口是按主体与按角色两个
// 方向组织的，没有"列出全部绑定"这种形状。角色数是有界的（内置角色加少量
// 自定义角色），而这只发生在启动路径上，代价可以忽略。
func hasNoBindings(ctx context.Context, store rbac.MutableStore) (bool, error) {
	roles, err := store.Roles(ctx)
	if err != nil {
		return false, fmt.Errorf("读取角色清单失败: %w", err)
	}
	for _, role := range roles {
		bindings, err := store.BindingsOfRole(ctx, role.ID)
		if err != nil {
			return false, fmt.Errorf("读取角色 %s 的绑定失败: %w", role.ID, err)
		}
		if len(bindings) > 0 {
			return false, nil
		}
	}
	return true, nil
}
