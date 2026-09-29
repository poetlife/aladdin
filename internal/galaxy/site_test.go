package galaxy

import (
	"net/url"
	"slices"
	"strings"
	"testing"
)

// 路径约束是**地址的约束**，不是风格偏好：路径会成为公开地址的一部分。
func TestValidEntryPath(t *testing.T) {
	valid := []string{
		"index.html",
		"assets/index-abc123.js",
		"guide/intro.md",
		"a-b_c.d~e",
	}
	for _, entryPath := range valid {
		if !ValidEntryPath(entryPath) {
			t.Errorf("%q 被判为不合法", entryPath)
		}
	}

	invalid := []string{
		"",
		"/index.html", // 前导 /
		"assets/",     // 结尾 /
		"a//b",        // 空段
		"../secret",   // ..
		"a/../b",      // 段内的 ..
		".",           // 当前目录
		"..",          // 上级目录
		"a b.html",    // 空格
		"a%20b.html",  // 百分号编码：收它就要处理等价性
		"a\\b",        // 反斜杠
		"图片/说明.md",    // 非 ASCII：地址里会变成百分号编码，等价性问题随之回来
		"a\tb",        // 控制字符
	}
	for _, entryPath := range invalid {
		if ValidEntryPath(entryPath) {
			t.Errorf("%q 被判为合法", entryPath)
		}
	}
}

// **白名单按形态给出**：两种形态的差别之一就是它。
func TestTextWhitelistIsPerForm(t *testing.T) {
	cases := []struct {
		form      ContentSlot
		entryPath string
		want      bool
	}{
		{SlotSite, "index.html", true},
		{SlotSite, "app.js", true},
		{SlotSite, "logo.svg", true},
		{SlotSite, "photo.png", false},
		{SlotDocs, "index.md", true},
		{SlotDocs, "theme.css", true},
		// **docs 不收 HTML**：它的页面是渲染出来的，不是写出来的。
		{SlotDocs, "index.html", false},
		{SlotDocs, "photo.png", false},
	}
	for _, tc := range cases {
		if got := IsTextPath(tc.form, tc.entryPath); got != tc.want {
			t.Errorf("IsTextPath(%s, %q) = %v，期望 %v", tc.form, tc.entryPath, got, tc.want)
		}
	}
}

// 产物路径：只有 docs 的 markdown 会变（`.md` → `.html`），其余原路径进产物。
func TestArtifactPath(t *testing.T) {
	cases := []struct {
		form      ContentSlot
		entryPath string
		want      string
	}{
		{SlotDocs, "index.md", "index.html"},
		{SlotDocs, "guide/intro.md", "guide/intro.html"},
		{SlotDocs, "theme.css", "theme.css"},
		{SlotSite, "index.html", "index.html"},
		{SlotSite, "assets/app.js", "assets/app.js"},
	}
	for _, tc := range cases {
		if got := ArtifactPath(tc.form, tc.entryPath); got != tc.want {
			t.Errorf("ArtifactPath(%s, %q) = %q，期望 %q", tc.form, tc.entryPath, got, tc.want)
		}
	}
}

// 渲染出来的页是 `.html`，而它**不在 docs 的写入白名单里**——两者不是同一个判断。
func TestArtifactContentTypeServesRenderedPages(t *testing.T) {
	contentType, ok := ArtifactContentType(SlotDocs, "guide/intro.html")
	if !ok || contentType != renderedContentType {
		t.Errorf("ArtifactContentType(docs, guide/intro.html) = %q/%v，期望 %q/true",
			contentType, ok, renderedContentType)
	}
	if _, ok := TextContentType(SlotDocs, "guide/intro.html"); ok {
		t.Error("写进来的 .html 不该被当成合法文本条目")
	}
}

