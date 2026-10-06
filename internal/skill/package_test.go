package skill

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// 包契约见 docs/design/skill/onboarding.md 的"一个技能包是什么"。

func TestBuildPackageAcceptsMinimalTree(t *testing.T) {
	pkg, err := BuildPackage([]FetchedFile{
		{Path: "palette.md", Data: []byte("# 色板\n")},
		{Path: ManifestPath, Data: []byte(manifest("mono-color", "单色印刷风出图。"))},
	})
	if err != nil {
		t.Fatalf("合法包被拒: %v", err)
	}
	if pkg.Manifest.Name != "mono-color" {
		t.Errorf("name = %q", pkg.Manifest.Name)
	}
	// 顺序固定：同样的输入必须得到同样的清单。
	if len(pkg.Files) != 2 || pkg.Files[0].Path != ManifestPath {
		t.Errorf("文件顺序 = %v，期望按路径升序", pkg.Files)
	}
}

// 收不收得下由"它是不是文本"决定，**不由"它像不像会被执行"决定**：平台从不执行
// 它。按后者判会得到一条自相矛盾的规则——一份写着"运行 build.sh"的 README 与那个
// build.sh 本身，没有任何理由前者收、后者不收。
//
// 二进制那半边在**取回**那一层被跳掉（见 readRepoArchive 的用例）；这里守的是
// "万一漏了那次跳过，这一层也不能放它进库"。
func TestBuildPackageAcceptsScriptsAndBinaryIsRejected(t *testing.T) {
	script := "#!/bin/sh\nset -eu\necho 出图\n"
	pkg, err := BuildPackage([]FetchedFile{
		{Path: ManifestPath, Data: []byte(manifest("mono-color", "出图。"))},
		{Path: "scripts/render.sh", Data: []byte(script)},
	})
	if err != nil {
		t.Fatalf("脚本被拒: %v", err)
	}
	if got, _ := fileData(pkg, "scripts/render.sh"); got != script {
		t.Errorf("脚本内容被改动了: %q", got)
	}

	// PNG 的前几个字节：含 NUL 且不是合法 UTF-8。
	png := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00, 0x00, 0x0d}
	if _, err := BuildPackage([]FetchedFile{
		{Path: ManifestPath, Data: []byte(manifest("mono-color", "出图。"))},
		{Path: "cover.png", Data: png},
	}); !errors.Is(err, ErrPackageInvalid) || !strings.Contains(err.Error(), "cover.png") {
		t.Errorf("二进制 err = %v，期望点名 cover.png 的 ErrPackageInvalid", err)
	}
}

func TestBuildPackageRequiresManifest(t *testing.T) {
	_, err := BuildPackage([]FetchedFile{{Path: "readme.md", Data: []byte("# 没清单\n")}})
	if !errors.Is(err, ErrPackageInvalid) || !strings.Contains(err.Error(), ManifestPath) {
		t.Errorf("缺清单 err = %v，期望点名 %s", err, ManifestPath)
	}
}

func TestBuildPackageRejectsPaths(t *testing.T) {
	cases := map[string]string{
		"上跳段":     "a/../b.md",
		"绝对路径":    "/b.md",
		"空段":      "a//b.md",
		"结尾斜杠":    "a/",
		"含空格":     "a b.md",
		"非 ASCII": "参考/配色.md",
	}
	for name, path := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := BuildPackage([]FetchedFile{
				{Path: ManifestPath, Data: []byte(manifest("mono-color", "出图。"))},
				{Path: path, Data: []byte("x\n")},
			})
			if !errors.Is(err, ErrPackageInvalid) {
				t.Errorf("路径 %q 被接受了（err = %v）", path, err)
			}
		})
	}
}

