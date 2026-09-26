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

	"github.com/poetlife/aladdin/internal/config"
	"github.com/poetlife/aladdin/internal/database"
	"github.com/poetlife/aladdin/internal/identity"
	identitygormstore "github.com/poetlife/aladdin/internal/identity/gormstore"
	"github.com/poetlife/aladdin/internal/observability"
	"github.com/poetlife/aladdin/internal/rbac/gormstore"
	"github.com/poetlife/aladdin/internal/server"
)

// version 由构建期经 -ldflags 注入，作为上报时的 service.version。
var version = "dev"

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
	telemetryOpts := cfg.TelemetryOptions(observability.ServiceServer, version)
	telemetryOpts.Logger = logger
	provider, err := observability.NewProvider(context.Background(), telemetryOpts)
	if err != nil {
		return err
	}
	metrics, err := observability.NewMetrics()
	if err != nil {
		return err
	}

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

	srv := server.New(cfg, logger, metrics, store, server.IdentityStores{
		Identities: identities,
		Sessions:   sessions,
		// 未配置客户端标识时这里是一个真正的 nil：那条登录路径整体缺席，
		// 而不是退化成一个"什么都通过"的校验器。
		Verifier: identity.NewGoogleVerifier(cfg.GoogleClientID),
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
