package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/poetlife/aladdin/internal/observability"
)

// clearEnv 清掉本包认识的全部环境变量。
//
// 测试必须对开发者本机的环境变量免疫：留一个 ALADDIN_ADDRESS 在外面，
// "用默认值"这类断言就会在别人机器上莫名其妙地失败。
func clearEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		EnvAddress, EnvLogLevel, EnvLogFile, EnvTimeout, EnvConfig,
		EnvDatabaseDriver, EnvDatabaseDSN,
		EnvCOSBucketURL, EnvCOSSecretID, EnvCOSSecretKey,
		EnvGalaxyPublicBucketURL, EnvGalaxyPublishBaseURL,
		EnvGoogleClientID, EnvGithubClientID, EnvGithubClientSecret, EnvPublicBaseURL,
		EnvBootstrapAdminSubject, EnvBootstrapAdminEmail, EnvBootstrapAdminScope,
	} {
		t.Setenv(k, "")
	}
}

// testPublicBucket 是 galaxy 公开桶的样例地址：形状与配置文件里给的一致。
const testPublicBucket = "https://aladdin-public-1250000000.cos.ap-guangzhou.myqcloud.com"

// galaxyPair 是"发布两项齐全"的最小取值，只把发布域留出来给用例替换。
func galaxyPair(publishBaseURL string) GalaxyConfig {
	return GalaxyConfig{PublicBucketURL: testPublicBucket, PublishBaseURL: publishBaseURL}
}

// isolateHome 把用户配置目录指到临时目录，避免测试写到真实的家目录里。
//
// 同时设 HOME 与 XDG_CONFIG_HOME：os.UserConfigDir 在 macOS 上用前者、
// 在 Linux 上用后者，只设一个会让另一个平台上的测试落到真实目录。
func isolateHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg"))
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("建目录失败: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("写文件失败: %v", err)
	}
}

// cliDir 返回本次测试中 CLI 默认配置文件所在的目录。
func cliDir(t *testing.T) string {
	t.Helper()
	path, err := DefaultCLIPath()
	if err != nil {
		t.Fatalf("取默认路径失败: %v", err)
	}
	return filepath.Dir(path)
}

func TestServerLayerPrecedence(t *testing.T) {
	const (
		fromFile  = "127.0.0.1:1101"
		fromLocal = "127.0.0.1:1102"
		fromEnv   = "127.0.0.1:1103"
	)

	cases := []struct {
		name  string
		file  string
		local string
		env   string
		want  string
	}{
		{"无任何来源用内置默认值", "", "", "", DefaultAddress},
		{"配置文件覆盖默认值", fromFile, "", "", fromFile},
		{"本地覆盖覆盖配置文件", fromFile, fromLocal, "", fromLocal},
		{"环境变量覆盖本地覆盖", fromFile, fromLocal, fromEnv, fromEnv},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clearEnv(t)
			dir := t.TempDir()
			t.Chdir(dir)

			if tc.file != "" {
				write(t, filepath.Join(dir, FileName), "address: "+tc.file+"\n")
			}
			if tc.local != "" {
				write(t, filepath.Join(dir, "config.local.yml"), "address: "+tc.local+"\n")
			}
			if tc.env != "" {
				t.Setenv(EnvAddress, tc.env)
			}

			cfg, err := LoadServer(ServerFlags{})
			if err != nil {
				t.Fatalf("LoadServer: %v", err)
			}
			if cfg.Address != tc.want {
				t.Errorf("address = %q，期望 %q", cfg.Address, tc.want)
			}
		})
	}
}

func TestCLILayerPrecedence(t *testing.T) {
	const (
		fromFile  = "127.0.0.1:2101"
		fromLocal = "127.0.0.1:2102"
		fromEnv   = "127.0.0.1:2103"
		fromFlag  = "127.0.0.1:2104"
	)

	cases := []struct {
		name  string
		file  string
		local string
		env   string
		flag  string
		want  string
	}{
		{"无任何来源用内置默认值", "", "", "", "", DefaultAddress},
		{"配置文件覆盖默认值", fromFile, "", "", "", fromFile},
		{"本地覆盖覆盖配置文件", fromFile, fromLocal, "", "", fromLocal},
		{"环境变量覆盖本地覆盖", fromFile, fromLocal, fromEnv, "", fromEnv},
		{"命令行参数覆盖环境变量", fromFile, fromLocal, fromEnv, fromFlag, fromFlag},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clearEnv(t)
			isolateHome(t)
			dir := cliDir(t)

			if tc.file != "" {
				write(t, filepath.Join(dir, FileName), "address: "+tc.file+"\n")
			}
			if tc.local != "" {
				write(t, filepath.Join(dir, "config.local.yml"), "address: "+tc.local+"\n")
			}
			if tc.env != "" {
				t.Setenv(EnvAddress, tc.env)
			}

			cfg, err := LoadCLI(CLIFlags{Address: tc.flag})
			if err != nil {
				t.Fatalf("LoadCLI: %v", err)
			}
			if cfg.Address != tc.want {
				t.Errorf("address = %q，期望 %q", cfg.Address, tc.want)
			}
		})
	}
}

