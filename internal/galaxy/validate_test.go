package galaxy

import (
	"strings"
	"testing"
)

// 占位符**逐字替换**，不解析 HTML：出现在元素属性、内联 CSS 的 url()、srcset
// 以及 <style> 块里都成立。
//
// 这条是"改写不需要枚举哪些属性算引用"的落点：依赖枚举的做法必然会漏
// （srcset、内联 CSS、将来新增的属性），而逐字替换不会漏。
func TestPlaceholderRewriteIsVerbatim(t *testing.T) {
	f := newFixture(t)
	project := f.createProject(t, "工程")

	first := f.uploadAsset(t, project.ID, "image/png", "a.png", []byte("aaa"))
	second := f.uploadAsset(t, project.ID, "image/jpeg", "b.jpg", []byte("bbb"))
	a := PlaceholderScheme + first.ID
	b := PlaceholderScheme + second.ID

	content := strings.Join([]string{
		"<!doctype html><html><body>",
		`<img src="` + a + `">`,
		`<img srcset="` + a + ` 1x, ` + b + ` 2x">`,
		`<div style="background:url(` + a + `)">x</div>`,
		`<style>body{background-image:url('` + b + `')}</style>`,
		"</body></html>",
	}, "")

	artifact := f.artifact(t, project.ID, content)

	if strings.Contains(string(artifact), PlaceholderScheme) {
		t.Errorf("产物里仍留下未改写的占位符:\n%s", artifact)
	}
	// 逐字替换：把原文里每个占位符换成它对应的地址，就应当得到产物。
	want := strings.NewReplacer(
		a, f.service.Origin().AssetURL(first.Digest),
		b, f.service.Origin().AssetURL(second.Digest),
	).Replace(content)
	if string(artifact) != want {
		t.Errorf("产物与「只把占位符换掉」的结果不同:\n实际 %s\n期望 %s", artifact, want)
	}
}

// 产物与用户正文的差异**恰好是占位符被替换**：不包裹、不补全、不注入。
func TestArtifactDiffersOnlyByPlaceholders(t *testing.T) {
	f := newFixture(t)
	project := f.createProject(t, "工程")
	asset := f.uploadAsset(t, project.ID, "image/png", "a.png", []byte("aaa"))
	a := PlaceholderScheme + asset.ID

	// 刻意是一份"不合法"的 HTML：缺闭合标签、没有 doctype、没有 body。
	// 正文就是用户写的全部内容，服务端不做任何补全。
	content := `<div>缺闭合<img src="` + a + `">`
	artifact := f.artifact(t, project.ID, content)

	want := strings.ReplaceAll(content, a, f.service.Origin().AssetURL(asset.Digest))
	if string(artifact) != want {
		t.Errorf("产物被改动过:\n实际 %s\n期望 %s", artifact, want)
	}
}

// 指向不存在的、别的工程的资产，一律是校验期的拒绝理由，且指出是哪一个。
func TestUnresolvablePlaceholdersAreRejected(t *testing.T) {
	f := newFixture(t)
	mine := f.createProject(t, "我的")
	theirs := f.createProject(t, "别人的")
	foreign := f.uploadAsset(t, theirs.ID, "image/png", "a.png", []byte("aaa"))

	cases := []struct {
		name    string
		content string
		missing string
	}{
		{"不存在的资产", `<img src="asset://ast_不存在">`, "ast_不存在"},
		{"别的工程的资产", `<img src="` + PlaceholderScheme + foreign.ID + `">`, foreign.ID},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			report := f.report(t, testOwner, mine.ID, tc.content)
			if report.OK() {
				t.Fatal("期望被拒，实际通过了")
			}
			message := problems(report)
			if !strings.Contains(message, tc.missing) {
				t.Errorf("问题 %q 没有指出是哪一个资产（%s）", message, tc.missing)
			}
			// 消息里带行号：只说"有引用不合法"会让用户在一份几百行的正文里自己找。
			if !strings.Contains(message, "第 1 行") {
				t.Errorf("问题 %q 没有指出位置", message)
			}
		})
	}
}

