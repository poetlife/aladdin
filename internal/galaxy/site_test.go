package galaxy

import "testing"

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
		form      SiteForm
		entryPath string
		want      bool
	}{
		{SiteFormStatic, "index.html", true},
		{SiteFormStatic, "app.js", true},
		{SiteFormStatic, "logo.svg", true},
		{SiteFormStatic, "photo.png", false},
		{SiteFormDocs, "index.md", true},
		{SiteFormDocs, "theme.css", true},
		// **docs 不收 HTML**：它的页面是渲染出来的，不是写出来的。
		{SiteFormDocs, "index.html", false},
		{SiteFormDocs, "photo.png", false},
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
		form      SiteForm
		entryPath string
		want      string
	}{
		{SiteFormDocs, "index.md", "index.html"},
		{SiteFormDocs, "guide/intro.md", "guide/intro.html"},
		{SiteFormDocs, "theme.css", "theme.css"},
		{SiteFormStatic, "index.html", "index.html"},
		{SiteFormStatic, "assets/app.js", "assets/app.js"},
	}
	for _, tc := range cases {
		if got := ArtifactPath(tc.form, tc.entryPath); got != tc.want {
			t.Errorf("ArtifactPath(%s, %q) = %q，期望 %q", tc.form, tc.entryPath, got, tc.want)
		}
	}
}

// 渲染出来的页是 `.html`，而它**不在 docs 的写入白名单里**——两者不是同一个判断。
func TestArtifactContentTypeServesRenderedPages(t *testing.T) {
	contentType, ok := ArtifactContentType(SiteFormDocs, "guide/intro.html")
	if !ok || contentType != renderedContentType {
		t.Errorf("ArtifactContentType(docs, guide/intro.html) = %q/%v，期望 %q/true",
			contentType, ok, renderedContentType)
	}
	if _, ok := TextContentType(SiteFormDocs, "guide/intro.html"); ok {
		t.Error("写进来的 .html 不该被当成合法文本条目")
	}
}

// 一份清单与形态是否相称：入口、白名单、条目的类别三处。
func TestValidateManifestForForm(t *testing.T) {
	text := Entry{Path: "index.html", Kind: EntryKindText, Digest: ContentDigest([]byte("x"))}
	asset := Entry{Path: "a.png", Kind: EntryKindAsset, AssetID: "ast_x"}

	if err := ValidateManifestForForm(SiteFormStatic, Manifest{text, asset}); err != nil {
		t.Errorf("相称的清单被拒: %v", err)
	}
	// 缺入口。
	if err := ValidateManifestForForm(SiteFormStatic, Manifest{asset}); err == nil {
		t.Error("缺入口文件的清单被接受了")
	}
	// docs 收 HTML 是错的。
	docsHTML := Entry{Path: "index.html", Kind: EntryKindText, Digest: ContentDigest([]byte("x"))}
	if err := ValidateManifestForForm(SiteFormDocs, Manifest{docsHTML}); err == nil {
		t.Error("docs 形态接受了 .html 文本条目")
	}
	// 同一个路径不能既是文本又是资产。
	conflict := Entry{Path: "index.html", Kind: EntryKindAsset, AssetID: "ast_x"}
	if err := ValidateManifestForForm(SiteFormStatic, Manifest{conflict}); err == nil {
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
	internal := []string{"", "#anchor", "?q=1", "style.css", "/g/prj_abc/a.js"}
	for _, dest := range internal {
		if isExternalDestination(dest) {
			t.Errorf("%q 被当成了外部地址", dest)
		}
	}
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
