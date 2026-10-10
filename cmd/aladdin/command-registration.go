package main

import (
	"context"
	"errors"
	"fmt"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	identityv1 "github.com/poetlife/aladdin/api/gen/aladdin/identity/v1"
	"github.com/poetlife/aladdin/api/gen/aladdin/identity/v1/identityv1connect"
	"github.com/poetlife/aladdin/internal/rbac"
	"github.com/poetlife/aladdin/pkg/client"
)

// registrationModeNames 是三个姿态在命令行上的写法。
//
// 它就是对外的取值本身（open / invite / closed），不另造一套简写：多一层映射
// 只会多一处"命令里写 a、界面上显示 b"的漂移。
const (
	registrationModeOpen   = "open"
	registrationModeInvite = "invite"
	registrationModeClosed = "closed"
)

func newRegistrationCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "registration",
		Short: "注册策略管理",
		Long: `管理站点的准入姿态：未登记的渠道身份能不能成为新账号。

已经在册的账号**不受它影响**，照常登录。`,
	}
	cmd.AddCommand(newRegistrationGetCommand(), newRegistrationSetCommand())
	return cmd
}

func newRegistrationGetCommand() *cobra.Command {
	return requirePermission(&cobra.Command{
		Use:   "get",
		Short: "读取当前注册策略",
		Long: `读取当前注册策略：准入姿态、新账号的默认角色与它的范围。

**没有记录时等价于 open 且不给默认角色**，也就是"开放注册、新账号零权限"——这
是这张表出现之前的行为，因此升级不改任何东西。`,
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
			resp, err := client.NewService(c, identityv1connect.NewRegistrationServiceClient).
				GetRegistrationPolicy(ctx, connect.NewRequest(&identityv1.GetRegistrationPolicyRequest{Scope: scope}))
			if err != nil {
				return err
			}
			if flags.output == "json" {
				return printJSON(resp.Msg.GetPolicy())
			}
			policy := resp.Msg.GetPolicy()
			printf(cmd.OutOrStdout(), "准入姿态：%s\n", describeRegistrationMode(policy.GetMode()))
			if policy.GetDefaultRoleId() == "" {
				printf(cmd.OutOrStdout(), "默认角色：无（新账号零权限）\n")
			} else {
				printf(cmd.OutOrStdout(), "默认角色：%s（范围 %s）\n",
					policy.GetDefaultRoleId(), describeScope(policy.GetDefaultScope()))
			}
			if policy.GetUpdatedBySubjectId() != "" {
				printf(cmd.OutOrStdout(), "上次改动：%s @ %s\n",
					policy.GetUpdatedBySubjectId(), policy.GetUpdatedAt())
			}
			return nil
		},
	}, rbac.PermissionIdentityRegistrationRead)
}

func newRegistrationSetCommand() *cobra.Command {
	var defaultRoleID string
	var defaultScope string

	cmd := &cobra.Command{
		Use:   "set <open|invite|closed>",
		Short: "设置准入姿态",
		Long: `设置未登记的渠道身份来登录时的待遇。

  open    直接登记成新账号（开放注册）
  invite  先要一份有效的邀请码
  closed  拒绝，不接受新账号

**已经在册的账号不受影响**：三种姿态都只拦新主体。

--default-role 与 --default-scope 给出新账号的默认角色与它的范围，**同进同退**。
它们遵循与配置文件同一条约定——**键出现即生效**：

  --default-role viewer        设默认角色；范围取 --default-scope，没给就是全局
  --default-scope tenant/acme  只改范围，角色沿用当前那一份
  --default-role ""            清掉默认角色（新账号零权限）
  两个都不给                    当前那一份原样保留

因此"只改姿态"不会顺手把默认角色抹掉。默认角色不能是一个能授予角色或改写注册
策略的角色（含经继承得到的）——否则开放注册或发一个邀请码就等于一次提权。

改这一项**不追溯**已经注册的账号：它写下的是一条真实的角色绑定，那条绑定不会
因为策略变了而消失或改变。`,
		Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			mode, err := parseRegistrationMode(args[0])
			if err != nil {
				return err
			}
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

			svc := client.NewService(c, identityv1connect.NewRegistrationServiceClient)

			roleID, roleScope, err := resolveDefaultRole(ctx, svc, scope, defaultRoleID, defaultScope,
				cmd.Flags().Changed("default-role") || cmd.Flags().Changed("default-scope"))
			if err != nil {
				return err
			}

			// 它改变的是"今后新来的人拿到什么"，与授予角色同属一类，因此与
			// `role assign` 一样要二次确认（见 docs/design/rbac/cli-permissions.md）。
			if err := confirm(fmt.Sprintf(
				"即将把准入姿态设为 %s，默认角色 %q（范围 %s）。这决定今后新注册的账号拿到什么，确认继续？",
				args[0], roleID, describeScope(roleScope))); err != nil {
				return err
			}

			resp, err := svc.PutRegistrationPolicy(ctx, connect.NewRequest(&identityv1.PutRegistrationPolicyRequest{
				Scope:         scope,
				Mode:          mode,
				DefaultRoleId: roleID,
				DefaultScope:  roleScope,
			}))
			if err != nil {
				return err
			}
			if flags.output == "json" {
				return printJSON(resp.Msg.GetPolicy())
			}
			printf(cmd.OutOrStdout(), "已设置准入姿态：%s\n",
				describeRegistrationMode(resp.Msg.GetPolicy().GetMode()))
			printf(cmd.OutOrStdout(), "新账号默认拿到：%s\n", describeDefaultRole(resp.Msg.GetPolicy()))
			return nil
		},
	}
	cmd.Flags().StringVar(&defaultRoleID, "default-role", "", "新账号的默认角色（显式给空表示清掉；不给则沿用当前）")
	cmd.Flags().StringVar(&defaultScope, "default-scope", "", "默认角色的范围（空表示全局；它同时成为新账号的默认作用域）")

	requirePermission(cmd, rbac.PermissionIdentityRegistrationWrite)
	return markDangerous(cmd)
}

