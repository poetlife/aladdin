package main

import (
	"fmt"
	"os"
	"path/filepath"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	galaxyv1 "github.com/poetlife/aladdin/api/gen/aladdin/galaxy/v1"
	"github.com/poetlife/aladdin/api/gen/aladdin/galaxy/v1/galaxyv1connect"
	"github.com/poetlife/aladdin/internal/galaxy"
	"github.com/poetlife/aladdin/internal/objectstore"
	"github.com/poetlife/aladdin/internal/rbac"
	"github.com/poetlife/aladdin/pkg/client"
)

func newGalaxyAttachmentCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "attachment",
		Short: "工程附件",
		Long: `工程里的**发布物**：二进制、zip 包、导出文件。

附件与资产是两件事：资产会被网页引用、由浏览器渲染，类型必须过白名单、发布后
进公开区；附件只给工程成员**下载**，类型不限，**永不进公开区**。下载时响应头由
服务端固定成"强制下载"，因此不管文件是什么，浏览器都只会把它存下来。

字节不可变：要换一份就重新上传一个。可变的是说明（attachment update）。附件可以
**标注**一个版本（"这一份是那一版的构建产物"），而标注不是引用——删掉那个版本时
标注被清空，附件本身不受影响。`,
	}
	cmd.AddCommand(
		newGalaxyAttachmentListCommand(),
		newGalaxyAttachmentUploadCommand(),
		newGalaxyAttachmentUpdateCommand(),
		newGalaxyAttachmentDownloadCommand(),
		newGalaxyAttachmentDeleteCommand(),
	)
	return cmd
}

// findAttachment 在附件清单里按标识找一个。
//
// 没有"单读一份附件"的接口，而附件清单本来就不大，因此 update 与 download 从
// 清单里找。
func findAttachment(attachments []*galaxyv1.Attachment, attachmentID string) *galaxyv1.Attachment {
	for _, attachment := range attachments {
		if attachment.GetId() == attachmentID {
			return attachment
		}
	}
	return nil
}

// attachmentLabel 是附件在人读输出里的名字。
//
// 与资产同一条：文件名是工具生成的，可能很长或不可读，但它就是附件唯一的展示
// 名——附件没有展示标题那一层。
func attachmentLabel(attachment *galaxyv1.Attachment) string {
	if attachment.GetFilename() == "" {
		return "(未命名)"
	}
	return attachment.GetFilename()
}

func newGalaxyAttachmentListCommand() *cobra.Command {
	return requirePermission(&cobra.Command{
		Use:   "list <工程标识>",
		Short: "列出工程的附件",
		Long: `列出工程里的附件，含短时有效的下载地址。

那个地址在有效期内谁拿到都能用，因此**不要把它贴给不该拿到这份文件的人**。
过期后重新执行本命令即得到新地址。`,
		Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, svc, done, err := galaxyCall()
			if err != nil {
				return err
			}
			defer done()

			resp, err := svc.ListAttachments(ctx, connect.NewRequest(&galaxyv1.ListAttachmentsRequest{
				ProjectId: args[0],
			}))
			if err != nil {
				return err
			}
			if flags.output == "json" {
				return printJSON(resp.Msg)
			}
			attachments := resp.Msg.GetAttachments()
			if len(attachments) == 0 {
				println(cmd.OutOrStdout(), "还没有附件。用 `aladdin galaxy attachment upload <工程标识> <文件>` 传一个。")
				return nil
			}
			for _, attachment := range attachments {
				printf(cmd.OutOrStdout(), "%s  %d 字节  %s\n",
					attachment.GetId(), attachment.GetSizeBytes(), attachmentLabel(attachment))
				if attachment.GetVersionId() != "" {
					printf(cmd.OutOrStdout(), "  标注版本: %s\n", attachment.GetVersionId())
				}
				printf(cmd.OutOrStdout(), "  sha256: %s\n", attachment.GetDigest())
				if attachment.GetDescription() != "" {
					printf(cmd.OutOrStdout(), "  说明: %s\n", oneLineSummary(attachment.GetDescription(), 80))
				}
				if attachment.GetDownloadUrl() != "" {
					printf(cmd.OutOrStdout(), "  下载: %s\n", attachment.GetDownloadUrl())
				}
			}
			return nil
		},
	}, rbac.PermissionGalaxyAttachmentRead)
}

