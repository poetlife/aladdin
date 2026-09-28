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
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".gif":  "image/gif",
	".webp": "image/webp",
	".mp4":  "video/mp4",
	".webm": "video/webm",
	".mp3":  "audio/mpeg",
	".wav":  "audio/wave",
	".ogg":  "application/ogg",
}

// inferAssetType 由文件路径推断要声明的类型。
//
// 推断不出来时**报用法错误**，不替用户猜一个：猜错的表现是"我传的是 PNG、
// 服务端记成了别的"，而那时它可能已经发布出去了。
func inferAssetType(path string) (string, error) {
	if mediaType, ok := assetTypeByExtension[strings.ToLower(filepath.Ext(path))]; ok {
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

func newGalaxyAssetCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "asset",
		Short: "资产库",
		Long: `工程资产库里的媒体文件：图片、视频、音频。

字节在对象存储，库内是元数据。资产一旦上传，字节不再变化——要换一张图就
重新传一个，再改正文里的引用。被任一版本引用着的资产不能删。`,
	}
	cmd.AddCommand(
		newGalaxyAssetListCommand(),
		newGalaxyAssetUploadCommand(),
		newGalaxyAssetDeleteCommand(),
	)
	return cmd
}

func newGalaxyAssetListCommand() *cobra.Command {
	return requirePermission(&cobra.Command{
		Use:   "list <工程标识>",
		Short: "列出工程的资产",
		Long: `列出工程资产库里的资产，含短时有效的读取地址。

那个地址在有效期内谁拿到都能用，因此未发布的资产里不得承载秘密。过期后
重新执行本命令即得到新地址。`,
		Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, svc, done, err := galaxyCall()
			if err != nil {
				return err
			}
			defer done()

			resp, err := svc.ListAssets(ctx, connect.NewRequest(&galaxyv1.ListAssetsRequest{
				ProjectId: args[0],
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
					a.GetId(), a.GetMediaType(), a.GetSizeBytes(), a.GetFilename())
				if a.GetUrl() != "" {
					printf(cmd.OutOrStdout(), "  地址: %s\n", a.GetUrl())
				}
			}
			return nil
		},
	}, rbac.PermissionGalaxyAssetRead)
}

func newGalaxyAssetUploadCommand() *cobra.Command {
	var contentType string

	cmd := &cobra.Command{
		Use:   "upload <工程标识> <文件路径>",
		Short: "上传一个资产",
		Long: `把一个文件传进工程的资产库。

字节不经过服务端：服务端分配资产标识并签发一份短时、只允许写、只对那一个
键有效的凭证，本命令用那份凭证把字节直接传给对象存储，随后提交核对。

声明的类型决定此后下发时回给浏览器的内容类型，因此它必须是服务端白名单里的
一项。默认按文件扩展名推断，也可以用 --content-type 显式给出；推断不出来时会
报用法错误，不会替你猜一个。`,
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
			c, err := newClient()
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
			}))
			if err != nil {
				return err
			}
			if flags.output == "json" {
				return printJSON(commit.Msg)
			}
			a := commit.Msg.GetAsset()
			printf(cmd.OutOrStdout(), "已上传 %s  %s  %d 字节\n",
				a.GetId(), a.GetMediaType(), a.GetSizeBytes())
			return nil
		},
	}
	cmd.Flags().StringVar(&contentType, "content-type", "",
		"声明的内容类型（缺省按文件扩展名推断，必须是服务端白名单里的一项）")

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
