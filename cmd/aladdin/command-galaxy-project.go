package main

import (
	"fmt"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	galaxyv1 "github.com/poetlife/aladdin/api/gen/aladdin/galaxy/v1"
	"github.com/poetlife/aladdin/internal/rbac"
)

func newGalaxyProjectCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "project",
		Short: "工程管理",
	}
	cmd.AddCommand(
		newGalaxyProjectListCommand(),
		newGalaxyProjectCreateCommand(),
		newGalaxyProjectGetCommand(),
		newGalaxyProjectUpdateCommand(),
		newGalaxyProjectDeleteCommand(),
	)
	return cmd
}

func newGalaxyProjectListCommand() *cobra.Command {
	return requirePermission(&cobra.Command{
		Use:   "list",
		Short: "列出自己的工程",
		Long: `列出调用者自己的工程。

范围由凭证决定，没有"列出所有工程"的形态——工程标识是发布地址的一部分，
任何枚举入口都会把"地址即凭据"降级成"打开就能逛"。`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, svc, done, err := galaxyCall()
			if err != nil {
				return err
			}
			defer done()

			resp, err := svc.ListProjects(ctx, connect.NewRequest(&galaxyv1.ListProjectsRequest{}))
			if err != nil {
				return err
			}
			if flags.output == "json" {
				return printJSON(resp.Msg)
			}
			projects := resp.Msg.GetProjects()
			if len(projects) == 0 {
				println(cmd.OutOrStdout(), "还没有工程。用 `aladdin galaxy project create --name <名称>` 建一个。")
				return nil
			}
			for _, p := range projects {
				printProject(cmd, p)
			}
			return nil
		},
	}, rbac.PermissionGalaxyProjectRead)
}

func newGalaxyProjectCreateCommand() *cobra.Command {
	var name, description string

	cmd := &cobra.Command{
		Use:   "create",
		Short: "创建一个工程",
		Long: `创建一个工程，标识由服务端分配，不可猜、不可改、不复用。

名称不参与任何查找，只是给你自己认的标签：不按名称查工程、不加唯一约束、
不进发布地址。因此它可以留空（与网页端一致），事后再用 project update 补上。`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, svc, done, err := galaxyCall()
			if err != nil {
				return err
			}
			defer done()

			resp, err := svc.CreateProject(ctx, connect.NewRequest(&galaxyv1.CreateProjectRequest{
				Name:        name,
				Description: description,
			}))
			if err != nil {
				return err
			}
			if flags.output == "json" {
				return printJSON(resp.Msg)
			}
			printProject(cmd, resp.Msg.GetProject())
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "工程名称（可留空；只用于你自己识别）")
	cmd.Flags().StringVar(&description, "description", "", "工程简介")

	requirePermission(cmd, rbac.PermissionGalaxyProjectWrite)
	return cmd
}

func newGalaxyProjectGetCommand() *cobra.Command {
	return requirePermission(&cobra.Command{
		Use:   "get <工程标识>",
		Short: "读取一个工程的元数据",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, svc, done, err := galaxyCall()
			if err != nil {
				return err
			}
			defer done()

			resp, err := svc.GetProject(ctx, connect.NewRequest(&galaxyv1.GetProjectRequest{
				ProjectId: args[0],
			}))
			if err != nil {
				return err
			}
			if flags.output == "json" {
				return printJSON(resp.Msg)
			}
			printProject(cmd, resp.Msg.GetProject())
			return nil
		},
	}, rbac.PermissionGalaxyProjectRead)
}

func newGalaxyProjectUpdateCommand() *cobra.Command {
	var name, description string

	cmd := &cobra.Command{
		Use:   "update <工程标识>",
		Short: "修改工程的名称与简介",
		Long: `修改工程的名称与简介。

只改你显式给出的那一项：没给的标志保持原值，给出空串才是清空。因此
"--name 新名字" 不会顺手把简介抹掉。

至少要给出 --name 与 --description 之一。`,
		Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			nameGiven := cmd.Flags().Changed("name")
			descriptionGiven := cmd.Flags().Changed("description")
			if !nameGiven && !descriptionGiven {
				return usageErrorf(
					"至少要给出 --name 或 --description 之一；两项都不给表示这次调用没有任何改动")
			}

			ctx, svc, done, err := galaxyCall()
			if err != nil {
				return err
			}
			defer done()

			projectID := args[0]
			// 请求表达的是**期望的完整状态**，因此先把当前值取回来，再用显式
			// 给出的那一项覆盖它——否则"只改名称"会顺带清空简介。
			current, err := svc.GetProject(ctx, connect.NewRequest(&galaxyv1.GetProjectRequest{
				ProjectId: projectID,
			}))
			if err != nil {
				return err
			}
			next := current.Msg.GetProject().GetName()
			if nameGiven {
				next = name
			}
			nextDescription := current.Msg.GetProject().GetDescription()
			if descriptionGiven {
				nextDescription = description
			}

			resp, err := svc.UpdateProject(ctx, connect.NewRequest(&galaxyv1.UpdateProjectRequest{
				ProjectId:   projectID,
				Name:        next,
				Description: nextDescription,
			}))
			if err != nil {
				return err
			}
			if flags.output == "json" {
				return printJSON(resp.Msg)
			}
			printProject(cmd, resp.Msg.GetProject())
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "新的工程名称（给出空串表示清空）")
	cmd.Flags().StringVar(&description, "description", "", "新的工程简介（给出空串表示清空）")

	requirePermission(cmd, rbac.PermissionGalaxyProjectWrite)
	return cmd
}

func newGalaxyProjectDeleteCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "delete <工程标识>",
		Short: "删除一个工程",
		Long: `删除一个工程，连带删除它的全部版本、资产与发布记录。

已发布的地址会立刻变成"不存在"。这是危险操作：交互式下需要二次确认，
非交互式环境下必须显式传入 --yes。`,
		Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			projectID := args[0]
			if err := confirm(fmt.Sprintf(
				"即将删除工程 %q，连同它的全部版本、资产与发布记录；已发布的地址会立刻失效。确认继续？",
				projectID)); err != nil {
				return err
			}

			ctx, svc, done, err := galaxyCall()
			if err != nil {
				return err
			}
			defer done()

			resp, err := svc.DeleteProject(ctx, connect.NewRequest(&galaxyv1.DeleteProjectRequest{
				ProjectId: projectID,
			}))
			if err != nil {
				return err
			}
			if flags.output == "json" {
				return printJSON(resp.Msg)
			}
			printf(cmd.OutOrStdout(), "已删除工程 %s\n", projectID)
			return nil
		},
	}
	requirePermission(cmd, rbac.PermissionGalaxyProjectWrite)
	return markDangerous(cmd)
}

func newGalaxyDraftCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "draft",
		Short: "当前草稿",
		Long: `工程的当前草稿：随时可改，改它不产生版本，也不参与发布。

草稿是工作区，不是历史。要留下一份不会被后续改动影响的正文，用
"aladdin galaxy version save" 存一版。`,
	}
	cmd.AddCommand(newGalaxyDraftGetCommand(), newGalaxyDraftSaveCommand())
	return cmd
}

func newGalaxyDraftGetCommand() *cobra.Command {
	return requirePermission(&cobra.Command{
		Use:   "get <工程标识>",
		Short: "读取当前草稿",
		Long: `读取当前草稿。

默认只输出正文本身，可以直接重定向成文件；--output json 时输出完整消息。`,
		Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, svc, done, err := galaxyCall()
			if err != nil {
				return err
			}
			defer done()

			resp, err := svc.GetDraft(ctx, connect.NewRequest(&galaxyv1.GetDraftRequest{
				ProjectId: args[0],
			}))
			if err != nil {
				return err
			}
			if flags.output == "json" {
				return printJSON(resp.Msg)
			}
			printf(cmd.OutOrStdout(), "%s", resp.Msg.GetDraft().GetContent())
			return nil
		},
	}, rbac.PermissionGalaxyProjectRead)
}

func newGalaxyDraftSaveCommand() *cobra.Command {
	var file string

	cmd := &cobra.Command{
		Use:   "save <工程标识>",
		Short: "保存当前草稿",
		Long: `保存当前草稿。改草稿不产生版本——它是工作区，不是历史。

正文从 --file 给出（- 表示标准输入），必须是一个完整的 HTML 文档。`,
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

			resp, err := svc.SaveDraft(ctx, connect.NewRequest(&galaxyv1.SaveDraftRequest{
				ProjectId: args[0],
				Content:   content,
			}))
			if err != nil {
				return err
			}
			if flags.output == "json" {
				return printJSON(resp.Msg)
			}
			printf(cmd.OutOrStdout(), "已保存草稿（%s），%d 字节\n",
				resp.Msg.GetDraft().GetUpdatedAt(), len(content))
			return nil
		},
	}
	cmd.Flags().StringVar(&file, "file", "", "正文文件路径（- 表示标准输入）")

	requirePermission(cmd, rbac.PermissionGalaxyProjectWrite)
	return cmd
}
