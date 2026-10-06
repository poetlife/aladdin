package skill

import (
	"archive/tar"
	"bytes"
	"errors"
	"strings"
	"testing"
)

// 压缩包解析是**路径逃逸、链接、超限**三类问题唯一挡得住的地方：放过去就到了包
// 校验那一层，而那一层看到的已经是一组"看着正常的路径"。

const archiveRoot = testOwner + "-" + testRepo + "-" + testSHA

func TestReadRepoArchiveStripsRootAndKeepsSubPath(t *testing.T) {
	archive := tarGz(t, archiveRoot, []tarEntry{
		regular("README.md", "仓库说明\n"),
		regular("skills/foo/SKILL.md", "清单\n"),
		regular("skills/foo/palette.md", "色板\n"),
		regular("skills/bar/SKILL.md", "别人的\n"),
	})
	files, err := readRepoArchive(bytes.NewReader(archive), "skills/foo")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	got := archiveFiles(files)
	if len(got) != 2 {
		t.Fatalf("取到 %d 条，期望 2 条: %v", len(got), got)
	}
	if got["SKILL.md"] != "清单\n" || got["palette.md"] != "色板\n" {
		t.Errorf("内容不对: %v", got)
	}
}

func TestReadRepoArchiveWithoutSubPathKeepsEverything(t *testing.T) {
	archive := tarGz(t, archiveRoot, []tarEntry{
		regular("SKILL.md", "清单\n"),
		regular("a/b/c.md", "深一层\n"),
	})
	files, err := readRepoArchive(bytes.NewReader(archive), "")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	got := archiveFiles(files)
	if got["SKILL.md"] != "清单\n" || got["a/b/c.md"] != "深一层\n" {
		t.Errorf("内容不对: %v", got)
	}
}

// 目录条目不是文件，静默跳过；而**链接直接拒绝**——包契约要求"一棵树"，链接把它
// 变成一张图，静默跳过会让一个依赖链接的仓库纳管进来少几个文件而没人看得出来。
func TestReadRepoArchiveRejectsLinks(t *testing.T) {
	for name, entry := range map[string]tarEntry{
		"符号链接": {name: "link.md", typeflag: tar.TypeSymlink, linkname: "/etc/passwd"},
		"硬链接":  {name: "hard.md", typeflag: tar.TypeLink, linkname: "a/b.md"},
	} {
		t.Run(name, func(t *testing.T) {
			archive := tarGz(t, archiveRoot, []tarEntry{
				regular("SKILL.md", "清单\n"),
				entry,
			})
			_, err := readRepoArchive(bytes.NewReader(archive), "")
			if !errors.Is(err, ErrPackageInvalid) {
				t.Errorf("err = %v，期望 ErrPackageInvalid", err)
			}
		})
	}
}

func TestReadRepoArchiveRejectsSpecialEntries(t *testing.T) {
	archive := tarGz(t, archiveRoot, []tarEntry{
		{name: "pipe", typeflag: tar.TypeFifo},
	})
	if _, err := readRepoArchive(bytes.NewReader(archive), ""); !errors.Is(err, ErrPackageInvalid) {
		t.Errorf("err = %v，期望 ErrPackageInvalid", err)
	}
}

// `a/../../etc` 这类取值在 Clean 之后才现形，因此判定必须在 Clean 之后做。
func TestReadRepoArchiveRejectsEscapingPaths(t *testing.T) {
	for name, entryName := range map[string]string{
		"上跳出归档根": "../../etc/passwd",
		// 空归档根 + 以 `/` 起的条目名，得到一条真正的绝对路径条目。
		"绝对路径": "etc/passwd",
	} {
		t.Run(name, func(t *testing.T) {
			root := archiveRoot
			if name == "绝对路径" {
				root = ""
			}
			archive := tarGz(t, root, []tarEntry{regular(entryName, "evil")})
			if _, err := readRepoArchive(bytes.NewReader(archive), ""); !errors.Is(err, ErrPackageInvalid) {
				t.Errorf("路径 %q err = %v，期望 ErrPackageInvalid", entryName, err)
			}
		})
	}
}

// 解压累计有上限：上限的意义是让一次纳管的最坏内存占用是有界的，而不是"看看远端
// 有多大"。
func TestReadRepoArchiveEnforcesExpandedLimit(t *testing.T) {
	var entries []tarEntry
	chunk := strings.Repeat("a", MaxExpandedBytes/8+1)
	for i := 0; i < 9; i++ {
		entries = append(entries, regular(pageName(i), chunk))
	}
	archive := tarGz(t, archiveRoot, entries)
	if _, err := readRepoArchive(bytes.NewReader(archive), ""); !errors.Is(err, ErrRemoteUnavailable) {
		t.Errorf("超限 err = %v，期望 ErrRemoteUnavailable", err)
	}
}

func TestReadRepoArchiveRejectsGarbage(t *testing.T) {
	if _, err := readRepoArchive(bytes.NewReader([]byte("这不是压缩包")), ""); !errors.Is(err, ErrRemoteUnavailable) {
		t.Errorf("err = %v，期望 ErrRemoteUnavailable", err)
	}
}
