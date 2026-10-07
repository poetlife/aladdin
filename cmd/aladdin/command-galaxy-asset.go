package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	galaxyv1 "github.com/poetlife/aladdin/api/gen/aladdin/galaxy/v1"
	"github.com/poetlife/aladdin/api/gen/aladdin/galaxy/v1/galaxyv1connect"
	"github.com/poetlife/aladdin/internal/galaxy"
	"github.com/poetlife/aladdin/internal/rbac"
	"github.com/poetlife/aladdin/pkg/client"
)

// assetTypeByExtension 是扩展名到**声明类型**的缺省对照表。
//
// 浏览器能直接给出 File.type，终端不能，因此这里给一份缺省。它是**省事的缺省
// 而不是权威**：能不能作为资产由服务端那个唯一的白名单入口判定（见
// docs/design/galaxy/cli.md）。表里的取值必须与服务端白名单逐条一致，由单元
// 测试拿那个入口逐个验证——白名单一旦变动，测试先失败，而不是等用户撞上
// "服务端拒绝了命令行自己猜的类型"。
//
// 不查 Go 标准库的 mime.TypeByExtension：它给 .wav / .ogg 的取值不在白名单里
// （audio/x-wav / audio/ogg），而服务端收的是 audio/wave 与 application/ogg。
var assetTypeByExtension = map[string]string{
	".png":   "image/png",
	".jpg":   "image/jpeg",
	".jpeg":  "image/jpeg",
	".gif":   "image/gif",
	".webp":  "image/webp",
	".mp4":   "video/mp4",
	".webm":  "video/webm",
	".mp3":   "audio/mpeg",
	".wav":   "audio/wave",
	".ogg":   "application/ogg",
	".woff2": "font/woff2",
	".woff":  "font/woff",
	".ttf":   "font/ttf",
	".otf":   "font/otf",
}

// assetTypeForPath 由文件组路径查它的**声明类型**（唯一入口）。
//
// 判据只有扩展名：文件组的路径由目录层级给出来，而类型是它唯一的额外信息。
// 第二个返回值为假表示这个扩展名不在表里——调用方据此报一句明确的话，而不是
// 猜一个类型。
func assetTypeForPath(entryPath string) (string, bool) {
	mediaType, ok := assetTypeByExtension[strings.ToLower(filepath.Ext(entryPath))]
	return mediaType, ok
}

// inferAssetType 由文件路径推断要声明的类型。
//
// 推断不出来时**报用法错误**，不替用户猜一个：猜错的表现是"我传的是 PNG、
// 服务端记成了别的"，而那时它可能已经发布出去了。
func inferAssetType(path string) (string, error) {
	if mediaType, ok := assetTypeForPath(path); ok {
		return mediaType, nil
	}
	return "", usageErrorf(
		"无法由文件 %s 的扩展名推断内容类型，请用 --content-type 显式给出", filepath.Base(path))
}

// resolveAssetType 定出这次上传要声明的类型（唯一入口）。
//
// 显式给出的优先，否则按扩展名推断。推断的取值只是缺省，服务端的白名单仍是
// 唯一的权威：这里不做归一，也不替用户改写成"看起来更对"的取值。
func resolveAssetType(declared, path string) (string, error) {
	if declared != "" {
		return declared, nil
	}
	return inferAssetType(path)
}

// findAsset 在资产清单里按标识找一个。
//
// 没有"单读一个资产"的接口，而资产清单本来就不大，因此 update 命令从清单里找。
func findAsset(assets []*galaxyv1.Asset, assetID string) *galaxyv1.Asset {
	for _, a := range assets {
		if a.GetId() == assetID {
			return a
		}
	}
	return nil
}

// assetLabel 是资产在人读输出里的名字：有标题就用标题，没有回退到文件名。
//
// 与网页端同一条规则（见 docs/design/galaxy/asset-library.md）：标题是给人看
// 的，文件名只是它缺失时的兜底。
func assetLabel(a *galaxyv1.Asset) string {
	if a.GetTitle() != "" {
		return a.GetTitle()
	}
	if a.GetFilename() == "" {
		return "(未命名)"
	}
	return a.GetFilename()
}

