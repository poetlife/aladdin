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
	"github.com/poetlife/aladdin/internal/observability"
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
	logger.Info("服务端配置已生效",
		zap.String("address", cfg.Address),
		zap.String("log_level", string(cfg.LogLevel)),
		zap.String("log_file", cfg.LogFile),
		zap.String("otel_endpoint", cfg.OTelEndpoint),
	)

	srv := server.New(cfg, logger, metrics)
	if err := server.ApplyDevSeed(srv, logger); err != nil {
		return err
	}

	// 监听的生命周期跟随信号上下文：收到 SIGINT/SIGTERM 时
	// Listen 会被取消，而不是留下一个孤立的监听套接字。
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

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
