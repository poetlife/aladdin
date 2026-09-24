// Package config 提供配置加载的统一入口。
//
// 服务端与 CLI 都经由此包读取配置，避免出现两套默认值与两套环境变量名。
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/poetlife/aladdin/internal/observability"
)

// Config 是服务端与 CLI 共用的配置。
//
// CLI 只使用 Address、LogLevel 与 Timeout；其余字段在 CLI 侧无意义但不影响加载。
type Config struct {
	// Address 是 gRPC 监听地址（服务端）或目标地址（CLI）。
	Address string
	// LogLevel 是日志级别。
	LogLevel observability.Level
	// LogFile 是服务端日志文件路径；CLI 不使用。
	LogFile string
	// Timeout 是单次请求的默认超时。
	Timeout time.Duration
	// DefaultScope 是未显式指定作用域时使用的作用域。
	//
	// 刻意不给它一个"全局作用域"的默认值：全局作用域只应由凭证自身绑定，
	// 不能由配置隐式授予（见 docs/design/rbac/cli-permissions.md）。
	DefaultScope string
}

// 环境变量名。三端统一使用 ALADDIN_ 前缀。
const (
	EnvAddress      = "ALADDIN_ADDRESS"
	EnvLogLevel     = "ALADDIN_LOG_LEVEL"
	EnvLogFile      = "ALADDIN_LOG_FILE"
	EnvTimeout      = "ALADDIN_TIMEOUT"
	EnvDefaultScope = "ALADDIN_DEFAULT_SCOPE"
)

// Default 返回内置默认配置。
func Default() Config {
	return Config{
		Address:      "127.0.0.1:9090",
		LogLevel:     observability.LevelInfo,
		LogFile:      "",
		Timeout:      30 * time.Second,
		DefaultScope: "",
	}
}

// Load 读取环境变量并与默认值合并，然后校验。
//
// 合并与校验只在此处实现；调用方不得再做一次"没配就用默认"的判断。
func Load() (Config, error) {
	cfg := Default()
	if v := os.Getenv(EnvAddress); v != "" {
		cfg.Address = v
	}
	if v := os.Getenv(EnvLogLevel); v != "" {
		cfg.LogLevel = observability.Level(strings.ToLower(v))
	}
	if v := os.Getenv(EnvLogFile); v != "" {
		cfg.LogFile = v
	}
	if v := os.Getenv(EnvTimeout); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return Config{}, fmt.Errorf("%s 不是合法的时长: %w", EnvTimeout, err)
		}
		cfg.Timeout = d
	}
	if v, ok := os.LookupEnv(EnvDefaultScope); ok {
		cfg.DefaultScope = v
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// Validate 校验配置的自洽性。
func (c Config) Validate() error {
	if c.Address == "" {
		return fmt.Errorf("%s 不能为空", EnvAddress)
	}
	if _, _, err := splitHostPort(c.Address); err != nil {
		return fmt.Errorf("%s 不是合法的 host:port: %w", EnvAddress, err)
	}
	switch c.LogLevel {
	case observability.LevelDebug, observability.LevelInfo, observability.LevelWarn, observability.LevelError:
	default:
		return fmt.Errorf("%s 取值非法: %q", EnvLogLevel, c.LogLevel)
	}
	if c.Timeout <= 0 {
		return fmt.Errorf("%s 必须为正", EnvTimeout)
	}
	return nil
}

// LoggerOptions 把配置转换为日志构建参数。
func (c Config) LoggerOptions(service string) observability.Options {
	return observability.Options{
		Level:    c.LogLevel,
		FilePath: c.LogFile,
		Service:  service,
	}
}

func splitHostPort(addr string) (string, string, error) {
	i := strings.LastIndex(addr, ":")
	if i < 0 {
		return "", "", fmt.Errorf("缺少端口")
	}
	host, port := addr[:i], addr[i+1:]
	if port == "" {
		return "", "", fmt.Errorf("端口为空")
	}
	if _, err := strconv.Atoi(port); err != nil {
		return "", "", fmt.Errorf("端口不是数字: %q", port)
	}
	return host, port, nil
}
