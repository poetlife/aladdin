package server

import (
	"context"
	"errors"
	"testing"
	"time"

	"connectrpc.com/connect"

	telemetryv1 "github.com/poetlife/aladdin/api/gen/aladdin/telemetry/v1"
	"github.com/poetlife/aladdin/internal/telemetry"
)

// stubStore 记录收到的查询参数并返回固定的结果。
//
// 用它而不是真库：这一层要验证的是"协议取值怎么折算成查询"，库本身的行为由
// internal/telemetry/gormstore 的契约测试覆盖。两处各测一件事，不重复。
type stubStore struct {
	stored         []telemetry.Entry
	stats          []telemetry.Stat
	lastFrom       time.Time
	lastTo         time.Time
	lastLimit      int
	deleteBeforeAt time.Time
	deleted        int64
	err            error
}

func (s *stubStore) Append(context.Context, []telemetry.Entry) error { return nil }

func (s *stubStore) Stats(_ context.Context, from, to time.Time) ([]telemetry.Stat, error) {
	s.lastFrom, s.lastTo = from, to
	if s.err != nil {
		return nil, s.err
	}
	return s.stats, nil
}

func (s *stubStore) Recent(_ context.Context, from, to time.Time, limit int) ([]telemetry.Entry, error) {
	s.lastFrom, s.lastTo, s.lastLimit = from, to, limit
	if s.err != nil {
		return nil, s.err
	}
	return s.stored, nil
}

func (s *stubStore) DeleteBefore(_ context.Context, cutoff time.Time) (int64, error) {
	s.deleteBeforeAt = cutoff
	return s.deleted, s.err
}

// 未指定或越界的时间窗是**拒绝**，不是回落到默认窗口——静默回落会让一个拼错的
// 取值看起来像是生效了。
func TestTelemetryAdminRejectsUnknownWindow(t *testing.T) {
	svc := NewTelemetryAdminService(&stubStore{})
	ctx := context.Background()

	if _, err := svc.ListEventStats(ctx, connect.NewRequest(&telemetryv1.ListEventStatsRequest{})); err == nil {
		t.Error("未指定窗口应被拒绝")
	} else if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("错误码 = %v，期望 InvalidArgument", connect.CodeOf(err))
	}

	if _, err := svc.ListRecentEvents(ctx, connect.NewRequest(&telemetryv1.ListRecentEventsRequest{})); err == nil {
		t.Error("未指定窗口应被拒绝")
	} else if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("错误码 = %v，期望 InvalidArgument", connect.CodeOf(err))
	}
}

// 计数按 (client, action, result) 回填，枚举可靠地折回协议取值。
func TestTelemetryAdminListEventStats(t *testing.T) {
	store := &stubStore{stats: []telemetry.Stat{
		{Client: "web", Action: "draft.save", Result: "blocked", Count: 3},
		{Client: "cli", Action: "cli.local_fail", Result: "fail", Count: 1},
	}}
	svc := NewTelemetryAdminService(store)
	svc.now = func() time.Time { return time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC) }

	resp, err := svc.ListEventStats(context.Background(), connect.NewRequest(&telemetryv1.ListEventStatsRequest{
		Window: telemetryv1.TimeWindow_TIME_WINDOW_LAST_7_DAYS,
	}))
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	stats := resp.Msg.GetStats()
	if len(stats) != 2 {
		t.Fatalf("行数 = %d，期望 2", len(stats))
	}
	first := stats[0]
	if first.GetClient() != telemetryv1.Client_CLIENT_WEB || first.GetResult() != telemetryv1.Result_RESULT_BLOCKED ||
		first.GetAction() != "draft.save" || first.GetCount() != 3 {
		t.Errorf("第一行 = %v，与写入值不一致", first)
	}
	// 窗口折算必须走同一个入口：近 7 天就是 now-7×24h 到 now。
	wantFrom := svc.now().Add(-7 * 24 * time.Hour)
	if !store.lastFrom.Equal(wantFrom) {
		t.Errorf("from = %v，期望 %v", store.lastFrom, wantFrom)
	}
	if !store.lastTo.Equal(svc.now()) {
		t.Errorf("to = %v，期望 now", store.lastTo)
	}
}

