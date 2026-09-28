package main

import (
	"errors"
	"io"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	rbacv1 "github.com/poetlife/aladdin/api/gen/aladdin/rbac/v1"
	"github.com/poetlife/aladdin/internal/auth"
	"github.com/poetlife/aladdin/internal/rbac"
)

// connectErr 造一个指定错误码的 Connect 错误，用于退出码映射表。
func connectErr(code connect.Code) error {
	return connect.NewError(code, errors.New("x"))
}

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
		{"未认证", connectErr(connect.CodeUnauthenticated), exitUnauthenticated},
		{"权限不足", connectErr(connect.CodePermissionDenied), exitPermissionDenied},
		{"服务不可用", connectErr(connect.CodeUnavailable), exitUnavailable},
		{"超时", connectErr(connect.CodeDeadlineExceeded), exitUnavailable},
		{"参数非法", connectErr(connect.CodeInvalidArgument), exitUsage},
		{"前置条件不满足", connectErr(connect.CodeFailedPrecondition), exitUsage},
		{"内部错误", connectErr(connect.CodeInternal), exitFailure},
		{"非 Connect 错误", errors.New("boom"), exitFailure},
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

// 拒绝提示读的是错误详情里的结构化 DenialDetail，而不是错误文本——
// 文本会变，枚举取值是契约。
func TestDescribeDenialReadsStructuredDetail(t *testing.T) {
	denied := func(reason rbacv1.DenialReason, metadata map[string]string) error {
		ce := connect.NewError(connect.CodePermissionDenied, errors.New("服务端原始文本"))
		detail, err := connect.NewErrorDetail(&rbacv1.DenialDetail{
			Reason:   reason,
			Metadata: metadata,
		})
		if err != nil {
			t.Fatalf("构造拒绝详情失败: %v", err)
		}
		ce.AddDetail(detail)
		return ce
	}

	if msg := describeDenial(denied(rbac.ReasonNoMatchingGrant, nil)); !strings.Contains(msg, "aladdin permissions") {
		t.Errorf("无匹配授权的提示应引导到 permissions 命令，得到 %q", msg)
	}
	if msg := describeDenial(denied(rbac.ReasonAnnotationMissing,
		map[string]string{"method": "/aladdin.test.v1.S/M"})); !strings.Contains(msg, "/aladdin.test.v1.S/M") {
		t.Errorf("漏注解的提示应报出方法名，得到 %q", msg)
	}
	// 没有详情的 Connect 错误、以及根本不是 Connect 的错误，都不该给出拒绝提示。
	if msg := describeDenial(connectErr(connect.CodePermissionDenied)); msg != "" {
		t.Errorf("没有详情时不应给出拒绝提示，得到 %q", msg)
	}
	if msg := describeDenial(errors.New("boom")); msg != "" {
		t.Errorf("非 Connect 错误不应给出拒绝提示，得到 %q", msg)
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
