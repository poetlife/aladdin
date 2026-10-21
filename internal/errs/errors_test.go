package errs_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"connectrpc.com/connect"

	errorsv1 "github.com/poetlife/aladdin/api/gen/aladdin/errors/v1"
	"github.com/poetlife/aladdin/internal/errs"
)

func TestGeneratedValueCarriesCatalogCodeAndText(t *testing.T) {
	err := errs.ErrScopeMismatch
	if err.Code() != connect.CodePermissionDenied {
		t.Errorf("传输码 = %v, want %v", err.Code(), connect.CodePermissionDenied)
	}
	if err.Reason() != errorsv1.FailureReason_FAILURE_REASON_SCOPE_MISMATCH {
		t.Errorf("原因 = %v, want SCOPE_MISMATCH", err.Reason())
	}
	if err.Message() == "" {
		t.Error("默认文案为空")
	}
}

func TestNewPanicsOnUnregisteredReason(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("未登记的原因应直接失败，而不是静默回落")
		}
	}()
	errs.New(errorsv1.FailureReason(9999))
}

func TestWrapKeepsCauseOutOfWireMessage(t *testing.T) {
	cause := errors.New("disk full")
	err := errs.Wrap(cause, errorsv1.FailureReason_FAILURE_REASON_STORE_UNAVAILABLE)

	if err.Message() != "服务暂时不可用，请稍后重试" {
		t.Errorf("对外文案 = %q", err.Message())
	}
	if strings.Contains(err.Message(), "disk full") {
		t.Error("对外文案泄漏了因果链")
	}
	if !strings.Contains(err.Error(), "disk full") {
		t.Error("日志用的文本应包含因果链")
	}
	if !errors.Is(err, cause) {
		t.Error("因果链应可被 errors.Is 找到")
	}
}

func TestFromAndIsSurviveWrapping(t *testing.T) {
	wrapped := fmt.Errorf("project=p1: %w", errs.ErrPermissionDenied)

	err, ok := errs.From(wrapped)
	if !ok {
		t.Fatal("包装后取不回失败分类")
	}
	if err.Reason() != errorsv1.FailureReason_FAILURE_REASON_PERMISSION_DENIED {
		t.Errorf("原因 = %v", err.Reason())
	}
	if !errs.Is(wrapped, errorsv1.FailureReason_FAILURE_REASON_PERMISSION_DENIED) {
		t.Error("Is 应穿过包装匹配原因")
	}
	if !errors.Is(wrapped, errs.ErrPermissionDenied) {
		t.Error("errors.Is 应按原因匹配包装后的错误")
	}
	if errs.Is(wrapped, errorsv1.FailureReason_FAILURE_REASON_SCOPE_MISMATCH) {
		t.Error("Is 匹配到了不相干的原因")
	}
}

func TestWithMetadataDoesNotMutateSharedValue(t *testing.T) {
	base := errs.ErrAnnotationMissing
	with := base.WithMetadata("method", "/aladdin.rbac.v1.RBACService/PutRole")

	if len(base.Metadata()) != 0 {
		t.Error("共享的生成值被原地修改了")
	}
	if got := with.Metadata()["method"]; got != "/aladdin.rbac.v1.RBACService/PutRole" {
		t.Errorf("metadata[method] = %q", got)
	}

	// Metadata 返回副本：改动它不能反过来影响错误本身。
	with.Metadata()["method"] = "mutated"
	if got := with.Metadata()["method"]; got != "/aladdin.rbac.v1.RBACService/PutRole" {
		t.Errorf("Metadata 返回的不是副本，取到 %q", got)
	}
}

func TestToConnectAttachesFailureDetail(t *testing.T) {
	err := errs.ErrPermissionDenied.WithMetadata("role", "viewer")
	ce := errs.ToConnect(err)

	if ce.Code() != connect.CodePermissionDenied {
		t.Fatalf("传输码 = %v", ce.Code())
	}
	detail := failureDetail(t, ce)
	if detail.GetReason() != errorsv1.FailureReason_FAILURE_REASON_PERMISSION_DENIED {
		t.Errorf("原因 = %v", detail.GetReason())
	}
	if detail.GetMessage() != ce.Message() {
		t.Errorf("详情文案 %q 与错误消息 %q 不同源", detail.GetMessage(), ce.Message())
	}
	if detail.GetMetadata()["role"] != "viewer" {
		t.Errorf("metadata[role] = %q", detail.GetMetadata()["role"])
	}
}

// TestToConnectPassesThroughEncodedErrors 验证"已经编码好的错误原样透传"。
//
// 包装它们会把一次取消变成内部错误，也会让同一个失败出现两个原因。
func TestToConnectPassesThroughEncodedErrors(t *testing.T) {
	for _, code := range []connect.Code{connect.CodeCanceled, connect.CodeDeadlineExceeded} {
		original := connect.NewError(code, errors.New("transport stopped"))

		if got := errs.ToConnect(original); !errors.Is(got, original) {
			t.Errorf("%v：直接的 Connect 错误被二次包装了", code)
		}

		wrapped := fmt.Errorf("stream closed: %w", original)
		if got := errs.ToConnect(wrapped); !errors.Is(got, original) {
			t.Errorf("%v：包在别的错误里的 Connect 错误被二次包装了", code)
		}
	}
}

// TestToConnectPrefersExplicitClassification 验证显式分类优先于内部包着的
// 传输错误：调用方已经说过这是什么失败时，原因比因果链更权威。
func TestToConnectPrefersExplicitClassification(t *testing.T) {
	cause := connect.NewError(connect.CodeCanceled, errors.New("client went away"))
	classified := errs.Wrap(cause, errorsv1.FailureReason_FAILURE_REASON_STORE_UNAVAILABLE)

	ce := errs.ToConnect(classified)
	if ce.Code() != connect.CodeUnavailable {
		t.Fatalf("传输码 = %v, want Unavailable", ce.Code())
	}
	if failureDetail(t, ce).GetReason() != errorsv1.FailureReason_FAILURE_REASON_STORE_UNAVAILABLE {
		t.Error("显式分类被内部错误覆盖了")
	}
}

// TestUnclassifiedErrorLeaksNothing 验证未分类错误对外只有固定文案。
func TestUnclassifiedErrorLeaksNothing(t *testing.T) {
	internal := errors.New("dsn=postgres://user:secret@db/aladdin")

	ce := errs.ToConnect(internal)
	if ce.Code() != connect.CodeInternal {
		t.Fatalf("传输码 = %v, want Internal", ce.Code())
	}
	if ce.Message() != "服务内部错误" {
		t.Errorf("对外文案 = %q", ce.Message())
	}
	if strings.Contains(ce.Message(), "secret") {
		t.Error("未分类错误把内部细节写进了对外文案")
	}
	if failureDetail(t, ce).GetReason() != errorsv1.FailureReason_FAILURE_REASON_INTERNAL_UNEXPECTED {
		t.Error("未分类错误没有标注为 INTERNAL_UNEXPECTED")
	}

	// 因果链仍留在日志能拿到的地方。
	if !strings.Contains(errs.Internal(internal).Error(), "postgres://") {
		t.Error("因果链应保留在错误链里供日志使用")
	}
}

func failureDetail(t *testing.T, ce *connect.Error) *errorsv1.FailureDetail {
	t.Helper()
	for _, d := range ce.Details() {
		msg, err := d.Value()
		if err != nil {
			continue
		}
		if detail, ok := msg.(*errorsv1.FailureDetail); ok {
			return detail
		}
	}
	t.Fatalf("错误中未携带 FailureDetail: %v", ce)
	return nil
}
