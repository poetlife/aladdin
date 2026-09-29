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
	"github.com/poetlife/aladdin/internal/galaxy"
)

// 两个 galaxy 存储实现共用同一套用例。
//
// 理由与档案、会话、身份别名的契约测试相同：**同一份契约只能有一套用例**。给
// gorm 实现单独写一套，两套就会各自漂移，而漂移的表现形式是"换后端之后，删一个
// 版本把序号重排了"。
type storeCase struct {
	name string
	open func(t *testing.T) galaxy.MutableStore
	// persistent 为真表示这个实现会把数据落到文件上，因此可以用"换一个实例再
	// 打开同一个库"来验证写入确实落了盘。内存实现没有这个性质。
	persistent bool
}

func storeCases(t *testing.T) []storeCase {
	t.Helper()
	return []storeCase{
		{
			name: "内存实现",
			open: func(*testing.T) galaxy.MutableStore { return galaxy.NewMemoryStore() },
		},
		{
			name:       "关系库实现",
			open:       func(t *testing.T) galaxy.MutableStore { return New(newGalaxyTestDB(t)) },
			persistent: true,
		},
	}
}

// testManifest 造一份最小的清单：一条文本条目。
//
// 摘要用一个合法形状的取值：存储层不判形状（那是上层的入口），但用真形状能让
// "序列化—反序列化往返之后还是同一份"这件事看起来更接近实际。
func testManifest(entryPath, digest string) galaxy.Manifest {
	return galaxy.Manifest{{Path: entryPath, Kind: galaxy.EntryKindText, Digest: digest}}
}