// 只覆盖显式给出的键：本地覆盖里没写的项必须保持主文件的值。
func TestOnlyExplicitKeysOverride(t *testing.T) {
	clearEnv(t)
	dir := t.TempDir()
	t.Chdir(dir)
	write(t, filepath.Join(dir, FileName), "address: 127.0.0.1:1301\nlog_level: warn\n")
	write(t, filepath.Join(dir, "config.local.yml"), "address: 127.0.0.1:1302\n")

	cfg, err := LoadServer(ServerFlags{})
	if err != nil {
		t.Fatalf("LoadServer: %v", err)
	}
	if cfg.Address != "127.0.0.1:1302" {
		t.Errorf("address = %q，期望本地覆盖的值", cfg.Address)
	}
	if cfg.LogLevel != observability.LevelWarn {
		t.Errorf("本地覆盖没写 log_level，却被改成了 %q", cfg.LogLevel)
	}
}

// "写了个空值"与"压根没写"必须产生不同结果：前者显式清空，后者沿用上层。
func TestEmptyValueClearsButAbsentKeyInherits(t *testing.T) {
	t.Run("空值清空上层取值", func(t *testing.T) {
		clearEnv(t)
		dir := t.TempDir()
		t.Chdir(dir)
		write(t, filepath.Join(dir, FileName), "log_file: /tmp/aladdin.log\n")
		write(t, filepath.Join(dir, "config.local.yml"), `log_file: ""`+"\n")

		cfg, err := LoadServer(ServerFlags{})
		if err != nil {
			t.Fatalf("LoadServer: %v", err)
		}
		if cfg.LogFile != "" {
			t.Errorf("log_file = %q，期望被显式清空", cfg.LogFile)
		}
	})

	t.Run("只写键不写值也算显式清空", func(t *testing.T) {
		clearEnv(t)
		dir := t.TempDir()
		t.Chdir(dir)
		write(t, filepath.Join(dir, FileName), "log_file: /tmp/aladdin.log\n")
		write(t, filepath.Join(dir, "config.local.yml"), "log_file:\n")

		cfg, err := LoadServer(ServerFlags{})
		if err != nil {
			t.Fatalf("LoadServer: %v", err)
		}
		if cfg.LogFile != "" {
			t.Errorf("log_file = %q，期望被显式清空", cfg.LogFile)
		}
	})

	t.Run("不写该键则沿用上层", func(t *testing.T) {
		clearEnv(t)
		dir := t.TempDir()
		t.Chdir(dir)
		write(t, filepath.Join(dir, FileName), "log_file: /tmp/aladdin.log\n")
		write(t, filepath.Join(dir, "config.local.yml"), "address: 127.0.0.1:1401\n")

		cfg, err := LoadServer(ServerFlags{})
		if err != nil {
			t.Fatalf("LoadServer: %v", err)
		}
		if cfg.LogFile != "/tmp/aladdin.log" {
			t.Errorf("log_file = %q，期望沿用主文件的值", cfg.LogFile)
		}
	})
}

// 环境变量为空串等同于没有设置：环境变量无法表达"显式清空"。
func TestEmptyEnvMeansUnset(t *testing.T) {
	clearEnv(t)
	dir := t.TempDir()
	t.Chdir(dir)
	write(t, filepath.Join(dir, FileName), "address: 127.0.0.1:1401\n")
	t.Setenv(EnvAddress, "")

	cfg, err := LoadServer(ServerFlags{})
	if err != nil {
		t.Fatalf("LoadServer: %v", err)
	}
	if cfg.Address != "127.0.0.1:1401" {
		t.Errorf("address = %q，空环境变量不应覆盖配置文件", cfg.Address)
	}
}

func TestFileLocationSemantics(t *testing.T) {
	t.Run("默认位置缺失不是错误", func(t *testing.T) {
		clearEnv(t)
		t.Chdir(t.TempDir())

		cfg, err := LoadServer(ServerFlags{})
		if err != nil {
			t.Fatalf("默认位置缺失不应报错: %v", err)
		}
		if cfg != DefaultServer() {
			t.Errorf("应全用内置默认值，得到 %+v", cfg)
		}
	})

	t.Run("CLI 默认位置缺失不是错误", func(t *testing.T) {
		clearEnv(t)
		isolateHome(t)

		cfg, err := LoadCLI(CLIFlags{})
		if err != nil {
			t.Fatalf("默认位置缺失不应报错: %v", err)
		}
		if cfg != DefaultCLI() {
			t.Errorf("应全用内置默认值，得到 %+v", cfg)
		}
	})

	t.Run("CLI 默认位置的配置确实被读到", func(t *testing.T) {
		clearEnv(t)
		isolateHome(t)
		write(t, filepath.Join(cliDir(t), FileName), "address: 127.0.0.1:2501\n")

		cfg, err := LoadCLI(CLIFlags{})
		if err != nil {
			t.Fatalf("LoadCLI: %v", err)
		}
		if cfg.Address != "127.0.0.1:2501" {
			t.Errorf("address = %q，期望读到默认位置的配置", cfg.Address)
		}
	})

	t.Run("显式指定的文件不存在是错误", func(t *testing.T) {
		clearEnv(t)
		missing := filepath.Join(t.TempDir(), "nope.yml")

		for _, tc := range []struct {
			name string
			load func() error
		}{
			{"命令行指定", func() error {
				_, err := LoadServer(ServerFlags{ConfigPath: missing})
				return err
			}},
			{"环境变量指定", func() error {
				t.Setenv(EnvConfig, missing)
				_, err := LoadServer(ServerFlags{})
				return err
			}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				if err := tc.load(); !errors.Is(err, ErrInvalid) {
					t.Errorf("err = %v，期望 ErrInvalid", err)
				}
			})
		}
	})

	t.Run("格式非法是错误", func(t *testing.T) {
		clearEnv(t)
		dir := t.TempDir()
		t.Chdir(dir)
		write(t, filepath.Join(dir, FileName), "address: [未闭合\n")

		if _, err := LoadServer(ServerFlags{}); !errors.Is(err, ErrInvalid) {
			t.Errorf("err = %v，期望 ErrInvalid", err)
		}
	})

	t.Run("空文件合法", func(t *testing.T) {
		clearEnv(t)
		dir := t.TempDir()
		t.Chdir(dir)
		write(t, filepath.Join(dir, FileName), "")

		cfg, err := LoadServer(ServerFlags{})
		if err != nil {
			t.Fatalf("空文件应合法: %v", err)
		}
		if cfg != DefaultServer() {
			t.Errorf("空文件应等价于没写任何键，得到 %+v", cfg)
		}
	})
}

