package main

import (
	"fmt"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	galaxyv1 "github.com/poetlife/aladdin/api/gen/aladdin/galaxy/v1"
	"github.com/poetlife/aladdin/internal/galaxy"
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
		newGalaxyProjectBaseCommand(),
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
				println(cmd.OutOrStdout(), "还没有工程。用 `aladdin galaxy project create --form static` 建一个。")
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
	var name, description, form string

	cmd := &cobra.Command{
		Use:   "create",
		Short: "创建一个工程",
		Long: `创建一个工程，标识由服务端分配，不可猜、不可改、不复用。

**形态在创建时定下，此后不可改**，因此必须显式给出：

  static  整站文件原样服务（手写单页、构建产物），入口是 index.html
  docs    一组 markdown 渲染成多页，入口是 index.md

名称不参与任何查找，只是给你自己认的标签：不按名称查工程、不加唯一约束、
不进发布地址。因此它可以留空，事后再用 project update 补上。`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			parsed, err := parseSiteFormFlag(form)
			if err != nil {
				return err
			}
			ctx, svc, done, err := galaxyCall()
			if err != nil {
				return err
			}
			defer done()

			resp, err := svc.CreateProject(ctx, connect.NewRequest(&galaxyv1.CreateProjectRequest{
				Name:        name,
				Description: description,
				Form:        parsed,
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
	cmd.Flags().StringVar(&form, "form", "", "形态：static 或 docs（必填，此后不可改）")

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

// newGalaxyProjectBaseCommand 输出构建要用的**发布根**。
//
// **命令行不自己拼这个地址**：它与发布态的地址、记号解析出的地址、内容安全策略
// 里的允许来源同源（见 docs/design/galaxy/publication.md），多一处拼接就是多一处
// 会漂的来源。
func newGalaxyProjectBaseCommand() *cobra.Command {
	return requirePermission(&cobra.Command{
		Use:   "base <工程标识>",
		Short: "输出该工程的发布根，供构建命令使用",
		Long: `输出该工程的发布根，形如 /g/<工程标识>/。

把它交给构建命令（vite 用 --base），产物里的绝对路径才会落在站点根上：

  vite build --base "$(aladdin galaxy project base <工程标识>)"

输出的是**路径**而不是完整地址：产物里写完整地址会让站点绑死在当前这个发布域上，
换一个域就要重新构建。`,
		Args: exactArgs(1),
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
			base := resp.Msg.GetProject().GetBaseUrl()
			if base == "" {
				return fmt.Errorf("这个部署没有配置发布域，算不出发布根")
			}
			// 只输出地址本身，便于 `$(...)` 直接接给构建命令。
			printf(cmd.OutOrStdout(), "%s\n", base)
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

**形态不在其中**：它创建时定下、此后不可改。
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
		Long: `工程的当前草稿：一组具名文件，随时可改，改它不产生版本，也不参与发布。

草稿是工作区，不是历史。要留下一份不会被后续改动影响的清单，用
"aladdin galaxy version save" 存一版。

**push 表达的是整组的期望状态**：目录里没有的路径就是"删掉"。`,
	}
	cmd.AddCommand(
		newGalaxyDraftListCommand(),
		newGalaxyDraftPullCommand(),
		newGalaxyDraftPushCommand(),
	)
	return cmd
}

func newGalaxyDraftListCommand() *cobra.Command {
	return requirePermission(&cobra.Command{
		Use:   "list <工程标识>",
		Short: "列出草稿里的路径与条目类别",
		Args:  exactArgs(1),
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
			entries := resp.Msg.GetDraft().GetEntries()
			if len(entries) == 0 {
				println(cmd.OutOrStdout(), "草稿还是空的。用 `aladdin galaxy draft push <工程标识> <目录>` 送一组文件上去。")
				return nil
			}
			for _, entry := range entries {
				printf(cmd.OutOrStdout(), "%s\n", describeEntry(entry))
			}
			return nil
		},
	}, rbac.PermissionGalaxyProjectRead)
}

func newGalaxyDraftPullCommand() *cobra.Command {
	return requirePermission(&cobra.Command{
		Use:   "pull <工程标识> <目录>",
		Short: "把草稿整组写到本地目录",
		Long: `把草稿整组写到本地目录。

它先取回清单，再按短时地址逐份下载。它是 push 的逆操作：两者往返之后目录内容
逐字相同。`,
		Args: exactArgs(2),
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
			entries := resp.Msg.GetDraft().GetEntries()
			if err := writeFileSet(args[1], entries, fetchEntry); err != nil {
				return err
			}
			printf(cmd.OutOrStdout(), "已写出 %d 个文件到 %s\n", len(entries), args[1])
			return nil
		},
	}, rbac.PermissionGalaxyProjectRead)
}

// newGalaxyDraftPushCommand 把本地目录**整组**替换成草稿。
//
// 它是内容唯一的写入路径（网页端只读，见 docs/design/galaxy/authoring.md）。
// 内部包含若干次直传：文本文件成为内容对象（按内容摘要、仅当不存在时写入），
// 非文本文件成为资产条目。两者走的是同一条直传链路，因此这一条命令是一次
// "整组送上去"的编排，而它仍然是一个原子动作——最后一次 PushDraft 要么整组
// 成为草稿，要么什么都没变。
func newGalaxyDraftPushCommand() *cobra.Command {
	return requirePermission(&cobra.Command{
		Use:   "push <工程标识> <目录>",
		Short: "以本地目录整组替换草稿",
		Long: `以本地目录整组替换草稿。

**目录里没有的路径就是"删掉"**：它表达的是整组的期望状态，不是增量。

目录里每个文件都要成为一个条目：文本文件（按工程形态的白名单）作为内容对象，
其余作为资产上传。文本里出现 asset://<资产标识> 的地方会额外登记一条资产条目。`,
		Args: exactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, svc, done, err := galaxyCall()
			if err != nil {
				return err
			}
			defer done()

			projectID := args[0]
			project, err := svc.GetProject(ctx, connect.NewRequest(&galaxyv1.GetProjectRequest{
				ProjectId: projectID,
			}))
			if err != nil {
				return err
			}
			form, err := siteFormOf(project.Msg.GetProject().GetForm())
			if err != nil {
				return err
			}
			files, err := readFileSet(args[1], form)
			if err != nil {
				return err
			}
			entries, err := uploadFileSet(ctx, svc, projectID, form, files)
			if err != nil {
				return err
			}
			resp, err := svc.PushDraft(ctx, connect.NewRequest(&galaxyv1.PushDraftRequest{
				ProjectId: projectID,
				Entries:   entries,
			}))
			if err != nil {
				return err
			}
			if flags.output == "json" {
				return printJSON(resp.Msg)
			}
			printf(cmd.OutOrStdout(), "已推送 %d 个文件（%s），%s\n",
				len(entries), resp.Msg.GetDraft().GetUpdatedAt(), args[1])
			return nil
		},
	}, rbac.PermissionGalaxyProjectWrite)
}

// siteFormOf 把接口枚举翻译成领域取值。
func siteFormOf(form galaxyv1.SiteForm) (galaxy.SiteForm, error) {
	switch form {
	case galaxyv1.SiteForm_SITE_FORM_STATIC:
		return galaxy.SiteFormStatic, nil
	case galaxyv1.SiteForm_SITE_FORM_DOCS:
		return galaxy.SiteFormDocs, nil
	default:
		return "", fmt.Errorf("服务端给出的工程形态无法识别：%s", form)
	}
}

// parseSiteFormFlag 把 --form 的取值解析成接口枚举。
func parseSiteFormFlag(raw string) (galaxyv1.SiteForm, error) {
	switch raw {
	case string(galaxy.SiteFormStatic):
		return galaxyv1.SiteForm_SITE_FORM_STATIC, nil
	case string(galaxy.SiteFormDocs):
		return galaxyv1.SiteForm_SITE_FORM_DOCS, nil
	default:
		return galaxyv1.SiteForm_SITE_FORM_UNSPECIFIED,
			usageErrorf("--form 必须是 static 或 docs（当前是 %q）；形态创建时定下、此后不可改", raw)
	}
}
