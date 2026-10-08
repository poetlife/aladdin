package gormstore

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/poetlife/aladdin/internal/config"
	"github.com/poetlife/aladdin/internal/database"
	"github.com/poetlife/aladdin/internal/database/migrate"
	"github.com/poetlife/aladdin/internal/skill"
)

// 两个 skill 存储实现共用同一套用例。
//
// 理由与 galaxy、档案、会话的契约测试相同：**同一份契约只能有一套用例**。给 gorm
// 实现单独写一套，两套就会各自漂移，而漂移的表现是"换后端之后，同一个标签筛出
// 来的东西不一样"。
type storeCase struct {
	name string
	open func(t *testing.T) skill.Store
	// persistent 为真表示这个实现会把数据落到文件上，因此可以用"换一个实例再打开
	// 同一个库"来验证写入确实落了盘。内存实现没有这个性质。
	persistent bool
}

func storeCases(t *testing.T) []storeCase {
	t.Helper()
	return []storeCase{
		{
			name: "内存实现",
			open: func(*testing.T) skill.Store { return skill.NewMemoryStore() },
		},
		{
			name:       "关系库实现",
			open:       func(t *testing.T) skill.Store { return New(newSkillTestDB(t)) },
			persistent: true,
		},
	}
}

// testVersion 造一份最小的版本。
func testVersion(id, skillID string, at time.Time) skill.Version {
	return skill.Version{
		ID:          id,
		SkillID:     skillID,
		Commit:      "1f4a9c2d3e5b6a7089abcdef1234567890abcdef",
		Name:        "mono-color",
		Description: "单色印刷风出图。",
		Files: []skill.File{
			{Path: "SKILL.md", Digest: "a" + repeat("0", 63), SizeBytes: 42},
			{Path: "palette.md", Digest: "b" + repeat("0", 63), SizeBytes: 7},
		},
		// 非零：这一列在两个写入路径上都要落得下去。取零值当夹具的话，漏掉一处的
		// 实现会照样通过——而那正是它上次漏掉的原因。
		SkippedFiles: 3,
		CreatedAt:    at,
	}
}

// imageID 造一个属于某个技能的展示图标识。
//
// **带上技能标识**：图标识是全库唯一的主键，两个技能共用一套夹具标识会在写入第二
// 个技能时撞主键——那是一次夹具问题，却会表现成"存储实现坏了"。
func imageID(skillID, suffix string) string { return "ski_" + skillID + "_" + suffix }

// testImage 造一张属于某个技能的展示图。
func testImage(skillID, suffix string, size int64) skill.Image {
	id := imageID(skillID, suffix)
	return skill.Image{ID: id, ObjectKey: skill.ImageKey(skillID, id), SizeBytes: size}
}

// testImages 造一组**有序**的展示图。
//
// 非空且不止一张：顺序是这一组数据唯一会被写歪的地方（多一张、少一张、次序反了
// 都只有拿三条以上才看得出来），用空值或单张当夹具，漏掉顺序的实现会照样通过。
func testImages(skillID string) []skill.Image {
	return []skill.Image{
		testImage(skillID, "a", 1024),
		testImage(skillID, "b", 2048),
		testImage(skillID, "c", 4096),
	}
}

func testSkill(id string, at time.Time) skill.Skill {
	return skill.Skill{
		ID:      id,
		Title:   "",
		Summary: "",
		Images:  testImages(id),
		Tags:    []string{"出图", "排版"},
		Source: skill.Source{
			Owner: "yanliudesign", Name: "mono-color-skill",
			Ref: "main", SubPath: "skills/mono", Commit: "1f4a9c2d3e5b6a7089abcdef1234567890abcdef",
		},
		Current: testVersion("skv_"+id, id, at),
	}
}

func repeat(value string, count int) string {
	result := make([]byte, 0, len(value)*count)
	for i := 0; i < count; i++ {
		result = append(result, value...)
	}
	return string(result)
}

