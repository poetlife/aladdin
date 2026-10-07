package main

import (
	"context"
	"fmt"
	"math"
	"net/http"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	galaxyv1 "github.com/poetlife/aladdin/api/gen/aladdin/galaxy/v1"
	"github.com/poetlife/aladdin/internal/galaxy"
	"github.com/poetlife/aladdin/internal/rbac"
)

// publication 是"回读发布态"这一组命令。
//
// publish 的返回值只说"发布成功了"：产物里的地址是**渲染时**补上的（源里、版本里
// 都只有 `asset://` 记号），因此它证明不了那些引用都解开了。这一组命令按**访客走
// 的那条地址**把产物取回来，"发布成功了"因此变成一个可核对的事实（见
// docs/design/galaxy/publication.md 的"回读发布态"）。
func newGalaxyPublicationCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "publication",
		Short: "回读发布态",
		Long: `读回**当前发布出去**的那一份产物。

publish 的返回值只说"发布成功了"，它证明不了产物里每一处引用都解开了——真实地址
是渲染时补上的，源里、版本里都只有记号。这里的三条命令按访客走的那条地址把产物
取回来：

  get     读回发布记录与产物清单（--path 取其中一份的字节）
  pull    把整份发布物写到本地目录
  verify  逐条复核：字节与发布记录一致、引用都落在产物清单上、资产可达

它们要求能访问**发布域**——那正是访客走的那条路，而不只是主站地址。`,
	}
	cmd.AddCommand(
		newGalaxyPublicationGetCommand(),
		newGalaxyPublicationPullCommand(),
		newGalaxyPublicationVerifyCommand(),
	)
	return cmd
}

func newGalaxyPublicationGetCommand() *cobra.Command {
	var slot, entryPath string

	cmd := &cobra.Command{
		Use:   "get <工程标识>",
		Short: "读回某个槽当前发布的产物清单",
		Long: `读回某个内容槽**当前发布**的那一份产物清单：每一项是产物路径（docs 槽的
markdown 在产物里是 .html）加上它在发布域上的那条地址。

--path 给出其中一份时输出它的字节：取的是**访客走的地址**，因此读到的是访客拿到
的东西，而不是"我推上去的那份源"（要那个用 version pull）。

未发布时这一条以非零状态结束——没有产物可读，用 publish 发一版。`,
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
			resp, err := svc.GetPublication(ctx, connect.NewRequest(&galaxyv1.GetPublicationRequest{
				ProjectId: args[0],
				Slot:      resolved,
			}))
			if err != nil {
				return err
			}
			if resp.Msg.GetPublication().GetId() == "" {
				return fmt.Errorf("工程 %s 的%s还没有发布过；用 `galaxy publish` 发一版",
					args[0], slotName(resolved))
			}
			entries := resp.Msg.GetEntries()
			if flags.output == "json" {
				return printJSON(resp.Msg)
			}
			if entryPath == "" {
				printf(cmd.OutOrStdout(), "%s  %s\n", slotName(resolved), resp.Msg.GetPublication().GetUrl())
				for _, entry := range entries {
					printf(cmd.OutOrStdout(), "%s\n", describeEntry(entry))
				}
				return nil
			}
			entry, ok := findEntry(entries, entryPath)
			if !ok {
				// 这里给的是**产物路径**，与 version get --path 的源路径不同：
				// docs 槽的 markdown 在产物里是 .html。差一个后缀是最容易踩的一脚。
				return usageErrorf("发布态里没有 %q：这里给的是产物路径（docs 槽的 markdown 在产物里是 .html）",
					entryPath)
			}
			data, err := fetchEntry(entry)
			if err != nil {
				return err
			}
			_, err = cmd.OutOrStdout().Write(data)
			return err
		},
	}
	cmd.Flags().StringVar(&entryPath, "path", "", "要取的文件在产物里的路径")
	addSlotFlag(cmd, &slot, "读哪个内容槽的发布态：site 或 docs（单槽工程可省略）")
	requirePermission(cmd, rbac.PermissionGalaxyProjectRead)
	return cmd
}

func newGalaxyPublicationPullCommand() *cobra.Command {
	var slot string

	cmd := &cobra.Command{
		Use:   "pull <工程标识> <目录>",
		Short: "把当前发布的产物整组写到本地目录",
		Long: `把某个内容槽**当前发布**的那一份产物整组写到本地目录。

取字节走的是**访客的那条地址**：文本条目由发布域给出，资产条目由它重定向到公开区。
因此这里取回来的是访客拿到的东西——把它与本地目录比一次，就能回答"发布出去的到底
是不是我这一份"。`,
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
			resp, err := svc.GetPublication(ctx, connect.NewRequest(&galaxyv1.GetPublicationRequest{
				ProjectId: args[0],
				Slot:      resolved,
			}))
			if err != nil {
				return err
			}
			if resp.Msg.GetPublication().GetId() == "" {
				return fmt.Errorf("工程 %s 的%s还没有发布过；没有产物可取",
					args[0], slotName(resolved))
			}
			entries := resp.Msg.GetEntries()
			if err := writeFileSet(args[1], entries, fetchEntry); err != nil {
				return err
			}
			printf(cmd.OutOrStdout(), "已写出 %d 个文件到 %s\n", len(entries), args[1])
			return nil
		},
	}
	addSlotFlag(cmd, &slot, "取哪个内容槽的发布态：site 或 docs（单槽工程可省略）")
	requirePermission(cmd, rbac.PermissionGalaxyProjectRead)
	return cmd
}