// 无法识别的键必须报错——包括"拼错的键"与"另一端认识但本端不认识的键"。
func TestUnknownKeysRejected(t *testing.T) {
	cases := []struct {
		name    string
		content string
		load    func() error
	}{
		{
			name:    "拼错的键",
			content: "addres: 127.0.0.1:1\n",
			load:    func() error { _, err := LoadServer(ServerFlags{}); return err },
		},
		{
			name:    "服务端不认识 timeout",
			content: "timeout: 30s\n",
			load:    func() error { _, err := LoadServer(ServerFlags{}); return err },
		},
		{
			name:    "CLI 不认识 log_file",
			content: "log_file: /tmp/x.log\n",
			load:    func() error { _, err := LoadCLI(CLIFlags{}); return err },
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clearEnv(t)
			dir := t.TempDir()
			path := filepath.Join(dir, FileName)
			write(t, path, tc.content)

			if tc.name == "CLI 不认识 log_file" {
				isolateHome(t)
				write(t, filepath.Join(cliDir(t), FileName), tc.content)
			} else {
				t.Chdir(dir)
			}

			if err := tc.load(); !errors.Is(err, ErrInvalid) {
				t.Errorf("err = %v，期望 ErrInvalid", err)
			}
		})
	}
}

// 本地覆盖文件的位置由主文件推导，因此 --config 指向任意路径时也成对出现。
func TestLocalOverrideFollowsExplicitPath(t *testing.T) {
	clearEnv(t)
	dir := t.TempDir()
	main := filepath.Join(dir, "custom.yml")
	write(t, main, "address: 127.0.0.1:1601\n")
	write(t, filepath.Join(dir, "custom.local.yml"), "address: 127.0.0.1:1602\n")

	cfg, err := LoadServer(ServerFlags{ConfigPath: main})
	if err != nil {
		t.Fatalf("LoadServer: %v", err)
	}
	if cfg.Address != "127.0.0.1:1602" {
		t.Errorf("address = %q，期望 custom.local.yml 生效", cfg.Address)
	}
}

func TestLocalOverridePathDerivation(t *testing.T) {
	cases := map[string]string{
		"config.yml":                     "config.local.yml",
		"config.yaml":                    "config.local.yml",
		"config":                         "config.local.yml",
		filepath.Join("a", "custom.yml"): filepath.Join("a", "custom.local.yml"),
	}
	for in, want := range cases {
		if got := localOverridePath(in); got != want {
			t.Errorf("localOverridePath(%q) = %q，期望 %q", in, got, want)
		}
	}
}