func TestStoreContract(t *testing.T) {
	for _, tc := range storeCases(t) {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
			store := tc.open(t)

			t.Run("没找到与出错是两类结论", func(t *testing.T) {
				if _, err := store.GetProject(ctx, "prj_没有"); !errors.Is(err, galaxy.ErrProjectNotFound) {
					t.Errorf("err = %v，期望 ErrProjectNotFound", err)
				}
				if _, err := store.GetDraft(ctx, "prj_没有"); !errors.Is(err, galaxy.ErrDraftNotFound) {
					t.Errorf("err = %v，期望 ErrDraftNotFound", err)
				}
				if _, err := store.GetVersion(ctx, "prj_没有", "ver_没有"); !errors.Is(err, galaxy.ErrVersionNotFound) {
					t.Errorf("err = %v，期望 ErrVersionNotFound", err)
				}
				if _, err := store.GetAsset(ctx, "prj_没有", "ast_没有"); !errors.Is(err, galaxy.ErrAssetNotFound) {
					t.Errorf("err = %v，期望 ErrAssetNotFound", err)
				}
				if _, err := store.GetPublication(ctx, "pub_没有"); !errors.Is(err, galaxy.ErrPublicationNotFound) {
					t.Errorf("err = %v，期望 ErrPublicationNotFound", err)
				}
			})

			t.Run("工程名称无唯一约束", func(t *testing.T) {
				for _, id := range []string{"prj_a", "prj_b"} {
					if err := store.CreateProject(ctx, galaxy.Project{
						ID: id, OwnerSubjectID: "usr_1", Name: "同名", Form: galaxy.SiteFormStatic,
						CreatedAt: now, UpdatedAt: now,
					}); err != nil {
						t.Fatalf("写入工程失败: %v", err)
					}
				}
				projects, err := store.ListProjectsByOwner(ctx, "usr_1")
				if err != nil {
					t.Fatalf("列出工程失败: %v", err)
				}
				if len(projects) != 2 {
					t.Errorf("工程数 = %d，期望 2（同名不该被唯一约束拦住）", len(projects))
				}
				// 形态随工程行往返。
				project, err := store.GetProject(ctx, "prj_a")
				if err != nil {
					t.Fatalf("读取工程失败: %v", err)
				}
				if project.Form != galaxy.SiteFormStatic {
					t.Errorf("形态 = %q，期望 static", project.Form)
				}
			})

			t.Run("列表按拥有者过滤", func(t *testing.T) {
				projects, err := store.ListProjectsByOwner(ctx, "usr_别人")
				if err != nil {
					t.Fatalf("列出工程失败: %v", err)
				}
				if len(projects) != 0 {
					t.Errorf("别人的工程数 = %d，期望空集", len(projects))
				}
			})

			t.Run("草稿行惰性创建且整组替换", func(t *testing.T) {
				if _, err := store.GetDraft(ctx, "prj_draft"); !errors.Is(err, galaxy.ErrDraftNotFound) {
					t.Fatalf("err = %v，期望 ErrDraftNotFound", err)
				}
				first := testManifest("index.html", "aa")
				if err := store.PutDraft(ctx, "prj_draft", first, now); err != nil {
					t.Fatalf("写入草稿失败: %v", err)
				}
				draft, err := store.GetDraft(ctx, "prj_draft")
				if err != nil {
					t.Fatalf("读取草稿失败: %v", err)
				}
				if len(draft.Manifest) != 1 || draft.Manifest[0] != first[0] {
					t.Errorf("草稿 = %+v，期望往返之后逐字相同", draft.Manifest)
				}
				// **整组替换**：第二次写入换掉第一次，不是合并。
				second := galaxy.Manifest{
					{Path: "index.html", Kind: galaxy.EntryKindText, Digest: "bb"},
					{Path: "a.png", Kind: galaxy.EntryKindAsset, AssetID: "ast_1"},
				}
				if err := store.PutDraft(ctx, "prj_draft", second, now); err != nil {
					t.Fatalf("覆盖草稿失败: %v", err)
				}
				draft, err = store.GetDraft(ctx, "prj_draft")
				if err != nil {
					t.Fatalf("读取草稿失败: %v", err)
				}
				if len(draft.Manifest) != 2 {
					t.Errorf("草稿 = %+v，期望整组换成两份", draft.Manifest)
				}
				// 资产条目也要能原样往返。
				assetEntry, ok := draft.Manifest.Find("a.png")
				if !ok || assetEntry.Kind != galaxy.EntryKindAsset || assetEntry.AssetID != "ast_1" {
					t.Errorf("资产条目 = %+v，期望往返之后类别与标识都在", assetEntry)
				}
			})

			t.Run("序号从 1 开始且可空洞", func(t *testing.T) {
				first, err := store.CreateVersion(ctx, galaxy.Version{
					ID: "ver_1", ProjectID: "prj_v", Manifest: testManifest("index.html", "aa"),
					RenderRulesVersion: 1, SavedAt: now,
				})
				if err != nil {
					t.Fatalf("写入版本失败: %v", err)
				}
				if first.Seq != 1 {
					t.Errorf("首个版本的序号 = %d，期望 1", first.Seq)
				}
				if _, err := store.CreateVersion(ctx, galaxy.Version{
					ID: "ver_2", ProjectID: "prj_v", Manifest: testManifest("index.html", "bb"), SavedAt: now,
				}); err != nil {
					t.Fatalf("写入版本失败: %v", err)
				}
				third, err := store.CreateVersion(ctx, galaxy.Version{
					ID: "ver_3", ProjectID: "prj_v", Manifest: testManifest("index.html", "cc"), SavedAt: now,
				})
				if err != nil {
					t.Fatalf("写入版本失败: %v", err)
				}
				// 删中间一个：序号**不重排**，空洞是允许的。
				if err := store.DeleteVersion(ctx, "prj_v", "ver_2"); err != nil {
					t.Fatalf("删除版本失败: %v", err)
				}
				thirdAfter, err := store.GetVersion(ctx, "prj_v", third.ID)
				if err != nil {
					t.Fatalf("读取版本失败: %v", err)
				}
				if thirdAfter.Seq != third.Seq {
					t.Errorf("序号 = %d，期望保持 %d", thirdAfter.Seq, third.Seq)
				}
				// 下一个版本的序号接着最大值走，不填补空洞。
				fourth, err := store.CreateVersion(ctx, galaxy.Version{
					ID: "ver_4", ProjectID: "prj_v", Manifest: testManifest("index.html", "dd"), SavedAt: now,
				})
				if err != nil {
					t.Fatalf("写入版本失败: %v", err)
				}
				if fourth.Seq != 4 {
					t.Errorf("第四个版本的序号 = %d，期望 4", fourth.Seq)
				}
				// 列表**带清单**（它只有路径与摘要），渲染规则版本随行。
				versions, err := store.ListVersions(ctx, "prj_v")
				if err != nil {
					t.Fatalf("列出版本失败: %v", err)
				}
				if len(versions) != 3 {
					t.Fatalf("版本数 = %d，期望 3", len(versions))
				}
				for _, version := range versions {
					if len(version.Manifest) == 0 {
						t.Errorf("版本 %s 的清单为空", version.ID)
					}
				}
			})

			t.Run("版本清单写入后不可改", func(t *testing.T) {
				// 契约里没有"更新版本"这个方法；这里验证的是"改别的东西不会改到它"。
				if err := store.PutProjectMeta(ctx, "prj_v", "改名", "", now); !errors.Is(err, galaxy.ErrProjectNotFound) {
					t.Fatalf("对不存在的工程改元数据 err = %v，期望 ErrProjectNotFound", err)
				}
				version, err := store.GetVersion(ctx, "prj_v", "ver_1")
				if err != nil {
					t.Fatalf("读取版本失败: %v", err)
				}
				if len(version.Manifest) != 1 || version.Manifest[0].Digest != "aa" {
					t.Errorf("版本清单 = %+v，期望逐字不变", version.Manifest)
				}
				if version.RenderRulesVersion != 1 {
					t.Errorf("渲染规则版本 = %d，期望 1", version.RenderRulesVersion)
				}
			})

			t.Run("资产不可变且属于唯一工程", func(t *testing.T) {
				asset := galaxy.Asset{
					ID: "ast_1", ProjectID: "prj_a", Digest: "aa", MediaKind: galaxy.MediaKindImage,
					MediaType: "image/png", SizeBytes: 3, Filename: "a.png", UploadedAt: now,
				}
				if err := store.CreateAsset(ctx, asset); err != nil {
					t.Fatalf("写入资产失败: %v", err)
				}
				first, err := store.GetAsset(ctx, "prj_a", "ast_1")
				if err != nil {
					t.Fatalf("读取资产失败: %v", err)
				}
				second, err := store.GetAsset(ctx, "prj_a", "ast_1")
				if err != nil {
					t.Fatalf("读取资产失败: %v", err)
				}
				if first.Digest != second.Digest || first.MediaType != second.MediaType || first.MediaKind != second.MediaKind {
					t.Error("两次读回的摘要、类型或类别不同")
				}
				// 用另一个工程去读：不存在。
				if _, err := store.GetAsset(ctx, "prj_b", "ast_1"); !errors.Is(err, galaxy.ErrAssetNotFound) {
					t.Errorf("err = %v，期望 ErrAssetNotFound", err)
				}
				// 删掉之后读不到。
				if err := store.DeleteAsset(ctx, "prj_a", "ast_1"); err != nil {
					t.Fatalf("删除资产失败: %v", err)
				}
				if _, err := store.GetAsset(ctx, "prj_a", "ast_1"); !errors.Is(err, galaxy.ErrAssetNotFound) {
					t.Errorf("err = %v，期望 ErrAssetNotFound", err)
				}
			})

			t.Run("产物清单按键读回且幂等", func(t *testing.T) {
				publication := galaxy.Publication{
					ID: "pub_1", ProjectID: "prj_a", VersionID: "ver_1",
					Manifest:             testManifest("index.html", "aa"),
					PublishedBySubjectID: "usr_1", PublishedAt: now,
				}
				if err := store.PutPublication(ctx, publication); err != nil {
					t.Fatalf("写入发布记录失败: %v", err)
				}
				// 同一条记录重复写入不产生第二条。
				publication.Manifest = testManifest("index.html", "bb")
				if err := store.PutPublication(ctx, publication); err != nil {
					t.Fatalf("重复写入发布记录失败: %v", err)
				}
				got, err := store.GetPublication(ctx, "pub_1")
				if err != nil {
					t.Fatalf("读取发布记录失败: %v", err)
				}
				if len(got.Manifest) != 1 || got.Manifest[0].Digest != "bb" {
					t.Errorf("产物清单 = %+v，期望被覆盖成最新一次", got.Manifest)
				}
				// 标识是主键，因此"同一次发布重复落库"不可能产生第二条记录：
				// 上面的读回既是内容断言，也是这条性质的判据（第二条会因主键
				// 冲突写不进去，而不是悄悄多出一行）。
			})

			t.Run("撤回不删发布记录", func(t *testing.T) {
				if err := store.SetCurrentPublication(ctx, "prj_a", "pub_1", now); err != nil {
					t.Fatalf("切换发布指针失败: %v", err)
				}
				if err := store.SetCurrentPublication(ctx, "prj_a", "", now); err != nil {
					t.Fatalf("清空发布指针失败: %v", err)
				}
				project, err := store.GetProject(ctx, "prj_a")
				if err != nil {
					t.Fatalf("读取工程失败: %v", err)
				}
				if project.CurrentPublicationID != "" {
					t.Errorf("发布指针 = %q，期望为空", project.CurrentPublicationID)
				}
				if _, err := store.GetPublication(ctx, "pub_1"); err != nil {
					t.Errorf("撤回之后发布记录不该消失: %v", err)
				}
			})

			t.Run("删除工程连带删掉全部记录", func(t *testing.T) {
				if err := store.DeleteProject(ctx, "prj_a"); err != nil {
					t.Fatalf("删除工程失败: %v", err)
				}
				if _, err := store.GetProject(ctx, "prj_a"); !errors.Is(err, galaxy.ErrProjectNotFound) {
					t.Errorf("工程仍在: %v", err)
				}
				if versions, err := store.ListVersions(ctx, "prj_a"); err != nil || len(versions) != 0 {
					t.Errorf("版本仍在: %v / %d 条", err, len(versions))
				}
				if assets, err := store.ListAssets(ctx, "prj_a"); err != nil || len(assets) != 0 {
					t.Errorf("资产仍在: %v / %d 条", err, len(assets))
				}
				if _, err := store.GetPublication(ctx, "pub_1"); !errors.Is(err, galaxy.ErrPublicationNotFound) {
					t.Errorf("发布记录仍在: %v", err)
				}
				if _, err := store.GetDraft(ctx, "prj_a"); !errors.Is(err, galaxy.ErrDraftNotFound) {
					t.Errorf("草稿仍在: %v", err)
				}
			})
		})
	}
}

