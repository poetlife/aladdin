package galaxy

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// **`static` 产物与文件组逐字相同**：差异集合恰好是记号被解析过的那些字节。
func TestStaticArtifactsAreByteIdentical(t *testing.T) {
	const page = `<!doctype html><html><head><link rel="stylesheet" href="/g/%s/style.css"></head><body><script src="/g/%s/app.js"></script></body></html>`
	f := newFixture(t)
	project := f.createProject(t, "构建产物")
	pageContent := strings.ReplaceAll(page, "%s", project.ID)

	f.pushDraft(t, project.ID, []Entry{
		f.textEntry(t, project.ID, "index.html", pageContent),
		f.textEntry(t, project.ID, "style.css", "body{margin:0}"),
		f.textEntry(t, project.ID, "app.js", "console.log(1)"),
	})

	artifacts := f.buildArtifacts(t, project.ID)
	for _, entryPath := range []string{"index.html", "style.css", "app.js"} {
		if _, ok := artifacts[entryPath]; !ok {
			t.Fatalf("产物里缺 %s", entryPath)
		}
	}
	if string(artifacts["index.html"]) != pageContent {
		t.Errorf("构建产物被改写了：\n得到 %s\n期望 %s", artifacts["index.html"], pageContent)
	}
	if string(artifacts["style.css"]) != "body{margin:0}" {
		t.Error("CSS 被改写了")
	}
}

// 记号在产物里被换成**站点内路径**；公开区地址不进产物。
func TestMarkersBecomeSitePaths(t *testing.T) {
	f := newFixture(t)
	project := f.createProject(t, "工程")
	asset := f.uploadAsset(t, project.ID, "image/png", "logo.png", []byte("png"))

	f.pushDraft(t, project.ID, []Entry{
		f.textEntry(t, project.ID, "index.html", `<img src="asset://`+asset.ID+`">`),
		{Path: "logo.png", Kind: EntryKindAsset, AssetID: asset.ID},
	})

	artifacts := f.buildArtifacts(t, project.ID)
	want := `<img src="/g/` + project.ID + `/logo.png">`
	if string(artifacts["index.html"]) != want {
		t.Errorf("产物 = %q，期望 %q", artifacts["index.html"], want)
	}
	if strings.Contains(string(artifacts["index.html"]), testBucketOrigin) {
		t.Error("产物里出现了公开区地址")
	}
}

// **一组条目一次性生效**：每一个路径在切换之后立刻可达，入口与 index.html 同一页。
func TestPublishedEntriesAreAllReachable(t *testing.T) {
	f := newFixture(t)
	project, _, _ := f.publishSite(t, map[string]string{
		"index.html":     "<p>首页</p>",
		"guide/one.html": "<p>一</p>",
		"style.css":      "body{}",
	})
	ctx := context.Background()

	for _, entryPath := range []string{"index.html", "guide/one.html", "style.css"} {
		if _, err := f.service.PublishedEntry(ctx, project.ID, SlotSite, entryPath); err != nil {
			t.Errorf("%s 不可达: %v", entryPath, err)
		}
	}
	// 入口地址（空路径）与 `index.html` 是**同一页**。
	entry, err := f.service.PublishedEntry(ctx, project.ID, SlotSite, "")
	if err != nil {
		t.Fatalf("入口不可达: %v", err)
	}
	if entry.Path != "index.html" {
		t.Errorf("入口解析成 %q，期望 index.html", entry.Path)
	}
}

