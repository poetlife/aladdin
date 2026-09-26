package server

import (
	"context"
	"errors"
	"fmt"

	"go.uber.org/zap"

	"github.com/poetlife/aladdin/internal/config"
	"github.com/poetlife/aladdin/internal/identity"
	"github.com/poetlife/aladdin/internal/rbac"
)

// ApplyBootstrap 按引导配置建立系统里的第一个管理员。
//
// 它解决的是一个真实的问题：存储里还没有任何角色绑定时，没有任何人有权
// 授予自己角色，于是"第一个管理员从哪来"没有答案。
//
// 它是**初始化**的输入，不是判定的输入：写入的是一条真实的角色绑定，
// 判定路径只读存储、从不读配置。因此生效之后删掉这些键，**不会**撤销
// 已经建立的绑定；改这些键也不改变任何一次判定结果（见 CLAUDE.md 第 7 条）。
//
// 四条边界在下面逐条落实：
//   - 物化：写的是真实绑定，不是一条"信任这个来源"的规则；
//   - 一次性：只在存储里一条绑定都没有时生效；
//   - 留痕：生效时以 warn 级记录被授予的主体与作用域；
//   - 落点是不可复用的主体标识：身份指认可以写成主体标识，也可以写成邮箱，
//     但邮箱只在这里被解析**一次**，绑定落在主体上（见 resolveBootstrapSubject）。
func ApplyBootstrap(
	ctx context.Context,
	store rbac.MutableStore,
	identities identity.IdentityStore,
	cfg config.BootstrapConfig,
	logger *zap.Logger,
) error {
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
			zap.String("subject_id", cfg.Subject),
			zap.String("email", cfg.Email))
		return nil
	}

	subject, err := resolveBootstrapSubject(ctx, identities, cfg)
	if err != nil {
		return err
	}
	scope := rbac.ParseScope(cfg.Scope)

	if err := ensureBootstrapSubject(ctx, store, subject, scope); err != nil {
		return err
	}
	if err := store.Bind(ctx, rbac.RoleBinding{
		SubjectID: subject,
		RoleID:    rbac.RoleSystemAdmin,
		Scope:     scope,
	}); err != nil {
		return fmt.Errorf("建立引导绑定失败: %w", err)
	}

	// 大声留痕：与开发种子旁路同级。这条在正常部署里只会出现一次，
	// 因此"它出现了"本身就是可检索、可告警的事件。
	//
	// 邮箱也记下来是刻意的：主体标识是一串不透明的值，而"当初是谁"只有
	// 这一行能回答。它只用于事后追溯，不参与任何判定。
	logger.Warn("已按引导配置建立第一个管理员",
		zap.String("subject_id", subject),
		zap.String("email", cfg.Email),
		zap.String("role_id", rbac.RoleSystemAdmin),
		zap.String("scope", scope.String()),
	)
	return nil
}

// resolveBootstrapSubject 把配置里的"身份指认"解析成主体标识。
//
// 两种写法：主体标识原样使用，不做任何推导或规范化；邮箱则按**已登记的
// 身份**查，且**必须恰好命中一个**。
//
// **不猜是刻意的。** 命中 0 个或多个时一律返回错误、由调用方拒绝启动——
// 猜一个等于把一次配置错误变成一次无声授权，而它不会在启动时说任何话。
//
// 0 个命中有个具体成因值得写进提示：邮箱与主体的对应关系是**登录时**才
// 产生的，所以还没登录过就用邮箱指认，注定查不到。不说这一点的话，
// 运维看到的是一句"没找到"，而他会认为自己明明填对了。
func resolveBootstrapSubject(
	ctx context.Context,
	identities identity.IdentityStore,
	cfg config.BootstrapConfig,
) (string, error) {
	if cfg.Email == "" {
		return cfg.Subject, nil
	}

	matches, err := identities.ListByDisplay(ctx, cfg.Email)
	if err != nil {
		return "", fmt.Errorf("按邮箱查身份失败: %w", err)
	}
	switch len(matches) {
	case 1:
		return matches[0].SubjectID, nil
	case 0:
		return "", fmt.Errorf(
			"引导配置里的邮箱 %q 没有命中任何已登记身份。"+
				"邮箱与主体的对应关系是登录时才产生的：请先用该账号登录一次，再重启服务端",
			cfg.Email)
	default:
		return "", fmt.Errorf(
			"引导配置里的邮箱 %q 命中了 %d 个已登记身份，无法确定是哪一个（同一个邮箱"+
				"可以分别挂在两个身份上）。请改用主体标识显式指定",
			cfg.Email, len(matches))
	}
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
