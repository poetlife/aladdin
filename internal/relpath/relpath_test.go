package relpath_test

import (
	"strings"
	"testing"

	"github.com/poetlife/aladdin/internal/relpath"
)

// 路径形状是**一处实现、两处消费**（galaxy 文件组的条目、skill 包内路径与来源
// 子路径）。它挡的是同一个东西：让"一条路径"此后只有一种写法，读取侧因此不需要
// 任何规范化与猜测。

func TestValidAccepts(t *testing.T) {
	for _, entryPath := range []string{
		"index.html",
		"docs/guide/intro.md",
		"a/b/c/d/e.txt",
		"~tilde",
		"LICENSE",
		"a-b_c.d",
	} {
		if !relpath.Valid(entryPath) {
			t.Errorf("%q 被判为不合法", entryPath)
		}
	}
}

func TestValidRejects(t *testing.T) {
	cases := map[string]string{
		"空路径":     "",
		"绝对路径":    "/etc/passwd",
		"结尾斜杠":    "a/",
		"空段":      "a//b",
		"当前目录段":   "a/./b",
		"上跳段":     "a/../b",
		"只有一个上跳段": "..",
		"超长":      strings.Repeat("a", relpath.MaxBytes+1),
		"含空格":     "a b",
		"含百分号":    "a%2Eb",
		"含反斜杠":    `a\b`,
		"含查询串分隔符": "a?b",
		"含控制字符":   "a\nb",
		"含冒号":     "a:b",
		// 字符集是 URL 的非保留字符，因此非 ASCII 段一律被拒。这是一条**已知
		// 代价**：上游仓库里一个中文文件名会让那份内容纳管不进来，而错误信息
		// 会点名那个路径。放宽它要同时回答"同一个字的两种 Unicode 写法算不算
		// 两条路径"，而那正是这条规则当初要消掉的问题。
		"非 ASCII 段": "参考/配色.md",
	}
	for name, entryPath := range cases {
		t.Run(name, func(t *testing.T) {
			if relpath.Valid(entryPath) {
				t.Errorf("%q 被判为合法", entryPath)
			}
		})
	}
}

// 长度上限按**字节数**算：它不是给人看的标题，是键与地址的一部分。
func TestValidLengthCountsBytes(t *testing.T) {
	if !relpath.Valid(strings.Repeat("a", relpath.MaxBytes)) {
		t.Errorf("恰好 %d 字节的路径被拒", relpath.MaxBytes)
	}
	if relpath.Valid(strings.Repeat("a", relpath.MaxBytes+1)) {
		t.Errorf("超过 %d 字节的路径被判为合法", relpath.MaxBytes)
	}
}
