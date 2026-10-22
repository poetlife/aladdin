package interceptor

import (
	"strings"
	"testing"

	"connectrpc.com/connect"

	errorsv1 "github.com/poetlife/aladdin/api/gen/aladdin/errors/v1"
	rbacv1 "github.com/poetlife/aladdin/api/gen/aladdin/rbac/v1"
	"github.com/poetlife/aladdin/internal/rbac"
)

func TestRejectCarriesBothDetails(t *testing.T) {
	cases := []struct {
		name       string
		reason     rbac.Reason
		wantCode   connect.Code
		wantReason errorsv1.FailureReason
	}{
		{
			name:       "没有匹配的授权",
			reason:     rbac.ReasonNoMatchingGrant,
			wantCode:   connect.CodePermissionDenied,
			wantReason: errorsv1.FailureReason_FAILURE_REASON_PERMISSION_DENIED,
		},
		{
			name:       "作用域不够",
			reason:     rbac.ReasonScopeMismatch,
			wantCode:   connect.CodePermissionDenied,
			wantReason: errorsv1.FailureReason_FAILURE_REASON_SCOPE_MISMATCH,
		},
		{
			name:       "凭证失效",
			reason:     rbac.ReasonSessionExpired,
			wantCode:   connect.CodeUnauthenticated,
			wantReason: errorsv1.FailureReason_FAILURE_REASON_SESSION_EXPIRED,
		},
		{
			name:       "主体不存在",
			reason:     rbac.ReasonSubjectNotFound,
			wantCode:   connect.CodeUnauthenticated,
			wantReason: errorsv1.FailureReason_FAILURE_REASON_SESSION_SUBJECT_NOT_FOUND,
		},
		{
			name:       "存储不可用",
			reason:     rbac.ReasonStoreUnavailable,
			wantCode:   connect.CodeUnavailable,
			wantReason: errorsv1.FailureReason_FAILURE_REASON_STORE_UNAVAILABLE,
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			ce := connectErrorOf(t, Reject(tt.reason))
			if ce.Code() != tt.wantCode {
				t.Errorf("传输码 = %v, want %v", ce.Code(), tt.wantCode)
			}

			detail := failureDetailOf(t, ce)
			if detail.GetReason() != tt.wantReason {
				t.Errorf("FailureDetail.reason = %v, want %v", detail.GetReason(), tt.wantReason)
			}
			if detail.GetMessage() == "" || detail.GetMessage() != ce.Message() {
				t.Errorf("FailureDetail.message = %q, 错误消息 = %q", detail.GetMessage(), ce.Message())
			}

			// 迁移期：旧客户端仍要能读到同一结论。
			legacy := denialDetailOf(t, ce)
			if legacy.GetReason() != tt.reason {
				t.Errorf("DenialDetail.reason = %v, want %v", legacy.GetReason(), tt.reason)
			}
		})
	}
}

// TestSameCodeTwoReasonsAreDistinguishable 是本设计的核心断言：
// 两个原因可以共用一个传输码，但客户端仍能区分它们。
func TestSameCodeTwoReasonsAreDistinguishable(t *testing.T) {
	noGrant := connectErrorOf(t, Reject(rbac.ReasonNoMatchingGrant))
	scope := connectErrorOf(t, Reject(rbac.ReasonScopeMismatch))

	if noGrant.Code() != scope.Code() {
		t.Fatalf("两个原因应共用同一个传输码，实际 %v / %v", noGrant.Code(), scope.Code())
	}
	first := failureDetailOf(t, noGrant)
	second := failureDetailOf(t, scope)
	if first.GetReason() == second.GetReason() {
		t.Fatal("两个不同原因给出了相同的 FailureReason")
	}
	if first.GetMessage() == second.GetMessage() {
		t.Error("两个不同原因给出了相同的提示，客户端无法据此给出不同解释")
	}
}

func TestDenyByAnnotationCarriesMethod(t *testing.T) {
	const procedure = "/aladdin.rbac.v1.RBACService/PutRole"
	ce := connectErrorOf(t, DenyByAnnotation(procedure, "方法缺少注解"))

	if ce.Code() != connect.CodeInternal {
		t.Errorf("传输码 = %v, want Internal", ce.Code())
	}
	detail := failureDetailOf(t, ce)
	if detail.GetReason() != errorsv1.FailureReason_FAILURE_REASON_ANNOTATION_MISSING {
		t.Errorf("原因 = %v", detail.GetReason())
	}
	if detail.GetMetadata()["method"] != procedure {
		t.Errorf("FailureDetail.metadata[method] = %q", detail.GetMetadata()["method"])
	}

	legacy := denialDetailOf(t, ce)
	if legacy.GetMetadata()["method"] != procedure {
		t.Errorf("DenialDetail.metadata[method] = %q", legacy.GetMetadata()["method"])
	}
}

func TestRejectUnknownProcedureIsClassifiedButNotADenial(t *testing.T) {
	ce := connectErrorOf(t, RejectUnknownProcedure("/not/a/method"))

	if ce.Code() != connect.CodeUnimplemented {
		t.Errorf("传输码 = %v, want Unimplemented", ce.Code())
	}
	if !strings.Contains(ce.Message(), "/not/a/method") {
		t.Errorf("错误消息没有带过程名: %q", ce.Message())
	}
	if got := failureDetailOf(t, ce).GetReason(); got != errorsv1.FailureReason_FAILURE_REASON_UNKNOWN_PROCEDURE {
		t.Errorf("原因 = %v", got)
	}
	// 这不是一次权限拒绝，不应带 DenialDetail。
	for _, d := range ce.Details() {
		msg, err := d.Value()
		if err != nil {
			continue
		}
		if _, ok := msg.(*rbacv1.DenialDetail); ok {
			t.Error("未知过程名不应携带 DenialDetail")
		}
	}
}

func denialDetailOf(t *testing.T, ce *connect.Error) *rbacv1.DenialDetail {
	t.Helper()
	for _, d := range ce.Details() {
		msg, err := d.Value()
		if err != nil {
			continue
		}
		if detail, ok := msg.(*rbacv1.DenialDetail); ok {
			return detail
		}
	}
	t.Fatalf("错误中未携带 DenialDetail: %v", ce)
	return nil
}