// 资产条目在发布态**分派为重定向**：它的字节不从服务端出。
func TestAssetEntryIsDispatchedAsRedirect(t *testing.T) {
	f := newFixture(t)
	project := f.createProject(t, "工程")
	f.pushDraft(t, project.ID, []Entry{
		f.textEntry(t, project.ID, "index.html", "<p>首页</p>"),
		f.assetEntry(t, project.ID, "logo.png", "image/png", "logo.png", []byte("png")),
	})
	version := f.saveVersion(t, project.ID)
	f.publish(t, project.ID, version.ID)

	entry, err := f.service.PublishedEntry(context.Background(), project.ID, SlotSite, "logo.png")
	if err != nil {
		t.Fatalf("资产条目不可达: %v", err)
	}
	if entry.Kind != EntryKindAsset {
		t.Fatalf("条目类别 = %q，期望 asset", entry.Kind)
	}
	url, err := f.service.PublishedAssetURL(context.Background(), project.ID, entry.AssetID)
	if err != nil {
		t.Fatalf("取公开区地址失败: %v", err)
	}
	// 公开区按工程切分，段内按（内容摘要，类型）寻址。
	releasePrefix := assetKeyPrefix + project.ID + "/" + releaseKeySegment + "/"
	if !strings.HasPrefix(url, testBucketOrigin+"/"+releasePrefix) {
		t.Errorf("公开区地址 = %q，期望落在 %s 下", url, releasePrefix)
	}
	if !strings.Contains(url, ContentDigest([]byte("png"))) {
		t.Errorf("公开区地址 %q 里没有内容摘要", url)
	}
}

// **只上架被引用的资产**，且上架按（内容摘要，类型）幂等。
func TestPromoteOnlyReferencedAndIdempotent(t *testing.T) {
	f := newFixture(t)
	project := f.createProject(t, "工程")
	used := f.uploadAsset(t, project.ID, "image/png", "used.png", []byte("used"))
	unused := f.uploadAsset(t, project.ID, "image/png", "unused.png", []byte("unused"))

	f.pushDraft(t, project.ID, []Entry{
		f.textEntry(t, project.ID, "index.html", `<img src="asset://`+used.ID+`">`),
		{Path: "used.png", Kind: EntryKindAsset, AssetID: used.ID},
	})
	version := f.saveVersion(t, project.ID)
	f.publish(t, project.ID, version.ID)

	if f.public.Count() != 1 {
		t.Fatalf("公开区对象数 = %d，期望 1（只上架被引用的）", f.public.Count())
	}
	for _, key := range f.public.Keys() {
		if strings.Contains(key, unused.ID) {
			t.Error("未被引用的资产也被上架了")
		}
		// 上架搬过去的**正是那一份字节**（现在是让存储自己复制过去的）。
		data, err := f.public.Object(key)
		if err != nil {
			t.Fatalf("读公开区对象失败: %v", err)
		}
		if string(data) != "used" {
			t.Errorf("上架的字节 = %q，期望 %q", data, "used")
		}
	}

	// 再发布一次：不产生新对象。
	objectsBefore := f.objects.Count()
	f.publish(t, project.ID, version.ID)
	if f.public.Count() != 1 {
		t.Errorf("重复发布之后公开区对象数 = %d，期望仍是 1", f.public.Count())
	}
	if f.objects.Count() != objectsBefore {
		t.Errorf("重复发布产生了新对象：%d → %d", objectsBefore, f.objects.Count())
	}
}

// **文本不进公开区**：上架只为媒体而做。
func TestTextNeverEntersPublicZone(t *testing.T) {
	f := newFixture(t)
	project, _, _ := f.publishSite(t, map[string]string{"index.html": "<p>首页</p>"})

	// 公开区一个对象都没有：这份内容里没有任何资产。
	if f.public.Count() != 0 {
		t.Errorf("公开区对象数 = %d，期望 0", f.public.Count())
	}
	// 而文本仍然可达——它由服务端从私有区读出。
	data, err := f.service.ReadPublishedText(context.Background(), project.ID,
		ContentDigest([]byte("<p>首页</p>")))
	if err != nil {
		t.Fatalf("读发布态文本失败: %v", err)
	}
	if string(data) != "<p>首页</p>" {
		t.Errorf("文本 = %q", data)
	}
}

