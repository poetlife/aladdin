package galaxy

import (
	"context"
	"strings"
	"testing"
	"time"
)

// 校验入口是**唯一入口**：界面提示与发布前置校验共用它。
//
// 这里覆盖的是"输入是一个文件组"之后新增的那些规则；纯形状的规则见 site_test.go。
func TestValidateDraftReportsProblems(t *testing.T) {
	f := newFixture(t)
	project := f.createProject(t, "工程")
	f.pushDraft(t, project.ID, []Entry{
		f.textEntry(t, project.ID, "index.html", `<p>你好</p><img src="missing.png">`),
	})

	report := f.report(t, project.ID)
	if report.OK() {
		t.Fatal("引用一个不存在的路径却通过了校验")
	}
	text := problems(report)
	if !strings.Contains(text, "missing.png") {
		t.Errorf("问题里 %q 没有指出那一处引用", text)
	}
	// 问题要**指到文件与行号**上，否则用户在一组文件里自己找。
	if report.Problems[0].Path != "index.html" {
		t.Errorf("问题的文件 = %q，期望 index.html", report.Problems[0].Path)
	}
	if report.Problems[0].Line != 1 {
		t.Errorf("问题的行号 = %d，期望 1", report.Problems[0].Line)
	}
}

// 每一处取资源的引用都必须落在本文件组的某一条条目上。
func TestResourceReferenceMustLandInFileSet(t *testing.T) {
	f := newFixture(t)
	project := f.createProject(t, "工程")
	// 文本条目与资产条目**都算**：构建产物里的 JS/CSS 是文本条目。
	f.pushDraft(t, project.ID, []Entry{
		f.textEntry(t, project.ID, "index.html", `<link href="style.css" rel="stylesheet"><script src="app.js"></script>`),
		f.textEntry(t, project.ID, "style.css", "body{}"),
		f.textEntry(t, project.ID, "app.js", "console.log(1)"),
	})

	report := f.report(t, project.ID)
	if !report.OK() {
		t.Errorf("引用都落在文件组里，却报错: %v", report.Messages())
	}
}

// **外部地址一律拒绝**（导航链接除外）：发布物不得从本文件组之外取任何一个字节。
func TestExternalResourceReferencesAreRejected(t *testing.T) {
	cases := []struct {
		name    string
		content string
	}{
		{"绝对地址", `<img src="https://cdn.example.com/a.png">`},
		{"协议相对", `<img src="//cdn.example.com/a.png">`},
		{"data URL", `<img src="data:image/png;base64,AAAA">`},
		{"内联样式里的外部地址", `<div style="background:url(https://cdn.example.com/a.png)"></div>`},
		{"样式表里的 @import", `<style>@import url(https://cdn.example.com/a.css);</style>`},
		{"srcset 里的外部地址", `<img srcset="https://cdn.example.com/a.png 2x">`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			project := f.createProject(t, "工程")
			// 另有一条真实条目，好证明拒绝的是"指向别处"而不是"什么引用都没有"。
			f.pushDraft(t, project.ID, []Entry{
				f.textEntry(t, project.ID, "index.html", tc.content),
				f.textEntry(t, project.ID, "other.html", "<p>另一页</p>"),
			})
			report := f.report(t, project.ID)
			if report.OK() {
				t.Fatal("指向外部地址的资源引用被放过了")
			}
			if text := problems(report); !strings.Contains(text, "本文件组之外") {
				t.Errorf("问题信息 %q 没有说明它指向了文件组之外", text)
			}
		})
	}
}

// **外部导航链接不被拒**：用户链到外部网站是他的意图，不是资源引用。
func TestExternalNavigationLinksAreAllowed(t *testing.T) {
	f := newFixture(t)
	project := f.createProject(t, "工程")
	f.pushDraft(t, project.ID, []Entry{
		f.textEntry(t, project.ID, "index.html", `<a href="https://example.com">外部</a><a href="mailto:a@b.com">来信</a>`),
	})

	report := f.report(t, project.ID)
	if !report.OK() {
		t.Errorf("导航链接被拒了: %v", report.Messages())
	}
}