// 用例从默认配置出发只改一项，而不是手写整个结构体字面量：
// 后者会在每次新增配置项时集体失败，而失败原因与用例本身无关。
func TestValidate(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*ServerConfig)
		ok     bool
	}{
		{"默认配置", func(*ServerConfig) {}, true},
		{"端口为 0 合法", func(c *ServerConfig) { c.Address = "127.0.0.1:0" }, true},
		{"空地址", func(c *ServerConfig) { c.Address = "" }, false},
		{"空主机名", func(c *ServerConfig) { c.Address = ":9090" }, false},
		{"缺端口", func(c *ServerConfig) { c.Address = "127.0.0.1" }, false},
		{"端口非数字", func(c *ServerConfig) { c.Address = "127.0.0.1:abc" }, false},
		{"端口越界", func(c *ServerConfig) { c.Address = "127.0.0.1:70000" }, false},
		{"日志级别非法", func(c *ServerConfig) { c.LogLevel = "verbose" }, false},
		{"端点合法", func(c *ServerConfig) { c.OTelEndpoint = "collector:4318" }, true},
		{"端点非法", func(c *ServerConfig) { c.OTelEndpoint = "nohost" }, false},
		{"采样比例取 1", func(c *ServerConfig) { c.OTelSampleRatio = 1 }, true},
		{"采样比例为零", func(c *ServerConfig) { c.OTelSampleRatio = 0 }, false},
		{"采样比例大于 1", func(c *ServerConfig) { c.OTelSampleRatio = 1.5 }, false},
		{"采样比例为负", func(c *ServerConfig) { c.OTelSampleRatio = -0.1 }, false},
		{"数据库后端为空", func(c *ServerConfig) { c.Database.Driver = "" }, false},
		{"数据库连接串为空", func(c *ServerConfig) { c.Database.DSN = "" }, false},
		// 后端取值是否属于已知集合不在这里判：合法取值只有 internal/database
		// 一份，在这里再列一遍就是第二个来源。拒绝发生在 database.Open，
		// 仍早于监听端口打开，用例见 internal/database 与 test/e2e。
		{"引导不配置", func(c *ServerConfig) { c.Bootstrap = BootstrapConfig{} }, true},
		{"引导两项齐全", func(c *ServerConfig) {
			c.Bootstrap = BootstrapConfig{Subject: "google:1", Scope: "root"}
		}, true},
		{"引导用邮箱指认主人", func(c *ServerConfig) {
			c.Bootstrap = BootstrapConfig{Email: "admin@example.com", Scope: "root"}
		}, true},
		{"引导作用域写全局哨兵", func(c *ServerConfig) {
			c.Bootstrap = BootstrapConfig{Email: "admin@example.com", Scope: "<global>"}
		}, true},
		{"引导两种身份指认同时给出", func(c *ServerConfig) {
			c.Bootstrap = BootstrapConfig{Subject: "google:1", Email: "admin@example.com", Scope: "root"}
		}, false},
		{"引导只填主体", func(c *ServerConfig) {
			c.Bootstrap = BootstrapConfig{Subject: "google:1"}
		}, false},
		{"引导只填邮箱", func(c *ServerConfig) {
			c.Bootstrap = BootstrapConfig{Email: "admin@example.com"}
		}, false},
		{"引导只填作用域", func(c *ServerConfig) {
			c.Bootstrap = BootstrapConfig{Scope: "root"}
		}, false},
		{"客户端标识为空即未启用", func(c *ServerConfig) { c.GoogleClientID = "" }, true},
		// galaxy 的发布存储：两项必须成对，且发布域不得与主应用同站。
		{"发布未配置", func(c *ServerConfig) { c.Galaxy = GalaxyConfig{} }, true},
		{"发布两项齐全", func(c *ServerConfig) { c.Galaxy = galaxyPair("https://pub.example.com") }, true},
		{"发布只给公开桶", func(c *ServerConfig) {
			c.Galaxy = GalaxyConfig{PublicBucketURL: testPublicBucket}
		}, false},
		{"发布只给发布域", func(c *ServerConfig) {
			c.Galaxy = GalaxyConfig{PublishBaseURL: "https://pub.example.com"}
		}, false},
		{"公开桶不是 https", func(c *ServerConfig) {
			c.Galaxy = galaxyPair("https://pub.example.com")
			c.Galaxy.PublicBucketURL = "http://aladdin-public-1250000000.cos.ap-guangzhou.myqcloud.com"
		}, false},
		{"发布域不是绝对地址", func(c *ServerConfig) {
			c.Galaxy = galaxyPair("pub.example.com")
		}, false},
		{"发布域带路径", func(c *ServerConfig) {
			c.Galaxy = galaxyPair("https://pub.example.com/g")
		}, false},
		{"发布域与对外地址同源", func(c *ServerConfig) {
			c.PublicBaseURL = "https://app.example.com"
			c.Galaxy = galaxyPair("https://app.example.com")
		}, false},
		{"发布域与对外地址同注册域", func(c *ServerConfig) {
			c.PublicBaseURL = "https://app.example.com"
			c.Galaxy = galaxyPair("https://pub.example.com")
		}, false},
		{"发布域多段后缀同注册域", func(c *ServerConfig) {
			c.PublicBaseURL = "https://app.example.co.uk"
			c.Galaxy = galaxyPair("https://pub.example.co.uk")
		}, false},
		{"发布域多段后缀不同注册域", func(c *ServerConfig) {
			c.PublicBaseURL = "https://app.example.co.uk"
			c.Galaxy = galaxyPair("https://pub.other.co.uk")
		}, true},
		{"发布域不同注册域", func(c *ServerConfig) {
			c.PublicBaseURL = "https://app.example.com"
			c.Galaxy = galaxyPair("https://pages.example.net")
		}, true},
		{"主应用未配置对外地址时发布域合法", func(c *ServerConfig) {
			c.Galaxy = galaxyPair("https://pub.example.com")
		}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := DefaultServer()
			tc.mutate(&cfg)

			err := cfg.Validate()
			if tc.ok && err != nil {
				t.Errorf("期望合法，得到 %v", err)
			}
			if !tc.ok && !errors.Is(err, ErrInvalid) {
				t.Errorf("期望 ErrInvalid，得到 %v", err)
			}
		})
	}
}

// 采样比例为 0 会让全部链路被静默丢弃，而"没有数据"这种表象极难归因到
// 一行配置上，因此必须拒绝启动而不是容忍。
func TestZeroSampleRatioRejected(t *testing.T) {
	cli := DefaultCLI()
	cli.OTelSampleRatio = 0
	if err := cli.Validate(); !errors.Is(err, ErrInvalid) {
		t.Errorf("CLI 采样比例为零期望 ErrInvalid，得到 %v", err)
	}
}

// 类型转换失败必须报出来：静默当成默认值会让用户以为"配了"，
// 而实际跑的是默认行为。
func TestTelemetryValuesMustParse(t *testing.T) {
	cases := []struct {
		name    string
		content string
	}{
		{"otel_insecure 不是布尔", "otel_insecure: 也许\n"},
		{"otel_sample_ratio 不是数字", "otel_sample_ratio: 一半\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clearEnv(t)
			dir := t.TempDir()
			t.Chdir(dir)
			write(t, filepath.Join(dir, FileName), tc.content)

			if _, err := LoadServer(ServerFlags{}); !errors.Is(err, ErrInvalid) {
				t.Errorf("err = %v，期望 ErrInvalid", err)
			}
		})
	}
}