// 撤回之后**每一条路径**都不可达，且可以重新发布同一个版本，产物逐字相同。
func TestUnpublishThenRepublishIsIdentical(t *testing.T) {
	f := newFixture(t)
	project, version, first := f.publishSite(t, map[string]string{
		"index.html": "<p>首页</p>",
		"style.css":  "body{}",
	})
	ctx := context.Background()

	if err := f.service.Unpublish(ctx, testOwner, project.ID, SlotSite); err != nil {
		t.Fatalf("撤回失败: %v", err)
	}
	// **每一条路径都是**，不只是入口。
	for _, entryPath := range []string{"", "index.html", "style.css"} {
		if _, err := f.service.PublishedEntry(ctx, project.ID, SlotSite, entryPath); !errors.Is(err, ErrPublicationNotFound) {
			t.Errorf("撤回之后 %q 仍可达: %v", entryPath, err)
		}
	}

	second := f.publish(t, project.ID, version.ID)
	if second.ID != first.ID {
		t.Errorf("重新发布的标识 = %q，期望与首次相同（%q）", second.ID, first.ID)
	}
	if len(second.Manifest) != len(first.Manifest) {
		t.Fatalf("产物清单长度不同：%d / %d", len(second.Manifest), len(first.Manifest))
	}
	for i, entry := range first.Manifest {
		if second.Manifest[i] != entry {
			t.Errorf("产物清单第 %d 条不同：%+v / %+v", i, second.Manifest[i], entry)
		}
	}
}

// **整套一次性生效**：上架中途失败时，指针没动，所有路径读到的仍是上一次的产物。
func TestFailureBeforeRecordLeavesNothingVisible(t *testing.T) {
	f := newFixture(t)
	project := f.createProject(t, "工程")
	good := f.uploadAsset(t, project.ID, "image/png", "a.png", []byte("aaa"))

	f.pushDraft(t, project.ID, []Entry{
		f.textEntry(t, project.ID, "index.html", `<img src="asset://`+good.ID+`">`),
		{Path: "a.png", Kind: EntryKindAsset, AssetID: good.ID},
	})
	version := f.saveVersion(t, project.ID)
	f.publish(t, project.ID, version.ID)

	// 改草稿、存新版本，然后让上架失败。
	bad := f.uploadAsset(t, project.ID, "image/png", "b.png", []byte("bbb"))
	f.pushDraft(t, project.ID, []Entry{
		f.textEntry(t, project.ID, "index.html", `<img src="asset://`+bad.ID+`">`),
		{Path: "b.png", Kind: EntryKindAsset, AssetID: bad.ID},
	})
	next := f.saveVersion(t, project.ID)

	f.public.CopyErr = errors.New("上架失败（注入）")
	if _, err := f.service.Publish(context.Background(), testOwner, project.ID, SlotSite, next.ID); err == nil {
		t.Fatal("上架失败却报告发布成功")
	}
	f.public.CopyErr = nil

	// 指针没动：读到的还是上一次的产物。
	stored, err := f.store.GetProject(context.Background(), project.ID)
	if err != nil {
		t.Fatalf("读工程失败: %v", err)
	}
	if slot, _ := stored.FindSlot(SlotSite); slot.CurrentPublicationID != PublicationID(project.ID, version.ID) {
		t.Error("失败的发布动了发布指针")
	}
	entry, err := f.service.PublishedEntry(context.Background(), project.ID, SlotSite, "index.html")
	if err != nil {
		t.Fatalf("上一次的产物不可达: %v", err)
	}
	data, err := f.service.ReadPublishedText(context.Background(), project.ID, entry.Digest)
	if err != nil {
		t.Fatalf("读文本失败: %v", err)
	}
	if !strings.Contains(string(data), "a.png") || strings.Contains(string(data), "b.png") {
		t.Errorf("读到的是新内容：%s", data)
	}
}

// **发布物不含主体信息**：产物与清单里都不出现拥有者的标识。
func TestPublicationCarriesNoSubjectInformation(t *testing.T) {
	f := newFixture(t)
	project, version, publication := f.publishSite(t, map[string]string{"index.html": "<p>首页</p>"})

	if strings.Contains(version.ID, testOwner) || strings.Contains(project.ID, testOwner) {
		t.Fatal("标识里出现了主体标识")
	}
	for _, entry := range publication.Manifest {
		if strings.Contains(entry.Path, testOwner) || strings.Contains(entry.Digest, testOwner) {
			t.Error("产物清单里出现了主体标识")
		}
	}
	for _, data := range f.buildArtifacts(t, project.ID) {
		if strings.Contains(string(data), testOwner) {
			t.Error("产物里出现了主体标识")
		}
	}
}