// 用户写 markdown 时的预期是 GitHub 那一套：表格、删除线、任务列表都要真的渲染。
//
// 裸的 CommonMark 里这些都没有，而它们的缺席**不报错**——一张 pipe 表会静默变成
// 一段带管道符的正文，正是这个测试要挡住的那种失败。
func TestDocsRenderingIsGFM(t *testing.T) {
	const src = "# 标题\n\n## 成品概览\n\n" +
		"| 项 | 值 |\n|----|-----|\n| 蓝本 | 乙 |\n\n" +
		"~~删掉~~\n\n- [ ] 未做\n\n```go\nfunc main() {}\n```\n"

	doc, err := RenderDoc(
		DocSource{Path: "index.md", Body: []byte(src)},
		SiteLinker{Slot: SlotDocs},
	)
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	body := string(doc.Body)
	for what, want := range map[string]string{
		"表格":   "<table>",
		"表头":   "<th>项</th>",
		"单元格":  "<td>蓝本</td>",
		"删除线":  "<del>删掉</del>",
		"任务项":  "type=\"checkbox\"",
		"标题锚点": "class=\"header-anchor\" href=\"#成品概览\"",
		"语言标签": "<span class=\"lang\">go</span>",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("%s没渲染出来：正文里找不到 %q\n正文：%s", what, want, body)
		}
	}
	// 表格是块级元素，落进段落里就说明它被当成普通文本了。
	if strings.Contains(body, "<p>| 项 | 值 |") {
		t.Errorf("表格被当成了正文：%s", body)
	}
	if doc.Title != "标题" {
		t.Errorf("标题 = %q，期望 %q", doc.Title, "标题")
	}
	// 目录与正文的锚点同源：目录里那一行指的就是正文里那个 id。
	if len(doc.Headings) != 1 {
		t.Fatalf("目录条目 = %v，期望只有二级标题一条", doc.Headings)
	}
	if got := doc.Headings[0]; got.ID != "成品概览" || got.Level != 2 || got.Text != "成品概览" {
		t.Errorf("目录条目 = %+v，期望 {成品概览 2 成品概览}", got)
	}
	if !strings.Contains(body, "<h2 id=\"成品概览\">") {
		t.Errorf("二级标题没有拿到目录里那个 id：%s", body)
	}
}

// 锚点标识自己算，不用 goldmark 的自动标识：后者只留 ASCII，一份中文文档会得到
// 一串 `heading-1`、`heading-2`——目录里每一行都指向一个说不出口的名字。
func TestHeadingSlug(t *testing.T) {
	cases := map[string]string{
		"成品概览":          "成品概览",
		"道士下山 · 制作过程":   "道士下山-制作过程",
		"Hello, World!": "hello-world",
		"《道士下山》":        "道士下山",
		"v1.2 发布":       "v1-2-发布",
		"   ":           "",
		"---":           "",
	}
	for text, want := range cases {
		if got := headingSlug(text); got != want {
			t.Errorf("headingSlug(%q) = %q，期望 %q", text, got, want)
		}
	}
	// 重名标题：第一个原样，其后依次加序号。
	seen := make(map[string]int)
	for i, want := range []string{"甲", "甲-1", "甲-2"} {
		if got := uniqueSlug(seen, "甲"); got != want {
			t.Errorf("第 %d 次同名标题 = %q，期望 %q", i+1, got, want)
		}
	}
	// 整条标题都是标点时留不下任何字符，退到 section——它同样要去重。
	if got := uniqueSlug(seen, headingSlug("   ")); got != "section" {
		t.Errorf("空标题的标识 = %q，期望 section", got)
	}
}

// 侧栏列出全部文档，当前这一页高亮——判据是"只有一条带 active"。
func TestRenderSidebarMarksCurrentPage(t *testing.T) {
	docs := []Doc{
		{ArtifactPath: "index.html", Title: "首页"},
		{ArtifactPath: "guide/intro.html", Title: "入门"},
	}
	hrefFor := func(doc Doc) string { return "/g/prj_x/" + doc.ArtifactPath }

	sidebar := RenderSidebar(docs, hrefFor, "guide/intro.html")
	if !strings.Contains(sidebar, `href="/g/prj_x/guide/intro.html" class="active" aria-current="page">入门</a>`) {
		t.Errorf("当前页没有被标出来：%s", sidebar)
	}
	if !strings.Contains(sidebar, `href="/g/prj_x/index.html">首页</a>`) {
		t.Errorf("另一页不该带标记：%s", sidebar)
	}
	if n := strings.Count(sidebar, "active"); n != 1 {
		t.Errorf("带标记的条目 = %d 条，期望 1 条：%s", n, sidebar)
	}
}

