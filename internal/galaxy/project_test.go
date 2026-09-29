package galaxy

import (
	"context"
	"encoding/base64"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/poetlife/aladdin/internal/objectstore"
)

// 工程标识由服务端分配，**不可猜**：它是发布地址的一部分，而发布态是公开匿名的。
//
// "不可猜"在实现上的判据是三条：形状固定、随机部分来自密码学随机源、且**不含
// 任何可控成分**（名称、时间、序号都不参与）。
func TestProjectIDIsUnguessable(t *testing.T) {
	f := newFixture(t)
	first := f.createProject(t, "第一个")
	second := f.createProject(t, "第一个")

	if !strings.HasPrefix(first.ID, "prj_") {
		t.Errorf("标识 = %q，期望带 prj_ 前缀", first.ID)
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(first.ID, "prj_"))
	if err != nil {
		t.Fatalf("标识的随机部分不是 base64url: %v", err)
	}
	if len(raw) != idEntropyBytes {
		t.Errorf("随机部分 %d 字节，期望 %d（128 位）", len(raw), idEntropyBytes)
	}
	if first.ID == second.ID {
		t.Error("两次分配拿到了同一个标识")
	}
	if strings.Contains(first.ID, "第一个") {
		t.Error("标识里含名称——那就是一个可控成分")
	}
}

// 名称**不是查找键**：两个工程可以同名，各自独立。
func TestProjectNameIsNotAKey(t *testing.T) {
	f := newFixture(t)
	first := f.createProject(t, "同名")
	second := f.createProject(t, "同名")

	if first.ID == second.ID {
		t.Fatal("两个工程拿到了同一个标识")
	}
	// 都能按各自的标识读回，且读回的正是自己那一个。
	for _, want := range []Project{first, second} {
		got, err := f.service.GetProject(context.Background(), testOwner, want.ID)
		if err != nil {
			t.Fatalf("读取工程失败: %v", err)
		}
		if got.ID != want.ID || got.Name != "同名" {
			t.Errorf("读回 = %+v，期望 %+v", got, want)
		}
	}
	// 列表里两条都在：同名的工程不会互相覆盖。
	projects, err := f.service.ListProjects(context.Background(), testOwner)
	if err != nil {
		t.Fatalf("列出工程失败: %v", err)
	}
	if len(projects) != 2 {
		t.Errorf("工程数 = %d，期望 2", len(projects))
	}
}

// 工程元数据有长度上限，且按**字符数**而不是字节数计。
func TestProjectMetaLimits(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	if _, err := f.service.CreateProject(ctx, testOwner, strings.Repeat("名", ProjectNameMaxRunes+1), "", SiteFormStatic); !errors.Is(err, ErrProjectNameTooLong) {
		t.Errorf("超长名称 err = %v，期望 ErrProjectNameTooLong", err)
	}
	if _, err := f.service.CreateProject(ctx, testOwner, "名", strings.Repeat("简", ProjectDescriptionMaxRunes+1), SiteFormStatic); !errors.Is(err, ErrProjectDescriptionTooLong) {
		t.Errorf("超长简介 err = %v，期望 ErrProjectDescriptionTooLong", err)
	}
	// 中文按字数算：ProjectNameMaxRunes 个汉字必须能通过。
	if _, err := f.service.CreateProject(ctx, testOwner, strings.Repeat("名", ProjectNameMaxRunes), "", SiteFormStatic); err != nil {
		t.Errorf("恰好到上限的中文名称被拒: %v", err)
	}
}

// **形态创建时定下，此后不可改**：缺形态（UNSPECIFIED 落到零值）也拒绝。
//
// 允许改形态等于让历史版本的产物无法复现——同一个 `.md` 在两种形态下的产物
// 完全不同。
func TestSiteFormIsFixedAtCreation(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	if _, err := f.service.CreateProject(ctx, testOwner, "没形态", "", ""); !errors.Is(err, ErrSiteFormInvalid) {
		t.Errorf("缺形态 err = %v，期望 ErrSiteFormInvalid", err)
	}
	project := f.createProjectForm(t, "文档站", SiteFormDocs)
	if project.Form != SiteFormDocs {
		t.Errorf("形态 = %q，期望 docs", project.Form)
	}
	// 改元数据**不动形态**。
	updated, err := f.service.UpdateProject(ctx, testOwner, project.ID, "改名", "新简介")
	if err != nil {
		t.Fatalf("改元数据失败: %v", err)
	}
	if updated.Form != SiteFormDocs {
		t.Errorf("改元数据之后形态 = %q，期望不变", updated.Form)
	}
}

