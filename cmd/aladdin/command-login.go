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

凭证文件权限为 0600；权限过宽时后续命令会拒绝使用该文件。`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runLogin(cmd, token)
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
