package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// FileName 是配置文件的约定名。两端各自在默认位置找同名文件。
const FileName = "config.yml"

// otelKeys 是两端**共有**的可观测性配置键：服务端与 CLI 都需要上报链路与指标。
//
// 单独列出来是为了让"这几个键在两端都有"这件事一眼可见——否则新增键时
// 很容易只加到一端，表现为"CLI 配了不生效"。
var otelKeys = []string{keyOTelEndpoint, keyOTelInsecure, keyOTelSampleRatio}

// 两端各自认识的键。刻意不共用：timeout 对服务端没有意义，log_file 对 CLI
// 没有意义，出现在对方的配置里必须报错而不是被静默忽略。
//
// 这两份清单是"哪些键合法"的唯一来源，也驱动示例配置的一致性检查。
// 新增键时必须同时补上取值映射与测试样例，见 config_test.go 的
// TestDeclaredKeysAllTakeEffect。
var (
	serverKeys = append([]string{keyAddress, keyLogLevel, keyLogFile}, otelKeys...)
	cliKeys    = append([]string{keyAddress, keyLogLevel, keyTimeout}, otelKeys...)
)

// fileValues 是一份配置文件里"出现过的键 → 原始取值"的映射。
//
// 键出现在映射里就表示它在文件里被写了——哪怕只写了键、值为空，那也是
// "显式清空这一项"，与"压根没写"是两回事。合并语义依赖这个区分。
type fileValues map[string]string

// str 返回指向取值的指针；键未出现时返回 nil，表示不参与覆盖。
func (v fileValues) str(key string) *string {
	s, ok := v[key]
	if !ok {
		return nil
	}
	return &s
}

func serverLayer(v fileValues) (layer, error) {
	l := layer{
		address:      v.str(keyAddress),
		logLevel:     v.str(keyLogLevel),
		logFile:      v.str(keyLogFile),
		otelEndpoint: v.str(keyOTelEndpoint),
	}
	if err := applyTelemetryScalars(v, &l); err != nil {
		return layer{}, err
	}
	return l, nil
}

func cliLayer(v fileValues) (layer, error) {
	l := layer{
		address:      v.str(keyAddress),
		logLevel:     v.str(keyLogLevel),
		otelEndpoint: v.str(keyOTelEndpoint),
	}
	if err := applyTelemetryScalars(v, &l); err != nil {
		return layer{}, err
	}
	if p := v.str(keyTimeout); p != nil {
		d, err := time.ParseDuration(*p)
		if err != nil {
			return layer{}, invalidKey(keyTimeout, EnvTimeout, "不是合法的时长: "+err.Error())
		}
		l.timeout = &d
	}
	return l, nil
}

// applyTelemetryScalars 解析两端共有的非字符串取值的可观测性配置。
//
// 与 validateTelemetry 同理：它在两端同名同义，因此只解析一处。
// 配置里存的是字符串，类型转换失败必须报出来——静默当成默认值会让用户
// 以为"配了"，而实际跑的是默认行为。
func applyTelemetryScalars(v fileValues, l *layer) error {
	if p := v.str(keyOTelInsecure); p != nil {
		b, err := strconv.ParseBool(*p)
		if err != nil {
			return invalidKey(keyOTelInsecure, EnvOTelInsecure, "不是布尔值: "+err.Error())
		}
		l.otelInsecure = &b
	}
	if p := v.str(keyOTelSampleRatio); p != nil {
		f, err := strconv.ParseFloat(*p, 64)
		if err != nil {
			return invalidKey(keyOTelSampleRatio, EnvOTelSampleRatio, "不是数字: "+err.Error())
		}
		l.otelSampleRatio = &f
	}
	return nil
}

// DefaultServerPath 返回服务端默认的配置文件位置：启动时的工作目录。
//
// 不读 /etc 之类的系统级固定路径：读一个用户没有显式指定的、位于启动目录
// 之外的文件，会让"配置为什么不生效"变成需要猜的问题。生产环境从固定位置
// 读取时，由服务单元的启动参数显式指定。
func DefaultServerPath() string {
	return FileName
}

// DefaultCLIPath 返回 CLI 默认的配置文件位置：用户配置目录下，
// 与凭证文件同目录但不同文件。
//
// 不共用文件：配置可以提交、可以贴给别人、可以写进镜像，凭证不可以。
func DefaultCLIPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "aladdin", FileName), nil
}

