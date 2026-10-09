package main

import (
	"context"
	"fmt"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	galaxyv1 "github.com/poetlife/aladdin/api/gen/aladdin/galaxy/v1"
	"github.com/poetlife/aladdin/api/gen/aladdin/galaxy/v1/galaxyv1connect"
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
"aladdin galaxy version save" 存一版——**每完成一个可演示的里程碑就存一版**，
这是推荐的做法，也是唯一能保证"随时退回去"的做法。

**每次 push 会把被替换掉的那份旧清单留成一条草稿快照**（draft history 看得到，
draft restore 能退回去）。那是兜底：它按保留策略过期，而版本不会。

**push 表达的是整组的期望状态**：目录里没有的路径就是"删掉"。**它只碰指定的那个
槽**：另一个槽的草稿与版本不受影响。

--slot 在单槽工程上可以省略；两个槽都有时必须给出。`,
	}
	cmd.AddCommand(
		newGalaxyDraftListCommand(),
		newGalaxyDraftPullCommand(),
		newGalaxyDraftPushCommand(),
		newGalaxyDraftHistoryCommand(),
		newGalaxyDraftRestoreCommand(),
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
	var slot, description string
	var save bool

	cmd := &cobra.Command{
		Use:   "push <工程标识> <目录>",
		Short: "以本地目录整组替换草稿",
		Long: `以本地目录整组替换**某一个槽**的草稿。

**目录里没有的路径就是"删掉"**：它表达的是整组的期望状态，不是增量。

目录里每个文件都要成为一个条目：文本文件（按该槽的白名单）作为内容对象，其余
作为资产上传。文本里出现 asset://<资产标识> 的地方会额外登记一条资产条目。

**被换掉的那份清单会留成一条草稿快照**（同目录里没有的路径就是删掉，因此它记的
正是"你刚刚覆盖掉的东西"）。用 draft history 看它，用 draft restore 退回去。
相同清单不重复留。

**推完记得存版本。** 每完成一个可演示的里程碑就存一版：

  aladdin galaxy draft push <工程标识> ./dist --save -m "加了封面"

--save 在推完之后立刻存一个版本，-m 给出这一版的说明（给出 -m 即隐含 --save）。
不带这两个时，命令在末尾提示"尚未存为版本"。

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
			pushed, err := svc.PushDraft(ctx, connect.NewRequest(&galaxyv1.PushDraftRequest{
				ProjectId: projectID,
				Entries:   entries,
				Slot:      resolved,
			}))
			if err != nil {
				return err
			}

			// -m 隐含 --save：写下说明这个动作本身就表达了"我要把这一版记下来"，
			// 而"给了说明却没存版本"只会让人以为说明被丢掉了。
			if save || cmd.Flags().Changed("description") {
				saved, err := svc.SaveVersion(ctx, connect.NewRequest(&galaxyv1.SaveVersionRequest{
					ProjectId:   projectID,
					Slot:        resolved,
					Description: description,
				}))
				if err != nil {
					return err
				}
				if flags.output != "json" {
					printf(cmd.OutOrStdout(), "已保存版本 #%d（%s）\n",
						saved.Msg.GetVersion().GetSeq(), saved.Msg.GetVersion().GetId())
				}
				if flags.output == "json" {
					return printJSON(pushed.Msg)
				}
				return nil
			}

			if flags.output == "json" {
				return printJSON(pushed.Msg)
			}
			printf(cmd.OutOrStdout(), "已推送 %d 个文件（%s），%s\n",
				len(entries), pushed.Msg.GetDraft().GetUpdatedAt(), args[1])
			printUnsavedHint(cmd, ctx, svc, projectID, resolved, converted, entries)
			return nil
		},
	}
	addSlotFlag(cmd, &slot, "要替换哪个内容槽的草稿：site 或 docs（单槽工程可省略）")
	cmd.Flags().BoolVar(&save, "save", false, "推完之后立刻把草稿存成一个版本")
	cmd.Flags().StringVarP(&description, "description", "m", "",
		"这一版的说明（给出它即隐含 --save）")

	requirePermission(cmd, rbac.PermissionGalaxyProjectWrite)
	return cmd
}

// printUnsavedHint 在"推完但没有存版本"时给出那句引导（唯一入口）。
//
// 它存在的理由是 agent：用命令行创作时最常见的流程是反复 push、从不 save，而
// 那条路上唯一的记录是草稿历史（会过期）。因此这条提示不是可选的装饰。
//
// **提示失败不影响这次 push 的结论**：引导靠一次额外的读取得出，读不到时给一句
// 不细分的提示，而不是把一次已经成功的推送表现成失败。
func printUnsavedHint(cmd *cobra.Command, ctx context.Context, svc galaxyv1connect.GalaxyServiceClient,
	projectID string, slot galaxyv1.ContentSlot, converted galaxy.ContentSlot, entries []*galaxyv1.FileEntry) {
	slotFlag := ""
	if converted != "" {
		// 双槽工程必须显式给槽；单槽工程给了也无害——提示里统一带上，照抄即可。
		slotFlag = " --slot " + string(converted)
	}
	suggestion := fmt.Sprintf(
		"aladdin galaxy version save <工程标识>%s -m \"说明\"", slotFlag)

	versions, err := svc.ListVersions(ctx, connect.NewRequest(&galaxyv1.ListVersionsRequest{
		ProjectId: projectID,
		Slot:      slot,
	}))
	if err == nil {
		if sameAsNewestVersion(versions.Msg.GetVersions(), entries) {
			// 手里的这一份与最新的版本是同一份内容：这次推送没有产生未保存的改动，
			// 提示只会变成噪声。
			return
		}
		if len(versions.Msg.GetVersions()) == 0 {
			// 最该醒目的一档：这个槽一份版本都没有，"退回去"目前只能靠会过期的
			// 草稿历史。
			printf(cmd.OutOrStdout(),
				"\n提醒：这个槽还没有任何版本，草稿没有可回退的对照。\n"+
					"      运行 %s 留下一个不会过期的快照。\n", suggestion)
			return
		}
	}
	printf(cmd.OutOrStdout(),
		"\n提示：草稿已更新，尚未存为版本。\n"+
			"      运行 %s 留下可回退的快照，或下次 push 加上 --save。\n"+
			"      （被覆盖掉的那一份在 draft history 里，但它会过期。）\n", suggestion)
}