func TestStoreRoundTrip(t *testing.T) {
	at := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	for _, tc := range storeCases(t) {
		t.Run(tc.name, func(t *testing.T) {
			store := tc.open(t)
			ctx := context.Background()

			if err := store.CreateSkill(ctx, testSkill("skl_a", at)); err != nil {
				t.Fatalf("创建技能: %v", err)
			}
			got, err := store.GetSkill(ctx, "skl_a")
			if err != nil {
				t.Fatalf("读取技能: %v", err)
			}
			if got.Source.SubPath != "skills/mono" || got.Source.Commit == "" {
				t.Errorf("来源没有往返: %+v", got.Source)
			}
			if len(got.Tags) != 2 || got.Tags[0] != "出图" || got.Tags[1] != "排版" {
				t.Errorf("标签 = %v，期望升序的同一份", got.Tags)
			}
			if len(got.Current.Files) != 2 || got.Current.Files[0].Path != "SKILL.md" {
				t.Errorf("文件清单没有往返: %+v", got.Current.Files)
			}
			if got.Current.Description != "单色印刷风出图。" {
				t.Errorf("description = %q", got.Current.Description)
			}
			if got.Current.SkippedFiles != 3 {
				t.Errorf("跳过的条数 = %d，期望 3（创建这条路上丢了？）", got.Current.SkippedFiles)
			}
			if len(got.Images) != 3 {
				t.Fatalf("图集 = %+v，期望 3 张（创建这条路上丢了？）", got.Images)
			}
			for i, want := range testImages("skl_a") {
				if got.Images[i] != want {
					t.Errorf("第 %d 张 = %+v，期望 %+v（顺序丢了？）", i, got.Images[i], want)
				}
			}

			items, err := store.ListSkills(ctx)
			if err != nil {
				t.Fatalf("列出技能: %v", err)
			}
			if len(items) != 1 || items[0].ID != "skl_a" {
				t.Errorf("列出的技能 = %+v", items)
			}
		})
	}
}

func TestStoreMissingSkill(t *testing.T) {
	for _, tc := range storeCases(t) {
		t.Run(tc.name, func(t *testing.T) {
			store := tc.open(t)
			ctx := context.Background()
			if _, err := store.GetSkill(ctx, "skl_missing"); !errors.Is(err, skill.ErrSkillNotFound) {
				t.Errorf("err = %v，期望 ErrSkillNotFound", err)
			}
			if err := store.DeleteSkill(ctx, "skl_missing"); !errors.Is(err, skill.ErrSkillNotFound) {
				t.Errorf("删除不存在的技能 err = %v，期望 ErrSkillNotFound", err)
			}
			if err := store.SetFavorite(ctx, "skl_missing", "subject-a", true); !errors.Is(err, skill.ErrSkillNotFound) {
				t.Errorf("收藏不存在的技能 err = %v，期望 ErrSkillNotFound", err)
			}
		})
	}
}

