package main

import (
	"errors"
	"fmt"
	"time"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	identityv1 "github.com/poetlife/aladdin/api/gen/aladdin/identity/v1"
	"github.com/poetlife/aladdin/api/gen/aladdin/identity/v1/identityv1connect"
	"github.com/poetlife/aladdin/internal/rbac"
	"github.com/poetlife/aladdin/pkg/client"
)

func newInviteCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "invite",
		Short: "邀请码管理",
		Long: `签发与管理邀请码。

邀请码**只作准入闸门**：它不带角色、不带范围、不绑定收件人。进来之后拿什么，
由注册策略那一份决定（见 aladdin registration set）。合成两处意味着同一个码
既是准入凭证又是权限凭证，泄漏一个码就泄漏了它所带的那份权限。`,
	}
	cmd.AddCommand(newInviteCreateCommand(), newInviteListCommand(), newInviteRevokeCommand())
	return cmd
}

func newInviteCreateCommand() *cobra.Command {
	var label string
	// 次数上限用 int32 而不是 int：pflag 有对应的取值类型，于是"命令行的 int
	// 转成接口的 int32"这一步根本不存在，也就不必为它写一次溢出检查。
	var maxUses int32
	var expiresIn time.Duration

	cmd := &cobra.Command{
		Use:   "create",
		Short: "签发一份邀请码",
		Long: `签发一份邀请码，并把它打印出来。

**明文只出现这一次。** 服务端只保存它的摘要（与口令、会话凭证同级），此后再也
读不回来——要收回一份只能用 invite revoke。请现在把它交给使用的人。

  aladdin invite create --label "给张三"                 一次性，7 天后过期
  aladdin invite create --max-uses 0 --expires-in 720h   不限次，30 天内有效
  aladdin invite create --expires-in 0                   一次性，永不过期

--max-uses 0 表示不限次；--expires-in 0 表示不过期。`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if maxUses < 0 {
				return errors.New("--max-uses 不能为负（0 表示不限次）")
			}
			scope, err := effectiveScope()
			if err != nil {
				return err
			}

			expiresAt := ""
			if expiresIn > 0 {
				expiresAt = time.Now().UTC().Add(expiresIn).Format(time.RFC3339)
			} else if expiresIn < 0 {
				return errors.New("--expires-in 不能为负（0 表示不过期）")
			}

			c, err := newClient(0)
			if err != nil {
				return err
			}
			defer func() { _ = c.Close() }()

			ctx, cancel := c.Context()
			defer cancel()

			resp, err := client.NewService(c, identityv1connect.NewRegistrationServiceClient).
				CreateInvite(ctx, connect.NewRequest(&identityv1.CreateInviteRequest{
					Scope:     scope,
					Label:     label,
					MaxUses:   maxUses,
					ExpiresAt: expiresAt,
				}))
			if err != nil {
				return err
			}
			if flags.output == "json" {
				return printJSON(map[string]any{
					"invite": resp.Msg.GetInvite(),
					// 明文与记录放在一起返回：它是这条命令唯一的产物，而调用方
					// 多半要把它交给别人。
					"code": resp.Msg.GetCode(),
				})
			}
			printf(cmd.OutOrStdout(), "邀请码：%s\n", resp.Msg.GetCode())
			printf(cmd.OutOrStdout(), "说明：%s\n", describeInviteLabel(resp.Msg.GetInvite().GetLabel()))
			printf(cmd.OutOrStdout(), "可用：%s\n", describeInviteBudget(resp.Msg.GetInvite()))
			return nil
		},
	}
	cmd.Flags().StringVar(&label, "label", "", "给管理员自己看的说明（不参与任何判断）")
	cmd.Flags().Int32Var(&maxUses, "max-uses", 1, "可用次数（0 表示不限次）")
	cmd.Flags().DurationVar(&expiresIn, "expires-in", 168*time.Hour, "有效期（如 24h；0 表示不过期）")

	requirePermission(cmd, rbac.PermissionIdentityRegistrationWrite)
	return cmd
}

