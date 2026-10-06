package skill

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestImportCreatesSkillAndStoresBytes(t *testing.T) {
	f := newFixture(t)
	item := f.importOne(t)

	if !strings.HasPrefix(item.ID, "skl_") {
		t.Errorf("技能标识 = %q，期望 skl_ 前缀", item.ID)
	}
	if item.Source.URL() != "https://github.com/"+testOwner+"/"+testRepo {
		t.Errorf("来源 = %q", item.Source.URL())
	}
	if item.Source.Commit != testSHA {
		t.Errorf("记录的提交 = %q", item.Source.Commit)
	}
	if got := item.Tags; len(got) != 2 || got[0] != "出图" || got[1] != "排版" {
		t.Errorf("标签 = %v，期望归一化并升序", got)
	}
	if item.Current.Name != "mono-color" {
		t.Errorf("版本的 name = %q", item.Current.Name)
	}
	// 字节按内容摘要进了对象存储，且**键上不带技能标识**。
	for _, file := range item.Current.Files {
		if _, err := f.objects.Read(context.Background(), ContentObjectKey(file.Digest)); err != nil {
			t.Errorf("文件 %q 的字节没有写进对象存储: %v", file.Path, err)
		}
	}
}

// 上游的二进制附件**跳过并计数**，而不是让整份包作废：真实仓库常把示例图与正文
// 放在一起，整份拒收会让合格的内容也进不来；而"平台里的包比上游少几个文件"必须
// 是一件看得见的事。
func TestImportSkipsBinariesAndCountsThem(t *testing.T) {
	f := newFixture(t)
	f.remote.setTree(testSHA, map[string]string{
		ManifestPath:     manifest("mono-color", "单色印刷风出图。"),
		"palette.md":     "# 色板\n",
		"examples/a.png": string([]byte{0x89, 'P', 'N', 'G', 0x00, 0x0d}),
		"examples/b.png": string([]byte{0x89, 'P', 'N', 'G', 0x00, 0x0d}),
	})
	item, err := f.service.Import(context.Background(), ImportParams{
		RepositoryURL: "https://github.com/" + testOwner + "/" + testRepo,
	})
	if err != nil {
		t.Fatalf("带示例图的包被整体拒了: %v", err)
	}
	if len(item.Current.Files) != 2 {
		t.Errorf("留下的文本 = %d 条，期望 2 条", len(item.Current.Files))
	}
	if item.Current.SkippedFiles != 2 {
		t.Errorf("跳过的条数 = %d，期望 2", item.Current.SkippedFiles)
	}
}

// 校验失败**不留任何痕迹**：库里没有新行，桶上也没有为它写的新对象。
func TestImportLeavesNoTraceOnRejection(t *testing.T) {
	f := newFixture(t)
	f.remote.setTree(testSHA, map[string]string{
		// 缺 SKILL.md。
		"readme.md": "# 没有清单\n",
	})
	_, err := f.service.Import(context.Background(), ImportParams{
		RepositoryURL: "https://github.com/" + testOwner + "/" + testRepo,
	})
	if !errors.Is(err, ErrPackageInvalid) {
		t.Fatalf("err = %v，期望 ErrPackageInvalid", err)
	}
	skills, err := f.store.ListSkills(context.Background())
	if err != nil {
		t.Fatalf("列出技能: %v", err)
	}
	if len(skills) != 0 {
		t.Errorf("被拒的纳管留下了 %d 条技能", len(skills))
	}
}

// 地址形状不合法时**不发生任何出站请求**：校验要发生在取字节之前。
func TestImportRejectsBadAddressBeforeAnyRequest(t *testing.T) {
	f := newFixture(t)
	_, err := f.service.Import(context.Background(), ImportParams{
		RepositoryURL: "https://gitlab.com/owner/repo",
	})
	if !errors.Is(err, ErrRepositoryInvalid) {
		t.Fatalf("err = %v，期望 ErrRepositoryInvalid", err)
	}
	if len(f.remote.resolved) != 0 || len(f.remote.fetched) != 0 {
		t.Errorf("被拒的地址仍然发起了请求: %v %v", f.remote.resolved, f.remote.fetched)
	}
}

