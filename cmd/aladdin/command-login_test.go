package main

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	identityv1 "github.com/poetlife/aladdin/api/gen/aladdin/identity/v1"
	"github.com/poetlife/aladdin/internal/auth"
)

// 非交互式环境下不给 --token 时，登录必须**立刻以用法错误结束**。
//
// 关键约定：不能让脚本挂在那里等一次永远不会到来的批准。测试进程的 stdin
// 不是终端，因此这里正处在该路径上。
func TestLoginRequiresTokenWhenNotInteractive(t *testing.T) {
	root := newRootCommand()
	root.SetArgs([]string{"login"})
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)

	err := root.Execute()

	var ue *usageError
	if !errors.As(err, &ue) {
		t.Fatalf("非交互式下不给 --token 应以用法错误拦下，实际：%v", err)
	}
	if !strings.Contains(err.Error(), auth.EnvToken) {
		t.Fatalf("提示里应指出机器凭证从哪来，实际：%v", err)
	}
}

// 登录是公开命令：它要在还没有任何凭证的时候可用。
func TestLoginIsPublic(t *testing.T) {
	if !isPublicCommand("aladdin login") {
		t.Fatal("登录必须是公开命令，否则未登录时无法执行")
	}
}

// 机器凭证按 参数 > 环境变量 解析：脚本里只设 ALADDIN_TOKEN 必须能生效，
// 而不是收到一句"请通过 --token 或 ALADDIN_TOKEN 提供"。
func TestLoginMachineTokenPrefersFlagThenEnv(t *testing.T) {
	original := flags.token
	t.Cleanup(func() { flags.token = original })

	t.Setenv(auth.EnvToken, "from-env")
	flags.token = ""
	if got := machineToken(); got != "from-env" {
		t.Fatalf("没给参数时应取环境变量，实际 %q", got)
	}

	flags.token = "from-flag"
	if got := machineToken(); got != "from-flag" {
		t.Fatalf("给了参数时应取参数，实际 %q", got)
	}
}

// 设备码登录不接受 --scope。
//
// 静默忽略它比报错更危险：使用者会以为自己按最小权限登录了，实际拿到的是
// 批准者主体的完整默认作用域。
func TestLoginDevicePathRejectsScope(t *testing.T) {
	original := flags.scope
	t.Cleanup(func() { flags.scope = original })

	root := newRootCommand()
	root.SetArgs([]string{"login", "--scope", "tenant/acme"})
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)

	err := root.Execute()

	var ue *usageError
	if !errors.As(err, &ue) {
		t.Fatalf("设备码登录给 --scope 应以用法错误拦下，实际：%v", err)
	}
	if !strings.Contains(err.Error(), "--scope") {
		t.Fatalf("提示里应点明是 --scope，实际：%v", err)
	}
}

// 登录提示写 stderr：写 stdout 会污染 --output json，也会让与口令同级的短码
// 进入脚本的管道。提示里还必须带有效期——不说，人就会对着失效的短码反复输入。
func TestDeviceLoginPromptGoesToStderr(t *testing.T) {
	cmd := newLoginCommand()
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	printDeviceLoginPrompt(cmd, &identityv1.StartDeviceLoginResponse{
		VerificationUri: "https://example.test/device",
		UserCode:        "BCDF-GHJK",
	}, time.Date(2026, 9, 28, 15, 4, 0, 0, time.Local))

	if stdout.Len() != 0 {
		t.Fatalf("stdout 上不该有提示，实际：%q", stdout.String())
	}
	for _, want := range []string{"https://example.test/device", "BCDF-GHJK", "2026-09-28 15:04"} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("stderr 上的提示里应有 %q，实际：%q", want, stderr.String())
		}
	}
}
