package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/poetlife/aladdin/internal/auth"
	"github.com/poetlife/aladdin/internal/config"
	"github.com/poetlife/aladdin/internal/observability"
	"github.com/poetlife/aladdin/pkg/client"
)

// globalFlags 是全部子命令共享的全局参数。
type globalFlags struct {
	address string
	token   string
	scope   string
	output  string
	debug   bool
	yes     bool
	timeout time.Duration
}

var flags globalFlags

// Execute 运行命令行并返回进程退出码。
func Execute() int {
	root := newRootCommand()
	if err := root.Execute(); err != nil {
		printf(os.Stderr, "aladdin: %s\n", describeError(err))
		return exitCodeFor(err)
	}
	return exitOK
}

func newRootCommand() *cobra.Command {
	root := &cobra.Command{
		Use:   "aladdin",
		Short: "阿拉丁神灯命令行",
		Long: `aladdin 命令行客户端。

凭证按 参数 > 环境变量 > 凭证文件 的优先级解析。
权限判定发生在服务端；本工具不缓存也不复用任何权限判定结果。`,
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			if err := checkCommands(cmd.Root()); err != nil {
				return err
			}
			// 危险操作在非交互式环境下不弹确认、也不默认放行：
			// 要求显式传入 --yes，否则直接以用法错误退出。
			if isDangerous(cmd) && !flags.yes && !isInteractive() {
				return usageErrorf(
					"命令 %q 是危险操作，非交互式环境下必须显式传入 --yes", cmd.CommandPath())
			}
			return nil
		},
	}

	pf := root.PersistentFlags()
	pf.StringVar(&flags.address, "address", "", "服务端地址（默认读取 ALADDIN_ADDRESS 或内置默认值）")
	pf.StringVar(&flags.token, "token", "", "访问凭证（优先级最高）")
	pf.StringVar(&flags.scope, "scope", "", "本次调用声明的作用域")
	pf.StringVar(&flags.output, "output", "text", "输出格式：text 或 json")
	pf.BoolVar(&flags.debug, "debug", false, "输出调试信息（不包含凭证原文）")
	pf.BoolVar(&flags.yes, "yes", false, "跳过危险操作的二次确认（非交互式环境必须显式指定）")
	pf.DurationVar(&flags.timeout, "timeout", 0, "单次调用超时")

	root.AddCommand(
		newVersionCommand(),
		newLoginCommand(),
		newWhoAmICommand(),
		newPermissionsCommand(),
		newRoleCommand(),
	)
	return root
}

// resolvedConfig 合并全局参数与配置默认值。
func resolvedConfig() (config.Config, error) {
	cfg, err := config.Load()
	if err != nil {
		return config.Config{}, err
	}
	if flags.address != "" {
		cfg.Address = flags.address
	}
	if flags.timeout > 0 {
		cfg.Timeout = flags.timeout
	}
	if flags.debug {
		cfg.LogLevel = observability.LevelDebug
	}
	return cfg, nil
}

// newClient 构造已注入凭证的 gRPC 客户端。
//
// 凭证缺失在这里就失败并给出"请先登录"的提示，
// 而不是等到服务端返回 Unauthenticated 才让用户去猜。
func newClient() (*client.Client, error) {
	cfg, err := resolvedConfig()
	if err != nil {
		return nil, err
	}
	cred, err := auth.Resolve(flags.token, flags.scope)
	if err != nil {
		return nil, err
	}
	if cred.Expired(time.Now()) {
		return nil, fmt.Errorf("凭证已于 %s 过期，请重新登录", cred.ExpiresAt.Format(time.RFC3339))
	}
	if flags.debug {
		// 只输出来源，绝不输出凭证原文。
		printf(os.Stderr, "debug: 凭证来源=%s 作用域=%q 地址=%s\n", cred.Source, cred.Scope, cfg.Address)
	}
	return client.Dial(client.Options{
		Address: cfg.Address,
		Token:   cred.Token,
		Scope:   cred.Scope,
		Timeout: cfg.Timeout,
	})
}

// confirm 对危险操作做二次确认。
//
// 非交互式环境下**不弹确认也不默认放行**，而是要求显式传入 --yes。
func confirm(prompt string) error {
	// --yes 与非交互式两种情况已在 PersistentPreRunE 中处理，
	// 走到这里说明确实需要向人确认。
	if flags.yes {
		return nil
	}
	printf(os.Stderr, "%s [y/N]: ", prompt)
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		return err
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return nil
	default:
		return usageErrorf("已取消")
	}
}

// isInteractive 报告 stdin 是否连在终端上。
//
// 用 term.IsTerminal 而不是判断 ModeCharDevice：/dev/null 也是字符设备，
// 后者会把"从 /dev/null 读"这种典型的 CI 场景误判为交互式，
// 于是危险操作会挂在那里等一个永远不会到来的确认。
func isInteractive() bool {
	return term.IsTerminal(int(os.Stdin.Fd()))
}

// effectiveScope 返回本次调用应当声明的作用域。
//
// 显式 --scope 优先，否则使用凭证自身绑定的作用域。
// 两者都为空表示确实要以全局作用域发起调用——服务端会据此判定是否放行，
// 这里不做任何本地放行判断。
func effectiveScope() (string, error) {
	if flags.scope != "" {
		return flags.scope, nil
	}
	cred, err := auth.Resolve(flags.token, "")
	if err != nil {
		return "", err
	}
	return cred.Scope, nil
}

// printJSON 按 --output 输出结果。
func printJSON(v any) error {
	if flags.output != "json" {
		return nil
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// describeError 为常见错误补充可操作的提示。
func describeError(err error) string {
	if msg := describeDenial(err); msg != "" {
		return msg
	}
	return err.Error()
}