// 取字节的地址由服务端拼：远端实现收到的是（仓库、引用、子路径），**收不到**调用方
// 给的那串地址。
func TestImportHandsRemoteOnlyTheParsedSource(t *testing.T) {
	f := newFixture(t)
	f.remote.setTree(testSHA, map[string]string{
		"skills/mono/" + ManifestPath: manifest("mono-color", "出图。"),
		"skills/other/x.md":           "# 不属于这个包\n",
	})
	if _, err := f.service.Import(context.Background(), ImportParams{
		RepositoryURL: "https://github.com/" + testOwner + "/" + testRepo + ".git/",
		Ref:           "v1.0.0",
		SubPath:       "skills/mono",
	}); err != nil {
		t.Fatalf("纳管失败: %v", err)
	}
	want := testOwner + "/" + testRepo + "@v1.0.0"
	if len(f.remote.resolved) != 1 || f.remote.resolved[0] != want {
		t.Errorf("远端收到的解析输入 = %v，期望 %q", f.remote.resolved, want)
	}
	if len(f.remote.fetched) != 1 || !strings.Contains(f.remote.fetched[0], "#skills/mono") {
		t.Errorf("远端收到的取回输入 = %v", f.remote.fetched)
	}
}

func TestImportWithoutObjectStoreIsUnavailable(t *testing.T) {
	store := NewMemoryStore()
	service := NewService(Deps{Store: store, Remote: newFakeRemote()})
	if _, err := service.Import(context.Background(), ImportParams{
		RepositoryURL: "https://github.com/owner/repo",
	}); !errors.Is(err, ErrObjectStoreUnavailable) {
		t.Errorf("err = %v，期望 ErrObjectStoreUnavailable", err)
	}
	if capabilities := service.Capabilities(); capabilities.CatalogEnabled {
		t.Error("没有对象存储时目录被报告为可用")
	}
}

// 说明层留空时回退到当前版本的 name / description；覆盖之后以覆盖为准；清空回到
// 回退值。
func TestEffectiveMetadataFallsBackToManifest(t *testing.T) {
	f := newFixture(t)
	item := f.importOne(t)
	ctx := context.Background()

	view, err := f.service.GetSkill(ctx, "subject-a", item.ID)
	if err != nil {
		t.Fatalf("读取技能: %v", err)
	}
	if view.EffectiveTitle() != "mono-color" {
		t.Errorf("有效标题 = %q，期望回退到 name", view.EffectiveTitle())
	}
	if !strings.Contains(view.EffectiveSummary(), "单色印刷风出图") {
		t.Errorf("有效简介 = %q，期望回退到 description", view.EffectiveSummary())
	}

	if _, err = f.service.UpdateMetadata(ctx, item.ID, "单色出图", "", []string{"出图"}); err != nil {
		t.Fatalf("改说明层: %v", err)
	}
	view, err = f.service.GetSkill(ctx, "subject-a", item.ID)
	if err != nil {
		t.Fatalf("读取技能: %v", err)
	}
	if view.EffectiveTitle() != "单色出图" {
		t.Errorf("覆盖之后的标题 = %q", view.EffectiveTitle())
	}
	if !strings.Contains(view.EffectiveSummary(), "单色印刷风出图") {
		t.Errorf("没覆盖的简介被改动了: %q", view.EffectiveSummary())
	}

	// 清空 = 回到回退值。
	if _, err = f.service.UpdateMetadata(ctx, item.ID, "", "", []string{"出图"}); err != nil {
		t.Fatalf("改说明层: %v", err)
	}
	view, err = f.service.GetSkill(ctx, "subject-a", item.ID)
	if err != nil {
		t.Fatalf("读取技能: %v", err)
	}
	if view.EffectiveTitle() != "mono-color" {
		t.Errorf("清空之后的标题 = %q，期望回到 name", view.EffectiveTitle())
	}
}