// 草稿行是**惰性创建**的：新建的工程没有草稿，而"没有"与"空"在编辑上等价。
func TestDraftIsLazilyCreated(t *testing.T) {
	f := newFixture(t)
	project := f.createProject(t, "新工程")
	ctx := context.Background()

	draft, entries, err := f.service.GetDraft(ctx, testOwner, project.ID)
	if err != nil {
		t.Fatalf("读取空草稿失败: %v", err)
	}
	if len(draft.Manifest) != 0 || len(entries) != 0 || !draft.UpdatedAt.IsZero() {
		t.Errorf("草稿 = %+v，期望是一份空清单且没有更新时间", draft)
	}
	// 推送之后再读回。
	f.pushDraft(t, project.ID, []Entry{f.textEntry(t, project.ID, "index.html", "<p>你好</p>")})
	draft, entries, err = f.service.GetDraft(ctx, testOwner, project.ID)
	if err != nil {
		t.Fatalf("读取草稿失败: %v", err)
	}
	if len(draft.Manifest) != 1 || draft.UpdatedAt.IsZero() {
		t.Errorf("草稿 = %+v，期望清单与更新时间都在", draft)
	}
	// 每一条都带一条短时读取地址（编辑器据此直连取字节）。
	if len(entries) != 1 || entries[0].URL == "" {
		t.Errorf("条目视图 = %+v，期望带一条读取地址", entries)
	}
}

// **push 表达整组的期望状态**：目录里没有的路径就是删掉。
func TestPushDraftReplacesWholeSet(t *testing.T) {
	f := newFixture(t)
	project := f.createProject(t, "工程")

	f.pushDraft(t, project.ID, []Entry{
		f.textEntry(t, project.ID, "index.html", "<p>一</p>"),
		f.textEntry(t, project.ID, "style.css", "body{}"),
	})
	f.pushDraft(t, project.ID, []Entry{
		f.textEntry(t, project.ID, "index.html", "<p>一</p>"),
	})

	draft, _, err := f.service.GetDraft(context.Background(), testOwner, project.ID)
	if err != nil {
		t.Fatalf("读取草稿失败: %v", err)
	}
	if len(draft.Manifest) != 1 || draft.Manifest[0].Path != "index.html" {
		t.Errorf("清单 = %+v，期望只剩 index.html", draft.Manifest)
	}
}

// 推送一份**没有入口文件**的清单被拒：缺入口是写入期的拒绝，不是等到发布才发现。
func TestPushDraftRequiresEntryFile(t *testing.T) {
	f := newFixture(t)
	project := f.createProject(t, "工程")
	ctx := context.Background()

	_, err := f.service.PushDraft(ctx, testOwner, project.ID, []Entry{
		f.textEntry(t, project.ID, "about.html", "<p>关于</p>"),
	})
	if !errors.Is(err, ErrEntrySetInvalid) {
		t.Fatalf("err = %v，期望 ErrEntrySetInvalid", err)
	}
	if !strings.Contains(err.Error(), "index.html") {
		t.Errorf("错误信息 %q 没有指出缺的是哪一份入口", err)
	}
}

// **版本不可变**：保存之后改草稿、改元数据，读回的清单逐字不变。
func TestVersionIsImmutable(t *testing.T) {
	f := newFixture(t)
	project := f.createProject(t, "工程")
	f.pushDraft(t, project.ID, []Entry{f.textEntry(t, project.ID, "index.html", "<p>第一版</p>")})
	version := f.saveVersion(t, project.ID)

	// 改草稿、改名称、改简介。
	f.advance(1)
	f.pushDraft(t, project.ID, []Entry{f.textEntry(t, project.ID, "index.html", "<p>第二版</p>")})
	if _, err := f.service.UpdateProject(context.Background(), testOwner, project.ID, "改名了", "新简介"); err != nil {
		t.Fatalf("改元数据失败: %v", err)
	}

	got, _, err := f.service.GetVersion(context.Background(), testOwner, project.ID, version.ID)
	if err != nil {
		t.Fatalf("读取版本失败: %v", err)
	}
	if !reflect.DeepEqual(got.Manifest, version.Manifest) {
		t.Errorf("版本清单 = %+v，期望逐字不变（%+v）", got.Manifest, version.Manifest)
	}
	if got.Seq != version.Seq || !got.SavedAt.Equal(version.SavedAt) {
		t.Errorf("版本 = %+v，序号与保存时间也不该变", got)
	}
}