func TestValidateCLITimeout(t *testing.T) {
	base := DefaultCLI()
	if err := base.Validate(); err != nil {
		t.Fatalf("默认配置应合法: %v", err)
	}

	zero := DefaultCLI()
	zero.Timeout = 0
	if err := zero.Validate(); !errors.Is(err, ErrInvalid) {
		t.Errorf("超时为零期望 ErrInvalid，得到 %v", err)
	}

	negative := DefaultCLI()
	negative.Timeout = -time.Second
	if err := negative.Validate(); !errors.Is(err, ErrInvalid) {
		t.Errorf("超时为负期望 ErrInvalid，得到 %v", err)
	}
}

// 两端默认地址同源：本机开发零配置互通依赖这一点。
func TestDefaultAddressShared(t *testing.T) {
	if DefaultServer().Address != DefaultCLI().Address {
		t.Errorf("两端默认地址不一致：服务端 %q，CLI %q",
			DefaultServer().Address, DefaultCLI().Address)
	}
}

// 数据库的两个键必须各自独立合并。
//
// 串了线的表现是"改了连接串却连到别处"，而配置看起来完全正确——这正是
// 五层合并里最难从现象反推回去的一类错误，因此单独守着。
func TestDatabaseKeysMergeIndependently(t *testing.T) {
	clearEnv(t)
	dir := t.TempDir()
	t.Chdir(dir)
	write(t, filepath.Join(dir, FileName), "database_driver: sqlite\ndatabase_dsn: from-file.db\n")
	write(t, filepath.Join(dir, "config.local.yml"), "database_dsn: from-local.db\n")

	// 本地覆盖只写了 dsn：driver 必须保持主文件的值。
	cfg, err := LoadServer(ServerFlags{})
	if err != nil {
		t.Fatalf("LoadServer: %v", err)
	}
	if cfg.Database.Driver != "sqlite" {
		t.Errorf("driver = %q，期望沿用主文件的值", cfg.Database.Driver)
	}
	if cfg.Database.DSN != "from-local.db" {
		t.Errorf("dsn = %q，期望取本地覆盖的值", cfg.Database.DSN)
	}

	// 环境变量只覆盖 dsn：driver 仍必须保持原值。
	t.Setenv(EnvDatabaseDSN, "from-env.db")
	cfg, err = LoadServer(ServerFlags{})
	if err != nil {
		t.Fatalf("LoadServer: %v", err)
	}
	if cfg.Database.Driver != "sqlite" {
		t.Errorf("driver = %q，被环境变量串改了", cfg.Database.Driver)
	}
	if cfg.Database.DSN != "from-env.db" {
		t.Errorf("dsn = %q，期望取环境变量的值", cfg.Database.DSN)
	}
}

// 校验错误必须指认出错的键，否则用户不知道该改哪一项。
func TestValidationErrorNamesKey(t *testing.T) {
	bad := DefaultCLI()
	bad.LogLevel = "verbose"

	err := bad.Validate()
	if err == nil {
		t.Fatal("期望报错")
	}
	for _, want := range []string{keyLogLevel, EnvLogLevel} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("错误信息 %q 缺少 %q", err, want)
		}
	}
}

// 示例配置文件是用户的上手入口。没有测试守着，它一定会与实现漂移——
// 而漂移的表现形式是"照着示例改还是没生效"。
func TestExampleConfigsMatchSchema(t *testing.T) {
	t.Run("服务端示例", func(t *testing.T) {
		raw := readExample(t, filepath.Join("..", "..", "config.server.example.yml"))
		checkKeys(t, raw, serverKeys)

		dir := t.TempDir()
		write(t, filepath.Join(dir, FileName), raw)
		clearEnv(t)
		t.Chdir(dir)
		if _, err := LoadServer(ServerFlags{}); err != nil {
			t.Errorf("示例配置无法被服务端加载: %v", err)
		}
	})

	t.Run("CLI 示例", func(t *testing.T) {
		raw := readExample(t, filepath.Join("..", "..", "config.cli.example.yml"))
		checkKeys(t, raw, cliKeys)

		clearEnv(t)
		isolateHome(t)
		write(t, filepath.Join(cliDir(t), FileName), raw)
		if _, err := LoadCLI(CLIFlags{}); err != nil {
			t.Errorf("示例配置无法被 CLI 加载: %v", err)
		}
	})
}

