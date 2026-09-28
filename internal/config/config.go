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
	"net/url"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/publicsuffix"

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

	EnvCOSBucketURL = "ALADDIN_COS_BUCKET_URL"
	// EnvGalaxyPublicBucketURL 与 EnvGalaxyPublishBaseURL 是 galaxy 发布的公开桶
	// 地址与对外地址。**它们复用同一对 COS 密钥**：子账号同时被授权两个桶，而
	// "公开还是私有"由桶侧策略决定，不由凭证决定（见
	// docs/design/galaxy/publication.md）。
	EnvGalaxyPublicBucketURL = "ALADDIN_GALAXY_PUBLIC_BUCKET_URL"
	EnvGalaxyPublishBaseURL  = "ALADDIN_GALAXY_PUBLISH_BASE_URL"
	// EnvCOSSecretID 与 EnvCOSSecretKey 是头像存储的子账号密钥。
	//
	// **它们与 EnvGithubClientSecret 是本仓库仅有的两组"只有环境变量、没有
	// 配置键"的取值**，与开发种子旁路同类：一个开关一旦能写进配置文件，它就会
	// 在某个人手上的生产环境里被写进去。配置文件会进版本库、进镜像、被贴给
	// 别人排查问题，而凭证不可以（见 docs/design/config/credentials.md）。
	// 这里是**环境变量名**，不是凭证值——gosec 按名字里的单词误报了。
	EnvCOSSecretID  = "ALADDIN_COS_SECRET_ID"  //nolint:gosec // 取值是变量名本身
	EnvCOSSecretKey = "ALADDIN_COS_SECRET_KEY" //nolint:gosec // 取值是变量名本身

	EnvGoogleClientID = "ALADDIN_GOOGLE_CLIENT_ID"
	EnvGithubClientID = "ALADDIN_GITHUB_CLIENT_ID"
	// EnvGithubClientSecret 是 GitHub 登录的客户端密钥。
	//
	// 与 COS 密钥同理：**只有环境变量，没有配置键**。
	EnvGithubClientSecret = "ALADDIN_GITHUB_CLIENT_SECRET" //nolint:gosec // 取值是变量名本身
	// EnvPublicBaseURL 是服务端的对外地址，用于构造重定向型登录的回调与回跳地址。
	EnvPublicBaseURL = "ALADDIN_PUBLIC_BASE_URL"

	EnvBootstrapAdminSubject = "ALADDIN_BOOTSTRAP_ADMIN_SUBJECT"
	EnvBootstrapAdminEmail   = "ALADDIN_BOOTSTRAP_ADMIN_EMAIL"
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

	keyCOSBucketURL = "cos_bucket_url"

	keyGalaxyPublicBucketURL = "galaxy_public_bucket_url"
	keyGalaxyPublishBaseURL  = "galaxy_publish_base_url"

	keyGoogleClientID        = "google_client_id"
	keyGithubClientID        = "github_client_id"
	keyPublicBaseURL         = "public_base_url"
	keyBootstrapAdminSubject = "bootstrap_admin_subject"
	keyBootstrapAdminEmail   = "bootstrap_admin_email"
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

// COSConfig 描述头像存放的对象存储。
//
// 它**可以整体为空**：头像存储是可选能力，未配置时昵称与简介照常可用，前端
// 只是不渲染头像上传区。但它**不能只配一半**——半套配置的失败方式是"看起来
// 配好了"，而它要到第一次上传时才以一次权限错误暴露（见
// docs/design/profile/avatar-storage.md）。
type COSConfig struct {
	// BucketURL 是完整的桶主机名，形如
	// https://<桶名>-<APPID>.cos.<地域>.myqcloud.com。
	//
	// 它**不是秘密**：没有签名取不到桶里的任何东西，因此它留在配置文件里。
	BucketURL string
	// SecretID 与 SecretKey 是子账号密钥。
	//
	// **只从环境变量来，没有对应的配置键**（见 EnvCOSSecretID）。因此它们
	// 也**不得进日志**——描述本结构时用 Describe，不要把它整体丢进日志。
	SecretID  string
	SecretKey string
}

