package galaxy

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/poetlife/aladdin/internal/objectstore"
)

// 声明的类型必须在白名单内，且**归一化之后**才判定。
//
// 直传之后服务端看不到字节，所以白名单从"字节确实是图片"降级成了"下发时的类型
// 必属一个无害集合"（见 docs/design/objectstore/README.md）。它仍然有意义的原因
// 是白名单里没有任何可执行类型。
func TestNormalizeAssetType(t *testing.T) {
	allowed := map[string]MediaKind{
		"image/png":                 MediaKindImage,
		"image/jpeg":                MediaKindImage,
		"image/gif":                 MediaKindImage,
		"image/webp":                MediaKindImage,
		"video/mp4":                 MediaKindVideo,
		"video/webm":                MediaKindVideo,
		"audio/mpeg":                MediaKindAudio,
		"audio/wave":                MediaKindAudio,
		"application/ogg":           MediaKindAudio,
		"font/woff2":                MediaKindFont,
		"font/woff":                 MediaKindFont,
		"font/ttf":                  MediaKindFont,
		"font/otf":                  MediaKindFont,
		"IMAGE/PNG":                 MediaKindImage,
		"image/jpeg; charset=utf-8": MediaKindImage,
	}
	for declared, wantKind := range allowed {
		mediaType, kind, err := NormalizeAssetType(declared)
		if err != nil {
			t.Errorf("%q 被拒: %v", declared, err)
			continue
		}
		if kind != wantKind {
			t.Errorf("%q 的类别 = %q，期望 %q", declared, kind, wantKind)
		}
		if mediaType != strings.ToLower(mediaType) || strings.Contains(mediaType, ";") {
			t.Errorf("%q 归一化成了 %q，期望小写且不带参数", declared, mediaType)
		}
	}

	rejected := []string{"text/html", "image/svg+xml", "application/javascript", "", "image"}
	for _, declared := range rejected {
		if _, _, err := NormalizeAssetType(declared); !errors.Is(err, ErrAssetTypeNotAllowed) {
			t.Errorf("%q 被接受了，期望 ErrAssetTypeNotAllowed", declared)
		}
	}
}

// 签发给存储的规则**只有本次声明的那一条**：类型与它那一档的上限绑在一起。
//
// 不把整份白名单摊进策略有两个原因，第二个是实测踩过的：摊进去会让凭证允许
// "用一个类型声明、拿另一个类型的上限"写同一个键；而策略文档会随白名单线性
// 变长，STS 对 Policy 有长度上限，越过它换证就失败。这条测试钉住"恰好一条"。
func TestBeginAssetUploadIssuesRuleForDeclaredTypeOnly(t *testing.T) {
	f := newFixture(t)
	project := f.createProject(t, "工程")

	if _, _, err := f.service.BeginAssetUpload(context.Background(), testOwner, project.ID, "image/png", 1024); err != nil {
		t.Fatalf("签发失败: %v", err)
	}
	issued := f.objects.Issued()
	if len(issued) != 1 {
		t.Fatalf("签发了 %d 次，期望恰好一次", len(issued))
	}
	want := []objectstore.TypeRule{{ContentType: "image/png", MaxBytes: ImageMaxBytes}}
	if !reflect.DeepEqual(issued[0].Rules, want) {
		t.Errorf("规则 = %+v，期望恰好 %+v", issued[0].Rules, want)
	}
}

// 规则由**声明的类型**派生，不由白名单派生：每种类型派生出它自己那一档的上限。
func TestAssetTypeRuleBindsTypeToItsTier(t *testing.T) {
	for declared, want := range map[string]objectstore.TypeRule{
		"image/webp":      {ContentType: "image/webp", MaxBytes: ImageMaxBytes},
		"video/webm":      {ContentType: "video/webm", MaxBytes: VideoMaxBytes},
		"audio/wave":      {ContentType: "audio/wave", MaxBytes: AudioMaxBytes},
		"application/ogg": {ContentType: "application/ogg", MaxBytes: AudioMaxBytes},
		"font/woff2":      {ContentType: "font/woff2", MaxBytes: FontMaxBytes},
		"FONT/TTF":        {ContentType: "font/ttf", MaxBytes: FontMaxBytes},
	} {
		mediaType, kind, err := NormalizeAssetType(declared)
		if err != nil {
			t.Fatalf("%q 应当被接受: %v", declared, err)
		}
		if got := AssetTypeRule(mediaType, kind); got != want {
			t.Errorf("%q 的规则 = %+v，期望 %+v", declared, got, want)
		}
	}
}

