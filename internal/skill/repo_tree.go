package skill

import (
	"fmt"
	"sort"
	"strings"
)

// 本文件把 GitHub 给的**目录树**读成一份"要取哪些字节、跳过哪些"的计划。它是纯
// 函数，因此可以完整地离线测。
//
// 为什么不是"下载整个仓库的压缩包再挑"：那样**耗时与带宽随上游大小增长**，而平台的
// 产物只有"留下的那几十个文本文件"。实测过的一个真实技能仓库（issue 点名的
// mono-color-skill）压缩包有 64 MiB，而它值得收的只有 41 个文本文件——为了它们把
// 整包拉下来，既慢又白费带宽，而"上游有多大"本不该是平台的事。
//
// 先问目录树、再逐条取字节，于是**成本只随平台要收的东西增长**：文件数上限 200、
// 文本总量上限 2 MiB 就是这条路真实的上界。代价是请求数变成 1+N，见
// docs/design/skill/onboarding.md 的"远端凭据"。

// Tree 是一次取回的结论：留下什么、跳过了什么。
//
// **"跳过了什么"必须回传，不能吞掉。** 平台只分发文本，而真实仓库常常把示例图
// 一类的东西和正文放在一起（issue 点名的 mono-color-skill 就是：`examples/` 下
// 二十多张 PNG，正文在仓库根）。整体拒收会让那样的技能根本纳管不进来；静默跳过
// 则会让"平台里的包比上游少三十个文件"变成一件没人知道的事。因此跳过，并且**把
// 它记下来**。
type Tree struct {
	// Files 是取回的文本条目。
	Files []FetchedFile
	// Skipped 是被跳过的条目的包内路径（不是文本、或超过单文件上限的）。
	Skipped []string
	// Blobs 是子路径之下的**全部**文件对象（含被跳过的），按路径排序。
	//
	// 它存在的理由是"包外的东西要按路径找"——目前唯一的消费方是封面：封面多半
	// 正是被跳过的那类二进制，而它仍然得能被取回来（见 cover.go）。有了这张表，
	// 取封面不必再向远端问一次目录树。
	Blobs []Blob
}

// Blob 是仓库里的一个文件对象，不论平台收不收它。
type Blob struct {
	// Path 是包内相对路径。
	Path string
	// SHA 是它在 git 里的对象标识。按它取字节，不按路径取。
	SHA string
	// Size 是字节数，取自目录树。
	Size int64
}

// Blob 按路径取一条文件对象。第二个返回值为假表示这一棵里没有它。
func (t Tree) Blob(path string) (Blob, bool) {
	for _, blob := range t.Blobs {
		if blob.Path == path {
			return blob, true
		}
	}
	return Blob{}, false
}

// repoTreeEntry 是 GitHub 目录树接口里的一条。
//
// `Mode` 是 git 的模式串，**它是唯一能区分"普通文件"与"符号链接"的地方**：
// 两者在 `Type` 上都是 `blob`，只有模式（`120000` 是链接）不同。
type repoTreeEntry struct {
	Path string `json:"path"`
	Mode string `json:"mode"`
	Type string `json:"type"`
	Size int64  `json:"size"`
	SHA  string `json:"sha"`
}

// git 模式串里与平台有关的两项。
const (
	gitModeSymlink   = "120000"
	gitModeSubmodule = "160000"
)

// plannedBlob 是一条计划要取的字节。
type plannedBlob struct {
	// Path 是包内相对路径。
	Path string
	// SHA 是这一条在 git 里的对象标识。**按它取字节，不按路径**：路径要拼进 URL，
	// 而 sha 是固定长度的十六进制，拼不出别的东西来。
	SHA string
}

// repoPlan 是"这次要取什么"的结论。
type repoPlan struct {
	// Files 是要取的条目，按路径排序（固定顺序，好让同样的输入得到同样的留痕）。
	Files []plannedBlob
	// Skipped 是被跳过的条目（超过单文件上限的）。
	//
	// **二进制不在这里判**：目录树只给大小，不给内容，因此"是不是文本"要到取回
	// 那一步才知道（见 GithubRemote.FetchTree）。超限的则这里就能判，于是那些
	// 几十 MB 的示例图**一个请求都不发**。
	Skipped []string
	// Blobs 是子路径之下的全部文件对象，按路径排序。
	Blobs []Blob
}

// planRepoTree 把一棵目录树折成一份取字节的计划（唯一入口）。
//
// 三条规则，与包契约定的一致：
//
//   - **只认普通文件**。目录与子模块不是内容；子模块更是一条指向**另一个仓库**的
//     链接，而包契约要求"一棵自足的树"，因此它被拒而不是跳过。
//   - **符号链接拒绝**，理由同上（见 docs/design/skill/onboarding.md）。
//   - **超过单文件上限的跳过并点名**，且不取它的字节。
func planRepoTree(entries []repoTreeEntry, subPath string) (repoPlan, error) {
	var plan repoPlan
	for _, entry := range entries {
		relative, keep := repoTreePath(entry.Path, subPath)
		if !keep {
			continue
		}
		switch entry.Mode {
		case gitModeSymlink:
			return repoPlan{}, fmt.Errorf("%w: 仓库里含符号链接 %q，技能包必须是一棵自足的树",
				ErrPackageInvalid, relative)
		case gitModeSubmodule:
			return repoPlan{}, fmt.Errorf("%w: 仓库里含子模块 %q，它指向另一个仓库",
				ErrPackageInvalid, relative)
		}
		if entry.Type != "blob" {
			// 目录：正常，不是内容。
			continue
		}
		plan.Blobs = append(plan.Blobs, Blob{Path: relative, SHA: entry.SHA, Size: entry.Size})
		if entry.Size > MaxFileBytes {
			plan.Skipped = append(plan.Skipped, relative)
			continue
		}
		plan.Files = append(plan.Files, plannedBlob{Path: relative, SHA: entry.SHA})
	}
	// 顺序固定：同样的一棵树在任何一次、任何一个实现里给出同样的清单。
	sort.Slice(plan.Files, func(i, j int) bool { return plan.Files[i].Path < plan.Files[j].Path })
	sort.Slice(plan.Blobs, func(i, j int) bool { return plan.Blobs[i].Path < plan.Blobs[j].Path })
	sort.Strings(plan.Skipped)
	return plan, nil
}

// repoTreePath 判定一条仓库内的路径要不要收，并给出它在包内的相对路径。
//
// 空子路径表示仓库根；非空时只收它之内的条目，并把它那一段剥掉。
func repoTreePath(path, subPath string) (string, bool) {
	if subPath == "" {
		return path, true
	}
	if path == subPath {
		return "", false
	}
	if !strings.HasPrefix(path, subPath+"/") {
		return "", false
	}
	return strings.TrimPrefix(path, subPath+"/"), true
}
