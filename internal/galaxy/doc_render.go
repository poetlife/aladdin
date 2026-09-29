package galaxy

import (
	"bytes"
	"fmt"
	"html"
	"path"
	"sort"
	"strconv"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	mdhtml "github.com/yuin/goldmark/renderer/html"
	"github.com/yuin/goldmark/text"
)

// RenderRulesVersion 是当前渲染规则的版本。
//
// 版本记录它保存时所用的渲染规则版本，重新发布用它而不是用当前最新的——否则
// "撤回后重新发布同一版本，产物逐字相同"会在渲染器升级的那天悄悄失效。
//
// **升级渲染器时必须把它加一。** 忘了加的表现是：一份以旧规则发布过的版本，
// 在升级后重新发布得到了不同的产物，而没有任何地方提示这件事发生了。
const RenderRulesVersion = 1

// Doc 是一份渲染好的文档页。
//
// 它是导航派生的输入，也是发布态每一页的输入。
type Doc struct {
	// SourcePath 是 markdown 源在文件组里的路径（guide/intro.md）。
	SourcePath string
	// ArtifactPath 是它在产物里的路径（guide/intro.html）。
	ArtifactPath string
	// Title 取首个一级标题；没有一级标题时回退到文件名。
	Title string
	// Body 是渲染出来的正文片段（不含页面外壳）。
	Body []byte
}

// DocSource 是渲染一份文档所需的源。
type DocSource struct {
	// Path 是源在文件组里的路径。
	Path string
	// Body 是源字节。
	Body []byte
}

// LinkResolver 把 markdown 里的一处引用解析成写进渲染结果的地址（唯一入口）。
//
// 它由调用方按形态给出（发布态给站点绝对地址、预览态给文内锚点），因为**同一
// 处引用在两处的正确地址本来就不同**——而"引用落在文件组的哪一条条目上"这个
// 判断只有一个实现，就在本文件的 resolveEntryPath。
type LinkResolver interface {
	// Resolve 返回写进结果的地址。from 是当前文档在文件组里的路径。
	//
	// isResource 区分两类写法（见 docs/design/galaxy/publication.md）：取资源
	// 的引用（图片）与导航链接。前者不得指向本文件组之外，后者可以是任意地址。
	Resolve(from, dest string, isResource bool) (string, error)
}

// RenderDoc 渲染**一份** markdown 源。
//
// 校验路径用它而不是 RenderDocs：一份坏文档不该掩盖另一份的问题，而导航的派生
// 要等所有文档都渲染通过之后才谈得上。
func RenderDoc(src DocSource, resolver LinkResolver) (Doc, error) {
	body, title, err := renderMarkdown(src.Body, src.Path, resolver)
	if err != nil {
		return Doc{}, err
	}
	if title == "" {
		title = strings.TrimSuffix(path.Base(src.Path), path.Ext(src.Path))
	}
	return Doc{
		SourcePath:   src.Path,
		ArtifactPath: ArtifactPath(SiteFormDocs, src.Path),
		Title:        title,
		Body:         body,
	}, nil
}

// RenderDocs 把一组 markdown 源渲染成文档页，按源路径升序（导航派生的唯一入口）。
//
// **它是确定性的**：同样的源与同样的渲染规则版本，得到逐字相同的结果。这一点
// 是"重复发布不产生新对象"的前提。
func RenderDocs(srcs []DocSource, resolverFor func(from string) LinkResolver) ([]Doc, error) {
	ordered := append([]DocSource(nil), srcs...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Path < ordered[j].Path })

	docs := make([]Doc, 0, len(ordered))
	for _, src := range ordered {
		doc, err := RenderDoc(src, resolverFor(src.Path))
		if err != nil {
			return nil, err
		}
		docs = append(docs, doc)
	}
	return docs, nil
}