// 声明了却不生效的键，会以"配置写了但没反应"的形式浪费使用者的时间。
// 每个声明过的键都必须真的能改变配置——新增键时不补样例，这个测试会先失败。
func TestDeclaredKeysAllTakeEffect(t *testing.T) {
	t.Run("服务端", func(t *testing.T) {
		samples := map[string]string{
			keyAddress:               "127.0.0.1:3101",
			keyLogLevel:              "warn",
			keyLogFile:               "/tmp/aladdin-test.log",
			keyDatabaseDriver:        "mysql",
			keyDatabaseDSN:           "aladdin@tcp(127.0.0.1:3306)/aladdin",
			keyCOSBucketURL:          "https://aladdin-1250000000.cos.ap-guangzhou.myqcloud.com",
			keyGalaxyPublicBucketURL: "https://aladdin-public-1250000000.cos.ap-guangzhou.myqcloud.com",
			keyGalaxyPublishBaseURL:  "https://pub.example.com",
			keyGoogleClientID:        "1234567890.apps.googleusercontent.com",
			keyGithubClientID:        "Iv1.0123456789abcdef",
			keyPublicBaseURL:         "https://aladdin.example.com",
			keyBootstrapAdminSubject: "google:110000000000000000001",
			keyBootstrapAdminEmail:   "admin@example.com",
			keyBootstrapAdminScope:   "root",
			keyOTelEndpoint:          "collector:4318",
			keyOTelInsecure:          "true",
			keyOTelSampleRatio:       "0.5",
		}
		// 身份指认与作用域必须成对出现。单独写一项会被校验拒绝，那样这个用例
		// 测的就成了"半套配置被拒吗"——那是另一回事，另有专门的用例守着。
		companions := map[string]string{
			keyBootstrapAdminSubject: keyBootstrapAdminScope + ": root\n",
			keyBootstrapAdminEmail:   keyBootstrapAdminScope + ": root\n",
			keyBootstrapAdminScope:   keyBootstrapAdminSubject + ": google:110000000000000000001\n",
			// 重定向型登录渠道的三项也必须成对出现，理由同上。
			keyGithubClientID: keyPublicBaseURL + ": https://aladdin.example.com\n",
			keyPublicBaseURL:  keyGithubClientID + ": Iv1.0123456789abcdef\n",
			// 发布的两项同理：只给其中之一会被校验拒绝，那样测的就成了
			// "半套配置被拒吗"——那是另一回事，另有专门的用例守着。
			keyGalaxyPublicBucketURL: keyGalaxyPublishBaseURL + ": https://pub.example.com\n",
			keyGalaxyPublishBaseURL:  keyGalaxyPublicBucketURL + ": https://aladdin-public-1250000000.cos.ap-guangzhou.myqcloud.com\n",
		}
		// 生效前提来自环境变量的键。头像桶地址本身不是秘密、可以写进配置文件，
		// 但它的两项密钥只有环境变量这一个来源（见 EnvCOSSecretID），因此这个
		// 键必须连环境变量一起给出——否则走的是"半套配置拒绝启动"那条路径，
		// 而那是另一回事，另有专门的用例守着。GitHub 的客户端密钥同理。
		envCompanions := map[string]map[string]string{
			keyCOSBucketURL: {
				EnvCOSSecretID:  "test-secret-id",
				EnvCOSSecretKey: "test-secret-key",
			},
			keyGithubClientID: {EnvGithubClientSecret: "test-github-secret"},
			keyPublicBaseURL:  {EnvGithubClientSecret: "test-github-secret"},
		}

		for _, key := range serverKeys {
			sample, ok := samples[key]
			if !ok {
				t.Fatalf("服务端键 %q 没有测试样例——新增键时必须补上", key)
			}

			clearEnv(t)
			for name, value := range envCompanions[key] {
				t.Setenv(name, value)
			}
			dir := t.TempDir()
			t.Chdir(dir)
			write(t, filepath.Join(dir, FileName), key+": "+sample+"\n"+companions[key])

			cfg, err := LoadServer(ServerFlags{})
			if err != nil {
				t.Fatalf("键 %s 的样例无法加载: %v", key, err)
			}
			if cfg == DefaultServer() {
				t.Errorf("键 %q 声明了却不生效", key)
			}
		}
	})

	t.Run("CLI", func(t *testing.T) {
		samples := map[string]string{
			keyAddress:         "127.0.0.1:3201",
			keyLogLevel:        "debug",
			keyTimeout:         "5s",
			keyOTelEndpoint:    "collector:4318",
			keyOTelInsecure:    "true",
			keyOTelSampleRatio: "0.5",
		}
		for _, key := range cliKeys {
			sample, ok := samples[key]
			if !ok {
				t.Fatalf("CLI 键 %q 没有测试样例——新增键时必须补上", key)
			}

			clearEnv(t)
			isolateHome(t)
			write(t, filepath.Join(cliDir(t), FileName), key+": "+sample+"\n")

			cfg, err := LoadCLI(CLIFlags{})
			if err != nil {
				t.Fatalf("键 %s 的样例无法加载: %v", key, err)
			}
			if cfg == DefaultCLI() {
				t.Errorf("键 %q 声明了却不生效", key)
			}
		}
	})
}

func readExample(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读示例配置失败: %v", err)
	}
	return string(raw)
}

// checkKeys 断言示例里出现的键与实现声明的键集合一致——两个方向都要查：
// 示例里多一个键会让照抄的人启动失败，少一个键则让那一项无从发现。
func checkKeys(t *testing.T, raw string, declared []string) {
	t.Helper()

	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatalf("解析示例配置失败: %v", err)
	}

	inFile := topLevelKeys(t, &doc)
	inDecl := make(map[string]bool, len(declared))
	for _, k := range declared {
		inDecl[k] = true
	}

	for k := range inFile {
		if !inDecl[k] {
			t.Errorf("示例里有实现不认识的键 %q——照抄它会启动失败", k)
		}
	}
	for _, k := range declared {
		if !inFile[k] {
			t.Errorf("实现声明的键 %q 没有出现在示例里", k)
		}
	}
}

