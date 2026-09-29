package galaxy

import (
	"fmt"
	"path"
	"strings"
)

// SiteForm 是工程的站点形态。它在**创建时**定下，此后不可改。
//
// 两种形态的差别落在四处，**没有一处在对象存储的键上**（见
// docs/design/galaxy/site-model.md）：
//
//   - 入口（index.html / index.md）；
//   - 文件组里是什么（整站文件 / markdown 加站点文件）；
//   - 发布时对文件做什么（原样服务 / markdown 渲染成页）；
//   - 扩展名白名单。
type SiteForm string

const (
	// SiteFormStatic 原样服务整站文件。覆盖手写单页与构建产物。
	SiteFormStatic SiteForm = "static"
	// SiteFormDocs 把 markdown 渲染成多页，并把站点文件按原路径一并带上。
	SiteFormDocs SiteForm = "docs"
)

// ParseSiteForm 判定一个取值是不是一种形态（唯一入口）。
//
// **没有缺省。** 形态创建时定下、此后不可改，因此"不填就用默认值"会让一部分
// 工程的形态来自一个客户端没意识到的选择。
func ParseSiteForm(raw string) (SiteForm, error) {
	form := SiteForm(strings.TrimSpace(raw))
	if !form.IsValid() {
		return "", fmt.Errorf("%w: %q", ErrSiteFormInvalid, raw)
	}
	return form, nil
}

// IsValid 判定形态是不是两种之一。零值（未填）不是一种形态。
func (f SiteForm) IsValid() bool {
	return f == SiteFormStatic || f == SiteFormDocs
}

// EntryPath 返回该形态的入口路径。
//
// 入口文件必须存在：缺失是**写入期**的拒绝（保存草稿即失败），而不是等到发布
// 才发现。
func (f SiteForm) EntryPath() string {
	if f == SiteFormDocs {
		return docsEntryPath
	}
	return staticEntryPath
}

const (
	staticEntryPath = "index.html"
	docsEntryPath   = "index.md"
)

// MarkdownExtension 是 markdown 源的扩展名。
//
// 它是**渲染输入**的标识：docs 形态下带这个扩展名的条目会被渲染成一页，而不是
// 原样服务。
const MarkdownExtension = ".md"

// markdownContentType 是 markdown 源的下发类型。
//
// 它实际上到不了浏览器：docs 形态下 `.md` 条目在产物里变成 `.html`。留一个取值
// 是为了让"扩展名 → 内容类型"这张表是完整的，而不是缺一格。
const markdownContentType = "text/markdown; charset=utf-8"

// textTypeWhitelist 是**按形态给出的**文本类型白名单（唯一入口）。
//
// 键是扩展名（小写、含点），值是下发时的内容类型。它同时回答两件事，且必须由
// 同一张表回答：
//
//   - 一个文件是不是文本条目（不是则必须作为资产上传）；
//   - 一条文本条目下发时回给浏览器的内容类型。
//
// **`docs` 不收 HTML。** 它的页面是渲染出来的，不是写出来的；放开 HTML 等于让
// 两套页面来源并存，而它们的优先级无从回答。`static` 则相反——它就是整站文件，
// HTML 是它的主体。
//
// SVG 只在 `static` 里：它是文本，`img` 取它不执行脚本，而 `object` / `frame`
// 被内容安全策略禁掉（见 csp.go）。
var textTypeWhitelist = map[SiteForm]map[string]string{
	SiteFormStatic: {
		".html": "text/html; charset=utf-8",
		".htm":  "text/html; charset=utf-8",
		".css":  "text/css; charset=utf-8",
		".js":   "text/javascript; charset=utf-8",
		".mjs":  "text/javascript; charset=utf-8",
		".json": "application/json; charset=utf-8",
		".txt":  "text/plain; charset=utf-8",
		".svg":  "image/svg+xml",
	},
	SiteFormDocs: {
		MarkdownExtension: markdownContentType,
		".css":            "text/css; charset=utf-8",
		".js":             "text/javascript; charset=utf-8",
		".mjs":            "text/javascript; charset=utf-8",
		".json":           "application/json; charset=utf-8",
	},
}

