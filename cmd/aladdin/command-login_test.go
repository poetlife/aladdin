package main

import (
	"errors"
	"io"
	"strings"
	"testing"

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