func newGalaxyAttachmentUploadCommand() *cobra.Command {
	var versionID, description, slot string
	var alsoSaveVersion bool

	cmd := &cobra.Command{
		Use:   "upload <工程标识> <文件路径>",
		Short: "上传一份附件",
		Long: `把一个文件传进工程的附件库。

字节不经过服务端：服务端分配附件标识并签发一份短时、只允许写、只对那一个键
有效的凭证，本命令用那份凭证把字节直接传给对象存储，随后提交核对。

**类型不限**：zip、二进制、无扩展名的文件都能传。下发时响应头由服务端固定成
强制下载，所以文件名以 .html 结尾也一样只是被存下来，不会被执行。

--version 可以标注一个版本（"这一份是那一版的构建产物"）。标注**不是引用**：
它不拦阻那个版本被删除，删掉时标注被清空。

--save-version 顺带把当前草稿存成一个版本，再把它标到这份附件上：构建产物与
产出它的那一版内容因此对得上，而这是"我到底发的哪一版"最常被问到的地方。
--slot 在单槽工程上可以省略；两个槽都有时必须给出。`,
		Args: exactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			projectID, path := args[0], args[1]
			info, err := os.Stat(path)
			if err != nil {
				return usageErrorf("读取文件失败: %s", err)
			}
			if info.IsDir() {
				return usageErrorf("%s 是一个目录：附件的输入单位是单个文件", path)
			}
			data, err := os.ReadFile(path) //nolint:gosec // 路径来自调用者自己的命令行参数，读的是他自己的文件
			if err != nil {
				return usageErrorf("读取文件失败: %s", err)
			}

			// 这里**不用 galaxyCall 那一个 context**：签发与提交是两次调用，中间
			// 夹着一次不计入 RPC 超时的字节传输。共用一个 30 秒的 deadline 会让
			// 一份稍大的附件在提交时必然拿到过期的 context——而那时字节已经传完
			// 了，用户看到的却是一次"超时"（与 asset upload 同源）。
			c, err := newClient(0)
			if err != nil {
				return err
			}
			defer func() { _ = c.Close() }()
			svc := client.NewService(c, galaxyv1connect.NewGalaxyServiceClient)

			// --save-version 先存版本再标注：反过来会让"没有版本可标"变成一个
			// 中途才发现的失败。另一个槽不受影响。
			markVersionID := versionID
			if alsoSaveVersion {
				saveCtx, cancelSave := c.Context()
				_, slot, err := projectForSlot(saveCtx, svc, cmd, projectID)
				if err != nil {
					cancelSave()
					return err
				}
				saved, err := svc.SaveVersion(saveCtx, connect.NewRequest(&galaxyv1.SaveVersionRequest{
					ProjectId: projectID,
					Slot:      slot,
				}))
				cancelSave()
				if err != nil {
					return err
				}
				markVersionID = saved.Msg.GetVersion().GetId()
				printf(cmd.OutOrStdout(), "已保存版本 #%d（%s）\n",
					saved.Msg.GetVersion().GetSeq(), markVersionID)
			}

			beginCtx, cancelBegin := c.Context()
			begin, err := svc.BeginAttachmentUpload(beginCtx, connect.NewRequest(&galaxyv1.BeginAttachmentUploadRequest{
				ProjectId: projectID,
				VersionId: markVersionID,
				SizeBytes: uint64(len(data)),
			}))
			cancelBegin()
			if err != nil {
				return err
			}

			// 上传声明的是**中性类型**：附件的下发类型与它无关（见 proto 的说明），
			// 策略里那一条规则带的是大小上限。这个取值必须与服务端签发时写进策略的
			// 那一个一致，否则存储侧会因为类型条件对不上而拒绝（见
			// objectstore.NeutralContentType）。
			if err := directUpload(begin.Msg.GetUpload(), data, objectstore.NeutralContentType); err != nil {
				return err
			}

			commitCtx, cancelCommit := c.Context()
			defer cancelCommit()
			commit, err := svc.CommitAttachmentUpload(commitCtx, connect.NewRequest(&galaxyv1.CommitAttachmentUploadRequest{
				ProjectId:    projectID,
				AttachmentId: begin.Msg.GetAttachmentId(),
				VersionId:    markVersionID,
				// 摘要由服务端那一个入口算：提交时会被核对，因此必须与服务端算的
				// 是同一个值。
				Digest:      galaxy.ContentDigest(data),
				Filename:    filepath.Base(path),
				Description: description,
			}))
			if err != nil {
				return err
			}
			if flags.output == "json" {
				return printJSON(commit.Msg)
			}
			attachment := commit.Msg.GetAttachment()
			printf(cmd.OutOrStdout(), "已上传附件 %s  %d 字节  %s\n",
				attachment.GetId(), attachment.GetSizeBytes(), attachmentLabel(attachment))
			printf(cmd.OutOrStdout(), "  sha256: %s\n", attachment.GetDigest())
			return nil
		},
	}
	cmd.Flags().StringVar(&versionID, "version", "", "标注一个版本标识（可留空）")
	cmd.Flags().StringVar(&description, "description", "", "说明（可留空，之后再改）")
	cmd.Flags().BoolVar(&alsoSaveVersion, "save-version", false,
		"先把当前草稿存成一个版本，再把它标到这份附件上")
	// --slot 只在 --save-version 生效时被读到（标注与下载都不按槽分）。
	addSlotFlag(cmd, &slot, "存版本时用哪个内容槽：site 或 docs（单槽工程可省略）")

	requirePermission(cmd, rbac.PermissionGalaxyAttachmentWrite)
	return cmd
}

