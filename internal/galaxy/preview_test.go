package galaxy

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// previewURL 拿一次预览地址并把它拆回形状。
//
// 断言用的是**形状**而不是拼字符串：客户端与测试都不该自己拼这条地址（见
// previewURL 的说明），因此这里从服务端给的那一条里拆出凭证。
func (f *fixture) previewURL(t *testing.T, projectID, entryPath string) (token, projectIDOut, entryPathOut string) {
	t.Helper()
	url, err := f.service.PreviewDraft(context.Background(), testOwner, projectID, entryPath)
	if err != nil {
		t.Fatalf("签发预览地址失败: %v", err)
	}
	if !strings.HasPrefix(url, testPageOrigin) {
		t.Fatalf("预览地址不在发布域上: %q", url)
	}
	token, projectIDOut, entryPathOut, ok := SplitPreviewPath(strings.TrimPrefix(url, testPageOrigin))
	if !ok {
		t.Fatalf("服务端给出的预览地址不符合通道形状: %q", url)
	}
	return token, projectIDOut, entryPathOut
}

// previewTarget 按路径取一次预览内容。
func (f *fixture) previewTarget(t *testing.T, token, projectID, entryPath string) PreviewTarget {
	t.Helper()
	target, err := f.service.OpenPreview(context.Background(), token, projectID, entryPath)
	if err != nil {
		t.Fatalf("取预览内容失败（%s）: %v", entryPath, err)
	}
	return target
}

// TestPreviewServesDraftSiteByPath 钉住生产实例的那一个形状。
//
// 一份构建产物式的 `static` 文件组：入口用**相对路径**引用了样式、脚本与图片
// （图片是一条资产条目）。预览必须让浏览器把这些都取到——这正是"文档有自己的
// 地址"买来的东西。此前预览把单份文件塞进 `srcdoc`，那三种引用一处也解析不了。
func TestPreviewServesDraftSiteByPath(t *testing.T) {
	f := newFixture(t)
	project := f.createProject(t, "道士下山·第一视角连环画")
	image := f.assetEntry(t, project.ID, "img/pov-01.jpg", "image/jpeg", "pov-01.jpg", []byte("jpeg-bytes"))
	f.pushDraft(t, project.ID, []Entry{
		f.textEntry(t, project.ID, "index.html", `<link rel="stylesheet" href="style.css"><img src="img/pov-01.jpg"><script src="app.js"></script>`),
		f.textEntry(t, project.ID, "style.css", "body{color:#222}"),
		f.textEntry(t, project.ID, "app.js", "console.log(1)"),
		image,
	})

	token, projectID, entryPath := f.previewURL(t, project.ID, "")
	if entryPath != "index.html" {
		t.Fatalf("入口地址应指向入口文件，实际是 %q", entryPath)
	}

	// 入口：文本由服务端在自然路径上给出，类型按扩展名派生。
	entry := f.previewTarget(t, token, projectID, "")
	if entry.ContentType != "text/html; charset=utf-8" {
		t.Fatalf("入口的类型是 %q", entry.ContentType)
	}
	if !strings.Contains(string(entry.Data), `href="style.css"`) {
		t.Fatalf("入口内容被改动了: %s", entry.Data)
	}

	// 兄弟文件：同一条通道按路径取到，且**类型不是一个中性值**——内容对象在桶上
	// 是 `application/octet-stream`，浏览器不会拿它当样式或脚本用。
	style := f.previewTarget(t, token, projectID, "style.css")
	if style.ContentType != "text/css; charset=utf-8" || string(style.Data) != "body{color:#222}" {
		t.Fatalf("样式没按路径取到: %q %q", style.ContentType, style.Data)
	}
	script := f.previewTarget(t, token, projectID, "app.js")
	if script.ContentType != "text/javascript; charset=utf-8" || string(script.Data) != "console.log(1)" {
		t.Fatalf("脚本没按路径取到: %q %q", script.ContentType, script.Data)
	}

	// 资产：给一个短时地址由浏览器直连，且**指向私有区**而不是公开区。
	asset := f.previewTarget(t, token, projectID, "img/pov-01.jpg")
	if asset.RedirectURL == "" {
		t.Fatal("资产条目应该给一个重定向地址")
	}
	if !strings.Contains(asset.RedirectURL, "/assets/") || strings.Contains(asset.RedirectURL, releaseKeyPrefix) {
		t.Fatalf("预览里的资产应指向私有区，实际是 %q", asset.RedirectURL)
	}

	// 不在草稿里的路径：与"这一页不存在"同一个结论。
	if _, err := f.service.OpenPreview(context.Background(), token, projectID, "nope.css"); !errors.Is(err, ErrPreviewNotFound) {
		t.Fatalf("草稿里没有的路径应得到否定结论，实际是 %v", err)
	}
}

