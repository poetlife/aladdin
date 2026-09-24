package config

import (
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/poetlife/aladdin/internal/observability"
)

// layer 是某一层"显式给出的键"。nil 字段表示该键没有出现，不参与覆盖。
//
// 三个来源（配置文件、环境变量、命令行参数）都先归一成 layer 再按顺序叠加，
// 于是"只覆盖显式键"这条语义只实现一次，不会在某一层被写成"整份替换"。
type layer struct {
	address  *string
	logLevel *string
	logFile  *string
	timeout  *time.Duration

	otelEndpoint    *string
	otelInsecure    *bool
	otelSampleRatio *float64
}

// ServerFlags 是服务端命令行显式给出的覆盖值。零值表示未给出。
//
// 服务端没有取值类参数：它只需要知道去哪找配置文件，而这条已在
// locateFile 中作为最高优先级的来源消费掉了。
type ServerFlags struct {
	ConfigPath string
}

// CLIFlags 是 CLI 命令行显式给出的覆盖值。零值表示未给出。
type CLIFlags struct {
	ConfigPath string
	Address    string
	Timeout    time.Duration
	// Debug 把日志级别提到 debug，等价于配置文件里的 log_level: debug。
	Debug bool
}

func (f CLIFlags) toLayer() layer {
	var l layer
	if f.Address != "" {
		l.address = &f.Address
	}
	if f.Timeout > 0 {
		l.timeout = &f.Timeout
	}
	if f.Debug {
		level := string(observability.LevelDebug)
		l.logLevel = &level
	}
	return l
}

// serverEnvOverrides 读取服务端认识的环境变量。
//
// 环境变量层只读本端认识的键，多余的 ALADDIN_* 变量一律忽略：配置文件是
// "为这个程序写的"，出现无法识别的键必是笔误，因此报错；环境变量是"与整个
// 系统共用的"，无法断言某个变量就是写给本进程的，因此忽略。这条不对称是
// 刻意的，见 docs/design/config/README.md。
//
// 值为空串等同于没有设置：环境变量无法表达"显式清空"，需要这一能力时
// 写进配置文件。
func serverEnvOverrides() (layer, error) {
	l, err := telemetryEnv()
	if err != nil {
		return layer{}, err
	}
	if v := os.Getenv(EnvAddress); v != "" {
		l.address = &v
	}
	if v := os.Getenv(EnvLogLevel); v != "" {
		l.logLevel = &v
	}
	if v := os.Getenv(EnvLogFile); v != "" {
		l.logFile = &v
	}
	return l, nil
}

func cliEnvOverrides() (layer, error) {
	l, err := telemetryEnv()
	if err != nil {
		return layer{}, err
	}
	if v := os.Getenv(EnvAddress); v != "" {
		l.address = &v
	}
	if v := os.Getenv(EnvLogLevel); v != "" {
		l.logLevel = &v
	}
	if v := os.Getenv(EnvTimeout); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return layer{}, invalidKey(keyTimeout, EnvTimeout, "不是合法的时长: "+err.Error())
		}
		l.timeout = &d
	}
	return l, nil
}

// telemetryEnv 读取两端共有的可观测性环境变量。
//
// 与配置文件的 applyTelemetryScalars 对应：同名同义，因此只解析一处。
func telemetryEnv() (layer, error) {
	var l layer
	if v := os.Getenv(EnvOTelEndpoint); v != "" {
		l.otelEndpoint = &v
	}
	if v := os.Getenv(EnvOTelInsecure); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return layer{}, invalidKey(keyOTelInsecure, EnvOTelInsecure, "不是布尔值: "+err.Error())
		}
		l.otelInsecure = &b
	}
	if v := os.Getenv(EnvOTelSampleRatio); v != "" {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return layer{}, invalidKey(keyOTelSampleRatio, EnvOTelSampleRatio, "不是数字: "+err.Error())
		}
		l.otelSampleRatio = &f
	}
	return l, nil
}