func newGalaxyAttachmentUpdateCommand() *cobra.Command {
	var description string

	cmd := &cobra.Command{
		Use:   "update <工程标识> <附件标识>",
		Short: "改一份附件的说明",
		Long: `改一份附件的说明。

**它只改说明**：文件名、字节数、内容摘要、标注的版本与对象键在改动前后逐字不变，
因此已经发出去的下载地址指向的还是同一份字节。

给出空串表示清空说明。`,
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

			resp, err := svc.UpdateAttachment(ctx, connect.NewRequest(&galaxyv1.UpdateAttachmentRequest{
				ProjectId:    args[0],
				AttachmentId: args[1],
				Description:  description,
			}))
			if err != nil {
				return err
			}
			if flags.output == "json" {
				return printJSON(resp.Msg)
			}
			printf(cmd.OutOrStdout(), "已更新附件 %s  %s\n",
				args[1], oneLineSummary(resp.Msg.GetAttachment().GetDescription(), 80))
			return nil
		},
	}
	cmd.Flags().StringVar(&description, "description", "", "新的说明（给出空串表示清空）")

	requirePermission(cmd, rbac.PermissionGalaxyAttachmentWrite)
	return cmd
}

func newGalaxyAttachmentDownloadCommand() *cobra.Command {
	var output string

	cmd := &cobra.Command{
		Use:   "download <工程标识> <附件标识> [目标路径]",
		Short: "下载一份附件",
		Long: `下载一份附件到本地。

不给目标路径时写到当前目录，文件名就是上传时的那个（被服务端滤过会破坏响应头的
字符之后的那一个）。

取字节走的是服务端签发的**短时下载地址**，命令行直接对对象存储发起，服务端不
代理字节。`,
		Args: rangeArgs(2, 3),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, svc, done, err := galaxyCall()
			if err != nil {
				return err
			}
			defer done()

			projectID, attachmentID := args[0], args[1]
			list, err := svc.ListAttachments(ctx, connect.NewRequest(&galaxyv1.ListAttachmentsRequest{
				ProjectId: projectID,
			}))
			if err != nil {
				return err
			}
			attachment := findAttachment(list.Msg.GetAttachments(), attachmentID)
			if attachment == nil {
				return usageErrorf("工程 %s 下没有附件 %s", projectID, attachmentID)
			}
			// 地址在这里**重新签发**而不是复用清单里那一条：清单可能是几次调用
			// 之前取的，而地址只有几分钟。
			url := attachment.GetDownloadUrl()
			if url == "" {
				signed, err := svc.GetAttachmentDownloadURL(ctx, connect.NewRequest(&galaxyv1.GetAttachmentDownloadURLRequest{
					ProjectId:    projectID,
					AttachmentId: attachmentID,
				}))
				if err != nil {
					return err
				}
				url = signed.Msg.GetUrl()
			}
			data, err := downloadBytes(url)
			if err != nil {
				return err
			}

			target := output
			if target == "" {
				if len(args) == 3 {
					target = args[2]
				} else {
					target = defaultDownloadName(attachment.GetFilename())
				}
			}
			// 目标是一个已存在的目录时写进去：用户给"下载到哪个目录"是很自然的
			// 一件事，而那不该表现成一次覆盖。
			if info, statErr := os.Stat(target); statErr == nil && info.IsDir() {
				target = filepath.Join(target, defaultDownloadName(attachment.GetFilename()))
			}
			if err := os.WriteFile(target, data, 0o600); err != nil {
				return usageErrorf("写入 %s 失败: %s", target, err)
			}
			printf(cmd.OutOrStdout(), "已写出 %d 字节到 %s\n", len(data), target)
			return nil
		},
	}
	cmd.Flags().StringVarP(&output, "output", "o", "", "写到哪个路径（缺省是当前目录下上传时的文件名）")

	requirePermission(cmd, rbac.PermissionGalaxyAttachmentRead)
	return cmd
}

