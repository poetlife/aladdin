package main

import (
	"context"
	"fmt"
	"strings"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	galaxyv1 "github.com/poetlife/aladdin/api/gen/aladdin/galaxy/v1"
	"github.com/poetlife/aladdin/api/gen/aladdin/galaxy/v1/galaxyv1connect"
	"github.com/poetlife/aladdin/internal/galaxy"
	"github.com/poetlife/aladdin/pkg/client"
)

func newGalaxyCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "galaxy",
		Short: "创作与发布",
		Long: `放下一组具名文件与素材，再把它们发布成一个别人能打开的站点。

工程、草稿、版本、资产与发布各自成一条命令，没有"一条命令走完全流程"的形态：
一次发布发的是某一版，而不是"文件现在的样子"（见 docs/design/galaxy/cli.md）。

**目录是整组的输入与输出单位**：draft push 一个目录就是"草稿整组换成它"，目录
里没有的路径就是删掉。**文件组的写入只有命令行这一条路**（网页端只读）。

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
			printf(cmd.OutOrStdout(), "单份文本上限: %d 字节\n", caps.GetMaxTextBytes())
			printf(cmd.OutOrStdout(), "整组内容上限: %d 字节\n", caps.GetMaxFileSetBytes())
			printf(cmd.OutOrStdout(), "文件数上限: %d 个\n", caps.GetMaxFiles())
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
	case galaxyv1.MediaKind_MEDIA_KIND_FONT:
		return "字体"
	default:
		return "未知类别"
	}
}

// slotFlagName 是"针对哪个内容槽"的参数名。
//
// 凡是碰内容或发布状态的命令都带它：一个工程可能两个槽都有，因此"打到哪个槽"
// 必须能显式给出（与"工程标识每次都显式给出"同一条取向）。
const slotFlagName = "slot"

// slotName 返回槽的取值字面（site / docs）。
func slotName(slot galaxyv1.ContentSlot) string {
	switch slot {
	case galaxyv1.ContentSlot_CONTENT_SLOT_SITE:
		return string(galaxy.SlotSite)
	case galaxyv1.ContentSlot_CONTENT_SLOT_DOCS:
		return string(galaxy.SlotDocs)
	default:
		return "未知槽"
	}
}

// slotLabel 是内容槽在命令行上的展示文案。
func slotLabel(slot galaxyv1.ContentSlot) string {
	switch slot {
	case galaxyv1.ContentSlot_CONTENT_SLOT_SITE:
		return "站点（整站文件原样服务）"
	case galaxyv1.ContentSlot_CONTENT_SLOT_DOCS:
		return "文档（markdown 渲染成多页）"
	default:
		return "未知槽"
	}
}

// parseSlotFlag 把 --slot 的取值解析成接口枚举。
func parseSlotFlag(raw string) (galaxyv1.ContentSlot, error) {
	switch raw {
	case string(galaxy.SlotSite):
		return galaxyv1.ContentSlot_CONTENT_SLOT_SITE, nil
	case string(galaxy.SlotDocs):
		return galaxyv1.ContentSlot_CONTENT_SLOT_DOCS, nil
	default:
		return galaxyv1.ContentSlot_CONTENT_SLOT_UNSPECIFIED,
			usageErrorf("--slot 必须是 site 或 docs（当前是 %q）", raw)
	}
}

// parseSlotFlags 解析一串 --slot（创建工程时用，可重复）。
func parseSlotFlags(raw []string) ([]galaxyv1.ContentSlot, error) {
	if len(raw) == 0 {
		return nil, usageErrorf("至少要给出一个 --slot（site 或 docs）")
	}
	out := make([]galaxyv1.ContentSlot, 0, len(raw))
	for _, value := range raw {
		slot, err := parseSlotFlag(value)
		if err != nil {
			return nil, err
		}
		out = append(out, slot)
	}
	return out, nil
}

// slotOf 把服务端给出的槽翻译成领域取值。
//
// 它只服务命令行这一侧的分类需求（决定一个文件走内容对象还是资产上传），
// 判定权威仍在服务端。
func slotOf(slot galaxyv1.ContentSlot) (galaxy.ContentSlot, error) {
	switch slot {
	case galaxyv1.ContentSlot_CONTENT_SLOT_SITE:
		return galaxy.SlotSite, nil
	case galaxyv1.ContentSlot_CONTENT_SLOT_DOCS:
		return galaxy.SlotDocs, nil
	default:
		return "", fmt.Errorf("服务端给出的内容槽无法识别：%s", slot)
	}
}

// addSlotFlag 给一条内容或发布命令加上 --slot。
func addSlotFlag(cmd *cobra.Command, target *string, usage string) {
	cmd.Flags().StringVar(target, slotFlagName, "", usage)
}

// resolveSlot 定出这次调用针对哪个槽（唯一入口）。
//
// **工程只有一个槽时可以省略**：从 project get 读回启用了哪些槽，只有一个就用
// 它。这是省事的缺省，不是判定权威——服务端始终要求显式的槽，缺省只发生在
// 命令行这一侧。**有两个槽时必须显式给出**：省略即报用法错误，因为"我这条命令
// 打到了哪个槽"不该靠一个默认值来回答（与"工程标识每次都显式给出"同一条取向）。
func resolveSlot(cmd *cobra.Command, project *galaxyv1.Project) (galaxyv1.ContentSlot, error) {
	raw, _ := cmd.Flags().GetString(slotFlagName)
	slots := project.GetSlots()
	if raw == "" {
		switch len(slots) {
		case 1:
			return slots[0].GetSlot(), nil
		case 0:
			return galaxyv1.ContentSlot_CONTENT_SLOT_UNSPECIFIED,
				fmt.Errorf("工程 %s 没有启用任何内容槽", project.GetId())
		default:
			return galaxyv1.ContentSlot_CONTENT_SLOT_UNSPECIFIED, usageErrorf(
				"这个工程有两个内容槽（%s），请用 --slot 指定一个", joinedSlotNames(slots))
		}
	}
	return parseSlotFlag(raw)
}

// joinedSlotNames 把工程上的槽列成一句可读的候选。
func joinedSlotNames(slots []*galaxyv1.ProjectSlot) string {
	names := make([]string, 0, len(slots))
	for _, slot := range slots {
		names = append(names, slotName(slot.GetSlot()))
	}
	return strings.Join(names, "、")
}

// printProject 输出一个工程的摘要（text 模式）。
//
// **发布状态按槽逐条给出**：一个工程可以既是站点又有文档，两槽各发各的，因此
// "这个工程发布了没有"这句话在这个形状下没有答案。
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
	if len(p.GetSlots()) == 0 {
		printf(cmd.OutOrStdout(), "  内容槽: （一个都没有）\n")
		return
	}
	for _, slot := range p.GetSlots() {
		printf(cmd.OutOrStdout(), "  %s:\n", slotLabel(slot.GetSlot()))
		if slot.GetBaseUrl() != "" {
			printf(cmd.OutOrStdout(), "    发布根: %s\n", slot.GetBaseUrl())
		}
		if slot.GetPublished() {
			printf(cmd.OutOrStdout(), "    已发布: %s（%s）\n", slot.GetPublishedUrl(), slot.GetPublishedAt())
			continue
		}
		printf(cmd.OutOrStdout(), "    未发布\n")
	}
}