// 上限**按类别分档**：图片档与视频档差两个数量级，用同一个上限卡两者都不对。
func TestMaxBytesForIsTiered(t *testing.T) {
	if MaxBytesFor(MediaKindImage) != ImageMaxBytes ||
		MaxBytesFor(MediaKindVideo) != VideoMaxBytes ||
		MaxBytesFor(MediaKindAudio) != AudioMaxBytes ||
		MaxBytesFor(MediaKindFont) != FontMaxBytes {
		t.Fatal("分类上限与常量不一致")
	}
	if ImageMaxBytes >= VideoMaxBytes {
		t.Error("图片上限不低于视频上限——两档没有区分开")
	}
	// 未知类别返回 0 而不是某个合法档：把"未知"表现成一个合法取值会让
	// "用视频的上限去卡图片"这类错误悄悄成立。
	if MaxBytesFor("unknown") != 0 {
		t.Error("未知类别给出了非零上限")
	}
}

// 类型与它的上限**绑在一起**：声明成图片就必须守图片那一档。
func TestBeginAssetUploadUsesTierOfDeclaredType(t *testing.T) {
	cases := []struct {
		name         string
		declaredType string
		size         int64
		wantTooLarge bool
	}{
		{"图片刚好在上限内", "image/png", ImageMaxBytes, false},
		{"图片超一点", "image/png", ImageMaxBytes + 1, true},
		{"音频在上限内", "audio/mpeg", AudioMaxBytes, false},
		{"音频超一点", "audio/ogg", AudioMaxBytes + 1, true},
		{"视频在上限内", "video/mp4", VideoMaxBytes, false},
		{"视频超一点", "video/mp4", VideoMaxBytes + 1, true},
		// 用视频的上限去卡图片不被接受：上限取自**声明的类型**。
		{"图片按自己的档判，不按视频的", "image/png", VideoMaxBytes, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			project := f.createProject(t, "工程")
			declared := tc.declaredType
			if declared == "audio/ogg" {
				declared = "application/ogg"
			}
			_, _, err := f.service.BeginAssetUpload(context.Background(), testOwner, project.ID, declared, tc.size)
			if tc.wantTooLarge {
				if !errors.Is(err, ErrAssetTooLarge) {
					t.Errorf("err = %v，期望 ErrAssetTooLarge", err)
				}
				return
			}
			if err != nil {
				t.Errorf("err = %v，期望通过", err)
			}
		})
	}
}

// 白名单外的类型**拒于签发这一步**，因此不会产生任何对象与凭证。
func TestBeginAssetUploadRejectsTypeNotAllowed(t *testing.T) {
	f := newFixture(t)
	project := f.createProject(t, "工程")

	for _, declared := range []string{"text/html", "image/svg+xml", ""} {
		if _, _, err := f.service.BeginAssetUpload(context.Background(), testOwner, project.ID, declared, 1024); !errors.Is(err, ErrAssetTypeNotAllowed) {
			t.Errorf("%q err = %v，期望 ErrAssetTypeNotAllowed", declared, err)
		}
	}
	if len(f.objects.Issued()) != 0 {
		t.Error("被拒的声明仍然签发了凭证")
	}
}