// 版本**不可变**，且回滚只切指针：旧版本读回来逐字不变，版本的顺序固定为最新在前。
func TestStoreVersionsAreImmutableAndOrdered(t *testing.T) {
	at := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	for _, tc := range storeCases(t) {
		t.Run(tc.name, func(t *testing.T) {
			store := tc.open(t)
			ctx := context.Background()
			if err := store.CreateSkill(ctx, testSkill("skl_a", at)); err != nil {
				t.Fatalf("创建技能: %v", err)
			}

			newer := testVersion("skv_new", "skl_a", at.Add(time.Hour))
			newer.Files = []skill.File{{Path: "SKILL.md", Digest: "c" + repeat("0", 63), SizeBytes: 3}}
			source := skill.Source{
				Owner: "yanliudesign", Name: "mono-color-skill",
				Ref: "main", SubPath: "skills/mono", Commit: "abcdef1234567890abcdef1234567890abcdef12",
			}
			if err := store.AppendVersion(ctx, "skl_a", newer, source); err != nil {
				t.Fatalf("追加版本: %v", err)
			}

			got, err := store.GetSkill(ctx, "skl_a")
			if err != nil {
				t.Fatalf("读取技能: %v", err)
			}
			if got.Current.ID != "skv_new" {
				t.Errorf("追加之后当前版本 = %q", got.Current.ID)
			}
			if got.Source.Commit != source.Commit {
				t.Errorf("追加之后记录的提交 = %q", got.Source.Commit)
			}
			if got.Current.SkippedFiles != 3 {
				t.Errorf("追加之后的跳过条数 = %d，期望 3", got.Current.SkippedFiles)
			}

			versions, err := store.ListVersions(ctx, "skl_a")
			if err != nil {
				t.Fatalf("列出版本: %v", err)
			}
			if len(versions) != 2 || versions[0].ID != "skv_new" {
				t.Fatalf("版本列表 = %+v，期望最新在前", versions)
			}
			if versions[1].Files[0].SizeBytes != 42 {
				t.Errorf("旧版本的文件清单被改动了: %+v", versions[1].Files)
			}

			if err := store.SetCurrentVersion(ctx, "skl_a", "skv_skl_a"); err != nil {
				t.Fatalf("回滚: %v", err)
			}
			got, err = store.GetSkill(ctx, "skl_a")
			if err != nil {
				t.Fatalf("读取技能: %v", err)
			}
			if got.Current.ID != "skv_skl_a" || got.Current.Files[0].SizeBytes != 42 {
				t.Errorf("回滚之后的当前版本 = %+v", got.Current)
			}
			// 回滚不动来源里记录的提交：同步的语义是"追远端"，与现在对外用哪一份无关。
			if got.Source.Commit != source.Commit {
				t.Errorf("回滚改动了记录的提交: %q", got.Source.Commit)
			}
		})
	}
}

// 版本必须**属于这个技能**：不属于时结论与"版本不存在"相同。
func TestStoreSetCurrentVersionRejectsForeignVersion(t *testing.T) {
	at := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	for _, tc := range storeCases(t) {
		t.Run(tc.name, func(t *testing.T) {
			store := tc.open(t)
			ctx := context.Background()
			if err := store.CreateSkill(ctx, testSkill("skl_a", at)); err != nil {
				t.Fatalf("创建技能: %v", err)
			}
			if err := store.CreateSkill(ctx, testSkill("skl_b", at)); err != nil {
				t.Fatalf("创建技能: %v", err)
			}
			if err := store.SetCurrentVersion(ctx, "skl_a", "skv_skl_b"); !errors.Is(err, skill.ErrVersionNotFound) {
				t.Errorf("err = %v，期望 ErrVersionNotFound", err)
			}
			if err := store.SetCurrentVersion(ctx, "skl_missing", "skv_skl_a"); !errors.Is(err, skill.ErrSkillNotFound) {
				t.Errorf("技能不存在时 err = %v，期望 ErrSkillNotFound", err)
			}
		})
	}
}