// 条数上限是**有界**的：未指定取默认值，超过上限收敛到上限，都不报错。
func TestTelemetryAdminRecentLimitIsBounded(t *testing.T) {
	cases := []struct {
		name  string
		limit uint32
		want  int
	}{
		{name: "未指定取默认值", limit: 0, want: defaultRecentLimit},
		{name: "正常取值原样使用", limit: 10, want: 10},
		{name: "超过上限收敛到上限", limit: maxRecentLimit + 1, want: maxRecentLimit},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := &stubStore{}
			svc := NewTelemetryAdminService(store)
			_, err := svc.ListRecentEvents(context.Background(), connect.NewRequest(&telemetryv1.ListRecentEventsRequest{
				Window: telemetryv1.TimeWindow_TIME_WINDOW_TODAY,
				Limit:  tc.limit,
			}))
			if err != nil {
				t.Fatalf("查询失败: %v", err)
			}
			if store.lastLimit != tc.want {
				t.Errorf("limit = %d，期望 %d", store.lastLimit, tc.want)
			}
		})
	}
}

// 明细字段一一回填，时间按 RFC3339(UTC)。
func TestTelemetryAdminListRecentEvents(t *testing.T) {
	occurred := time.Date(2026, 9, 30, 4, 30, 0, 0, time.UTC)
	store := &stubStore{stored: []telemetry.Entry{{
		Record: telemetry.Record{
			Client:     "cli",
			Surface:    "cli",
			Action:     "cli.local_fail",
			Result:     "fail",
			DurationMS: 7,
			TraceID:    "4bf92f3577b34da6a3ce929d0e0e4736",
			SubjectID:  "subject/abc",
			Attrs:      map[string]string{"command": "galaxy"},
		},
		OccurredAt: occurred,
	}}}
	svc := NewTelemetryAdminService(store)

	resp, err := svc.ListRecentEvents(context.Background(), connect.NewRequest(&telemetryv1.ListRecentEventsRequest{
		Window: telemetryv1.TimeWindow_TIME_WINDOW_TODAY,
	}))
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	events := resp.Msg.GetEvents()
	if len(events) != 1 {
		t.Fatalf("条数 = %d，期望 1", len(events))
	}
	e := events[0]
	if e.GetOccurredAt() != occurred.Format(time.RFC3339) {
		t.Errorf("occurred_at = %q，期望 %q", e.GetOccurredAt(), occurred.Format(time.RFC3339))
	}
	if e.GetClient() != telemetryv1.Client_CLIENT_CLI || e.GetSurface() != telemetryv1.Surface_SURFACE_CLI ||
		e.GetResult() != telemetryv1.Result_RESULT_FAIL || e.GetAction() != "cli.local_fail" {
		t.Errorf("枚举回填不一致：%v", e)
	}
	if e.GetDurationMs() != 7 || e.GetClientTraceId() != "4bf92f3577b34da6a3ce929d0e0e4736" ||
		e.GetSubjectId() != "subject/abc" || e.GetAttrs()["command"] != "galaxy" {
		t.Errorf("字段回填不一致：%v", e)
	}
}

// "库用不了"必须让请求失败，而不是被读成"最近没有事件"。
func TestTelemetryAdminStoreUnavailable(t *testing.T) {
	svc := NewTelemetryAdminService(&stubStore{err: telemetry.ErrStoreUnavailable})
	ctx := context.Background()

	_, err := svc.ListEventStats(ctx, connect.NewRequest(&telemetryv1.ListEventStatsRequest{
		Window: telemetryv1.TimeWindow_TIME_WINDOW_TODAY,
	}))
	if !errors.Is(err, telemetry.ErrStoreUnavailable) {
		t.Fatalf("err = %v，期望包着 ErrStoreUnavailable", err)
	}
	if connect.CodeOf(err) != connect.CodeUnavailable {
		t.Errorf("错误码 = %v，期望 Unavailable", connect.CodeOf(err))
	}

	_, err = svc.ListRecentEvents(ctx, connect.NewRequest(&telemetryv1.ListRecentEventsRequest{
		Window: telemetryv1.TimeWindow_TIME_WINDOW_TODAY,
	}))
	if connect.CodeOf(err) != connect.CodeUnavailable {
		t.Errorf("明细错误码 = %v，期望 Unavailable", connect.CodeOf(err))
	}
}
