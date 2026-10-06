package galaxy

import (
	"bytes"
	"fmt"
	"html"
	"path"
	"sort"
	"strings"
	"unicode"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/renderer"
	mdhtml "github.com/yuin/goldmark/renderer/html"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// RenderRulesVersion 是当前渲染规则的版本。
//
// 版本记录它保存时所用的渲染规则版本，重新发布用它而不是用当前最新的——否则
// "撤回后重新发布同一版本，产物逐字相同"会在渲染器升级的那天悄悄失效。
//
// **升级渲染器时必须把它加一。** 忘了加的表现是：一份以旧规则发布过的版本，
// 在升级后重新发布得到了不同的产物，而没有任何地方提示这件事发生了。
const RenderRulesVersion = 4

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
	// Headings 是这一页的二级与三级标题，按出现顺序，供本页目录用。
	Headings []Heading
	// Body 是渲染出来的正文片段（不含页面外壳）。
	Body []byte
}

// Heading 是正文里的一个标题，目录从它派生。
//
// 只收二级与三级：一级标题是页面标题本身（它就在正文最上面），再往下细分
// 就不是"本页有哪些部分"了。
type Heading struct {
	// ID 是标题在页面内的锚点标识，与正文里 `<h2 id="…">` 的那个值是同一个。
	ID string
	// Level 是标题层级（2 或 3）。
	Level int
	// Text 是标题的纯文本，不含渲染标记。
	Text string
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
	rendered, err := renderMarkdown(src.Body, src.Path, resolver)
	if err != nil {
		return Doc{}, err
	}
	title := rendered.title
	if title == "" {
		title = strings.TrimSuffix(path.Base(src.Path), path.Ext(src.Path))
	}
	return Doc{
		SourcePath:   src.Path,
		ArtifactPath: ArtifactPath(SlotDocs, src.Path),
		Title:        title,
		Headings:     rendered.headings,
		Body:         rendered.body,
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

// rendered 是一份 markdown 渲染出来的全部东西。
type rendered struct {
	body     []byte
	title    string
	headings []Heading
}

// renderMarkdown 渲染一份 markdown，并把其中的引用交给 resolver 改写。
//
// **改写发生在语法树上，不在渲染出来的 HTML 上。** 对 HTML 做字符串替换就得
// 自己回答"哪些 `href=` 是真的链接、哪些出现在代码块里"——而那是解析 HTML，
// 正是本模块刻意不做的事。
//
// 标题的锚点标识与目录也在这一趟里定下来：两者必须**同源**，否则会出现"目录
// 指向一个正文里不存在的锚点"这种只有在点下去的时候才发现的错。
func renderMarkdown(src []byte, from string, resolver LinkResolver) (rendered, error) {
	md := goldmark.New(
		// **GFM 是渲染规则的组成部分，不是可选项。** 裸的 CommonMark 里没有表格：
		// 一张 pipe 表会被当成一个普通段落，管道符原样留在正文里。用户写 markdown
		// 时的预期是 GitHub 那一套，因此这里取整个 GFM（表格、删除线、任务列表、
		// 裸地址成链），而不是只挑表格一项——半套 GFM 同样是"要读实现才能回答
		// 哪些写法能用"。
		//
		// Linkify 产出的是 ast.AutoLink，而下面改写的遍历只认 ast.Link 与
		// ast.Image，因此裸地址不会被喂给 LinkResolver。这是对的：一段正文里写了
		// 一个外部地址，它本来就不该拿文件组去解析。
		goldmark.WithExtensions(extension.GFM),
		goldmark.WithRendererOptions(
			// **允许内联 HTML。** 它是用户自己的内容，跑在用户自己的发布域上；
			// 关掉它只会让"我在文档里嵌一段 HTML"变成一个需要读 aladdin 源码
			// 才能回答的问题。隔离由发布域的响应头承担（见 csp.go），不由
			// markdown 渲染器承担。
			mdhtml.WithUnsafe(),
			// 标题带锚点、代码块带语言标签：两处默认渲染给不出这份标记，
			// 而它们是这一版版式的组成部分（见 docStyleSheet）。
			renderer.WithNodeRenderers(util.Prioritized(docMarkup{}, 100)),
		),
	)
	reader := text.NewReader(src)
	tree := md.Parser().Parse(reader)

	seen := make(map[string]int)
	var headings []Heading
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
		case *ast.Heading:
			text := strings.TrimSpace(nodeText(n, src))
			slug := uniqueSlug(seen, headingSlug(text))
			n.SetAttributeString("id", []byte(slug))
			if n.Level == 2 || n.Level == 3 {
				headings = append(headings, Heading{ID: slug, Level: n.Level, Text: text})
			}
		}
		return ast.WalkContinue, nil
	}); err != nil {
		return rendered{}, err
	}
	if resolveErr != nil {
		return rendered{}, resolveErr
	}

	var out bytes.Buffer
	if err := md.Renderer().Render(&out, src, tree); err != nil {
		return rendered{}, fmt.Errorf("渲染失败: %w", err)
	}
	return rendered{body: out.Bytes(), title: firstHeading(tree, src), headings: headings}, nil
}

