package main

import (
	"context"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	galaxyv1 "github.com/poetlife/aladdin/api/gen/aladdin/galaxy/v1"
	"github.com/poetlife/aladdin/api/gen/aladdin/galaxy/v1/galaxyv1connect"
	"github.com/poetlife/aladdin/pkg/client"
)

func newGalaxyCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "galaxy",
		Short: "创作与发布",
		Long: `创作一份 HTML 并把它发布成一个别人能打开的页面。

工程、草稿、版本、资产与发布各自成一条命令，没有"一条命令走完全流程"的形态：
一次发布发的是某一版，而不是"文件现在的样子"（见 docs/design/galaxy/cli.md）。

工程标识由服务端分配且不可猜，每次都显式给出——本地不保存"当前工程"。`,
	}
	cmd.AddCommand(
		newGalaxyCapabilitiesCommand(),
		newGalaxyProjectCommand(),
		newGalaxyDraftCommand(),
		newGalaxyVersionCommand(),
		newGalaxyValidateCommand(),
		newGalaxyAssetCommand(),
		newGalaxyPublishCommand(),
		newGalaxyUnpublishCommand(),
	)
	return cmd
}

// galaxyCall 为一次 galaxy 调用准备好客户端与 context。
//
// 凭证注入、超时与连接关闭对每条命令都一样，因此收在一处：漏掉其中任何一件的
// 表现都是"这一条命令与别的不一样"，而那正是最难从现象反推的一类问题。
func galaxyCall() (context.Context, galaxyv1connect.GalaxyServiceClient, func(), error) {
	c, err := newClient()
	if err != nil {
		return nil, nil, nil, err
	}
	ctx, cancel := c.Context()
	svc := client.NewService(c, galaxyv1connect.NewGalaxyServiceClient)
	return ctx, svc, func() {
		cancel()
		_ = c.Close()
	}, nil
}

func newGalaxyCapabilitiesCommand() *cobra.Command {
	return markAuthenticatedOnly(&cobra.Command{
		Use:   "capabilities",
		Short: "读取本部署下创作能力的边界",
		Long: `打印这个部署能做什么：素材能不能传、能不能发布、各项上限。

它是部署形态的公开事实，不因调用者而异。命令本身不拿它做任何前置判断——
能力由服务端在每一次调用里判定。`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, svc, done, err := galaxyCall()
			if err != nil {
				return err
			}
			defer done()

			resp, err := svc.GetCapabilities(ctx, connect.NewRequest(&galaxyv1.GetCapabilitiesRequest{}))
			if err != nil {
				return err
			}
			if flags.output == "json" {
				return printJSON(resp.Msg)
			}

			caps := resp.Msg.GetCapabilities()
			printf(cmd.OutOrStdout(), "素材上传: %s\n", yesNo(caps.GetAssetUploadEnabled()))
			printf(cmd.OutOrStdout(), "发布: %s\n", yesNo(caps.GetPublishEnabled()))
			printf(cmd.OutOrStdout(), "正文上限: %d 字节\n", caps.GetMaxDocumentBytes())
			printf(cmd.OutOrStdout(), "发布产物上限: %d 字节\n", caps.GetMaxArtifactBytes())
			for _, limit := range caps.GetAssetLimits() {
				printf(cmd.OutOrStdout(), "%s上限: %d 字节\n",
					mediaKindLabel(limit.GetKind()), limit.GetMaxBytes())
			}
			return nil
		},
	})
}

// yesNo 把能力下发的布尔项写成一行可读文本。
func yesNo(enabled bool) string {
	if enabled {
		return "可用"
	}
	return "不可用"
}

// mediaKindLabel 是资产类别在命令行上的展示文案。
func mediaKindLabel(kind galaxyv1.MediaKind) string {
	switch kind {
	case galaxyv1.MediaKind_MEDIA_KIND_IMAGE:
		return "图片"
	case galaxyv1.MediaKind_MEDIA_KIND_VIDEO:
		return "视频"
	case galaxyv1.MediaKind_MEDIA_KIND_AUDIO:
		return "音频"
	default:
		return "未知类别"
	}
}

// printProject 输出一个工程的摘要（text 模式）。
func printProject(cmd *cobra.Command, p *galaxyv1.Project) {
	name := p.GetName()
	if name == "" {
		// 名称可以留空，但输出里留一片空白看起来像坏了。
		name = "(未命名)"
	}
	printf(cmd.OutOrStdout(), "%s  %s\n", p.GetId(), name)
	if p.GetDescription() != "" {
		printf(cmd.OutOrStdout(), "  简介: %s\n", p.GetDescription())
	}
	if p.GetPublished() {
		printf(cmd.OutOrStdout(), "  已发布: %s（%s）\n", p.GetPublishedUrl(), p.GetPublishedAt())
		return
	}
	printf(cmd.OutOrStdout(), "  未发布\n")
}
