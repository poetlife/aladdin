package main

import (
	"context"
	"os"
	"sync"
	"time"

	"github.com/poetlife/aladdin/internal/config"
	"github.com/poetlife/aladdin/internal/observability"
)

// telemetryFlushTimeout 是退出时冲刷遥测数据的时间上限。
//
// CLI 是短命进程：不主动冲刷，最后一批 span 一定会随进程一起消失。
const telemetryFlushTimeout = 3 * time.Second

var (
	telemetryOnce     sync.Once
	telemetryProvider *observability.Provider
	telemetryErr      error
)

// ensureTelemetry 构建本次进程的遥测实现，只构建一次。
//
// 惰性构建而不是在 Execute 开头就建：纯本地命令（如查看版本）不该因为遥测
// 配置有问题而失败——一个写坏的 otel_endpoint 不该让人连版本号都查不到。
func ensureTelemetry(cfg config.CLIConfig) error {
	telemetryOnce.Do(func() {
		opts := cfg.TelemetryOptions(observability.ServiceCLI, version)
		telemetryProvider, telemetryErr = observability.NewProvider(context.Background(), opts)
	})
	return telemetryErr
}

// shutdownTelemetry 冲刷并关闭遥测；从未构建过时什么也不做。
//
// 必须用一个尚未取消的 context，并且要独立于请求超时：拿某次调用的
// 超时上下文来冲刷，会在调用刚超时的场景下一次都冲刷不了。
func shutdownTelemetry() {
	if telemetryProvider == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), telemetryFlushTimeout)
	defer cancel()
	if err := telemetryProvider.Shutdown(ctx); err != nil {
		printf(os.Stderr, "aladdin: 关闭遥测失败: %s\n", err)
	}
}