// 没有二三级标题时目录整块不出现：一份只有一个标题的文档不该在右边留一列空白。
func TestRenderTOC(t *testing.T) {
	if toc := RenderTOC(nil); toc != "" {
		t.Errorf("没有标题时目录 = %q，期望空串", toc)
	}
	toc := RenderTOC([]Heading{{ID: "甲", Level: 2, Text: "甲"}, {ID: "乙", Level: 3, Text: "乙"}})
	for _, want := range []string{`class="lvl-2"`, `class="lvl-3"`, `href="#甲"`, `href="#乙"`, `>乙</a>`} {
		if !strings.Contains(toc, want) {
			t.Errorf("目录里找不到 %q：%s", want, toc)
		}
	}
}

// 页面外壳自带一份排版：在 <head> 里、正文之前，且明暗两套取值都在同一处。
func TestSitePageCarriesStyleSheet(t *testing.T) {
	page := string(renderSitePage(sitePage{
		Title:     "标题",
		SiteTitle: "站点",
		HomeHref:  "/g/prj_x/index.html",
		Sidebar:   "<aside class=\"sidebar\"></aside>\n",
		TOC:       "<aside class=\"toc\"></aside>\n",
		Body:      []byte("<table><tr><td>乙</td></tr></table>"),
	}))

	open := strings.Index(page, "<style>")
	closeIdx := strings.Index(page, "</style>")
	content := strings.Index(page, "<main")
	sidebar := strings.Index(page, "class=\"sidebar\"")
	if open < 0 || closeIdx < 0 {
		t.Fatalf("外壳里没有内联样式：%s", page)
	}
	// 样式要在正文与侧栏之前：放到后面会让浏览器先按默认样式排一遍再重排。
	if open >= closeIdx || closeIdx >= content || closeIdx >= sidebar {
		t.Errorf("样式的落点不对：<style> %d、</style> %d、<main> %d、侧栏 %d", open, closeIdx, content, sidebar)
	}
	style := page[open:closeIdx]
	for what, want := range map[string]string{
		"表格边线": "border-collapse",
		"斑马纹":  "nth-child(2n)",
		"暗色取值": "prefers-color-scheme:dark",
		"正文宽度": "688px",
		"顶栏规则": ".nav-title{",
	} {
		if !strings.Contains(style, want) {
			t.Errorf("样式里缺少%s（%q）", what, want)
		}
	}
	if !strings.Contains(page, "<title>标题 | 站点</title>") {
		t.Errorf("页面标题不是「页名 | 站名」：%s", page[:min(len(page), 400)])
	}
	if !strings.Contains(page, `href="/g/prj_x/index.html"`) {
		t.Error("顶栏标题没有链到入口那一页")
	}
}

// 一份清单与形态是否相称：入口、白名单、条目的类别三处。
func TestValidateManifestForSlot(t *testing.T) {
	text := Entry{Path: "index.html", Kind: EntryKindText, Digest: ContentDigest([]byte("x"))}
	asset := Entry{Path: "a.png", Kind: EntryKindAsset, AssetID: "ast_x"}

	if err := ValidateManifestForSlot(SlotSite, Manifest{text, asset}); err != nil {
		t.Errorf("相称的清单被拒: %v", err)
	}
	// 缺入口。
	if err := ValidateManifestForSlot(SlotSite, Manifest{asset}); err == nil {
		t.Error("缺入口文件的清单被接受了")
	}
	// docs 收 HTML 是错的。
	docsHTML := Entry{Path: "index.html", Kind: EntryKindText, Digest: ContentDigest([]byte("x"))}
	if err := ValidateManifestForSlot(SlotDocs, Manifest{docsHTML}); err == nil {
		t.Error("docs 形态接受了 .html 文本条目")
	}
	// 同一个路径不能既是文本又是资产。
	conflict := Entry{Path: "index.html", Kind: EntryKindAsset, AssetID: "ast_x"}
	if err := ValidateManifestForSlot(SlotSite, Manifest{conflict}); err == nil {
		t.Error("文本路径被当成了资产条目")
	}
}