// oneLineSummary 把一段可能多行的文本压成一行，供列表里的备注摘要展示。
//
// 它只用于**展示**：连着的空白折成一个空格，超过上限时截断并加一个省略号，
// 让"这里还有内容"看得出来。备注原文不被改写。
func oneLineSummary(text string, limit int) string {
	text = strings.Join(strings.Fields(text), " ")
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit]) + "…"
}

func newGalaxyAssetCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "asset",
		Short: "资产库",
		Long: `工程资产库里的媒体文件：图片、视频、音频。

字节在对象存储，库内是元数据。资产一旦上传，字节不再变化——要换一张图就
重新传一个，再改正文里的引用。但**说明层**（展示标题、标签、备注）是可改的，
用 asset update 改它，改它不影响已发布的页面。被任一版本引用着的资产不能删。`,
	}
	cmd.AddCommand(
		newGalaxyAssetListCommand(),
		newGalaxyAssetUploadCommand(),
		newGalaxyAssetUpdateCommand(),
		newGalaxyAssetDeleteCommand(),
	)
	return cmd
}

func newGalaxyAssetListCommand() *cobra.Command {
	var tags []string

	cmd := &cobra.Command{
		Use:   "list <工程标识>",
		Short: "列出工程的资产",
		Long: `列出工程资产库里的资产，含短时有效的读取地址。

那个地址在有效期内谁拿到都能用，因此未发布的资产里不得承载秘密。过期后
重新执行本命令即得到新地址。

可用 --tag 按标签筛选；给多个时取**交集**（同时带这些标签的才列出）。`,
		Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, svc, done, err := galaxyCall()
			if err != nil {
				return err
			}
			defer done()

			resp, err := svc.ListAssets(ctx, connect.NewRequest(&galaxyv1.ListAssetsRequest{
				ProjectId: args[0],
				Tags:      tags,
			}))
			if err != nil {
				return err
			}
			if flags.output == "json" {
				return printJSON(resp.Msg)
			}
			assets := resp.Msg.GetAssets()
			if len(assets) == 0 {
				println(cmd.OutOrStdout(), "资产库是空的。")
				return nil
			}
			for _, a := range assets {
				printf(cmd.OutOrStdout(), "%s  %s  %d 字节  %s\n",
					a.GetId(), a.GetMediaType(), a.GetSizeBytes(), assetLabel(a))
				if a.GetTitle() != "" {
					printf(cmd.OutOrStdout(), "  文件名: %s\n", a.GetFilename())
				}
				if len(a.GetTags()) > 0 {
					printf(cmd.OutOrStdout(), "  标签: %s\n", strings.Join(a.GetTags(), ", "))
				}
				if a.GetNotes() != "" {
					printf(cmd.OutOrStdout(), "  备注: %s\n", oneLineSummary(a.GetNotes(), 80))
				}
				if a.GetUrl() != "" {
					printf(cmd.OutOrStdout(), "  地址: %s\n", a.GetUrl())
				}
			}
			return nil
		},
	}
	cmd.Flags().StringArrayVar(&tags, "tag", nil,
		"按标签筛选，可重复；给多个时取交集（同时带这些标签的才列出）")

	requirePermission(cmd, rbac.PermissionGalaxyAssetRead)
	return cmd
}

