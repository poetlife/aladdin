package main

import (
	"fmt"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	rbacv1 "github.com/poetlife/aladdin/api/gen/aladdin/rbac/v1"
	"github.com/poetlife/aladdin/api/gen/aladdin/rbac/v1/rbacv1connect"
	"github.com/poetlife/aladdin/internal/rbac"
	"github.com/poetlife/aladdin/pkg/client"
)

func newScopeCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "scope",
		Short: "范围目录管理",
	}
	cmd.AddCommand(newScopeListCommand(), newScopeCreateCommand(), newScopeDeleteCommand())
	return cmd
}

func newScopeListCommand() *cobra.Command {
	return requirePermission(&cobra.Command{
		Use:   "list",
		Short: "列出已登记的范围",
		Long: `列出已登记的范围。

**不含全局**：全局是模型的根，不是目录里的一条（配置与日志里写作 <global>，
界面上显示为「全局」）。`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := newClient(0)
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
			resp, err := client.NewService(c, rbacv1connect.NewRBACServiceClient).
				ListScopes(ctx, connect.NewRequest(&rbacv1.ListScopesRequest{Scope: scope}))
			if err != nil {
				return err
			}
			if flags.output == "json" {
				return printJSON(resp.Msg)
			}
			if len(resp.Msg.GetScopes()) == 0 {
				println(cmd.OutOrStdout(), "（还没有登记任何范围；用 scope create 登记一个）")
				return nil
			}
			for _, s := range resp.Msg.GetScopes() {
				if s.GetDisplayName() == "" {
					printf(cmd.OutOrStdout(), "%s\n", s.GetPath())
					continue
				}
				printf(cmd.OutOrStdout(), "%-24s %s\n", s.GetPath(), s.GetDisplayName())
			}
			return nil
		},
	}, rbac.PermissionRbacScopeRead)
}

func newScopeCreateCommand() *cobra.Command {
	var displayName string

	cmd := &cobra.Command{
		Use:   "create <path>",
		Short: "登记一个范围（或改它的显示名）",
		Long: `登记一个范围，或改一个已登记范围的显示名。

路径是标识，**一经登记不可更改**。要"改路径"请新建一个、把绑定迁过去、再删掉旧的。

角色绑定只能指向已登记的范围，因此给人授权之前必须先有这一条；指向未登记的范围
会被拒绝。重复登记同一个路径不是错误，那是一次改显示名。`,
		Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			scope, err := effectiveScope()
			if err != nil {
				return err
			}

			c, err := newClient(0)
			if err != nil {
				return err
			}
			defer func() { _ = c.Close() }()

			ctx, cancel := c.Context()
			defer cancel()

			resp, err := client.NewService(c, rbacv1connect.NewRBACServiceClient).
				PutScope(ctx, connect.NewRequest(&rbacv1.PutScopeRequest{
					Scope:       scope,
					Path:        args[0],
					DisplayName: displayName,
				}))
			if err != nil {
				return err
			}
			if flags.output == "json" {
				return printJSON(resp.Msg)
			}
			printf(cmd.OutOrStdout(), "已登记范围 %s\n", resp.Msg.GetScope().GetPath())
			return nil
		},
	}
	cmd.Flags().StringVar(&displayName, "display-name", "", "展示名（留空则界面显示路径本身）")

	requirePermission(cmd, rbac.PermissionRbacScopeWrite)
	return cmd
}

func newScopeDeleteCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "delete <path>",
		Short: "删除一个范围",
		Long: `删除一个已登记的范围。不可逆。

范围内（含其后代）仍有角色绑定时拒绝——引用还在，就不允许把被引用的东西抽走。
先回收那些绑定，再删这个范围。`,
		Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			scope, err := effectiveScope()
			if err != nil {
				return err
			}
			if err := confirm(fmt.Sprintf("即将删除范围 %q，该范围及其后代上的绑定必须先清空。确认继续？", args[0])); err != nil {
				return err
			}

			c, err := newClient(0)
			if err != nil {
				return err
			}
			defer func() { _ = c.Close() }()

			ctx, cancel := c.Context()
			defer cancel()

			_, err = client.NewService(c, rbacv1connect.NewRBACServiceClient).
				DeleteScope(ctx, connect.NewRequest(&rbacv1.DeleteScopeRequest{
					Scope: scope,
					Path:  args[0],
				}))
			if err != nil {
				return err
			}
			if flags.output == "json" {
				return printJSON(map[string]string{"deleted": args[0]})
			}
			printf(cmd.OutOrStdout(), "已删除范围 %s\n", args[0])
			return nil
		},
	}

	requirePermission(cmd, rbac.PermissionRbacScopeWrite)
	return markDangerous(cmd)
}
