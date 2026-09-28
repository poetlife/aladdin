package main

import (
	"fmt"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	galaxyv1 "github.com/poetlife/aladdin/api/gen/aladdin/galaxy/v1"
	"github.com/poetlife/aladdin/internal/rbac"
)

func newGalaxyVersionCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "version",
		Short: "版本",
		Long: `正文的一次不可变快照。

保存即冻结：此后改草稿、改工程名称、删资产都不改变一个版本读回的内容。
因此"发布过的页面内容变了"这件事不会发生。发布只能发布版本，不能发布草稿。`,
	}
	cmd.AddCommand(
		newGalaxyVersionSaveCommand(),
		newGalaxyVersionListCommand(),
		newGalaxyVersionGetCommand(),
		newGalaxyVersionDeleteCommand(),
	)
	return cmd
}

func newGalaxyVersionSaveCommand() *cobra.Command {
	return requirePermission(&cobra.Command{
		Use:   "save <工程标识>",
		Short: "把当前草稿保存成一个版本",
		Long: `把当前草稿的当前内容保存成一个不可变版本。

连续保存两次相同正文会产生两个版本，而不是"检测到重复就不新增"：两次保存
是两个不同的动作。`,
		Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, svc, done, err := galaxyCall()
			if err != nil {
				return err
			}
			defer done()

			resp, err := svc.SaveVersion(ctx, connect.NewRequest(&galaxyv1.SaveVersionRequest{
				ProjectId: args[0],
			}))
			if err != nil {
				return err
			}
			if flags.output == "json" {
				return printJSON(resp.Msg)
			}
			v := resp.Msg.GetVersion()
			printf(cmd.OutOrStdout(), "已保存版本 #%d（%s）\n", v.GetSeq(), v.GetId())
			return nil
		},
	}, rbac.PermissionGalaxyProjectWrite)
}

func newGalaxyVersionListCommand() *cobra.Command {
	return requirePermission(&cobra.Command{
		Use:   "list <工程标识>",
		Short: "列出版本",
		Long: `列出工程的版本，按序号排序。

列表不带正文——正文由 version get 取。序号只用于展示与排序：删除一个版本
会让后续序号出现空洞，这不是错误。`,
		Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, svc, done, err := galaxyCall()
			if err != nil {
				return err
			}
			defer done()

			resp, err := svc.ListVersions(ctx, connect.NewRequest(&galaxyv1.ListVersionsRequest{
				ProjectId: args[0],
			}))
			if err != nil {
				return err
			}
			if flags.output == "json" {
				return printJSON(resp.Msg)
			}
			versions := resp.Msg.GetVersions()
			if len(versions) == 0 {
				println(cmd.OutOrStdout(), "还没有版本。用 `aladdin galaxy version save` 存一版。")
				return nil
			}
			for _, v := range versions {
				printf(cmd.OutOrStdout(), "#%-4d %s  %s\n", v.GetSeq(), v.GetId(), v.GetSavedAt())
			}
			return nil
		},
	}, rbac.PermissionGalaxyProjectRead)
}

func newGalaxyVersionGetCommand() *cobra.Command {
	return requirePermission(&cobra.Command{
		Use:   "get <工程标识> <版本标识>",
		Short: "读取一个版本（含正文）",
		Long: `读取一个版本，含正文。保存时的内容此后逐字不变。

默认只输出正文本身，可以直接重定向成文件；--output json 时输出完整消息。`,
		Args: exactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, svc, done, err := galaxyCall()
			if err != nil {
				return err
			}
			defer done()

			resp, err := svc.GetVersion(ctx, connect.NewRequest(&galaxyv1.GetVersionRequest{
				ProjectId: args[0],
				VersionId: args[1],
			}))
			if err != nil {
				return err
			}
			if flags.output == "json" {
				return printJSON(resp.Msg)
			}
			printf(cmd.OutOrStdout(), "%s", resp.Msg.GetVersion().GetContent())
			return nil
		},
	}, rbac.PermissionGalaxyProjectRead)
}

func newGalaxyVersionDeleteCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "delete <工程标识> <版本标识>",
		Short: "删除一个版本",
		Long: `删除一个版本。

被当前发布指向的版本不可删——那会让发布地址指向一个不存在的版本。
这是危险操作：交互式下需要二次确认，非交互式环境下必须显式传入 --yes。`,
		Args: exactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			projectID, versionID := args[0], args[1]
			if err := confirm(fmt.Sprintf(
				"即将删除工程 %q 下的版本 %q，且无法恢复。确认继续？",
				projectID, versionID)); err != nil {
				return err
			}

			ctx, svc, done, err := galaxyCall()
			if err != nil {
				return err
			}
			defer done()

			resp, err := svc.DeleteVersion(ctx, connect.NewRequest(&galaxyv1.DeleteVersionRequest{
				ProjectId: projectID,
				VersionId: versionID,
			}))
			if err != nil {
				return err
			}
			if flags.output == "json" {
				return printJSON(resp.Msg)
			}
			printf(cmd.OutOrStdout(), "已删除版本 %s\n", versionID)
			return nil
		},
	}
	requirePermission(cmd, rbac.PermissionGalaxyProjectWrite)
	return markDangerous(cmd)
}

func newGalaxyValidateCommand() *cobra.Command {
	var file string

	cmd := &cobra.Command{
		Use:   "validate <工程标识>",
		Short: "校验一段正文能不能发布",
		Long: `校验一段正文能不能发布。正文从 --file 给出（- 表示标准输入），
不必是已保存的草稿——校验的正是你手上这一份。

它与服务端的发布前置校验是同一份规则，因此"这里通过"与"发布能成功"是同
一个结论。

有问题时逐条列出，并以非零状态退出，好让 "validate && publish" 这类脚本成立；
"这段正文不能发布"是一个结论，不是一次调用失败。`,
		Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			content, err := readContent(file)
			if err != nil {
				return err
			}
			ctx, svc, done, err := galaxyCall()
			if err != nil {
				return err
			}
			defer done()

			resp, err := svc.ValidateContent(ctx, connect.NewRequest(&galaxyv1.ValidateContentRequest{
				ProjectId: args[0],
				Content:   content,
			}))
			if err != nil {
				return err
			}
			if flags.output == "json" {
				if err := printJSON(resp.Msg); err != nil {
					return err
				}
			} else {
				problems := resp.Msg.GetProblems()
				if len(problems) == 0 {
					println(cmd.OutOrStdout(), "无问题：这段正文可以发布。")
					return nil
				}
				for _, problem := range problems {
					printf(cmd.OutOrStdout(), "- %s\n", problem.GetMessage())
				}
			}
			if len(resp.Msg.GetProblems()) > 0 {
				return fmt.Errorf("正文有 %d 处问题，不能发布", len(resp.Msg.GetProblems()))
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&file, "file", "", "正文文件路径（- 表示标准输入）")

	requirePermission(cmd, rbac.PermissionGalaxyProjectRead)
	return cmd
}