// Enabled 表示这个部署配置了头像存储。
//
// 它只在配置**校验通过**之后才有意义：半套配置不会走到这里（Validate 会拒绝
// 启动），因此这里不需要再回答"配了一半算不算"。
func (c COSConfig) Enabled() bool { return c.BucketURL != "" }

// GalaxyConfig 是 galaxy 发布所需的两个地址。
//
// 它们**成对出现**：一个指向公开读的桶（发布物引用的媒体从那里取），一个指向
// 发布页面的对外地址。二者缺一即发布不可用，而"只配一半"由 validateGalaxy
// 拒绝启动——半套配置的失败方式既不是"没启用"，也不是"配错了"，而是"看起来
// 配好了"，直到第一次发布才失败。
type GalaxyConfig struct {
	// PublicBucketURL 是发布物资产的**公开读**桶地址。
	//
	// 它与私有桶（COS.BucketURL）是两个独立的桶，不是同一个桶的两个前缀：桶的
	// 公开读是一个"整个桶"级别的策略，把公开与私有的东西放进同一个桶，意味着
	// 一次策略写错就是全桶公开。它**不是秘密**（只是一个主机名），因此留在
	// 配置文件里。
	PublicBucketURL string
	// PublishBaseURL 是发布页面的对外地址（发布域）。
	//
	// **它必须与 PublicBaseURL 不同源**，且不只是主机名不同：同一注册域下的
	// 两个主机虽然不同源，却可能共享一张按域设置的 cookie。这条由
	// validateGalaxy 强制（见 docs/design/galaxy/publication.md）。
	PublishBaseURL string
}

// Enabled 表示这个部署配置了发布存储。校验通过之后才调用它。
func (c GalaxyConfig) Enabled() bool { return c.PublicBucketURL != "" && c.PublishBaseURL != "" }

// Describe 描述这项配置。它没有密钥，因此原样给出两个地址。
//
// 启动日志要能回答"我改的那一行到底有没有被读到"——发布这一项尤其如此：
// 它的失败方式是"发布入口没渲染"，而那可能只是配置没读到。
func (c GalaxyConfig) Describe() string {
	if !c.Enabled() {
		return "未启用"
	}
	return "公开桶 " + c.PublicBucketURL + "，发布域 " + c.PublishBaseURL
}

// Describe 描述这项配置，**不含密钥**。
//
// 与 database.Describe 同理：启动日志要能回答"我改的那一行到底有没有被读到"，
// 而这一项里有一半是凭证。给一个专门的描述入口，好过指望每个调用方都记得
// 别把整个结构体丢进日志。
func (c COSConfig) Describe() string {
	if !c.Enabled() {
		return "未启用"
	}
	return c.BucketURL
}

// BootstrapConfig 描述如何建立系统里的第一个管理员。
//
// 它**只在存储中不存在任何角色绑定时生效**，且写入的是一条真实的角色绑定；
// 判定路径只读存储、从不读配置，因此它不是权限的来源（见
// docs/design/config/README.md 的"唯一例外：引导"）。
//
// 身份指认与作用域必须同时给出或同时留空：半套引导的失败方式是"看起来
// 生效了"——要么建出一条作用域不明的绑定，要么建出一条主体不明的绑定。
type BootstrapConfig struct {
	// Subject 是获得首个管理员角色的**主体标识**。
	//
	// 由 aladdin 分配、分配即冻结，原样使用，不推导、不规范。登录之后界面上
	// 就显示它，不必去日志里找。
	Subject string
	// Email 是用**邮箱**指认主体的便利写法，与 Subject 互斥。
	//
	// 它只在该邮箱**恰好命中一个已登记身份**时生效——0 个或多个一律拒绝启动，
	// 不猜（同一个邮箱字符串可以分别挂在两个身份上，这是刻意允许的）。
	//
	// 它**不意味着身份可以按邮箱确定**：解析只发生一次、绑定落在主体上，
	// 此后的任何判定都不看邮箱。见 docs/design/identity/google-login.md 与
	// CLAUDE.md 第 7 条。
	Email string
	// Scope 是这次授予的作用域，**文本形式**：写 "<global>" 表示全局作用域。
	//
	// 全局作用域的内部值是空串，而空串在这里表示"没填"——两者不能共用一种
	// 写法，否则一次手滑漏填就会静默变成全局管理员。
	Scope string
}

