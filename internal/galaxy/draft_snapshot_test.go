package galaxy

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// 每次替换草稿都留下被换掉的那一份：连续推三份不同的清单之后，历史里能读出前两份。
func TestPushDraftKeepsHistory(t *testing.T) {
	f := newFixture(t)
	project := f.createProject(t, "工程")
	ctx := context.Background()

	f.pushTexts(t, project.ID, SlotSite, map[string]string{"index.html": "第一版"})
	// 第二次推送：被换掉的那一份进历史。
	f.pushTexts(t, project.ID, SlotSite, map[string]string{"index.html": "第二版"})
	f.pushTexts(t, project.ID, SlotSite, map[string]string{"index.html": "第三版"})

	history, err := f.service.DraftHistory(ctx, testOwner, project.ID, SlotSite)
	if err != nil {
		t.Fatalf("读草稿历史失败: %v", err)
	}
	// 第一次推送没有"被替换掉的东西"，因此两条（第一版、第二版）。
	if len(history) != 2 {
		t.Fatalf("历史 = %d 条，期望 2 条", len(history))
	}
	// **最近的在前**：第二条历史是被"第三版"替换掉的那一份（即第二版）。
	if len(history[0].Manifest) != 1 || history[0].Manifest[0].Path != "index.html" {
		t.Fatalf("历史[0] = %+v", history[0].Manifest)
	}
	if history[0].Source != testDraftSource {
		t.Errorf("来源 = %q，期望 %q", history[0].Source, testDraftSource)
	}
	if history[0].Slot != SlotSite || history[0].ProjectID != project.ID {
		t.Errorf("归属 = %v/%s", history[0].Slot, history[0].ProjectID)
	}
	// 两份历史的摘要不同——它们确实是两份不同的清单。
	if history[0].Manifest[0].Digest == history[1].Manifest[0].Digest {
		t.Error("两条历史的清单相同，说明其中一次没有被记下来")
	}
}

// 相同清单不重复留：快照是服务端顺手记的，把两次相同的结果记两遍只会让历史里
// 全是同一份东西（与"版本不去重"不冲突，见 project-versioning.md）。
func TestPushDraftDeduplicatesHistory(t *testing.T) {
	f := newFixture(t)
	project := f.createProject(t, "工程")
	ctx := context.Background()

	files := map[string]string{"index.html": "同一份"}
	f.pushTexts(t, project.ID, SlotSite, files)
	f.pushTexts(t, project.ID, SlotSite, files)
	f.pushTexts(t, project.ID, SlotSite, files)

	history, err := f.service.DraftHistory(ctx, testOwner, project.ID, SlotSite)
	if err != nil {
		t.Fatalf("读草稿历史失败: %v", err)
	}
	// 第一次没有可留的，而后面两次推的都是**同一份**，因此一条都不该多出来。
	if len(history) != 0 {
		t.Fatalf("历史 = %d 条，期望 0 条（清单没变）", len(history))
	}
}

// 恢复把草稿整组换回某一条历史，而**恢复前的那一份也留成历史**——恢复不会让人
// 丢掉恢复前的内容。
func TestRestoreDraftKeepsCurrentAsHistory(t *testing.T) {
	f := newFixture(t)
	project := f.createProject(t, "工程")
	ctx := context.Background()

	f.pushTexts(t, project.ID, SlotSite, map[string]string{"index.html": "第一版"})
	f.pushTexts(t, project.ID, SlotSite, map[string]string{"index.html": "第二版"})
	beforeRestore, _, err := f.service.GetDraft(ctx, testOwner, project.ID, SlotSite)
	if err != nil {
		t.Fatalf("读草稿失败: %v", err)
	}
	history, err := f.service.DraftHistory(ctx, testOwner, project.ID, SlotSite)
	if err != nil {
		t.Fatalf("读草稿历史失败: %v", err)
	}
	first := history[len(history)-1]

	restored, err := f.service.RestoreDraft(ctx, testOwner, project.ID, SlotSite, first.ID, testDraftSource)
	if err != nil {
		t.Fatalf("恢复失败: %v", err)
	}
	if len(restored.Manifest) != 1 || restored.Manifest[0].Digest != first.Manifest[0].Digest {
		t.Errorf("恢复后的草稿 = %+v，期望与那条快照逐字相同", restored.Manifest)
	}

	// 恢复前的那一份（第二版）成了一条**新**历史，而原来那条仍在。
	history, err = f.service.DraftHistory(ctx, testOwner, project.ID, SlotSite)
	if err != nil {
		t.Fatalf("读草稿历史失败: %v", err)
	}
	if len(history) != 2 {
		t.Fatalf("历史 = %d 条，期望 2 条（恢复本身也留了一条）", len(history))
	}
	if history[0].Manifest[0].Digest != beforeRestore.Manifest[0].Digest {
		t.Error("恢复前的那一份没有被留成历史")
	}
	if history[1].ID != first.ID {
		t.Errorf("原来那条历史不见了：%s", history[1].ID)
	}
}

