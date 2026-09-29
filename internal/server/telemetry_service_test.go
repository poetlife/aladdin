package server

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"

	telemetryv1 "github.com/poetlife/aladdin/api/gen/aladdin/telemetry/v1"
	"github.com/poetlife/aladdin/internal/observability"
	"github.com/poetlife/aladdin/internal/rbac"
	"github.com/poetlife/aladdin/internal/server/interceptor"
	"github.com/poetlife/aladdin/internal/telemetry"
)

// newTestTelemetryService 装配一个把日志留在内存里的上报服务。
func newTestTelemetryService(t *testing.T) (*TelemetryService, *observer.ObservedLogs) {
	t.Helper()
	core, logs := observer.New(zapcore.DebugLevel)
	recorder := telemetry.NewRecorder(zap.New(core), telemetry.NewLimiter(telemetry.DefaultLimits))
	return NewTelemetryService(recorder), logs
}

// 上报**永远成功**：未知动作、不允许匿名都只丢弃，不映射成错误。
//
// 一旦把丢弃升级成错误，一次客户端清单不一致就会让前端弹框、CLI 非零退出——
// 观测影响业务的不该有的形状。
func TestReportEventsAlwaysSucceeds(t *testing.T) {
	svc, logs := newTestTelemetryService(t)

	req := connect.NewRequest(&telemetryv1.ReportEventsRequest{Events: []*telemetryv1.Event{
		{
			Client:  telemetryv1.Client_CLIENT_WEB,
			Surface: telemetryv1.Surface_SURFACE_WEB_AUTH,
			Action:  telemetryv1.Action_ACTION_AUTH_LOGIN,
			Result:  telemetryv1.Result_RESULT_FAIL,
		},
		{Action: telemetryv1.Action_ACTION_UNSPECIFIED},
	}})
	req.Header().Set(observability.HeaderClient, observability.ClientWeb)

	resp, err := svc.ReportEvents(context.Background(), req)
	if err != nil {
		t.Fatalf("上报不应返回错误：%v", err)
	}
	if resp == nil || resp.Msg == nil {
		t.Fatal("应返回一个（空）响应")
	}
	if got := logs.FilterMessage("客户端事件").Len(); got != 1 {
		t.Errorf("应落一行事件日志，得到 %d 行", got)
	}
	if got := logs.FilterMessage("客户端事件被丢弃").Len(); got != 1 {
		t.Errorf("未登记动作应被丢弃并留一行 DEBUG，得到 %d 行", got)
	}
}

// 匿名可以上报"发生在拿到会话之前"的动作，但其余动作匿名时被丢弃。
// 这是公开方法 + 动作白名单这套设计的核心分界。
func TestReportEventsAnonymousBoundary(t *testing.T) {
	svc, logs := newTestTelemetryService(t)

	login := &telemetryv1.Event{
		Client:  telemetryv1.Client_CLIENT_WEB,
		Surface: telemetryv1.Surface_SURFACE_WEB_AUTH,
		Action:  telemetryv1.Action_ACTION_AUTH_LOGIN,
		Result:  telemetryv1.Result_RESULT_FAIL,
	}
	publish := &telemetryv1.Event{
		Client:  telemetryv1.Client_CLIENT_WEB,
		Surface: telemetryv1.Surface_SURFACE_WEB_EDITOR,
		Action:  telemetryv1.Action_ACTION_PUBLISH,
		Result:  telemetryv1.Result_RESULT_OK,
	}

	// 匿名：登录动作放行，发布动作丢弃。
	req := connect.NewRequest(&telemetryv1.ReportEventsRequest{Events: []*telemetryv1.Event{login, publish}})
	req.Header().Set(observability.HeaderClient, observability.ClientWeb)
	if _, err := svc.ReportEvents(context.Background(), req); err != nil {
		t.Fatalf("上报不应返回错误：%v", err)
	}
	if got := logs.FilterMessage("客户端事件").Len(); got != 1 {
		t.Errorf("匿名时只应放行登录动作，落 %d 行", got)
	}

	// 带上主体：两个动作都放行，且事件带 subject_id。换一套日志，避免与上面的
	// 匿名阶段混在一起计数。
	svc, logs = newTestTelemetryService(t)
	ctx := interceptor.WithSubject(context.Background(), rbac.Subject{ID: "sub_1"})
	req = connect.NewRequest(&telemetryv1.ReportEventsRequest{Events: []*telemetryv1.Event{login, publish}})
	req.Header().Set(observability.HeaderClient, observability.ClientWeb)
	if _, err := svc.ReportEvents(ctx, req); err != nil {
		t.Fatalf("上报不应返回错误：%v", err)
	}
	entries := logs.FilterMessage("客户端事件").All()
	if len(entries) != 2 {
		t.Fatalf("已认证时两个动作都应放行，落 %d 行", len(entries))
	}
	for _, entry := range entries {
		if entry.ContextMap()["subject_id"] != "sub_1" {
			t.Errorf("事件应带上主体标识，得到 %v", entry.ContextMap()["subject_id"])
		}
	}
}
