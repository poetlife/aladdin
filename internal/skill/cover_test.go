package skill

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/poetlife/aladdin/internal/imagetype"
	"github.com/poetlife/aladdin/internal/objectstore"
)

// 封面是**说明层的一项**，不是包的内容。这一组用例守的正是这条线：
//
//   - 它进对象存储，但不进文件清单、不改 skipped_files；
//   - 两个来源（纳管时从仓库取 / 界面上传）落到同一个键，覆盖写；
//   - 从仓库取那条**核对字节**，因为服务端看得见它。

// 一段最小的 PNG：八个字节的签名加一点内容。魔数核对只看文件头。
var testPNG = string([]byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00, 0x00, 0x0d, 'I', 'H', 'D', 'R'})

// coverTree 造一棵带封面的树。
func coverTree() map[string]string {
	return map[string]string{
		ManifestPath:  manifest("mono-color", "单色印刷风出图。"),
		"palette.md":  "# 色板\n",
		"cover.png":   testPNG,
		"notes.html":  "<!doctype html><p>不是图</p>",
		"huge.png":    testPNG + strings.Repeat("\x00", imagetype.MaxBytes),
		"cover.txt":   "文本",
		"cover2.jpeg": testPNG, // 扩展名说是 JPEG，字节是 PNG
	}
}

func (f *fixture) importWithCover(t *testing.T, coverPath string, extraTags ...string) (Skill, error) {
	t.Helper()
	f.remote.setTree(testSHA, coverTree())
	return f.service.Import(context.Background(), ImportParams{
		RepositoryURL: "https://github.com/" + testOwner + "/" + testRepo,
		CoverPath:     coverPath,
		Tags:          extraTags,
	})
}

// 封面进对象存储，而**不在文件清单里**、不改跳过条数。
func TestImportWithCoverKeepsItOutOfThePackage(t *testing.T) {
	f := newFixture(t)
	item, err := f.importWithCover(t, "cover.png")
	if err != nil {
		t.Fatalf("纳管失败: %v", err)
	}

	if item.CoverKey != CoverKey(item.ID) {
		t.Errorf("封面键 = %q，期望 %q", item.CoverKey, CoverKey(item.ID))
	}
	data, err := f.objects.Read(context.Background(), item.CoverKey)
	if err != nil {
		t.Fatalf("封面没有写进对象存储: %v", err)
	}
	if string(data) != testPNG {
		t.Errorf("封面字节 = %q", data)
	}

	// 文件清单里没有它——封面不是包的内容。
	for _, file := range item.Current.Files {
		if file.Path == "cover.png" {
			t.Error("封面进了文件清单")
		}
	}
	// 它仍然是"被跳过的那类二进制"，跳过条数照算。
	if item.Current.SkippedFiles == 0 {
		t.Error("跳过条数为零：封面不该把它从跳过里挪出来")
	}
	if url := f.service.CoverURL(context.Background(), item); url == "" {
		t.Error("有封面却拿不到地址")
	}
}

// 从仓库取那条的**四道校验**。任何一道不过即整次纳管失败——不留下"技能进来了、
// 只是没有封面"这种要人去猜的状态。
func TestImportCoverRejections(t *testing.T) {
	cases := map[string]string{
		"不在包里":      "nope.png",
		"路径形状不合法":   "../cover.png",
		"扩展名不是图":    "cover.txt",
		"看着是图、字节不是": "notes.html",
		"超上限":       "huge.png",
	}
	for name, coverPath := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			if _, err := f.importWithCover(t, coverPath); !errors.Is(err, ErrCoverNotAllowed) {
				t.Fatalf("err = %v，期望 ErrCoverNotAllowed", err)
			}
			// **不留痕迹**：被拒的纳管之后，库里没有新行。
			skills, err := f.store.ListSkills(context.Background())
			if err != nil {
				t.Fatalf("列出技能: %v", err)
			}
			if len(skills) != 0 {
				t.Errorf("被拒的纳管留下了 %d 条技能", len(skills))
			}
		})
	}
}

