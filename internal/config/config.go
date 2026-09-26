// Package config 提供配置加载的统一入口。
//
// 服务端与 CLI 都经由此包读取配置：来源分层、合并语义与校验只在这里实现一处。
// 调用方不得自行读取环境变量或配置文件，也不得再做一次"没配就用默认"的判断。
//
// 服务端与 CLI 各有一份配置文件，不共用：address 在两端语义相反——
// 服务端是监听地址，CLI 是目标地址。共用一份 schema 会让配置在两端之间
// 传递时静默指向错误的位置（见 docs/design/config/README.md）。
package config

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/poetlife/aladdin/internal/observability"
)

// ErrInvalid 表示配置本身不合法：取值非法、显式指定的文件不存在、
// 文件格式错误，或出现了无法识别的键。
//
// 调用方据此把它归入"用法错误"类别，与"未认证""权限不足"区分开：
// 配置错误的正确处理是修正配置后重跑，后两者分别触发重新登录与不重试。
var ErrInvalid = errors.New("配置不合法")

// 环境变量名。统一 ALADDIN_ 前缀。
//
// 变量名只在此处定义，调用方不得手写字符串字面量。
const (
	EnvAddress  = "ALADDIN_ADDRESS"
	EnvLogLevel = "ALADDIN_LOG_LEVEL"
	EnvLogFile  = "ALADDIN_LOG_FILE"
	EnvTimeout  = "ALADDIN_TIMEOUT"
	// EnvConfig 指定配置文件路径，优先级高于该端的默认位置。
	EnvConfig = "ALADDIN_CONFIG"

	EnvDatabaseDriver = "ALADDIN_DATABASE_DRIVER"
	EnvDatabaseDSN    = "ALADDIN_DATABASE_DSN"

	EnvGoogleClientID        = "ALADDIN_GOOGLE_CLIENT_ID"
	EnvBootstrapAdminSubject = "ALADDIN_BOOTSTRAP_ADMIN_SUBJECT"
	EnvBootstrapAdminScope   = "ALADDIN_BOOTSTRAP_ADMIN_SCOPE"

	EnvOTelEndpoint    = "ALADDIN_OTEL_ENDPOINT"
	EnvOTelInsecure    = "ALADDIN_OTEL_INSECURE"
	EnvOTelSampleRatio = "ALADDIN_OTEL_SAMPLE_RATIO"
)

// 配置文件中的键名。同时用于校验错误的提示，便于用户定位要改哪一项。
const (
	keyAddress  = "address"
	keyLogLevel = "log_level"
	keyLogFile  = "log_file"
	keyTimeout  = "timeout"

	keyDatabaseDriver = "database_driver"
	keyDatabaseDSN    = "database_dsn"

	keyGoogleClientID        = "google_client_id"
	keyBootstrapAdminSubject = "bootstrap_admin_subject"
	keyBootstrapAdminScope   = "bootstrap_admin_scope"

	keyOTelEndpoint    = "otel_endpoint"
	keyOTelInsecure    = "otel_insecure"
	keyOTelSampleRatio = "otel_sample_ratio"
)

const (
	// DefaultAddress 既是服务端的默认监听地址，也是 CLI 的默认目标地址。
	//
	// 两端取同一个值，本机开发时零配置即可互通。分成两个字面量就会在
	// 某一端调整默认端口后，让这个特性静默失效。
	DefaultAddress = "127.0.0.1:9090"
	// DefaultTimeout 是 CLI 单次调用的默认超时。
	DefaultTimeout = 30 * time.Second
	// DefaultLogLevel 是两端的默认日志级别。
	DefaultLogLevel = observability.LevelInfo
	// DefaultSampleRatio 是默认采样比例：全采。
	//
	// 骨架阶段不接受"恰好没采到"这种排障体验；需要降量时显式配置。
	DefaultSampleRatio = 1.0

	// DefaultDatabaseDriver 是默认的数据库后端。
	//
	// 取 sqlite 是因为它不需要一个独立进程、一个账号、一条网络路径，
	// 于是"第一次跑起来"没有前置条件。
	//
	// **后端取值的合法集合不在这里**：它由 internal/database 定义（方言知识
	// 只应有一处），本常量是否仍在那个集合内由一个守卫测试保证。
	DefaultDatabaseDriver = "sqlite"
	// DefaultDatabaseDSN 是默认的数据库连接串：启动时工作目录下的一个文件。
	//
	// 基准与配置文件的默认位置一致，两处的相对路径含义相同。
	DefaultDatabaseDSN = "aladdin.db"
)