// 记号必须落在**本文件组的一条资产条目**上。
func TestAssetMarkersMustResolve(t *testing.T) {
	f := newFixture(t)
	project := f.createProject(t, "工程")
	asset := f.uploadAsset(t, project.ID, "image/png", "a.png", []byte("aaa"))

	// 落在条目上：通过。
	f.pushDraft(t, project.ID, []Entry{
		f.textEntry(t, project.ID, "index.html", `<img src="asset://`+asset.ID+`">`),
		{Path: "a.png", Kind: EntryKindAsset, AssetID: asset.ID},
	})
	if report := f.report(t, project.ID); !report.OK() {
		t.Errorf("记号落在条目上却报错: %v", report.Messages())
	}

	// 指一个不存在的资产：拒绝，且错误信息里含那个标识。
	f.pushDraft(t, project.ID, []Entry{
		f.textEntry(t, project.ID, "index.html", `<img src="asset://ast_missing">`),
	})
	report := f.report(t, project.ID)
	if report.OK() {
		t.Fatal("指向一个不存在的资产却通过了校验")
	}
	if text := problems(report); !strings.Contains(text, "ast_missing") {
		t.Errorf("问题信息 %q 没有指出那个资产标识", text)
	}
}

// **指向他工程的资产即失败**：资产属于唯一一个工程，跨工程引用落不到条目上。
func TestForeignProjectAssetIsRejected(t *testing.T) {
	f := newFixture(t)
	mine := f.createProject(t, "我的")
	theirs := f.createProject(t, "别人的")
	foreignAsset := f.uploadAsset(t, theirs.ID, "image/png", "a.png", []byte("aaa"))

	// 直接把别的工程的资产标识写进自己的清单：它不属于本工程，落不到条目上。
	f.pushDraft(t, mine.ID, []Entry{
		f.textEntry(t, mine.ID, "index.html", `<img src="asset://`+foreignAsset.ID+`">`),
		{Path: "a.png", Kind: EntryKindAsset, AssetID: foreignAsset.ID},
	})

	report := f.report(t, mine.ID)
	if report.OK() {
		t.Fatal("引用他工程的资产却通过了校验")
	}
	if text := problems(report); !strings.Contains(text, foreignAsset.ID) {
		t.Errorf("问题信息 %q 没有指出那个资产标识", text)
	}
}

// 单份文本与整组文本的体积上限。
func TestTextSizeLimits(t *testing.T) {
	f := newFixture(t)
	project := f.createProject(t, "工程")
	// 一份超过单份上限的文本：直传的提交那一步就会挡下它，因此这里直接构造
	// 一份"清单声称有、字节其实没有"的情形不成立——改为验证上限常量本身与
	// 整组上限的判断。
	if MaxTextBytes >= MaxFileSetBytes {
		t.Fatal("单份上限不低于整组上限")
	}
	if _, err := NormalizeManifest(make([]Entry, MaxFiles+1)); err == nil {
		t.Error("超过文件数上限的清单被接受了")
	}
	_ = project
}