func newGalaxyAssetUploadCommand() *cobra.Command {
	var contentType, title, notes string
	var tags []string

	cmd := &cobra.Command{
		Use:   "upload <工程标识> <文件路径>",
		Short: "上传一个资产",
		Long: `把一个文件传进工程的资产库。

字节不经过服务端：服务端分配资产标识并签发一份短时、只允许写、只对那一个
键有效的凭证，本命令用那份凭证把字节直接传给对象存储，随后提交核对。

声明的类型决定此后下发时回给浏览器的内容类型，因此它必须是服务端白名单里的
一项。默认按文件扩展名推断，也可以用 --content-type 显式给出；推断不出来时会
报用法错误，不会替你猜一个。

可以用 --title / --tag / --notes 顺带带上初始的说明层元数据，也可以之后再
用 asset update 改；两者结果相同。标签的归一化（去空白、转小写、去重、长度与
数量上限）由服务端做。`,
		Args: exactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			projectID, path := args[0], args[1]
			data, err := os.ReadFile(path) //nolint:gosec // 路径来自调用者自己的命令行参数，读的是他自己的文件
			if err != nil {
				return usageErrorf("读取文件失败: %s", err)
			}
			declaredType, err := resolveAssetType(contentType, path)
			if err != nil {
				return err
			}

			// 这里**不用 galaxyCall 那一个 context**：签发与提交是两次调用，
			// 中间夹着一次不计入 RPC 超时的字节传输。共用一个 30 秒的 deadline
			// 会让一份稍大的资产在提交时必然拿到过期的 context——而那时字节
			// 已经传完了，用户看到的却是一次"超时"。
			c, err := newClient(0)
			if err != nil {
				return err
			}
			defer func() { _ = c.Close() }()
			svc := client.NewService(c, galaxyv1connect.NewGalaxyServiceClient)

			beginCtx, cancelBegin := c.Context()
			begin, err := svc.BeginAssetUpload(beginCtx, connect.NewRequest(&galaxyv1.BeginAssetUploadRequest{
				ProjectId:   projectID,
				ContentType: declaredType,
				SizeBytes:   uint64(len(data)),
			}))
			cancelBegin()
			if err != nil {
				return err
			}

			if err := directUpload(begin.Msg.GetUpload(), data, declaredType); err != nil {
				return err
			}

			commitCtx, cancelCommit := c.Context()
			defer cancelCommit()
			commit, err := svc.CommitAssetUpload(commitCtx, connect.NewRequest(&galaxyv1.CommitAssetUploadRequest{
				ProjectId:   projectID,
				AssetId:     begin.Msg.GetAssetId(),
				ContentType: declaredType,
				// 摘要由服务端那一个入口算：它是公开区地址的键，上架时会被
				// 读回字节核对，因此必须与服务端算的是同一个值。
				Digest:   galaxy.ContentDigest(data),
				Filename: filepath.Base(path),
				Title:    title,
				Tags:     tags,
				Notes:    notes,
			}))
			if err != nil {
				return err
			}
			if flags.output == "json" {
				return printJSON(commit.Msg)
			}
			a := commit.Msg.GetAsset()
			printf(cmd.OutOrStdout(), "已上传 %s  %s  %d 字节  %s\n",
				a.GetId(), a.GetMediaType(), a.GetSizeBytes(), assetLabel(a))
			return nil
		},
	}
	cmd.Flags().StringVar(&contentType, "content-type", "",
		"声明的内容类型（缺省按文件扩展名推断，必须是服务端白名单里的一项）")
	cmd.Flags().StringVar(&title, "title", "", "展示标题（可留空，之后再改）")
	cmd.Flags().StringArrayVar(&tags, "tag", nil, "标签，可重复；可留空，之后再改")
	cmd.Flags().StringVar(&notes, "notes", "", "备注（可留空，之后再改）")

	requirePermission(cmd, rbac.PermissionGalaxyAssetWrite)
	return cmd
}

