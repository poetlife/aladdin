package main

import (
	"errors"
	"io"
	"strings"
	"testing"
)

// 本机构建的二进制不能自更新，且拒绝的理由要说清楚。
//
// 测试里的 version 就是 "dev"（没有 -ldflags 注入），因此这条路径同时证明了
// 一件事：本机构建被拒绝时**不会发起任何网络请求**——它要是发了，这个用例会
// 去访问真实的 GitHub。
func TestUpdateRefusesLocalBuilds(t *testing.T) {
	original := version
	version = "dev"
	t.Cleanup(func() { version = original })

	root := newRootCommand()
	root.SetArgs([]string{"update"})
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)

	err := root.Execute()
	if err == nil {
		t.Fatal("本机构建不该能自更新")
	}
	if !strings.Contains(err.Error(), "不是发布产物") {
		t.Fatalf("拒绝的理由应说明当前版本不是发布产物，实际：%v", err)
	}
	// 这不是参数写错了，因此不该落进用法错误的退出码。
	var ue *usageError
	if errors.As(err, &ue) {
		t.Fatalf("本机构建被拒绝不该算用法错误：%v", err)
	}
}

// 升级是公开命令：它不碰服务端，也不需要凭证。
func TestUpdateIsPublic(t *testing.T) {
	if !isPublicCommand("aladdin update") {
		t.Fatal("升级必须是公开命令，否则它会被要求先登录")
	}
}
