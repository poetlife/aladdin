package galaxy

import (
	"fmt"
	"path"
	"strings"
)

// ContentSlot 是工程的一块独立内容。
//
// 一个工程有一到两个槽，**创建时至少选一个，此后只增不删**。每个槽各有自己的
// 草稿、版本、发布指针与地址，**两个槽互不影响**——在一个槽上的任何动作都不改变
// 另一个槽。两种槽的差别落在四处，**没有一处在对象存储的键上**（见
// docs/design/galaxy/site-model.md）：
//
//   - 入口（index.html / index.md）；
//   - 文件组里是什么（整站文件 / markdown 加站点文件）；
//   - 发布时对文件做什么（原样服务 / markdown 渲染成页）；
//   - 扩展名白名单。
//
// 让历史版本无法复现的从来不是"多了一个槽"，而是"同一个槽换了语义"，因此槽的
// 语义不可变，而槽**可加**。
type ContentSlot string

const (
	// SlotSite 原样服务整站文件。覆盖手写单页与构建产物，地址是工程根。
	SlotSite ContentSlot = "site"
	// SlotDocs 把 markdown 渲染成多页，并把站点文件按原路径一并带上。
	// 地址是工程根下的 `docs/`。
	SlotDocs ContentSlot = "docs"
)

// ParseContentSlot 判定一个取值是不是一个内容槽（唯一入口）。
//
// **没有缺省。** 一个工程可能两个槽都有，因此"不填就用默认值"会让一部分请求
// 打到一个客户端没意识到的槽上。
func ParseContentSlot(raw string) (ContentSlot, error) {
	slot := ContentSlot(strings.TrimSpace(raw))
	if !slot.IsValid() {
		return "", fmt.Errorf("%w: %q", ErrContentSlotInvalid, raw)
	}
	return slot, nil
}

// IsValid 判定内容槽是不是两种之一。零值（未填）不是一种槽。
func (c ContentSlot) IsValid() bool {
	return c == SlotSite || c == SlotDocs
}

// Order 给内容槽一个**固定的展示顺序**：`site` 在前，`docs` 在后。
//
// 顺序固定让同样的输入得到同样的留痕、列表与界面，排障时可比（与清单按路径
// 排序同一条理由）。
func (c ContentSlot) Order() int {
	switch c {
	case SlotSite:
		return 1
	case SlotDocs:
		return 2
	default:
		return 0
	}
}

// ContentSlots 按固定顺序返回全部内容槽。
//
// **只用于遍历全部取值**（管理面、命令行解析、测试）：判断一个工程启用了哪些
// 槽读工程本身，不要拿它去猜。
func ContentSlots() []ContentSlot {
	return []ContentSlot{SlotSite, SlotDocs}
}

// EntryPath 返回该槽的入口路径。
//
// 入口文件必须存在：缺失是**写入期**的拒绝（保存草稿即失败），而不是等到发布
// 才发现。
func (c ContentSlot) EntryPath() string {
	if c == SlotDocs {
		return docsEntryPath
	}
	return staticEntryPath
}

const (
	staticEntryPath = "index.html"
	docsEntryPath   = "index.md"
)

// ReservedSegment 是文档槽占住的那一段路径。
//
// 它让两个槽的地址空间在同一棵树下互不重叠：site 槽占工程根，docs 槽占它下面的
// `docs/`。正因为这一段是固定的，**文档的地址才不会随后加一个槽而移动**——那是
// "槽可加"成立的前提。
const ReservedSegment = "docs"

// RootSuffix 返回该槽的根在工程根之后的那一段（site 槽为空，docs 槽为
// `docs/`）。
func (c ContentSlot) RootSuffix() string {
	if c == SlotDocs {
		return ReservedSegment + "/"
	}
	return ""
}

// ValidateSlotPath 判定一条路径与内容槽是否相称（唯一入口）。
//
// **只有一条规则：`docs` 是保留首段，site 槽的路径不得占用它。** 否则
// `/g/<标识>/docs/...` 会同时是"site 槽里的一条路径"和"docs 槽的地址"，而两者
// 的优先级无从回答。判定必须是唯一的：命令行据此在上送前早退，服务端据此拒绝
// 写入，两处各判一份的表现是"命令行以为能发，服务端拒了它"。
//
// 只判**首段**，且**区分大小写**——与"集合成员测试不处理编码与大小写差异"同一
// 条取向：路径在写入时就被限定死，读取侧不做任何猜测。
func ValidateSlotPath(slot ContentSlot, entryPath string) error {
	if slot != SlotSite {
		return nil
	}
	if entryPath == ReservedSegment || strings.HasPrefix(entryPath, ReservedSegment+"/") {
		return fmt.Errorf("%w: %q 的首段是 %q，它由文档槽占用",
			ErrReservedPath, entryPath, ReservedSegment)
	}
	return nil
}

// MarkdownExtension 是 markdown 源的扩展名。
//
// 它是**渲染输入**的标识：docs 槽下带这个扩展名的条目会被渲染成一页，而不是
// 原样服务。
const MarkdownExtension = ".md"

// markdownContentType 是 markdown 源的下发类型。
//
// 它实际上到不了浏览器：docs 槽下 `.md` 条目在产物里变成 `.html`。留一个取值
// 是为了让"扩展名 → 内容类型"这张表是完整的，而不是缺一格。
const markdownContentType = "text/markdown; charset=utf-8"

