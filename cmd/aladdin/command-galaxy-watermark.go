package main

import (
	"fmt"
	"image"
	_ "image/gif"  // 注册解码器：截图可能被存成 gif
	_ "image/jpeg" // 同上：截图多半是 png 或 jpeg
	_ "image/png"
	"os"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	galaxyv1 "github.com/poetlife/aladdin/api/gen/aladdin/galaxy/v1"
	"github.com/poetlife/aladdin/internal/rbac"
	"github.com/poetlife/aladdin/internal/watermark"
)

// watermark 是"溯源"那一条路的入口：一段截图先解出码，再按码查回一次访问。
//
// 它被单独成一组而不是挂在 publication 下面：它读的不是发布态，而是一张图。
// 见 docs/design/galaxy/watermark.md。
func newGalaxyWatermarkCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "watermark",
		Short: "从截图里回溯一次访问",
		Long: `从一张截图里解出盲水印，再按它查回那次访问。

发布出来的站点上叠着两层水印（见 docs/design/galaxy/watermark.md）：明水印是给人
看的，盲水印是给这条命令读的。**能对抗的变换有限**：缩放、重编码、带偏移的裁剪都
可以，重新排版或重绘不行——解不出就是解不出，不会返回一个"最像的码"。

查它要 galaxy.watermark.read：让作者查出"谁看了我的页面"是另一条隐私决策。`,
	}
	cmd.AddCommand(newGalaxyWatermarkExtractCommand())
	return cmd
}

func newGalaxyWatermarkExtractCommand() *cobra.Command {
	var codeOnly bool

	cmd := &cobra.Command{
		Use:   "extract <图片文件>",
		Short: "从一张截图里解出水印码，并查回那次访问",
		Long: `读一张图，先解出盲水印的码，再按码查回那次访问。

--code-only 只打印码、不查服务端：取证的第一步是"这张图上有没有码"，而它不需要
任何权限，也不必连得上服务端。

**查不到不是错误的一种特例**：码不存在、超出保留窗口、图里根本没有码，三者都让
这一条以非零状态结束，且给出同一句话——区分它们会把一次查询变成一次"这枚码是不
是真的"的试探。`,
		Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			img, err := readImageFile(args[0])
			if err != nil {
				return err
			}
			code, err := watermark.Extract(img)
			if err != nil {
				return fmt.Errorf("这张图里解不出水印：%w", err)
			}
			if codeOnly {
				printf(cmd.OutOrStdout(), "%s\n", code)
				return nil
			}

			ctx, svc, done, err := galaxyCall()
			if err != nil {
				return err
			}
			defer done()

			resp, err := svc.LookupWatermark(ctx, connect.NewRequest(&galaxyv1.LookupWatermarkRequest{Code: code}))
			if err != nil {
				return err
			}
			visit := resp.Msg.GetVisit()
			if visit == nil {
				return fmt.Errorf("水印 %s 查不到对应记录：超出保留期，或这条记录已经不在了", code)
			}
			printWatermarkVisit(cmd, visit)
			return nil
		},
	}
	cmd.Flags().BoolVar(&codeOnly, "code-only", false, "只打印解出的码，不查服务端")
	// 声明是**静态**的（见 permission-decl.go）：它只用于 --help 与构建期检查，
	// 不是本地放行判断。--code-only 那一档不查服务端、也就不需要这个权限，但声明
	// 仍然写在这里：同一族命令共用一个码，比"按 flag 分档"更不容易漏。
	requirePermission(cmd, rbac.PermissionGalaxyWatermarkRead)
	return cmd
}

// printWatermarkVisit 把一次访问摊成人读的几行。
//
// **主体为空是常态**（发布域是匿名可取用的），因此这一行要说明"这一档是匿名"，
// 而不是印出一个空白让人以为查询出了问题。
func printWatermarkVisit(cmd *cobra.Command, visit *galaxyv1.WatermarkRecord) {
	out := cmd.OutOrStdout()
	who := visit.GetSubjectId()
	if who == "" {
		who = "（匿名访问）"
	}
	// 一行一个字段、标签对齐：这一份是给人对着工单读的，不是给程序解析的
	// （要机器可读用 --output json 的那条约定，见 command-galaxy-publication.go）。
	printf(out, "水印    %s\n", visit.GetCode())
	printf(out, "主体    %s\n", who)
	printf(out, "时间    %s\n", visit.GetVisitedAt())
	printf(out, "工程    %s（%s）\n", visit.GetProjectId(), slotLabel(visit.GetSlot()))
	printf(out, "来源    %s\n", visit.GetSourceAddress())
	printf(out, "浏览器  %s\n", visit.GetUserAgentDigest())
}

// readImageFile 打开并解码一张图（格式由内容嗅探，不看扩展名）。
func readImageFile(path string) (image.Image, error) {
	// 路径来自命令行参数（取证的人自己给的），不是请求输入。
	//nolint:gosec // 路径来自命令行参数
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("打开图片失败: %w", err)
	}
	defer func() { _ = file.Close() }()

	img, _, err := image.Decode(file)
	if err != nil {
		return nil, fmt.Errorf("这不是一张能解码的图片（支持 png / jpeg / gif）: %w", err)
	}
	return img, nil
}