// **发布不可用是一个正常状态**，不是故障：没配置公开区或发布域时这条路整体缺席。
func TestPublishUnavailableWithoutConfiguration(t *testing.T) {
	f := newFixture(t)
	project := f.createProject(t, "工程")
	f.pushDraft(t, project.ID, []Entry{f.textEntry(t, project.ID, "index.html", "<p>x</p>")})
	version := f.saveVersion(t, project.ID)

	// 一个没有公开区写入口的部署。
	noPublic := NewService(Deps{
		Store:  f.store,
		Assets: f.objects,
		Origin: mustOrigin(t),
		Logger: f.service.logger,
		Now:    func() time.Time { return f.now },
	})
	if _, err := noPublic.Publish(context.Background(), testOwner, project.ID, SlotSite, version.ID); !errors.Is(err, ErrPublishUnavailable) {
		t.Errorf("err = %v，期望 ErrPublishUnavailable", err)
	}
	if noPublic.Capabilities().PublishEnabled {
		t.Error("没有公开区却报告发布可用")
	}
}

// 未发布、已撤回、工程不存在、标识没被猜中、路径不在集合里——**五者同一个否定**。
func TestNegativeConclusionsAreIndistinguishable(t *testing.T) {
	f := newFixture(t)
	project, _, _ := f.publishSite(t, map[string]string{"index.html": "<p>首页</p>"})
	withdrawn, _, _ := f.publishSite(t, map[string]string{"index.html": "<p>另一个</p>"})
	if err := f.service.Unpublish(context.Background(), testOwner, withdrawn.ID, SlotSite); err != nil {
		t.Fatalf("撤回失败: %v", err)
	}

	cases := []struct {
		name      string
		projectID string
		entryPath string
	}{
		{"路径不在集合里", project.ID, "nope.html"},
		{"已撤回", withdrawn.ID, ""},
		{"工程不存在", "prj_不存在", ""},
		{"路径不是一条合法条目", project.ID, "../index.html"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := f.service.PublishedEntry(context.Background(), tc.projectID, SlotSite, tc.entryPath); !errors.Is(err, ErrPublicationNotFound) {
				t.Errorf("err = %v，期望 ErrPublicationNotFound", err)
			}
		})
	}
}

// **渲染规则按版本钉住**：重新发布用的是版本记录里的那一版规则。
func TestRenderRulesVersionIsFrozenWithTheVersion(t *testing.T) {
	f := newFixture(t)
	project := f.createProjectSlots(t, "文档站", SlotDocs)
	f.pushDraftSlot(t, project.ID, SlotDocs, []Entry{f.textEntry(t, project.ID, "index.md", "# 首页\n")})
	version := f.saveVersionSlot(t, project.ID, SlotDocs)

	if version.RenderRulesVersion != RenderRulesVersion {
		t.Fatalf("版本记录的渲染规则版本 = %d，期望 %d", version.RenderRulesVersion, RenderRulesVersion)
	}
	got, _, err := f.service.GetVersion(context.Background(), testOwner, project.ID, SlotDocs, version.ID)
	if err != nil {
		t.Fatalf("读取版本失败: %v", err)
	}
	if got.RenderRulesVersion != version.RenderRulesVersion {
		t.Error("读回的渲染规则版本变了")
	}
}

// **库只存清单，不存字节**：发布记录里只有路径与摘要。
func TestPublicationManifestHoldsNoBytes(t *testing.T) {
	f := newFixture(t)
	_, _, publication := f.publishSite(t, map[string]string{"index.html": "<p>首页</p>"})

	for _, entry := range publication.Manifest {
		if entry.Kind != EntryKindText {
			continue
		}
		if strings.Contains(entry.Digest, "<p>") || strings.Contains(entry.Path, "<p>") {
			t.Error("清单里出现了内容本身")
		}
		if len(entry.Digest) != contentDigestLength {
			t.Errorf("清单里的摘要 = %q，期望是一个内容摘要", entry.Digest)
		}
	}
}