// 改说明层**不改内容层任何一项**。
func TestUpdateMetadataLeavesContentUntouched(t *testing.T) {
	f := newFixture(t)
	item := f.importOne(t)
	ctx := context.Background()

	updated, err := f.service.UpdateMetadata(ctx, item.ID, "新标题", "新简介", []string{"排版"})
	if err != nil {
		t.Fatalf("改说明层: %v", err)
	}
	if updated.Current.ID != item.Current.ID {
		t.Errorf("当前版本被改了: %q → %q", item.Current.ID, updated.Current.ID)
	}
	if len(updated.Current.Files) != len(item.Current.Files) {
		t.Fatalf("文件数变了: %d → %d", len(item.Current.Files), len(updated.Current.Files))
	}
	for i, file := range updated.Current.Files {
		if file != item.Current.Files[i] {
			t.Errorf("第 %d 条文件变了: %+v → %+v", i, item.Current.Files[i], file)
		}
	}
	if updated.Current.Name != "mono-color" || updated.Current.Description != item.Current.Description {
		t.Error("版本的 name / description 被改动了")
	}
}

// 同步：远端没有新提交时**不产生版本、不留痕、不动指针**。
func TestResyncWithoutRemoteChangeDoesNothing(t *testing.T) {
	f := newFixture(t)
	item := f.importOne(t)

	before, err := f.service.ListVersions(context.Background(), item.ID)
	if err != nil {
		t.Fatalf("列出版本: %v", err)
	}
	after, changed, err := f.service.Resync(context.Background(), item.ID)
	if err != nil {
		t.Fatalf("同步: %v", err)
	}
	if changed {
		t.Error("远端没有变化却报告有变化")
	}
	if after.Current.ID != item.Current.ID {
		t.Errorf("当前版本变了: %q → %q", item.Current.ID, after.Current.ID)
	}
	versions, err := f.service.ListVersions(context.Background(), item.ID)
	if err != nil {
		t.Fatalf("列出版本: %v", err)
	}
	if len(versions) != len(before) {
		t.Errorf("版本数从 %d 变成 %d", len(before), len(versions))
	}
}

// 同步：有变化时落新版本、切指针，而**旧版本读回来逐字不变**。
func TestResyncProducesNewImmutableVersion(t *testing.T) {
	f := newFixture(t)
	item := f.importOne(t)
	oldVersion := item.Current.ID
	ctx := context.Background()

	const newSHA = "abcdef1234567890abcdef1234567890abcdef12"
	f.remote.setTree(newSHA, map[string]string{
		ManifestPath: manifest("mono-color", "改过触发说明的一版。"),
		"palette.md": "# 色板（第二版）\n",
	})

	updated, changed, err := f.service.Resync(ctx, item.ID)
	if err != nil {
		t.Fatalf("同步: %v", err)
	}
	if !changed {
		t.Fatal("远端有新提交却报告没有变化")
	}
	if updated.Current.ID == oldVersion {
		t.Error("指针没有切换")
	}
	if updated.Source.Commit != newSHA {
		t.Errorf("记录的提交 = %q", updated.Source.Commit)
	}

	versions, err := f.service.ListVersions(ctx, item.ID)
	if err != nil {
		t.Fatalf("列出版本: %v", err)
	}
	if len(versions) != 2 {
		t.Fatalf("版本数 = %d，期望 2", len(versions))
	}
	// 旧版本仍在，且内容与当时一致。
	var found bool
	for _, version := range versions {
		if version.ID != oldVersion {
			continue
		}
		found = true
		if version.Commit != testSHA {
			t.Errorf("旧版本的提交被改了: %q", version.Commit)
		}
		if version.Description != item.Current.Description {
			t.Errorf("旧版本的 description 被改了: %q", version.Description)
		}
		if len(version.Files) != len(item.Current.Files) {
			t.Errorf("旧版本的文件清单被改了")
		}
	}
	if !found {
		t.Error("旧版本不见了")
	}
}