// DatabaseConfig 描述服务端把数据存在哪。
//
// 它只承载取值，不解释取值：连接串两种形态的区分、方言语义与脱敏摘要
// 都属于持久化模块（见 docs/design/persistence/schema.md）。
// 配置模块负责的是"这两项按同一套分层规则被读到"。
type DatabaseConfig struct {
	// Driver 是数据库后端。
	Driver string
	// DSN 是连接串。**可能含口令，因此不得进日志**——
	// 它的地位与 CLI 的凭证文件相同（见 docs/design/config/README.md）。
	DSN string
}

// BootstrapConfig 描述如何建立系统里的第一个管理员。
//
// 它**只在存储中不存在任何角色绑定时生效**，且写入的是一条真实的角色绑定；
// 判定路径只读存储、从不读配置，因此它不是权限的来源（见
// docs/design/config/README.md 的"唯一例外：引导"）。
//
// 两项必须同时给出或同时留空：半套引导的失败方式是"看起来生效了"——
// 要么建出一条作用域不明的绑定，要么建出一条主体不明的绑定。
type BootstrapConfig struct {
	// Subject 是获得首个管理员角色的主体标识。
	//
	// 取值必须是**主体标识**（由 aladdin 分配、分配即冻结），不是邮箱，
	// 也不是渠道上的身份标识：邮箱在渠道侧可改名、可回收，回收给另一个
	// 人的那天就是一次无痕提权。
	Subject string
	// Scope 是这次授予的作用域。
	Scope string
}

// Empty 表示不引导。这是默认情形。
func (b BootstrapConfig) Empty() bool { return b.Subject == "" && b.Scope == "" }

// Partial 表示只给出了两项中的一项——必须拒绝启动。
func (b BootstrapConfig) Partial() bool {
	return (b.Subject == "") != (b.Scope == "")
}

// ServerConfig 是服务端启动配置。
type ServerConfig struct {
	// Address 是**监听**地址，不是目标地址。
	Address string
	// LogLevel 是日志级别。
	LogLevel observability.Level
	// LogFile 是日志文件路径；为空表示写标准错误。
	LogFile string
	// Database 是数据的存放位置。
	Database DatabaseConfig
	// GoogleClientID 是 Google 登录用的客户端标识；为空表示未启用该登录方式。
	//
	// 它**不是秘密**：这个值明文出现在浏览器里，是这类登录方式的设计前提，
	// 因此放在配置里不违反"配置中不得出现凭证"。真正需要保密的是客户端
	// 密钥，而浏览器登录流程不使用它（见 docs/design/identity/google-login.md）。
	GoogleClientID string
	// Bootstrap 描述如何建立第一个管理员。
	Bootstrap BootstrapConfig
	// OTelEndpoint 是 OTLP/HTTP 端点。为空表示不上报——
	// 但链路标识照常生成、传播、回写响应头（见 docs/observability.md）。
	OTelEndpoint string
	// OTelInsecure 为真时用明文 HTTP 连接端点。
	OTelInsecure bool
	// OTelSampleRatio 是采样比例，取值 (0, 1]。
	OTelSampleRatio float64
}

