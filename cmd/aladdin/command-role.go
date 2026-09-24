package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	rbacv1 "github.com/poetlife/aladdin/api/gen/aladdin/rbac/v1"
	"github.com/poetlife/aladdin/internal/rbac"
)

func newRoleCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "role",
		Short: "角色管理",
	}
	cmd.AddCommand(newRoleListCommand(), newRoleAssignCommand())
	return cmd
}

func newRoleListCommand() *cobra.Command {
	return requirePermission(&cobra.Command{
		Use:   "list",
		Short: "列出当前作用域下可见的角色",
		Args:  cobra.NoArgs,
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
			resp, err := rbacv1.NewRBACServiceClient(c.Conn()).
				ListRoles(ctx, &rbacv1.ListRolesRequest{Scope: scope})
			if err != nil {
				return err
			}
			if flags.output == "json" {
				return printJSON(resp)
			}
			for _, r := range resp.GetRoles() {
				builtin := ""
				if r.GetBuiltin() {
					builtin = " [内置]"
				}
				printf(cmd.OutOrStdout(), "%-16s %s%s\n  权限: %s\n",
					r.GetId(), r.GetDisplayName(), builtin, strings.Join(r.GetPermissions(), ", "))
			}
			return nil
		},
	}, rbac.PermissionRbacRoleRead)
}

func newRoleAssignCommand() *cobra.Command {
	var subjectID, roleID string

	cmd := &cobra.Command{
		Use:   "assign",
		Short: "为主体授予角色",
		Long: `为主体授予角色。

这是危险操作：它会改变权限边界，因此需要二次确认；
非交互式环境下必须显式传入 --yes。`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if subjectID == "" || roleID == "" {
				return usageErrorf("--subject 与 --role 均为必填")
			}
			scope, err := effectiveScope()
			if err != nil {
				return err
			}
			if err := confirm(fmt.Sprintf(
				"即将在作用域 %q 上为主体 %q 授予角色 %q，确认继续？",
				scope, subjectID, roleID)); err != nil {
				return err
			}

			c, err := newClient()
			if err != nil {
				return err
			}
			defer func() { _ = c.Close() }()

			ctx, cancel := c.Context()
			defer cancel()

			resp, err := rbacv1.NewRBACServiceClient(c.Conn()).AssignRole(ctx, &rbacv1.AssignRoleRequest{
				Scope:     scope,
				SubjectId: subjectID,
				RoleId:    roleID,
				Grant:     true,
			})
			if err != nil {
				return err
			}
			if flags.output == "json" {
				return printJSON(map[string]string{"change_id": resp.GetChangeId()})
			}
			printf(cmd.OutOrStdout(),
				"已授予。变更标识 %s\n注意：变更尚未生效，需执行 publish 使缓存失效后才会被新的判定观察到。\n",
				resp.GetChangeId())
			return nil
		},
	}
	cmd.Flags().StringVar(&subjectID, "subject", "", "目标主体标识")
	cmd.Flags().StringVar(&roleID, "role", "", "角色标识")

	requirePermission(cmd, rbac.PermissionRbacSubjectAssign)
	return markDangerous(cmd)
}
