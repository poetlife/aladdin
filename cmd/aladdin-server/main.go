// Command aladdin-server 是 aladdin 的 gRPC 服务端入口。
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go.uber.org/zap"

	"github.com/poetlife/aladdin/internal/buildinfo"
	"github.com/poetlife/aladdin/internal/config"
	"github.com/poetlife/aladdin/internal/database"
	"github.com/poetlife/aladdin/internal/galaxy"
	galaxygormstore "github.com/poetlife/aladdin/internal/galaxy/gormstore"
	"github.com/poetlife/aladdin/internal/identity"
	identitygormstore "github.com/poetlife/aladdin/internal/identity/gormstore"
	"github.com/poetlife/aladdin/internal/objectstore"
	"github.com/poetlife/aladdin/internal/objectstore/cosupload"
	"github.com/poetlife/aladdin/internal/observability"
	profilegormstore "github.com/poetlife/aladdin/internal/profile/gormstore"
	"github.com/poetlife/aladdin/internal/rbac/gormstore"
	registrationgormstore "github.com/poetlife/aladdin/internal/registration/gormstore"
	"github.com/poetlife/aladdin/internal/server"
	"github.com/poetlife/aladdin/internal/skill"
	skillgormstore "github.com/poetlife/aladdin/internal/skill/gormstore"
	telemetrygormstore "github.com/poetlife/aladdin/internal/telemetry/gormstore"
)

