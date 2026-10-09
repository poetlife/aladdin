//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	identityv1 "github.com/poetlife/aladdin/api/gen/aladdin/identity/v1"
	profilev1 "github.com/poetlife/aladdin/api/gen/aladdin/profile/v1"
	telemetryv1 "github.com/poetlife/aladdin/api/gen/aladdin/telemetry/v1"
	"github.com/poetlife/aladdin/api/gen/aladdin/telemetry/v1/telemetryv1connect"
	"github.com/poetlife/aladdin/internal/rbac"
)

// 客户端事件的读侧管理面。
//
// 它与写侧是**准入条件相反**的两件事：ReportEvents 是公开方法（登录页失败、
// 命令行未登录退出都发生在拿到会话之前），而读侧要 `telemetry.read`。因此这组
// 用例要同时钉住两件事：有权限的人真能读到写侧记下的东西，没权限的人真的被拒。

// telemetryClients 构造走 Connect 协议的两个客户端。
func telemetryClients(t *testing.T, h harness, token, scope string) (telemetryv1connect.TelemetryServiceClient, telemetryv1connect.TelemetryAdminServiceClient) {
	t.Helper()
	httpClient := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &headerTransport{
			base:  http.DefaultTransport,
			token: token,
			scope: scope,
		},
	}
	base := "http://" + h.address
	return telemetryv1connect.NewTelemetryServiceClient(httpClient, base),
		telemetryv1connect.NewTelemetryAdminServiceClient(httpClient, base)
}

// 审计员能看到刚上报的事件：写侧落库、读侧查出来，串成一条链。
//
// 这条用例同时覆盖了"读侧字段与写侧一致"：断言的是写侧接受的那几个有界取值，
// 读侧必须原样给出。
func TestTelemetryAdminReadsReportedEvent(t *testing.T) {
	h := startServer(t, rbac.RoleAuditor, testScope)
	report, admin := telemetryClients(t, h, testToken, testScope)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := report.ReportEvents(ctx, connect.NewRequest(&telemetryv1.ReportEventsRequest{
		Events: []*telemetryv1.Event{{
			Client:     telemetryv1.Client_CLIENT_WEB,
			Surface:    telemetryv1.Surface_SURFACE_WEB_EDITOR,
			Action:     telemetryv1.Action_ACTION_PUBLISH,
			Result:     telemetryv1.Result_RESULT_BLOCKED,
			DurationMs: 12,
		}},
	})); err != nil {
		t.Fatalf("上报失败: %v", err)
	}

	resp, err := admin.ListRecentEvents(ctx, connect.NewRequest(&telemetryv1.ListRecentEventsRequest{
		Scope:  testScope,
		Window: telemetryv1.TimeWindow_TIME_WINDOW_LAST_7_DAYS,
	}))
	if err != nil {
		t.Fatalf("读取明细失败: %v", err)
	}
	events := resp.Msg.GetEvents()
	if len(events) == 0 {
		t.Fatal("上报过一条事件，明细里却一条都没有")
	}
	got := events[0]
	if got.GetAction() != "publish" || got.GetResult() != telemetryv1.Result_RESULT_BLOCKED ||
		got.GetClient() != telemetryv1.Client_CLIENT_WEB {
		t.Errorf("明细与上报的取值不一致：%v", got)
	}
	if got.GetOccurredAt() == "" {
		t.Error("发生时间应由服务端盖章，不能为空")
	}

	// 计数与明细走同一个窗口，同一批数据必须对得上。
	statsResp, err := admin.ListEventStats(ctx, connect.NewRequest(&telemetryv1.ListEventStatsRequest{
		Scope:  testScope,
		Window: telemetryv1.TimeWindow_TIME_WINDOW_LAST_7_DAYS,
	}))
	if err != nil {
		t.Fatalf("读取计数失败: %v", err)
	}
	found := false
	for _, stat := range statsResp.Msg.GetStats() {
		if stat.GetAction() == "publish" && stat.GetResult() == telemetryv1.Result_RESULT_BLOCKED {
			found = true
			if stat.GetCount() < 1 {
				t.Errorf("计数 = %d，期望至少 1", stat.GetCount())
			}
		}
	}
	if !found {
		t.Error("计数里没有刚上报的那个组合")
	}
}

// 没有 telemetry.read 的主体读不到：写侧公开不等于读侧公开。
//
// 两种协议各打一次。判定只有一处实现，但那处实现要能在三条协议上都被走到
// （见 AGENTS.md 的"改判定必须三种协议都对"）。
func TestTelemetryAdminDeniesWithoutPermission(t *testing.T) {
	h := startServer(t, rbac.RoleViewer, testScope)

	t.Run("Connect", func(t *testing.T) {
		_, admin := telemetryClients(t, h, testToken, testScope)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, err := admin.ListEventStats(ctx, connect.NewRequest(&telemetryv1.ListEventStatsRequest{
			Scope:  testScope,
			Window: telemetryv1.TimeWindow_TIME_WINDOW_TODAY,
		}))
		if connect.CodeOf(err) != connect.CodePermissionDenied {
			t.Fatalf("错误码 = %v，期望 PermissionDenied", connect.CodeOf(err))
		}
	})

	t.Run("gRPC", func(t *testing.T) {
		c := h.dial(t, testToken, testScope)
		ctx, cancel := c.Context()
		defer cancel()
		_, err := telemetryv1.NewTelemetryAdminServiceClient(c.Conn()).
			ListRecentEvents(ctx, &telemetryv1.ListRecentEventsRequest{
				Scope:  testScope,
				Window: telemetryv1.TimeWindow_TIME_WINDOW_TODAY,
			})
		if err == nil {
			t.Fatal("无权限主体竟然读到了遥测")
		}
	})
}