// 同步失败时**当前指针不动**：校验没过只是"这一次没同步成功"。
func TestResyncFailureKeepsPointer(t *testing.T) {
	f := newFixture(t)
	item := f.importOne(t)

	const newSHA = "deadbeef1234567890abcdef1234567890abcdef"
	f.remote.setTree(newSHA, map[string]string{"readme.md": "# 缺清单\n"})
	if _, _, err := f.service.Resync(context.Background(), item.ID); !errors.Is(err, ErrPackageInvalid) {
		t.Fatalf("err = %v，期望 ErrPackageInvalid", err)
	}
	after, err := f.service.GetSkill(context.Background(), "subject-a", item.ID)
	if err != nil {
		t.Fatalf("读取技能: %v", err)
	}
	if after.Current.ID != item.Current.ID {
		t.Errorf("同步失败却换了版本: %q → %q", item.Current.ID, after.Current.ID)
	}
	if after.Source.Commit != testSHA {
		t.Errorf("同步失败却改了记录的提交: %q", after.Source.Commit)
	}
}

// 回滚只切指针，而**读到的正文随之变回目标版本那一份**。
func TestSetCurrentVersionRollsBackContent(t *testing.T) {
	f := newFixture(t)
	item := f.importOne(t)
	ctx := context.Background()
	oldVersion := item.Current.ID

	const newSHA = "0f0f0f0f1234567890abcdef1234567890abcdef"
	f.remote.setTree(newSHA, map[string]string{
		ManifestPath: manifest("mono-color", "第二版。"),
		"palette.md": "# 第二版色板\n",
	})
	if _, _, err := f.service.Resync(ctx, item.ID); err != nil {
		t.Fatalf("同步: %v", err)
	}
	if got, err := f.service.GetFile(ctx, "subject-a", item.ID, "palette.md"); err != nil {
		t.Fatalf("取用: %v", err)
	} else if string(got.Data) != "# 第二版色板\n" {
		t.Fatalf("同步后的正文 = %q", got.Data)
	}

	rolled, err := f.service.SetCurrentVersion(ctx, item.ID, oldVersion)
	if err != nil {
		t.Fatalf("回滚: %v", err)
	}
	if rolled.Current.ID != oldVersion {
		t.Errorf("回滚之后的当前版本 = %q", rolled.Current.ID)
	}
	if got, err := f.service.GetFile(ctx, "subject-a", item.ID, "palette.md"); err != nil {
		t.Fatalf("取用: %v", err)
	} else if string(got.Data) != "# 色板\n" {
		t.Errorf("回滚后的正文 = %q，期望第一版", got.Data)
	}
}

// 版本不属于这个技能时，结论与"版本不存在"相同——区分它们等于提供一个跨技能的
// 版本枚举接口。
func TestSetCurrentVersionRejectsForeignVersion(t *testing.T) {
	f := newFixture(t)
	first := f.importOne(t)

	f.remote.setTree("aaaa111122223333444455556666777788889999", validTree())
	second, err := f.service.Import(context.Background(), ImportParams{
		RepositoryURL: "https://github.com/other/repo",
	})
	if err != nil {
		t.Fatalf("纳管第二个技能: %v", err)
	}
	if _, err := f.service.SetCurrentVersion(context.Background(), first.ID, second.Current.ID); !errors.Is(err, ErrVersionNotFound) {
		t.Errorf("err = %v，期望 ErrVersionNotFound", err)
	}
}

