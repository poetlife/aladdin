package telemetry

import (
	"context"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"

	telemetryv1 "github.com/poetlife/aladdin/api/gen/aladdin/telemetry/v1"
	"github.com/poetlife/aladdin/internal/observability"
)

// newRecordingRecorder 构造一个把日志留在内存里的记录器。
func newRecordingRecorder(t *testing.T, limiter *Limiter) (*Recorder, *observer.ObservedLogs) {
	t.Helper()
	core, logs := observer.New(zapcore.DebugLevel)
	return NewRecorder(zap.New(core), limiter), logs
}

func TestReportLogsAcceptedEvent(t *testing.T) {
	rec, logs := newRecordingRecorder(t, NewLimiter(DefaultLimits))

	ev := webEvent(telemetryv1.Action_ACTION_PUBLISH)
	ev.DurationMs = 42
	accepted := rec.Report(context.Background(), Request{
		HeaderClient: observability.ClientWeb,
		SubjectID:    "sub_1",
		Events:       []*telemetryv1.Event{ev},
	})
	if accepted != 1 {
		t.Fatalf("应接受 1 条，得到 %d", accepted)
	}

	entry := logs.FilterMessage("客户端事件").All()
	if len(entry) != 1 {
		t.Fatalf("应写一行事件日志，得到 %d 行", len(entry))
	}
	fields := entry[0].ContextMap()
	want := map[string]any{
		"client":      "web",
		"surface":     "web.editor",
		"action":      "publish",
		"result":      "ok",
		"subject_id":  "sub_1",
		"duration_ms": uint32(42),
	}
	for key, value := range want {
		if fields[key] != value {
			t.Errorf("字段 %s = %v，期望 %v", key, fields[key], value)
		}
	}
}

// TestReportDropsBadEventWithoutError 覆盖"未知动作不污染业务错误率"这条验收。
func TestReportDropsBadEventWithoutError(t *testing.T) {
	rec, logs := newRecordingRecorder(t, NewLimiter(DefaultLimits))

	bad := webEvent(telemetryv1.Action_ACTION_UNSPECIFIED)
	accepted := rec.Report(context.Background(), Request{
		HeaderClient: observability.ClientWeb,
		SubjectID:    "sub_1",
		Events:       []*telemetryv1.Event{bad},
	})
	if accepted != 0 {
		t.Fatalf("未登记动作应被丢弃，得到 %d", accepted)
	}

	dropped := logs.FilterMessage("客户端事件被丢弃").All()
	if len(dropped) != 1 {
		t.Fatalf("应有一行 DEBUG 说明丢弃原因，得到 %d 行", len(dropped))
	}
	if level := dropped[0].Level; level != zapcore.DebugLevel {
		t.Errorf("丢弃应记 DEBUG（不当业务错误），得到 %s", level)
	}
	if reason := dropped[0].ContextMap()["reason"]; reason != reasonUnknownAction {
		t.Errorf("丢弃原因 = %v，期望 %q", reason, reasonUnknownAction)
	}
	// 丢弃的事件不得留下"客户端事件"那行。
	if logs.FilterMessage("客户端事件").Len() != 0 {
		t.Error("被丢弃的事件不应写事件日志")
	}
}

func TestReportRejectsWholeBatchWhenOverLimit(t *testing.T) {
	limiter := NewLimiter(Limits{CallsPerWindow: 1, EventsPerWindow: 10, MaxKeys: 10, Window: time.Minute})
	rec, logs := newRecordingRecorder(t, limiter)

	req := Request{
		HeaderClient: observability.ClientWeb,
		SubjectID:    "sub_1",
		Events:       []*telemetryv1.Event{webEvent(telemetryv1.Action_ACTION_PUBLISH)},
	}
	if accepted := rec.Report(context.Background(), req); accepted != 1 {
		t.Fatalf("首次应接受，得到 %d", accepted)
	}
	if accepted := rec.Report(context.Background(), req); accepted != 0 {
		t.Fatalf("超过调用次数上限后整批丢弃，得到 %d", accepted)
	}
	if logs.FilterMessage("客户端事件上报超出上限，本批丢弃").Len() != 1 {
		t.Error("超限应留一行 DEBUG")
	}
}

// TestReportNilLoggerIsSafe 保证"没配 logger"时不会 panic，只是什么都不写。
func TestReportNilLoggerIsSafe(t *testing.T) {
	rec := NewRecorder(nil, NewLimiter(DefaultLimits))
	accepted := rec.Report(context.Background(), Request{
		HeaderClient: observability.ClientWeb,
		SubjectID:    "sub_1",
		Events:       []*telemetryv1.Event{webEvent(telemetryv1.Action_ACTION_PUBLISH)},
	})
	if accepted != 1 {
		t.Errorf("接受计数与是否配 logger 无关，得到 %d", accepted)
	}
}