// 清单本身的形状：路径合不合法、有没有重复、来源是不是恰好一个、数量超没超。
func TestNormalizeManifest(t *testing.T) {
	digest := ContentDigest([]byte("x"))
	if _, err := NormalizeManifest([]Entry{
		{Path: "index.html", Kind: EntryKindText, Digest: digest},
		{Path: "index.html", Kind: EntryKindText, Digest: digest},
	}); err == nil {
		t.Error("重复路径被接受了")
	}
	if _, err := NormalizeManifest([]Entry{
		{Path: "index.html", Kind: EntryKindText},
	}); err == nil {
		t.Error("形状不合法的摘要被接受了")
	}
	if _, err := NormalizeManifest([]Entry{
		{Path: "index.html", Kind: EntryKindAsset},
	}); err == nil {
		t.Error("缺资产标识的资产条目被接受了")
	}
	if _, err := NormalizeManifest([]Entry{
		{Path: "index.html", Kind: EntryKindText, Digest: digest, AssetID: "ast_x"},
	}); err == nil {
		t.Error("同时带摘要与资产标识的条目被接受了")
	}
	manifest, err := NormalizeManifest([]Entry{
		{Path: "b.html", Kind: EntryKindText, Digest: digest},
		{Path: "a.html", Kind: EntryKindText, Digest: digest},
	})
	if err != nil {
		t.Fatalf("合法清单被拒: %v", err)
	}
	if manifest[0].Path != "a.html" || manifest[1].Path != "b.html" {
		t.Errorf("清单顺序 = %v，期望按路径升序", manifest)
	}
}

// 内部引用的解析：两种写法都接受（站点根相对与相对当前文档）。
func TestResolveEntryPath(t *testing.T) {
	const siteRoot = "/g/prj_abc/"
	cases := []struct {
		from, dest string
		want       string
	}{
		{"index.html", "style.css", "style.css"},
		{"guide/intro.md", "../style.css", "style.css"},
		{"guide/intro.md", "notes.md", "guide/notes.md"},
		// 构建产物写的是发布根相对的绝对路径。
		{"index.html", "/g/prj_abc/assets/app.js", "assets/app.js"},
		{"index.html", "/assets/app.js", "assets/app.js"},
	}
	for _, tc := range cases {
		got, ok := resolveEntryPath(siteRoot, tc.from, tc.dest)
		if !ok || got != tc.want {
			t.Errorf("resolveEntryPath(%q, %q) = %q/%v，期望 %q/true", tc.from, tc.dest, got, ok, tc.want)
		}
	}
	// 越出根目录的解析结果落进"不合法"，于是它自然不在清单里。
	if _, ok := resolveEntryPath(siteRoot, "index.html", "../../secret"); ok {
		t.Error("越出根目录的引用被判为可解析")
	}
}

// 外部地址的判定：带 scheme、协议相对都算外部；锚点与查询串不算。
func TestIsExternalDestination(t *testing.T) {
	external := []string{"https://cdn.example.com/x.js", "http://x", "//cdn.example.com/x.js", "data:image/png;base64,AAA", "mailto:a@b.com"}
	for _, dest := range external {
		if !isExternalDestination(dest) {
			t.Errorf("%q 没被当成外部地址", dest)
		}
	}
	// 既不是外部、也不落在文件组上的那一档写法：空串，以及只有后缀的 `#小节` /
	// `?q=1`。它们**先**被 splitDestination 摘成"没有位置"，因此走不到这里；
	// 这几个取值在这里断言的是"不会被误判成外部地址"。
	notExternal := []string{"", "#anchor", "?q=1", "style.css", "/g/prj_abc/a.js"}
	for _, dest := range notExternal {
		if isExternalDestination(dest) {
			t.Errorf("%q 被当成了外部地址", dest)
		}
	}
}

