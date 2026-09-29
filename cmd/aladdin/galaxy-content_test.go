package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	galaxyv1 "github.com/poetlife/aladdin/api/gen/aladdin/galaxy/v1"
	"github.com/poetlife/aladdin/internal/galaxy"
)

// 目录按**整组**读出来，路径以 `/` 分隔、与目录层级一致。
func TestReadFileSetKeepsByteFaithful(t *testing.T) {
	dir := t.TempDir()
	want := []byte("<!doctype html>\n<html><body>  <p>x</p></body></html>")
	writeTestFile(t, dir, "index.html", want)
	writeTestFile(t, dir, "assets/app.js", []byte("console.log(1)"))

	files, err := readFileSet(dir, galaxy.SlotSite)
	if err != nil {
		t.Fatalf("读取目录失败：%v", err)
	}
	if len(files) != 2 {
		t.Fatalf("文件数 = %d，期望 2：%+v", len(files), files)
	}
	byPath := map[string][]byte{}
	for _, file := range files {
		byPath[file.Path] = file.Data
	}
	// 字节逐字读出：不补换行、不去尾空白。
	if !bytes.Equal(byPath["index.html"], want) {
		t.Errorf("index.html = %q，期望 %q", byPath["index.html"], want)
	}
	if string(byPath["assets/app.js"]) != "console.log(1)" {
		t.Errorf("assets/app.js = %q", byPath["assets/app.js"])
	}
}

// 隐藏文件被跳过——构建产物的目录里常有它们，而它们不是站点的一部分。
func TestReadFileSetSkipsHiddenFiles(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, dir, "index.html", []byte("<p>x</p>"))
	writeTestFile(t, dir, ".DS_Store", []byte("junk"))
	writeTestFile(t, dir, ".git/config", []byte("junk"))

	files, err := readFileSet(dir, galaxy.SlotSite)
	if err != nil {
		t.Fatalf("读取目录失败：%v", err)
	}
	if len(files) != 1 || files[0].Path != "index.html" {
		t.Errorf("文件集 = %+v，期望只剩 index.html", files)
	}
}

// 白名单外的扩展名**被拒**而不是被静默丢掉：静默丢掉会让目录里少一个文件而
// 用户毫无察觉。
func TestReadFileSetRejectsUnknownExtensions(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, dir, "index.html", []byte("<p>x</p>"))
	writeTestFile(t, dir, "notes.xyz", []byte("?"))
	writeTestFile(t, dir, "app.js.map", []byte("{}"))

	_, err := readFileSet(dir, galaxy.SlotSite)
	if err == nil {
		t.Fatal("白名单外的扩展名被接受了")
	}
	var ue *usageError
	if !errors.As(err, &ue) {
		t.Errorf("应当是用法错误，实际：%v", err)
	}
}

// **文本与资产的分界与服务端共用同一张表**：产品文件在 docs 形态下是资产、
// 在 static 形态下也是资产；而 .html 在 docs 下不是合法文本。
func TestReadFileSetUsesServerWhitelist(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, dir, "index.md", []byte("# 首页"))
	writeTestFile(t, dir, "logo.png", []byte("png"))

	files, err := readFileSet(dir, galaxy.SlotDocs)
	if err != nil {
		t.Fatalf("读取目录失败：%v", err)
	}
	if len(files) != 2 {
		t.Fatalf("文件数 = %d，期望 2", len(files))
	}
	// docs 形态下 `.html` 不在白名单里，因此它不是文本条目。
	if galaxy.IsTextPath(galaxy.SlotDocs, "page.html") {
		t.Error("docs 形态把 .html 当成了文本")
	}
}