func newGalaxyPublicationVerifyCommand() *cobra.Command {
	var slot string

	cmd := &cobra.Command{
		Use:   "verify <工程标识>",
		Short: "逐条复核当前发布的产物",
		Long: `按访客走的那条路把当前发布的产物取回来，逐条复核：

  - 每一份文本产物的字节与发布记录里的摘要一致；
  - 产物里没有残留没解开的记号，每一处取资源引用都落在产物清单上；
  - 每一条资产引用的发布态地址都可达（重定向到公开区）。

有问题时逐条列出，并以非零状态退出——好让 "publish && publication verify" 这类
写法成立。**取不回来是另一次失败**（发布域不可达、网络中断），它会以一条明确的
错误结束，而不是伪装成"产物有问题"。`,
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
			resp, err := svc.GetPublication(ctx, connect.NewRequest(&galaxyv1.GetPublicationRequest{
				ProjectId: args[0],
				Slot:      resolved,
			}))
			if err != nil {
				return err
			}
			if resp.Msg.GetPublication().GetId() == "" {
				return fmt.Errorf("工程 %s 的%s还没有发布过；没有可复核的产物",
					args[0], slotName(resolved))
			}
			result, err := verifyPublication(project, resolved, resp.Msg.GetPublication(), resp.Msg.GetEntries())
			if err != nil {
				return err
			}
			if flags.output == "json" {
				if err := printJSON(result); err != nil {
					return err
				}
			} else {
				printVerifyResult(cmd, result)
			}
			if len(result.Problems) > 0 {
				return fmt.Errorf("发布态有 %d 处问题", len(result.Problems))
			}
			return nil
		},
	}
	addSlotFlag(cmd, &slot, "复核哪个内容槽的发布态：site 或 docs（单槽工程可省略）")
	requirePermission(cmd, rbac.PermissionGalaxyProjectRead)
	return cmd
}

// verifyResult 是一次发布态复核的结论。
//
// 它是**命令行的输出形状**，不是接口类型：清单与判定都来自服务端（GetPublication
// 与 internal/galaxy 的产物复核入口），这里只有取字节、核对与排版。
type verifyResult struct {
	Publication   *galaxyv1.Publication `json:"publication"`
	CheckedTexts  int                   `json:"checked_texts"`
	CheckedAssets int                   `json:"checked_assets"`
	// Assets 是每一条资产引用的最终地址与核对结果，供脚本逐条读。
	Assets   []verifyAsset   `json:"assets"`
	Problems []verifyProblem `json:"problems"`
}

// verifyAsset 是一条资产引用的最终地址与核对结果。
type verifyAsset struct {
	Path string `json:"path"`
	// URL 是访客走的那条地址（它会重定向到公开区）。
	URL string `json:"url"`
	// Status 是 "ok" 或一句说明为什么不可达。
	Status string `json:"status"`
}

// verifyProblem 是一处问题。形状与 ValidationProblem 对齐（路径 + 行号 + 话）。
type verifyProblem struct {
	Path    string `json:"path,omitempty"`
	Line    int32  `json:"line,omitempty"`
	Message string `json:"message"`
}

// verifyPublication 按访客路径复核一份发布态产物（唯一入口）。
//
// 三件事：把每一份文本产物取回来核对摘要、对着**同一份产物复核规则**
// （internal/galaxy 的 AuditArtifacts）跑一遍、核对每一条资产引用的发布态地址。
//
// **取不回来是另一次失败**（发布域不可达、网络中断），以 error 返回；"产物有问题"
// 落在问题清单里。两者混起来会让一次网络抖动看起来像内容写错了——与 validate 的
// 取向一致（见 docs/design/galaxy/authoring.md 的"校验未完成"那一档）。
func verifyPublication(project *galaxyv1.Project, slot galaxyv1.ContentSlot, publication *galaxyv1.Publication, entries []*galaxyv1.FileEntry) (verifyResult, error) {
	// 两个清单先建成空切片：脚本读到的"没有问题"应当是 `[]`，不是一个 null。
	result := verifyResult{
		Publication: publication,
		Assets:      []verifyAsset{},
		Problems:    []verifyProblem{},
	}

	// 产物清单里可以命中的全部路径：复核"每一处取资源引用都落在清单上"用的就是它。
	paths := make(map[string]bool, len(entries))
	for _, entry := range entries {
		paths[entry.GetPath()] = true
	}

	artifacts := make(map[string][]byte, len(entries))
	for _, entry := range entries {
		if entry.GetDigest() == "" {
			continue // 资产条目由下面那一档核对，不取字节
		}
		data, err := fetchEntry(entry)
		if err != nil {
			return verifyResult{}, fmt.Errorf("取回发布态产物 %s 失败：%w", entry.GetPath(), err)
		}
		result.CheckedTexts++
		artifacts[entry.GetPath()] = data
		if actual := galaxy.ContentDigest(data); actual != entry.GetDigest() {
			result.Problems = append(result.Problems, verifyProblem{
				Path: entry.GetPath(),
				Message: fmt.Sprintf("取回的字节与发布记录里的摘要不符（记录 %s，实际 %s）",
					entry.GetDigest(), actual),
			})
		}
	}

	// 复核规则只有一处实现：残留的记号、指向别处的引用、落在清单之外的引用都由
	// 它报出来（见 internal/galaxy/artifact_audit.go）。
	for _, problem := range galaxy.AuditArtifacts(siteRootOf(project, slot), paths, artifacts).Problems {
		result.Problems = append(result.Problems, verifyProblem{
			Path:    problem.Path,
			Line:    fitInt32(problem.Line),
			Message: problem.Message,
		})
	}

	for _, entry := range entries {
		if entry.GetAssetId() == "" {
			continue
		}
		status := checkPublishedAsset(entry.GetUrl())
		result.CheckedAssets++
		result.Assets = append(result.Assets, verifyAsset{
			Path:   entry.GetPath(),
			URL:    entry.GetUrl(),
			Status: status,
		})
		if status != assetStatusOK {
			result.Problems = append(result.Problems, verifyProblem{
				Path:    entry.GetPath(),
				Message: "资产引用的发布态地址不可达：" + status,
			})
		}
	}
	return result, nil
}

