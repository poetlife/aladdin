//go:build e2e

package e2e

import (
	"context"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"

	identityv1 "github.com/poetlife/aladdin/api/gen/aladdin/identity/v1"
	telemetryv1 "github.com/poetlife/aladdin/api/gen/aladdin/telemetry/v1"
	"github.com/poetlife/aladdin/internal/observability"
	"github.com/poetlife/aladdin/internal/rbac"
)

// anonymousConn 建一条**不带凭证**的连接，用来验证公开方法。
func anonymousConn(t *testing.T, h harness) *grpc.ClientConn {
	t.Helper()
	conn, err := grpc.NewClient(h.address,
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("连接失败: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// 请求留痕必须带 client：它让"这个失败是浏览器发的还是命令行发的"不必靠猜。
// 取值只认白名单，因此一个任意值不会被写进日志（那条由单元测试覆盖）。
func TestRequestLogCarriesClient(t *testing.T) {
	h := startServer(t, rbac.RoleViewer, testScope)
	c := h.dial(t, testToken, testScope)

	ctx, cancel := c.Context()
	defer cancel()
	ctx = metadata.AppendToOutgoingContext(ctx, observability.HeaderClient, observability.ClientWeb)

	if _, err := identityv1.NewIdentityServiceClient(c.Conn()).
		WhoAmI(ctx, &identityv1.WhoAmIRequest{}); err != nil {
		t.Fatalf("WhoAmI 失败: %v", err)
	}

	for _, entry := range h.logs.FilterMessage("请求完成").All() {
		fields := entry.ContextMap()
		if fields["procedure"] != "/aladdin.identity.v1.IdentityService/WhoAmI" {
			continue
		}
		if fields["client"] != observability.ClientWeb {
			t.Errorf("client = %v，期望 %q", fields["client"], observability.ClientWeb)
		}
		return
	}
	t.Fatal("没有找到 WhoAmI 的请求留痕")
}

// 匿名也能上报"发生在拿到会话之前"的动作，服务端把它落成一行事件日志。
//
// 这是公开方法 + 动作白名单这套设计的核心分界：登录页上的失败发生在会话之前，
// 要求先认证才能上报是循环依赖。
func TestReportEventsAcceptsAnonymousLoginEvent(t *testing.T) {
	h := startServer(t, rbac.RoleViewer, testScope)
	conn := anonymousConn(t, h)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ctx = metadata.AppendToOutgoingContext(ctx, observability.HeaderClient, observability.ClientWeb)

	_, err := telemetryv1.NewTelemetryServiceClient(conn).ReportEvents(ctx, &telemetryv1.ReportEventsRequest{
		Events: []*telemetryv1.Event{
			{
				Client:  telemetryv1.Client_CLIENT_WEB,
				Surface: telemetryv1.Surface_SURFACE_WEB_AUTH,
				Action:  telemetryv1.Action_ACTION_AUTH_LOGIN,
				Result:  telemetryv1.Result_RESULT_FAIL,
				Attrs:   map[string]string{"channel": "password"},
			},
		},
	})
	if err != nil {
		t.Fatalf("匿名的登录失败上报不应被拒绝: %v", err)
	}

	for _, entry := range h.logs.FilterMessage("客户端事件").All() {
		fields := entry.ContextMap()
		if fields["action"] != "auth.login" {
			continue
		}
		if fields["client"] != observability.ClientWeb {
			t.Errorf("client = %v，期望 %q", fields["client"], observability.ClientWeb)
		}
		if fields["result"] != "fail" {
			t.Errorf("result = %v，期望 fail", fields["result"])
		}
		if fields["attr_channel"] != "password" {
			t.Errorf("attr_channel = %v，期望 password", fields["attr_channel"])
		}
		if _, ok := fields["subject_id"]; ok {
			t.Error("匿名上报不应带 subject_id")
		}
		return
	}
	t.Fatal("没有找到 auth.login 的事件日志")
}

// 未登记的动作只被丢弃，**不报错**：遥测的数据问题绝不能升级成客户端的业务错误。
func TestReportEventsDropsUnknownActionWithoutError(t *testing.T) {
	h := startServer(t, rbac.RoleViewer, testScope)
	conn := anonymousConn(t, h)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := telemetryv1.NewTelemetryServiceClient(conn).ReportEvents(ctx, &telemetryv1.ReportEventsRequest{
		Events: []*telemetryv1.Event{
			{Action: telemetryv1.Action_ACTION_UNSPECIFIED},
		},
	})
	if err != nil {
		t.Fatalf("未登记动作不应让调用失败: %v", err)
	}
	if got := h.logs.FilterMessage("客户端事件").Len(); got != 0 {
		t.Errorf("未登记动作不应落事件日志，落了 %d 行", got)
	}
	if got := h.logs.FilterMessage("客户端事件被丢弃").Len(); got == 0 {
		t.Error("丢弃应留一行 DEBUG 说明原因")
	}
}