// 图集能加、能换、能删、能重排，而**每一步都不碰内容层任何一项**（它是说明层）。
//
// 顺序是这一组用例的主角：它必须与对象键无关——删中间一张、重排之后，其余每一张的
// 键逐字不变，而那正是"一张一个键、顺序是单独一列"换来的性质。
func TestStoreImageWrites(t *testing.T) {
	at := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	for _, tc := range storeCases(t) {
		t.Run(tc.name, func(t *testing.T) {
			store := tc.open(t)
			ctx := context.Background()
			if err := store.CreateSkill(ctx, testSkill("skl_a", at)); err != nil {
				t.Fatalf("创建技能: %v", err)
			}

			// 追加：排在末尾，已有三张的键与次序都不动。
			added := testImage("skl_a", "d", 8192)
			if err := store.PutImage(ctx, "skl_a", added); err != nil {
				t.Fatalf("追加展示图: %v", err)
			}
			got := getSkill(t, store, "skl_a")
			if len(got.Images) != 4 || got.Images[3] != added {
				t.Fatalf("追加之后图集 = %+v", got.Images)
			}
			for i, want := range testImages("skl_a") {
				if got.Images[i] != want {
					t.Errorf("追加动了第 %d 张: %+v，期望 %+v", i, got.Images[i], want)
				}
			}

			// 换图（标识已在图集里）：只更新键与字节数，**位置不动**。
			replaced := testImage("skl_a", "b", 999)
			if err := store.PutImage(ctx, "skl_a", replaced); err != nil {
				t.Fatalf("换图: %v", err)
			}
			got = getSkill(t, store, "skl_a")
			if len(got.Images) != 4 || got.Images[1] != replaced {
				t.Fatalf("换图之后图集 = %+v", got.Images)
			}

			// 删中间一张：其余每一张的键与相对次序**逐字不变**。
			if err := store.DeleteImage(ctx, "skl_a", imageID("skl_a", "b")); err != nil {
				t.Fatalf("删图: %v", err)
			}
			got = getSkill(t, store, "skl_a")
			original := testImages("skl_a")
			want := []skill.Image{original[0], original[2], added}
			if len(got.Images) != len(want) {
				t.Fatalf("删图之后图集 = %+v", got.Images)
			}
			for i := range want {
				if got.Images[i] != want[i] {
					t.Errorf("删图之后第 %d 张 = %+v，期望 %+v", i, got.Images[i], want[i])
				}
			}

			// 重排：第一项即首图。
			order := []string{imageID("skl_a", "d"), imageID("skl_a", "c"), imageID("skl_a", "a")}
			if err := store.ReorderImages(ctx, "skl_a", order); err != nil {
				t.Fatalf("重排: %v", err)
			}
			got = getSkill(t, store, "skl_a")
			for i, wantID := range order {
				if got.Images[i].ID != wantID {
					t.Errorf("重排之后第 %d 张 = %q，期望 %q", i, got.Images[i].ID, wantID)
				}
			}

			// 内容层一项都不动。
			if got.Current.ID != "skv_skl_a" || len(got.Current.Files) != 2 {
				t.Errorf("图集写入碰了内容层: %+v", got.Current)
			}
		})
	}
}

// 重排与删除的拒绝语义：给出的标识不是当前图集的一个排列时整个拒绝、顺序原样；
// 删一张不在图集里的图是"没有这一张"，而不是静默成功。
func TestStoreImageRejections(t *testing.T) {
	at := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	for _, tc := range storeCases(t) {
		t.Run(tc.name, func(t *testing.T) {
			store := tc.open(t)
			ctx := context.Background()
			if err := store.CreateSkill(ctx, testSkill("skl_a", at)); err != nil {
				t.Fatalf("创建技能: %v", err)
			}

			for name, order := range map[string][]string{
				"少一张": {imageID("skl_a", "a"), imageID("skl_a", "b")},
				"多一张": {
					imageID("skl_a", "a"), imageID("skl_a", "b"),
					imageID("skl_a", "c"), imageID("skl_a", "d"),
				},
				"重复": {
					imageID("skl_a", "a"), imageID("skl_a", "a"), imageID("skl_a", "c"),
				},
				"带了别人的": {
					imageID("skl_a", "a"), imageID("skl_a", "b"), imageID("skl_b", "a"),
				},
				"空的": {},
			} {
				if err := store.ReorderImages(ctx, "skl_a", order); !errors.Is(err, skill.ErrImageOrderMismatch) {
					t.Errorf("%s：err = %v，期望 ErrImageOrderMismatch", name, err)
				}
				// 被拒之后顺序原样（不做部分重排）。
				got := getSkill(t, store, "skl_a")
				for i, want := range testImages("skl_a") {
					if got.Images[i] != want {
						t.Errorf("%s：被拒之后第 %d 张 = %+v，期望 %+v", name, i, got.Images[i], want)
					}
				}
			}

			if err := store.DeleteImage(ctx, "skl_a", imageID("skl_a", "missing")); !errors.Is(err, skill.ErrImageNotFound) {
				t.Errorf("删一张不在图集里的图 err = %v，期望 ErrImageNotFound", err)
			}
			if err := store.DeleteImage(ctx, "skl_missing", imageID("skl_a", "a")); !errors.Is(err, skill.ErrSkillNotFound) {
				t.Errorf("技能不存在 err = %v，期望 ErrSkillNotFound", err)
			}
			if err := store.PutImage(ctx, "skl_missing", skill.Image{ID: "ski_x"}); !errors.Is(err, skill.ErrSkillNotFound) {
				t.Errorf("技能不存在时写图 err = %v，期望 ErrSkillNotFound", err)
			}
			if err := store.ReorderImages(ctx, "skl_missing", []string{imageID("skl_a", "a")}); !errors.Is(err, skill.ErrSkillNotFound) {
				t.Errorf("技能不存在时重排 err = %v，期望 ErrSkillNotFound", err)
			}
		})
	}
}