// headingSlug 把标题文本变成一个锚点标识。
//
// **它自己算，不用 goldmark 的自动标识。** 后者只保留 ASCII 字母数字，一份中文
// 文档因此会得到 `heading`、`heading-1`、`heading-2` 这样一串与内容毫无关系的
// 标识——目录里每一行都指向一个说不出口的名字。这里保留字母、数字与表意文字
// （中日韩都算字母），把空格与标点折成一个连字符。
func headingSlug(text string) string {
	var out strings.Builder
	dash := false
	for _, r := range strings.ToLower(strings.TrimSpace(text)) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			out.WriteRune(r)
			dash = false
		case out.Len() > 0 && !dash:
			// 空格、标点与符号都折成一个连字符；连着来只算一个。
			out.WriteByte('-')
			dash = true
		}
	}
	return strings.Trim(out.String(), "-")
}

// uniqueSlug 处理同一页里的重名标题：第一个原样，其后依次加 `-1`、`-2`。
func uniqueSlug(seen map[string]int, slug string) string {
	if slug == "" {
		slug = "section"
	}
	seen[slug]++
	if times := seen[slug]; times > 1 {
		return fmt.Sprintf("%s-%d", slug, times-1)
	}
	return slug
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

// docMarkup 覆盖两类节点的渲染：标题与围栏代码块。
//
// 只覆盖这两类，别的一律走 goldmark 自己的渲染——本模块要的是**足够像一份文档
// 站的排版**，不是自己重写一个渲染器。这两类之所以要接管，是因为默认渲染给不出
// 锚点与语言标签这两个纯标记（不需要脚本就能有）。
type docMarkup struct{}

// RegisterFuncs 实现 renderer.NodeRenderer。
func (docMarkup) RegisterFuncs(reg renderer.NodeRendererFuncRegisterer) {
	reg.Register(ast.KindHeading, renderHeading)
	reg.Register(ast.KindFencedCodeBlock, renderFencedCodeBlock)
}

// renderHeading 渲染标题，并把锚点写在标题文字之前。
//
// 锚点是**纯 CSS 就能用**的：平时透明，指到标题上才显形（见 docStyleSheet）。
// 它不需要脚本，也不改变标题的纯文本——复制标题时不会把 `#` 一起带走，因为
// 它是标题里一个独立的 `<a>`，而不是标题文字的一部分。
func renderHeading(w util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	n := node.(*ast.Heading)
	var out bytes.Buffer
	if !entering {
		fmt.Fprintf(&out, "</h%d>\n", n.Level)
		return writeMarkup(w, &out)
	}
	id := attributeString(n, "id")
	fmt.Fprintf(&out, "<h%d", n.Level)
	if id != "" {
		fmt.Fprintf(&out, " id=\"%s\"", html.EscapeString(id))
	}
	out.WriteByte('>')
	if id != "" {
		fmt.Fprintf(&out, "<a class=\"header-anchor\" href=\"#%s\" aria-hidden=\"true\">#</a>", html.EscapeString(id))
	}
	return writeMarkup(w, &out)
}

// renderFencedCodeBlock 渲染围栏代码块，带上语言标签。
//
// 语言标签取自围栏后面的那个词（```go 里的 go）。没有语言就不出标签——给一段
// 没写语言的代码贴上"text"是替用户说话，而它本来就没说。
func renderFencedCodeBlock(w util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkContinue, nil
	}
	n := node.(*ast.FencedCodeBlock)
	lang := string(n.Language(source))

	var out bytes.Buffer
	out.WriteString("<div class=\"code-block\">")
	if lang != "" {
		fmt.Fprintf(&out, "<span class=\"lang\">%s</span>", html.EscapeString(lang))
	}
	out.WriteString("<pre><code")
	if lang != "" {
		fmt.Fprintf(&out, " class=\"language-%s\"", html.EscapeString(lang))
	}
	out.WriteByte('>')
	for i := 0; i < n.Lines().Len(); i++ {
		segment := n.Lines().At(i)
		out.Write(util.EscapeHTML(segment.Value(source)))
	}
	out.WriteString("</code></pre></div>\n")
	return writeMarkup(w, &out)
}