// 头像存储的四种情形：全空不启用、全给启用，两种半套都拒绝启动。
func TestValidateCOS(t *testing.T) {
	const bucket = "https://aladdin-1250000000.cos.ap-guangzhou.myqcloud.com"
	full := COSConfig{BucketURL: bucket, SecretID: "id", SecretKey: "key"}

	t.Run("全空即未启用", func(t *testing.T) {
		if err := validateCOS(COSConfig{}); err != nil {
			t.Errorf("err = %v，期望不启用且不报错", err)
		}
	})

	t.Run("全给即启用", func(t *testing.T) {
		if err := validateCOS(full); err != nil {
			t.Errorf("err = %v，期望通过", err)
		}
	})

	t.Run("有桶没密钥", func(t *testing.T) {
		cos := full
		cos.SecretID, cos.SecretKey = "", ""
		err := validateCOS(cos)
		if err == nil {
			t.Fatal("期望拒绝启动")
		}
		// 错误信息必须指出缺的是哪一项，否则用户只知道"配错了"。
		for _, want := range []string{EnvCOSSecretID, EnvCOSSecretKey} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("错误信息 %q 缺少 %q", err, want)
			}
		}
	})

	t.Run("只缺一项密钥", func(t *testing.T) {
		cos := full
		cos.SecretKey = ""
		err := validateCOS(cos)
		if err == nil {
			t.Fatal("期望拒绝启动")
		}
		if !strings.Contains(err.Error(), EnvCOSSecretKey) {
			t.Errorf("错误信息 %q 应当指出缺的是 %s", err, EnvCOSSecretKey)
		}
	})

	t.Run("有密钥没桶", func(t *testing.T) {
		cos := full
		cos.BucketURL = ""
		err := validateCOS(cos)
		if err == nil {
			t.Fatal("期望拒绝启动：密钥有主、桶没主")
		}
		if !strings.Contains(err.Error(), keyCOSBucketURL) {
			t.Errorf("错误信息 %q 应当指出 %s 为空", err, keyCOSBucketURL)
		}
	})

	t.Run("桶地址不是 https", func(t *testing.T) {
		for _, bad := range []string{
			"http://aladdin-1250000000.cos.ap-guangzhou.myqcloud.com",
			"aladdin-1250000000.cos.ap-guangzhou.myqcloud.com",
			"https://",
		} {
			cos := full
			cos.BucketURL = bad
			if err := validateCOS(cos); err == nil {
				t.Errorf("桶地址 %q 被接受了，期望拒绝启动", bad)
			}
		}
	})
}

// 密钥只从环境变量来，且半套配置在真实加载路径上就拒绝启动。
func TestCOSSecretsComeFromEnv(t *testing.T) {
	const bucket = "https://aladdin-1250000000.cos.ap-guangzhou.myqcloud.com"
	dir := t.TempDir()
	t.Chdir(dir)
	clearEnv(t)

	t.Setenv(EnvCOSBucketURL, bucket)
	t.Setenv(EnvCOSSecretID, "the-id")
	if _, err := LoadServer(ServerFlags{}); err == nil {
		t.Fatal("只给了 SecretID，期望拒绝启动")
	} else if !strings.Contains(err.Error(), EnvCOSSecretKey) {
		t.Errorf("错误信息 %q 应当指出缺的是 %s", err, EnvCOSSecretKey)
	}

	t.Setenv(EnvCOSSecretKey, "the-key")
	cfg, err := LoadServer(ServerFlags{})
	if err != nil {
		t.Fatalf("三项齐全仍加载失败: %v", err)
	}
	if !cfg.COS.Enabled() {
		t.Error("三项齐全时头像应当已启用")
	}
	if cfg.COS.BucketURL != bucket || cfg.COS.SecretID != "the-id" || cfg.COS.SecretKey != "the-key" {
		t.Errorf("cfg.COS = %+v，与设置的不一致", cfg.COS)
	}
}

// **密钥不可由配置提供。**
//
// 这条不是风格偏好：一旦它们有了配置键，它们就会出现在某个人的 config.yml 里，
// 而那个文件会进版本库、进镜像、被贴给别人排查问题。因此写进配置文件必须被
// 当成"无法识别的键"拒绝，而不是被静默接受。
func TestCOSSecretsAreNotConfigKeys(t *testing.T) {
	for _, key := range []string{"cos_secret_id", "cos_secret_key"} {
		dir := t.TempDir()
		t.Chdir(dir)
		clearEnv(t)
		write(t, filepath.Join(dir, FileName), key+": some-secret\n")

		if _, err := LoadServer(ServerFlags{}); !errors.Is(err, ErrInvalid) {
			t.Errorf("把 %s 写进配置文件 = %v，期望 ErrInvalid（未知键）", key, err)
		}
	}
}

// Describe 是"把这项配置写进启动日志"的唯一安全入口：含桶地址，不含密钥。
func TestCOSDescribeHidesSecrets(t *testing.T) {
	cos := COSConfig{
		BucketURL: "https://aladdin-1250000000.cos.ap-guangzhou.myqcloud.com",
		SecretID:  "the-id",
		SecretKey: "the-key",
	}

	got := cos.Describe()
	for _, secret := range []string{"the-id", "the-key"} {
		if strings.Contains(got, secret) {
			t.Errorf("Describe = %q，不该含密钥", got)
		}
	}
	if !strings.Contains(got, cos.BucketURL) {
		t.Errorf("Describe = %q，应当含桶地址——那是回答「这一项到底有没有被读到」的唯一线索", got)
	}

	if empty := (COSConfig{}).Describe(); empty != "未启用" {
		t.Errorf("未启用时 Describe = %q，期望 %q", empty, "未启用")
	}
}

