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
		Long: `文件清单的一次不可变快照。

保存即冻结：此后改草稿、改工程名称、删资产都不改变一个版本读回的清单。
因此"发布过的页面内容变了"这件事不会发生。发布只能发布版本，不能发布草稿。

**每完成一个可演示的里程碑就存一版**：这是推荐的做法，也是唯一能保证"随时退回去"
的做法（草稿历史会过期，版本不会）。-m 给这一版一句说明，日后再看列表时认得出
哪一版是哪一版。

**版本按内容槽隔离**：每个槽各有自己的一串版本，序号在槽内递增。--slot 在单槽
工程上可以省略；两个槽都有时必须给出。`,
	}
	cmd.AddCommand(
		newGalaxyVersionSaveCommand(),
		newGalaxyVersionListCommand(),
		newGalaxyVersionGetCommand(),
		newGalaxyVersionPullCommand(),
		newGalaxyVersionDescribeCommand(),
		newGalaxyVersionDeleteCommand(),
	)
	return cmd
}

func newGalaxyVersionSaveCommand() *cobra.Command {
	var slot, description, fromSnapshot string

	cmd := &cobra.Command{
		Use:   "save <工程标识>",
		Short: "把某个槽的当前草稿保存成一个版本",
		Long: `把某个内容槽当前草稿的清单冻结成一个不可变版本。

连续保存两次相同清单会产生两个版本，而不是"检测到重复就不新增"：两次保存
是两个不同的动作。

-m 给这一版一句说明（加了什么、改了什么）。说明**可以事后改**（version describe），
因为写错一句话与"把这一版的内容改掉"是两件事。

--from-snapshot 存的是**某一条草稿历史**的清单，而不是当前草稿：它回答的是
"我想把那次中间态正式记下来"。那条历史引用的资产必须都还在，否则会被拒绝——
存一个发布不出去的版本与"版本不可变"的含义冲突。

这一次冻结**不搬运任何字节**：字节本来就是按内容摘要寻址的不可变对象，由多个
版本共享。**它也不碰另一个槽**。`,
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
			resp, err := svc.SaveVersion(ctx, connect.NewRequest(&galaxyv1.SaveVersionRequest{
				ProjectId:      args[0],
				Slot:           resolved,
				Description:    description,
				FromSnapshotId: fromSnapshot,
			}))
			if err != nil {
				return err
			}
			if flags.output == "json" {
				return printJSON(resp.Msg)
			}
			v := resp.Msg.GetVersion()
			printf(cmd.OutOrStdout(), "已保存版本 #%d（%s），%d 个文件\n",
				v.GetSeq(), v.GetId(), len(v.GetEntries()))
			if v.GetDescription() != "" {
				printf(cmd.OutOrStdout(), "  说明: %s\n", v.GetDescription())
			}
			return nil
		},
	}
	addSlotFlag(cmd, &slot, "要保存哪个内容槽的草稿：site 或 docs（单槽工程可省略）")
	cmd.Flags().StringVarP(&description, "description", "m", "", "这一版的说明（可留空，之后也能改）")
	cmd.Flags().StringVar(&fromSnapshot, "from-snapshot", "",
		"存某一条草稿历史（draft history 的标识）而不是当前草稿")
	requirePermission(cmd, rbac.PermissionGalaxyProjectWrite)
	return cmd
}

