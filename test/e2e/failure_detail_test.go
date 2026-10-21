//go:build e2e

package e2e

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	errorsv1 "github.com/poetlife/aladdin/api/gen/aladdin/errors/v1"
	rbacv1 "github.com/poetlife/aladdin/api/gen/aladdin/rbac/v1"
	"github.com/poetlife/aladdin/internal/rbac"
)

// 本文件验证失败分类在真实连接上的表现。
//
// 判定逻辑的单元测试在 internal/rbac，转换的单元测试在 internal/errs 与
// internal/server/interceptor；这里验证的是**它怎么被送出去**——三种协议
// 下详情都能被结构化读取，且旧客户端依赖的 DenialDetail 仍在。

// TestFailureDetailOnGRPC 验证 gRPC 路径上能读到新的失败详情。
func TestFailureDetailOnGRPC(t *testing.T) {
	h := startServer(t, rbac.RoleViewer, testScope)
	c := h.dial(t, testToken, "tenant")

	ctx, cancel := c.Context()
	defer cancel()

	_, err := rbacv1.NewRBACServiceClient(c.Conn()).
		ListRoles(ctx, &rbacv1.ListRolesRequest{Scope: "tenant"})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("期望 PermissionDenied，实际 %v", err)
	}

	detail := failureDetail(t, err)
	if detail.GetReason() != errorsv1.FailureReason_FAILURE_REASON_SCOPE_MISMATCH {
		t.Errorf("原因 = %v, want SCOPE_MISMATCH", detail.GetReason())
	}
	if detail.GetMessage() == "" {
		t.Error("FailureDetail.message 为空")
	}
	if got := detail.GetMessage(); got != status.Convert(err).Message() {
		t.Errorf("详情文案 %q 与错误消息 %q 不同源", got, status.Convert(err).Message())
	}

	// 迁移期：旧客户端仍要能读到同一结论。
	if legacy := denialDetail(t, err); legacy.GetReason() != rbac.ReasonScopeMismatch {
		t.Errorf("DenialDetail.reason = %v", legacy.GetReason())
	}
}

// TestFailureDetailOnConnect 验证 Connect 路径上的同一份详情。
//
// 前端与命令行都走 Connect；只要这里能解出来，客户端就不需要匹配文案。
func TestFailureDetailOnConnect(t *testing.T) {
	h := startServer(t, rbac.RoleViewer, testScope)
	client := connectClient(t, h, testToken, "tenant")

	_, err := client.ListRoles(context.Background(),
		connect.NewRequest(&rbacv1.ListRolesRequest{Scope: "tenant"}))
	if rpcCode(err) != codePermissionDenied {
		t.Fatalf("期望 PermissionDenied，实际 %v", err)
	}

	detail := failureDetailFromConnect(t, err)
	if detail.GetReason() != errorsv1.FailureReason_FAILURE_REASON_SCOPE_MISMATCH {
		t.Errorf("原因 = %v, want SCOPE_MISMATCH", detail.GetReason())
	}
	if detail.GetMessage() == "" {
		t.Error("FailureDetail.message 为空")
	}
}

// TestFailureDetailOnMiddlewareRejection 验证认证中间件写出的拒绝也带新详情。
//
// 认证发生在 RPC 拦截器之前，走的是 HTTP 中间件 + ErrorWriter 那条路；
// 它若漏挂详情，客户端在"未登录"这一档就只能读文案。
func TestFailureDetailOnMiddlewareRejection(t *testing.T) {
	h := startServer(t, rbac.RoleViewer, testScope)

	t.Run("gRPC", func(t *testing.T) {
		c := h.dial(t, "", testScope)
		ctx, cancel := c.Context()
		defer cancel()

		_, err := rbacv1.NewRBACServiceClient(c.Conn()).
			ListRoles(ctx, &rbacv1.ListRolesRequest{Scope: testScope})
		if status.Code(err) != codes.Unauthenticated {
			t.Fatalf("期望 Unauthenticated，实际 %v", err)
		}
		if got := failureDetail(t, err).GetReason(); got != errorsv1.FailureReason_FAILURE_REASON_SESSION_EXPIRED {
			t.Errorf("原因 = %v", got)
		}
	})

	t.Run("Connect", func(t *testing.T) {
		client := connectClient(t, h, "", testScope)
		_, err := client.ListRoles(context.Background(),
			connect.NewRequest(&rbacv1.ListRolesRequest{Scope: testScope}))
		if rpcCode(err) != codeUnauthenticated {
			t.Fatalf("期望 Unauthenticated，实际 %v", err)
		}
		if got := failureDetailFromConnect(t, err).GetReason(); got != errorsv1.FailureReason_FAILURE_REASON_SESSION_EXPIRED {
			t.Errorf("原因 = %v", got)
		}
	})
}

