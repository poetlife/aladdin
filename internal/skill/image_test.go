package skill

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/poetlife/aladdin/internal/imagetype"
	"github.com/poetlife/aladdin/internal/objectstore"
)

// 展示图集是**说明层的一项**，不是包的内容。这一组用例守的正是这几条线：
//
//   - 它进对象存储（一张一个键），但不进文件清单、不改 skipped_files；
//   - **顺序与地址无关**：删中间一张、重排都不改其余任何一张的键；
//   - 两个来源（纳管时逐张从仓库取 / 界面上逐张直传）落到同一组图上；
//   - 从仓库取那条**核对字节**，因为服务端看得见它；
//   - 列表只签首图，详情才签整组。

// 一段最小的 PNG：八个字节的签名加一点内容。魔数核对只看文件头。
var testPNG = string([]byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00, 0x00, 0x0d, 'I', 'H', 'D', 'R'})

// galleryTree 造一棵带展示图的树。
//
// 图都在 `gallery/` 下、且**都是二进制**：它们因此既在目录树里（能被取回来当展示
// 图），又被包契约跳过（不进文件清单）——这正是"展示图不是包的内容"那句话的形状。
func galleryTree() map[string]string {
	return map[string]string{
		ManifestPath:         manifest("mono-color", "单色印刷风出图。"),
		"palette.md":         "# 色板\n",
		"gallery/a.png":      testPNG + "a",
		"gallery/b.png":      testPNG + "b",
		"gallery/c.jpg":      testPNG + "c", // 名字说是 JPEG，字节是 PNG
		"gallery/notes.html": "<!doctype html><p>不是图</p>",
		"gallery/text.txt":   "文本",
		"gallery/big.png":    testPNG + strings.Repeat("\x00", imagetype.MaxBytes),
	}
}

// importWithImages 纳管一个带展示图的技能。
func (f *fixture) importWithImages(t *testing.T, imagePaths []string) (Skill, error) {
	t.Helper()
	f.remote.setTree(testSHA, galleryTree())
	return f.service.Import(context.Background(), ImportParams{
		RepositoryURL: "https://github.com/" + testOwner + "/" + testRepo,
		ImagePaths:    imagePaths,
	})
}

// importGallery 纳管一个带三张展示图的技能，失败即终止用例。
func (f *fixture) importGallery(t *testing.T) Skill {
	t.Helper()
	item, err := f.importWithImages(t, []string{"gallery/a.png", "gallery/b.png", "gallery/c.jpg"})
	if err != nil {
		t.Fatalf("带展示图纳管失败: %v", err)
	}
	return item
}

// 展示图进对象存储，而**不在文件清单里**、不改跳过条数；顺序即给定顺序。
func TestImportWithImagesKeepsThemOutOfThePackage(t *testing.T) {
	f := newFixture(t)
	item := f.importGallery(t)
	ctx := context.Background()

	if len(item.Images) != 3 {
		t.Fatalf("图集 = %d 张，期望 3 张", len(item.Images))
	}
	for i, want := range []string{"gallery/a.png", "gallery/b.png", "gallery/c.jpg"} {
		if got := item.Images[i]; got.ID == "" || got.ObjectKey != ImageKey(item.ID, got.ID) {
			t.Errorf("第 %d 张（%s）的键 = %q", i, want, got.ObjectKey)
		}
		if _, err := f.objects.Read(ctx, item.Images[i].ObjectKey); err != nil {
			t.Errorf("第 %d 张没有写进对象存储: %v", i, err)
		}
	}
	// 每张一个键：地址不重复。
	seen := map[string]bool{}
	for _, image := range item.Images {
		if seen[image.ObjectKey] {
			t.Errorf("两张图共用了同一个键 %q", image.ObjectKey)
		}
		seen[image.ObjectKey] = true
	}

	// 文件清单里没有它们——展示图不是包的内容。（同目录下**是文本**的那两条照收，
	// 它们与展示图无关。）
	for _, file := range item.Current.Files {
		for _, imagePath := range []string{"gallery/a.png", "gallery/b.png", "gallery/c.jpg"} {
			if file.Path == imagePath {
				t.Errorf("展示图 %q 进了文件清单", file.Path)
			}
		}
	}
	// 它们仍然是"被跳过的那类二进制"，跳过条数照算。
	if item.Current.SkippedFiles == 0 {
		t.Error("跳过条数为零：展示图不该把它们从跳过里挪出来")
	}

	// **列表只签首图**，详情才签整组。
	list := f.service.ViewOf(ctx, item, false)
	if len(list.Shown) != 1 || list.CoverURL() == "" {
		t.Errorf("列表形状的呈现 = %d 张、地址 %q，期望只有首图且地址非空", len(list.Shown), list.CoverURL())
	}
	if list.CoverURL() != f.service.imageURL(ctx, item.ID, item.Images[0]) {
		t.Errorf("首图地址不是第一张的：%q", list.CoverURL())
	}
	detail := f.service.ViewOf(ctx, item, true)
	if len(detail.Shown) != 3 {
		t.Errorf("详情形状的呈现 = %d 张，期望 3 张", len(detail.Shown))
	}
	for i := range detail.Shown {
		if detail.Shown[i].ID != item.Images[i].ID {
			t.Errorf("第 %d 张的标识 = %q，期望 %q", i, detail.Shown[i].ID, item.Images[i].ID)
		}
	}
}