// newGalaxyVersionDescribeCommand 改一个版本的说明。
//
// 它只动说明那一层：清单、序号与保存时间逐字不变（见
// docs/design/galaxy/project-versioning.md）。命令名用 describe 而不是 update，
// 是因为"更新一个版本"读起来像"改这一版的内容"，而那个动作不存在。
func newGalaxyVersionDescribeCommand() *cobra.Command {
	var slot, description string

	cmd := &cobra.Command{
		Use:   "describe <工程标识> <版本标识>",
		Short: "给一个版本补写或修改说明",
		Long: `给一个版本补写或修改那一句说明。

**它只改说明**：清单、序号、保存时间与渲染规则版本在改动前后逐字不变，因此已经
发出去的页面不受影响，产物里也没有说明这一项。给出空串表示清空。

写错一句话不必再存一版——那正是说明可以事后改的理由。`,
		Args: exactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !cmd.Flags().Changed("description") {
				return usageErrorf("必须给出 --description（空串表示清空说明）")
			}
			ctx, svc, done, err := galaxyCall()
			if err != nil {
				return err
			}
			defer done()

			_, resolved, err := projectForSlot(ctx, svc, cmd, args[0])
			if err != nil {
				return err
			}
			resp, err := svc.UpdateVersion(ctx, connect.NewRequest(&galaxyv1.UpdateVersionRequest{
				ProjectId:   args[0],
				VersionId:   args[1],
				Slot:        resolved,
				Description: description,
			}))
			if err != nil {
				return err
			}
			if flags.output == "json" {
				return printJSON(resp.Msg)
			}
			v := resp.Msg.GetVersion()
			printf(cmd.OutOrStdout(), "已更新版本 #%d 的说明: %s\n", v.GetSeq(), v.GetDescription())
			return nil
		},
	}
	addSlotFlag(cmd, &slot, "这个版本属于哪个内容槽：site 或 docs（单槽工程可省略）")
	cmd.Flags().StringVarP(&description, "description", "m", "", "新的说明（给出空串表示清空）")
	requirePermission(cmd, rbac.PermissionGalaxyProjectWrite)
	return cmd
}

func newGalaxyVersionListCommand() *cobra.Command {
	var slot string

	cmd := &cobra.Command{
		Use:   "list <工程标识>",
		Short: "列出某个内容槽的版本",
		Long: `列出某个内容槽的版本，按序号排序。

序号只用于展示与排序：删除一个版本会让后续序号出现空洞，这不是错误。两个槽
各数各的，因此它们各自的第一个版本序号都是 1。`,
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
			resp, err := svc.ListVersions(ctx, connect.NewRequest(&galaxyv1.ListVersionsRequest{
				ProjectId: args[0],
				Slot:      resolved,
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
				printf(cmd.OutOrStdout(), "#%-4d %s  %s  %d 个文件\n",
					v.GetSeq(), v.GetId(), v.GetSavedAt(), len(v.GetEntries()))
				if v.GetDescription() != "" {
					printf(cmd.OutOrStdout(), "      说明: %s\n", v.GetDescription())
				}
			}
			return nil
		},
	}
	addSlotFlag(cmd, &slot, "要列出哪个内容槽的版本：site 或 docs（单槽工程可省略）")
	requirePermission(cmd, rbac.PermissionGalaxyProjectRead)
	return cmd
}

func newGalaxyVersionGetCommand() *cobra.Command {
	var slot, entryPath string

	cmd := &cobra.Command{
		Use:   "get <工程标识> <版本标识>",
		Short: "读取一个版本里的某一份文件",
		Long: `读取一个版本里的某一份文件。

--path 指明读哪一份（形如 index.html 或 guide/intro.md）；不给时输出整个清单的
摘要。要一次取回整组，用 version pull。

保存时的内容此后逐字不变。`,
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
			resp, err := svc.GetVersion(ctx, connect.NewRequest(&galaxyv1.GetVersionRequest{
				ProjectId: args[0],
				VersionId: args[1],
				Slot:      resolved,
			}))
			if err != nil {
				return err
			}
			entries := resp.Msg.GetVersion().GetEntries()
			if flags.output == "json" {
				return printJSON(resp.Msg)
			}
			if entryPath == "" {
				for _, entry := range entries {
					printf(cmd.OutOrStdout(), "%s\n", describeEntry(entry))
				}
				return nil
			}
			entry, ok := findEntry(entries, entryPath)
			if !ok {
				return usageErrorf("版本 %s 里没有 %q", args[1], entryPath)
			}
			data, err := fetchEntry(entry)
			if err != nil {
				return err
			}
			_, err = cmd.OutOrStdout().Write(data)
			return err
		},
	}
	cmd.Flags().StringVar(&entryPath, "path", "", "要读的文件在文件组里的路径")
	addSlotFlag(cmd, &slot, "这个版本属于哪个内容槽：site 或 docs（单槽工程可省略）")

	requirePermission(cmd, rbac.PermissionGalaxyProjectRead)
	return cmd
}