// TestSameCodeTwoReasonsAreDistinguishable 验证两个原因共用一个传输码时，
// 客户端仍能区分它们——这正是"按原因分支"比"按码分支"多出来的信息。
func TestSameCodeTwoReasonsAreDistinguishable(t *testing.T) {
	h := startServer(t, rbac.RoleViewer, testScope)

	// 场景一：有权限但作用域不够。
	scopeClient := h.dial(t, testToken, "tenant")
	ctx, cancel := scopeClient.Context()
	defer cancel()
	_, scopeErr := rbacv1.NewRBACServiceClient(scopeClient.Conn()).
		ListRoles(ctx, &rbacv1.ListRolesRequest{Scope: "tenant"})

	// 场景二：作用域对，但角色没有这条权限。
	roleClient := h.dial(t, testToken, testScope)
	roleCtx, roleCancel := roleClient.Context()
	defer roleCancel()
	_, roleErr := rbacv1.NewRBACServiceClient(roleClient.Conn()).
		PutRole(roleCtx, &rbacv1.PutRoleRequest{
			Scope: testScope,
			Role:  &rbacv1.Role{Id: "new-role", DisplayName: "新角色"},
		})

	if status.Code(scopeErr) != codes.PermissionDenied || status.Code(roleErr) != codes.PermissionDenied {
		t.Fatalf("两个场景都应是 PermissionDenied：scope=%v role=%v", scopeErr, roleErr)
	}

	scopeDetail := failureDetail(t, scopeErr)
	roleDetail := failureDetail(t, roleErr)
	if scopeDetail.GetReason() == roleDetail.GetReason() {
		t.Fatalf("两个不同原因给出了相同的 FailureReason: %v", scopeDetail.GetReason())
	}
	if scopeDetail.GetMessage() == roleDetail.GetMessage() {
		t.Error("两个不同原因给出了相同的提示")
	}

	if scopeDetail.GetReason() != errorsv1.FailureReason_FAILURE_REASON_SCOPE_MISMATCH {
		t.Errorf("作用域场景的原因 = %v", scopeDetail.GetReason())
	}
	if roleDetail.GetReason() != errorsv1.FailureReason_FAILURE_REASON_PERMISSION_DENIED {
		t.Errorf("无权限场景的原因 = %v", roleDetail.GetReason())
	}
}

func failureDetail(t *testing.T, err error) *errorsv1.FailureDetail {
	t.Helper()
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("不是 gRPC 状态错误: %v", err)
	}
	for _, d := range st.Details() {
		if detail, ok := d.(*errorsv1.FailureDetail); ok {
			return detail
		}
	}
	t.Fatalf("错误中未携带 FailureDetail: %v", err)
	return nil
}

func failureDetailFromConnect(t *testing.T, err error) *errorsv1.FailureDetail {
	t.Helper()
	var ce *connect.Error
	if !errors.As(err, &ce) {
		t.Fatalf("不是 Connect 错误: %v", err)
	}
	for _, d := range ce.Details() {
		msg, valueErr := d.Value()
		if valueErr != nil {
			continue
		}
		if detail, ok := msg.(*errorsv1.FailureDetail); ok {
			return detail
		}
	}
	t.Fatalf("错误中未携带 FailureDetail: %v", err)
	return nil
}