// locateFile 按 命令行参数 > 环境变量 > 默认位置 的顺序确定配置文件。
//
// explicit 为 true 表示路径由用户显式给出（参数或环境变量），
// 此时"文件不存在"是错误——指定动作本身表明"我认为那里有配置"，
// 静默降级会掩盖路径拼写错误。为 false 时缺失表示"无配置文件"，不是错误。
func locateFile(flagPath, defaultPath string) (path string, explicit bool) {
	if flagPath != "" {
		return flagPath, true
	}
	if v := os.Getenv(EnvConfig); v != "" {
		return v, true
	}
	return defaultPath, false
}

// localOverridePath 由主配置文件的路径推导本地覆盖文件的位置：
// 同名但插入 .local（config.yml → config.local.yml）。
//
// 由主文件推导而不是固定取 config.local.yml，是为了让 --config 指向任意
// 路径时，覆盖文件仍与它成对出现、可预期。
func localOverridePath(path string) string {
	dir, base := filepath.Dir(path), filepath.Base(path)
	for _, ext := range []string{".yml", ".yaml"} {
		if trimmed := strings.TrimSuffix(base, ext); trimmed != base {
			return filepath.Join(dir, trimmed+".local.yml")
		}
	}
	return filepath.Join(dir, base+".local.yml")
}

// readFileLayer 读取一个配置文件并转换为 layer。
//
// 主文件与本地覆盖文件走同一条路径，差别只在 required：
// 缺失时后者跳过、前者按 explicit 决定，格式错误与无法识别的键一律报错。
func readFileLayer(path string, required bool, parse func(string) (layer, error)) (layer, error) {
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			if required {
				return layer{}, fmt.Errorf("%w: 配置文件 %s 不存在", ErrInvalid, path)
			}
			return layer{}, nil
		}
		return layer{}, fmt.Errorf("%w: 无法读取配置文件 %s: %v", ErrInvalid, path, err)
	}
	return parse(path)
}

func parseServerFile(path string) (layer, error) {
	v, err := readFileValues(path, serverKeys)
	if err != nil {
		return layer{}, err
	}
	return serverLayer(v)
}

func parseCLIFile(path string) (layer, error) {
	v, err := readFileValues(path, cliKeys)
	if err != nil {
		return layer{}, err
	}
	return cliLayer(v)
}

// readFileValues 解析配置文件，取出出现过的键，并拒绝不属于本端的键。
//
// 手工遍历映射，而不是把未知键检查交给 yaml 的 KnownFields：yaml.v3 在值为
// null 时不会调用自定义解码器，于是 `log_file:`（写了键、没写值）会被当成
// "压根没写"，而它的语义应当是"显式清空"。自己走一遍才能同时拿到精确的
// 键集合与能指名道姓的未知键报错。
func readFileValues(path string, allowed []string) (fileValues, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // 路径来源受控，非用户输入
	if err != nil {
		return nil, fmt.Errorf("%w: 无法读取配置文件 %s: %v", ErrInvalid, path, err)
	}
	// 空文件合法，等价于"一个键都没写"。
	if strings.TrimSpace(string(raw)) == "" {
		return fileValues{}, nil
	}

	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("%w: 解析配置文件 %s 失败: %v", ErrInvalid, path, err)
	}
	root := &doc
	if root.Kind == yaml.DocumentNode {
		if len(root.Content) == 0 {
			return fileValues{}, nil
		}
		root = root.Content[0]
	}
	if root.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("%w: 配置文件 %s 的顶层必须是键值映射", ErrInvalid, path)
	}

	recognized := make(map[string]bool, len(allowed))
	for _, k := range allowed {
		recognized[k] = true
	}

	out := make(fileValues, len(root.Content)/2)
	for i := 0; i+1 < len(root.Content); i += 2 {
		key := root.Content[i].Value
		// 无法识别的键必须报错：拼错的键名若被静默忽略，用户会以为配置已生效，
		// 而实际跑的是默认值——这在服务端表现为"改了半天配置没反应"。
		if !recognized[key] {
			return nil, fmt.Errorf("%w: 配置文件 %s 中出现无法识别的键 %q", ErrInvalid, path, key)
		}

		// 只写了键、没写值（YAML null）与写了空值是同一件事：显式清空。
		value := ""
		if node := root.Content[i+1]; node.Tag != "!!null" {
			if err := node.Decode(&value); err != nil {
				return nil, fmt.Errorf("%w: 配置文件 %s 的 %s 取值无法解析: %v",
					ErrInvalid, path, key, err)
			}
		}
		out[key] = value
	}
	return out, nil
}