// 版本**不因别处变化而变**：删掉别的版本之后，剩下的版本读回内容不变。
func TestVersionUnaffectedByOtherDeletions(t *testing.T) {
	f := newFixture(t)
	project := f.createProject(t, "工程")
	ctx := context.Background()

	f.pushDraft(t, project.ID, []Entry{f.textEntry(t, project.ID, "index.html", "<p>一</p>")})
	first := f.saveVersion(t, project.ID)
	f.pushDraft(t, project.ID, []Entry{f.textEntry(t, project.ID, "index.html", "<p>二</p>")})
	second := f.saveVersion(t, project.ID)
	f.pushDraft(t, project.ID, []Entry{f.textEntry(t, project.ID, "index.html", "<p>三</p>")})
	third := f.saveVersion(t, project.ID)

	if err := f.service.DeleteVersion(ctx, testOwner, project.ID, second.ID); err != nil {
		t.Fatalf("删除中间版本失败: %v", err)
	}
	for _, want := range []Version{first, third} {
		got, _, err := f.service.GetVersion(ctx, testOwner, project.ID, want.ID)
		if err != nil {
			t.Fatalf("读取版本 %s 失败: %v", want.ID, err)
		}
		if !reflect.DeepEqual(got.Manifest, want.Manifest) {
			t.Errorf("版本 %s 清单 = %+v，期望 %+v", want.ID, got.Manifest, want.Manifest)
		}
		if got.Seq != want.Seq {
			t.Errorf("版本 %s 的序号 = %d，期望保持 %d（序号不因删除重排）", want.ID, got.Seq, want.Seq)
		}
	}
}

// **版本不可覆盖**：连续保存两次相同清单产生两个版本。
func TestSavingTwiceCreatesTwoVersions(t *testing.T) {
	f := newFixture(t)
	project := f.createProject(t, "工程")

	f.pushDraft(t, project.ID, []Entry{f.textEntry(t, project.ID, "index.html", "<p>一样的</p>")})
	first := f.saveVersion(t, project.ID)
	f.advance(1)
	second := f.saveVersion(t, project.ID)

	if first.ID == second.ID {
		t.Fatal("两次保存合并成了一个版本")
	}
	if second.Seq != first.Seq+1 {
		t.Errorf("第二个版本的序号 = %d，期望 %d", second.Seq, first.Seq+1)
	}
	// 两份清单一样，但它们是两条记录——把"我明明保存了两次"合成一次是需要解释的。
	versions, err := f.service.ListVersions(context.Background(), testOwner, project.ID)
	if err != nil {
		t.Fatalf("列出版本失败: %v", err)
	}
	if len(versions) != 2 {
		t.Errorf("版本数 = %d，期望 2", len(versions))
	}
	if versions[0].RenderRulesVersion != RenderRulesVersion {
		t.Errorf("渲染规则版本 = %d，期望保存那一刻的 %d",
			versions[0].RenderRulesVersion, RenderRulesVersion)
	}
}

// 序号**不是标识**：删中间一个版本之后，其余版本仍可按各自的标识读回。
func TestSequenceIsNotAnIdentifier(t *testing.T) {
	f := newFixture(t)
	project := f.createProject(t, "工程")
	ctx := context.Background()

	f.pushDraft(t, project.ID, []Entry{f.textEntry(t, project.ID, "index.html", "<p>一</p>")})
	first := f.saveVersion(t, project.ID)
	f.pushDraft(t, project.ID, []Entry{f.textEntry(t, project.ID, "index.html", "<p>二</p>")})
	second := f.saveVersion(t, project.ID)

	if err := f.service.DeleteVersion(ctx, testOwner, project.ID, first.ID); err != nil {
		t.Fatalf("删除失败: %v", err)
	}
	// 序号 1 空了，但序号 2 的那个版本仍然按**它自己的标识**读得到。
	got, _, err := f.service.GetVersion(ctx, testOwner, project.ID, second.ID)
	if err != nil {
		t.Fatalf("读取版本失败: %v", err)
	}
	if got.Seq != 2 {
		t.Errorf("序号 = %d，期望保持 2（空洞是允许的）", got.Seq)
	}
	if _, _, err := f.service.GetVersion(ctx, testOwner, project.ID, "ver_不存在"); !errors.Is(err, ErrVersionNotFound) {
		t.Errorf("err = %v，期望 ErrVersionNotFound", err)
	}
}

