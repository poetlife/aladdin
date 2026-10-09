package server

import (
	"context"
	"errors"
	"testing"
	"time"

	"connectrpc.com/connect"
	"go.uber.org/zap"

	telemetryv1 "github.com/poetlife/aladdin/api/gen/aladdin/telemetry/v1"
	"github.com/poetlife/aladdin/internal/profile"
	"github.com/poetlife/aladdin/internal/rbac"
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
	svc := NewTelemetryAdminService(&stubStore{}, nil, nil)
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
	svc := NewTelemetryAdminService(store, nil, nil)
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
			svc := NewTelemetryAdminService(store, nil, nil)
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
	svc := NewTelemetryAdminService(store, nil, nil)

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
	// 没装配档案入口时不做解析：这是"读侧换展示信息"这件事的开关，它默认是关的。
	if len(resp.Msg.GetSubjects()) != 0 {
		t.Errorf("subjects = %v，期望为空", resp.Msg.GetSubjects())
	}
}

// resolvedSubject 是读侧解析用例里那个有昵称的主体。
const resolvedSubject = "usr_resolved"

// 明细里的主体标识在读侧换成展示名与头像。
//
// 同时钉住两件事：**事件本身仍然只有标识**（返回的 events 与写侧落库的一模一样），
// 以及匿名事件不占 subjects 的键。
func TestTelemetryAdminResolvesSubjectProfiles(t *testing.T) {
	ctx := context.Background()
	profileStore := profile.NewMemoryStore()
	if err := profileStore.PutText(ctx, resolvedSubject, "阿拉丁", "", time.Now()); err != nil {
		t.Fatalf("写入档案失败: %v", err)
	}

	store := &stubStore{stored: []telemetry.Entry{
		{Record: telemetry.Record{
			Client: "web", Surface: "web.editor", Action: "editor.open", Result: "ok",
			SubjectID: resolvedSubject,
		}},
		// 匿名：允许匿名的动作会是这个形状（登录页上的失败）。
		{Record: telemetry.Record{
			Client: "web", Surface: "web.auth", Action: "auth.login", Result: "fail",
		}},
		// 同一个人又出现一次：项数不因此变多。
		{Record: telemetry.Record{
			Client: "web", Surface: "web.editor", Action: "publish", Result: "blocked",
			SubjectID: resolvedSubject,
		}},
	}}
	svc := NewTelemetryAdminService(store, newTestProfiles(t, profileStore), zap.NewNop())

	resp, err := svc.ListRecentEvents(ctx, connect.NewRequest(&telemetryv1.ListRecentEventsRequest{
		Window: telemetryv1.TimeWindow_TIME_WINDOW_TODAY,
	}))
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}

	subjects := resp.Msg.GetSubjects()
	if len(subjects) != 1 {
		t.Fatalf("subjects 项数 = %d，期望 1（只有带主体的那些，且同一个只算一项）", len(subjects))
	}
	if got := subjects[resolvedSubject].GetDisplayName(); got != "阿拉丁" {
		t.Errorf("display_name = %q，期望昵称", got)
	}
	// 头像没设、这个部署也没有对象存储：地址为空是"没有可显示的头像"，不是错误。
	if got := subjects[resolvedSubject].GetAvatarUrl(); got != "" {
		t.Errorf("avatar_url = %q，期望为空", got)
	}

	// 解析只发生在读侧：事件本身与写侧落库的那一份逐字一致。
	events := resp.Msg.GetEvents()
	if len(events) != 3 {
		t.Fatalf("事件条数 = %d，期望 3", len(events))
	}
	if events[0].GetSubjectId() != resolvedSubject || events[1].GetSubjectId() != "" {
		t.Errorf("事件的 subject_id 被改写了：%q / %q", events[0].GetSubjectId(), events[1].GetSubjectId())
	}
}

// 解析失败**降级不失败**：档案存储抖动不该让排障要看的那一页读不出来。
//
// 界面上表现为主体列回退到只显示标识——这是可接受的降级，而整页报错不是。
func TestTelemetryAdminDegradesWhenSubjectResolutionFails(t *testing.T) {
	store := &stubStore{stored: []telemetry.Entry{{
		Record: telemetry.Record{
			Client: "web", Surface: "web.editor", Action: "editor.open", Result: "ok",
			SubjectID: resolvedSubject,
		},
	}}}
	svc := NewTelemetryAdminService(store, newTestProfiles(t, failingProfileStore{}), zap.NewNop())

	resp, err := svc.ListRecentEvents(context.Background(), connect.NewRequest(&telemetryv1.ListRecentEventsRequest{
		Window: telemetryv1.TimeWindow_TIME_WINDOW_TODAY,
	}))
	if err != nil {
		t.Fatalf("解析失败不应让明细查询失败: %v", err)
	}
	if len(resp.Msg.GetEvents()) != 1 {
		t.Errorf("事件条数 = %d，期望 1", len(resp.Msg.GetEvents()))
	}
	if len(resp.Msg.GetSubjects()) != 0 {
		t.Errorf("subjects = %v，期望为空", resp.Msg.GetSubjects())
	}
}

// newTestProfiles 构造一个接在给定存储上的档案入口。
//
// Subjects 用空的内存实现、Identities 留 nil：这些用例要的是"昵称读得回来"，
// 身份的加入会让断言多出与本层无关的前提。
func newTestProfiles(t *testing.T, store profile.Store) *profile.Profiles {
	t.Helper()
	return profile.NewProfiles(profile.ProfilesDeps{
		Store:    store,
		Subjects: rbac.NewMemoryStore(),
		Logger:   zap.NewNop(),
	})
}

// failingProfileStore 让档案读取一律失败。
type failingProfileStore struct{ profile.Store }

func (failingProfileStore) Get(context.Context, string) (profile.Profile, error) {
	return profile.Profile{}, profile.ErrStoreUnavailable
}

// "库用不了"必须让请求失败，而不是被读成"最近没有事件"。
func TestTelemetryAdminStoreUnavailable(t *testing.T) {
	svc := NewTelemetryAdminService(&stubStore{err: telemetry.ErrStoreUnavailable}, nil, nil)
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
