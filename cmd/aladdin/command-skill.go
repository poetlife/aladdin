package main

import (
	"context"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	skillv1 "github.com/poetlife/aladdin/api/gen/aladdin/skill/v1"
	"github.com/poetlife/aladdin/api/gen/aladdin/skill/v1/skillv1connect"
	"github.com/poetlife/aladdin/internal/config"
	"github.com/poetlife/aladdin/internal/rbac"
	"github.com/poetlife/aladdin/pkg/client"
)

// 平台技能目录的命令行。
//
// **这是 agent 取用技能的那条路**（见 docs/design/skill/agent-access.md）：平台
// 是真相源，本机不装副本。因此这里有一条硬性约定——
//
//   - **stdout 只有内容**：`get` 打的是说明层与正文，`cat` 打的是那一条文件的
//     字节，别的什么都不打；提示与错误一律走 stderr。
//   - **没有"装到某个目录"这样的命令形态**，也不认识任何 agent 的 skill 目录
//     位置。平台能保证的是它这一侧没有任何一条会创建文件的路径。
func newSkillCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "skill",
		Short: "平台技能目录",
		Long: `检索平台纳管的创作辅助技能，并把它们的正文取到当前会话里。

**平台是真相源，本机不装副本。** 这几条命令只把内容打在标准输出上，不往任何
skill 目录（~/.claude/skills、Cursor 的目录等）写东西——装了本地副本之后真相源
就有两份，而分叉的表现不是报错，是"同一条指令在两台机器上得到不同的结果"。

取用（get / cat）计一次使用，按"人日"归并：同一个人在同一天取几次都算一次。

维护侧（add / sync / rollback / update / delete）要 skill.catalog.write，只有
技能管理员有。`,
	}
	cmd.AddCommand(
		newSkillListCommand(),
		newSkillGetCommand(),
		newSkillCatCommand(),
		newSkillVersionsCommand(),
		newSkillFavoriteCommand(),
		newSkillUnfavoriteCommand(),
		newSkillAddCommand(),
		newSkillSyncCommand(),
		newSkillRollbackCommand(),
		newSkillUpdateCommand(),
		newSkillDeleteCommand(),
	)
	return cmd
}

// skillCall 为一次技能目录调用准备好客户端与 context。
//
// 凭证注入、超时与连接关闭对每条命令都一样，因此收在一处：漏掉其中任何一件的
// 表现都是"这一条命令与别的不一样"。
//
// builtinTimeout 是这条命令在内置默认值那一层要用的超时，零值表示用全局内置默认。
// **只有取回远端内容的那两条命令（add / sync）给它**：它们的耗时随远端仓库的文件数
// 增长，而全局那 30 秒是给一次普通调用的量级（见 config.DefaultSkillCatalogTimeout）。
// 它不参与分层——用户显式给出的 --timeout、环境变量或配置文件照常覆盖它。
func skillCall(builtinTimeout time.Duration) (context.Context, skillv1connect.SkillServiceClient, skillv1connect.SkillAdminServiceClient, func(), error) {
	c, err := newClient(builtinTimeout)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	ctx, cancel := c.Context()
	read := client.NewService(c, skillv1connect.NewSkillServiceClient)
	admin := client.NewService(c, skillv1connect.NewSkillAdminServiceClient)
	return ctx, read, admin, func() {
		cancel()
		_ = c.Close()
	}, nil
}

func newSkillListCommand() *cobra.Command {
	var tags []string
	var favoritedOnly bool

	cmd := &cobra.Command{
		Use:   "list [关键词]",
		Short: "列出 / 搜索平台技能",
		Long: `列出平台纳管的技能。

关键词是大小写不敏感的子串匹配，匹配**有效标题与有效简介**（不搜正文：正文是
给取用者读的，把它拉进检索会让"这个技能是干什么的"淹没在"它提到了多少次某个
词"里）。多个 --tag 取**交集**：只列出同时带这些标签的技能。

排序是确定的（按有效标题的字典序），没有"按热度"这类排序——那会让"我上次看到
的第三条"变成一个无法复现的位置。`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			query := ""
			if len(args) == 1 {
				query = args[0]
			}
			ctx, svc, _, done, err := skillCall(0)
			if err != nil {
				return err
			}
			defer done()

			resp, err := svc.ListSkills(ctx, connect.NewRequest(&skillv1.ListSkillsRequest{
				Query:         query,
				Tags:          tags,
				FavoritedOnly: favoritedOnly,
			}))
			if err != nil {
				return err
			}
			if flags.output == "json" {
				return printJSON(resp.Msg)
			}
			skills := resp.Msg.GetSkills()
			if len(skills) == 0 {
				println(cmd.OutOrStdout(), "没有匹配的技能。")
				return nil
			}
			for _, item := range skills {
				printSkillSummary(cmd, item)
			}
			if resp.Msg.GetTruncated() {
				println(cmd.ErrOrStderr(),
					"结果被截断，请用关键词或标签缩小范围。")
			}
			return nil
		},
	}
	cmd.Flags().StringArrayVar(&tags, "tag", nil, "按标签筛选（可重复，多值取交集）")
	cmd.Flags().BoolVar(&favoritedOnly, "favorite", false, "只看我收藏的")

	requirePermission(cmd, rbac.PermissionSkillCatalogRead)
	return cmd
}

func newSkillGetCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "get <技能标识>",
		Short: "读取一个技能：说明层、来源、文件清单与正文",
		Long: `读取一个技能，**包括它的 SKILL.md 正文**。

这是 agent 的主入口：一次拿到"这是什么"（有效标题、有效简介、触发说明、标签、
来源、文件清单）与"它说了什么"（正文）。别的文件用 cat 单取。

它计一次使用（取用是使用量的计量点）。`,
		Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, svc, _, done, err := skillCall(0)
			if err != nil {
				return err
			}
			defer done()

			detail, err := svc.GetSkill(ctx, connect.NewRequest(&skillv1.GetSkillRequest{
				SkillId: args[0],
			}))
			if err != nil {
				return err
			}
			body, err := svc.GetSkillFile(ctx, connect.NewRequest(&skillv1.GetSkillFileRequest{
				SkillId: args[0],
				Path:    manifestPath,
			}))
			if err != nil {
				return err
			}
			if flags.output == "json" {
				return printJSON(map[string]any{
					"skill": detail.Msg.GetSkill(),
					"body":  string(body.Msg.GetContent()),
				})
			}
			printSkillDetail(cmd, detail.Msg.GetSkill())
			println(cmd.OutOrStdout(), "")
			println(cmd.OutOrStdout(), strings.TrimRight(string(body.Msg.GetContent()), "\n"))
			return nil
		},
	}
	requirePermission(cmd, rbac.PermissionSkillCatalogRead)
	return cmd
}

func newSkillCatCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cat <技能标识> <路径>",
		Short: "取一条文件的正文",
		Long: `把技能里某一条文件的字节打到标准输出。**除正文之外什么都不打**，
因此可以整份接进管道。

取一条不存在的路径与"技能不存在"是两个不同的结论：技能在，是那条路径不在。`,
		Args: exactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, svc, _, done, err := skillCall(0)
			if err != nil {
				return err
			}
			defer done()

			resp, err := svc.GetSkillFile(ctx, connect.NewRequest(&skillv1.GetSkillFileRequest{
				SkillId: args[0],
				Path:    args[1],
			}))
			if err != nil {
				return err
			}
			if flags.output == "json" {
				return printJSON(resp.Msg)
			}
			// 正文原样出去：不加换行、不加任何提示。
			printf(cmd.OutOrStdout(), "%s", resp.Msg.GetContent())
			return nil
		},
	}
	requirePermission(cmd, rbac.PermissionSkillCatalogRead)
	return cmd
}

func newSkillVersionsCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "versions <技能标识>",
		Short: "列出技能的版本",
		Long: `列出这个技能的版本，最新的在前。

版本**不删**：同步产生新版本、回滚切指针，因此这个列表只增不减，` + "`当前`" + `
标出目录对外服务的那一份。`,
		Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, svc, _, done, err := skillCall(0)
			if err != nil {
				return err
			}
			defer done()

			resp, err := svc.ListSkillVersions(ctx, connect.NewRequest(&skillv1.ListSkillVersionsRequest{
				SkillId: args[0],
			}))
			if err != nil {
				return err
			}
			if flags.output == "json" {
				return printJSON(resp.Msg)
			}
			for _, version := range resp.Msg.GetVersions() {
				marker := "  "
				if version.GetCurrent() {
					marker = "* "
				}
				printf(cmd.OutOrStdout(), "%s%s  %s  %d 个文件  %s\n",
					marker, version.GetId(), shortCommit(version.GetCommit()),
					version.GetFileCount(), version.GetCreatedAt())
				if version.GetSkippedFiles() > 0 {
					printf(cmd.OutOrStdout(), "      上游另有 %d 个条目未收（非文本）\n",
						version.GetSkippedFiles())
				}
			}
			return nil
		},
	}
	requirePermission(cmd, rbac.PermissionSkillCatalogRead)
	return cmd
}

func newSkillFavoriteCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "favorite <技能标识>",
		Short: "收藏一个技能",
		Long: `收藏是**你自己的标记**：它不改变目录内容、不影响别人，因此不需要写权限。

它与 unfavorite 都是幂等的：收藏已经收藏过的不报错，取消没收藏过的也不报错。`,
		Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return setSkillFavorite(cmd, args[0], true)
		},
	}
	requirePermission(cmd, rbac.PermissionSkillCatalogRead)
	return cmd
}

func newSkillUnfavoriteCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "unfavorite <技能标识>",
		Short: "取消收藏",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return setSkillFavorite(cmd, args[0], false)
		},
	}
	requirePermission(cmd, rbac.PermissionSkillCatalogRead)
	return cmd
}

// setSkillFavorite 是收藏与取消共用的实现。
//
// 两个方向一条实现：只授一半的能力（能收藏、收不回来）是一个可以造出来的状态，
// 而它没有任何用处。
func setSkillFavorite(cmd *cobra.Command, skillID string, favorited bool) error {
	ctx, svc, _, done, err := skillCall(0)
	if err != nil {
		return err
	}
	defer done()

	if _, err := svc.SetSkillFavorite(ctx, connect.NewRequest(&skillv1.SetSkillFavoriteRequest{
		SkillId:   skillID,
		Favorited: favorited,
	})); err != nil {
		return err
	}
	if flags.output == "json" {
		return printJSON(map[string]any{"skill_id": skillID, "favorited": favorited})
	}
	if favorited {
		println(cmd.OutOrStdout(), "已收藏。")
	} else {
		println(cmd.OutOrStdout(), "已取消收藏。")
	}
	return nil
}

func newSkillAddCommand() *cobra.Command {
	var ref, subPath, title, summary, coverPath string
	var tags, imagePaths []string

	cmd := &cobra.Command{
		Use:   "add <仓库地址>",
		Short: "从远端纳管一个技能",
		Long: `从 GitHub 纳管一个技能。

**地址只接受 https://github.com/<owner>/<repo> 的形状**，取字节的地址由服务端
自己拼。网页上看来的地址（带 /tree/… 那一段）会被拒，请把引用与子路径拆成
--ref 与 --path。

包契约最小集：根目录要有 SKILL.md，它的 frontmatter 里 name 与 description 都
必填（description 就是"这个技能什么时候该被用上"）；其余文件必须是文本。上限
是 200 个文件、单文件 256 KiB、合计 2 MiB。

**校验失败不留任何痕迹**：库里没有新行，桶上也没有为它写的新对象。

**这条命令的耗时随仓库里的文件数增长**（取回是"1 + 文件数"次远端请求），因此它
带一个比别的命令长得多的内置默认超时。要更短或更长就用 --timeout；显式给出的一律
优先。超时后的重试是安全的——失败本就不留痕迹。

--image 给一条**包内**的图片路径（如 examples/cover.png），可以重复；**出现顺序
就是图集顺序，第一张即卡片封面**。它们会被逐张取回来存成技能的展示图，**不进文件
清单**——技能包只收文本，展示图是说明层的一项；因此这些路径多半正是"被跳过的那类
二进制"，那不影响它们被取回来当展示图。取不到、张数或合计超限就是整次纳管失败，
不会留下一个"只是没有图"的技能。上限是 12 张、单张 2 MiB、合计 12 MiB。

--cover 是旧名，等价于把那一张放在 --image 的最前面（也就是首图）。`,
		Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, _, admin, done, err := skillCall(config.DefaultSkillCatalogTimeout)
			if err != nil {
				return err
			}
			defer done()

			// --cover 给的那一张**排在第一位**：旧名说的是"封面"，而封面就是首图。
			images := imagePaths
			if coverPath != "" {
				images = append([]string{coverPath}, imagePaths...)
			}
			resp, err := admin.ImportSkill(ctx, connect.NewRequest(&skillv1.ImportSkillRequest{
				RepositoryUrl: args[0],
				Ref:           ref,
				SubPath:       subPath,
				Title:         title,
				Summary:       summary,
				Tags:          tags,
				ImagePaths:    images,
			}))
			if err != nil {
				return err
			}
			if flags.output == "json" {
				return printJSON(resp.Msg)
			}
			printSkillDetail(cmd, resp.Msg.GetSkill())
			return nil
		},
	}
	cmd.Flags().StringVar(&ref, "ref", "", "分支 / 标签 / 提交（默认分支留空）")
	cmd.Flags().StringVar(&subPath, "path", "", "技能包在仓库里的子路径（仓库根留空）")
	cmd.Flags().StringVar(&title, "title", "", "标题（留空则用 SKILL.md 的 name）")
	cmd.Flags().StringVar(&summary, "summary", "", "简介（留空则用 SKILL.md 的 description）")
	cmd.Flags().StringArrayVar(&tags, "tag", nil, "标签（可重复）")
	cmd.Flags().StringArrayVar(&imagePaths, "image", nil,
		"包内的一张图片路径，作为展示图（可重复，顺序即图集顺序、第一张即封面）")
	cmd.Flags().StringVar(&coverPath, "cover", "",
		"包内的一张图片路径，作为首图（旧名，等价于放在最前面的 --image）")

	requirePermission(cmd, rbac.PermissionSkillCatalogWrite)
	return cmd
}

func newSkillSyncCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sync <技能标识>",
		Short: "追远端：有新提交就产生新版本并生效",
		Long: `解析来源的引用，与已记录的提交比对，有变化则取回、校验、产生新版本并切换
指针。

**远端没有新提交时什么都不做**：不产生版本、不留痕、不动指针——否则"这个技能
上次真的更新是什么时候"会变成一个看不出答案的问题。失败时当前指针也不动。

来源的引用是标签或提交时，同步永远是"没有变化"——那不是缺陷，是这种来源的
应有之义：想持续跟进就用分支。

**这条命令的耗时随仓库里的文件数增长**（有变化时要重新取回整棵树），因此它带一个
比别的命令长得多的内置默认超时。要更短或更长就用 --timeout；显式给出的一律优先。
超时后的重试是安全的——失败时当前指针不动。`,
		Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, _, admin, done, err := skillCall(config.DefaultSkillCatalogTimeout)
			if err != nil {
				return err
			}
			defer done()

			resp, err := admin.ResyncSkill(ctx, connect.NewRequest(&skillv1.ResyncSkillRequest{
				SkillId: args[0],
			}))
			if err != nil {
				return err
			}
			if flags.output == "json" {
				return printJSON(resp.Msg)
			}
			if !resp.Msg.GetChanged() {
				println(cmd.OutOrStdout(), "远端没有新提交，技能保持原样。")
				return nil
			}
			println(cmd.OutOrStdout(), "已同步到新版本：")
			printSkillDetail(cmd, resp.Msg.GetSkill())
			return nil
		},
	}
	requirePermission(cmd, rbac.PermissionSkillCatalogWrite)
	return cmd
}

func newSkillRollbackCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "rollback <技能标识> <版本标识>",
		Short: "把当前指针切到某个已有版本",
		Long: `回滚**只切指针，不改任何字节**：版本不可变，回滚之后读到的就是目标版本那一份。

回滚之后再来一次 sync 会切回远端最新——那不是回滚失效，而是两条语义各说各的：
回滚说的是"现在我对外用哪一份"，同步说的是"追远端"。要停在旧版本上就别同步它。`,
		Args: exactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, _, admin, done, err := skillCall(0)
			if err != nil {
				return err
			}
			defer done()

			resp, err := admin.SetCurrentSkillVersion(ctx, connect.NewRequest(&skillv1.SetCurrentSkillVersionRequest{
				SkillId:   args[0],
				VersionId: args[1],
			}))
			if err != nil {
				return err
			}
			if flags.output == "json" {
				return printJSON(resp.Msg)
			}
			printSkillDetail(cmd, resp.Msg.GetSkill())
			return nil
		},
	}
	requirePermission(cmd, rbac.PermissionSkillCatalogWrite)
	return cmd
}

func newSkillUpdateCommand() *cobra.Command {
	var title, summary string
	var tags []string

	cmd := &cobra.Command{
		Use:   "update <技能标识>",
		Short: "改说明层：标题、简介与标签",
		Long: `改说明层。**它不改内容层任何一项**：当前版本、文件清单与所有字节在改动前后
逐字不变。

请求表达的是**期望的完整状态**：留空就是清空（标题或简介清空即回到 SKILL.md 里
的回退值），标签以给出的集合**整体替换**，不给表示去掉全部标签。因此改一项时要
把另外两项也带上。

改标签用 --tag；不传 --tag 会把标签清空——它和"没改标签"不是一回事。`,
		Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, _, admin, done, err := skillCall(0)
			if err != nil {
				return err
			}
			defer done()

			resp, err := admin.UpdateSkillMetadata(ctx, connect.NewRequest(&skillv1.UpdateSkillMetadataRequest{
				SkillId: args[0],
				Title:   title,
				Summary: summary,
				Tags:    tags,
			}))
			if err != nil {
				return err
			}
			if flags.output == "json" {
				return printJSON(resp.Msg)
			}
			printSkillDetail(cmd, resp.Msg.GetSkill())
			return nil
		},
	}
	cmd.Flags().StringVar(&title, "title", "", "标题（留空即清空，回到 SKILL.md 的 name）")
	cmd.Flags().StringVar(&summary, "summary", "", "简介（留空即清空，回到 description）")
	cmd.Flags().StringArrayVar(&tags, "tag", nil, "标签（整体替换；不给表示清空）")

	requirePermission(cmd, rbac.PermissionSkillCatalogWrite)
	return cmd
}

func newSkillDeleteCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "delete <技能标识>",
		Short: "删除一个技能",
		Long: `删除一个技能：技能、版本、标签、收藏与使用记录一起消失，**不可逆**。

桶上的字节不删：内容对象按摘要全局共享，同一份字节可能正被别的技能引用。`,
		Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, _, admin, done, err := skillCall(0)
			if err != nil {
				return err
			}
			defer done()

			if _, err := admin.DeleteSkill(ctx, connect.NewRequest(&skillv1.DeleteSkillRequest{
				SkillId: args[0],
			})); err != nil {
				return err
			}
			if flags.output == "json" {
				return printJSON(map[string]any{"skill_id": args[0], "deleted": true})
			}
			println(cmd.OutOrStdout(), "已删除。")
			return nil
		},
	}
	requirePermission(cmd, rbac.PermissionSkillCatalogWrite)
	return markDangerous(cmd)
}

// manifestPath 是包契约要求的那份清单文件。
//
// 它在服务端有一处定义（skill.ManifestPath），这里**不引用那个常量**：命令行是
// 独立二进制，它认识的是"SKILL.md 是主入口"这条约定，而不是服务端的内部取值。
const manifestPath = "SKILL.md"

// printSkillSummary 打一条技能的摘要（列表用）。
func printSkillSummary(cmd *cobra.Command, item *skillv1.Skill) {
	favorite := "  "
	if item.GetFavorited() {
		favorite = "* "
	}
	printf(cmd.OutOrStdout(), "%s%s  %s\n", favorite, item.GetId(), item.GetTitle())
	if summary := item.GetSummary(); summary != "" {
		printf(cmd.OutOrStdout(), "    %s\n", summary)
	}
	if tags := item.GetTags(); len(tags) > 0 {
		printf(cmd.OutOrStdout(), "    标签：%s\n", strings.Join(tags, "、"))
	}
	usage := item.GetUsage()
	if usage.GetUseDays() > 0 {
		printf(cmd.OutOrStdout(), "    最近 30 天：%d 个人日 / %d 人\n",
			usage.GetUseDays(), usage.GetUserCount())
	}
}

// printSkillDetail 打一个技能的详情（维护面与 get 共用）。
func printSkillDetail(cmd *cobra.Command, item *skillv1.Skill) {
	printf(cmd.OutOrStdout(), "%s  %s\n", item.GetId(), item.GetTitle())
	if summary := item.GetSummary(); summary != "" {
		printf(cmd.OutOrStdout(), "%s\n", summary)
	}
	if tags := item.GetTags(); len(tags) > 0 {
		printf(cmd.OutOrStdout(), "标签：%s\n", strings.Join(tags, "、"))
	}
	source := item.GetSource()
	if source.GetRepositoryUrl() != "" {
		where := source.GetRepositoryUrl()
		if source.GetRef() != "" {
			where += "@" + source.GetRef()
		}
		if source.GetSubPath() != "" {
			where += "/" + source.GetSubPath()
		}
		printf(cmd.OutOrStdout(), "来源：%s（%s）\n", where, shortCommit(source.GetCommit()))
	}
	printf(cmd.OutOrStdout(), "当前版本：%s（%d 个文件，%d 字节）\n",
		item.GetCurrentVersionId(), item.GetFileCount(), item.GetTotalBytes())
	for _, file := range item.GetFiles() {
		printf(cmd.OutOrStdout(), "  %s  %d 字节\n", file.GetPath(), file.GetSizeBytes())
	}
	// 列表只给首图，因此这一段只在详情、以及各条写入的返回里出现。
	if images := item.GetImages(); len(images) > 0 {
		printf(cmd.OutOrStdout(), "展示图：%d 张（第一张即卡片封面）\n", len(images))
	}
}

// shortCommit 把提交标识截短：它在界面上是用来对账的，不是用来复制的。
func shortCommit(commit string) string {
	if len(commit) > 12 {
		return commit[:12]
	}
	return commit
}