// 删除只删行：桶上的字节留着——内容对象按摘要全局共享，同一份字节可能正被别处
// 引用。
func TestDeleteLeavesSharedBytes(t *testing.T) {
	f := newFixture(t)
	item := f.importOne(t)
	digest := item.Current.Files[0].Digest

	if err := f.service.Delete(context.Background(), item.ID); err != nil {
		t.Fatalf("删除: %v", err)
	}
	if _, err := f.service.GetSkill(context.Background(), "subject-a", item.ID); !errors.Is(err, ErrSkillNotFound) {
		t.Errorf("删除之后仍能读到技能: %v", err)
	}
	if _, err := f.objects.Read(context.Background(), ContentObjectKey(digest)); err != nil {
		t.Errorf("删除把共享的字节删掉了: %v", err)
	}
}

// 收藏是**主体级的**：一个人收藏不改变另一个人看到的。
func TestFavoriteIsPerSubject(t *testing.T) {
	f := newFixture(t)
	item := f.importOne(t)
	ctx := context.Background()

	if err := f.service.SetFavorite(ctx, "subject-a", item.ID, true); err != nil {
		t.Fatalf("收藏: %v", err)
	}
	mine, err := f.service.GetSkill(ctx, "subject-a", item.ID)
	if err != nil {
		t.Fatalf("读取技能: %v", err)
	}
	theirs, err := f.service.GetSkill(ctx, "subject-b", item.ID)
	if err != nil {
		t.Fatalf("读取技能: %v", err)
	}
	if !mine.Favorited {
		t.Error("收藏没有生效")
	}
	if theirs.Favorited {
		t.Error("别人的收藏影响到了我")
	}

	// 幂等：再收藏一次不报错，取消两次也不报错。
	if err := f.service.SetFavorite(ctx, "subject-a", item.ID, true); err != nil {
		t.Fatalf("重复收藏: %v", err)
	}
	for i := 0; i < 2; i++ {
		if err := f.service.SetFavorite(ctx, "subject-a", item.ID, false); err != nil {
			t.Fatalf("取消收藏: %v", err)
		}
	}
}

// 收藏**不改变排序**：排序必须确定，否则"我上次看到的第三条"无法复现。
func TestFavoriteDoesNotReorder(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	f.remote.setTree(testSHA, map[string]string{
		ManifestPath: manifest("alpha", "甲。"),
	})
	alpha, err := f.service.Import(ctx, ImportParams{RepositoryURL: "https://github.com/o/alpha"})
	if err != nil {
		t.Fatalf("纳管: %v", err)
	}
	f.remote.setTree(testSHA, map[string]string{
		ManifestPath: manifest("beta", "乙。"),
	})
	if _, err := f.service.Import(ctx, ImportParams{RepositoryURL: "https://github.com/o/beta"}); err != nil {
		t.Fatalf("纳管: %v", err)
	}

	before, err := f.service.ListSkills(ctx, "subject-a", Filter{})
	if err != nil {
		t.Fatalf("列出: %v", err)
	}
	if err := f.service.SetFavorite(ctx, "subject-a", alpha.ID, true); err != nil {
		t.Fatalf("收藏: %v", err)
	}
	after, err := f.service.ListSkills(ctx, "subject-a", Filter{})
	if err != nil {
		t.Fatalf("列出: %v", err)
	}
	for i := range before.Skills {
		if before.Skills[i].ID != after.Skills[i].ID {
			t.Fatalf("收藏改变了顺序: 第 %d 位 %q → %q", i, before.Skills[i].ID, after.Skills[i].ID)
		}
	}
}

