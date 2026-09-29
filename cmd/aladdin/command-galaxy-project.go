package main

import (
	"context"
	"fmt"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	galaxyv1 "github.com/poetlife/aladdin/api/gen/aladdin/galaxy/v1"
	"github.com/poetlife/aladdin/api/gen/aladdin/galaxy/v1/galaxyv1connect"
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
		newGalaxyProjectSlotCommand(),
		newGalaxyProjectUpdateCommand(),
		newGalaxyProjectDeleteCommand(),
	)
	return cmd
}

// projectForSlot 读回工程，并定出这次调用针对哪个槽（唯一入口）。
//
// 顺序不可换：省略 --slot 时要看这个工程启用了几个槽才能定下来。
func projectForSlot(ctx context.Context, svc galaxyv1connect.GalaxyServiceClient, cmd *cobra.Command, projectID string) (*galaxyv1.Project, galaxyv1.ContentSlot, error) {
	resp, err := svc.GetProject(ctx, connect.NewRequest(&galaxyv1.GetProjectRequest{ProjectId: projectID}))
	if err != nil {
		return nil, galaxyv1.ContentSlot_CONTENT_SLOT_UNSPECIFIED, err
	}
	project := resp.Msg.GetProject()
	slot, err := resolveSlot(cmd, project)
	if err != nil {
		return nil, galaxyv1.ContentSlot_CONTENT_SLOT_UNSPECIFIED, err
	}
	return project, slot, nil
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
				println(cmd.OutOrStdout(), "还没有工程。用 `aladdin galaxy project create --slot site` 建一个。")
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
	var slots []string

	cmd := &cobra.Command{
		Use:   "create",
		Short: "创建一个工程",
		Long: `创建一个工程，标识由服务端分配，不可猜、不可改、不复用。

**至少给一个内容槽，可以给两个**（--slot 可重复）：

  site  整站文件原样服务（手写单页、构建产物），入口是 index.html，
        地址是站点根
  docs  一组 markdown 渲染成多页，入口是 index.md，地址是站点根下的 docs/

两个槽各有自己的草稿、版本与发布地址，**互不影响**：站点发出去不影响文档，
推文档也不会覆盖站点。**槽此后只增不删**（用 project slot add 加）。

名称不参与任何查找，只是给你自己认的标签：不按名称查工程、不加唯一约束、
不进发布地址。因此它可以留空，事后再用 project update 补上。`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			parsed, err := parseSlotFlags(slots)
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
				Slots:       parsed,
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
	cmd.Flags().StringArrayVar(&slots, slotFlagName, nil,
		"要启用的内容槽：site 或 docs，可重复给出（至少一个）")

	requirePermission(cmd, rbac.PermissionGalaxyProjectWrite)
	return cmd
}

// newGalaxyProjectSlotCommand 是内容槽的父命令。
//
// **只有"加"，没有"删"**：槽的语义与地址是固定的，已有的版本与地址都挂在它
// 上面，因此删除是另一件事（见 docs/design/galaxy/site-model.md）。
func newGalaxyProjectSlotCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "slot",
		Short: "内容槽",
		Long: `工程的内容槽：site（整站文件原样服务）与 docs（markdown 渲染成多页）。

一个工程可以两个槽都有，两槽各有自己的草稿、版本、发布指针与地址，互不影响。
**槽只增不删**——这里只有 add。`,
	}
	cmd.AddCommand(newGalaxyProjectSlotAddCommand())
	return cmd
}

