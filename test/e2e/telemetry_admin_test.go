//go:build e2e

package e2e

import (
	"context"
	"net/http"
	"testing"
	"time"

	"connectrpc.com/connect"

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