func TestListSkillsFiltering(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.remote.setTree(testSHA, map[string]string{
		ManifestPath: manifest("mono-color", "单色印刷风出图。"),
	})
	if _, err := f.service.Import(ctx, ImportParams{
		RepositoryURL: "https://github.com/o/mono", Tags: []string{"出图", "印刷"},
	}); err != nil {
		t.Fatalf("纳管: %v", err)
	}
	f.remote.setTree(testSHA, map[string]string{
		ManifestPath: manifest("taste", "前端审美与反模板化。"),
	})
	if _, err := f.service.Import(ctx, ImportParams{
		RepositoryURL: "https://github.com/o/taste", Tags: []string{"前端"},
	}); err != nil {
		t.Fatalf("纳管: %v", err)
	}

	// 关键词大小写不敏感，且能命中简介。
	got, err := f.service.ListSkills(ctx, "subject-a", Filter{Query: "TASTE"})
	if err != nil {
		t.Fatalf("列出: %v", err)
	}
	if len(got.Skills) != 1 || got.Skills[0].EffectiveTitle() != "taste" {
		t.Errorf("按标题搜到 %d 条", len(got.Skills))
	}
	got, err = f.service.ListSkills(ctx, "subject-a", Filter{Query: "单色印刷风"})
	if err != nil {
		t.Fatalf("列出: %v", err)
	}
	if len(got.Skills) != 1 || got.Skills[0].Current.Name != "mono-color" {
		t.Errorf("按简介搜到 %d 条", len(got.Skills))
	}

	// 标签是**取交集**。
	got, err = f.service.ListSkills(ctx, "subject-a", Filter{Tags: []string{"出图", "印刷"}})
	if err != nil {
		t.Fatalf("列出: %v", err)
	}
	if len(got.Skills) != 1 {
		t.Errorf("两个标签取交集得到 %d 条", len(got.Skills))
	}
	got, err = f.service.ListSkills(ctx, "subject-a", Filter{Tags: []string{"出图", "前端"}})
	if err != nil {
		t.Fatalf("列出: %v", err)
	}
	if len(got.Skills) != 0 {
		t.Errorf("没有一条同时带这两个标签，却得到 %d 条", len(got.Skills))
	}

	// 候选标签是**整个目录**的，不随筛选收窄。
	got, err = f.service.ListSkills(ctx, "subject-a", Filter{Tags: []string{"出图"}})
	if err != nil {
		t.Fatalf("列出: %v", err)
	}
	if len(got.AvailableTags) != 3 {
		t.Errorf("候选标签 = %v，期望整个目录的 3 个", got.AvailableTags)
	}

	// 只看收藏。
	if err := f.service.SetFavorite(ctx, "subject-a", got.Skills[0].ID, true); err != nil {
		t.Fatalf("收藏: %v", err)
	}
	got, err = f.service.ListSkills(ctx, "subject-a", Filter{FavoritedOnly: true})
	if err != nil {
		t.Fatalf("列出: %v", err)
	}
	if len(got.Skills) != 1 {
		t.Errorf("只看收藏得到 %d 条", len(got.Skills))
	}
}

// 排序是确定的：按有效标题的字典序，标题相同时按标识。
func TestListSkillsOrderIsDeterministic(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	for _, name := range []string{"zeta", "alpha", "mu"} {
		f.remote.setTree(testSHA, map[string]string{
			ManifestPath: manifest(name, "说明。"),
		})
		if _, err := f.service.Import(ctx, ImportParams{
			RepositoryURL: "https://github.com/o/" + name,
		}); err != nil {
			t.Fatalf("纳管: %v", err)
		}
	}
	first, err := f.service.ListSkills(ctx, "subject-a", Filter{})
	if err != nil {
		t.Fatalf("列出: %v", err)
	}
	second, err := f.service.ListSkills(ctx, "subject-a", Filter{})
	if err != nil {
		t.Fatalf("列出: %v", err)
	}
	if len(first.Skills) != 3 {
		t.Fatalf("得到 %d 条", len(first.Skills))
	}
	for i := range first.Skills {
		if first.Skills[i].ID != second.Skills[i].ID {
			t.Fatalf("两次查询的顺序不同: 第 %d 位", i)
		}
	}
	if first.Skills[0].EffectiveTitle() != "alpha" || first.Skills[2].EffectiveTitle() != "zeta" {
		t.Errorf("顺序 = %v", []string{
			first.Skills[0].EffectiveTitle(),
			first.Skills[1].EffectiveTitle(),
			first.Skills[2].EffectiveTitle(),
		})
	}
}