// CLIConfig 是命令行客户端的运行配置。
//
// 不含凭证：凭证单独存放、单独保护（见 internal/auth 与
// docs/design/config/credentials.md）。
type CLIConfig struct {
	// Address 是**目标**地址，不是监听地址。
	Address string
	// Timeout 是单次调用的超时。
	Timeout time.Duration
	// LogLevel 控制输出详细度；debug 级别等价于命令行 --debug。
	LogLevel observability.Level
	// 以下三项与服务端同名同义。
	OTelEndpoint    string
	OTelInsecure    bool
	OTelSampleRatio float64
}

// DefaultServer 返回服务端的内置默认配置。
//
// 默认监听回环地址而不是 0.0.0.0：默认配置下服务端不对外暴露，
// 要对外提供服务必须显式改动这一项。忘记改的后果是连不上，而不是被扫到。
func DefaultServer() ServerConfig {
	return ServerConfig{
		Address:  DefaultAddress,
		LogLevel: DefaultLogLevel,
		Database: DatabaseConfig{
			Driver: DefaultDatabaseDriver,
			DSN:    DefaultDatabaseDSN,
		},
		OTelSampleRatio: DefaultSampleRatio,
	}
}

// DefaultCLI 返回 CLI 的内置默认配置。
func DefaultCLI() CLIConfig {
	return CLIConfig{
		Address:         DefaultAddress,
		Timeout:         DefaultTimeout,
		LogLevel:        DefaultLogLevel,
		OTelSampleRatio: DefaultSampleRatio,
	}
}

// LoggerOptions 把服务端配置转换为日志构建参数。
func (c ServerConfig) LoggerOptions(service string) observability.Options {
	return observability.Options{Level: c.LogLevel, FilePath: c.LogFile, Service: service}
}

// TelemetryOptions 把配置转换为追踪与指标的构建参数。
//
// service 与 version 由调用方传入而不是取自配置：同一个二进制在不同环境
// 报告成不同服务名，会让"这是哪个服务"变成需要猜的问题。
func (c ServerConfig) TelemetryOptions(service, version string) observability.ProviderOptions {
	return observability.ProviderOptions{
		ServiceName:    service,
		ServiceVersion: version,
		Endpoint:       c.OTelEndpoint,
		Insecure:       c.OTelInsecure,
		SampleRatio:    c.OTelSampleRatio,
	}
}

// TelemetryOptions 把配置转换为追踪与指标的构建参数。
func (c CLIConfig) TelemetryOptions(service, version string) observability.ProviderOptions {
	return observability.ProviderOptions{
		ServiceName:    service,
		ServiceVersion: version,
		Endpoint:       c.OTelEndpoint,
		Insecure:       c.OTelInsecure,
		SampleRatio:    c.OTelSampleRatio,
	}
}

// Validate 校验服务端配置的自洽性。
func (c ServerConfig) Validate() error {
	if err := validateAddress(c.Address); err != nil {
		return err
	}
	if err := validateLogLevel(c.LogLevel); err != nil {
		return err
	}
	if err := validateDatabase(c.Database); err != nil {
		return err
	}
	if err := validateBootstrap(c.Bootstrap); err != nil {
		return err
	}
	return validateTelemetry(c.OTelEndpoint, c.OTelSampleRatio)
}

// Validate 校验 CLI 配置的自洽性。
func (c CLIConfig) Validate() error {
	if err := validateAddress(c.Address); err != nil {
		return err
	}
	if err := validateLogLevel(c.LogLevel); err != nil {
		return err
	}
	if c.Timeout <= 0 {
		return invalidKey(keyTimeout, EnvTimeout, "必须为正")
	}
	return validateTelemetry(c.OTelEndpoint, c.OTelSampleRatio)
}