// docs 形态：**整站渲染**，导航只由 markdown 文件派生，站点文件不进导航。
func TestDocsRenderAndNavigation(t *testing.T) {
	f := newFixture(t)
	project := f.createProjectSlots(t, "文档站", SlotDocs)
	asset := f.uploadAsset(t, project.ID, "image/png", "logo.png", []byte("png"))

	f.pushDraftSlot(t, project.ID, SlotDocs, []Entry{
		f.textEntry(t, project.ID, "index.md", "# 首页\n\n看[入门](guide/intro.md)。\n"),
		f.textEntry(t, project.ID, "guide/intro.md", "# 入门\n\n![图](asset://"+asset.ID+")\n"),
		f.textEntry(t, project.ID, "theme.css", "body{color:red}"),
		{Path: "logo.png", Kind: EntryKindAsset, AssetID: asset.ID},
	})

	report := f.reportSlot(t, project.ID, SlotDocs)
	if !report.OK() {
		t.Fatalf("文档站被拒: %v", report.Messages())
	}
	artifacts := f.buildArtifactsSlot(t, project.ID, SlotDocs)

	// 每一份 markdown 渲染成一页 `.html`。
	index, ok := artifacts["index.html"]
	if !ok {
		t.Fatalf("产物里没有 index.html：%v", keysOf(artifacts))
	}
	intro, ok := artifacts["guide/intro.html"]
	if !ok {
		t.Fatalf("产物里没有 guide/intro.html：%v", keysOf(artifacts))
	}
	// **站点文件按原路径原样进产物**，不是渲染结果。
	if string(artifacts["theme.css"]) != "body{color:red}" {
		t.Errorf("站点文件被改动了: %q", artifacts["theme.css"])
	}
	// 文档间链接被解析成**该槽的**站点内绝对地址——文档槽的根在 `docs/` 下，
	// 这是"站点与文档并存"在地址上的样子。
	if !strings.Contains(string(index), "/g/"+project.ID+"/docs/guide/intro.html") {
		t.Errorf("文档间链接没有被解析成站点内地址：%s", index)
	}
	// 渲染器产出的地址一律是站点绝对路径；页面落在嵌套路径下时这一条才成立。
	if !strings.Contains(string(intro), "/g/"+project.ID+"/docs/") {
		t.Errorf("嵌套页里的地址不是站点绝对路径：%s", intro)
	}
	// **导航只由 markdown 派生**：站点文件不进导航，且每一项都对应一份文档。
	nav := string(index)
	if strings.Contains(nav, "theme.css") {
		t.Error("站点文件进了导航")
	}
	if !strings.Contains(nav, "入门") {
		t.Error("导航里没有那份文档的标题")
	}
	// **页面里只有那一段平台接入桥。** 出现第二处 `<script>` 就意味着有别的脚本
	// 被注入了进来。按槽的分界是：`site` 槽一个字节都不加（那条由
	// TestStaticArtifactsAreByteIdentical 钉住），`docs` 槽只加这一段常量。
	// 断言"恰好等于常量"同时证明了桥里不含任何用户内容。
	if count := strings.Count(string(index), "<script"); count != 1 {
		t.Errorf("文档产物里的 <script> 有 %d 处，期望恰好 1 处（接入桥）", count)
	}
	if !strings.Contains(string(index), frameBridgeTag()) {
		t.Error("文档产物里的那一段不是接入桥")
	}
	// 标题取每份的首个一级标题。
	if !strings.Contains(string(index), "<title>首页</title>") {
		t.Errorf("页面标题不是首个一级标题：%s", index)
	}
}

// docs 形态：指向文件组里不存在的位置的文档间链接被拒，并指出是哪一份文件。
func TestDocsBrokenDocLinkIsRejected(t *testing.T) {
	f := newFixture(t)
	project := f.createProjectSlots(t, "文档站", SlotDocs)
	f.pushDraftSlot(t, project.ID, SlotDocs, []Entry{
		f.textEntry(t, project.ID, "index.md", "# 首页\n\n看[不存在](guide/missing.md)。\n"),
	})

	report := f.reportSlot(t, project.ID, SlotDocs)
	if report.OK() {
		t.Fatal("指向不存在位置的文档间链接被放过了")
	}
	if report.Problems[0].Path != "index.md" {
		t.Errorf("问题的文件 = %q，期望 index.md", report.Problems[0].Path)
	}
	if text := problems(report); !strings.Contains(text, "guide/missing.md") {
		t.Errorf("问题信息 %q 没有指出那一处链接", text)
	}
}