// defaultDownloadName 定出一份附件缺省的本地文件名。
//
// 服务端滤过的只是**响应头**里那一个名字，库里存的是原始文件名——它可能含路径
// 分隔符、可能只是空、也可能是 `..`。因此这里取它的最后一段，并显式挡掉那几个
// 指向目录本身的取值，而不是把一段不可信输入当路径用。
func defaultDownloadName(filename string) string {
	name := filepath.Base(filepath.FromSlash(filename))
	switch name {
	case "", ".", "..", string(filepath.Separator):
		return "attachment"
	default:
		return name
	}
}

func newGalaxyAttachmentDeleteCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "delete <工程标识> <附件标识>",
		Short: "删除一份附件",
		Long: `删除一份附件：删私有区的对象与元数据行。

**它不被任何引用拦阻**：附件不被文件组引用，版本标注也不构成引用。字节删掉就
没有了，这是危险操作：交互式下需要二次确认，非交互式环境下必须显式传入 --yes。`,
		Args: exactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			projectID, attachmentID := args[0], args[1]
			if err := confirm(fmt.Sprintf(
				"即将删除工程 %q 下的附件 %q，且无法恢复。确认继续？",
				projectID, attachmentID)); err != nil {
				return err
			}

			ctx, svc, done, err := galaxyCall()
			if err != nil {
				return err
			}
			defer done()

			resp, err := svc.DeleteAttachment(ctx, connect.NewRequest(&galaxyv1.DeleteAttachmentRequest{
				ProjectId:    projectID,
				AttachmentId: attachmentID,
			}))
			if err != nil {
				return err
			}
			if flags.output == "json" {
				return printJSON(resp.Msg)
			}
			printf(cmd.OutOrStdout(), "已删除附件 %s\n", attachmentID)
			return nil
		},
	}
	requirePermission(cmd, rbac.PermissionGalaxyAttachmentWrite)
	return markDangerous(cmd)
}