// 原始文件名**不进对象键**：它可能含路径分隔符、控制字符以及别人的名字。
func TestAssetFilenameStaysOutOfObjectKey(t *testing.T) {
	f := newFixture(t)
	project := f.createProject(t, "工程")

	nasty := "../../etc/passwd\x00 name.png"
	asset := f.uploadAsset(t, project.ID, "image/png", nasty, []byte("aaa"))

	want := AssetObjectKey(project.ID, asset.MediaKind, asset.ID)
	issued := f.objects.Issued()
	if len(issued) != 1 || issued[0].Key != want {
		t.Errorf("对象键 = %v，期望恰好 %q", issued, want)
	}
	// 文件名里的任何一段都不得出现在键里。
	for _, piece := range []string{"passwd", "etc", "name.png", ".."} {
		if strings.Contains(issued[0].Key, piece) {
			t.Errorf("对象键 %q 里出现了文件名的一段 %q", issued[0].Key, piece)
		}
	}
	// 文件名只作为一个标签保留下来。
	if asset.Filename != nasty {
		t.Errorf("文件名 = %q，期望原样保留（只用于展示）", asset.Filename)
	}
}

// 提交核对**存在性**：上传没完成时给出一个可以重来的结论。
func TestCommitAssetUploadRequiresObject(t *testing.T) {
	f := newFixture(t)
	project := f.createProject(t, "工程")
	ctx := context.Background()

	assetID, _, err := f.service.BeginAssetUpload(ctx, testOwner, project.ID, "image/png", 3)
	if err != nil {
		t.Fatalf("签发失败: %v", err)
	}
	// 刻意不写对象：模拟上传中断。
	if _, err := f.service.CommitAssetUpload(ctx, testOwner, project.ID, assetID, "image/png", strings.Repeat("a", 64), "a.png"); !errors.Is(err, ErrAssetObjectMissing) {
		t.Errorf("err = %v，期望 ErrAssetObjectMissing", err)
	}
	if assets, err := f.store.ListAssets(ctx, project.ID); err != nil || len(assets) != 0 {
		t.Errorf("未提交的上传留下了元数据行: %v / %d 条", err, len(assets))
	}
}

// 提交按**真实字节数**判定，超限的对象的被删掉：它是这次失败唯一的残留物。
func TestCommitAssetUploadRejectsOversizedObject(t *testing.T) {
	f := newFixture(t)
	project := f.createProject(t, "工程")
	ctx := context.Background()

	assetID, credential, err := f.service.BeginAssetUpload(ctx, testOwner, project.ID, "image/png", 16)
	if err != nil {
		t.Fatalf("签发失败: %v", err)
	}
	f.objects.SimulateUpload(credential.Key, make([]byte, ImageMaxBytes+1))

	digest := ContentDigest(make([]byte, ImageMaxBytes+1))
	if _, err := f.service.CommitAssetUpload(ctx, testOwner, project.ID, assetID, "image/png", digest, "big.png"); !errors.Is(err, ErrAssetTooLarge) {
		t.Fatalf("err = %v，期望 ErrAssetTooLarge", err)
	}
	if _, err := f.objects.Head(ctx, credential.Key); !errors.Is(err, objectstore.ErrObjectNotFound) {
		t.Error("超限的对象没有被删掉")
	}
}

// 摘要的形状必须先校验：它**是公开区的对象键**，一个含分隔符的取值会把
// "按内容寻址"变成"按调用方给的路径写"。
func TestCommitAssetUploadChecksDigestShape(t *testing.T) {
	f := newFixture(t)
	project := f.createProject(t, "工程")
	ctx := context.Background()

	bad := []string{
		"",
		"../../../etc/passwd",
		strings.Repeat("a", 63),
		strings.Repeat("A", 64), // 大写不是这个格式
		strings.Repeat("z", 64),
	}
	for _, digest := range bad {
		assetID, credential, err := f.service.BeginAssetUpload(ctx, testOwner, project.ID, "image/png", 3)
		if err != nil {
			t.Fatalf("签发失败: %v", err)
		}
		f.objects.SimulateUpload(credential.Key, []byte("aaa"))

		if _, err := f.service.CommitAssetUpload(ctx, testOwner, project.ID, assetID, "image/png", digest, "a.png"); !errors.Is(err, ErrDigestInvalid) {
			t.Errorf("摘要 %q err = %v，期望 ErrDigestInvalid", digest, err)
		}
	}
	if !IsContentDigest(ContentDigest([]byte("aaa"))) {
		t.Error("自己算出来的摘要过不了形状校验")
	}
}