// **取资源的位置不得指向本工程资产库之外**；而 `<a href>` 是导航链接，不受此限。
//
// 这条区分回答了"只能引用资产库里的资产"到底在禁什么：禁的是让页面去别处取
// 字节，不是禁止用户写出一个外部链接。
func TestExternalResourceReferencesAreRejected(t *testing.T) {
	f := newFixture(t)
	project := f.createProject(t, "工程")
	asset := f.uploadAsset(t, project.ID, "image/png", "a.png", []byte("aaa"))
	placeholder := PlaceholderScheme + asset.ID

	rejected := []struct {
		name    string
		content string
		value   string
	}{
		{"绝对地址的图片", `<img src="https://evil.example.com/a.png">`, "https://evil.example.com/a.png"},
		{"http", `<img src="http://evil.example.com/a.png">`, "http://evil.example.com/a.png"},
		{"协议相对地址", `<img src="//evil.example.com/a.png">`, "//evil.example.com/a.png"},
		{"data 形式的图片", `<img src="data:image/png;base64,AAAA">`, "data:image/png;base64,AAAA"},
		{"相对路径", `<img src="local.png">`, "local.png"},
		{"视频", `<video src="https://evil.example.com/v.mp4"></video>`, "https://evil.example.com/v.mp4"},
		{"srcset", `<img srcset="https://evil.example.com/a.png 1x">`, "https://evil.example.com/a.png"},
		{"内联样式里的 url()", `<div style="background:url(https://evil.example.com/a.png)">x</div>`, "https://evil.example.com/a.png"},
		{"<style> 块里的 url()", `<style>body{background:url(https://evil.example.com/a.png)}</style>`, "https://evil.example.com/a.png"},
		{"外链样式表", `<link rel="stylesheet" href="https://evil.example.com/a.css">`, "https://evil.example.com/a.css"},
		{"外链脚本", `<script src="https://evil.example.com/a.js"></script>`, "https://evil.example.com/a.js"},
	}
	for _, tc := range rejected {
		t.Run("拒绝/"+tc.name, func(t *testing.T) {
			report := f.report(t, testOwner, project.ID, tc.content)
			if report.OK() {
				t.Fatal("期望被拒，实际通过了")
			}
			if message := problems(report); !strings.Contains(message, tc.value) {
				t.Errorf("问题 %q 没有指出那一处（%s）", message, tc.value)
			}
		})
	}

	allowed := []struct {
		name    string
		content string
	}{
		{"外部链接", `<a href="https://example.com">去看看</a>`},
		{"外部链接带占位符的同页图片", `<a href="https://example.com"><img src="` + placeholder + `"></a>`},
		{"注释里的地址", `<!-- <img src="https://evil.example.com/a.png"> -->`},
		{"脚本里的字符串", `<script>var s = '<img src="https://evil.example.com/a.png">';</script>`},
		{"普通文本里的地址", `<p>见 https://example.com 的说明</p>`},
		{"空取值的属性", `<img src="">`},
	}
	for _, tc := range allowed {
		t.Run("允许/"+tc.name, func(t *testing.T) {
			if report := f.report(t, testOwner, project.ID, tc.content); !report.OK() {
				t.Errorf("被拒了: %s", problems(report))
			}
		})
	}
}

