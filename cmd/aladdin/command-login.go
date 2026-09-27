package main

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	identityv1 "github.com/poetlife/aladdin/api/gen/aladdin/identity/v1"
	"github.com/poetlife/aladdin/internal/auth"
	"github.com/poetlife/aladdin/pkg/client"
)

func newLoginCommand() *cobra.Command {
	var token string
	cmd := &cobra.Command{
		Use:   "login",
		Short: "登录并保存凭证",
		Long: `登录并把凭证写入本地凭证文件。

两种登录方式，由是否给出 --token 决定：

  - 给出 --token：用一份机器凭证换取访问凭证（机器身份）。
  - 不给 --token：走**设备码**登录。终端打印一个短码，你在浏览器里已经登录的
    界面上批准，本机随后拿到属于**你本人**的会话凭证。

设备码登录需要你在浏览器里操作，因此非交互式环境下必须显式给出 --token。
两种方式都会覆盖本机原有的那一份凭证。

凭证文件权限为 0600；权限过宽时后续命令会拒绝使用该文件。`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if token != "" {
				return runLogin(cmd, token)
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
	cmd.Flags().StringVar(&token, "token", "", "机器凭证（不写入 shell 历史时优先使用 ALADDIN_TOKEN 环境变量）")
	return cmd
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
	c, err := client.Dial(client.Options{Address: cfg.Address, Timeout: cfg.Timeout})
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Timeout)
	defer cancel()

	resp, err := identityv1.NewIdentityServiceClient(c.Conn()).Login(ctx, &identityv1.LoginRequest{
		Credential: &identityv1.LoginRequest_Token{
			Token: &identityv1.TokenCredential{Token: token},
		},
	})
	if err != nil {
		return err
	}

	cred := auth.Credential{Token: resp.GetAccessToken(), Scope: flags.scope}
	if resp.GetExpiresAt() != "" {
		ts, err := time.Parse(time.RFC3339, resp.GetExpiresAt())
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
			"expires_at":      resp.GetExpiresAt(),
		})
	}
	printf(cmd.OutOrStdout(), "凭证已保存到 %s，过期时间 %s\n", path, resp.GetExpiresAt())
	return nil
}