// getSkill 读一条技能，失败即终止用例。
func getSkill(t *testing.T, store skill.Store, skillID string) skill.Skill {
	t.Helper()
	got, err := store.GetSkill(context.Background(), skillID)
	if err != nil {
		t.Fatalf("读取技能: %v", err)
	}
	return got
}

// 标签是**整体替换**：改一次之后旧标签一条都不剩。
func TestStoreUpdateMetadataReplacesTags(t *testing.T) {
	at := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	for _, tc := range storeCases(t) {
		t.Run(tc.name, func(t *testing.T) {
			store := tc.open(t)
			ctx := context.Background()
			if err := store.CreateSkill(ctx, testSkill("skl_a", at)); err != nil {
				t.Fatalf("创建技能: %v", err)
			}
			if err := store.UpdateMetadata(ctx, "skl_a", "新标题", "新简介", []string{"前端", "审美"}); err != nil {
				t.Fatalf("改说明层: %v", err)
			}
			got, err := store.GetSkill(ctx, "skl_a")
			if err != nil {
				t.Fatalf("读取技能: %v", err)
			}
			if got.Title != "新标题" || got.Summary != "新简介" {
				t.Errorf("说明层没有改到: %+v", got)
			}
			if len(got.Tags) != 2 || got.Tags[0] != "前端" || got.Tags[1] != "审美" {
				t.Errorf("标签 = %v，期望整体替换", got.Tags)
			}
			// 内容层一项都不动。
			if got.Current.ID != "skv_skl_a" || len(got.Current.Files) != 2 {
				t.Errorf("说明层的改动碰了内容层: %+v", got.Current)
			}

			// 清空。
			if err := store.UpdateMetadata(ctx, "skl_a", "", "", nil); err != nil {
				t.Fatalf("清空说明层: %v", err)
			}
			got, err = store.GetSkill(ctx, "skl_a")
			if err != nil {
				t.Fatalf("读取技能: %v", err)
			}
			if got.Title != "" || got.Tags != nil {
				t.Errorf("清空之后仍有说明层: %+v", got)
			}
		})
	}
}

// 收藏的两个方向都幂等，且只在**发起的主体**名下。
func TestStoreFavoriteIsIdempotentAndPerSubject(t *testing.T) {
	at := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	for _, tc := range storeCases(t) {
		t.Run(tc.name, func(t *testing.T) {
			store := tc.open(t)
			ctx := context.Background()
			if err := store.CreateSkill(ctx, testSkill("skl_a", at)); err != nil {
				t.Fatalf("创建技能: %v", err)
			}
			for i := 0; i < 2; i++ {
				if err := store.SetFavorite(ctx, "skl_a", "subject-a", true); err != nil {
					t.Fatalf("收藏: %v", err)
				}
			}
			mine, err := store.FavoriteIDs(ctx, "subject-a")
			if err != nil {
				t.Fatalf("读取收藏: %v", err)
			}
			theirs, err := store.FavoriteIDs(ctx, "subject-b")
			if err != nil {
				t.Fatalf("读取收藏: %v", err)
			}
			if !mine["skl_a"] || len(mine) != 1 {
				t.Errorf("我的收藏 = %v", mine)
			}
			if len(theirs) != 0 {
				t.Errorf("别人的收藏 = %v，期望空", theirs)
			}
			for i := 0; i < 2; i++ {
				if err := store.SetFavorite(ctx, "skl_a", "subject-a", false); err != nil {
					t.Fatalf("取消收藏: %v", err)
				}
			}
			mine, err = store.FavoriteIDs(ctx, "subject-a")
			if err != nil {
				t.Fatalf("读取收藏: %v", err)
			}
			if len(mine) != 0 {
				t.Errorf("取消之后仍有收藏 = %v", mine)
			}
		})
	}
}