// **草稿不可发布**：没有版本时发布被拒，而草稿清单不会被读走。
func TestDraftCannotBePublished(t *testing.T) {
	f := newFixture(t)
	project := f.createProject(t, "工程")
	f.pushDraft(t, project.ID, []Entry{f.textEntry(t, project.ID, "index.html", "<p>只有草稿</p>")})

	if _, err := f.service.Publish(context.Background(), testOwner, project.ID, "ver_不存在"); !errors.Is(err, ErrVersionNotFound) {
		t.Errorf("err = %v，期望 ErrVersionNotFound", err)
	}
	// 发布没有被记录，指针也没有动。
	stored, err := f.store.GetProject(context.Background(), project.ID)
	if err != nil {
		t.Fatalf("读取工程失败: %v", err)
	}
	if stored.CurrentPublicationID != "" {
		t.Error("被拒的发布动了发布指针")
	}
}

// **版本引用了哪些资产由清单决定**，不解析任何文本。
func TestReferencesComeFromManifest(t *testing.T) {
	f := newFixture(t)
	project := f.createProject(t, "工程")

	first := f.uploadAsset(t, project.ID, "image/png", "a.png", []byte("aaa"))
	second := f.uploadAsset(t, project.ID, "image/png", "b.png", []byte("bbb"))

	f.pushDraft(t, project.ID, []Entry{
		f.textEntry(t, project.ID, "index.html", `<img src="asset://`+first.ID+`">`),
		{Path: "a.png", Kind: EntryKindAsset, AssetID: first.ID},
	})
	version := f.saveVersion(t, project.ID)

	if got := version.Manifest.AssetIDs(); len(got) != 1 || got[0] != first.ID {
		t.Errorf("版本引用 = %v，期望 [%s]", got, first.ID)
	}

	// 之后改草稿不影响这个版本：它自己的清单是它自己的结论。
	f.pushDraft(t, project.ID, []Entry{
		f.textEntry(t, project.ID, "index.html", `<img src="asset://`+second.ID+`">`),
		{Path: "b.png", Kind: EntryKindAsset, AssetID: second.ID},
	})
	reloaded, _, err := f.service.GetVersion(context.Background(), testOwner, project.ID, version.ID)
	if err != nil {
		t.Fatalf("读取版本失败: %v", err)
	}
	if got := reloaded.Manifest.AssetIDs(); len(got) != 1 || got[0] != first.ID {
		t.Errorf("改草稿之后版本引用 = %v，期望不变（[%s]）", got, first.ID)
	}
}

// **归属不可绕过**：另一个主体读、改、发布都拿不到别人的工程。
//
// 拒绝的类别与"工程不存在"完全一致——区分它们等于提供一个工程枚举接口。
func TestOwnershipCannotBeBypassed(t *testing.T) {
	f := newFixture(t)
	project := f.createProject(t, "别人的工程")
	f.pushDraft(t, project.ID, []Entry{f.textEntry(t, project.ID, "index.html", "<p>x</p>")})
	version := f.saveVersion(t, project.ID)
	asset := f.uploadAsset(t, project.ID, "image/png", "a.png", []byte("aaa"))
	ctx := context.Background()

	cases := []struct {
		name string
		call func() error
	}{
		{"读工程", func() error {
			_, err := f.service.GetProject(ctx, testOther, project.ID)
			return err
		}},
		{"列工程里的版本", func() error {
			_, err := f.service.ListVersions(ctx, testOther, project.ID)
			return err
		}},
		{"读草稿", func() error {
			_, _, err := f.service.GetDraft(ctx, testOther, project.ID)
			return err
		}},
		{"推送草稿", func() error {
			_, err := f.service.PushDraft(ctx, testOther, project.ID, nil)
			return err
		}},
		{"读版本", func() error {
			_, _, err := f.service.GetVersion(ctx, testOther, project.ID, version.ID)
			return err
		}},
		{"改元数据", func() error {
			_, err := f.service.UpdateProject(ctx, testOther, project.ID, "改", "")
			return err
		}},
		{"删版本", func() error {
			return f.service.DeleteVersion(ctx, testOther, project.ID, version.ID)
		}},
		{"列资产", func() error {
			_, _, err := f.service.ListAssets(ctx, testOther, project.ID, nil)
			return err
		}},
		{"取资产地址", func() error {
			_, err := f.service.AssetURL(ctx, testOther, project.ID, asset.ID)
			return err
		}},
		{"删资产", func() error {
			return f.service.DeleteAsset(ctx, testOther, project.ID, asset.ID)
		}},
		{"校验草稿", func() error {
			_, err := f.service.ValidateDraft(ctx, testOther, project.ID)
			return err
		}},
		{"预览草稿", func() error {
			_, err := f.service.PreviewDraft(ctx, testOther, project.ID, "")
			return err
		}},
		{"上传内容对象", func() error {
			_, _, err := f.service.BeginContentUpload(ctx, testOther, project.ID, ContentDigest([]byte("x")), 1)
			return err
		}},
		{"发布", func() error {
			_, err := f.service.Publish(ctx, testOther, project.ID, version.ID)
			return err
		}},
		{"撤回", func() error {
			return f.service.Unpublish(ctx, testOther, project.ID)
		}},
		{"删工程", func() error {
			return f.service.DeleteProject(ctx, testOther, project.ID)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.call(); !errors.Is(err, ErrProjectNotFound) {
				t.Errorf("err = %v，期望 ErrProjectNotFound", err)
			}
		})
	}

	// 另一个主体**没有**工程，因此列表是空的而不是被拒：那是两件事。
	projects, err := f.service.ListProjects(ctx, testOther)
	if err != nil {
		t.Fatalf("列出别人的工程失败: %v", err)
	}
	if len(projects) != 0 {
		t.Errorf("工程数 = %d，期望空集", len(projects))
	}
}

