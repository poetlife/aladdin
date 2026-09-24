// Command aladdin-server 是 aladdin 的 gRPC 服务端入口。
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/poetlife/aladdin/internal/config"
	"github.com/poetlife/aladdin/internal/observability"
	"github.com/poetlife/aladdin/internal/server"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "aladdin-server: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	logger, err := observability.NewLogger(cfg.LoggerOptions("aladdin-server"))
	if err != nil {
		return err
	}
	defer func() { _ = logger.Sync() }()

	srv := server.New(cfg, logger)
	if err := server.ApplyDevSeed(srv, logger); err != nil {
		return err
	}

	// 监听的生命周期跟随信号上下文：收到 SIGINT/SIGTERM 时
	// Listen 会被取消，而不是留下一个孤立的监听套接字。
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	return srv.ListenAndServe(ctx)
}