// TextContentType 返回一条文本条目下发时的内容类型（唯一入口）。
//
// 第二个返回值为假表示这个扩展名不在该形态的白名单里——它要么是一个资产
// （二进制、媒体），要么是一个根本不该出现的路径。
func TextContentType(form SiteForm, entryPath string) (string, bool) {
	table, ok := textTypeWhitelist[form]
	if !ok {
		return "", false
	}
	contentType, ok := table[strings.ToLower(path.Ext(entryPath))]
	return contentType, ok
}

// IsTextPath 判定一个路径在该形态下是不是一条文本条目（唯一入口）。
//
// 服务端与命令行**共用这一张表**：命令行据此决定一个文件走内容对象还是走资产
// 上传，服务端据此决定接受哪一种条目。两处各判一份的表现是"命令行以为这是
// 文本，服务端拒了它"。
func IsTextPath(form SiteForm, entryPath string) bool {
	_, ok := TextContentType(form, entryPath)
	return ok
}

// IsMarkdownPath 判定一个路径是不是 markdown 源。
func IsMarkdownPath(entryPath string) bool {
	return strings.EqualFold(path.Ext(entryPath), MarkdownExtension)
}

// renderedContentType 是 docs 形态渲染出来的每一页的下发类型。
const renderedContentType = "text/html; charset=utf-8"

// ArtifactContentType 返回一条**产物**文本条目下发时的内容类型（唯一入口）。
//
// 它与 TextContentType 不同，因为产物里的路径不等于文件组里的路径：docs 形态
// 的每一页是 `.md` 渲染出来的 `.html`，而 `.html` **不在 docs 的写入白名单里**
// （它是不许用户写的东西，却是渲染必然的产出）。把两者合成一个函数会让
// "写进来的 .html 被拒"与"渲染出来的 .html 要服务"变成同一个判断。
func ArtifactContentType(form SiteForm, artifactPath string) (string, bool) {
	if form == SiteFormDocs && strings.EqualFold(path.Ext(artifactPath), ".html") {
		return renderedContentType, true
	}
	return TextContentType(form, artifactPath)
}

// ArtifactPath 返回一条文本条目在**产物**里的路径。
//
// 只有 docs 形态的 markdown 会变：`guide/intro.md` → `guide/intro.html`。其余
// 条目（static 的整站文件、docs 的站点文件）原路径进产物——站点文件不是渲染
// 结果，"`theme.css` 还是 `theme.css`"。
func ArtifactPath(form SiteForm, entryPath string) string {
	if form == SiteFormDocs && IsMarkdownPath(entryPath) {
		return strings.TrimSuffix(entryPath, path.Ext(entryPath)) + ".html"
	}
	return entryPath
}

// ValidateManifestForForm 判定一份清单与工程形态是否相称（唯一入口）。
//
// 三条，都在**写入期**查，而不是等发布才发现：
//
//  1. **入口文件必须存在**——缺失时发布出去的是一个打不开的站点；
//  2. **文本条目的路径必须在该形态的白名单里**（`docs` 不收 HTML）；文本文件的
//     类型由路径派生，白名单外的路径根本没有下发类型可用；
//  3. **资产条目的路径不得是一个文本路径**——同一个路径不能既是"原样服务的
//     文本"又是"一个媒体文件"，而两者的分派完全相反。
func ValidateManifestForForm(form SiteForm, manifest Manifest) error {
	if !form.IsValid() {
		return fmt.Errorf("%w: %q", ErrSiteFormInvalid, form)
	}
	if _, ok := manifest.Find(form.EntryPath()); !ok {
		return fmt.Errorf("%w: 缺少入口文件 %s", ErrEntrySetInvalid, form.EntryPath())
	}
	for _, entry := range manifest {
		isTextPath := IsTextPath(form, entry.Path)
		switch entry.Kind {
		case EntryKindText:
			if !isTextPath {
				return fmt.Errorf("%w: %s 形态不收 %s 这类文本条目（路径 %q）",
					ErrEntrySetInvalid, form, path.Ext(entry.Path), entry.Path)
			}
		case EntryKindAsset:
			if isTextPath {
				return fmt.Errorf("%w: %q 在 %s 形态下是一条文本路径，不能作为资产条目",
					ErrEntrySetInvalid, entry.Path, form)
			}
		}
	}
	return nil
}
