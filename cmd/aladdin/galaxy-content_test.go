package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// 正文按字节原样读出：不补换行、不去尾空白。它不仅会被存进草稿，也会被拿来
// 校验并最终发布出去，任何"顺手规整"都会让用户发布的东西与手上的文件不一致。
func TestReadContentFromFileIsByteFaithful(t *testing.T) {
	path := filepath.Join(t.TempDir(), "page.html")
	want := "<!doctype html>\n<html><body>  <p>x</p></body></html>"
	if err := os.WriteFile(path, []byte(want), 0o600); err != nil {
		t.Fatalf("准备文件失败：%v", err)
	}

	got, err := readContent(path)
	if err != nil {
		t.Fatalf("读取正文失败：%v", err)
	}
	if got != want {
		t.Fatalf("正文应逐字读出，期望 %q，实际 %q", want, got)
	}
}

// '-' 表示标准输入。它与 --file 共用同一个入口，因此"管道的这一头"与"文件那
// 一头"进到服务端的一定是同一份字节。
func TestReadContentFromStdin(t *testing.T) {
	want := "<html>from stdin</html>"
	f, err := os.CreateTemp(t.TempDir(), "stdin")
	if err != nil {
		t.Fatalf("准备标准输入失败：%v", err)
	}
	if _, err := f.WriteString(want); err != nil {
		t.Fatalf("写入标准输入失败：%v", err)
	}
	if _, err := f.Seek(0, 0); err != nil {
		t.Fatalf("回绕标准输入失败：%v", err)
	}

	original := os.Stdin
	os.Stdin = f
	t.Cleanup(func() { os.Stdin = original })

	got, err := readContent(contentStdinPath)
	if err != nil {
		t.Fatalf("从标准输入读取失败：%v", err)
	}
	if got != want {
		t.Fatalf("期望 %q，实际 %q", want, got)
	}
}

// 读不到、以及根本没给来源，都按**用法错误**处理：它们与内部故障是两回事，
// 脚本要能据此分支。
func TestReadContentFailuresAreUsageErrors(t *testing.T) {
	cases := map[string]string{
		"没给来源":  "",
		"文件不存在": filepath.Join(t.TempDir(), "missing.html"),
		"目录":    t.TempDir(),
	}
	for name, path := range cases {
		_, err := readContent(path)
		var ue *usageError
		if !errors.As(err, &ue) {
			t.Errorf("%s：应当是用法错误，实际：%v", name, err)
		}
	}
}
