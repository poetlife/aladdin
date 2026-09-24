//go:build e2e

package e2e

import (
	"context"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	grpc_health_v1 "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"

	identityv1 "github.com/poetlife/aladdin/api/gen/aladdin/identity/v1"
	"github.com/poetlife/aladdin/internal/observability"
	"github.com/poetlife/aladdin/internal/rbac"
)

const (
	// 一个合法的入站 traceparent。
	incomingTraceparent = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	incomingTraceID     = "4bf92f3577b34da6a3ce929d0e0e4736"
	incomingSpanID      = "00f067aa0ba902b7"
)

// whoAmIHeaders 发一次调用并取回响应头。
func whoAmIHeaders(t *testing.T, h harness, outgoingTraceparent string) metadata.MD {
	t.Helper()

	c := h.dial(t, testToken, testScope)

	ctx, cancel := c.Context()
	defer cancel()
	if outgoingTraceparent != "" {
		ctx = metadata.AppendToOutgoingContext(ctx,
			observability.TraceparentHeader, outgoingTraceparent)
	}

	var header metadata.MD
	if _, err := identityv1.NewIdentityServiceClient(c.Conn()).
		WhoAmI(ctx, &identityv1.WhoAmIRequest{}, grpc.Header(&header)); err != nil {
		t.Fatalf("WhoAmI 失败: %v", err)
	}
	return header
}

// echoedTraceparent 取出服务端回写的 traceparent，并要求它格式合法。
func echoedTraceparent(t *testing.T, header metadata.MD) []string {
	t.Helper()

	values := header.Get(observability.TraceparentHeader)
	if len(values) == 0 {
		t.Fatalf("响应头里没有 %s，实际响应头为 %v",
			observability.TraceparentHeader, header)
	}
	parts := strings.Split(values[0], "-")
	if len(parts) != 4 {
		t.Fatalf("回写的 traceparent 形状不对: %q", values[0])
	}
	if parts[0] != "00" || len(parts[1]) != 32 || len(parts[2]) != 16 || len(parts[3]) != 2 {
		t.Fatalf("回写的 traceparent 不合规范: %q", values[0])
	}
	return parts
}

// 服务端必须在响应头回写 traceparent：**trace-id 与请求一致、span-id 是自己的**。
//
// 回写自己的 span-id 而不是原样回显请求头，客户端才能拿到"服务端在哪一步
// 处理了这次请求"；只回显请求头的话，客户端手里的信息不比进来时更多。
func TestResponseCarriesTraceparent(t *testing.T) {
	h := startServer(t, rbac.RoleViewer, testScope)

	parts := echoedTraceparent(t, whoAmIHeaders(t, h, incomingTraceparent))

	if parts[1] != incomingTraceID {
		t.Errorf("回写的 trace-id = %q，期望继承请求的 %q", parts[1], incomingTraceID)
	}
	if parts[2] == incomingSpanID {
		t.Error("回写的 span-id 等于请求里的值：服务端必须有自己的 span")
	}
}

// 没有入站 traceparent 时也要回写：链路的一端总是要有起点。
func TestResponseCarriesTraceparentWithoutIncoming(t *testing.T) {
	h := startServer(t, rbac.RoleViewer, testScope)

	parts := echoedTraceparent(t, whoAmIHeaders(t, h, ""))

	if parts[1] == incomingTraceID {
		t.Error("没有入站值时不应凭空出现那个 trace-id")
	}
	if strings.Trim(parts[1], "0") == "" {
		t.Error("trace-id 是全零，不是合法值")
	}
}