func TestSplitDestination(t *testing.T) {
	cases := []struct {
		dest         string
		wantLocation string
		wantSuffix   string
	}{
		{"guide/intro.md", "guide/intro.md", ""},
		{"guide/intro.md#小节", "guide/intro.md", "#小节"},
		{"#小节", "", "#小节"},
		{"?q=1", "", "?q=1"},
		{"a.md?q=1#f", "a.md", "?q=1#f"},
		{"https://example.com/x#f", "https://example.com/x", "#f"},
		{"", "", ""},
	}
	for _, tc := range cases {
		location, suffix := splitDestination(tc.dest)
		if location != tc.wantLocation || suffix != tc.wantSuffix {
			t.Errorf("splitDestination(%q) = %q/%q，期望 %q/%q",
				tc.dest, location, suffix, tc.wantLocation, tc.wantSuffix)
		}
	}
}

// 只有后缀的引用（页内锚点、查询串）指向**这一页自己**，发布态也必须原样保留。
//
// 它们以前会被当成"站点内位置"拿去查清单，于是每一处页内跳转都变成一次发布拒绝，
// 而预览因为容忍坏引用照常显示——"预览说没问题、发布说不行"。
func TestPageInternalReferenceIsKeptVerbatim(t *testing.T) {
	const src = "# 标题\n\n## 成品概览\n\n甲。\n\n见[成品概览](#成品概览)，按[时间排](?sort=time)。\n"
	manifest := docsManifest(t, map[string]string{"index.md": src})
	site := SiteLinker{Slot: SlotDocs, Manifest: manifest, SiteRoot: "/g/prj_x/docs/"}

	for name, linker := range map[string]LinkResolver{
		"发布态": site,
		"预览态": PreviewLinker{Site: site},
	} {
		doc, err := RenderDoc(DocSource{Path: "index.md", Body: []byte(src)}, linker)
		if err != nil {
			t.Fatalf("%s：渲染被拒: %v", name, err)
		}
		hrefs := hrefsIn(doc.Body)
		for _, want := range []string{"#成品概览", "?sort=time"} {
			if !slices.Contains(hrefs, want) {
				t.Errorf("%s：正文里没有 %q 这处地址，实际有 %q", name, want, hrefs)
			}
		}
	}
}

// 带上锚点的站内引用：位置照常解析成产物地址，后缀原样接回去——它指的是那一页的
// 那一节，不是那一页的顶部。
func TestCrossDocumentAnchorKeepsSuffix(t *testing.T) {
	const index = "# 首页\n\n看[入门的小节](guide/intro.md#第一次)\n"
	const intro = "# 入门\n\n## 第一次\n"
	manifest := docsManifest(t, map[string]string{"index.md": index, "guide/intro.md": intro})
	linker := SiteLinker{Slot: SlotDocs, Manifest: manifest, SiteRoot: "/g/prj_x/docs/"}

	doc, err := RenderDoc(DocSource{Path: "index.md", Body: []byte(index)}, linker)
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	if hrefs := hrefsIn(doc.Body); !slices.Contains(hrefs, "/g/prj_x/docs/guide/intro.html#第一次") {
		t.Errorf("跨文档锚点的地址不对，实际有 %q", hrefs)
	}
	// 位置本身还是要真的在清单里——不然它就被"只有后缀"那一档顺带放过去了。
	if _, err := RenderDoc(
		DocSource{Path: "index.md", Body: []byte("# 首页\n\n[没有](guide/nope.md#第一次)\n")},
		linker,
	); err == nil {
		t.Error("指向不存在文件的锚点被放过了")
	}
}