// assetStatusOK 是一条资产引用核对通过的取值。
const assetStatusOK = "ok"

// checkPublishedAsset 核对一条资产引用的发布态地址：它应当给出一个跳转到公开区的
// 302，而不是 404。
//
// **不跟随重定向**：跟下去会把整份媒体下载一遍，而这里要回答的只是"那一条地址还给
// 不给"。地址由服务端下发（发布域上的槽根接条目路径），不是用户输入。
func checkPublishedAsset(rawURL string) string {
	if rawURL == "" {
		return "发布态地址为空（发布域可能没有配置）"
	}
	//nolint:gosec // 地址来自服务端下发的发布态地址，不是用户输入
	request, err := http.NewRequestWithContext(context.Background(), http.MethodGet, rawURL, nil)
	if err != nil {
		return "发布态地址不是一条可请求的地址"
	}
	client := &http.Client{
		Timeout: downloadTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Do(request)
	if err != nil {
		return "取不到（" + err.Error() + "）"
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusFound {
		return fmt.Sprintf("没有重定向到公开区（HTTP %d）", resp.StatusCode)
	}
	if resp.Header.Get("Location") == "" {
		return "重定向没有目标"
	}
	return assetStatusOK
}

// siteRootOf 取一个槽的**发布根**（形如 /g/<工程标识>/docs/）。
//
// 它取自服务端下发的 base_url——与 `project base` 是同一个值、同一处派生，命令行
// 不自己拼（见 docs/design/galaxy/cli.md）。
func siteRootOf(project *galaxyv1.Project, slot galaxyv1.ContentSlot) string {
	for _, candidate := range project.GetSlots() {
		if candidate.GetSlot() == slot {
			return candidate.GetBaseUrl()
		}
	}
	return ""
}

// printVerifyResult 把一次复核的结论写成几行给人看的话。
//
// 通过时不逐条列出每一条资产引用（机器要的是 --output json 里那份清单）：一份
// 几十张图的文档站，逐条列一遍只会把"有没有问题"埋掉。
func printVerifyResult(cmd *cobra.Command, result verifyResult) {
	printf(cmd.OutOrStdout(), "已发布：%s（版本 %s，%s）\n",
		result.Publication.GetUrl(),
		result.Publication.GetVersionId(),
		result.Publication.GetPublishedAt())
	printf(cmd.OutOrStdout(), "已核对 %d 份文本产物、%d 条资产引用\n",
		result.CheckedTexts, result.CheckedAssets)
	if len(result.Problems) == 0 {
		println(cmd.OutOrStdout(), "无问题：发布态的字节与发布记录一致，引用都解开了。")
		return
	}
	for _, problem := range result.Problems {
		printf(cmd.OutOrStdout(), "- %s\n", describeVerifyProblem(problem))
	}
}

// describeVerifyProblem 把一处问题写成一句带位置的话。
func describeVerifyProblem(problem verifyProblem) string {
	switch {
	case problem.Path != "" && problem.Line > 0:
		return fmt.Sprintf("%s:%d %s", problem.Path, problem.Line, problem.Message)
	case problem.Path != "":
		return fmt.Sprintf("%s %s", problem.Path, problem.Message)
	default:
		return problem.Message
	}
}

// fitInt32 把一个行号收进接口类型：超出范围只可能是被构造出来的输入，那时取上界，
// 不静默回绕成负数（与 internal/server 里同一处转换同一条取向）。
func fitInt32(value int) int32 {
	switch {
	case value < math.MinInt32:
		return math.MinInt32
	case value > math.MaxInt32:
		return math.MaxInt32
	default:
		return int32(value) //nolint:gosec // 上下界已在上面显式判定
	}
}