// textTypeWhitelist 是**按内容槽给出的**文本类型白名单（唯一入口）。
//
// 键是扩展名（小写、含点），值是下发时的内容类型。它同时回答两件事，且必须由
// 同一张表回答：
//
//   - 一个文件是不是文本条目（不是则必须作为资产上传）；
//   - 一条文本条目下发时回给浏览器的内容类型。
//
// **`docs` 不收 HTML。** 它的页面是渲染出来的，不是写出来的；放开 HTML 等于让
// 两套页面来源并存，而它们的优先级无从回答。`site` 则相反——它就是整站文件，
// HTML 是它的主体。
//
// SVG 只在 `site` 里：它是文本，`img` 取它不执行脚本，而 `object` / `frame`
// 被内容安全策略禁掉（见 csp.go）。
var textTypeWhitelist = map[ContentSlot]map[string]string{
	SlotSite: {
		".html": "text/html; charset=utf-8",
		".htm":  "text/html; charset=utf-8",
		".css":  "text/css; charset=utf-8",
		".js":   "text/javascript; charset=utf-8",
		".mjs":  "text/javascript; charset=utf-8",
		".json": "application/json; charset=utf-8",
		".txt":  "text/plain; charset=utf-8",
		".svg":  "image/svg+xml",
	},
	SlotDocs: {
		MarkdownExtension: markdownContentType,
		".css":            "text/css; charset=utf-8",
		".js":             "text/javascript; charset=utf-8",
		".mjs":            "text/javascript; charset=utf-8",
		".json":           "application/json; charset=utf-8",
	},
}

// TextContentType 返回一条文本条目下发时的内容类型（唯一入口）。
//
// 第二个返回值为假表示这个扩展名不在该槽的白名单里——它要么是一个资产
// （二进制、媒体），要么是一个根本不该出现的路径。
func TextContentType(slot ContentSlot, entryPath string) (string, bool) {
	table, ok := textTypeWhitelist[slot]
	if !ok {
		return "", false
	}
	contentType, ok := table[strings.ToLower(path.Ext(entryPath))]
	return contentType, ok
}

// IsTextPath 判定一个路径在该槽下是不是一条文本条目（唯一入口）。
//
// 服务端与命令行**共用这一张表**：命令行据此决定一个文件走内容对象还是走资产
// 上传，服务端据此决定接受哪一种条目。两处各判一份的表现是"命令行以为这是
// 文本，服务端拒了它"。
func IsTextPath(slot ContentSlot, entryPath string) bool {
	_, ok := TextContentType(slot, entryPath)
	return ok
}

// IsMarkdownPath 判定一个路径是不是 markdown 源。
func IsMarkdownPath(entryPath string) bool {
	return strings.EqualFold(path.Ext(entryPath), MarkdownExtension)
}

// renderedContentType 是 docs 槽渲染出来的每一页的下发类型。
const renderedContentType = "text/html; charset=utf-8"

// ArtifactContentType 返回一条**产物**文本条目下发时的内容类型（唯一入口）。
//
// 它与 TextContentType 不同，因为产物里的路径不等于文件组里的路径：docs 槽的
// 每一页是 `.md` 渲染出来的 `.html`，而 `.html` **不在 docs 的写入白名单里**
// （它是不许用户写的东西，却是渲染必然的产出）。把两者合成一个函数会让
// "写进来的 .html 被拒"与"渲染出来的 .html 要服务"变成同一个判断。
func ArtifactContentType(slot ContentSlot, artifactPath string) (string, bool) {
	if slot == SlotDocs && strings.EqualFold(path.Ext(artifactPath), ".html") {
		return renderedContentType, true
	}
	return TextContentType(slot, artifactPath)
}

// ArtifactPath 返回一条文本条目在**产物**里的路径。
//
// 只有 docs 槽的 markdown 会变：`guide/intro.md` → `guide/intro.html`。其余
// 条目（site 槽的整站文件、docs 槽的站点文件）原路径进产物——站点文件不是渲染
// 结果，"`theme.css` 还是 `theme.css`"。
func ArtifactPath(slot ContentSlot, entryPath string) string {
	if slot == SlotDocs && IsMarkdownPath(entryPath) {
		return strings.TrimSuffix(entryPath, path.Ext(entryPath)) + ".html"
	}
	return entryPath
}

// ValidateManifestForSlot 判定一份清单与内容槽是否相称（唯一入口）。
//
// 四条，都在**写入期**查，而不是等发布才发现：
//
//  1. **入口文件必须存在**——缺失时发布出去的是一个打不开的站点；
//  2. **路径不得落在保留段里**（site 槽的 `docs/`，见 ValidateSlotPath）；
//  3. **文本条目的路径必须在该槽的白名单里**（`docs` 不收 HTML）；文本文件的
//     类型由路径派生，白名单外的路径根本没有下发类型可用；
//  4. **资产条目的路径不得是一个文本路径**——同一个路径不能既是"原样服务的
//     文本"又是"一个媒体文件"，而两者的分派完全相反。
func ValidateManifestForSlot(slot ContentSlot, manifest Manifest) error {
	if !slot.IsValid() {
		return fmt.Errorf("%w: %q", ErrContentSlotInvalid, slot)
	}
	if _, ok := manifest.Find(slot.EntryPath()); !ok {
		return fmt.Errorf("%w: 缺少入口文件 %s", ErrEntrySetInvalid, slot.EntryPath())
	}
	for _, entry := range manifest {
		if err := ValidateSlotPath(slot, entry.Path); err != nil {
			return err
		}
		isTextPath := IsTextPath(slot, entry.Path)
		switch entry.Kind {
		case EntryKindText:
			if !isTextPath {
				return fmt.Errorf("%w: %s 槽不收 %s 这类文本条目（路径 %q）",
					ErrEntrySetInvalid, slot, path.Ext(entry.Path), entry.Path)
			}
		case EntryKindAsset:
			if isTextPath {
				return fmt.Errorf("%w: %q 在 %s 槽下是一条文本路径，不能作为资产条目",
					ErrEntrySetInvalid, entry.Path, slot)
			}
		}
	}
	return nil
}