// Empty 表示不引导。这是默认情形。
func (b BootstrapConfig) Empty() bool {
	return b.Subject == "" && b.Email == "" && b.Scope == ""
}

// Ambiguous 表示两种身份指认同时给出——不知道以谁为准，必须拒绝启动。
func (b BootstrapConfig) Ambiguous() bool {
	return b.Subject != "" && b.Email != ""
}

// Partial 表示身份指认与作用域只给出了其中一项——必须拒绝启动。
func (b BootstrapConfig) Partial() bool {
	return (b.Subject == "" && b.Email == "") != (b.Scope == "")
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
	// COS 是头像存放的对象存储。零值表示未启用头像。
	COS COSConfig
	// Galaxy 是 galaxy 发布所需的存储与对外地址。零值表示未启用发布。
	Galaxy GalaxyConfig
	// GoogleClientID 是 Google 登录用的客户端标识；为空表示未启用该登录方式。
	//
	// 它**不是秘密**：这个值明文出现在浏览器里，是这类登录方式的设计前提，
	// 因此放在配置里不违反"配置中不得出现凭证"。真正需要保密的是客户端
	// 密钥，而浏览器登录流程不使用它（见 docs/design/identity/google-login.md）。
	GoogleClientID string
	// GithubClientID 是 GitHub 登录用的客户端标识；为空表示未启用该登录方式。
	//
	// 与 GoogleClientID 同理，它不是秘密（见
	// docs/design/identity/github-login.md）。
	GithubClientID string
	// GithubClientSecret 是 GitHub 登录用的客户端密钥。
	//
	// **只从环境变量来，没有对应的配置键**（见 EnvGithubClientSecret）。
	// 因此它也**不得进日志**——描述本模块的配置时不要把它整体丢进日志。
	GithubClientSecret string
	// PublicBaseURL 是服务端的**对外地址**，用于构造重定向型登录的回调地址
	// 与回跳前端的地址。
	//
	// 它**不是秘密**（只是一个主机名），因此留在配置文件里。它必须由配置
	// 给出，**不得由请求头推导**——用请求头构造跳转目标等于给攻击者一个把
	// 会话凭证送到任意主机的原语（见 docs/design/identity/github-login.md）。
	PublicBaseURL string
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

// PublicURL 由对外源与一个以 / 开头的路径拼出绝对地址。
//
// 它是"对外源怎么和一个路径组合"的**唯一实现**：回调地址与回跳前端的地址
// 都从它派生，两处各拼一次迟早会出现一处少一个斜杠。
//
// 末尾斜杠在取值校验时已排除，因此这里只需处理"路径已带 /"的情形。
func (c ServerConfig) PublicURL(path string) string {
	return strings.TrimSuffix(c.PublicBaseURL, "/") + path
}

// PublicScheme 返回对外源的协议，**小写**；取值不成立时返回空串。
//
// 它是"从对外源取协议"的**唯一实现**：取值校验与"cookie 是否要求加密传输"
// 都从它派生。两处各读一次原始字符串迟早会分岔——协议名大小写不敏感，而按
// 字面量比较会认为 `HTTPS://…` 不是 https。空串表示"没有配置"，因此调用方
// 判等即可，不需要额外区分"没配"与"不合法"。
func (c ServerConfig) PublicScheme() string {
	u, err := url.Parse(c.PublicBaseURL)
	if err != nil {
		return ""
	}
	return u.Scheme
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
	if err := validateCOS(c.COS); err != nil {
		return err
	}
	if err := validateGalaxy(c); err != nil {
		return err
	}
	if err := validatePublicBaseURL(c.PublicBaseURL); err != nil {
		return err
	}
	if err := validateGithubLogin(c); err != nil {
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

// validateCOS 校验头像存储配置。
//
// 只有两种情形放行：**全空**（不启用头像）与**全给**（启用头像）。部分给出
// 一律拒绝启动——半套配置在这里是最该拦下的一类，因为它的失败方式既不是"没
// 启用"（界面会渲染一个传不上去的上传区），也不是"配错了"（启动时就能看见），
// 而是"看起来配好了"，直到第一次上传才以一次 403 暴露。
//
// 桶地址必须是 https：用明文把密钥与图片送出去，与"桶私有"这个前提直接冲突。
func validateCOS(cos COSConfig) error {
	missingSecrets := make([]string, 0, 2)
	if cos.SecretID == "" {
		missingSecrets = append(missingSecrets, EnvCOSSecretID)
	}
	if cos.SecretKey == "" {
		missingSecrets = append(missingSecrets, EnvCOSSecretKey)
	}
	hasBucket := cos.BucketURL != ""
	hasAnySecret := cos.SecretID != "" || cos.SecretKey != ""

	switch {
	case !hasBucket && !hasAnySecret:
		return nil // 未启用头像。这是默认情形
	case !hasBucket:
		return fmt.Errorf("%w: 已设置 %s 与 %s，但配置项 %s（%s）为空：密钥有主、桶没主",
			ErrInvalid, EnvCOSSecretID, EnvCOSSecretKey, keyCOSBucketURL, EnvCOSBucketURL)
	case len(missingSecrets) > 0:
		return fmt.Errorf("%w: 已配置 %s（%s），但缺少 %s：桶地址与密钥必须同时给出",
			ErrInvalid, keyCOSBucketURL, EnvCOSBucketURL, strings.Join(missingSecrets, " 与 "))
	}

	bucket, err := url.Parse(cos.BucketURL)
	if err != nil || bucket.Scheme != "https" || bucket.Host == "" {
		return invalidKey(keyCOSBucketURL, EnvCOSBucketURL,
			fmt.Sprintf("必须是带主机名的 https 地址，当前 %q", cos.BucketURL))
	}
	return nil
}

// checkOriginShape 是"一个地址能不能当作对外源"的**唯一判据**。
//
// 服务端的对外地址与发布域两处用它，因此它只实现一次：两处各写一份的表现是
// "一处允许 http 回环、另一处不允许"，而配置的合法性不该取决于读的是哪一份代码。
//
// 要求：绝对地址、有主机名、不带用户信息、不带查询串与 fragment、路径为空或只有
// `/`。协议必须 https，**唯一例外是本地回环主机**——本地开发没有证书。
func checkOriginShape(key, env, raw, pathReason string) error {
	u, err := url.Parse(raw)
	if err != nil || !u.IsAbs() || u.Host == "" {
		return invalidKey(key, env, fmt.Sprintf("必须是带主机名的绝对地址，当前 %q", raw))
	}
	if u.User != nil {
		return invalidKey(key, env, "不得带用户信息")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return invalidKey(key, env, "不得带查询串或 fragment")
	}
	if u.Path != "" && u.Path != "/" {
		return invalidKey(key, env, fmt.Sprintf("路径必须为空或只有 /，当前 %q：%s", u.Path, pathReason))
	}
	loopbackHTTP := u.Scheme == "http" && isLoopbackHost(u.Hostname())
	if u.Scheme != "https" && !loopbackHTTP {
		return invalidKey(key, env, fmt.Sprintf("必须是 https（仅本地回环主机允许 http），当前协议 %q", u.Scheme))
	}
	return nil
}

// validateGalaxy 校验 galaxy 发布所需的两个地址。
//
// 只有两种情形放行：**两项全空**（不启用发布）与**两项齐全**（启用发布）。部分
// 给出一律拒绝启动——半套配置的失败方式既不是"没启用"（前端会渲染一个点了报错
// 的发布入口），也不是"配错了"（启动时能看见），而是"看起来配好了"，直到第一次
// 发布才失败。这与 validateCOS、validateGithubLogin 是同一条取向。
//
// **发布域必须与主应用不同源，且不只是主机名不同。** 发布物里跑着用户写的脚本，
// 同源意味着那段脚本与应用共享 origin。而"同一注册域"这一条更隐蔽：同一注册域下
// 的两个主机虽然不同源，却可能共享一张按域设置的 cookie——脚本读不到 HttpOnly
// 条目，但它可以把请求发到同站的应用地址上带着 cookie 走。因此判据是**可注册域
// 必须不同**。
func validateGalaxy(c ServerConfig) error {
	hasPublicBucket := c.Galaxy.PublicBucketURL != ""
	hasPublishBase := c.Galaxy.PublishBaseURL != ""
	switch {
	case !hasPublicBucket && !hasPublishBase:
		return nil // 未启用发布。这是默认情形
	case !hasPublicBucket:
		return invalidKey(keyGalaxyPublicBucketURL, EnvGalaxyPublicBucketURL,
			"与 "+keyGalaxyPublishBaseURL+"（"+EnvGalaxyPublishBaseURL+"）必须同时给出：只给发布域等于有页面地址、没有放页面的地方")
	case !hasPublishBase:
		return invalidKey(keyGalaxyPublishBaseURL, EnvGalaxyPublishBaseURL,
			"与 "+keyGalaxyPublicBucketURL+"（"+EnvGalaxyPublicBucketURL+"）必须同时给出：只给公开桶等于有存的地方、没有对外地址")
	}

	// 公开桶是"资源从哪来"，它必须是带主机名的 https 地址。
	bucket, err := url.Parse(c.Galaxy.PublicBucketURL)
	if err != nil || bucket.Scheme != "https" || bucket.Host == "" {
		return invalidKey(keyGalaxyPublicBucketURL, EnvGalaxyPublicBucketURL,
			fmt.Sprintf("必须是带主机名的 https 地址，当前 %q", c.Galaxy.PublicBucketURL))
	}
	if err := checkOriginShape(keyGalaxyPublishBaseURL, EnvGalaxyPublishBaseURL, c.Galaxy.PublishBaseURL,
		"带路径会让 /g/<工程标识> 变成一个子路径，而那不在本 spec 的地址形状里"); err != nil {
		return err
	}

	// 主应用没有配置对外地址时无从比较：此时"不同源"这条没有对象，而不是
	// "自动通过"——发布域自己的取值要求已经在上一步查过。
	if c.PublicBaseURL == "" {
		return nil
	}
	return validateDifferentSite(c.PublicBaseURL, c.Galaxy.PublishBaseURL)
}

// validateDifferentSite 判定两个地址是否属于**同一站点**（同源或同注册域）。
//
// 判据只有一个，因为"发布域与主应用不同源"这条约束的解释只有一种：它们是两个
// 互不信任的站点。同源比同注册域更强，因此先判前者。
//
// 注册域的判定用公共后缀表（Public Suffix List）而不是"取最后两段"：后者在
// `example.co.uk` 这类多段后缀上会错，而错的方向是把两个不同站点判成同一个
// （或反过来），两种都不该出现在一条安全约束里。
func validateDifferentSite(appBaseURL, publishBaseURL string) error {
	app, err := url.Parse(appBaseURL)
	if err != nil {
		return nil // 主应用地址自身的形状由它自己的校验负责
	}
	publish, err := url.Parse(publishBaseURL)
	if err != nil {
		return nil
	}
	if strings.EqualFold(app.Host, publish.Host) {
		return invalidKey(keyGalaxyPublishBaseURL, EnvGalaxyPublishBaseURL,
			"与 "+keyPublicBaseURL+"（"+EnvPublicBaseURL+"）同源：发布物里跑着用户写的脚本，同源意味着它能读写应用的 cookie 与本地存储")
	}
	appSite, appErr := registrableDomain(app.Hostname())
	publishSite, publishErr := registrableDomain(publish.Hostname())
	if appErr != nil || publishErr != nil {
		// 取不出注册域（回环地址、或用不了公共后缀表的主机）时，不同主机名已经
		// 足够：这一条是"更强的那一条"，不该因为判不出来而变成拒绝启动。
		return nil
	}
	if appSite == publishSite {
		return invalidKey(keyGalaxyPublishBaseURL, EnvGalaxyPublishBaseURL,
			fmt.Sprintf("与 %s（%s）同属注册域 %s：同一注册域下的两个主机可能共享一张按域设置的 cookie，脚本可以把请求发到同站的应用地址上",
				keyPublicBaseURL, EnvPublicBaseURL, appSite))
	}
	return nil
}

// registrableDomain 返回一个主机名的可注册域（eTLD+1）。
func registrableDomain(host string) (string, error) {
	return publicsuffix.EffectiveTLDPlusOne(host)
}

// validatePublicBaseURL 校验服务端对外地址的形状。
//
// 空值是合法的：它表示"没有配置"（是否需要它由 validateGithubLogin 判定）。
// ——只有重定向型登录渠道才用它，因此不能在这里要求它非空。
//
// 路径必须为空或只有 `/`：带路径前缀会衍生出一个同样要登记在渠道控制台里的
// 回调地址，那是一个只在部署时才暴露的陷阱。协议必须 https，**唯一例外是
// 本地回环主机**——本地开发没有证书，写死 https 会让登录在本机根本跑不通。
func validatePublicBaseURL(raw string) error {
	if raw == "" {
		return nil
	}
	return checkOriginShape(keyPublicBaseURL, EnvPublicBaseURL, raw,
		"带路径会衍生出一个也要登记在渠道控制台里的回调地址")
}

// isLoopbackHost 判断主机名是不是本地回环地址。
func isLoopbackHost(host string) bool {
	switch host {
	case "localhost", "127.0.0.1", "::1":
		return true
	default:
		return false
	}
}

// validateGithubLogin 校验重定向型登录渠道的配置。
//
// 只有两种情形放行：**三项全空**（不启用）与**三项齐全**（启用）。部分给出
// 一律拒绝启动——它的失败方式既不是"没启用"（界面会渲染一个点不通的入口），
// 也不是"配错了"（启动时就能看见），而是"看起来配好了"，直到有人点了登录。
// 这与 validateCOS 是同一条取向。
//
// 客户端密钥没有配置键、只从环境变量读，因此这里校验的是它**是否已被提供**，
// 而不是它有没有出现在配置文件里。
//
// **对外地址不参与这一对。** 它有自己与渠道无关的用途——命令行登录的批准页
// 地址也由它构造（见 docs/design/identity/device-login.md），因此"只给出对外
// 地址"是合法配置（不启用 GitHub，但命令行登录可用）。把它绑进"三项必须同时
// 给出"，会让"只想要命令行登录"变成一种配不出来的部署。
func validateGithubLogin(c ServerConfig) error {
	hasClientID := c.GithubClientID != ""
	hasSecret := c.GithubClientSecret != ""
	if !hasClientID && !hasSecret {
		return nil // 未启用 GitHub 登录。这是默认情形
	}

	missing := make([]string, 0, 3)
	if !hasClientID {
		missing = append(missing, keyGithubClientID)
	}
	if !hasSecret {
		missing = append(missing, EnvGithubClientSecret)
	}
	if c.PublicBaseURL == "" {
		missing = append(missing, keyPublicBaseURL)
	}
	if len(missing) > 0 {
		return fmt.Errorf("%w: GitHub 登录的配置不完整，缺少 %s：客户端标识与客户端密钥必须同时给出，且启用它时对外地址不能为空",
			ErrInvalid, strings.Join(missing, " 与 "))
	}
	return nil
}

// validateBootstrap 校验引导配置两项齐全。
//
// 与 validateDatabase 同理，这里只判"形状"：引导是否**真的**生效取决于
// 存储中是否存在绑定，那是服务端启动时才知道的事。
func validateBootstrap(b BootstrapConfig) error {
	if b.Ambiguous() {
		return invalidKey(keyBootstrapAdminEmail, EnvBootstrapAdminEmail,
			"与 "+keyBootstrapAdminSubject+"（"+EnvBootstrapAdminSubject+"）互斥，只能给出一个：两个都给等于没说清以谁为准")
	}
	if b.Partial() {
		return invalidKey(keyBootstrapAdminSubject, EnvBootstrapAdminSubject,
			"身份指认（"+keyBootstrapAdminSubject+" 或 "+keyBootstrapAdminEmail+
				"）与 "+keyBootstrapAdminScope+"（"+EnvBootstrapAdminScope+"）必须同时给出或同时留空")
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
