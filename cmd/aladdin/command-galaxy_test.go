package main

import (
	"errors"
	"io"
	"testing"

	"github.com/spf13/cobra"
)

func executeRoot(t *testing.T, args ...string) error {
	t.Helper()
	root := newRootCommand()
	root.SetArgs(args)
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	return root.Execute()
}

// 危险操作的集合是**刻意选出来的**，不是"所有会改东西的命令"。发布在内，
// 撤回发布在外（见 docs/design/galaxy/cli.md）。这条测试把它钉住：多一个少
// 一个都意味着有人无意中改动了脚本要面对的确认行为。
func TestGalaxyDangerousCommandSet(t *testing.T) {
	want := map[string]bool{
		"aladdin galaxy project delete": true,
		"aladdin galaxy version delete": true,
		"aladdin galaxy asset delete":   true,
		"aladdin galaxy publish":        true,
	}
	got := map[string]bool{}
	var walk func(cmd *cobra.Command)
	walk = func(cmd *cobra.Command) {
		if isDangerous(cmd) {
			got[cmd.CommandPath()] = true
		}
		for _, child := range cmd.Commands() {
			walk(child)
		}
	}
	root := newRootCommand()
	galaxyCmd, _, err := root.Find([]string{"galaxy"})
	if err != nil {
		t.Fatalf("找不到 galaxy 命令：%v", err)
	}
	walk(galaxyCmd)

	for path := range want {
		if !got[path] {
			t.Errorf("%q 应当是危险操作（交互式二次确认、非交互式必须 --yes）", path)
		}
	}
	for path := range got {
		if !want[path] {
			t.Errorf("%q 不应当被标成危险操作", path)
		}
	}
}

// 非交互式环境下，危险操作不给 --yes 必须**在发起任何请求之前**就失败，
// 而不是跑完再说。测试进程的 stdin 不是终端，正处在这条路径上。
func TestGalaxyDangerousCommandsRequireYesWhenNotInteractive(t *testing.T) {
	cases := [][]string{
		{"galaxy", "project", "delete", "prj_1"},
		{"galaxy", "version", "delete", "prj_1", "ver_1"},
		{"galaxy", "asset", "delete", "prj_1", "ast_1"},
		{"galaxy", "publish", "prj_1", "ver_1"},
	}
	for _, args := range cases {
		err := executeRoot(t, args...)
		var ue *usageError
		if !errors.As(err, &ue) {
			t.Errorf("%v：非交互式下应当以用法错误拦下，实际：%v", args, err)
		}
	}
}

// 位置参数的个数不对，落到"用法错误"这一个退出码，而不是未分类失败。
// 这些用例都在参数校验阶段结束，不会发起任何请求。
func TestGalaxyPositionalArgsAreUsageErrors(t *testing.T) {
	cases := [][]string{
		{"galaxy", "project", "get"},
		{"galaxy", "project", "get", "prj_1", "多出来的"},
		{"galaxy", "project", "delete"},
		{"galaxy", "version", "get", "prj_1"},
		{"galaxy", "version", "get", "prj_1", "ver_1", "多出来的"},
		{"galaxy", "asset", "list", "prj_1", "多出来的"},
		{"galaxy", "publish", "prj_1"},
		{"galaxy", "unpublish"},
	}
	for _, args := range cases {
		err := executeRoot(t, args...)
		var ue *usageError
		if !errors.As(err, &ue) {
			t.Errorf("%v：应当是用法错误，实际：%v", args, err)
		}
	}
}

// 必填的取值缺失时同样在发起请求之前以用法错误结束。
//
// 工程名称不在此列：它可以留空（与网页端一致），因此 create 不带任何标志是
// 一次合法的调用——只是没有名字。
func TestGalaxyMissingRequiredInputAreUsageErrors(t *testing.T) {
	cases := [][]string{
		{"galaxy", "draft", "save", "prj_1"},
		{"galaxy", "validate", "prj_1"},
		{"galaxy", "project", "update", "prj_1"},
	}
	for _, args := range cases {
		err := executeRoot(t, args...)
		var ue *usageError
		if !errors.As(err, &ue) {
			t.Errorf("%v：应当是用法错误，实际：%v", args, err)
		}
	}
}

// 命令树必须整体通过"三选一"检查：声明权限、标记只需认证、或进公开白名单。
// checkCommands 每次执行时都会跑，这里再固定一次，好让漏声明在 go test 里就
// 暴露，而不是等到某次手工执行。
func TestGalaxyCommandsPassCoverageCheck(t *testing.T) {
	if err := checkCommands(newRootCommand()); err != nil {
		t.Fatalf("命令树未通过覆盖检查：%v", err)
	}
}

// 子命令都得挂在 galaxy 下：多出一个平级的顶层命令会悄悄扩大命令行的面。
func TestGalaxyCommandTreeShape(t *testing.T) {
	root := newRootCommand()
	galaxyCmd, _, err := root.Find([]string{"galaxy"})
	if err != nil {
		t.Fatalf("找不到 galaxy 命令：%v", err)
	}
	got := map[string]bool{}
	for _, child := range galaxyCmd.Commands() {
		got[child.Name()] = true
	}
	for _, name := range []string{
		"capabilities", "project", "draft", "version", "validate", "asset", "publish", "unpublish",
	} {
		if !got[name] {
			t.Errorf("galaxy 下缺少子命令 %q", name)
		}
	}
	if len(got) != 8 {
		t.Errorf("galaxy 下子命令数量变了：期望 8，实际 %d", len(got))
	}
}
