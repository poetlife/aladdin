package main

import (
	"strings"

	"github.com/spf13/cobra"

	identityv1 "github.com/poetlife/aladdin/api/gen/aladdin/identity/v1"
)

func newPermissionsCommand() *cobra.Command {
	return markAuthenticatedOnly(&cobra.Command{
		Use:   "permissions",
		Short: "打印当前作用域下生效的权限码",
		Long: `向服务端查询当前主体在指定作用域下展开后的最终权限码集合。

服务端返回的已是展开结果，本工具不做任何本地推导。`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := newClient()
			if err != nil {
				return err
			}
			defer func() { _ = c.Close() }()

			ctx, cancel := c.Context()
			defer cancel()

			scope, err := effectiveScope()
			if err != nil {
				return err
			}
			resp, err := identityv1.NewIdentityServiceClient(c.Conn()).
				GetSessionPermissions(ctx, &identityv1.GetSessionPermissionsRequest{Scope: scope})
			if err != nil {
				return err
			}
			if flags.output == "json" {
				return printJSON(map[string]any{
					"scope":       resp.GetScope(),
					"permissions": resp.GetPermissions(),
				})
			}
			printf(cmd.OutOrStdout(), "作用域: %s\n", resp.GetScope())
			if len(resp.GetPermissions()) == 0 {
				println(cmd.OutOrStdout(), "（无生效权限）")
				return nil
			}
			println(cmd.OutOrStdout(), strings.Join(resp.GetPermissions(), "\n"))
			return nil
		},
	})
}