// validateTelemetry 校验两端共有的可观测性配置。
//
// 只写一处：分头写迟早会出现"一端把 0.5 当合法、另一端当非法"。
func validateTelemetry(endpoint string, ratio float64) error {
	if endpoint != "" {
		if err := checkHostPort(endpoint); err != nil {
			return invalidKey(keyOTelEndpoint, EnvOTelEndpoint, "不是合法的 host:port："+err.Error())
		}
	}
	// 比例必须落在 (0, 1]：0 会静默丢弃全部链路，而这种"没数据"的现象
	// 极难归因到一行配置上，因此宁可拒绝启动。
	if ratio <= 0 || ratio > 1 {
		return invalidKey(keyOTelSampleRatio, EnvOTelSampleRatio,
			fmt.Sprintf("必须落在 (0, 1]，当前 %v", ratio))
	}
	return nil
}

// invalidKey 构造一条指明出处的配置错误。
//
// 同时给出配置项名与环境变量名：取值可能来自任意一层，
// 只说其中一个会让另一层的使用者找不到该改哪里。
func invalidKey(name, env, reason string) error {
	return fmt.Errorf("%w: 配置项 %s（%s）%s", ErrInvalid, name, env, reason)
}

func validateAddress(addr string) error {
	if addr == "" {
		return invalidKey(keyAddress, EnvAddress, "不能为空")
	}
	if err := checkHostPort(addr); err != nil {
		return invalidKey(keyAddress, EnvAddress, "不是合法的 host:port："+err.Error())
	}
	return nil
}

func validateLogLevel(l observability.Level) error {
	switch l {
	case observability.LevelDebug, observability.LevelInfo, observability.LevelWarn, observability.LevelError:
		return nil
	default:
		return invalidKey(keyLogLevel, EnvLogLevel, fmt.Sprintf("取值非法: %q", string(l)))
	}
}

// validateDatabase 校验数据库配置的**形状**：两项都不能为空。
//
// 后端取值是否属于已知集合、连接串能否用，属于方言语义，由持久化模块
// 在建立连接时判定（仍早于监听端口打开）。在这里再写一遍取值集合，
// 等于让"有哪些后端"有两个来源，而它们迟早会漂移。
func validateDatabase(db DatabaseConfig) error {
	if db.Driver == "" {
		return invalidKey(keyDatabaseDriver, EnvDatabaseDriver, "不能为空")
	}
	if db.DSN == "" {
		return invalidKey(keyDatabaseDSN, EnvDatabaseDSN, "不能为空")
	}
	return nil
}

// validateBootstrap 校验引导配置两项齐全。
//
// 与 validateDatabase 同理，这里只判"形状"：引导是否**真的**生效取决于
// 存储中是否存在绑定，那是服务端启动时才知道的事。
func validateBootstrap(b BootstrapConfig) error {
	if b.Partial() {
		return invalidKey(keyBootstrapAdminSubject, EnvBootstrapAdminSubject,
			"与 "+keyBootstrapAdminScope+"（"+EnvBootstrapAdminScope+"）必须同时给出或同时留空")
	}
	return nil
}

// checkHostPort 校验 host:port 的形状。
//
// 端口允许为 0：服务端用它表示"由系统分配端口"，端到端测试依赖这一点。
// 主机不允许为空——`:9090` 在 Go 里等价于监听所有网卡，与"默认不对外暴露"
// 的取向冲突；要对外提供服务就显式写 `0.0.0.0:9090`。
func checkHostPort(addr string) error {
	i := strings.LastIndex(addr, ":")
	if i < 0 {
		return errors.New("缺少端口")
	}
	host, port := addr[:i], addr[i+1:]
	if host == "" {
		return errors.New("主机为空；要监听所有网卡请显式写 0.0.0.0")
	}
	if port == "" {
		return errors.New("端口为空")
	}
	n, err := strconv.Atoi(port)
	if err != nil {
		return fmt.Errorf("端口不是数字: %q", port)
	}
	if n < 0 || n > 65535 {
		return fmt.Errorf("端口超出范围: %d", n)
	}
	return nil
}