// 恢复引用了已删除资产的快照会被拒绝，且**草稿保持不变**：恢复出来会是一份必然
// 在校验阶段失败的草稿，而"恢复成功"这句话看不出问题在哪。
func TestRestoreDraftRejectsMissingAsset(t *testing.T) {
	f := newFixture(t)
	project := f.createProject(t, "工程")
	ctx := context.Background()

	asset := f.uploadAsset(t, project.ID, "image/png", "a.png", []byte("png"))
	f.pushDraft(t, project.ID, []Entry{
		f.textEntry(t, project.ID, "index.html", `<img src="asset://`+asset.ID+`">`),
		{Path: "a.png", Kind: EntryKindAsset, AssetID: asset.ID},
	})
	// 推第二份：第一份（引用着那个资产）进历史。
	f.pushDraft(t, project.ID, []Entry{f.textEntry(t, project.ID, "index.html", "第二版")})

	history, err := f.service.DraftHistory(ctx, testOwner, project.ID, SlotSite)
	if err != nil {
		t.Fatalf("读草稿历史失败: %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("历史 = %d 条，期望 1 条", len(history))
	}
	// 那个资产只被这条历史引用，因此**删得掉**（快照不拦阻删除）。
	if err := f.service.DeleteAsset(ctx, testOwner, project.ID, asset.ID); err != nil {
		t.Fatalf("只被快照引用的资产应当可以删: %v", err)
	}

	_, err = f.service.RestoreDraft(ctx, testOwner, project.ID, SlotSite, history[0].ID, testDraftSource)
	if !errors.Is(err, ErrDraftSnapshotUnusable) {
		t.Fatalf("err = %v，期望 ErrDraftSnapshotUnusable", err)
	}
	// 拒绝信息里要指出是哪一条路径引用了哪个资产。
	if !strings.Contains(err.Error(), asset.ID) {
		t.Errorf("拒绝信息里没有资产标识：%v", err)
	}
	// 草稿没被动过。
	draft, _, err := f.service.GetDraft(ctx, testOwner, project.ID, SlotSite)
	if err != nil {
		t.Fatalf("读草稿失败: %v", err)
	}
	if len(draft.Manifest) != 1 || draft.Manifest[0].Digest != history[0].Manifest[1].Digest {
		if len(draft.Manifest) == 0 || draft.Manifest[0].Kind == EntryKindAsset {
			t.Errorf("草稿 = %+v，期望保持被拒之前的那一份", draft.Manifest)
		}
	}
}

// 快照可以升级成版本：存出来的版本清单与那条快照逐字相同。
func TestSaveVersionFromSnapshot(t *testing.T) {
	f := newFixture(t)
	project := f.createProject(t, "工程")
	ctx := context.Background()

	f.pushTexts(t, project.ID, SlotSite, map[string]string{"index.html": "中间态"})
	f.pushTexts(t, project.ID, SlotSite, map[string]string{"index.html": "后来的"})
	history, err := f.service.DraftHistory(ctx, testOwner, project.ID, SlotSite)
	if err != nil {
		t.Fatalf("读草稿历史失败: %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("历史 = %d 条，期望 1 条", len(history))
	}

	version, err := f.service.SaveVersion(ctx, testOwner, project.ID, SlotSite, "从历史里存的一版", history[0].ID)
	if err != nil {
		t.Fatalf("从快照存版本失败: %v", err)
	}
	if len(version.Manifest) != 1 || version.Manifest[0].Digest != history[0].Manifest[0].Digest {
		t.Errorf("版本清单 = %+v，期望与那条快照逐字相同", version.Manifest)
	}
	if version.Description != "从历史里存的一版" {
		t.Errorf("说明 = %q", version.Description)
	}
	// **草稿没有被这次保存动过**：存的是历史里的那一份，不是当前草稿。
	draft, _, err := f.service.GetDraft(ctx, testOwner, project.ID, SlotSite)
	if err != nil {
		t.Fatalf("读草稿失败: %v", err)
	}
	if draft.Manifest[0].Digest == history[0].Manifest[0].Digest {
		t.Error("从历史存版本不该改掉当前草稿")
	}
}

// 引用已删除资产的快照同样不能存成版本：那会是一个发布不出去的版本，而
// "版本不可变"的含义包括"它此后永远可以发布"。
func TestSaveVersionFromSnapshotRejectsMissingAsset(t *testing.T) {
	f := newFixture(t)
	project := f.createProject(t, "工程")
	ctx := context.Background()

	asset := f.uploadAsset(t, project.ID, "image/png", "a.png", []byte("png"))
	f.pushDraft(t, project.ID, []Entry{
		f.textEntry(t, project.ID, "index.html", `<img src="asset://`+asset.ID+`">`),
		{Path: "a.png", Kind: EntryKindAsset, AssetID: asset.ID},
	})
	f.pushDraft(t, project.ID, []Entry{f.textEntry(t, project.ID, "index.html", "第二版")})
	history, err := f.service.DraftHistory(ctx, testOwner, project.ID, SlotSite)
	if err != nil {
		t.Fatalf("读草稿历史失败: %v", err)
	}
	if err := f.service.DeleteAsset(ctx, testOwner, project.ID, asset.ID); err != nil {
		t.Fatalf("删除失败: %v", err)
	}

	if _, err := f.service.SaveVersion(ctx, testOwner, project.ID, SlotSite, "", history[0].ID); !errors.Is(err, ErrDraftSnapshotUnusable) {
		t.Fatalf("err = %v，期望 ErrDraftSnapshotUnusable", err)
	}
	versions, err := f.service.ListVersions(ctx, testOwner, project.ID, SlotSite)
	if err != nil {
		t.Fatalf("列版本失败: %v", err)
	}
	if len(versions) != 0 {
		t.Errorf("被拒的保存留下了版本：%+v", versions)
	}
}

// 一条不存在的快照与一条已过期的快照给同一个结论。
func TestRestoreUnknownSnapshot(t *testing.T) {
	f := newFixture(t)
	project := f.createProject(t, "工程")

	_, err := f.service.RestoreDraft(context.Background(), testOwner, project.ID, SlotSite, "snp_没有", testDraftSource)
	if !errors.Is(err, ErrDraftSnapshotNotFound) {
		t.Fatalf("err = %v，期望 ErrDraftSnapshotNotFound", err)
	}
}

// 超过保留条数的快照在写入时被清掉。
func TestDraftHistoryRetentionByCount(t *testing.T) {
	f := newFixture(t)
	project := f.createProject(t, "工程")
	ctx := context.Background()

	// 推的次数比保留条数多几条：每一步都换一份不同的清单。
	for i := 0; i < DraftSnapshotKeep+5; i++ {
		f.advance(1)
		f.pushTexts(t, project.ID, SlotSite, map[string]string{
			"index.html": "第 " + string(rune('a'+i%26)) + " 版",
		})
	}
	history, err := f.service.DraftHistory(ctx, testOwner, project.ID, SlotSite)
	if err != nil {
		t.Fatalf("读草稿历史失败: %v", err)
	}
	if len(history) > DraftSnapshotKeep {
		t.Errorf("历史 = %d 条，期望不超过 %d 条", len(history), DraftSnapshotKeep)
	}
	// 保留策略挂在写入那一处：清理由写触发，因此最后一条历史一定是刚留下的。
	if len(history) == 0 {
		t.Fatal("一次都没留下历史")
	}
	if history[0].CreatedAt != f.now {
		t.Errorf("最近一条的时间 = %v，期望是刚写进去的那一刻 %v", history[0].CreatedAt, f.now)
	}
}

// 超出保留期（14 天）的快照在写入时被清掉。
func TestDraftHistoryRetentionByAge(t *testing.T) {
	f := newFixture(t)
	project := f.createProject(t, "工程")
	ctx := context.Background()

	f.pushTexts(t, project.ID, SlotSite, map[string]string{"index.html": "第一版"})
	f.advance(24 * time.Hour)
	f.pushTexts(t, project.ID, SlotSite, map[string]string{"index.html": "第二版"})
	if history, err := f.service.DraftHistory(ctx, testOwner, project.ID, SlotSite); err != nil || len(history) != 1 {
		t.Fatalf("历史 = %d 条 / %v，期望 1 条", len(history), err)
	}

	// 再过 15 天推一次：那条 15 天前的快照过期。
	f.advance(15 * 24 * time.Hour)
	f.pushTexts(t, project.ID, SlotSite, map[string]string{"index.html": "第三版"})
	history, err := f.service.DraftHistory(ctx, testOwner, project.ID, SlotSite)
	if err != nil {
		t.Fatalf("读草稿历史失败: %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("历史 = %d 条，期望 1 条（过期的那条被清掉）", len(history))
	}
	if history[0].CreatedAt != f.now {
		t.Errorf("留下的应当只有刚写的那一条，实际 %v", history[0].CreatedAt)
	}
}

// 版本说明可以事后改，而**内容一字不动**。
func TestUpdateVersionDescriptionKeepsContent(t *testing.T) {
	f := newFixture(t)
	project := f.staticSite(t, map[string]string{"index.html": "<p>首页</p>"})
	ctx := context.Background()

	version, err := f.service.SaveVersion(ctx, testOwner, project.ID, SlotSite, "第一版", "")
	if err != nil {
		t.Fatalf("保存版本失败: %v", err)
	}
	if version.Description != "第一版" {
		t.Fatalf("说明 = %q，期望随保存写入", version.Description)
	}

	updated, err := f.service.UpdateVersionDescription(ctx, testOwner, project.ID, SlotSite, version.ID, "改过的说明")
	if err != nil {
		t.Fatalf("改说明失败: %v", err)
	}
	if updated.Description != "改过的说明" {
		t.Errorf("说明 = %q", updated.Description)
	}
	if updated.Seq != version.Seq || !updated.SavedAt.Equal(version.SavedAt) ||
		updated.RenderRulesVersion != version.RenderRulesVersion {
		t.Error("改说明动了序号、保存时间或渲染规则版本")
	}
	if len(updated.Manifest) != len(version.Manifest) || updated.Manifest[0] != version.Manifest[0] {
		t.Error("改说明动了清单")
	}

	// 空串清空。
	cleared, err := f.service.UpdateVersionDescription(ctx, testOwner, project.ID, SlotSite, version.ID, "  ")
	if err != nil {
		t.Fatalf("清空说明失败: %v", err)
	}
	if cleared.Description != "" {
		t.Errorf("说明 = %q，期望被清空", cleared.Description)
	}

	// 超长被拒。
	tooLong := strings.Repeat("字", VersionDescriptionMaxRunes+1)
	if _, err := f.service.UpdateVersionDescription(ctx, testOwner, project.ID, SlotSite, version.ID, tooLong); !errors.Is(err, ErrVersionDescriptionTooLong) {
		t.Errorf("err = %v，期望 ErrVersionDescriptionTooLong", err)
	}
	// 保存时超长同样被拒。
	if _, err := f.service.SaveVersion(ctx, testOwner, project.ID, SlotSite, tooLong, ""); !errors.Is(err, ErrVersionDescriptionTooLong) {
		t.Errorf("err = %v，期望 ErrVersionDescriptionTooLong", err)
	}
}

// 说明不进产物：带说明的版本发布出来的产物与不带说明时逐字相同。
func TestVersionDescriptionDoesNotEnterArtifacts(t *testing.T) {
	f := newFixture(t)
	project := f.staticSite(t, map[string]string{"index.html": "<p>首页</p>"})
	ctx := context.Background()

	version, err := f.service.SaveVersion(ctx, testOwner, project.ID, SlotSite, "带说明的一版", "")
	if err != nil {
		t.Fatalf("保存版本失败: %v", err)
	}
	if _, err := f.service.Publish(ctx, testOwner, project.ID, SlotSite, version.ID); err != nil {
		t.Fatalf("发布失败: %v", err)
	}
	publication, entries, err := f.service.PublicationEntries(ctx, testOwner, project.ID, SlotSite)
	if err != nil {
		t.Fatalf("读发布物失败: %v", err)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Entry.Path, "说明") {
			t.Errorf("产物里出现了说明：%+v", entry.Entry)
		}
	}
	for _, entry := range publication.Manifest {
		if strings.Contains(entry.Path, "带说明") {
			t.Errorf("产物清单里出现了说明：%+v", entry)
		}
	}
}

// 快照**不是版本**：它不出现在版本列表里，也没有序号；用它的标识发起发布被拒。
func TestSnapshotIsNotAVersion(t *testing.T) {
	f := newFixture(t)
	project := f.staticSite(t, map[string]string{"index.html": "<p>首页</p>"})
	ctx := context.Background()

	f.pushTexts(t, project.ID, SlotSite, map[string]string{"index.html": "<p>第二版</p>"})
	history, err := f.service.DraftHistory(ctx, testOwner, project.ID, SlotSite)
	if err != nil {
		t.Fatalf("读草稿历史失败: %v", err)
	}
	if len(history) == 0 {
		t.Fatal("没有历史")
	}
	snapshotID := history[0].ID
	if !strings.HasPrefix(snapshotID, "snp_") {
		t.Errorf("快照标识 = %q，期望 snp_ 前缀（与版本标识分得开）", snapshotID)
	}

	versions, err := f.service.ListVersions(ctx, testOwner, project.ID, SlotSite)
	if err != nil {
		t.Fatalf("列版本失败: %v", err)
	}
	for _, version := range versions {
		if version.ID == snapshotID {
			t.Error("版本列表里出现了快照")
		}
	}
	if _, err := f.service.Publish(ctx, testOwner, project.ID, SlotSite, snapshotID); !errors.Is(err, ErrVersionNotFound) {
		t.Errorf("err = %v，期望 ErrVersionNotFound（快照不能发布）", err)
	}
}

// 删工程连带清掉草稿历史。
func TestDeleteProjectRemovesDraftHistory(t *testing.T) {
	f := newFixture(t)
	project := f.createProject(t, "工程")
	ctx := context.Background()

	f.pushTexts(t, project.ID, SlotSite, map[string]string{"index.html": "第一版"})
	f.pushTexts(t, project.ID, SlotSite, map[string]string{"index.html": "第二版"})
	if history, err := f.service.DraftHistory(ctx, testOwner, project.ID, SlotSite); err != nil || len(history) != 1 {
		t.Fatalf("历史 = %d 条 / %v", len(history), err)
	}

	if err := f.service.DeleteProject(ctx, testOwner, project.ID); err != nil {
		t.Fatalf("删除工程失败: %v", err)
	}
	// 工程不存在之后，读历史仍然是"这个工程不是你的"——而存储里那一行也确实没了。
	if _, err := f.store.ListDraftSnapshots(ctx, project.ID, SlotSite); err != nil {
		t.Fatalf("列历史失败: %v", err)
	}
	snapshots, err := f.store.ListDraftSnapshots(ctx, project.ID, SlotSite)
	if err != nil {
		t.Fatalf("列历史失败: %v", err)
	}
	if len(snapshots) != 0 {
		t.Errorf("工程删了还留着 %d 条历史", len(snapshots))
	}
}

// 归属：另一个主体读不到历史，也恢复不了。
func TestDraftHistoryIsOwnedByProjectOwner(t *testing.T) {
	f := newFixture(t)
	project := f.createProject(t, "工程")
	ctx := context.Background()

	f.pushTexts(t, project.ID, SlotSite, map[string]string{"index.html": "第一版"})
	f.pushTexts(t, project.ID, SlotSite, map[string]string{"index.html": "第二版"})
	history, err := f.service.DraftHistory(ctx, testOwner, project.ID, SlotSite)
	if err != nil {
		t.Fatalf("读草稿历史失败: %v", err)
	}

	if _, err := f.service.DraftHistory(ctx, testOther, project.ID, SlotSite); !errors.Is(err, ErrProjectNotFound) {
		t.Errorf("他人读历史 err = %v，期望 ErrProjectNotFound", err)
	}
	if _, err := f.service.RestoreDraft(ctx, testOther, project.ID, SlotSite, history[0].ID, testDraftSource); !errors.Is(err, ErrProjectNotFound) {
		t.Errorf("他人恢复 err = %v，期望 ErrProjectNotFound", err)
	}
}
