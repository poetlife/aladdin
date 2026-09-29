package main

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	galaxyv1 "github.com/poetlife/aladdin/api/gen/aladdin/galaxy/v1"
	"github.com/poetlife/aladdin/internal/galaxy"
)

// 扩展名表是**省事的缺省**，权威是服务端那一个白名单入口。这条测试把两者钉
// 在一起：白名单一旦变动（改取值、去掉某一类），这里先失败，而不是等用户撞上
// "服务端拒绝了命令行自己猜的类型"。
func TestAssetTypeByExtensionMatchesServerWhitelist(t *testing.T) {
	if len(assetTypeByExtension) == 0 {
		t.Fatal("扩展名表为空，推断会全部失败")
	}
	for extension, declared := range assetTypeByExtension {
		normalized, _, err := galaxy.NormalizeAssetType(declared)
		if err != nil {
			t.Errorf("%s 推断出的类型 %q 不在服务端白名单里：%v", extension, declared, err)
			continue
		}
		if normalized != declared {
			t.Errorf("%s 推断出的类型 %q 不是规范形式（服务端归一后是 %q）——"+
				"存进元数据的会是后者，直传上去的却是前者", extension, declared, normalized)
		}
	}
}

// 推断不出来时报用法错误，不替用户猜一个。
func TestInferAssetTypeRejectsUnknownExtension(t *testing.T) {
	for _, name := range []string{"photo.tiff", "noext", "clip.mkv"} {
		_, err := inferAssetType(name)
		var ue *usageError
		if !errors.As(err, &ue) {
			t.Errorf("%s 推断不出来时必须是用法错误，实际：%v", name, err)
		}
	}
}

// 扩展名大小写不该影响推断：一个叫 COVER.PNG 的文件与 cover.png 是同一类。
func TestInferAssetTypeIgnoresExtensionCase(t *testing.T) {
	got, err := inferAssetType("COVER.PNG")
	if err != nil {
		t.Fatalf("大写扩展名应当能推断：%v", err)
	}
	if got != "image/png" {
		t.Fatalf("期望 image/png，实际 %q", got)
	}
}

// 显式给出的类型优先，且不被改写——归一与判定都在服务端。
func TestResolveAssetTypePrefersExplicitFlag(t *testing.T) {
	got, err := resolveAssetType("video/webm", "photo.png")
	if err != nil {
		t.Fatalf("显式给出类型时不应报错：%v", err)
	}
	if got != "video/webm" {
		t.Fatalf("显式给出的类型应当胜出，实际 %q", got)
	}

	got, err = resolveAssetType("", filepath.Join("a", "b", "cover.webp"))
	if err != nil {
		t.Fatalf("没给类型时应当按扩展名推断：%v", err)
	}
	if got != "image/webp" {
		t.Fatalf("期望 image/webp，实际 %q", got)
	}
}

// update 必须改点什么：三项都不给是一次没有任何改动的调用，**在发起请求之前**
// 就该被拦下（与 project update 同一条取向）。
func TestAssetUpdateRequiresAField(t *testing.T) {
	err := executeRoot(t, "galaxy", "asset", "update", "prj_x", "ast_y")
	var ue *usageError
	if !errors.As(err, &ue) {
		t.Fatalf("三项都不给时必须是用法错误，实际：%v", err)
	}
}

// --tag 与 --clear-tags 表达的是同一件事的两种答案（换一组 / 清空），同时给出
// 时没有"哪个说了算"的合理规则，因此在解析阶段就拒。
func TestAssetUpdateTagFlagsAreMutuallyExclusive(t *testing.T) {
	err := executeRoot(t, "galaxy", "asset", "update", "prj_x", "ast_y", "--tag", "a", "--clear-tags")
	if err == nil {
		t.Fatal("同时给出 --tag 与 --clear-tags 应当被拒")
	}
}

// 列表里资产的显示名：有标题用标题，没有回退到文件名，都没有时给一句说明——
// **不是**一条会被当成文件名的空串。
func TestAssetLabelFallsBackToFilename(t *testing.T) {
	cases := []struct {
		name     string
		title    string
		filename string
		want     string
	}{
		{"有标题时用标题", "首页封面", "IMG_2031.png", "首页封面"},
		{"没有标题时回退到文件名", "", "IMG_2031.png", "IMG_2031.png"},
		{"都没有时给一句说明", "", "", "(未命名)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := assetLabel(&galaxyv1.Asset{Title: tc.title, Filename: tc.filename})
			if got != tc.want {
				t.Errorf("assetLabel = %q，期望 %q", got, tc.want)
			}
		})
	}
}

// 备注摘要是**展示用**的：多行压成一行、超长截断并留一个省略号，让"还有内容"
// 看得出来；没超长时不加任何记号。
func TestOneLineSummaryCollapsesAndTruncates(t *testing.T) {
	if got := oneLineSummary("第一行\n第二行", 80); got != "第一行 第二行" {
		t.Errorf("多行摘要 = %q，期望折成一行", got)
	}
	if got := oneLineSummary("短的", 80); got != "短的" {
		t.Errorf("未超长的摘要 = %q，期望原样", got)
	}
	long := strings.Repeat("字", 10)
	if got := oneLineSummary(long, 4); got != "字字字字…" {
		t.Errorf("超长摘要 = %q，期望截断并加省略号", got)
	}
}

// 从清单里按标识找资产：找得到给那一条，找不到给 nil（调用方据此报用法错误，
// 而不是对一个零值资产发起更新）。
func TestFindAsset(t *testing.T) {
	assets := []*galaxyv1.Asset{{Id: "ast_1"}, {Id: "ast_2"}}
	if got := findAsset(assets, "ast_2"); got == nil || got.GetId() != "ast_2" {
		t.Errorf("findAsset = %v，期望 ast_2", got)
	}
	if got := findAsset(assets, "ast_9"); got != nil {
		t.Errorf("findAsset 找到了不存在的资产: %v", got)
	}
}