// 从仓库取那条的**四道校验**，以及张数与合计的两道上限。任何一道不过即整次纳管失败
// ——不留下"技能进来了、只是没有图"这种要人去猜的状态，也不留下痕迹。
func TestImportImageRejections(t *testing.T) {
	// 12 张各 1 MiB + 1 字节：单张都在上限内，合计超过 12 MiB。
	bigTree := map[string]string{ManifestPath: manifest("mono-color", "出图。")}
	bigPaths := make([]string, 0, MaxImages)
	for i := 0; i < MaxImages; i++ {
		path := fmt.Sprintf("gallery/big-%02d.png", i)
		bigTree[path] = testPNG + strings.Repeat("\x01", (1<<20)+1)
		bigPaths = append(bigPaths, path)
	}

	cases := []struct {
		name       string
		imagePaths []string
		tree       map[string]string
		want       error
	}{
		{name: "不在包里", imagePaths: []string{"gallery/nope.png"}, want: ErrImageNotAllowed},
		{name: "路径形状不合法", imagePaths: []string{"../a.png"}, want: ErrImageNotAllowed},
		{name: "扩展名不是图", imagePaths: []string{"gallery/text.txt"}, want: ErrImageNotAllowed},
		{name: "看着是图、字节不是", imagePaths: []string{"gallery/notes.html"}, want: ErrImageNotAllowed},
		{name: "单张超上限", imagePaths: []string{"gallery/big.png"}, want: ErrImageNotAllowed},
		{name: "同一条路径给了两次", imagePaths: []string{"gallery/a.png", "gallery/a.png"}, want: ErrImageNotAllowed},
		{name: "合计超上限", imagePaths: bigPaths, tree: bigTree, want: ErrImagesTooLarge},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			if tc.tree != nil {
				f.remote.setTree(testSHA, tc.tree)
			} else {
				f.remote.setTree(testSHA, galleryTree())
			}
			_, err := f.service.Import(context.Background(), ImportParams{
				RepositoryURL: "https://github.com/" + testOwner + "/" + testRepo,
				ImagePaths:    tc.imagePaths,
			})
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v，期望 %v", err, tc.want)
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

	t.Run("张数超上限", func(t *testing.T) {
		f := newFixture(t)
		tree := map[string]string{ManifestPath: manifest("mono-color", "出图。")}
		paths := make([]string, 0, MaxImages+1)
		for i := 0; i <= MaxImages; i++ {
			path := fmt.Sprintf("gallery/%02d.png", i)
			tree[path] = testPNG + fmt.Sprint(i)
			paths = append(paths, path)
		}
		f.remote.setTree(testSHA, tree)
		_, err := f.service.Import(context.Background(), ImportParams{
			RepositoryURL: "https://github.com/" + testOwner + "/" + testRepo,
			ImagePaths:    paths,
		})
		if !errors.Is(err, ErrTooManyImages) {
			t.Fatalf("err = %v，期望 ErrTooManyImages", err)
		}
	})
}

// **扩展名决定"是不是一张图"，字节决定"是哪一种图"。** 两者不符时以字节为准：一份
// 叫着 `.jpeg` 而实际是 PNG 的文件到处都有。
func TestImportImageTrustsBytesOverExtension(t *testing.T) {
	f := newFixture(t)
	item, err := f.importWithImages(t, []string{"gallery/c.jpg"})
	if err != nil {
		t.Fatalf("名字与内容不符的图被拒了: %v", err)
	}
	if len(item.Images) != 1 {
		t.Fatalf("图集 = %d 张，期望 1 张", len(item.Images))
	}
}

// **展示图的单张上限是它自己那一档，不是包契约的单文件上限。**
//
// 展示图上限 2 MiB 比包里的单文件上限 256 KiB 宽，而取字节那一步是共用的。共用时把
// 包里的上限写死进去，表现是"这张图明明在上限内，却被按包里的规矩拒了"。
func TestImportImageUsesItsOwnSizeLimit(t *testing.T) {
	f := newFixture(t)
	big := testPNG + strings.Repeat("\x01", 512<<10)
	f.remote.setTree(testSHA, map[string]string{
		ManifestPath:      manifest("mono-color", "出图。"),
		"gallery/big.png": big,
	})
	item, err := f.service.Import(context.Background(), ImportParams{
		RepositoryURL: "https://github.com/" + testOwner + "/" + testRepo,
		ImagePaths:    []string{"gallery/big.png"},
	})
	if err != nil {
		t.Fatalf("上限之内的展示图被拒了: %v", err)
	}
	if data, err := f.objects.Read(context.Background(), item.Images[0].ObjectKey); err != nil {
		t.Fatalf("展示图没有写进对象存储: %v", err)
	} else if len(data) != len(big) {
		t.Errorf("展示图字节数 = %d，期望 %d", len(data), len(big))
	}
}

// 不给展示图时就**没有**图：空图集是"没有封面"的唯一判据，界面据此渲染占位。
func TestImportWithoutImagesHasNone(t *testing.T) {
	f := newFixture(t)
	item := f.importOne(t)
	if len(item.Images) != 0 {
		t.Errorf("图集 = %+v，期望空", item.Images)
	}
	if url := f.service.ViewOf(context.Background(), item, true).CoverURL(); url != "" {
		t.Errorf("没有展示图却给出了首图地址 %q", url)
	}
}

// 直传那条路：签发的规则与白名单同源，声明的类型、单张大小、张数与合计在签发时就挡。
func TestBeginImageUploadValidates(t *testing.T) {
	f := newFixture(t)
	item := f.importGallery(t)
	ctx := context.Background()

	// 新增一张：服务端分配标识，且它与这一次的键对得上。
	imageID, credential, err := f.service.BeginImageUpload(ctx, item.ID, "", "image/png", 1024)
	if err != nil {
		t.Fatalf("合法声明被拒: %v", err)
	}
	if imageID == "" || credential.Key != ImageKey(item.ID, imageID) {
		t.Errorf("签发的标识/键 = %q / %q", imageID, credential.Key)
	}
	// 同一张再签一次（换图）：键不变。
	_, again, err := f.service.BeginImageUpload(ctx, item.ID, item.Images[0].ID, "image/png", 1024)
	if err != nil {
		t.Fatalf("换图签发被拒: %v", err)
	}
	if again.Key != item.Images[0].ObjectKey {
		t.Errorf("换图签发的键 = %q，期望不变", again.Key)
	}

	if _, _, err := f.service.BeginImageUpload(ctx, item.ID, "", "image/svg+xml", 1024); !errors.Is(err, imagetype.ErrTypeNotAllowed) {
		t.Errorf("SVG err = %v，期望 ErrTypeNotAllowed", err)
	}
	if _, _, err := f.service.BeginImageUpload(ctx, item.ID, "", "image/png", imagetype.MaxBytes+1); !errors.Is(err, ErrImageTooLarge) {
		t.Errorf("单张超上限 err = %v，期望 ErrImageTooLarge", err)
	}
	if _, _, err := f.service.BeginImageUpload(ctx, "skl_missing", "", "image/png", 1); !errors.Is(err, ErrSkillNotFound) {
		t.Errorf("技能不存在 err = %v，期望 ErrSkillNotFound", err)
	}
	if _, _, err := f.service.BeginImageUpload(ctx, item.ID, "ski_missing", "image/png", 1); !errors.Is(err, ErrImageNotFound) {
		t.Errorf("换一张不在图集里的 err = %v，期望 ErrImageNotFound", err)
	}

	// 张数：补到上限之后再要一张就被拒。
	for len(item.Images) < MaxImages {
		imageID, credential, err := f.service.BeginImageUpload(ctx, item.ID, "", "image/png", 16)
		if err != nil {
			t.Fatalf("补到上限的过程中被拒: %v", err)
		}
		f.objects.SimulateUpload(credential.Key, []byte(testPNG))
		if item, err = f.service.CommitImageUpload(ctx, item.ID, imageID); err != nil {
			t.Fatalf("补到上限的过程中提交失败: %v", err)
		}
	}
	if _, _, err := f.service.BeginImageUpload(ctx, item.ID, "", "image/png", 16); !errors.Is(err, ErrTooManyImages) {
		t.Errorf("超过张数上限 err = %v，期望 ErrTooManyImages", err)
	}

	// 合计：单张声明在上限内，但"其余 + 这一次"放不下。判的是两次的总和，因此新增
	// 与换图两条路都要挡。
	t.Run("合计超上限", func(t *testing.T) {
		f := newFixture(t)
		tree := map[string]string{ManifestPath: manifest("mono-color", "出图。")}
		paths := make([]string, 0, MaxImages-1)
		for i := 0; i < MaxImages-1; i++ {
			path := fmt.Sprintf("gallery/%02d.png", i)
			tree[path] = testPNG + strings.Repeat("\x01", 1<<20)
			paths = append(paths, path)
		}
		f.remote.setTree(testSHA, tree)
		full, err := f.service.Import(ctx, ImportParams{
			RepositoryURL: "https://github.com/" + testOwner + "/" + testRepo,
			ImagePaths:    paths,
		})
		if err != nil {
			t.Fatalf("纳管失败: %v", err)
		}
		if _, _, err := f.service.BeginImageUpload(ctx, full.ID, "", "image/png", imagetype.MaxBytes); !errors.Is(err, ErrImagesTooLarge) {
			t.Errorf("新增时合计超上限 err = %v，期望 ErrImagesTooLarge", err)
		}
		if _, _, err := f.service.BeginImageUpload(ctx, full.ID, full.Images[0].ID, "image/png", imagetype.MaxBytes); !errors.Is(err, ErrImagesTooLarge) {
			t.Errorf("换图时合计超上限 err = %v，期望 ErrImagesTooLarge", err)
		}
	})
}

// 提交要核对对象确实到了；到了就把这一张排到图集末尾。
func TestCommitImageUpload(t *testing.T) {
	f := newFixture(t)
	item := f.importGallery(t)
	ctx := context.Background()

	imageID, credential, err := f.service.BeginImageUpload(ctx, item.ID, "", "image/png", 16)
	if err != nil {
		t.Fatalf("签发失败: %v", err)
	}
	// 没传字节就提交：一次可以重来的上传，**不是**存储故障、也不是内容问题。
	if _, err := f.service.CommitImageUpload(ctx, item.ID, imageID); !errors.Is(err, ErrImageUploadMissing) {
		t.Fatalf("对象不存在却提交成功了: err = %v", err)
	}

	data := []byte(testPNG + "d")
	f.objects.SimulateUpload(credential.Key, data)
	updated, err := f.service.CommitImageUpload(ctx, item.ID, imageID)
	if err != nil {
		t.Fatalf("提交失败: %v", err)
	}
	if len(updated.Images) != 4 || updated.Images[3].ID != imageID {
		t.Fatalf("提交之后图集 = %+v", updated.Images)
	}
	if updated.Images[3].SizeBytes != int64(len(data)) {
		t.Errorf("记下的字节数 = %d，期望 %d", updated.Images[3].SizeBytes, len(data))
	}
	// 前三张一张没动。
	for i, want := range item.Images {
		if updated.Images[i] != want {
			t.Errorf("追加动了第 %d 张: %+v，期望 %+v", i, updated.Images[i], want)
		}
	}
	// 再提交一次是幂等的：还是四张，位置不变。
	again, err := f.service.CommitImageUpload(ctx, item.ID, imageID)
	if err != nil {
		t.Fatalf("重复提交失败: %v", err)
	}
	if len(again.Images) != 4 || again.Images[3].ID != imageID {
		t.Errorf("重复提交之后图集 = %+v", again.Images)
	}
}

// 换图是**就地覆盖**：标识、键与位置都不变，只有字节数跟着真实值走。
func TestCommitImageUploadReplacesInPlace(t *testing.T) {
	f := newFixture(t)
	item := f.importGallery(t)
	ctx := context.Background()
	target := item.Images[1]
	data := []byte(testPNG + strings.Repeat("x", 128))

	f.objects.SimulateUpload(target.ObjectKey, data)
	updated, err := f.service.CommitImageUpload(ctx, item.ID, target.ID)
	if err != nil {
		t.Fatalf("换图失败: %v", err)
	}
	if len(updated.Images) != 3 {
		t.Fatalf("换图之后图集 = %+v", updated.Images)
	}
	if updated.Images[1].ID != target.ID || updated.Images[1].ObjectKey != target.ObjectKey {
		t.Errorf("换图改了标识或键: %+v", updated.Images[1])
	}
	if updated.Images[1].SizeBytes != int64(len(data)) {
		t.Errorf("换图之后的字节数 = %d，期望 %d", updated.Images[1].SizeBytes, len(data))
	}
	// 顺序与其余两张都不动。
	for _, i := range []int{0, 2} {
		if updated.Images[i] != item.Images[i] {
			t.Errorf("换图动了第 %d 张: %+v", i, updated.Images[i])
		}
	}
}

// 提交时按**真实字节数**复核单张上限：声明可以撒谎，而这一步服务端看得见字节。
// 超限的对象要被删掉——它没有被任何一行引用。
func TestCommitImageUploadRejectsRealOversize(t *testing.T) {
	f := newFixture(t)
	item := f.importOne(t)
	ctx := context.Background()

	imageID, credential, err := f.service.BeginImageUpload(ctx, item.ID, "", "image/png", 16)
	if err != nil {
		t.Fatalf("签发失败: %v", err)
	}
	f.objects.SimulateUpload(credential.Key, []byte(testPNG+strings.Repeat("\x00", imagetype.MaxBytes)))
	if _, err := f.service.CommitImageUpload(ctx, item.ID, imageID); !errors.Is(err, ErrImageTooLarge) {
		t.Fatalf("err = %v，期望 ErrImageTooLarge", err)
	}
	if _, err := f.objects.Read(ctx, credential.Key); !errors.Is(err, objectstore.ErrObjectNotFound) {
		t.Errorf("超限的对象没有被删掉: err = %v", err)
	}
	if got, err := f.store.GetSkill(ctx, item.ID); err != nil {
		t.Fatalf("读取技能: %v", err)
	} else if len(got.Images) != 0 {
		t.Errorf("被拒的提交进了图集: %+v", got.Images)
	}
}

// 删一张：**其余每一张的键与相对次序逐字不变**；删掉的只有它那一个对象。
func TestDeleteImageKeepsTheRest(t *testing.T) {
	f := newFixture(t)
	item := f.importGallery(t)
	ctx := context.Background()

	remaining, err := f.service.DeleteImage(ctx, item.ID, item.Images[1].ID)
	if err != nil {
		t.Fatalf("删图失败: %v", err)
	}
	if len(remaining.Images) != 2 {
		t.Fatalf("删图之后图集 = %+v", remaining.Images)
	}
	if remaining.Images[0] != item.Images[0] || remaining.Images[1] != item.Images[2] {
		t.Errorf("删中间一张动了其余: %+v", remaining.Images)
	}
	if _, err := f.objects.Read(ctx, item.Images[1].ObjectKey); !errors.Is(err, objectstore.ErrObjectNotFound) {
		t.Errorf("删掉的那一张的对象还在: err = %v", err)
	}
	for _, i := range []int{0, 2} {
		if _, err := f.objects.Read(ctx, item.Images[i].ObjectKey); err != nil {
			t.Errorf("第 %d 张的对象被误删了: %v", i, err)
		}
	}

	// 删一张已经不在的图：它是一次拼错了标识，不是一个状态。
	if _, err := f.service.DeleteImage(ctx, item.ID, item.Images[1].ID); !errors.Is(err, ErrImageNotFound) {
		t.Errorf("重复删 err = %v，期望 ErrImageNotFound", err)
	}
	if _, err := f.service.DeleteImage(ctx, "skl_missing", item.Images[0].ID); !errors.Is(err, ErrSkillNotFound) {
		t.Errorf("技能不存在 err = %v，期望 ErrSkillNotFound", err)
	}
}

// 重排是**整体替换**：第一项即首图；给出的不是当前图集的一个排列时整个拒绝。
func TestReorderImages(t *testing.T) {
	f := newFixture(t)
	item := f.importGallery(t)
	ctx := context.Background()

	order := []string{item.Images[2].ID, item.Images[0].ID, item.Images[1].ID}
	updated, err := f.service.ReorderImages(ctx, item.ID, order)
	if err != nil {
		t.Fatalf("重排失败: %v", err)
	}
	for i, want := range order {
		if updated.Images[i].ID != want {
			t.Errorf("重排之后第 %d 张 = %q，期望 %q", i, updated.Images[i].ID, want)
		}
	}
	// 首图跟着换，而**地址（键）一张都没变**。
	if got := f.service.ViewOf(ctx, updated, false).CoverURL(); got != f.service.imageURL(ctx, item.ID, item.Images[2]) {
		t.Errorf("首图地址 = %q，期望第二张原地址", got)
	}
	for i, image := range updated.Images {
		if _, err := f.objects.Read(ctx, image.ObjectKey); err != nil {
			t.Errorf("重排之后第 %d 张取不到了: %v", i, err)
		}
	}

	if _, err := f.service.ReorderImages(ctx, item.ID, order[:2]); !errors.Is(err, ErrImageOrderMismatch) {
		t.Errorf("少一张 err = %v，期望 ErrImageOrderMismatch", err)
	}
	if _, err := f.service.ReorderImages(ctx, "skl_missing", order); !errors.Is(err, ErrSkillNotFound) {
		t.Errorf("技能不存在 err = %v，期望 ErrSkillNotFound", err)
	}
}

// 图集的每一步写入都**不碰内容层**：当前版本、文件清单与跳过条数逐字不变。
func TestImageChangesLeaveContentLayerAlone(t *testing.T) {
	f := newFixture(t)
	item := f.importGallery(t)
	ctx := context.Background()

	// 加一张。
	imageID, credential, err := f.service.BeginImageUpload(ctx, item.ID, "", "image/png", 16)
	if err != nil {
		t.Fatalf("签发失败: %v", err)
	}
	f.objects.SimulateUpload(credential.Key, []byte(testPNG+"d"))
	added, err := f.service.CommitImageUpload(ctx, item.ID, imageID)
	if err != nil {
		t.Fatalf("提交失败: %v", err)
	}
	assertContentUnchanged(t, item, added)

	// 换一张。
	f.objects.SimulateUpload(added.Images[0].ObjectKey, []byte(testPNG+strings.Repeat("y", 64)))
	replaced, err := f.service.CommitImageUpload(ctx, item.ID, added.Images[0].ID)
	if err != nil {
		t.Fatalf("换图失败: %v", err)
	}
	assertContentUnchanged(t, item, replaced)

	// 删一张。
	deleted, err := f.service.DeleteImage(ctx, item.ID, added.Images[0].ID)
	if err != nil {
		t.Fatalf("删图失败: %v", err)
	}
	assertContentUnchanged(t, item, deleted)

	// 重排。
	ids := make([]string, 0, len(deleted.Images))
	for i := len(deleted.Images) - 1; i >= 0; i-- {
		ids = append(ids, deleted.Images[i].ID)
	}
	reordered, err := f.service.ReorderImages(ctx, item.ID, ids)
	if err != nil {
		t.Fatalf("重排失败: %v", err)
	}
	assertContentUnchanged(t, item, reordered)
}

// 删技能时展示图的对象**一起删**——每个键都由它独占；内容对象一个都不删（按摘要共享）。
func TestDeleteSkillRemovesImagesButKeepsContent(t *testing.T) {
	f := newFixture(t)
	item := f.importGallery(t)
	ctx := context.Background()
	contentDigest := item.Current.Files[0].Digest

	if err := f.service.Delete(ctx, item.ID); err != nil {
		t.Fatalf("删除失败: %v", err)
	}
	for i, image := range item.Images {
		if _, err := f.objects.Read(ctx, image.ObjectKey); !errors.Is(err, objectstore.ErrObjectNotFound) {
			t.Errorf("第 %d 张的对象没有被删掉: err = %v", i, err)
		}
	}
	if _, err := f.objects.Read(ctx, ContentObjectKey(contentDigest)); err != nil {
		t.Errorf("内容对象被误删了: %v", err)
	}
}

// 没有配置对象存储时，图集整体不可用（与目录同一条降级取向）。
func TestImagesWithoutObjectStoreAreUnavailable(t *testing.T) {
	store := NewMemoryStore()
	service := NewService(Deps{Store: store, Remote: newFakeRemote()})
	ctx := context.Background()

	if _, _, err := service.BeginImageUpload(ctx, "skl_x", "", "image/png", 1); !errors.Is(err, ErrObjectStoreUnavailable) {
		t.Errorf("签发 err = %v，期望 ErrObjectStoreUnavailable", err)
	}
	if _, err := service.CommitImageUpload(ctx, "skl_x", "ski_x"); !errors.Is(err, ErrObjectStoreUnavailable) {
		t.Errorf("提交 err = %v，期望 ErrObjectStoreUnavailable", err)
	}
	if _, err := service.DeleteImage(ctx, "skl_x", "ski_x"); !errors.Is(err, ErrObjectStoreUnavailable) {
		t.Errorf("删图 err = %v，期望 ErrObjectStoreUnavailable", err)
	}
	if _, err := service.Import(ctx, ImportParams{RepositoryURL: "https://github.com/a/b"}); !errors.Is(err, ErrObjectStoreUnavailable) {
		t.Errorf("纳管 err = %v，期望 ErrObjectStoreUnavailable", err)
	}
}

// failingObjects 是一个"每次调用都失败"的对象存储：它嵌内存实现，只把造故障要用的
// 那几个方法换掉。**存储这次失败了**与**这个部署没有桶**是两种结论，这一组用例就是
// 用来把它们分开的。
type failingObjects struct{ *objectstore.MemoryStore }

func (failingObjects) IssueUpload(context.Context, string, []objectstore.TypeRule, bool) (objectstore.Credential, error) {
	return objectstore.Credential{}, errStorageDown
}

func (failingObjects) Head(context.Context, string) (objectstore.ObjectStat, error) {
	return objectstore.ObjectStat{}, errStorageDown
}

func (failingObjects) Put(context.Context, string, string, []byte) error { return errStorageDown }

// errStorageDown 是一次**原始的**存储故障（像 SDK 抛回来的那样）：它本身不带任何
// 领域标记，分类是调用方的事。
var errStorageDown = errors.New("connection reset by peer")

// **一次存储故障不是"这个部署没有桶"。** 前者可重试、要去查桶；后者是部署形态的
// 事实、要去查配置。原来这两条路都把故障包成了 ErrObjectStoreUnavailable，于是客户端
// 拿到的是一句"服务暂时不可用"——既不知道是哪一头，也没有任何地方留下原因。
func TestImageStorageFailureIsNotMissingConfiguration(t *testing.T) {
	f := newFixture(t)
	item := f.importOne(t)
	service := NewService(Deps{
		Store:   f.store,
		Objects: failingObjects{MemoryStore: objectstore.NewMemoryStore()},
		Remote:  f.remote,
	})
	ctx := context.Background()

	// 上报的两个动作各自撞一次存储：签发（换直传凭证）与提交（核对上传）。
	_, _, beginErr := service.BeginImageUpload(ctx, item.ID, "", "image/png", 16)
	if !errors.Is(beginErr, objectstore.ErrStoreUnavailable) {
		t.Errorf("签发 err = %v，期望 ErrStoreUnavailable", beginErr)
	}
	if errors.Is(beginErr, ErrObjectStoreUnavailable) {
		t.Errorf("签发 err = %v：一次存储故障被说成了\"没有配置对象存储\"", beginErr)
	}

	_, commitErr := service.CommitImageUpload(ctx, item.ID, "ski_x")
	if !errors.Is(commitErr, objectstore.ErrStoreUnavailable) {
		t.Errorf("提交 err = %v，期望 ErrStoreUnavailable", commitErr)
	}
	if errors.Is(commitErr, ErrObjectStoreUnavailable) {
		t.Errorf("提交 err = %v：一次存储故障被说成了\"没有配置对象存储\"", commitErr)
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