// telemetryFlushTimeout 是退出时冲刷遥测数据的时间上限。
//
// 它必须独立于优雅退出的预算：后者已经用掉了大部分时间，若共用同一个
// deadline，最后一批 span 恰好会在"服务已停、数据未发"的窗口里丢掉。
const telemetryFlushTimeout = 5 * time.Second

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "aladdin-server: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	configPath := flag.String("config", "",
		"配置文件路径（默认读取 ALADDIN_CONFIG，否则读启动目录下的 "+config.FileName+"）")
	flag.Parse()

	// LoadServer 内部完成五层合并与校验，且校验发生在监听之前：
	// 配置错误直接终止进程，不会带着半套配置去监听一个错误的地址。
	cfg, err := config.LoadServer(config.ServerFlags{ConfigPath: *configPath})
	if err != nil {
		return err
	}
	logger, err := observability.NewLogger(cfg.LoggerOptions(observability.ServiceServer))
	if err != nil {
		return err
	}
	defer func() { _ = logger.Sync() }()

	// 信号上下文在这里就建起来，而不是等到监听之前：库结构迁移也在它的
	// 覆盖范围之内。一次迁移可能跑上几秒，期间收到停止信号却继续跑下去，
	// 就是"停不下来的进程"——那正是编排系统最终发 SIGKILL 的场景。
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// 遥测必须早于服务端构建：otel.Meter 取的是调用当时注册的实现，
	// 晚一步取到的 meter 什么都不做，而且不会报错——只是指标永远为空。
	//
	// 版本号取自 internal/buildinfo——它是构建期注入的唯一落点，页面上的
	// "后端版本"与这里上报的 service.version 是同一个变量。
	telemetryOpts := cfg.TelemetryOptions(observability.ServiceServer, buildinfo.Version)
	telemetryOpts.Logger = logger
	provider, err := observability.NewProvider(context.Background(), telemetryOpts)
	if err != nil {
		return err
	}
	metrics, err := observability.NewMetrics()
	if err != nil {
		return err
	}

	// 已启用的登录渠道。未配置客户端标识时各校验器的构造函数返回一个
	// **真正的 nil**，该渠道被注册表丢弃：这条登录路径整体缺席，而不是
	// 退化成一个"什么都通过"的校验器。
	//
	// 它在这里构造而不是直接内联进 server.New：下面那行启动日志要用它回答
	// "我改的那几个登录配置到底有没有被读到"——重定向型渠道尤其需要，它要
	// 三处取值一致，任何一处没读到都表现为"点登录没反应"。
	channels := identity.NewRegistry(
		identity.Channel{
			Source:   identity.SourceGoogle,
			ClientID: cfg.GoogleClientID,
			// 回调地址必须与授权时给出的一致，两处都从对外源与同一个
			// 路径常量派生（见 identity.GoogleCallbackPath）。
			Verifier: identity.NewGoogleVerifier(
				cfg.GoogleClientID, cfg.GoogleClientSecret, cfg.PublicURL(identity.GoogleCallbackPath)),
		},
		identity.Channel{
			Source:   identity.SourceGithub,
			ClientID: cfg.GithubClientID,
			// 回调地址必须与授权时给出的一致，两处都从对外源与同一个
			// 路径常量派生（见 identity.GithubCallbackPath）。
			Verifier: identity.NewGithubVerifier(
				cfg.GithubClientID, cfg.GithubClientSecret, cfg.PublicURL(identity.GithubCallbackPath)),
		},
	)

	// 生效配置留痕：这是回答"我改的配置文件到底有没有被读到"的唯一途径，
	// 而它无法从"服务能启动"这个事实中推断出来。服务端配置中不含凭证，
	// 因此可以整体入日志。
	//
	// 数据库一项是例外：连接串可能含口令，因此只记**脱敏摘要**。
	// driver 仍记原值——摘要在取值非法时会省略内容，那时原值就是唯一的线索。
	logger.Info("服务端配置已生效",
		zap.String("address", cfg.Address),
		zap.String("log_level", string(cfg.LogLevel)),
		zap.String("log_file", cfg.LogFile),
		zap.String("otel_endpoint", cfg.OTelEndpoint),
		zap.String("database_driver", cfg.Database.Driver),
		zap.String("database", database.Describe(cfg.Database)),
		// 头像存储同理：只记桶地址，**不记密钥**（见 config.COSConfig.Describe）。
		zap.String("cos", cfg.COS.Describe()),
		// 发布这一项没有密钥，因此原样给出发布域：它的失败方式是"发布入口没
		// 渲染"，而那可能只是配置没读到。它用的桶地址已经记在上面那一项里了。
		zap.String("galaxy", cfg.Galaxy.Describe()),
		// 已启用的登录方式与对外地址。两项都不是秘密；客户端密钥没有配置键，
		// 自然也进不来这里。
		zap.Strings("login_channels", channels.Sources()),
		zap.String("public_base_url", cfg.PublicBaseURL),
	)

	// 打开库并迁移，全程发生在监听之前：迁移失败即拒绝启动，不会出现
	// "服务在跑、表还没建好"的中间状态。内置角色的补齐也包含在里面
	// （见 gormstore.Open）——这三步的先后顺序固化在那里，不在这里。
	store, err := gormstore.Open(ctx, cfg.Database, logger)
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()

	// 认证模块的存储复用同一条连接：会话表与身份别名表由同一份迁移建好，
	// 因此它们的实现必须长在这条连接上，而不是各开一次库——两份可写副本
	// 意味着一次撤销可能只落到其中一份上（见 docs/design/persistence/schema.md）。
	sessions := identity.NewSessions(identitygormstore.New(store.DB()))

	// 身份存储单独持有：引导要用它按邮箱解析主体，而那是**存储层**的哑查询。
	// 刻意不经过 Identities——那一层负责"这次登录该归到谁"的判断，引导只需要
	// "这个邮箱登记在哪些身份上"，把注册语义引进来只会多一条没人想要的路径。
	identityStore := identitygormstore.NewIdentityStore(store.DB())
	identities := identity.NewIdentities(identityStore, store)

	// 私有区对象存储是**一条公共链路**：头像与 galaxy 资产共用它，差别只在键、
	// 白名单与上限（见 docs/design/objectstore/README.md）。未配置时返回一个
	// **真正的 nil**：两项功能整体缺席，而不是退化成一个"什么都存不下"的实现。
	objects, err := newObjectStore(cfg.COS)
	if err != nil {
		return err
	}

	// 公开区与发布域：公开区与资产私有区共用上面那个桶（靠逐对象的公开读区分），
	// 因此这里只多一个发布域。配置校验已经强制"给了发布域就必须有桶"，所以发布
	// 启用时 objects 必然非 nil。未配置时 Public 为 nil、Origin 为零值，领域层
	// 据此让发布这条路整体缺席。
	var publicWriter galaxy.PublicStore
	origin := galaxy.PublicOrigin{}
	if cfg.Galaxy.Enabled() {
		writer, err := cosupload.NewPublicWriter(cfg.COS.BucketURL, cfg.COS.SecretID, cfg.COS.SecretKey)
		if err != nil {
			return fmt.Errorf("构造公开区存储失败: %w", err)
		}
		publicWriter = writer
		origin, err = galaxy.NewPublicOrigin(cfg.COS.BucketURL, cfg.Galaxy.PublishBaseURL, cfg.PublicBaseURL)
		if err != nil {
			return fmt.Errorf("构造发布地址失败: %w", err)
		}
	}

	srv := server.New(cfg, logger, metrics, store, server.IdentityStores{
		Identities: identities,
		Sessions:   sessions,
		Channels:   channels,
		// 注册策略与邀请码与其它表同库同连接：注册闸门在每一条渠道登录路径上，
		// 它不是可选件。
		Registrations: registrationgormstore.New(store.DB()),
	}, server.ProfileStores{
		Profiles: profilegormstore.New(store.DB()),
		Avatars:  objects,
	}, server.GalaxyStores{
		Projects: galaxygormstore.New(store.DB()),
		Assets:   objects,
		Public:   publicWriter,
		Origin:   origin,
	}, server.TelemetryStores{
		// 事件表与其它表同库同连接：写侧记进去的与读侧查出来的必须是同一份数据。
		Events: telemetrygormstore.New(store.DB()),
	}, server.SkillStores{
		Catalog: skillgormstore.New(store.DB()),
		// 技能的内容对象与 galaxy 的资产在**同一个桶**里，靠 `skills/` 那一段
		// 前缀并排：服务端自己读写它们，不经过直传。
		Objects: objects,
		// 远端拉取：生产实现是 GitHub。凭据可空——空是常态，匿名也能纳管公开
		// 仓库，只是限频额度低（见 docs/design/skill/onboarding.md 的"远端凭据"）。
		Remote: skill.NewGithubRemote(cfg.Skill.GithubToken),
	})

	// 引导先于种子：它只在存储里一条绑定都没有时生效，而种子会写入绑定。
	if err := server.ApplyBootstrap(ctx, store, identityStore, cfg.Bootstrap, logger); err != nil {
		return err
	}
	if err := server.ApplyDevSeed(srv, logger); err != nil {
		return err
	}

	serveErr := srv.ListenAndServe(ctx)

	// 冲刷遥测数据。必须用一个尚未取消的 context：上面那个 ctx 在收到停止
	// 信号时已经失效，拿它调用一次都冲刷不了。
	flushCtx, cancelFlush := context.WithTimeout(context.Background(), telemetryFlushTimeout)
	defer cancelFlush()
	if err := provider.Shutdown(flushCtx); err != nil {
		logger.Warn("关闭遥测失败", zap.Error(err))
	}

	return serveErr
}

// newObjectStore 按配置构造私有区的直传存储；未配置时返回真正的 nil。
//
// 返回 nil 而不是一个"空实现"，是为了让"这个部署有没有对象存储"在
// server.ProfileStores / server.GalaxyStores 里是一个**可判定的布尔事实**，
// 而不是一个总是成功、但什么也存不下的实现——后者会让前端渲染出一个传不上去
// 的上传区。
//
// 半套配置不会走到这里：config.LoadServer 已经拒绝启动了。
func newObjectStore(cos config.COSConfig) (objectstore.Store, error) {
	if !cos.Enabled() {
		return nil, nil
	}
	store, err := cosupload.New(cosupload.Config{
		BucketURL: cos.BucketURL,
		SecretID:  cos.SecretID,
		SecretKey: cos.SecretKey,
	})
	if err != nil {
		return nil, fmt.Errorf("构造对象存储失败: %w", err)
	}
	return store, nil
}