func newGalaxyProjectSlotAddCommand() *cobra.Command {
	var slot string

	cmd := &cobra.Command{
		Use:   "add <工程标识>",
		Short: "给一个工程加一个内容槽",
		Long: `给一个工程加一个内容槽。

**它是单向的**：没有"删掉一个槽"的对应命令，因为已有的版本与地址都挂在槽上。
**加一个槽不改变另一个槽**：那个槽的草稿、版本与发布指针都原样。

一条要注意的：文档槽占住 "docs" 这一段路径，因此**站点槽里已经存在 docs/... 时
不能加文档槽**——那些路径会从"站点里的一份文件"变成"文档的地址"，而它们可能
已经发布出去了。命令会拒绝并指出是哪一条。`,
		Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			parsed, err := parseSlotFlag(slot)
			if err != nil {
				return err
			}
			ctx, svc, done, err := galaxyCall()
			if err != nil {
				return err
			}
			defer done()

			resp, err := svc.AddProjectSlot(ctx, connect.NewRequest(&galaxyv1.AddProjectSlotRequest{
				ProjectId: args[0],
				Slot:      parsed,
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
	cmd.Flags().StringVar(&slot, slotFlagName, "", "要加的槽：site 或 docs（必填）")

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
	var slot string

	cmd := &cobra.Command{
		Use:   "base <工程标识>",
		Short: "输出某个内容槽的发布根，供构建命令使用",
		Long: `输出某个内容槽的发布根：site 槽形如 /g/<工程标识>/，docs 槽是它下面的
/g/<工程标识>/docs/。

把它交给构建命令（vite 用 --base），产物里的绝对路径才会落在站点根上：

  vite build --base "$(aladdin galaxy project base <工程标识> --slot site)"

输出的是**路径**而不是完整地址：产物里写完整地址会让站点绑死在当前这个发布域上，
换一个域就要重新构建。

--slot 在单槽工程上可以省略；两个槽都有时必须给出，否则不知道该输出哪一个。`,
		Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, svc, done, err := galaxyCall()
			if err != nil {
				return err
			}
			defer done()

			project, resolved, err := projectForSlot(ctx, svc, cmd, args[0])
			if err != nil {
				return err
			}
			if flags.output == "json" {
				return printJSON(project)
			}
			base := ""
			for _, candidate := range project.GetSlots() {
				if candidate.GetSlot() == resolved {
					base = candidate.GetBaseUrl()
					break
				}
			}
			if base == "" {
				return fmt.Errorf("这个部署没有配置发布域，算不出发布根")
			}
			// 只输出地址本身，便于 `$(...)` 直接接给构建命令。
			printf(cmd.OutOrStdout(), "%s\n", base)
			return nil
		},
	}
	addSlotFlag(cmd, &slot, "要取哪个内容槽的发布根：site 或 docs（单槽工程可省略）")
	requirePermission(cmd, rbac.PermissionGalaxyProjectRead)
	return cmd
}

func newGalaxyProjectUpdateCommand() *cobra.Command {
	var name, description string

	cmd := &cobra.Command{
		Use:   "update <工程标识>",
		Short: "修改工程的名称与简介",
		Long: `修改工程的名称与简介。

只改你显式给出的那一项：没给的标志保持原值，给出空串才是清空。因此
"--name 新名字" 不会顺手把简介抹掉。

**内容槽不在其中**：槽只增不删，"有哪些槽"由 project slot add 回答。
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
		Long: `工程**某个内容槽**的当前草稿：一组具名文件，随时可改，改它不产生版本，也不
参与发布。

草稿是工作区，不是历史。要留下一份不会被后续改动影响的清单，用
"aladdin galaxy version save" 存一版。

**push 表达的是整组的期望状态**：目录里没有的路径就是"删掉"。**它只碰指定的那个
槽**：另一个槽的草稿与版本不受影响。

--slot 在单槽工程上可以省略；两个槽都有时必须给出。`,
	}
	cmd.AddCommand(
		newGalaxyDraftListCommand(),
		newGalaxyDraftPullCommand(),
		newGalaxyDraftPushCommand(),
	)
	return cmd
}

func newGalaxyDraftListCommand() *cobra.Command {
	var slot string

	cmd := &cobra.Command{
		Use:   "list <工程标识>",
		Short: "列出草稿里的路径与条目类别",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, svc, done, err := galaxyCall()
			if err != nil {
				return err
			}
			defer done()

			_, resolved, err := projectForSlot(ctx, svc, cmd, args[0])
			if err != nil {
				return err
			}
			resp, err := svc.GetDraft(ctx, connect.NewRequest(&galaxyv1.GetDraftRequest{
				ProjectId: args[0],
				Slot:      resolved,
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
	}
	addSlotFlag(cmd, &slot, "要看哪个内容槽的草稿：site 或 docs（单槽工程可省略）")
	requirePermission(cmd, rbac.PermissionGalaxyProjectRead)
	return cmd
}

func newGalaxyDraftPullCommand() *cobra.Command {
	var slot string

	cmd := &cobra.Command{
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

			_, resolved, err := projectForSlot(ctx, svc, cmd, args[0])
			if err != nil {
				return err
			}
			resp, err := svc.GetDraft(ctx, connect.NewRequest(&galaxyv1.GetDraftRequest{
				ProjectId: args[0],
				Slot:      resolved,
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
	}
	addSlotFlag(cmd, &slot, "要下载哪个内容槽的草稿：site 或 docs（单槽工程可省略）")
	requirePermission(cmd, rbac.PermissionGalaxyProjectRead)
	return cmd
}

// newGalaxyDraftPushCommand 把本地目录**整组**替换成某个槽的草稿。
//
// 它是内容唯一的写入路径（网页端只读，见 docs/design/galaxy/authoring.md）。
// 内部包含若干次直传：文本文件成为内容对象（按内容摘要、仅当不存在时写入），
// 非文本文件成为资产条目。两者走的是同一条直传链路，因此这一条命令是一次
// "整组送上去"的编排，而它仍然是一个原子动作——最后一次 PushDraft 要么整组
// 成为草稿，要么什么都没变。
func newGalaxyDraftPushCommand() *cobra.Command {
	var slot string

	cmd := &cobra.Command{
		Use:   "push <工程标识> <目录>",
		Short: "以本地目录整组替换草稿",
		Long: `以本地目录整组替换**某一个槽**的草稿。

**目录里没有的路径就是"删掉"**：它表达的是整组的期望状态，不是增量。

目录里每个文件都要成为一个条目：文本文件（按该槽的白名单）作为内容对象，其余
作为资产上传。文本里出现 asset://<资产标识> 的地方会额外登记一条资产条目。

一条本地就拦住的规则：**docs 是文档槽占用的保留段**，因此推 site 槽时目录里出现
docs/... 会直接报错并指出那个路径（服务端同样会拒，这里省一次往返）。`,
		Args: exactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, svc, done, err := galaxyCall()
			if err != nil {
				return err
			}
			defer done()

			projectID := args[0]
			_, resolved, err := projectForSlot(ctx, svc, cmd, projectID)
			if err != nil {
				return err
			}
			converted, err := slotOf(resolved)
			if err != nil {
				return err
			}
			files, err := readFileSet(args[1], converted)
			if err != nil {
				return err
			}
			entries, err := uploadFileSet(ctx, svc, projectID, converted, files)
			if err != nil {
				return err
			}
			resp, err := svc.PushDraft(ctx, connect.NewRequest(&galaxyv1.PushDraftRequest{
				ProjectId: projectID,
				Entries:   entries,
				Slot:      resolved,
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
	}
	addSlotFlag(cmd, &slot, "要替换哪个内容槽的草稿：site 或 docs（单槽工程可省略）")
	requirePermission(cmd, rbac.PermissionGalaxyProjectWrite)
	return cmd
}