// 同一份字节得到同一个摘要：它是"同一份字节"的标识，公开区按它寻址。
func TestSameBytesSameDigest(t *testing.T) {
	same := []byte("一样的")
	first, second := ContentDigest(same), ContentDigest(same)
	if first != second {
		t.Error("同一份字节算出了不同的摘要")
	}
	if ContentDigest([]byte("a")) == ContentDigest([]byte("b")) {
		t.Error("不同的字节算出了相同的摘要")
	}
}

// 资产**不可变**：读回两次的摘要与类型相同，而接口面上没有替换字节的方法。
func TestAssetIsImmutable(t *testing.T) {
	f := newFixture(t)
	project := f.createProject(t, "工程")
	asset := f.uploadAsset(t, project.ID, "image/png", "a.png", []byte("aaa"))
	ctx := context.Background()

	first, err := f.service.ListAssets(ctx, testOwner, project.ID)
	if err != nil {
		t.Fatalf("列资产失败: %v", err)
	}
	second, err := f.service.ListAssets(ctx, testOwner, project.ID)
	if err != nil {
		t.Fatalf("列资产失败: %v", err)
	}
	if len(first) != 1 || len(second) != 1 {
		t.Fatalf("资产数 = %d / %d，期望各 1", len(first), len(second))
	}
	if first[0].Asset.Digest != second[0].Asset.Digest || first[0].Asset.MediaType != second[0].Asset.MediaType {
		t.Error("两次读回的摘要或类型不同")
	}
	if !first[0].Asset.UploadedAt.Equal(asset.UploadedAt) {
		t.Error("上传时间变了")
	}

	// 接口面上不存在"替换资产字节"的方法。这条靠反射钉住方法的集合：
	// 一个 ReplaceAsset / UpdateAsset 的出现会让这条断言失败。
	serviceType := reflect.TypeOf(f.service)
	for i := 0; i < serviceType.NumMethod(); i++ {
		name := serviceType.Method(i).Name
		if strings.Contains(name, "Replace") || name == "UpdateAsset" || name == "PutAsset" {
			t.Errorf("服务上出现了替换资产的方法: %s", name)
		}
	}
}

// 他工程的资产不可见：查询同时约束工程标识，因此"不存在"与"属于别的工程"
// 是同一个结论。
func TestAssetLookupsAreProjectScoped(t *testing.T) {
	f := newFixture(t)
	mine := f.createProject(t, "我的")
	theirs := f.createProject(t, "别人的")
	asset := f.uploadAsset(t, mine.ID, "image/png", "a.png", []byte("aaa"))
	ctx := context.Background()

	if _, err := f.service.AssetURL(ctx, testOwner, theirs.ID, asset.ID); !errors.Is(err, ErrAssetNotFound) {
		t.Errorf("用另一个工程的标识取地址 err = %v，期望 ErrAssetNotFound", err)
	}
	// 别人的工程里资产清单是空的。
	views, err := f.service.ListAssets(ctx, testOwner, theirs.ID)
	if err != nil {
		t.Fatalf("列资产失败: %v", err)
	}
	if len(views) != 0 {
		t.Errorf("资产数 = %d，期望空集", len(views))
	}
}