// 持久化**确实生效**：用同一个库文件构造两次存储，第二次能读到第一次写入的数据。
//
// 这条只有落盘的实现有意义——内存实现"再打开一次"拿到的本来就是同一份 map。
func TestGormStorePersistsAcrossOpenings(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "galaxy.db")
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

	first := New(newGalaxyTestDB(t, path))
	if err := first.CreateProject(ctx, galaxy.Project{
		ID: "prj_persist", OwnerSubjectID: "usr_1", Name: "落盘", Form: galaxy.SiteFormDocs,
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("写入工程失败: %v", err)
	}
	if _, err := first.CreateVersion(ctx, galaxy.Version{
		ID: "ver_persist", ProjectID: "prj_persist", Manifest: testManifest("index.md", "aa"), SavedAt: now,
	}); err != nil {
		t.Fatalf("写入版本失败: %v", err)
	}

	second := New(newGalaxyTestDB(t, path))
	project, err := second.GetProject(ctx, "prj_persist")
	if err != nil {
		t.Fatalf("第二次打开之后读不到工程: %v", err)
	}
	if project.Name != "落盘" || project.Form != galaxy.SiteFormDocs {
		t.Errorf("工程 = %+v", project)
	}
	version, err := second.GetVersion(ctx, "prj_persist", "ver_persist")
	if err != nil {
		t.Fatalf("第二次打开之后读不到版本: %v", err)
	}
	if len(version.Manifest) != 1 || version.Manifest[0].Path != "index.md" || version.Seq != 1 {
		t.Errorf("版本 = %+v", version)
	}
}

// newGalaxyTestDB 打开一个已迁移的测试库。
//
// 不传路径时每个用例一个临时目录：迁移建表用的是真实 DDL，用例之间共用文件会
// 让"表已存在"这类差异混进断言。
func newGalaxyTestDB(t *testing.T, optPath ...string) *gorm.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "galaxy.db")
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
		// 必须真的关掉：sqlite 的写锁是文件级的，上一个连接不关，下一个连接
		// 打开同一个文件时会在写入上卡住——而现象是测试"偶尔超时"。
		if err := sqlDB.Close(); err != nil {
			t.Fatalf("关闭连接池失败: %v", err)
		}
	})
	return db
}