// **扩展名决定"是不是一张图"，字节决定"是哪一种图"。** 两者不符时以字节为准：一份
// 叫着 `.jpeg` 而实际是 PNG 的文件到处都有，而写进对象、此后决定浏览器怎么渲染它的
// 那个取值必须是**字节说的那一个**。
func TestImportCoverTrustsBytesOverExtension(t *testing.T) {
	f := newFixture(t)
	item, err := f.importWithCover(t, "cover2.jpeg")
	if err != nil {
		t.Fatalf("名字与内容不符的图被拒了: %v", err)
	}
	if item.CoverKey == "" {
		t.Fatal("没有拿到封面")
	}
}

// **封面的字节上限是它自己那一档，不是包契约的单文件上限。**
//
// 封面上限 2 MiB 比包里的单文件上限 256 KiB 宽，而取字节那一步是共用的。共用时把
// 包里的上限写死进去，表现是"这张封面明明在上限内，却被按包里的规矩拒了"。
func TestImportCoverUsesItsOwnSizeLimit(t *testing.T) {
	f := newFixture(t)
	// 一张 512 KiB 的图：超过包里的单文件上限，但在封面上限之内。
	big := testPNG + strings.Repeat("\x01", 512<<10)
	f.remote.setTree(testSHA, map[string]string{
		ManifestPath: manifest("mono-color", "出图。"),
		"cover.png":  big,
	})
	item, err := f.service.Import(context.Background(), ImportParams{
		RepositoryURL: "https://github.com/" + testOwner + "/" + testRepo,
		CoverPath:     "cover.png",
	})
	if err != nil {
		t.Fatalf("上限之内的封面被拒了: %v", err)
	}
	if data, err := f.objects.Read(context.Background(), item.CoverKey); err != nil {
		t.Fatalf("封面没有写进对象存储: %v", err)
	} else if len(data) != len(big) {
		t.Errorf("封面字节数 = %d，期望 %d", len(data), len(big))
	}
}

// 不指定封面时就**没有**封面：空串是"没有"的唯一判据，界面据此渲染占位。
func TestImportWithoutCoverHasNone(t *testing.T) {
	f := newFixture(t)
	item := f.importOne(t)
	if item.CoverKey != "" {
		t.Errorf("封面键 = %q，期望空", item.CoverKey)
	}
	if url := f.service.CoverURL(context.Background(), item); url != "" {
		t.Errorf("没有封面却给出了地址 %q", url)
	}
}

// 上传那条路：签发的规则与白名单同源，声明的类型与大小在签发时就挡。
func TestBeginCoverUploadValidates(t *testing.T) {
	f := newFixture(t)
	item := f.importOne(t)
	ctx := context.Background()

	if _, err := f.service.BeginCoverUpload(ctx, item.ID, "image/png", 1024); err != nil {
		t.Fatalf("合法声明被拒: %v", err)
	}
	if _, err := f.service.BeginCoverUpload(ctx, item.ID, "image/svg+xml", 1024); !errors.Is(err, imagetype.ErrTypeNotAllowed) {
		t.Errorf("SVG err = %v，期望 ErrTypeNotAllowed", err)
	}
	if _, err := f.service.BeginCoverUpload(ctx, item.ID, "image/png", imagetype.MaxBytes+1); !errors.Is(err, ErrCoverTooLarge) {
		t.Errorf("超上限 err = %v，期望 ErrCoverTooLarge", err)
	}
	if _, err := f.service.BeginCoverUpload(ctx, "skl_missing", "image/png", 1); !errors.Is(err, ErrSkillNotFound) {
		t.Errorf("技能不存在 err = %v，期望 ErrSkillNotFound", err)
	}
}

