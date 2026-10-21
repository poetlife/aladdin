package interceptor

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
	"google.golang.org/protobuf/types/known/emptypb"

	errorsv1 "github.com/poetlife/aladdin/api/gen/aladdin/errors/v1"
	"github.com/poetlife/aladdin/internal/errs"
)

func TestFailureInterceptorConvertsUnclassified(t *testing.T) {
	core, logs := observer.New(zap.ErrorLevel)
	it := &FailureInterceptor{Logger: zap.New(core)}
	wrapped := it.WrapUnary(func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
		return nil, errors.New("boom: password=hunter2")
	})

	_, err := wrapped(context.Background(), connect.NewRequest(&emptypb.Empty{}))
	ce := connectErrorOf(t, err)
	if ce.Code() != connect.CodeInternal {
		t.Errorf("传输码 = %v, want Internal", ce.Code())
	}
	if ce.Message() != "服务内部错误" {
		t.Errorf("对外文案 = %q", ce.Message())
	}
	if strings.Contains(ce.Message(), "hunter2") {
		t.Error("未分类错误的内部细节泄漏到了对外文案")
	}
	if got := failureDetailOf(t, ce).GetReason(); got != errorsv1.FailureReason_FAILURE_REASON_INTERNAL_UNEXPECTED {
		t.Errorf("原因 = %v", got)
	}

	if logs.Len() != 1 {
		t.Fatalf("日志条数 = %d, want 1", logs.Len())
	}
	if got := logs.All()[0].ContextMap()["reason"]; got != errorsv1.FailureReason_FAILURE_REASON_INTERNAL_UNEXPECTED.String() {
		t.Errorf("日志 reason = %v", got)
	}
}

func TestFailureInterceptorPassesEncodedErrorsThrough(t *testing.T) {
	it := &FailureInterceptor{Logger: zap.NewNop()}
	original := connect.NewError(connect.CodeCanceled, errors.New("caller went away"))
	wrapped := it.WrapUnary(func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
		return nil, original
	})

	_, err := wrapped(context.Background(), connect.NewRequest(&emptypb.Empty{}))
	if !errors.Is(err, original) {
		t.Error("已编码的取消被二次包装了")
	}
}

// TestFailureInterceptorUnwrapsNestedConnectErrors 验证包在普通错误里的
// Connect 错误仍按原码送出，而不是退化成 Unknown。
func TestFailureInterceptorUnwrapsNestedConnectErrors(t *testing.T) {
	it := &FailureInterceptor{Logger: zap.NewNop()}
	original := connect.NewError(connect.CodeUnavailable, errors.New("upstream down"))
	wrapped := it.WrapUnary(func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
		return nil, fmt.Errorf("calling upstream: %w", original)
	})

	_, err := wrapped(context.Background(), connect.NewRequest(&emptypb.Empty{}))
	ce := connectErrorOf(t, err)
	if ce.Code() != connect.CodeUnavailable {
		t.Errorf("传输码 = %v, want Unavailable", ce.Code())
	}
}

func TestFailureInterceptorConvertsClassifiedErrors(t *testing.T) {
	it := &FailureInterceptor{Logger: zap.NewNop()}
	wrapped := it.WrapUnary(func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
		return nil, errs.ErrStoreUnavailable
	})

	_, err := wrapped(context.Background(), connect.NewRequest(&emptypb.Empty{}))
	ce := connectErrorOf(t, err)
	if ce.Code() != connect.CodeUnavailable {
		t.Errorf("传输码 = %v", ce.Code())
	}
	if got := failureDetailOf(t, ce).GetReason(); got != errorsv1.FailureReason_FAILURE_REASON_STORE_UNAVAILABLE {
		t.Errorf("原因 = %v", got)
	}
}

// fakeStreamConn 只需要 Spec：被测的转换不看流上的消息。
type fakeStreamConn struct{ connect.StreamingHandlerConn }

func (fakeStreamConn) Spec() connect.Spec { return connect.Spec{Procedure: "/test.v1.Test/Stream"} }

func TestFailureInterceptorCoversStreamingHandler(t *testing.T) {
	it := &FailureInterceptor{Logger: zap.NewNop()}
	wrapped := it.WrapStreamingHandler(func(context.Context, connect.StreamingHandlerConn) error {
		return errs.ErrPermissionDenied
	})

	err := wrapped(context.Background(), fakeStreamConn{})
	ce := connectErrorOf(t, err)
	if ce.Code() != connect.CodePermissionDenied {
		t.Errorf("传输码 = %v", ce.Code())
	}
	if got := failureDetailOf(t, ce).GetReason(); got != errorsv1.FailureReason_FAILURE_REASON_PERMISSION_DENIED {
		t.Errorf("原因 = %v", got)
	}
}

// TestFailureInterceptorLogsUpstreamInternalFailures 验证在内层就编码好的
// 内部失败（注解缺失是典型）仍按错误级别留痕，并带原因。
func TestFailureInterceptorLogsUpstreamInternalFailures(t *testing.T) {
	core, logs := observer.New(zap.ErrorLevel)
	it := &FailureInterceptor{Logger: zap.New(core)}
	wrapped := it.WrapUnary(func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
		return nil, DenyByAnnotation("/test.v1.Test/Get", "方法缺少注解")
	})

	_, err := wrapped(context.Background(), connect.NewRequest(&emptypb.Empty{}))
	if connectErrorOf(t, err).Code() != connect.CodeInternal {
		t.Fatalf("传输码 = %v, want Internal", connect.CodeOf(err))
	}

	if logs.Len() != 1 {
		t.Fatalf("日志条数 = %d, want 1", logs.Len())
	}
	entry := logs.All()[0]
	if entry.Message != "服务端内部失败" {
		t.Errorf("日志消息 = %q", entry.Message)
	}
	if got := entry.ContextMap()["reason"]; got != errorsv1.FailureReason_FAILURE_REASON_ANNOTATION_MISSING.String() {
		t.Errorf("日志 reason = %v", got)
	}
}

func connectErrorOf(t *testing.T, err error) *connect.Error {
	t.Helper()
	var ce *connect.Error
	if !errors.As(err, &ce) {
		t.Fatalf("不是 Connect 错误: %v", err)
	}
	return ce
}

func failureDetailOf(t *testing.T, ce *connect.Error) *errorsv1.FailureDetail {
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