// hrefsIn 取出正文里每一处 href 的取值，并做百分号解码。
//
// 必须解码后再比：渲染器会给地址里的非 ASCII 做百分号编码（`#成品概览` 出来是
// `#%E6%88%90…`），而浏览器在匹配 id 之前也会先解码——两边是同一处地址，直白的
// 字符串比对只会比出编码形式这个噪音。
func hrefsIn(body []byte) []string {
	var hrefs []string
	rest := string(body)
	for {
		i := strings.Index(rest, `href="`)
		if i < 0 {
			return hrefs
		}
		rest = rest[i+len(`href="`):]
		j := strings.IndexByte(rest, '"')
		if j < 0 {
			return hrefs
		}
		raw := rest[:j]
		rest = rest[j:]
		decoded, err := url.PathUnescape(raw)
		if err != nil {
			decoded = raw
		}
		hrefs = append(hrefs, decoded)
	}
}

// 取资源的引用没有"只有后缀"这一档：`![图](#某处)` 根本不是一个地址。
func TestResourceReferenceCannotBeOnlyASuffix(t *testing.T) {
	const src = "# 标题\n\n![图](#成品概览)\n"
	manifest := docsManifest(t, map[string]string{"index.md": src})
	if _, err := RenderDoc(
		DocSource{Path: "index.md", Body: []byte(src)},
		SiteLinker{Slot: SlotDocs, Manifest: manifest, SiteRoot: "/g/prj_x/docs/"},
	); err == nil {
		t.Error("指向页内锚点的图片引用被接受了")
	}
}

// docsManifest 按「路径 → 源」造一份文本条目的清单。
func docsManifest(t *testing.T, sources map[string]string) Manifest {
	t.Helper()
	entries := make([]Entry, 0, len(sources))
	for entryPath, body := range sources {
		entries = append(entries, Entry{
			Path:   entryPath,
			Kind:   EntryKindText,
			Digest: ContentDigest([]byte(body)),
		})
	}
	manifest, err := NormalizeManifest(entries)
	if err != nil {
		t.Fatalf("构造清单失败: %v", err)
	}
	return manifest
}

// 记号的识别：**恰好一个**记号才算，多一个字符就不算（未解析的 asset:// 必须
// 显式失败，而不是被静默截断）。
func TestPlaceholderID(t *testing.T) {
	if id, ok := placeholderID(" asset://abc-123 "); !ok || id != "abc-123" {
		t.Errorf("placeholderID = %q/%v，期望 abc-123/true", id, ok)
	}
	for _, value := range []string{"asset://", "asset://abc/extra", "https://x", "asset://abc def"} {
		if _, ok := placeholderID(value); ok {
			t.Errorf("%q 被当成了一个记号", value)
		}
	}
}

// 记号的识别入口给出**去重且有序**的标识，供命令行登记条目。
func TestAssetMarkerIDs(t *testing.T) {
	source := []byte(`<img src="asset://a1"><style>b{background:url(asset://b2)}</style><img srcset="asset://a1 2x">`)
	got := AssetMarkerIDs(source)
	if len(got) != 2 || got[0] != "a1" || got[1] != "b2" {
		t.Errorf("AssetMarkerIDs = %v，期望 [a1 b2]", got)
	}
}

// 记号替换是**逐字替换**：出现在属性、内联 CSS 与 srcset 里都成立。
func TestSubstituteAssetMarkersIsVerbatim(t *testing.T) {
	source := []byte(`<img src="asset://a1" srcset="asset://a1 2x"><style>b{background:url(asset://a1)}</style>`)
	out, err := SubstituteAssetMarkers(source, func(string) (string, error) { return "/g/prj_x/assets/a1", nil })
	if err != nil {
		t.Fatalf("替换失败: %v", err)
	}
	want := `<img src="/g/prj_x/assets/a1" srcset="/g/prj_x/assets/a1 2x"><style>b{background:url(/g/prj_x/assets/a1)}</style>`
	if string(out) != want {
		t.Errorf("替换结果 = %q，期望 %q", out, want)
	}
	// 没改动的字节逐字保留。
	if _, err := SubstituteAssetMarkers([]byte("<p>没有任何记号</p>"), func(string) (string, error) {
		t.Error("不该调用 resolve")
		return "", nil
	}); err != nil {
		t.Fatalf("无记号的文本替换失败: %v", err)
	}
}