// "不是你的"与"不存在"返回**同一个结论**：否则它就是一个可用的枚举接口。
func TestForeignAndMissingProjectsAreIndistinguishable(t *testing.T) {
	f := newFixture(t)
	project := f.createProject(t, "存在但不是你的")
	ctx := context.Background()

	foreign, errForeign := f.service.GetProject(ctx, testOther, project.ID)
	missing, errMissing := f.service.GetProject(ctx, testOwner, "prj_不存在")

	if !errors.Is(errForeign, ErrProjectNotFound) || !errors.Is(errMissing, ErrProjectNotFound) {
		t.Fatalf("两种拒绝的类别不同：%v / %v", errForeign, errMissing)
	}
	if errForeign.Error() != errMissing.Error() {
		t.Errorf("错误信息不同：%q / %q", errForeign, errMissing)
	}
	if foreign != missing {
		t.Error("两种情形返回的工程不同")
	}
}

// 删除工程**连同版本、资产与发布记录**：发布地址因此立刻不可达。
func TestDeleteProjectRemovesEverything(t *testing.T) {
	f := newFixture(t)
	project := f.createProject(t, "待删")
	asset := f.uploadAsset(t, project.ID, "image/png", "a.png", []byte("aaa"))
	f.pushDraft(t, project.ID, []Entry{
		f.textEntry(t, project.ID, "index.html", `<img src="asset://`+asset.ID+`">`),
		{Path: "a.png", Kind: EntryKindAsset, AssetID: asset.ID},
	})
	version := f.saveVersion(t, project.ID)
	f.publish(t, project.ID, version.ID)
	ctx := context.Background()

	// 删除之前：私有区有对象、公开区有副本、地址可达。
	if _, _, err := f.service.PublishedEntry(ctx, project.ID, ""); err != nil {
		t.Fatalf("删除之前应当可达: %v", err)
	}
	promotedBefore := f.public.Count()

	if err := f.service.DeleteProject(ctx, testOwner, project.ID); err != nil {
		t.Fatalf("删除工程失败: %v", err)
	}

	// 工程、版本、资产、发布记录都没了。
	if _, err := f.store.GetProject(ctx, project.ID); !errors.Is(err, ErrProjectNotFound) {
		t.Errorf("工程仍在: %v", err)
	}
	if versions, err := f.store.ListVersions(ctx, project.ID); err != nil || len(versions) != 0 {
		t.Errorf("版本仍在: %v / %d 条", err, len(versions))
	}
	if assets, err := f.store.ListAssets(ctx, project.ID, nil); err != nil || len(assets) != 0 {
		t.Errorf("资产仍在: %v / %d 条", err, len(assets))
	}
	// 私有区的资产对象被删掉（否则成为无从被引用的孤儿）。
	if _, err := f.objects.Head(ctx, AssetObjectKey(project.ID, asset.MediaKind, asset.ID)); !errors.Is(err, objectstore.ErrObjectNotFound) {
		t.Errorf("私有区对象仍在: %v", err)
	}
	// **公开区的副本不回收**：它是一份独立对象，召回它需要一次对账。
	if f.public.Count() != promotedBefore {
		t.Error("公开区的副本被删掉了——那需要一次对账，不属于删除语义")
	}
	// 发布地址变成"不存在"。
	if _, _, err := f.service.PublishedEntry(ctx, project.ID, ""); !errors.Is(err, ErrPublicationNotFound) {
		t.Errorf("删除之后仍能取到产物: %v", err)
	}
}