func newGalaxyAssetUpdateCommand() *cobra.Command {
	var title, notes string
	var tags []string
	var clearTags bool

	cmd := &cobra.Command{
		Use:   "update <工程标识> <资产标识>",
		Short: "改资产的标题、标签与备注",
		Long: `改一个资产的展示标题、标签与备注。

只改你显式给出的那一项：没给的标志保持原值，给出空串才是清空。因此
"--title 新标题" 不会顺手把标签与备注抹掉。标签整组替换：给出的 --tag 就是
全部标签，要用 --clear-tags 清空。

**改的是说明层，不是字节。** 内容摘要、媒体类型与字节数在改动前后逐字不变，
已发布页面的外观也不会因此变化。要换图就重新上传一个，再改引用它的地方。

标签的归一化（去空白、转小写、去重、长度与数量上限）由服务端做。

至少要给出 --title、--tag / --clear-tags 与 --notes 之一。`,
		Args: exactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			titleGiven := cmd.Flags().Changed("title")
			tagsGiven := cmd.Flags().Changed("tag") || clearTags
			notesGiven := cmd.Flags().Changed("notes")
			if !titleGiven && !tagsGiven && !notesGiven {
				return usageErrorf(
					"至少要给出 --title、--tag / --clear-tags 或 --notes 之一；三项都不给表示这次调用没有任何改动")
			}

			ctx, svc, done, err := galaxyCall()
			if err != nil {
				return err
			}
			defer done()

			projectID, assetID := args[0], args[1]
			// 请求表达的是**期望的完整状态**，因此先把当前值取回来，再用显式
			// 给出的那一项覆盖它——否则"只改标题"会顺带清空标签与备注。
			list, err := svc.ListAssets(ctx, connect.NewRequest(&galaxyv1.ListAssetsRequest{
				ProjectId: projectID,
			}))
			if err != nil {
				return err
			}
			current := findAsset(list.Msg.GetAssets(), assetID)
			if current == nil {
				return usageErrorf("工程 %s 下没有资产 %s", projectID, assetID)
			}
			next := &galaxyv1.UpdateAssetRequest{
				ProjectId: projectID,
				AssetId:   assetID,
				Title:     current.GetTitle(),
				Tags:      current.GetTags(),
				Notes:     current.GetNotes(),
			}
			if titleGiven {
				next.Title = title
			}
			if tagsGiven {
				next.Tags = tags
			}
			if notesGiven {
				next.Notes = notes
			}

			resp, err := svc.UpdateAsset(ctx, connect.NewRequest(next))
			if err != nil {
				return err
			}
			if flags.output == "json" {
				return printJSON(resp.Msg)
			}
			a := resp.Msg.GetAsset()
			printf(cmd.OutOrStdout(), "已更新资产 %s  %s  标签: %s\n",
				a.GetId(), assetLabel(a), strings.Join(a.GetTags(), ", "))
			return nil
		},
	}
	cmd.Flags().StringVar(&title, "title", "", "展示标题（给出空串表示清空）")
	cmd.Flags().StringArrayVar(&tags, "tag", nil, "标签，可重复；给出的一整组即全部标签")
	cmd.Flags().BoolVar(&clearTags, "clear-tags", false, "清空全部标签（与 --tag 互斥）")
	cmd.Flags().StringVar(&notes, "notes", "", "备注（给出空串表示清空）")
	cmd.MarkFlagsMutuallyExclusive("tag", "clear-tags")

	requirePermission(cmd, rbac.PermissionGalaxyAssetWrite)
	return cmd
}

func newGalaxyAssetDeleteCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "delete <工程标识> <资产标识>",
		Short: "删除一个资产",
		Long: `删除一个资产：删私有区的对象与元数据行。

被任一版本引用时会被拒绝，错误信息会指出被哪些版本引用——引用了已删除资产
的版本必然在校验阶段失败，把问题挡在它产生的地方。

已发布页面在公开区的副本不因删除私有资产而消失。这是危险操作：交互式下需要
二次确认，非交互式环境下必须显式传入 --yes。`,
		Args: exactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			projectID, assetID := args[0], args[1]
			if err := confirm(fmt.Sprintf(
				"即将删除工程 %q 下的资产 %q，且无法恢复。确认继续？",
				projectID, assetID)); err != nil {
				return err
			}

			ctx, svc, done, err := galaxyCall()
			if err != nil {
				return err
			}
			defer done()

			resp, err := svc.DeleteAsset(ctx, connect.NewRequest(&galaxyv1.DeleteAssetRequest{
				ProjectId: projectID,
				AssetId:   assetID,
			}))
			if err != nil {
				return err
			}
			if flags.output == "json" {
				return printJSON(resp.Msg)
			}
			printf(cmd.OutOrStdout(), "已删除资产 %s\n", assetID)
			return nil
		},
	}
	requirePermission(cmd, rbac.PermissionGalaxyAssetWrite)
	return markDangerous(cmd)
}