// resolveDefaultRole 定出这次要写下去的默认角色与范围。
//
// 两个标志都没给时，**沿用当前那一份**：否则"只改姿态"会顺手把默认角色抹掉，而
// 那是一件使用者没提、也没打算提的事。这条约定与配置文件同源——**键出现即生效**，
// 不写表示沿用上一层（见 docs/design/config/README.md）。
//
// 显式给 `--default-role ""` 是"清掉它"：键出现了，取的就是它给的空值。
func resolveDefaultRole(
	ctx context.Context,
	svc identityv1connect.RegistrationServiceClient,
	scope, givenRole, givenScope string,
	changed bool,
) (string, string, error) {
	if changed {
		return givenRole, givenScope, nil
	}
	resp, err := svc.GetRegistrationPolicy(ctx, connect.NewRequest(&identityv1.GetRegistrationPolicyRequest{Scope: scope}))
	if err != nil {
		return "", "", err
	}
	return resp.Msg.GetPolicy().GetDefaultRoleId(), resp.Msg.GetPolicy().GetDefaultScope(), nil
}

// describeDefaultRole 给出"新账号默认拿到什么"的展示写法。
func describeDefaultRole(policy *identityv1.RegistrationPolicy) string {
	if policy.GetDefaultRoleId() == "" {
		return "没有任何角色（零权限）"
	}
	return fmt.Sprintf("%s（范围 %s）", policy.GetDefaultRoleId(), describeScope(policy.GetDefaultScope()))
}

// parseRegistrationMode 把命令行的写法折成接口取值。
//
// 只认三个词，**不接受空**：准入姿态没有"没填"这一档，落成开放注册会让一次
// 漏写把站点悄悄打开。
func parseRegistrationMode(raw string) (identityv1.RegistrationMode, error) {
	switch raw {
	case registrationModeOpen:
		return identityv1.RegistrationMode_REGISTRATION_MODE_OPEN, nil
	case registrationModeInvite:
		return identityv1.RegistrationMode_REGISTRATION_MODE_INVITE, nil
	case registrationModeClosed:
		return identityv1.RegistrationMode_REGISTRATION_MODE_CLOSED, nil
	default:
		return identityv1.RegistrationMode_REGISTRATION_MODE_UNSPECIFIED,
			errors.New("准入姿态只能是 open、invite 或 closed")
	}
}

// describeRegistrationMode 把接口取值折成人话。
func describeRegistrationMode(mode identityv1.RegistrationMode) string {
	switch mode {
	case identityv1.RegistrationMode_REGISTRATION_MODE_INVITE:
		return "需要邀请码（invite）"
	case identityv1.RegistrationMode_REGISTRATION_MODE_CLOSED:
		return "不接受新账号（closed）"
	case identityv1.RegistrationMode_REGISTRATION_MODE_OPEN:
		return "开放注册（open）"
	default:
		// 读不出来时**不猜一个姿态**：把一次读取失败说成"这里开放注册"，正是
		// 这条命令最不该做的事。
		return "未知（服务端没有答上来）"
	}
}

// describeScope 把空范围显示成「全局」。
//
// 空值与「全局」是同一个意思的两种写法，界面上只出现后者（见
// docs/design/rbac/scopes.md）。
func describeScope(scope string) string {
	if scope == "" {
		return "全局"
	}
	return fmt.Sprintf("%q", scope)
}
