package skill

import (
	"errors"
	"strings"
	"testing"
)

// 目录树是"要取哪些字节、跳过哪些"的**唯一决策点**：它之后每一条被选中的条目都会
// 真去发一次请求，因此选错一条就是白取一次、漏掉一条就是包少一个文件。

func blob(path string, size int64) repoTreeEntry {
	return repoTreeEntry{Path: path, Mode: "100644", Type: "blob", Size: size, SHA: "sha-" + path}
}

func tree(path string) repoTreeEntry {
	return repoTreeEntry{Path: path, Mode: "040000", Type: "tree"}
}

func plannedPaths(plan repoPlan) []string {
	paths := make([]string, 0, len(plan.Files))
	for _, file := range plan.Files {
		paths = append(paths, file.Path)
	}
	return paths
}

func TestPlanRepoTreeKeepsBlobsAndSkipsOversized(t *testing.T) {
	plan, err := planRepoTree([]repoTreeEntry{
		tree("examples"),
		blob("SKILL.md", 100),
		blob("examples/a.png", 7<<20),
		blob("palette.md", 28),
	}, "")
	if err != nil {
		t.Fatalf("计划失败: %v", err)
	}
	got := plannedPaths(plan)
	if len(got) != 2 || got[0] != "SKILL.md" || got[1] != "palette.md" {
		t.Errorf("要取的条目 = %v，期望两份文本且按路径排序", got)
	}
	if len(plan.Skipped) != 1 || plan.Skipped[0] != "examples/a.png" {
		t.Errorf("跳过 = %v，期望那张超限的图", plan.Skipped)
	}
}

// 子路径之内的才收，且要把它那一段剥掉。
func TestPlanRepoTreeAppliesSubPath(t *testing.T) {
	plan, err := planRepoTree([]repoTreeEntry{
		blob("README.md", 10),
		blob("skills/foo/SKILL.md", 20),
		blob("skills/foo/deep/a.md", 30),
		blob("skills/bar/SKILL.md", 40),
	}, "skills/foo")
	if err != nil {
		t.Fatalf("计划失败: %v", err)
	}
	got := plannedPaths(plan)
	if len(got) != 2 || got[0] != "SKILL.md" || got[1] != "deep/a.md" {
		t.Errorf("要取的条目 = %v", got)
	}
}

// **链接与子模块直接拒绝。** 包契约要求"一棵自足的树"，而两者都把树变成一张指向
// 别处的图——跳过会让一个依赖链接的仓库纳管进来少几个文件而没人看得出来。
func TestPlanRepoTreeRejectsLinksAndSubmodules(t *testing.T) {
	cases := map[string]repoTreeEntry{
		"符号链接": {Path: "link.md", Mode: gitModeSymlink, Type: "blob", Size: 12, SHA: "s"},
		"子模块":  {Path: "vendor", Mode: gitModeSubmodule, Type: "commit", SHA: "s"},
	}
	for name, entry := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := planRepoTree([]repoTreeEntry{blob("SKILL.md", 1), entry}, "")
			if !errors.Is(err, ErrPackageInvalid) {
				t.Errorf("err = %v，期望 ErrPackageInvalid", err)
			}
		})
	}
}

// 顺序固定：同样的一棵树在任何一次、任何一个实现里给出同样的清单。
func TestPlanRepoTreeOrderIsDeterministic(t *testing.T) {
	entries := []repoTreeEntry{
		blob("z.md", 1), blob("a.md", 1), blob("m/zz.md", 1), blob("m/aa.md", 1),
	}
	first, err := planRepoTree(entries, "")
	if err != nil {
		t.Fatalf("计划失败: %v", err)
	}
	second, err := planRepoTree(entries, "")
	if err != nil {
		t.Fatalf("计划失败: %v", err)
	}
	if strings.Join(plannedPaths(first), ",") != strings.Join(plannedPaths(second), ",") {
		t.Error("两次计划的顺序不同")
	}
	if got := plannedPaths(first); got[0] != "a.md" || got[3] != "z.md" {
		t.Errorf("顺序 = %v", got)
	}
}