// writeMarkup 把一段拼好的标记交给渲染器。
//
// 拼进 buffer 再写一次，是为了让"写失败"只有一处要处理：渲染器的写入目标是一个
// 接口，每一次写都可能失败，逐次检查会把这两个函数淹掉，而它们的正事是标记。
func writeMarkup(w util.BufWriter, markup *bytes.Buffer) (ast.WalkStatus, error) {
	if _, err := w.WriteString(markup.String()); err != nil {
		return ast.WalkStop, err
	}
	return ast.WalkContinue, nil
}

// attributeString 读一个节点上的属性取值。取不到时返回空串。
func attributeString(node ast.Node, name string) string {
	value, ok := node.AttributeString(name)
	if !ok {
		return ""
	}
	switch typed := value.(type) {
	case []byte:
		return string(typed)
	case string:
		return typed
	}
	return ""
}

// isExternalDestination 判定一处引用是不是指向本文件组之外。
//
// 三种写法都算外部：带 scheme 的绝对地址（http、https、data、mailto …）、协议
// 相对地址（`//host/x`）、以及 `asset://`（那是**本模块的记号**，由调用方先
// 处理掉，走到这里说明它没被认出来）。
//
// **"不是外部"不等于"要拿文件组去解析"。** `#小节` 与 `?q=1` 也不是外部地址，但它们
// 同样不落在任何条目上——把这个二值判断当成"外部 / 站内文件引用"来用，就会让每一处
// `[见小节](#小节)` 都变成一次"引用了不存在的位置"。那两档在进到本函数之前就由
// splitDestination 摘掉了（见 link_resolver.go）。
func isExternalDestination(dest string) bool {
	if dest == "" {
		return false
	}
	if strings.HasPrefix(dest, "//") {
		return true
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

// RenderSidebar 把侧栏渲染成一段 HTML：全部文档，当前这一页高亮。
//
// **侧栏只由 markdown 文件的树派生**，不引入清单文件，站点文件不进侧栏：文件组
// 是"有哪些文档"的唯一信源。排序取路径序，标题取每份的首个一级标题；要固定
// 顺序就用带序号的文件名。
//
// currentArtifactPath 是当前这一页在产物里的路径——按**产物路径**而不是源路径
// 比对，因为侧栏链接指向的就是产物路径，两者用的是同一个坐标系。
func RenderSidebar(docs []Doc, hrefFor func(Doc) string, currentArtifactPath string) string {
	if len(docs) == 0 {
		return ""
	}
	var out strings.Builder
	out.WriteString("<aside class=\"sidebar\">\n<nav>\n<ul>\n")
	for _, doc := range docs {
		out.WriteString("<li><a href=\"")
		out.WriteString(html.EscapeString(hrefFor(doc)))
		out.WriteString("\"")
		if doc.ArtifactPath == currentArtifactPath {
			out.WriteString(" class=\"active\" aria-current=\"page\"")
		}
		out.WriteString(">")
		out.WriteString(html.EscapeString(doc.Title))
		out.WriteString("</a></li>\n")
	}
	out.WriteString("</ul>\n</nav>\n</aside>\n")
	return out.String()
}

// RenderTOC 把本页目录渲染成一段 HTML。没有二三级标题时返回空串，外壳据此
// 整块省掉——一份只有一个标题的文档不该在右边留一列空白。
func RenderTOC(headings []Heading) string {
	if len(headings) == 0 {
		return ""
	}
	var out strings.Builder
	out.WriteString("<aside class=\"toc\">\n<p class=\"toc-title\">本页</p>\n<ul>\n")
	for _, heading := range headings {
		fmt.Fprintf(&out, "<li class=\"lvl-%d\"><a href=\"#%s\">%s</a></li>\n",
			heading.Level, html.EscapeString(heading.ID), html.EscapeString(heading.Text))
	}
	out.WriteString("</ul>\n</aside>\n")
	return out.String()
}

// docStyleSheet 是文档页外壳自带的那一份排版。
//
// **它是渲染规则的一部分，不是往用户内容上叠的东西。** `docs` 槽的页面本来就是
// 渲染出来的：正文由 markdown 渲染，外壳与这份样式由 aladdin 生成，两者一起构成
// 产物，并按版本钉住（见 RenderRulesVersion）。这与"用户自己的页面不被注入任何
// aladdin 编写的脚本或样式"并不冲突——那条说的是 `site` 槽那种逐字原样交付的
// 产物（见 docs/design/galaxy/authoring.md）。
//
// 它内联而不是外链，理由是**没有第二种可能**：外链要给出一个地址，而预览与发布
// 的站点根不同，外壳这一层并不知道自己在哪一个下面。内联没有这个地址问题，也和
// 发布域的 CSP 相容（`style-src` 含 `'unsafe-inline'`，见 csp.go）。
//
// **明暗两套取值都在这一个文件里，暗色只跟随系统**（`prefers-color-scheme`）。
// 不做一个手动开关是有意的：产物里不出现 aladdin 编写的脚本，因此没有地方记住
// 用户的选择，一个翻页就回到原样的开关还不如没有。要选择就得有脚本，而那是
// 另一条 spec 决定（见 docs/design/galaxy/site-model.md）。
//
// 取值照搬 VitePress 默认主题：配色 token、字号阶梯、正文 688px、二级标题带上
// 边线、表格斑马纹、代码块外壳与语言标签、标题悬停锚点。**借的是取值，不是它
// 的文件**——这里手写，只留下这份版式真正要用的部分。
const docStyleSheet = `:root{
color-scheme:light;
--bg:#fff;--bg-alt:#f6f6f7;--bg-soft:#f6f6f7;
--text-1:#3c3c43;--text-2:#67676c;--text-3:#929295;
--divider:#e2e2e3;--border:#c2c2c4;
--brand-1:#3451b2;--brand-2:#3a5ccc;
--code-color:var(--brand-1);--code-bg:rgba(142,150,170,.14);
--code-block-bg:var(--bg-alt);--code-block-color:var(--text-2);
--nav-h:64px;--sidebar-w:272px;--toc-w:224px;--doc-w:688px;--layout-w:1440px;
--font-base:-apple-system,BlinkMacSystemFont,"Segoe UI","PingFang SC","Hiragino Sans GB","Microsoft YaHei",sans-serif,"Apple Color Emoji","Segoe UI Emoji";
--font-mono:ui-monospace,"SFMono-Regular",Menlo,Monaco,Consolas,"Liberation Mono","Courier New",monospace
}
@media (prefers-color-scheme:dark){:root{
color-scheme:dark;
--bg:#1b1b1f;--bg-alt:#161618;--bg-soft:#202127;
--text-1:#dfdfd6;--text-2:#98989f;--text-3:#6a6a71;
--divider:#2e2e32;--border:#3c3f44;
--brand-1:#a8b1ff;--brand-2:#5c73e7;
--code-bg:rgba(101,117,133,.16)
}}
*,*::before,*::after{box-sizing:border-box}
html{-webkit-text-size-adjust:100%}
body{margin:0;background:var(--bg);color:var(--text-1);font-family:var(--font-base);font-size:16px;line-height:1.5}
img{max-width:100%;height:auto}
/* 顶栏 */
.nav{position:sticky;top:0;z-index:10;background:var(--bg);border-bottom:1px solid var(--divider)}
.nav-inner{display:flex;align-items:center;height:var(--nav-h);max-width:var(--layout-w);margin:0 auto;padding:0 24px}
.nav-title{font-size:1rem;font-weight:600;color:var(--text-1);text-decoration:none}
.nav-title:hover{color:var(--brand-1)}
/* 版式 */
.layout{display:flex;max-width:var(--layout-w);margin:0 auto;padding:0 24px}
.sidebar{position:sticky;top:var(--nav-h);flex:none;align-self:flex-start;width:var(--sidebar-w);max-height:calc(100vh - var(--nav-h));overflow-y:auto;padding:32px 24px 32px 0}
.sidebar ul{margin:0;padding:0;list-style:none}
.sidebar a{display:block;margin:2px 0;padding:6px 12px;border-radius:6px;font-size:.875rem;color:var(--text-2);text-decoration:none;transition:color .25s,background-color .25s}
.sidebar a:hover{color:var(--text-1);background:var(--bg-soft)}
.sidebar a.active{color:var(--brand-1);font-weight:600;background:var(--bg-soft)}
.content{display:flex;flex:1;gap:40px;justify-content:center;align-items:flex-start;min-width:0;padding:32px 0 96px}
.doc{flex:1;min-width:0;max-width:var(--doc-w)}
.toc{position:sticky;top:calc(var(--nav-h) + 32px);flex:none;align-self:flex-start;width:var(--toc-w);max-height:calc(100vh - var(--nav-h) - 64px);overflow-y:auto}
.toc-title{margin:0 0 8px;font-size:.875rem;font-weight:600}
.toc ul{margin:0;padding:0;list-style:none;border-left:1px solid var(--divider)}
.toc a{display:block;margin-left:-1px;padding:3px 0 3px 16px;border-left:2px solid transparent;font-size:.8125rem;line-height:1.5;color:var(--text-2);text-decoration:none}
.toc a:hover{color:var(--brand-1);border-left-color:var(--brand-1)}
.toc .lvl-3 a{padding-left:32px}
/* 正文 */
.doc{font-size:16px;line-height:1.75}
.doc [id]{scroll-margin-top:calc(var(--nav-h) + 24px)}
.doc h1,.doc h2,.doc h3,.doc h4,.doc h5,.doc h6{position:relative;font-weight:600;outline:none}
.doc h1{margin:0 0 16px;font-size:2rem;line-height:1.25;letter-spacing:-.02em}
.doc h2{margin:48px 0 16px;padding-top:24px;border-top:1px solid var(--divider);font-size:1.5rem;line-height:1.33;letter-spacing:-.02em}
.doc h3{margin:32px 0 0;font-size:1.25rem;line-height:1.4;letter-spacing:-.01em}
.doc h4{margin:24px 0 0;font-size:1.125rem;line-height:1.33;letter-spacing:-.01em}
.doc p,.doc summary{margin:16px 0}
.doc a{font-weight:500;color:var(--brand-1);text-decoration:underline;text-underline-offset:2px}
.doc a:hover{color:var(--brand-2)}
.doc strong{font-weight:600}
.doc ul,.doc ol{margin:16px 0;padding-left:1.25rem}
.doc ul{list-style:disc}
.doc ol{list-style:decimal}
.doc li+li{margin-top:8px}
.doc li>ul,.doc li>ol{margin:8px 0 0}
.doc li>p:first-child{margin-top:0}
.doc li>p:last-child{margin-bottom:0}
.doc li:has(>input[type=checkbox]){list-style:none}
.doc li>input[type=checkbox]{margin:0 .25rem .125rem -1.25rem;vertical-align:middle;accent-color:var(--brand-1)}
.doc blockquote{margin:16px 0;border-left:2px solid var(--divider);padding-left:1rem;color:var(--text-2)}
.doc blockquote>p{margin:0}
.doc hr{margin:16px 0;border:0;border-top:1px solid var(--divider)}
/* 表格 */
.doc table{display:block;max-width:100%;border-collapse:collapse;margin:20px 0;overflow-x:auto}
.doc tr{border-top:1px solid var(--divider);background:var(--bg)}
.doc tr:nth-child(2n){background:var(--bg-soft)}
.doc th,.doc td{border:1px solid var(--divider);padding:8px 16px;text-align:left;vertical-align:top;font-size:.875rem}
.doc th{font-weight:600;color:var(--text-2);background:var(--bg-soft)}
/* 行内代码与代码块 */
.doc :not(pre)>code{padding:.1875rem .375rem;border-radius:.25rem;background:var(--code-bg);color:var(--code-color);font-family:var(--font-mono);font-size:.875em}
.doc a>code{color:var(--brand-1)}
.doc .code-block{position:relative;margin:16px 0;border-radius:8px;background:var(--code-block-bg);overflow:hidden}
.doc .code-block pre{margin:0;padding:20px 24px;overflow-x:auto}
.doc .code-block code{display:block;width:fit-content;min-width:100%;font-family:var(--font-mono);font-size:.875em;line-height:1.7;color:var(--code-block-color)}
.doc .code-block .lang{position:absolute;top:8px;right:12px;font-family:var(--font-mono);font-size:.75rem;color:var(--text-3);user-select:none}
/* 标题锚点：平时透明，指到标题上才显形 */
.doc .header-anchor{position:absolute;left:0;margin-left:-.87em;font-weight:500;color:var(--text-3);text-decoration:none;opacity:0;user-select:none;transition:color .25s,opacity .25s}
.doc h2 .header-anchor{top:24px}
.doc :is(h1,h2,h3,h4,h5,h6):hover .header-anchor,.doc .header-anchor:focus{opacity:1}
.doc .header-anchor:hover{color:var(--brand-1)}
/* 窄屏：目录先收，再收侧栏——侧栏折成一行可横滑的链接，没有汉堡菜单也还能导航 */
@media (max-width:1280px){.toc{display:none}}
@media (max-width:960px){
.layout{display:block;padding:0 16px}
.sidebar{position:static;width:auto;max-height:none;overflow-x:auto;padding:16px 0 0}
.sidebar ul{display:flex;gap:6px}
.sidebar a{white-space:nowrap;border:1px solid var(--divider)}
.content{display:block;padding:24px 0 64px}
.doc{max-width:none}
.doc h1{font-size:1.75rem}
}
`

// sitePage 是拼一页所需的所有部件。
type sitePage struct {
	// Title 是这一页的标题，进 `<title>`。
	Title string
	// SiteTitle 是整站的名称（顶栏上那一个）。
	SiteTitle string
	// HomeHref 是顶栏标题链到的地址（入口那一页）。
	HomeHref string
	// Sidebar 与 TOC 是两块已经渲染好的片段；TOC 为空时整块省掉。
	Sidebar string
	TOC     string
	// Body 是正文，逐字来自渲染结果。
	Body []byte
}

// renderSitePage 把一页文档拼成完整的 HTML 文档。
//
// 这是本模块**唯一**一处服务端生成标记的地方，而它只生成外壳、侧栏、目录、样式与
// **接入桥**（见 doc_frame_bridge.go）：**正文逐字来自渲染结果**。页内跳转靠锚点、
// 目录高亮靠 CSS，灯箱交给宿主——页里那段脚本只发一条消息，不接管页面。
//
// `site` 槽不走这里：那条路逐字交付用户的整站，aladdin 一个字节都不加（见
// docs/design/galaxy/site-model.md）。
func renderSitePage(page sitePage) []byte {
	title := page.Title
	if page.SiteTitle != "" && page.SiteTitle != page.Title {
		title = page.Title + " | " + page.SiteTitle
	}

	var out bytes.Buffer
	out.WriteString("<!doctype html>\n<html lang=\"zh\">\n<head>\n")
	out.WriteString("<meta charset=\"utf-8\">\n")
	out.WriteString("<meta name=\"viewport\" content=\"width=device-width, initial-scale=1\">\n")
	out.WriteString("<title>")
	out.WriteString(html.EscapeString(title))
	out.WriteString("</title>\n<style>")
	out.WriteString(docStyleSheet)
	out.WriteString("</style>\n</head>\n<body>\n")

	out.WriteString("<header class=\"nav\">\n<div class=\"nav-inner\">\n")
	if page.SiteTitle != "" {
		out.WriteString("<a class=\"nav-title\" href=\"")
		out.WriteString(html.EscapeString(page.HomeHref))
		out.WriteString("\">")
		out.WriteString(html.EscapeString(page.SiteTitle))
		out.WriteString("</a>\n")
	}
	out.WriteString("</div>\n</header>\n")

	out.WriteString("<div class=\"layout\">\n")
	out.WriteString(page.Sidebar)
	out.WriteString("<main class=\"content\">\n<div class=\"doc\">\n")
	out.Write(page.Body)
	out.WriteString("</div>\n")
	out.WriteString(page.TOC)
	out.WriteString("</main>\n</div>\n")
	// 接入桥放在正文之后：它不改变页面结构，也不该出现在任何"壳与正文谁先谁后"
	// 的断言中间。
	out.WriteString(frameBridgeTag())
	out.WriteString("</body>\n</html>\n")
	return out.Bytes()
}

// RenderDocsArtifacts 把渲染好的文档页拼成产物字节。
//
// 返回的是「产物路径 → 字节」，调用方据此写内容对象并落产物清单。
func RenderDocsArtifacts(docs []Doc, siteRoot string) map[string][]byte {
	if len(docs) == 0 {
		return map[string][]byte{}
	}
	hrefFor := func(doc Doc) string { return siteRoot + doc.ArtifactPath }

	// 顶栏上那个站名取自入口文档（index.md）的一级标题：产物里没有别处记着
	// "这个站叫什么"，而入口本来就是这份文档集的首页。找不到入口（清单里没有
	// index.md）时退到第一份文档——总比一个空的顶栏好。
	home := docs[0]
	entryArtifact := ArtifactPath(SlotDocs, SlotDocs.EntryPath())
	for _, doc := range docs {
		if doc.ArtifactPath == entryArtifact {
			home = doc
			break
		}
	}

	pages := make(map[string][]byte, len(docs))
	for _, doc := range docs {
		pages[doc.ArtifactPath] = renderSitePage(sitePage{
			Title:     doc.Title,
			SiteTitle: home.Title,
			HomeHref:  hrefFor(home),
			Sidebar:   RenderSidebar(docs, hrefFor, doc.ArtifactPath),
			TOC:       RenderTOC(doc.Headings),
			Body:      doc.Body,
		})
	}
	return pages
}