func TestValidatePublicBaseURL(t *testing.T) {
	t.Run("未配置是合法的", func(t *testing.T) {
		if err := validatePublicBaseURL(""); err != nil {
			t.Errorf("err = %v，期望空值放行（是否需要它由登录渠道的校验决定）", err)
		}
	})

	t.Run("接受的形式", func(t *testing.T) {
		for _, ok := range []string{
			"https://aladdin.example.com",
			"https://aladdin.example.com/",
			// 本地回环允许 http：本地开发没有证书，写死 https 会让登录在本机跑不通。
			"http://localhost:5173",
			"http://127.0.0.1:5173",
			"http://[::1]:5173",
		} {
			if err := validatePublicBaseURL(ok); err != nil {
				t.Errorf("%q 被拒绝了：%v", ok, err)
			}
		}
	})

	t.Run("拒绝的形式", func(t *testing.T) {
		for _, bad := range []string{
			"aladdin.example.com",              // 不是绝对地址
			"https://",                         // 没有主机名
			"https://aladdin.example.com/x",    // 带路径：会衍生出一个也要登记的回调地址
			"https://user@aladdin.example.com", // 带用户信息
			"https://aladdin.example.com?a=1",  // 带查询串
			"https://aladdin.example.com#f",    // 带 fragment
			"http://aladdin.example.com",       // 非回环主机必须 https
		} {
			if err := validatePublicBaseURL(bad); err == nil {
				t.Errorf("%q 被接受了，期望拒绝启动", bad)
			}
		}
	})
}

// GitHub 登录的标识与密钥必须成对给出：缺任何一项都拒绝启动，且错误信息指出
// 缺的是哪一项。
//
// **对外地址不参与这一对**：它有自己的、与渠道无关的用途（命令行登录的批准页
// 地址也由它构造），因此"只有对外地址"是合法配置。
func TestValidateGithubLogin(t *testing.T) {
	full := ServerConfig{
		GithubClientID:     "Iv1.0123456789abcdef",
		GithubClientSecret: "the-secret",
		PublicBaseURL:      "https://aladdin.example.com",
	}

	t.Run("全空即未启用", func(t *testing.T) {
		if err := validateGithubLogin(ServerConfig{}); err != nil {
			t.Errorf("err = %v，期望不启用且不报错", err)
		}
	})

	t.Run("只有对外地址是合法配置", func(t *testing.T) {
		// 这条路径是"只想要命令行登录"的部署：它不需要任何渠道凭据。
		if err := validateGithubLogin(ServerConfig{PublicBaseURL: full.PublicBaseURL}); err != nil {
			t.Errorf("err = %v，期望通过——对外地址不依赖 GitHub", err)
		}
	})

	t.Run("全给即启用", func(t *testing.T) {
		if err := validateGithubLogin(full); err != nil {
			t.Errorf("err = %v，期望通过", err)
		}
	})

	t.Run("缺什么就指出什么", func(t *testing.T) {
		cases := map[string]struct {
			cfg  ServerConfig
			want string
		}{
			"缺客户端密钥": {ServerConfig{GithubClientID: full.GithubClientID, PublicBaseURL: full.PublicBaseURL}, EnvGithubClientSecret},
			"缺对外地址":  {ServerConfig{GithubClientID: full.GithubClientID, GithubClientSecret: "s"}, keyPublicBaseURL},
			"缺客户端标识": {ServerConfig{GithubClientSecret: "s", PublicBaseURL: full.PublicBaseURL}, keyGithubClientID},
		}
		for name, c := range cases {
			err := validateGithubLogin(c.cfg)
			if err == nil {
				t.Errorf("%s：期望拒绝启动", name)
				continue
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("%s：错误信息 %q 应当指出缺的是 %q", name, err, c.want)
			}
		}
	})
}

// 客户端密钥只从环境变量来，半套配置在真实加载路径上就拒绝启动。
func TestGithubClientSecretComesFromEnv(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	clearEnv(t)

	t.Setenv(EnvGithubClientID, "Iv1.0123456789abcdef")
	t.Setenv(EnvPublicBaseURL, "https://aladdin.example.com")
	if _, err := LoadServer(ServerFlags{}); err == nil {
		t.Fatal("没给客户端密钥，期望拒绝启动")
	} else if !strings.Contains(err.Error(), EnvGithubClientSecret) {
		t.Errorf("错误信息 %q 应当指出缺的是 %s", err, EnvGithubClientSecret)
	}

	t.Setenv(EnvGithubClientSecret, "the-secret")
	cfg, err := LoadServer(ServerFlags{})
	if err != nil {
		t.Fatalf("三项齐全仍加载失败: %v", err)
	}
	if cfg.GithubClientSecret != "the-secret" {
		t.Error("客户端密钥没有从环境变量读进来")
	}
	if got := cfg.PublicURL("/auth/github/callback"); got != "https://aladdin.example.com/auth/github/callback" {
		t.Errorf("PublicURL = %q", got)
	}
}

// **客户端密钥不可由配置提供**，与 COS 密钥同理：一旦它有了配置键，它就会
// 出现在某个人的 config.yml 里，而那个文件会进版本库、进镜像。
func TestGithubClientSecretIsNotConfigKey(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	clearEnv(t)
	write(t, filepath.Join(dir, FileName), "github_client_secret: some-secret\n")

	if _, err := LoadServer(ServerFlags{}); !errors.Is(err, ErrInvalid) {
		t.Errorf("把 github_client_secret 写进配置文件 = %v，期望 ErrInvalid（未知键）", err)
	}
}

func topLevelKeys(t *testing.T, node *yaml.Node) map[string]bool {
	t.Helper()
	if node.Kind == yaml.DocumentNode {
		if len(node.Content) == 0 {
			return map[string]bool{}
		}
		node = node.Content[0]
	}
	if node.Kind != yaml.MappingNode {
		t.Fatalf("示例配置的顶层不是映射")
	}
	out := map[string]bool{}
	for i := 0; i+1 < len(node.Content); i += 2 {
		out[node.Content[i].Value] = true
	}
	return out
}