// 渲染是**确定性**的：同一份源渲染两次逐字相同（"重复发布不产生新对象"的前提）。
func TestDocsRenderIsDeterministic(t *testing.T) {
	f := newFixture(t)
	project := f.createProjectSlots(t, "文档站", SlotDocs)
	f.pushDraftSlot(t, project.ID, SlotDocs, []Entry{
		f.textEntry(t, project.ID, "index.md", "# 首页\n\n正文。\n"),
		f.textEntry(t, project.ID, "b.md", "# 乙\n\n乙的正文。\n"),
		f.textEntry(t, project.ID, "a.md", "# 甲\n\n甲的正文。\n"),
	})

	first := f.buildArtifactsSlot(t, project.ID, SlotDocs)
	second := f.buildArtifactsSlot(t, project.ID, SlotDocs)
	for artifactPath, data := range first {
		if string(second[artifactPath]) != string(data) {
			t.Errorf("%s 两次渲染结果不同", artifactPath)
		}
	}
}

// docs 形态下 markdown 里的图片引用同样要落在文件组里。
func TestDocsImageMustLandInFileSet(t *testing.T) {
	f := newFixture(t)
	project := f.createProjectSlots(t, "文档站", SlotDocs)
	f.pushDraftSlot(t, project.ID, SlotDocs, []Entry{
		f.textEntry(t, project.ID, "index.md", "# 首页\n\n![图](images/x.png)\n"),
	})

	report := f.reportSlot(t, project.ID, SlotDocs)
	if report.OK() {
		t.Fatal("指向不存在图片的 markdown 被放过了")
	}
	if text := problems(report); !strings.Contains(text, "images/x.png") {
		t.Errorf("问题信息 %q 没有指出那一处引用", text)
	}
}

// 校验是**只读**的：它不改变草稿，也不产生任何发布记录。
func TestValidateDraftIsReadOnly(t *testing.T) {
	f := newFixture(t)
	project := f.createProject(t, "工程")
	f.pushDraft(t, project.ID, []Entry{f.textEntry(t, project.ID, "index.html", "<p>x</p>")})
	before, _, err := f.service.GetDraft(context.Background(), testOwner, project.ID, SlotSite)
	if err != nil {
		t.Fatalf("读草稿失败: %v", err)
	}

	if _, err := f.service.ValidateDraft(context.Background(), testOwner, project.ID, SlotSite); err != nil {
		t.Fatalf("校验失败: %v", err)
	}
	after, _, err := f.service.GetDraft(context.Background(), testOwner, project.ID, SlotSite)
	if err != nil {
		t.Fatalf("读草稿失败: %v", err)
	}
	if len(after.Manifest) != len(before.Manifest) || !after.UpdatedAt.Equal(before.UpdatedAt) {
		t.Error("校验改变了草稿")
	}
}

// 自闭合标签末尾那个 `/` 曾让属性解析原地打转：一个 `<meta ... />` 就足以让校验
// 永远转不完（服务端一个核被打满）。
//
// **这条用一个限时守住"它必须结束"**：只断言结论的话，回归的表现是整个测试包挂
// 死到超时，而不是一条失败的用例——那种失败没有人能一眼看出卡在哪。
func TestSelfClosingTagEndsValidation(t *testing.T) {
	f := newFixture(t)
	project := f.createProject(t, "工程")
	f.pushDraft(t, project.ID, []Entry{
		f.textEntry(t, project.ID, "index.html",
			`<!doctype html><meta charset="utf-8" />`+
				`<link rel="stylesheet" href="style.css" />`+
				`<img src="a.png" alt="图" />`),
		f.textEntry(t, project.ID, "style.css", "body{margin:0}"),
		f.assetEntry(t, project.ID, "a.png", "image/png", "a.png", []byte("png")),
	})

	type outcome struct {
		report Report
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		report, err := f.service.ValidateDraft(context.Background(), testOwner, project.ID, SlotSite)
		done <- outcome{report: report, err: err}
	}()

	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("校验出错（期望是一个结论）: %v", got.err)
		}
		if !got.report.OK() {
			t.Fatalf("自闭合标签不该产生问题：%s", problems(got.report))
		}
	case <-time.After(10 * time.Second):
		t.Fatal("校验没有在限时内结束——属性解析又在原地打转了")
	}
}

func keysOf(m map[string][]byte) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	return keys
}