// 目录不存在、不是目录、符号链接：都是**用法错误**，与内部故障分开。
func TestReadFileSetFailuresAreUsageErrors(t *testing.T) {
	base := t.TempDir()
	file := filepath.Join(base, "a.html")
	writeTestFile(t, base, "a.html", []byte("<p>x</p>"))
	link := filepath.Join(base, "link.html")

	dirWithLink := t.TempDir()
	writeTestFile(t, dirWithLink, "index.html", []byte("<p>x</p>"))
	if err := os.Symlink(file, filepath.Join(dirWithLink, "linked.html")); err != nil {
		t.Skipf("这个平台上建不了符号链接：%v", err)
	}

	cases := map[string]string{
		"目录不存在": filepath.Join(base, "missing"),
		"路径是文件": file,
	}
	for name, path := range cases {
		if _, err := readFileSet(path, galaxy.SlotSite); !isUsageError(err) {
			t.Errorf("%s：应当是用法错误，实际：%v", name, err)
		}
	}
	if _, err := readFileSet(dirWithLink, galaxy.SlotSite); !isUsageError(err) {
		t.Errorf("符号链接：应当是用法错误，实际：%v", err)
	}
	_ = link
}

// writeFileSet 按条目路径写出目录，层级与文件名逐字对应。
func TestWriteFileSetRoundTrips(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "out")
	entries := []*galaxyv1.FileEntry{
		{Path: "index.html", Source: &galaxyv1.FileEntry_Digest{Digest: "aa"}},
		{Path: "assets/app.js", Source: &galaxyv1.FileEntry_Digest{Digest: "bb"}},
	}
	contents := map[string][]byte{
		"index.html":    []byte("<p>x</p>"),
		"assets/app.js": []byte("console.log(1)"),
	}
	err := writeFileSet(dir, entries, func(entry *galaxyv1.FileEntry) ([]byte, error) {
		return contents[entry.GetPath()], nil
	})
	if err != nil {
		t.Fatalf("写出目录失败：%v", err)
	}
	for entryPath, want := range contents {
		got, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(entryPath)))
		if err != nil {
			t.Fatalf("读回 %s 失败：%v", entryPath, err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s = %q，期望 %q", entryPath, got, want)
		}
	}
}

// 条目路径落在目录之外时**拒绝写出**：写文件是不可逆的，宁可拒一次。
func TestWriteFileSetRefusesEscapingPaths(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "out")
	entries := []*galaxyv1.FileEntry{
		{Path: "../escaped.html", Source: &galaxyv1.FileEntry_Digest{Digest: "aa"}},
	}
	err := writeFileSet(dir, entries, func(*galaxyv1.FileEntry) ([]byte, error) {
		return []byte("x"), nil
	})
	if !isUsageError(err) {
		t.Errorf("应当是用法错误，实际：%v", err)
	}
}

// **资产类型表必须与服务端白名单逐条一致**：表里的每个取值都要能通过服务端
// 那个唯一的类型入口。白名单一旦变动，这里先失败。
func TestAssetTypeTableMatchesServerWhitelist(t *testing.T) {
	for extension, declared := range assetTypeByExtension {
		if _, _, err := galaxy.NormalizeAssetType(declared); err != nil {
			t.Errorf("%s → %q 不在服务端白名单里：%v", extension, declared, err)
		}
	}
}

// **判定文本的那张表就是服务端那一张**：这里只确认取值的来源是同一个入口，
// 不另写一份对照表。
func TestTextClassificationComesFromServer(t *testing.T) {
	if !galaxy.IsTextPath(galaxy.SlotSite, "index.html") {
		t.Error("static 的 index.html 应当是文本")
	}
	if galaxy.IsTextPath(galaxy.SlotDocs, "index.html") {
		t.Error("docs 的 .html 不该是文本")
	}
}

func isUsageError(err error) bool {
	var ue *usageError
	return errors.As(err, &ue)
}

func writeTestFile(t *testing.T, dir, relative string, data []byte) {
	t.Helper()
	target := filepath.Join(dir, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatalf("建目录失败：%v", err)
	}
	if err := os.WriteFile(target, data, 0o644); err != nil {
		t.Fatalf("写文件失败：%v", err)
	}
}
