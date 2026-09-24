package main

import (
	"errors"
	"io"
	"testing"

	"github.com/spf13/cobra"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/poetlife/aladdin/internal/auth"
)

// TestCheckCommandsRequiresDeclaration 确保新增命令必须表态：
// 要么声明权限码，要么进公开白名单。
func TestCheckCommandsRequiresDeclaration(t *testing.T) {
	root := newRootCommand()
	if err := checkCommands(root); err != nil {
		t.Fatalf("内置命令集应全部有声明，实际: %v", err)
	}
}

func TestCheckCommandsCatchesUndeclared(t *testing.T) {
	root := newRootCommand()
	root.AddCommand(undeclaredTestCommand())
	if err := checkCommands(root); err == nil {
		t.Error("未声明权限的命令应被 checkCommands 捕获")
	}
}

func TestExitCodeFor(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{"无错误", nil, exitOK},
		{"缺少凭证", auth.ErrNoCredential, exitUnauthenticated},
		{"凭证文件权限过宽", auth.ErrCredentialFileInsecure, exitUnauthenticated},
		{"未认证", status.Error(codes.Unauthenticated, "x"), exitUnauthenticated},
		{"权限不足", status.Error(codes.PermissionDenied, "x"), exitPermissionDenied},
		{"服务不可用", status.Error(codes.Unavailable, "x"), exitUnavailable},
		{"超时", status.Error(codes.DeadlineExceeded, "x"), exitUnavailable},
		{"参数非法", status.Error(codes.InvalidArgument, "x"), exitUsage},
		{"前置条件不满足", status.Error(codes.FailedPrecondition, "x"), exitUsage},
		{"内部错误", status.Error(codes.Internal, "x"), exitFailure},
		{"非 grpc 错误", errors.New("boom"), exitFailure},
	}

	seen := map[int]string{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := exitCodeFor(tt.err)
			if got != tt.want {
				t.Errorf("exitCodeFor(%v) = %d, want %d", tt.err, got, tt.want)
			}
		})
	}

	// 四类失败必须互不相同，否则脚本无法据此分支。
	for _, tc := range []struct {
		name string
		code int
	}{
		{"用法错误", exitUsage},
		{"未认证", exitUnauthenticated},
		{"权限不足", exitPermissionDenied},
		{"服务不可用", exitUnavailable},
	} {
		if prev, ok := seen[tc.code]; ok {
			t.Errorf("退出码 %d 被 %s 与 %s 共用，脚本将无法区分", tc.code, prev, tc.name)
		}
		seen[tc.code] = tc.name
		if tc.code == exitOK {
			t.Errorf("%s 的退出码不能是 0", tc.name)
		}
	}
}

// undeclaredTestCommand 模拟一个漏声明权限的新命令。
func undeclaredTestCommand() *cobra.Command {
	return &cobra.Command{
		Use: "undeclared-test",
		RunE: func(*cobra.Command, []string) error {
			return nil
		},
	}
}

// TestDangerousCommandRequiresYes 覆盖危险操作在非交互式环境下的门禁。
//
// 关键约定：非交互式环境下**不默认放行**，也不弹一个永远等不到回答的确认，
// 而是要求显式传入 --yes。测试进程的 stdin 不是终端，因此这里正处在该路径上。
func TestDangerousCommandRequiresYes(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantYes bool
	}{
		{"未传 --yes 应被拒绝", []string{"role", "assign", "--subject", "u1", "--role", "viewer"}, false},
		{"传入 --yes 后放行到参数校验之后的阶段", []string{"role", "assign", "--subject", "u1", "--role", "viewer", "--yes"}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := newRootCommand()
			root.SetArgs(tt.args)
			// 禁止 cobra 在出错时打印用法，测试只关心返回的错误。
			root.SetOut(io.Discard)
			root.SetErr(io.Discard)

			err := root.Execute()
			var ue *usageError
			isUsageRejection := errors.As(err, &ue)

			if tt.wantYes && isUsageRejection {
				t.Fatalf("传入 --yes 后不应再被门禁拦下: %v", err)
			}
			if !tt.wantYes && !isUsageRejection {
				t.Fatalf("未传 --yes 应被门禁以用法错误拦下，实际: %v", err)
			}
		})
	}
}

// TestIsDangerousReflectsAnnotation 确保危险标记真的被读到。
func TestIsDangerousReflectsAnnotation(t *testing.T) {
	root := newRootCommand()
	for _, cmd := range root.Commands() {
		if cmd.Name() != "role" {
			continue
		}
		for _, sub := range cmd.Commands() {
			want := sub.Name() == "assign"
			if got := isDangerous(sub); got != want {
				t.Errorf("role %s: isDangerous = %v, want %v", sub.Name(), got, want)
			}
		}
	}
}
