package main

import (
	"fmt"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	galaxyv1 "github.com/poetlife/aladdin/api/gen/aladdin/galaxy/v1"
	"github.com/poetlife/aladdin/internal/rbac"
)

func newGalaxyPublishCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "publish <工程标识> <版本标识>",
		Short: "发布一个版本",
		Long: `发布一个版本：校验、把引用的资产上架到公开区、落库产物、切换发布指针。

只能发布版本，不能发布草稿——草稿是可变的，发布一个可变的东西没有意义。

发布出去的地址谁拿到都能打开，不需要登录。这是危险操作：交互式下需要二次
确认，非交互式环境下必须显式传入 --yes。

资产较多的工程发布可能要超过默认的 30 秒超时，此时用 --timeout 放宽。`,
		Args: exactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			projectID, versionID := args[0], args[1]
			if err := confirm(fmt.Sprintf(
				"即将发布工程 %q 的版本 %q；发布后该地址对任何拿到它的人可打开。确认继续？",
				projectID, versionID)); err != nil {
				return err
			}

			ctx, svc, done, err := galaxyCall()
			if err != nil {
				return err
			}
			defer done()

			resp, err := svc.Publish(ctx, connect.NewRequest(&galaxyv1.PublishRequest{
				ProjectId: projectID,
				VersionId: versionID,
			}))
			if err != nil {
				return err
			}
			if flags.output == "json" {
				return printJSON(resp.Msg)
			}
			printf(cmd.OutOrStdout(), "已发布: %s\n", resp.Msg.GetProject().GetPublishedUrl())
			return nil
		},
	}
	requirePermission(cmd, rbac.PermissionGalaxyProjectPublish)
	return markDangerous(cmd)
}

func newGalaxyUnpublishCommand() *cobra.Command {
	return requirePermission(&cobra.Command{
		Use:   "unpublish <工程标识>",
		Short: "撤回发布",
		Long: `撤回发布：把发布指针置空，地址立刻不可达。

发布记录保留，因此可以重新发布同一个版本；公开区的副本不因撤回而删除。

撤回不是危险操作——它是发布的反向操作，一步就能让地址不可达，且不产生新的
暴露。把收回也加上摩擦，只会让人在发错之后不敢撤。`,
		Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, svc, done, err := galaxyCall()
			if err != nil {
				return err
			}
			defer done()

			resp, err := svc.Unpublish(ctx, connect.NewRequest(&galaxyv1.UnpublishRequest{
				ProjectId: args[0],
			}))
			if err != nil {
				return err
			}
			if flags.output == "json" {
				return printJSON(resp.Msg)
			}
			printf(cmd.OutOrStdout(), "已撤回发布，工程 %s 的发布地址立刻不可达\n",
				resp.Msg.GetProject().GetId())
			return nil
		},
	}, rbac.PermissionGalaxyProjectPublish)
}