func newGalaxyVersionPullCommand() *cobra.Command {
	var slot string

	cmd := &cobra.Command{
		Use:   "pull <工程标识> <版本标识> <目录>",
		Short: "把某个版本整组写到本地目录",
		Long: `把某个版本整组写到本地目录，路径与内容都与该版本一致。

它是 draft push 的一次快照：取回来的是一份不会被后续改动影响的内容。`,
		Args: exactArgs(3),
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
			resp, err := svc.GetVersion(ctx, connect.NewRequest(&galaxyv1.GetVersionRequest{
				ProjectId: args[0],
				VersionId: args[1],
				Slot:      resolved,
			}))
			if err != nil {
				return err
			}
			entries := resp.Msg.GetVersion().GetEntries()
			if err := writeFileSet(args[2], entries, fetchEntry); err != nil {
				return err
			}
			printf(cmd.OutOrStdout(), "已写出 %d 个文件到 %s\n", len(entries), args[2])
			return nil
		},
	}
	addSlotFlag(cmd, &slot, "这个版本属于哪个内容槽：site 或 docs（单槽工程可省略）")
	requirePermission(cmd, rbac.PermissionGalaxyProjectRead)
	return cmd
}

// findEntry 按路径在清单里找一条条目。
func findEntry(entries []*galaxyv1.FileEntry, entryPath string) (*galaxyv1.FileEntry, bool) {
	for _, entry := range entries {
		if entry.GetPath() == entryPath {
			return entry, true
		}
	}
	return nil, false
}

func newGalaxyVersionDeleteCommand() *cobra.Command {
	var slot string

	cmd := &cobra.Command{
		Use:   "delete <工程标识> <版本标识>",
		Short: "删除一个版本",
		Long: `删除一个版本。

被**它所属槽的**发布指向的版本不可删——那会让那条发布地址指向一个不存在的
版本。另一个槽的发布指针不影响这个判定。
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

			_, resolved, err := projectForSlot(ctx, svc, cmd, projectID)
			if err != nil {
				return err
			}
			resp, err := svc.DeleteVersion(ctx, connect.NewRequest(&galaxyv1.DeleteVersionRequest{
				ProjectId: projectID,
				VersionId: versionID,
				Slot:      resolved,
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
	addSlotFlag(cmd, &slot, "这个版本属于哪个内容槽：site 或 docs（单槽工程可省略）")
	requirePermission(cmd, rbac.PermissionGalaxyProjectWrite)
	return markDangerous(cmd)
}

// newGalaxyValidateCommand 校验**某一个槽已保存的草稿**能不能发布。
//
// 它校验的是草稿而不是"你手上这一份"：内容的写入只有命令行一条路径（push 整组
// 表达期望状态），因此"校验一份还没保存的内容"这个形状不存在。事实上的用法是
// `push && validate && publish`，每一步的失败都能被单独处理。
func newGalaxyValidateCommand() *cobra.Command {
	var slot string

	cmd := &cobra.Command{
		Use:   "validate <工程标识>",
		Short: "校验某个槽的当前草稿能不能发布",
		Long: `校验某个内容槽的当前草稿能不能发布。

它与服务端的发布前置校验是同一份规则，因此"这里通过"与"发布能成功"是同一个
结论。

有问题时逐条列出（含文件与行号），并以非零状态退出，好让 "validate && publish"
这类脚本成立；"这份内容不能发布"是一个结论，不是一次调用失败。`,
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
			resp, err := svc.ValidateDraft(ctx, connect.NewRequest(&galaxyv1.ValidateDraftRequest{
				ProjectId: args[0],
				Slot:      resolved,
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
					println(cmd.OutOrStdout(), "无问题：这份草稿可以发布。")
					return nil
				}
				for _, problem := range problems {
					printf(cmd.OutOrStdout(), "- %s\n", describeProblem(problem))
				}
			}
			if len(resp.Msg.GetProblems()) > 0 {
				return fmt.Errorf("草稿有 %d 处问题，不能发布", len(resp.Msg.GetProblems()))
			}
			return nil
		},
	}
	addSlotFlag(cmd, &slot, "要校验哪个内容槽的草稿：site 或 docs（单槽工程可省略）")

	requirePermission(cmd, rbac.PermissionGalaxyProjectRead)
	return cmd
}

// describeProblem 把一处问题写成一句带位置的话。
func describeProblem(problem *galaxyv1.ValidationProblem) string {
	switch {
	case problem.GetPath() != "" && problem.GetLine() > 0:
		return fmt.Sprintf("%s:%d %s", problem.GetPath(), problem.GetLine(), problem.GetMessage())
	case problem.GetPath() != "":
		return fmt.Sprintf("%s %s", problem.GetPath(), problem.GetMessage())
	default:
		return problem.GetMessage()
	}
}