// 使用量按**人日**归并：同一主体同一天写几次都只算一天，且最近使用时间取更晚的
// 那一次（晚到的写不该让它倒退）。
func TestStoreUsageIsPerSubjectPerDay(t *testing.T) {
	at := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	for _, tc := range storeCases(t) {
		t.Run(tc.name, func(t *testing.T) {
			store := tc.open(t)
			ctx := context.Background()
			if err := store.CreateSkill(ctx, testSkill("skl_a", at)); err != nil {
				t.Fatalf("创建技能: %v", err)
			}
			// 同一天的几次取用，**故意乱序**：晚到的那条不该把最近使用时间拉回去。
			for _, offset := range []time.Duration{2 * time.Minute, 0, time.Minute} {
				if err := store.RecordUse(ctx, "skl_a", "subject-a", at.Add(offset)); err != nil {
					t.Fatalf("记一次取用: %v", err)
				}
			}
			// 同一个主体的前一天，以及另一个主体的大前天。
			if err := store.RecordUse(ctx, "skl_a", "subject-a", at.Add(-24*time.Hour)); err != nil {
				t.Fatalf("记一次取用: %v", err)
			}
			if err := store.RecordUse(ctx, "skl_a", "subject-b", at.Add(-48*time.Hour)); err != nil {
				t.Fatalf("记一次取用: %v", err)
			}

			usage, err := store.Usage(ctx, []string{"skl_a"}, at.Add(-72*time.Hour))
			if err != nil {
				t.Fatalf("读取使用量: %v", err)
			}
			got := usage["skl_a"]
			if got.UseDays != 3 {
				t.Errorf("使用日次 = %d，期望 3", got.UseDays)
			}
			if got.Users != 2 {
				t.Errorf("使用人数 = %d，期望 2", got.Users)
			}
			if !got.LastUsedAt.Equal(at.Add(2 * time.Minute)) {
				t.Errorf("最近使用时间 = %v，期望 %v", got.LastUsedAt, at.Add(2*time.Minute))
			}

			// 窗口之外的不算。
			usage, err = store.Usage(ctx, []string{"skl_a"}, at.Add(-24*time.Hour))
			if err != nil {
				t.Fatalf("读取使用量: %v", err)
			}
			if usage["skl_a"].UseDays != 2 {
				t.Errorf("窗口内的使用日次 = %d，期望 2", usage["skl_a"].UseDays)
			}

			// 回收：早于某一天的行整条消失。
			if err := store.DeleteUsageBefore(ctx, at); err != nil {
				t.Fatalf("回收: %v", err)
			}
			usage, err = store.Usage(ctx, []string{"skl_a"}, at.Add(-72*time.Hour))
			if err != nil {
				t.Fatalf("读取使用量: %v", err)
			}
			got = usage["skl_a"]
			if got.UseDays != 1 || got.Users != 1 {
				t.Errorf("回收之后的使用量 = %+v", got)
			}
			if !got.LastUsedAt.Equal(at.Add(2 * time.Minute)) {
				t.Errorf("回收之后最近使用时间 = %v", got.LastUsedAt)
			}
		})
	}
}