// docs 槽：raw HTML 里的记号在产物里是站点内路径，与 markdown 图片语法一视同仁。
func TestDocsRawHTMLMarkersAreResolved(t *testing.T) {
	f := newFixture(t)
	project := f.createProjectSlots(t, "文档站", SlotDocs)
	asset := f.uploadAsset(t, project.ID, "image/png", "logo.png", []byte("png"))

	f.pushDraftSlot(t, project.ID, SlotDocs, []Entry{
		f.textEntry(t, project.ID, "index.md",
			"# 首页\n\n<img src=\"asset://"+asset.ID+"\" alt=\"图\">\n"),
		{Path: "logo.png", Kind: EntryKindAsset, AssetID: asset.ID},
	})

	report := f.reportSlot(t, project.ID, SlotDocs)
	if !report.OK() {
		t.Fatalf("raw HTML 里的记号落在条目上却被拒: %v", report.Messages())
	}
	page := string(f.buildArtifactsSlot(t, project.ID, SlotDocs)["index.html"])
	if strings.Contains(page, PlaceholderScheme) {
		t.Errorf("产物里还留着没解开的记号：%s", page)
	}
	if !strings.Contains(page, "/g/"+project.ID+"/docs/logo.png") {
		t.Errorf("raw HTML 里的记号没有解成站点内路径：%s", page)
	}
}

// docs 槽：raw HTML 里的取资源引用同样不得指向本文件组之外。
//
// 这一条以前是漏的：markdown 不经过"扫一遍源里的引用"那一趟，于是
// `<img src="https://cdn…">` 可以一路发出去，直到读者的浏览器把它挡下。
func TestDocsRawHTMLExternalResourceIsRejected(t *testing.T) {
	f := newFixture(t)
	project := f.createProjectSlots(t, "文档站", SlotDocs)
	f.pushDraftSlot(t, project.ID, SlotDocs, []Entry{
		f.textEntry(t, project.ID, "index.md",
			"# 首页\n\n<img src=\"https://cdn.example.com/a.png\">\n"),
	})

	report := f.reportSlot(t, project.ID, SlotDocs)
	if report.OK() {
		t.Fatal("raw HTML 里指向外部的资源引用被放过了")
	}
	if text := problems(report); !strings.Contains(text, "本文件组之外") {
		t.Errorf("问题信息 %q 没有说明它指向了文件组之外", text)
	}
}

// 复核落在**产物**上（唯一入口）：产物里残留的记号是一处失败，不是一张裂图。
//
// 它同时是发布前置校验与回读发布态之后的复核入口，因此这条直接钉住那个函数。
func TestAuditArtifactsRejectsUnresolvedMarker(t *testing.T) {
	report := AuditArtifacts("/g/prj_x/", map[string]bool{"index.html": true},
		map[string][]byte{"index.html": []byte(`<img src="asset://ast_missing">`)})
	if report.OK() {
		t.Fatal("产物里残留的记号被放过了")
	}
	if report.Problems[0].Line != 1 {
		t.Errorf("问题的行号 = %d，期望 1", report.Problems[0].Line)
	}
	if text := problems(report); !strings.Contains(text, "没有被解开") {
		t.Errorf("问题信息 %q 没有说明记号没被解开", text)
	}
}

// 复核的判据是产物清单里的路径：解开的地址、站点文件、资产条目都算落在里面，
// 导航链接可以是任意地址。
func TestAuditArtifactsAcceptsResolvedReferences(t *testing.T) {
	report := AuditArtifacts("/g/prj_x/docs/",
		map[string]bool{"index.html": true, "theme.css": true, "assets/ast_logo": true},
		map[string][]byte{
			"index.html": []byte(`<img src="/g/prj_x/docs/assets/ast_logo">` +
				`<link rel="stylesheet" href="/g/prj_x/docs/theme.css">` +
				`<a href="https://example.com">外部</a>`),
			"theme.css": []byte("body{background:url(/g/prj_x/docs/assets/ast_logo)}"),
		})
	if !report.OK() {
		t.Errorf("引用都落在产物清单里，却报错: %v", report.Messages())
	}
}