func newInviteListCommand() *cobra.Command {
	return requirePermission(&cobra.Command{
		Use:   "list",
		Short: "列出已签发的邀请码",
		Long: `列出已签发的邀请码：说明、用量、有效期与状态。

**没有明文**：库里存的是摘要，明文只在签发那一刻返回过一次。要收回一份，用
invite revoke。`,
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
				ListInvites(ctx, connect.NewRequest(&identityv1.ListInvitesRequest{Scope: scope}))
			if err != nil {
				return err
			}
			if flags.output == "json" {
				return printJSON(resp.Msg.GetInvites())
			}
			if len(resp.Msg.GetInvites()) == 0 {
				println(cmd.OutOrStdout(), "（还没有签发过邀请码；用 invite create 签发一份）")
				return nil
			}
			for _, invite := range resp.Msg.GetInvites() {
				printf(cmd.OutOrStdout(), "%-24s %-16s %-12s %s\n",
					invite.GetId(),
					describeInviteLabel(invite.GetLabel()),
					describeInviteBudget(invite),
					describeInviteState(invite),
				)
			}
			return nil
		},
	}, rbac.PermissionIdentityRegistrationRead)
}

func newInviteRevokeCommand() *cobra.Command {
	return requirePermission(&cobra.Command{
		Use:   "revoke <邀请码标识>",
		Short: "撤销一份邀请码",
		Long: `撤销一份邀请码。撤销之后它立即不可兑换。

**只撤销，不删除。** 用量与签发人留着，方便日后回答"这个码被谁用过、用过几次"
——删掉会连留痕一起丢掉。

已经用它注册进来的账号**不受影响**：它们拿到的角色绑定是真实的记录，与这份码
再无关系。

不需要二次确认：撤销不产生新的暴露，随时可以再签发一份。`,
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

			resp, err := client.NewService(c, identityv1connect.NewRegistrationServiceClient).
				RevokeInvite(ctx, connect.NewRequest(&identityv1.RevokeInviteRequest{
					Scope: scope,
					Id:    args[0],
				}))
			if err != nil {
				return err
			}
			if flags.output == "json" {
				return printJSON(resp.Msg.GetInvite())
			}
			printf(cmd.OutOrStdout(), "已撤销邀请码 %s\n", resp.Msg.GetInvite().GetId())
			return nil
		},
	}, rbac.PermissionIdentityRegistrationWrite)
}

// describeInviteLabel 给出说明的展示写法。留空时说明"没有"。
func describeInviteLabel(label string) string {
	if label == "" {
		return "—"
	}
	return label
}

// describeInviteBudget 给出用量的展示写法：已用几次 / 上限几次。
func describeInviteBudget(invite *identityv1.Invite) string {
	if invite.GetMaxUses() == 0 {
		return fmt.Sprintf("%d/不限", invite.GetUsedCount())
	}
	return fmt.Sprintf("%d/%d", invite.GetUsedCount(), invite.GetMaxUses())
}

// describeInviteState 给出一份邀请码当前的状态。
//
// 它是**展示**，不是判定：真正决定能不能兑换的是服务端那次原子扣减。这里的结论
// 只用来让管理员一眼看出该做什么——补发一份、还是不用管。
func describeInviteState(invite *identityv1.Invite) string {
	switch {
	case invite.GetRevokedAt() != "":
		return "已撤销"
	case invite.GetExpiresAt() != "" && !time.Now().Before(mustParseTime(invite.GetExpiresAt())):
		return "已过期"
	case invite.GetMaxUses() > 0 && invite.GetUsedCount() >= invite.GetMaxUses():
		return "已用尽"
	default:
		if invite.GetExpiresAt() != "" {
			return "可用，至 " + invite.GetExpiresAt()
		}
		return "可用"
	}
}

// mustParseTime 解析服务端给的 ISO 时间。解析不出来时返回零值——它只会让这一行
// 显示成"已过期"，而不会影响任何判定。
func mustParseTime(raw string) time.Time {
	at, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}
	}
	return at
}
