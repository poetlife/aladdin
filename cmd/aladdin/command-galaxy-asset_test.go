package main

import (
	"errors"
	"path/filepath"
	"testing"

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