// 未指定窗口被拒绝：静默回落到某个默认窗口会让一个拼错的取值看起来像生效了。
func TestTelemetryAdminRejectsUnknownWindow(t *testing.T) {
	h := startServer(t, rbac.RoleAuditor, testScope)
	_, admin := telemetryClients(t, h, testToken, testScope)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := admin.ListEventStats(ctx, connect.NewRequest(&telemetryv1.ListEventStatsRequest{
		Scope: testScope,
	}))
	if err == nil {
		t.Fatal("未指定窗口应被拒绝")
	}
}

// 明细里的主体标识在读侧换成展示名，而**写侧（日志与库）里始终只有标识**。
//
// 两件事必须一起钉住：昵称要看得见（这正是不用再去别处查人的理由），昵称又不得
// 被写进事件（那是脱敏硬规则）。后者的观察点是服务端日志——只断言返回值证明不了
// "没写进去"，而这类字段的价值恰恰在于"没出现"。
func TestTelemetryAdminResolvesSubjectDisplayName(t *testing.T) {
	const nickname = "阿拉丁的测试昵称"

	fake := newFakeGoogleToken("google-sub-resolved", "resolved@example.com")
	h := startServerWith(t, rbac.RoleAuditor, testScope,
		withChannels(googleChannel(fake)), withPublicBaseURL(e2ePublicBaseURL))

	// 主体只能由登录产生，因此这里走一次完整的重定向登录，而不是注入一个标识。
	identityClient, sessionCtx, sessionToken := loginOverGRPC(t, h)
	who, err := identityClient.WhoAmI(sessionCtx, &identityv1.WhoAmIRequest{})
	if err != nil {
		t.Fatalf("WhoAmI 失败: %v", err)
	}
	subjectID := who.GetSubjectId()

	if _, err := connectProfile(t, h, sessionToken).UpdateProfile(sessionCtx,
		connect.NewRequest(&profilev1.UpdateProfileRequest{Nickname: nickname})); err != nil {
		t.Fatalf("设置昵称失败: %v", err)
	}

	// 用这个会话上报一条事件：它落库时带的是主体标识，不带任何展示信息。
	report, _ := telemetryClients(t, h, sessionToken, "")
	if _, err := report.ReportEvents(sessionCtx, connect.NewRequest(&telemetryv1.ReportEventsRequest{
		Events: []*telemetryv1.Event{{
			Client:     telemetryv1.Client_CLIENT_WEB,
			Surface:    telemetryv1.Surface_SURFACE_WEB_EDITOR,
			Action:     telemetryv1.Action_ACTION_EDITOR_OPEN,
			Result:     telemetryv1.Result_RESULT_OK,
			DurationMs: 42,
		}},
	})); err != nil {
		t.Fatalf("上报失败: %v", err)
	}

	// 读侧用审计员（有 telemetry.read）。
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, admin := telemetryClients(t, h, testToken, testScope)
	resp, err := admin.ListRecentEvents(ctx, connect.NewRequest(&telemetryv1.ListRecentEventsRequest{
		Scope:  testScope,
		Window: telemetryv1.TimeWindow_TIME_WINDOW_LAST_7_DAYS,
	}))
	if err != nil {
		t.Fatalf("读取明细失败: %v", err)
	}

	subjects := resp.Msg.GetSubjects()
	resolved, ok := subjects[subjectID]
	if !ok {
		t.Fatalf("subjects 里没有刚上报的主体 %s（拿到 %d 项）", subjectID, len(subjects))
	}
	if resolved.GetDisplayName() != nickname {
		t.Errorf("display_name = %q，期望昵称", resolved.GetDisplayName())
	}

	// 明细本身仍然只有标识：读侧换的是展示，事件没有被改写。
	found := false
	for _, e := range resp.Msg.GetEvents() {
		if e.GetAction() == "editor.open" && e.GetSubjectId() == subjectID {
			found = true
			if e.GetDurationMs() != 42 {
				t.Errorf("duration_ms = %d，期望 42", e.GetDurationMs())
			}
		}
	}
	if !found {
		t.Fatal("明细里没有刚上报的那一条")
	}

	// 昵称一次都没有进过遥测这条链路。库里那一条也只存标识：Record 的字段集里
	// 根本没有承载展示信息的位置（见 internal/telemetry/sanitize.go），因此这个
	// 观察点落在日志上——它是唯一一处"写下来了就会被看见"的地方。
	//
	// 排除带 `sql` 字段的那些行：那是 SQL 日志器在 debug 级别把语句连同参数原样
	// 打出来，记的是**档案自己的写入**（gorm 的既有行为，默认级别下不出现），
	// 与遥测无关。这里问的是"遥测有没有把展示信息写进去"。
	for _, entry := range h.logs.All() {
		if _, isSQL := entry.ContextMap()["sql"]; isSQL {
			continue
		}
		if strings.Contains(entry.Message, nickname) {
			t.Fatalf("日志消息里出现了昵称：%s", entry.Message)
		}
		for key, value := range entry.ContextMap() {
			if strings.Contains(fmt.Sprint(value), nickname) {
				t.Fatalf("日志字段 %s 里出现了昵称：%v", key, value)
			}
		}
	}
}