// renderMarkdown 渲染一份 markdown，并把其中的引用交给 resolver 改写。
//
// **改写发生在语法树上，不在渲染出来的 HTML 上。** 对 HTML 做字符串替换就得
// 自己回答"哪些 `href=` 是真的链接、哪些出现在代码块里"——而那是解析 HTML，
// 正是本模块刻意不做的事。
func renderMarkdown(src []byte, from string, resolver LinkResolver) ([]byte, string, error) {
	md := goldmark.New(
		// **允许内联 HTML。** 它是用户自己的内容，跑在用户自己的发布域上；
		// 关掉它只会让"我在文档里嵌一段 HTML"变成一个需要读 aladdin 源码
		// 才能回答的问题。隔离由发布域的响应头承担（见 csp.go），不由
		// markdown 渲染器承担。
		goldmark.WithRendererOptions(mdhtml.WithUnsafe()),
	)
	reader := text.NewReader(src)
	tree := md.Parser().Parse(reader)

	var resolveErr error
	if err := ast.Walk(tree, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch n := node.(type) {
		case *ast.Image:
			url, err := resolver.Resolve(from, string(n.Destination), true)
			if err != nil {
				resolveErr = err
				return ast.WalkStop, nil
			}
			n.Destination = []byte(url)
		case *ast.Link:
			url, err := resolver.Resolve(from, string(n.Destination), false)
			if err != nil {
				resolveErr = err
				return ast.WalkStop, nil
			}
			n.Destination = []byte(url)
		}
		return ast.WalkContinue, nil
	}); err != nil {
		return nil, "", err
	}
	if resolveErr != nil {
		return nil, "", resolveErr
	}

	var out bytes.Buffer
	if err := md.Renderer().Render(&out, src, tree); err != nil {
		return nil, "", fmt.Errorf("渲染失败: %w", err)
	}
	return out.Bytes(), firstHeading(tree, src), nil
}

// firstHeading 取首个一级标题的纯文本。
func firstHeading(tree ast.Node, src []byte) string {
	var title string
	_ = ast.Walk(tree, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering || title != "" {
			return ast.WalkContinue, nil
		}
		if heading, ok := node.(*ast.Heading); ok && heading.Level == 1 {
			title = strings.TrimSpace(nodeText(heading, src))
			return ast.WalkStop, nil
		}
		return ast.WalkContinue, nil
	})
	return title
}

// nodeText 取一个节点的纯文本：把其下所有文本段的取值接起来。
//
// 不看任何渲染结果：标题里可能带强调、行内代码与会链接，它们的文本都要算进
// 标题里，而"哪些片段是文本"由语法树回答。
func nodeText(node ast.Node, src []byte) string {
	var out strings.Builder
	_ = ast.Walk(node, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		if textNode, ok := n.(*ast.Text); ok {
			out.Write(textNode.Segment.Value(src))
		}
		return ast.WalkContinue, nil
	})
	return out.String()
}

// isExternalDestination 判定一处引用是不是指向本文件组之外。
//
// 三种写法都算外部：带 scheme 的绝对地址（http、https、data、mailto …）、协议
// 相对地址（`//host/x`）、以及 `asset://`（那是**本模块的记号**，由调用方先
// 处理掉，走到这里说明它没被认出来）。纯锚点与查询串是页内导航，不算外部。
func isExternalDestination(dest string) bool {
	if dest == "" {
		return false
	}
	if strings.HasPrefix(dest, "//") {
		return true
	}
	if strings.HasPrefix(dest, "#") || strings.HasPrefix(dest, "?") {
		return false
	}
	colon := strings.IndexByte(dest, ':')
	if colon <= 0 {
		return false
	}
	scheme := strings.ToLower(dest[:colon])
	for i := 0; i < len(scheme); i++ {
		c := scheme[i]
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '+' && c != '-' && c != '.' {
			return false
		}
	}
	return true
}

// resolveEntryPath 把一处**内部**引用解析成文件组里的路径（唯一入口）。
//
// 两种写法都接受，因为构建工具两种都会产出：
//
//   - **站点根相对**：`/g/<工程标识>/assets/index.js`（siteRoot 由调用方给出）；
//   - **相对当前文档**：`../images/x.png`、`guide/intro.md`。
//
// 解析只做一次路径规范化（`path.Join`），随后就是**集合成员测试**：解析出来的
// 路径在不在清单里。它不碰文件系统，也不做任何额外的猜测——`..` 越出根目录的
// 结果会变成一个不合法的路径，于是直接落进"不在清单里"。
func resolveEntryPath(siteRoot, from, dest string) (string, bool) {
	trimmed := strings.TrimSpace(dest)
	if trimmed == "" {
		return "", false
	}
	// 站点根相对：先摘掉发布根前缀（如果带），再去掉前导 `/`。
	if strings.HasPrefix(trimmed, "/") {
		if siteRoot != "" && strings.HasPrefix(trimmed, siteRoot) {
			trimmed = strings.TrimPrefix(trimmed, siteRoot)
		}
		trimmed = strings.TrimPrefix(trimmed, "/")
		return trimmed, trimmed != ""
	}
	joined := path.Join(path.Dir(from), trimmed)
	// path.Join 会把越出根目录的结果表示成 "../x"，而条目路径不允许 `..`，
	// 因此它天然落进"不在清单里"。
	return joined, joined != "" && !strings.HasPrefix(joined, "../")
}

// DocNavItem 是导航里的一条。
type DocNavItem struct {
	// Href 是导航链接的地址。
	Href  string
	Title string
}

