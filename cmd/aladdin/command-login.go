package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	identityv1 "github.com/poetlife/aladdin/api/gen/aladdin/identity/v1"
	"github.com/poetlife/aladdin/api/gen/aladdin/identity/v1/identityv1connect"
	"github.com/poetlife/aladdin/internal/auth"
	"github.com/poetlife/aladdin/pkg/client"
)

func newLoginCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "login",
		Short: "登录并保存凭证",
		Long: `登录并把凭证写入本地凭证文件。

两种登录方式，由是否给出机器凭证（--token 或 ALADDIN_TOKEN）决定：

  - 给出机器凭证：用它换取访问凭证（机器身份）。
  - 不给：走**设备码**登录。终端打印一个短码，你在浏览器里已经登录的
    界面上批准，本机随后拿到属于**你本人**的会话凭证。

设备码登录需要你在浏览器里操作，因此非交互式环境下必须显式给出机器凭证。
它也不接受 --scope：交付的会话带的是批准者主体的默认作用域，由服务端在这次
登录里确定。两种方式都会覆盖本机原有的那一份凭证。

凭证文件权限为 0600；权限过宽时后续命令会拒绝使用该文件。`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if token := machineToken(); token != "" {
				return runLogin(cmd, token)
			}
			// 设备码登录的作用域只能来自批准者主体，不接受请求方指定。
			// 静默忽略一个"看起来能收窄权限"的参数，比报错更危险：使用者会
			// 以为自己按最小权限登录了，实际拿到的是批准者的完整作用域。
			if flags.scope != "" {
				return usageErrorf(
					"设备码登录不接受 --scope：交付的会话带的是批准者主体的默认作用域，由服务端确定")
			}
			// 非交互式环境下不让它挂在那里等一次永远不会到来的批准：
			// 明确要求用机器凭证，而不是让脚本卡到超时。
			if !isInteractive() {
				return usageErrorf(
					"缺少凭证：非交互式环境下请通过 --token 或 %s 提供机器凭证", auth.EnvToken)
			}
			return runDeviceLogin(cmd)
		},
	}
	return cmd
}

// machineToken 取本次调用可用的机器凭证：显式参数优先，其次是环境变量。
//
// 登录要据此**选择走哪条路**（有机器凭证就去换取，否则走设备码），因此这里
// 不能直接用 auth.Resolve：后者在两者都缺时会回过头去读凭证文件，而登录恰恰
// 发生在还没有凭证文件的时候。
//
// 参数读的是根命令的持久 flag，登录不再自己声明一份：重名的两份会让
// `aladdin --token X login` 落进"缺少凭证"，而它本该和 `aladdin login --token X`
// 完全一样。
func machineToken() string {
	if flags.token != "" {
		return flags.token
	}
	return os.Getenv(auth.EnvToken)
}

func runLogin(cmd *cobra.Command, token string) error {
	if token == "" {
		return fmt.Errorf("缺少凭证：请通过 --token 或 %s 提供", auth.EnvToken)
	}

	cfg, err := resolvedConfig()
	if err != nil {
		return err
	}
	if err := ensureTelemetry(cfg); err != nil {
		return err
	}
	debugTarget(cfg)
	c, err := client.Dial(client.Options{Address: cfg.Address, TLS: cfg.TLSConfig(), Timeout: cfg.Timeout})
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Timeout)
	defer cancel()

	resp, err := client.NewService(c, identityv1connect.NewIdentityServiceClient).Login(ctx,
		connect.NewRequest(&identityv1.LoginRequest{
			Credential: &identityv1.LoginRequest_Token{
				Token: &identityv1.TokenCredential{Token: token},
			},
		}))
	if err != nil {
		return err
	}

	cred := auth.Credential{Token: resp.Msg.GetAccessToken(), Scope: flags.scope}
	if resp.Msg.GetExpiresAt() != "" {
		ts, err := time.Parse(time.RFC3339, resp.Msg.GetExpiresAt())
		if err != nil {
			return fmt.Errorf("服务端返回的 expires_at 不是 RFC3339: %w", err)
		}
		cred.ExpiresAt = ts
	}

	path, err := auth.Save(cred)
	if err != nil {
		return err
	}
	if flags.output == "json" {
		return printJSON(map[string]string{
			"credential_file": path,
			"expires_at":      resp.Msg.GetExpiresAt(),
		})
	}
	printf(cmd.OutOrStdout(), "凭证已保存到 %s，过期时间 %s\n", path, resp.Msg.GetExpiresAt())
	return nil
}