// 删除带走技能、版本、标签、收藏与使用记录，但**不碰桶上的字节**（那是服务层的
// 事，这里只断言"行都没了"）。
func TestStoreDeleteRemovesEverything(t *testing.T) {
	at := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	for _, tc := range storeCases(t) {
		t.Run(tc.name, func(t *testing.T) {
			store := tc.open(t)
			ctx := context.Background()
			if err := store.CreateSkill(ctx, testSkill("skl_a", at)); err != nil {
				t.Fatalf("创建技能: %v", err)
			}
			if err := store.RecordUse(ctx, "skl_a", "subject-a", at); err != nil {
				t.Fatalf("记一次取用: %v", err)
			}
			if err := store.SetFavorite(ctx, "skl_a", "subject-a", true); err != nil {
				t.Fatalf("收藏: %v", err)
			}
			if err := store.DeleteSkill(ctx, "skl_a"); err != nil {
				t.Fatalf("删除: %v", err)
			}

			items, err := store.ListSkills(ctx)
			if err != nil {
				t.Fatalf("列出技能: %v", err)
			}
			if len(items) != 0 {
				t.Errorf("删除之后仍有 %d 条技能", len(items))
			}
			if _, err := store.ListVersions(ctx, "skl_a"); !errors.Is(err, skill.ErrSkillNotFound) {
				t.Errorf("删除之后仍能列出版本: %v", err)
			}
			usage, err := store.Usage(ctx, []string{"skl_a"}, at)
			if err != nil {
				t.Fatalf("读取使用量: %v", err)
			}
			if len(usage) != 0 {
				t.Errorf("删除之后仍有使用记录: %v", usage)
			}
			// 收藏行也要一起走：不然它会指向一个不存在的技能，而"我收藏了什么"
			// 从此带上一个查不到的空壳。
			favorites, err := store.FavoriteIDs(ctx, "subject-a")
			if err != nil {
				t.Fatalf("读取收藏: %v", err)
			}
			if len(favorites) != 0 {
				t.Errorf("删除之后仍有收藏: %v", favorites)
			}
		})
	}
}

// 写入确实落了盘：关系库实现换一个实例打开同一个文件，读回来还是同一份。
func TestStorePersistsAcrossInstances(t *testing.T) {
	at := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "skill.db")
	first := New(newSkillTestDB(t, path))
	ctx := context.Background()
	if err := first.CreateSkill(ctx, testSkill("skl_a", at)); err != nil {
		t.Fatalf("创建技能: %v", err)
	}
	if err := first.RecordUse(ctx, "skl_a", "subject-a", at); err != nil {
		t.Fatalf("记一次取用: %v", err)
	}

	second := New(newSkillTestDB(t, path))
	got, err := second.GetSkill(ctx, "skl_a")
	if err != nil {
		t.Fatalf("重新打开之后读取技能: %v", err)
	}
	if got.Current.Description != "单色印刷风出图。" || len(got.Tags) != 2 {
		t.Errorf("重新打开之后读到的技能不完整: %+v", got)
	}
	usage, err := second.Usage(ctx, []string{"skl_a"}, at)
	if err != nil {
		t.Fatalf("重新打开之后读取使用量: %v", err)
	}
	if usage["skl_a"].UseDays != 1 {
		t.Errorf("重新打开之后的使用量 = %+v", usage["skl_a"])
	}
}

// newSkillTestDB 打开一个跑过迁移的 sqlite 库。
func newSkillTestDB(t *testing.T, optPath ...string) *gorm.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "skill.db")
	if len(optPath) > 0 {
		path = optPath[0]
	}
	db, err := database.Open(config.DatabaseConfig{
		Driver: string(database.DialectSQLite),
		DSN:    path,
	}, zap.NewNop())
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	if err := migrate.Run(context.Background(), db, zap.NewNop()); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	t.Cleanup(func() {
		sqlDB, err := db.DB()
		if err != nil {
			t.Fatalf("获取连接池失败: %v", err)
		}
		// 必须真的关掉：sqlite 的写锁是文件级的，上一个连接不关，下一个连接打开
		// 同一个文件时会在写入上卡住——而现象是测试"偶尔超时"。
		if err := sqlDB.Close(); err != nil {
			t.Fatalf("关闭连接池失败: %v", err)
		}
	})
	return db
}