// RenderNav 把导航渲染成一段 HTML。
//
// **导航只由 markdown 文件的树派生**，不引入清单文件，站点文件不进导航：文件组
// 是"有哪些文档"的唯一信源。排序取路径序，标题取每份的首个一级标题；要固定
// 顺序就用带序号的文件名。
func RenderNav(docs []Doc, hrefFor func(Doc) string) string {
	if len(docs) == 0 {
		return ""
	}
	var out strings.Builder
	out.WriteString("<nav>\n<ul>\n")
	for _, doc := range docs {
		out.WriteString("<li><a href=\"")
		out.WriteString(html.EscapeString(hrefFor(doc)))
		out.WriteString("\">")
		out.WriteString(html.EscapeString(doc.Title))
		out.WriteString("</a></li>\n")
	}
	out.WriteString("</ul>\n</nav>\n")
	return out.String()
}

// renderSitePage 把一页文档拼成完整的 HTML 文档。
//
// 这是本模块**唯一**一处服务端生成标记的地方，而它只生成外壳与导航：正文逐字
// 来自渲染结果，且**产物里不出现 aladdin 编写的脚本**——页面切换靠锚点与原生
// 机制，不靠一段注入的路由脚本。
func renderSitePage(title, nav string, body []byte) []byte {
	var out bytes.Buffer
	out.WriteString("<!doctype html>\n<html lang=\"zh\">\n<head>\n")
	out.WriteString("<meta charset=\"utf-8\">\n")
	out.WriteString("<meta name=\"viewport\" content=\"width=device-width, initial-scale=1\">\n")
	out.WriteString("<title>")
	out.WriteString(html.EscapeString(title))
	out.WriteString("</title>\n</head>\n<body>\n")
	out.WriteString(nav)
	out.WriteString("<main>\n")
	out.Write(body)
	out.WriteString("</main>\n</body>\n</html>\n")
	return out.Bytes()
}

// RenderDocsArtifacts 把渲染好的文档页拼成产物字节，按产物路径升序。
//
// 返回的是「产物路径 → 字节」，调用方据此写内容对象并落产物清单。
func RenderDocsArtifacts(docs []Doc, siteRoot string) map[string][]byte {
	hrefFor := func(doc Doc) string { return siteRoot + doc.ArtifactPath }
	nav := RenderNav(docs, hrefFor)
	pages := make(map[string][]byte, len(docs))
	for _, doc := range docs {
		pages[doc.ArtifactPath] = renderSitePage(doc.Title, nav, doc.Body)
	}
	return pages
}

// previewAnchor 返回一份文档在预览文档里的锚点。
//
// 用**序号**而不是路径变形：路径到锚点的字符串变换（把 `/` 换成 `-`）会让
// `a/b` 与 `a-b` 撞车，而碰撞的表现是"点一个链接跳到另一页"。
func previewAnchor(index int) string { return "doc-" + strconv.Itoa(index) }

// RenderPreviewDocument 把整站拼成**一份**文档，用文内锚点导航。
//
// 这是 docs 形态在预览里的形状：沙箱文档没有自己的源，页间跳转只能靠锚点。
// 它与发布态的产物**形状不同**（一份 vs 一页一个地址），因此预览不承诺与发布态
// 逐像素一致——内容一致由资产不可变保证。
func RenderPreviewDocument(docs []Doc, siteRoot string) []byte {
	var out bytes.Buffer
	out.WriteString("<!doctype html>\n<html lang=\"zh\">\n<head>\n")
	out.WriteString("<meta charset=\"utf-8\">\n")
	out.WriteString("<meta name=\"viewport\" content=\"width=device-width, initial-scale=1\">\n")
	out.WriteString("<title>预览</title>\n</head>\n<body>\n<nav>\n<ul>\n")
	for i, doc := range docs {
		out.WriteString("<li><a href=\"#")
		out.WriteString(previewAnchor(i))
		out.WriteString("\">")
		out.WriteString(html.EscapeString(doc.Title))
		out.WriteString("</a></li>\n")
	}
	out.WriteString("</ul>\n</nav>\n")
	for i, doc := range docs {
		out.WriteString("<hr>\n<section id=\"")
		out.WriteString(previewAnchor(i))
		out.WriteString("\">\n<main>\n")
		out.Write(doc.Body)
		out.WriteString("</main>\n</section>\n")
	}
	out.WriteString("</body>\n</html>\n")
	return out.Bytes()
}

// PreviewAnchorFor 返回一份源路径在预览文档里的锚点。
//
// 预览的链接解析器用它把文档间链接变成文内锚点（见 preview_link.go 的使用处）。
func PreviewAnchorFor(docs []Doc, sourcePath string) (string, bool) {
	for i, doc := range docs {
		if doc.SourcePath == sourcePath {
			return "#" + previewAnchor(i), true
		}
	}
	return "", false
}