// 只差大小写的两条路径在大小写不敏感的文件系统上是同一个文件，而包会被取到那样的
// 文件系统上。挡在纳管这一步，而不是等到下发时。
func TestBuildPackageRejectsCaseInsensitiveDuplicates(t *testing.T) {
	_, err := BuildPackage([]FetchedFile{
		{Path: ManifestPath, Data: []byte(manifest("mono-color", "出图。"))},
		{Path: "Palette.md", Data: []byte("a\n")},
		{Path: "palette.md", Data: []byte("b\n")},
	})
	if !errors.Is(err, ErrPackageInvalid) || !strings.Contains(err.Error(), "大小写") {
		t.Errorf("大小写重复 err = %v", err)
	}

	_, err = BuildPackage([]FetchedFile{
		{Path: ManifestPath, Data: []byte(manifest("mono-color", "出图。"))},
		{Path: "palette.md", Data: []byte("a\n")},
		{Path: "palette.md", Data: []byte("b\n")},
	})
	if !errors.Is(err, ErrPackageInvalid) {
		t.Errorf("完全相同的路径 err = %v", err)
	}
}

func TestBuildPackageEnforcesLimits(t *testing.T) {
	oversized := strings.Repeat("a", MaxFileBytes+1)
	if _, err := BuildPackage([]FetchedFile{
		{Path: ManifestPath, Data: []byte(manifest("mono-color", "出图。"))},
		{Path: "big.md", Data: []byte(oversized)},
	}); !errors.Is(err, ErrPackageInvalid) || !strings.Contains(err.Error(), "单文件上限") {
		t.Errorf("单文件超限 err = %v", err)
	}

	files := []FetchedFile{{Path: ManifestPath, Data: []byte(manifest("mono-color", "出图。"))}}
	for i := 0; i < MaxFiles; i++ {
		files = append(files, FetchedFile{Path: pageName(i), Data: []byte("x\n")})
	}
	if _, err := BuildPackage(files); !errors.Is(err, ErrPackageInvalid) || !strings.Contains(err.Error(), "文件数") {
		t.Errorf("文件数超限 err = %v", err)
	}
}

func TestParseManifest(t *testing.T) {
	got, err := ParseManifest([]byte("---\nname: mono-color\ndescription: >\n  单色印刷风出图。\n  当用户要一张克制的海报时使用。\nextra: 忽略我\n---\n\n正文\n"))
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if got.Name != "mono-color" {
		t.Errorf("name = %q", got.Name)
	}
	if !strings.Contains(got.Description, "当用户要一张克制的海报时使用") {
		t.Errorf("description = %q", got.Description)
	}
}

func TestParseManifestRejects(t *testing.T) {
	cases := map[string]string{
		"没有 frontmatter": "正文\n",
		"没有结束标记":         "---\nname: x\ndescription: y\n",
		"缺 name":         "---\ndescription: y\n---\n",
		"缺 description":  "---\nname: x\n---\n",
		"name 大写":        "---\nname: MonoColor\ndescription: y\n---\n",
		"name 含下划线":      "---\nname: mono_color\ndescription: y\n---\n",
		"description 超长": "---\nname: x\ndescription: " + strings.Repeat("字", MaxDescriptionRunes+1) + "\n---\n",
		"不是合法 YAML":      "---\nname: [未闭合\n---\n",
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseManifest([]byte(data)); !errors.Is(err, ErrPackageInvalid) {
				t.Errorf("err = %v，期望 ErrPackageInvalid", err)
			}
		})
	}
}

// 摘要一类的取值只由一处实现（objectstore.ContentDigest），这里断言键的形状——
// 它不带技能标识、也不带版本标识，因此同一份字节在整个目录里只有一个对象。
func TestContentObjectKeyIsContentAddressed(t *testing.T) {
	digest := "a" + strings.Repeat("0", 63)
	if got := ContentObjectKey(digest); got != "skills/text/"+digest {
		t.Errorf("对象键 = %q", got)
	}
}

func fileData(pkg Package, path string) (string, bool) {
	for _, file := range pkg.Files {
		if file.Path == path {
			return string(file.Data), true
		}
	}
	return "", false
}

func pageName(index int) string {
	return fmt.Sprintf("pages/page-%03d.md", index)
}
