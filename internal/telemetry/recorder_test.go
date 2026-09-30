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

// newRecordingRecorder 构造一个把日志留在内存里的记录器（不落库）。
func newRecordingRecorder(t *testing.T, limiter *Limiter) (*Recorder, *observer.ObservedLogs) {
	t.Helper()
	return newRecordingRecorderWithStore(t, limiter, nil)
}

// newRecordingRecorderWithStore 同上，但带上一个存储。
func newRecordingRecorderWithStore(t *testing.T, limiter *Limiter, store Store) (*Recorder, *observer.ObservedLogs) {
	t.Helper()
	core, logs := observer.New(zapcore.DebugLevel)
	return NewRecorder(zap.New(core), limiter, store), logs
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
	rec := NewRecorder(nil, NewLimiter(DefaultLimits), nil)
	accepted := rec.Report(context.Background(), Request{
		HeaderClient: observability.ClientWeb,
		SubjectID:    "sub_1",
		Events:       []*telemetryv1.Event{webEvent(telemetryv1.Action_ACTION_PUBLISH)},
	})
	if accepted != 1 {
		t.Errorf("接受计数与是否配 logger 无关，得到 %d", accepted)
	}
}

// 通过校验的事件要落库，且落库字段与日志字段同源（同一份已脱敏的 Record）。
func TestReportPersistsAcceptedEvents(t *testing.T) {
	store := NewMemoryStore()
	rec, _ := newRecordingRecorderWithStore(t, NewLimiter(DefaultLimits), store)
	// 把落库时间钉死：时间由服务端盖章，测试要断言的是"盖了章"，不是"盖了几点"。
	stamped := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	rec.now = func() time.Time { return stamped }

	accepted := rec.Report(context.Background(), Request{
		HeaderClient: observability.ClientWeb,
		SubjectID:    "sub_1",
		Events: []*telemetryv1.Event{
			webEvent(telemetryv1.Action_ACTION_PUBLISH),
			webEvent(telemetryv1.Action_ACTION_UNSPECIFIED), // 未登记，不落库
		},
	})
	if accepted != 1 {
		t.Fatalf("应接受 1 条，得到 %d", accepted)
	}

	from, to := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC)
	entries, err := store.Recent(context.Background(), from, to, 10)
	if err != nil {
		t.Fatalf("读取落库结果失败: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("落库条数 = %d，期望 1（被丢弃的那条不进库）", len(entries))
	}
	got := entries[0]
	if got.Action != "publish" || got.SubjectID != "sub_1" || got.Client != "web" {
		t.Errorf("落库内容 = %+v，与日志字段不一致", got)
	}
	if !got.OccurredAt.Equal(stamped) {
		t.Errorf("落库时间 = %v，期望服务端盖章的 %v", got.OccurredAt, stamped)
	}
}

// 没有存储时一切照旧：只是不落库，日志与接受计数不受影响。
func TestReportWithoutStoreDoesNotPersist(t *testing.T) {
	rec, logs := newRecordingRecorder(t, NewLimiter(DefaultLimits))
	accepted := rec.Report(context.Background(), Request{
		HeaderClient: observability.ClientWeb,
		SubjectID:    "sub_1",
		Events:       []*telemetryv1.Event{webEvent(telemetryv1.Action_ACTION_PUBLISH)},
	})
	if accepted != 1 {
		t.Fatalf("应接受 1 条，得到 %d", accepted)
	}
	if logs.FilterMessage("客户端事件").Len() != 1 {
		t.Error("没有存储时日志仍要照写")
	}
}

// TestReportSwallowsStoreFailure 覆盖本模块最重要的一条边界：
// **库写不进去不能让上报变成失败**。遥测的数据问题绝不该升级成客户端的业务错误。
func TestReportSwallowsStoreFailure(t *testing.T) {
	rec, logs := newRecordingRecorderWithStore(t, NewLimiter(DefaultLimits), failingStore{})

	accepted := rec.Report(context.Background(), Request{
		HeaderClient: observability.ClientWeb,
		SubjectID:    "sub_1",
		Events:       []*telemetryv1.Event{webEvent(telemetryv1.Action_ACTION_PUBLISH)},
	})
	if accepted != 1 {
		t.Fatalf("落库失败不影响接受计数，得到 %d", accepted)
	}
	if logs.FilterMessage("客户端事件").Len() != 1 {
		t.Error("落库失败时日志仍要照写")
	}
	warned := logs.FilterMessage("客户端事件落库失败，本批只留在日志里").All()
	if len(warned) != 1 {
		t.Fatalf("落库失败应留一行 WARN，得到 %d 行", len(warned))
	}
	if level := warned[0].Level; level != zapcore.WarnLevel {
		t.Errorf("落库失败应记 WARN，得到 %s", level)
	}
}

// failingStore 是一个永远写不进去的存储，用来验证失败路径。
type failingStore struct{}

func (failingStore) Append(context.Context, []Entry) error { return ErrStoreUnavailable }
func (failingStore) Stats(context.Context, time.Time, time.Time) ([]Stat, error) {
	return nil, ErrStoreUnavailable
}
func (failingStore) Recent(context.Context, time.Time, time.Time, int) ([]Entry, error) {
	return nil, ErrStoreUnavailable
}
func (failingStore) DeleteBefore(context.Context, time.Time) (int64, error) {
	return 0, ErrStoreUnavailable
}