// 取用是使用量的**计量点**：同一个人在同一天里取几次都算一次；两个人算两次。
func TestGetFileCountsUseDays(t *testing.T) {
	f := newFixture(t)
	item := f.importOne(t)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if _, err := f.service.GetFile(ctx, "subject-a", item.ID, "palette.md"); err != nil {
			t.Fatalf("取用: %v", err)
		}
	}
	if _, err := f.service.GetFile(ctx, "subject-a", item.ID, ManifestPath); err != nil {
		t.Fatalf("取用: %v", err)
	}
	if _, err := f.service.GetFile(ctx, "subject-b", item.ID, ManifestPath); err != nil {
		t.Fatalf("取用: %v", err)
	}

	view, err := f.service.GetSkill(ctx, "subject-a", item.ID)
	if err != nil {
		t.Fatalf("读取技能: %v", err)
	}
	if view.Usage.UseDays != 2 {
		t.Errorf("使用日次 = %d，期望 2（两个人各一天）", view.Usage.UseDays)
	}
	if view.Usage.Users != 2 {
		t.Errorf("使用人数 = %d，期望 2", view.Usage.Users)
	}
	if view.Usage.LastUsedAt.IsZero() {
		t.Error("最近使用时间没有记下")
	}

	// 换一天再取一次，日次加一。
	f.now = f.now.Add(24 * time.Hour)
	if _, err := f.service.GetFile(ctx, "subject-a", item.ID, ManifestPath); err != nil {
		t.Fatalf("取用: %v", err)
	}
	view, err = f.service.GetSkill(ctx, "subject-a", item.ID)
	if err != nil {
		t.Fatalf("读取技能: %v", err)
	}
	if view.Usage.UseDays != 3 {
		t.Errorf("跨天之后的使用日次 = %d，期望 3", view.Usage.UseDays)
	}
}

// 取一条不存在的路径与"技能不存在"是**两个不同的结论**。
func TestGetFileDistinguishesMissingPathFromMissingSkill(t *testing.T) {
	f := newFixture(t)
	item := f.importOne(t)
	ctx := context.Background()

	if _, err := f.service.GetFile(ctx, "subject-a", item.ID, "nope.md"); !errors.Is(err, ErrFileNotFound) {
		t.Errorf("err = %v，期望 ErrFileNotFound", err)
	}
	if _, err := f.service.GetFile(ctx, "subject-a", "skl_missing", ManifestPath); !errors.Is(err, ErrSkillNotFound) {
		t.Errorf("err = %v，期望 ErrSkillNotFound", err)
	}
}

// 超过保留期的使用日次在回收之后不再存在。
func TestPurgeUsage(t *testing.T) {
	f := newFixture(t)
	item := f.importOne(t)
	ctx := context.Background()

	if _, err := f.service.GetFile(ctx, "subject-a", item.ID, ManifestPath); err != nil {
		t.Fatalf("取用: %v", err)
	}
	f.now = f.now.Add(UsageRetention + 24*time.Hour)
	if _, err := f.service.GetFile(ctx, "subject-a", item.ID, ManifestPath); err != nil {
		t.Fatalf("取用: %v", err)
	}
	if err := f.service.PurgeUsage(ctx); err != nil {
		t.Fatalf("回收: %v", err)
	}
	view, err := f.service.GetSkill(ctx, "subject-a", item.ID)
	if err != nil {
		t.Fatalf("读取技能: %v", err)
	}
	// 只剩今天那一条：过期的那条被回收了。
	if view.Usage.UseDays != 1 {
		t.Errorf("回收之后的使用日次 = %d，期望 1", view.Usage.UseDays)
	}
}