// 非法的入站 traceparent 绝不能被继承，也不能被原样回显。
//
// 不校验的后果有两层：日志里出现格式非法的 trace_id 会让所有基于它的检索
// 失效；而"入站值优先"意味着调用方可以用一个固定值把不同请求的日志并成
// 一条链路——这是可观测性数据的完整性问题。
//
// 同时断言它**不导致请求失败**：观测数据的格式问题不能升级成业务故障。
func TestInvalidIncomingTraceparentRejected(t *testing.T) {
	cases := map[string]string{
		"trace-id 全零": "00-00000000000000000000000000000000-00f067aa0ba902b7-01",
		"大写十六进制":      "00-4BF92F3577B34DA6A3CE929D0E0E4736-00F067AA0BA902B7-01",
		"长度不对":        "00-4bf92f-00f067aa0ba902b7-01",
		"根本不是这个格式":    "abc",
	}

	for name, bad := range cases {
		t.Run(name, func(t *testing.T) {
			h := startServer(t, rbac.RoleViewer, testScope)

			// 请求本身必须成功：畸形传播头不是业务错误。
			parts := echoedTraceparent(t, whoAmIHeaders(t, h, bad))

			if parts[1] == "00000000000000000000000000000000" {
				t.Error("继承了全零的 trace-id")
			}
			if parts[1] == incomingTraceID {
				t.Error("非法入站值被当作了有效值")
			}
		})
	}
}

// 响应里还有一个给人看的 x-trace-id：直接给 32 位 trace-id，复制即可搜日志。
//
// 它与 traceparent **必须同源**——两个 ID 不一致比只有一个更糟，排障的人会
// 拿它们互相搜，两边都搜不到。
func TestResponseCarriesPlainTraceID(t *testing.T) {
	h := startServer(t, rbac.RoleViewer, testScope)
	header := whoAmIHeaders(t, h, incomingTraceparent)

	traceIDFromTraceparent := echoedTraceparent(t, header)[1]

	values := header.Get(observability.ResponseTraceIDHeader)
	if len(values) == 0 {
		t.Fatalf("响应头里没有 %s，实际为 %v", observability.ResponseTraceIDHeader, header)
	}
	if values[0] != traceIDFromTraceparent {
		t.Errorf("%s = %q，与 traceparent 里的 trace-id %q 不一致",
			observability.ResponseTraceIDHeader, values[0], traceIDFromTraceparent)
	}
	if len(values[0]) != 32 {
		t.Errorf("%s = %q，期望 32 位十六进制", observability.ResponseTraceIDHeader, values[0])
	}
}

// 入站的 x-trace-id 必须被忽略：它只在响应方向有意义，不是传播机制。
//
// 接受它就等于把自定义头重新变成传播协议，标准组件再也接不上链路。
func TestIncomingPlainTraceIDIgnored(t *testing.T) {
	h := startServer(t, rbac.RoleViewer, testScope)

	const injected = "aaaabbbbccccddddeeeeffff00001111"

	c := h.dial(t, testToken, testScope)
	ctx, cancel := c.Context()
	defer cancel()
	ctx = metadata.AppendToOutgoingContext(ctx, observability.ResponseTraceIDHeader, injected)

	var header metadata.MD
	if _, err := identityv1.NewIdentityServiceClient(c.Conn()).
		WhoAmI(ctx, &identityv1.WhoAmIRequest{}, grpc.Header(&header)); err != nil {
		t.Fatalf("WhoAmI 失败: %v", err)
	}

	if got := header.Get(observability.ResponseTraceIDHeader); len(got) > 0 && got[0] == injected {
		t.Error("入站的 x-trace-id 被当成了链路标识")
	}
}

// 健康检查与反射属于基础设施过程，但链路依旧要通——否则"服务是否健康"
// 这件事本身不可观测。
func TestInfraProcedureCarriesTraceparent(t *testing.T) {
	h := startServer(t, rbac.RoleViewer, testScope)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, err := grpc.NewClient(h.address,
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("连接失败: %v", err)
	}
	defer func() { _ = conn.Close() }()

	var header metadata.MD
	if err := conn.Invoke(ctx, "/grpc.health.v1.Health/Check",
		&grpc_health_v1.HealthCheckRequest{},
		&grpc_health_v1.HealthCheckResponse{},
		grpc.Header(&header),
	); err != nil {
		t.Fatalf("健康检查调用失败: %v", err)
	}

	echoedTraceparent(t, header)
}