// **被任一版本引用的资产不可删**，错误信息指出被哪些版本引用。
//
// 拒绝的理由是"版本不可变的含义包括它此后永远可以发布"：一个引用了已删除资产的
// 版本必然在校验阶段失败，而那时用户看到一句"资产不存在"，却无法从版本内容里
// 知道该怎么做。
func TestReferencedAssetCannotBeDeleted(t *testing.T) {
	f := newFixture(t)
	project := f.createProject(t, "工程")
	// 图片写进一条 HTML 的 src：HTML 里引用素材靠的是 asset:// 记号，而它进入
	// 文件组时成为一条资产条目——拒绝删除的判据正是那份清单。
	asset := f.uploadAsset(t, project.ID, "image/png", "a.png", []byte("aaa"))
	f.pushDraft(t, project.ID, []Entry{
		f.textEntry(t, project.ID, "index.html", `<img src="asset://`+asset.ID+`">`),
		{Path: "a.png", Kind: EntryKindAsset, AssetID: asset.ID},
	})
	version := f.saveVersion(t, project.ID)
	ctx := context.Background()

	err := f.service.DeleteAsset(ctx, testOwner, project.ID, asset.ID)
	if !errors.Is(err, ErrAssetReferenced) {
		t.Fatalf("err = %v，期望 ErrAssetReferenced", err)
	}
	if !strings.Contains(err.Error(), version.ID) {
		t.Errorf("错误信息 %q 没有指出引用它的版本 %s", err, version.ID)
	}

	// 删掉引用它的那个版本之后，资产可以删，且元数据与私有区对象一并消失。
	if err := f.service.DeleteVersion(ctx, testOwner, project.ID, version.ID); err != nil {
		t.Fatalf("删版本失败: %v", err)
	}
	if err := f.service.DeleteAsset(ctx, testOwner, project.ID, asset.ID); err != nil {
		t.Fatalf("删资产失败: %v", err)
	}
	if _, err := f.objects.Head(ctx, AssetObjectKey(project.ID, asset.MediaKind, asset.ID)); !errors.Is(err, objectstore.ErrObjectNotFound) {
		t.Error("私有区对象仍在")
	}
	if assets, err := f.store.ListAssets(ctx, project.ID); err != nil || len(assets) != 0 {
		t.Errorf("元数据行仍在: %v / %d 条", err, len(assets))
	}
}

// 删资产**不因对象缺失而失败**：库内的行是"存在哪些资产"的权威。
func TestDeleteAssetToleratesMissingObject(t *testing.T) {
	f := newFixture(t)
	project := f.createProject(t, "工程")
	asset := f.uploadAsset(t, project.ID, "image/png", "a.png", []byte("aaa"))
	ctx := context.Background()

	// 桶上的对象先没了（对账、人工清理都会造成这种情形）。
	if err := f.objects.Delete(ctx, AssetObjectKey(project.ID, asset.MediaKind, asset.ID)); err != nil {
		t.Fatalf("删除对象失败: %v", err)
	}
	if err := f.service.DeleteAsset(ctx, testOwner, project.ID, asset.ID); err != nil {
		t.Errorf("删除一个对象已缺失的资产失败: %v", err)
	}
}

// 未配置私有桶时，资产相关动作明确报"未启用"，而工程与版本照常可用。
func TestAssetUnavailableWithoutStore(t *testing.T) {
	f := newFixture(t)
	project := f.createProject(t, "工程")
	f.pushDraft(t, project.ID, []Entry{f.textEntry(t, project.ID, "index.html", "<p>x</p>")})
	version := f.saveVersion(t, project.ID)

	// 重装一个没有对象存储的部署：工程与版本照常，资产整体缺席。
	bare := NewService(Deps{
		Store:  f.store,
		Public: f.public,
		Origin: mustOrigin(t),
		Logger: f.service.logger,
		Now:    func() time.Time { return f.now },
	})
	ctx := context.Background()

	if _, _, err := bare.BeginAssetUpload(ctx, testOwner, project.ID, "image/png", 3); !errors.Is(err, ErrAssetUnavailable) {
		t.Errorf("签发 err = %v，期望 ErrAssetUnavailable", err)
	}
	if _, err := bare.ListAssets(ctx, testOwner, project.ID); !errors.Is(err, ErrAssetUnavailable) {
		t.Errorf("列资产 err = %v，期望 ErrAssetUnavailable", err)
	}
	// 工程与版本照常可用。
	if _, _, err := bare.GetVersion(ctx, testOwner, project.ID, version.ID); err != nil {
		t.Errorf("读版本失败: %v", err)
	}
	if _, err := bare.ListVersions(ctx, testOwner, project.ID); err != nil {
		t.Errorf("列版本失败: %v", err)
	}
	// 能力下发如实反映这一项缺席。
	capabilities := bare.Capabilities()
	if capabilities.AssetUploadEnabled {
		t.Error("没有私有桶却报告资产可用")
	}
	if !capabilities.PublishEnabled {
		t.Error("发布了却报告发布不可用")
	}
}