// mergeServer 把一层叠加到服务端配置上：只覆盖显式给出的键。
func mergeServer(cfg ServerConfig, l layer) ServerConfig {
	if l.address != nil {
		cfg.Address = *l.address
	}
	if l.logLevel != nil {
		cfg.LogLevel = observability.Level(strings.ToLower(*l.logLevel))
	}
	if l.logFile != nil {
		cfg.LogFile = *l.logFile
	}
	cfg.OTelEndpoint, cfg.OTelInsecure, cfg.OTelSampleRatio = mergeTelemetry(
		cfg.OTelEndpoint, cfg.OTelInsecure, cfg.OTelSampleRatio, l)
	return cfg
}

// mergeCLI 把一层叠加到 CLI 配置上：只覆盖显式给出的键。
func mergeCLI(cfg CLIConfig, l layer) CLIConfig {
	if l.address != nil {
		cfg.Address = *l.address
	}
	if l.logLevel != nil {
		cfg.LogLevel = observability.Level(strings.ToLower(*l.logLevel))
	}
	if l.timeout != nil {
		cfg.Timeout = *l.timeout
	}
	cfg.OTelEndpoint, cfg.OTelInsecure, cfg.OTelSampleRatio = mergeTelemetry(
		cfg.OTelEndpoint, cfg.OTelInsecure, cfg.OTelSampleRatio, l)
	return cfg
}

// mergeTelemetry 叠加两端共有的可观测性配置。
func mergeTelemetry(endpoint string, insecure bool, ratio float64, l layer) (string, bool, float64) {
	if l.otelEndpoint != nil {
		endpoint = *l.otelEndpoint
	}
	if l.otelInsecure != nil {
		insecure = *l.otelInsecure
	}
	if l.otelSampleRatio != nil {
		ratio = *l.otelSampleRatio
	}
	return endpoint, insecure, ratio
}

// LoadServer 合并出服务端启动配置。
//
// 顺序（后者覆盖前者）：
// 内置默认值 < 配置文件 < 本地覆盖 < 环境变量 < 命令行参数。
// 合并与校验只在此处实现，调用方不得再做一次"没配就用默认"的判断。
func LoadServer(f ServerFlags) (ServerConfig, error) {
	path, explicit := locateFile(f.ConfigPath, DefaultServerPath())

	cfg := DefaultServer()

	primary, err := readFileLayer(path, explicit, parseServerFile)
	if err != nil {
		return ServerConfig{}, err
	}
	cfg = mergeServer(cfg, primary)

	local, err := readFileLayer(localOverridePath(path), false, parseServerFile)
	if err != nil {
		return ServerConfig{}, err
	}
	cfg = mergeServer(cfg, local)

	env, err := serverEnvOverrides()
	if err != nil {
		return ServerConfig{}, err
	}
	cfg = mergeServer(cfg, env)

	return cfg, cfg.Validate()
}

// LoadCLI 合并出 CLI 运行配置。
//
// 顺序与服务端完全一致。凭证不在这里解析：它是另一条来源链，
// 有自己的入口（见 internal/auth）。
func LoadCLI(f CLIFlags) (CLIConfig, error) {
	defaultPath, err := DefaultCLIPath()
	if err != nil {
		return CLIConfig{}, err
	}
	path, explicit := locateFile(f.ConfigPath, defaultPath)

	cfg := DefaultCLI()

	primary, err := readFileLayer(path, explicit, parseCLIFile)
	if err != nil {
		return CLIConfig{}, err
	}
	cfg = mergeCLI(cfg, primary)

	local, err := readFileLayer(localOverridePath(path), false, parseCLIFile)
	if err != nil {
		return CLIConfig{}, err
	}
	cfg = mergeCLI(cfg, local)

	env, err := cliEnvOverrides()
	if err != nil {
		return CLIConfig{}, err
	}
	cfg = mergeCLI(cfg, env)

	cfg = mergeCLI(cfg, f.toLayer())

	return cfg, cfg.Validate()
}