// TestPreviewRewritesPublishRootToPreviewRoot 钉住那一处改写。
//
// 构建产物写的是**站点绝对地址**（命令行把发布根注入进去，见 cli.md），它必须落在
// 这个工程的**草稿**上，而不是已发布的那一版或一个不存在的位置。
func TestPreviewRewritesPublishRootToPreviewRoot(t *testing.T) {
	f := newFixture(t)
	project := f.createProject(t, "构建产物")
	publishRoot := f.service.origin.SiteRoot(project.ID)
	f.pushDraft(t, project.ID, []Entry{
		f.textEntry(t, project.ID, "index.html", `<script src="`+publishRoot+`assets/index-abc.js"></script>`),
		f.textEntry(t, project.ID, "assets/index-abc.js", "console.log('built')"),
	})

	token, projectID, _ := f.previewURL(t, project.ID, "")
	entry := f.previewTarget(t, token, projectID, "")
	previewRoot := PreviewRoot(project.ID, token)
	if !strings.Contains(string(entry.Data), `src="`+previewRoot+`assets/index-abc.js"`) {
		t.Fatalf("发布根没有换成预览根: %s", entry.Data)
	}

	// 换出来的那段地址**真的能取到那一份文件**——否则这次改写只是换了个写法。
	script := f.previewTarget(t, token, projectID, "assets/index-abc.js")
	if string(script.Data) != "console.log('built')" {
		t.Fatalf("改写后的地址指不到草稿: %q", script.Data)
	}
}

// TestPreviewRendersDocsAtPreviewPaths 钉住 `docs` 形态：页面由服务端渲染，站点
// 文件按原路径给出，页间导航是**真实地址**（不再是一份拼起来的文档加文内锚点）。
func TestPreviewRendersDocsAtPreviewPaths(t *testing.T) {
	f := newFixture(t)
	project := f.createProjectForm(t, "文档站", SiteFormDocs)
	f.pushDraft(t, project.ID, []Entry{
		f.textEntry(t, project.ID, "index.md", "# 首页\n\n[下一步](guide/intro.md)\n"),
		f.textEntry(t, project.ID, "guide/intro.md", "# 入门\n"),
		f.textEntry(t, project.ID, "theme.css", "body{margin:0}"),
	})

	token, projectID, entryPath := f.previewURL(t, project.ID, "")
	if entryPath != "index.html" {
		t.Fatalf("docs 的入口应指向渲染出来的那一页，实际是 %q", entryPath)
	}
	entry := f.previewTarget(t, token, projectID, "")
	if entry.ContentType != "text/html; charset=utf-8" || !strings.Contains(string(entry.Data), "首页") {
		t.Fatalf("入口不是渲染出来的那一页: %q %s", entry.ContentType, entry.Data)
	}
	// 导航指向预览根下的产物路径：点得动，且落在草稿上。
	if !strings.Contains(string(entry.Data), PreviewRoot(project.ID, token)+"guide/intro.html") {
		t.Fatalf("导航没有指向预览根: %s", entry.Data)
	}
	page := f.previewTarget(t, token, projectID, "guide/intro.html")
	if !strings.Contains(string(page.Data), "入门") {
		t.Fatalf("按产物路径取不到那一页: %s", page.Data)
	}
	// 站点文件按原路径给出，类型由扩展名派生——文档预览里样式因此生效。
	style := f.previewTarget(t, token, projectID, "theme.css")
	if style.ContentType != "text/css; charset=utf-8" || string(style.Data) != "body{margin:0}" {
		t.Fatalf("站点文件没按路径取到: %q %q", style.ContentType, style.Data)
	}
}

// TestPreviewGrantsAreShortLivedAndProjectScoped 钉住凭证的两条边界：它只对**这一个
// 工程**成立，且过期即失效。
func TestPreviewGrantsAreShortLivedAndProjectScoped(t *testing.T) {
	f := newFixture(t)
	first := f.staticSite(t, map[string]string{"index.html": "第一个"})
	second := f.staticSite(t, map[string]string{"index.html": "第二个"})
	token, projectID, _ := f.previewURL(t, first.ID, "")

	// 拿第一个工程的凭证去取第二个工程：否定结论。
	if _, err := f.service.OpenPreview(context.Background(), token, second.ID, "index.html"); !errors.Is(err, ErrPreviewNotFound) {
		t.Fatalf("凭证不该对别的工程成立，实际是 %v", err)
	}
	// 伪造的凭证：同一个结论。
	if _, err := f.service.OpenPreview(context.Background(), token+"x", projectID, "index.html"); !errors.Is(err, ErrPreviewNotFound) {
		t.Fatalf("伪造的凭证应得到否定结论，实际是 %v", err)
	}
	// 过期：同一个结论——地址是短时的，这一点不能只在界面上说说。
	f.advance(PreviewGrantTTL)
	if _, err := f.service.OpenPreview(context.Background(), token, projectID, "index.html"); !errors.Is(err, ErrPreviewNotFound) {
		t.Fatalf("过期的凭证应得到否定结论，实际是 %v", err)
	}
}

// TestPreviewDraftWithoutEntryGivesNoAddress 钉住"还没内容"与"打不开的地址"是两件事。
func TestPreviewDraftWithoutEntryGivesNoAddress(t *testing.T) {
	f := newFixture(t)
	project := f.createProject(t, "新工程")
	url, err := f.service.PreviewDraft(context.Background(), testOwner, project.ID, "")
	if err != nil {
		t.Fatalf("没有草稿不是错误: %v", err)
	}
	if url != "" {
		t.Fatalf("还没有入口时不该给出一条地址，实际是 %q", url)
	}
}

// TestPreviewRequiresReadPermission 钉住"凭证由权限换出来"这条：别人的工程签不出凭证。
func TestPreviewRequiresReadPermission(t *testing.T) {
	f := newFixture(t)
	project := f.staticSite(t, map[string]string{"index.html": "内容"})
	if _, err := f.service.PreviewDraft(context.Background(), testOther, project.ID, ""); !errors.Is(err, ErrProjectNotFound) {
		t.Fatalf("非拥有者不该签出预览地址，实际是 %v", err)
	}
}