// 提交要核对对象确实到了；到了就把技能指向它。
func TestCommitCoverUpload(t *testing.T) {
	f := newFixture(t)
	item := f.importOne(t)
	ctx := context.Background()

	// 没传字节就提交：一次可以重来的上传，不是故障。
	if _, err := f.service.CommitCoverUpload(ctx, item.ID); err == nil {
		t.Fatal("对象不存在却提交成功了")
	}

	f.objects.SimulateUpload(CoverKey(item.ID), []byte(testPNG))
	updated, err := f.service.CommitCoverUpload(ctx, item.ID)
	if err != nil {
		t.Fatalf("提交失败: %v", err)
	}
	if updated.CoverKey != CoverKey(item.ID) {
		t.Errorf("封面键 = %q", updated.CoverKey)
	}
}

// 换封面与移除封面**都不碰内容层**：当前版本、文件清单与跳过条数逐字不变。
func TestCoverChangesLeaveContentLayerAlone(t *testing.T) {
	f := newFixture(t)
	item := f.importOne(t)
	ctx := context.Background()

	f.objects.SimulateUpload(CoverKey(item.ID), []byte(testPNG))
	set, err := f.service.CommitCoverUpload(ctx, item.ID)
	if err != nil {
		t.Fatalf("提交失败: %v", err)
	}
	assertContentUnchanged(t, item, set)

	cleared, err := f.service.ClearCover(ctx, item.ID)
	if err != nil {
		t.Fatalf("移除失败: %v", err)
	}
	if cleared.CoverKey != "" {
		t.Errorf("移除之后封面键 = %q", cleared.CoverKey)
	}
	assertContentUnchanged(t, item, cleared)

	// 幂等：没有封面时再移除一次也成功。
	if _, err := f.service.ClearCover(ctx, item.ID); err != nil {
		t.Fatalf("重复移除失败: %v", err)
	}
}

// 删技能时封面对象**一起删**——这个键由它独占；内容对象一个都不删（按摘要共享）。
func TestDeleteSkillRemovesCoverButKeepsContent(t *testing.T) {
	f := newFixture(t)
	item, err := f.importWithCover(t, "cover.png")
	if err != nil {
		t.Fatalf("纳管失败: %v", err)
	}
	ctx := context.Background()
	contentDigest := item.Current.Files[0].Digest

	if err := f.service.Delete(ctx, item.ID); err != nil {
		t.Fatalf("删除失败: %v", err)
	}
	if _, err := f.objects.Read(ctx, item.CoverKey); !errors.Is(err, objectstore.ErrObjectNotFound) {
		t.Errorf("封面对象没有被删掉: err = %v", err)
	}
	if _, err := f.objects.Read(ctx, ContentObjectKey(contentDigest)); err != nil {
		t.Errorf("内容对象被误删了: %v", err)
	}
}

// 没有配置对象存储时，封面整体不可用（与目录同一条降级取向）。
func TestCoverWithoutObjectStoreIsUnavailable(t *testing.T) {
	store := NewMemoryStore()
	service := NewService(Deps{Store: store, Remote: newFakeRemote()})
	if _, err := service.BeginCoverUpload(context.Background(), "skl_x", "image/png", 1); !errors.Is(err, ErrObjectStoreUnavailable) {
		t.Errorf("err = %v，期望 ErrObjectStoreUnavailable", err)
	}
}

// assertContentUnchanged 断言内容层一项都没动。
func assertContentUnchanged(t *testing.T, before, after Skill) {
	t.Helper()
	if after.Current.ID != before.Current.ID {
		t.Errorf("当前版本被改了: %q → %q", before.Current.ID, after.Current.ID)
	}
	if after.Current.SkippedFiles != before.Current.SkippedFiles {
		t.Errorf("跳过条数被改了: %d → %d", before.Current.SkippedFiles, after.Current.SkippedFiles)
	}
	if len(after.Current.Files) != len(before.Current.Files) {
		t.Fatalf("文件数变了: %d → %d", len(before.Current.Files), len(after.Current.Files))
	}
	for i, file := range after.Current.Files {
		if file != before.Current.Files[i] {
			t.Errorf("第 %d 条文件变了: %+v → %+v", i, before.Current.Files[i], file)
		}
	}
}