// 占位符的边界由**标识的字母表**决定：多一个字符就不是一个占位符，因此它会被
// 当成一处外部资源引用报出来，而不是被静默改写掉前半截。
func TestPlaceholderBoundary(t *testing.T) {
	assetID := "ast_abcDEF-123_xyz"
	f := newFixture(t)
	project := f.createProject(t, "工程")

	// 恰好一个占位符：识别得出来，且指向的资源必须存在。
	if got := ReferencedAssetIDs(Document(PlaceholderScheme + assetID)); len(got) != 1 || got[0] != assetID {
		t.Errorf("识别 = %v，期望 [%s]", got, assetID)
	}
	// 后面多了一个 `/`：它不再是一个占位符。
	if got := ReferencedAssetIDs(Document(PlaceholderScheme + assetID + "/extra")); len(got) != 1 {
		t.Errorf("识别 = %v，边界之后的字符应当仍属于标识的字母表之外", got)
	}
	report := f.report(t, testOwner, project.ID, `<img src="`+PlaceholderScheme+assetID+`/extra">`)
	if report.OK() {
		t.Error("一个不是占位符的取值被放过了")
	}
	// 前后空白容忍。
	if got := ReferencedAssetIDs(Document(`asset://` + assetID + ` `)); len(got) != 1 || got[0] != assetID {
		t.Errorf("识别 = %v，期望容忍尾部空白", got)
	}
}

// 正文与产物各有一档体积上限：正文那一条在编辑期就要拦住，产物那一条在发布前拦。
func TestDocumentAndArtifactSizeLimits(t *testing.T) {
	f := newFixture(t)
	project := f.createProject(t, "工程")

	// 正文超限：保存草稿与发布校验都拒。
	huge := Document(strings.Repeat("x", MaxDocumentBytes+1))
	if _, err := f.service.SaveDraft(t.Context(), testOwner, project.ID, huge); err == nil {
		t.Error("超限的正文被保存了")
	}
	if _, _, err := f.service.validateDocument(t.Context(), project.ID, huge); err != nil {
		t.Fatalf("校验出错: %v", err)
	}

	// 产物超限：正文合法、但改写之后超过产物的上限。
	//
	// 改写会把每个占位符换成一条完整的公开地址，因此一页引用很多次时产物会明显
	// 长于正文。这里按"每次引用增长多少"算出需要的引用次数，让正文仍在上限内、
	// 而产物越过去。
	asset := f.uploadAsset(t, project.ID, "image/png", "a.png", []byte("aaa"))
	reference := `<img src="` + PlaceholderScheme + asset.ID + `">`
	address := f.service.Origin().AssetURL(asset.Digest)
	repeats := MaxArtifactBytes/(len(address)+len(`<img src="">`)) + 1
	content := Document(strings.Repeat(reference, repeats))
	if len(content) > MaxDocumentBytes {
		t.Fatalf("夹具的正文 %d 字节已经超过正文上限（%d），测的就不是产物上限了",
			len(content), MaxDocumentBytes)
	}
	_, report, err := f.service.validateDocument(t.Context(), project.ID, content)
	if err != nil {
		t.Fatalf("校验出错: %v", err)
	}
	if report.OK() {
		t.Fatal("产物超限却没有被拒")
	}
	if !strings.Contains(problems(report), "发布产物") {
		t.Errorf("问题 %q 没有指出是产物超限", problems(report))
	}
}

// 校验的**唯一入口**同时服务编辑器与发布：同一个输入在两处得到同一个结论。
func TestValidationEntryIsShared(t *testing.T) {
	f := newFixture(t)
	project := f.createProject(t, "工程")
	content := Document(`<img src="asset://ast_不存在">`)

	// 编辑器那条：拿回问题清单。
	editor := f.report(t, testOwner, project.ID, string(content))
	// 发布那条：同一个结论变成了拒绝。
	f.saveDraft(t, project.ID, string(content))
	version := f.saveVersion(t, project.ID)
	_, publishErr := f.service.Publish(t.Context(), testOwner, project.ID, version.ID)
	if publishErr == nil {
		t.Fatal("发布通过了，编辑器却说有问题——两处用了两套规则")
	}

	editorProblems := problems(editor)
	for _, message := range editor.Messages() {
		if !strings.Contains(publishErr.Error(), message) {
			t.Errorf("发布错误 %q 里没有编辑器的结论 %q", publishErr, message)
		}
	}
	if !strings.Contains(editorProblems, "ast_不存在") {
		t.Errorf("编辑器的结论 %q 没有指出那一处", editorProblems)
	}
}