// sameAsNewestVersion 判定手里的这一份与最新的那个版本是不是同一份内容。
//
// 空清单的版本与空草稿也是同一份——两处都为空时它是"什么都没变"。
func sameAsNewestVersion(versions []*galaxyv1.Version, entries []*galaxyv1.FileEntry) bool {
	if len(versions) == 0 {
		return false
	}
	newest := versions[0]
	for _, candidate := range versions {
		if candidate.GetSeq() > newest.GetSeq() {
			newest = candidate
		}
	}
	return entriesSignature(newest.GetEntries()) == entriesSignature(entries)
}

// newGalaxyDraftHistoryCommand 列出草稿历史。
func newGalaxyDraftHistoryCommand() *cobra.Command {
	var slot string

	cmd := &cobra.Command{
		Use:   "history <工程标识>",
		Short: "列出草稿的过去状态",
		Long: `列出这个槽**被替换掉的那些草稿清单**，最近的在前。

它们**不是版本**：没有序号、不能发布，而且会过期（每个槽最近 50 条、且不超过
14 天）。想留下不会过期的一份，把某一条存成版本：

  aladdin galaxy version save <工程标识> --from-snapshot <快照标识> -m "说明"

每一次 push 与每一次 draft restore 都会留下一条（相同清单不重复留），因此"刚刚
被覆盖掉的那一份"总在这里。`,
		Args: exactArgs(1),
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
			resp, err := svc.ListDraftSnapshots(ctx, connect.NewRequest(&galaxyv1.ListDraftSnapshotsRequest{
				ProjectId: args[0],
				Slot:      resolved,
			}))
			if err != nil {
				return err
			}
			if flags.output == "json" {
				return printJSON(resp.Msg)
			}
			snapshots := resp.Msg.GetSnapshots()
			if len(snapshots) == 0 {
				println(cmd.OutOrStdout(),
					"还没有草稿历史。每次 draft push 会留下一条——相同清单不重复留。")
				return nil
			}
			for _, snapshot := range snapshots {
				printf(cmd.OutOrStdout(), "%s  %s  %d 个文件  来自 %s\n",
					snapshot.GetId(), snapshot.GetCreatedAt(),
					len(snapshot.GetEntries()), describeSnapshotSource(snapshot.GetSource()))
			}
			return nil
		},
	}
	addSlotFlag(cmd, &slot, "要看哪个内容槽的草稿历史：site 或 docs（单槽工程可省略）")
	requirePermission(cmd, rbac.PermissionGalaxyProjectRead)
	return cmd
}

// describeSnapshotSource 把快照的来源写成人读的一小段。
//
// 空值有明确含义：那一端没有带上报端标识（老版本的命令行、或第三方客户端），因此
// 不能写成"未知"就算了——"这一条不是网页/命令行改的"是要说出来的事实。
func describeSnapshotSource(source string) string {
	switch source {
	case "web":
		return "网页端"
	case "cli":
		return "命令行"
	default:
		return "未知来源"
	}
}

// newGalaxyDraftRestoreCommand 把草稿换回某一条历史。
func newGalaxyDraftRestoreCommand() *cobra.Command {
	var slot string

	cmd := &cobra.Command{
		Use:   "restore <工程标识> <快照标识>",
		Short: "把草稿换回某一条历史",
		Long: `把某个槽的草稿**整组换回**某一条草稿历史的清单。

**恢复不会让你丢掉恢复前的内容**：它是一次草稿替换，因此当前那份也会被留成一条
新的历史记录。找错了再恢复回来即可。

**引用了已删除资产的快照不能恢复**：恢复出来会是一份校验必然失败的草稿，而那时
"恢复成功"这句话看不出问题在哪。命令会如实拒绝并指出是哪一条路径引用了哪个资产。

它只碰指定的那个槽。`,
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
			resp, err := svc.RestoreDraftSnapshot(ctx, connect.NewRequest(&galaxyv1.RestoreDraftSnapshotRequest{
				ProjectId:  projectID,
				Slot:       resolved,
				SnapshotId: args[1],
			}))
			if err != nil {
				return err
			}
			if flags.output == "json" {
				return printJSON(resp.Msg)
			}
			printf(cmd.OutOrStdout(), "已恢复 %d 个文件的清单（恢复前的那份已留成新的历史记录）\n",
				len(resp.Msg.GetDraft().GetEntries()))
			return nil
		},
	}
	addSlotFlag(cmd, &slot, "要恢复到哪个内容槽：site 或 docs（单槽工程可省略）")
	requirePermission(cmd, rbac.PermissionGalaxyProjectWrite)
	return cmd
}
