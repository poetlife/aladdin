package observability

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// 指标名与属性键的唯一来源。调用方不得手写字符串字面量。
const (
	metricServerRequests   = "aladdin.server.requests"
	metricServerDuration   = "aladdin.server.request.duration"
	metricRBACDecisions    = "aladdin.rbac.decisions"
	metricRBACDecisionTime = "aladdin.rbac.decision.duration"

	attrProcedure  = "procedure"
	attrCode       = "code"
	attrPermission = "permission"
	attrDecision   = "decision"
)

// Metrics 是本进程上报的指标集合。
//
// 方法对 nil 接收者安全：调用点不必为"没有指标"单独判空，也就不会出现
// "测试里传了 nil、某条分支忘了判空"的崩溃。
type Metrics struct {
	serverRequests   metric.Int64Counter
	serverDuration   metric.Float64Histogram
	rbacDecisions    metric.Int64Counter
	rbacDecisionTime metric.Float64Histogram
}

// NewMetrics 从全局 meter provider 取回本进程的指标。
//
// 必须在 Provider 构建之后调用：otel.Meter 取的是调用当时注册的 provider，
// 提前调用会拿到一个什么都不做的 meter，且不会报错——只是指标永远为空。
func NewMetrics() (*Metrics, error) {
	meter := otel.Meter(scopeName)

	requests, err := meter.Int64Counter(metricServerRequests,
		metric.WithDescription("RPC 请求总数，按过程与结果码分组"))
	if err != nil {
		return nil, fmt.Errorf("注册指标 %s 失败: %w", metricServerRequests, err)
	}

	duration, err := meter.Float64Histogram(metricServerDuration,
		metric.WithUnit("s"),
		metric.WithDescription("RPC 请求耗时"))
	if err != nil {
		return nil, fmt.Errorf("注册指标 %s 失败: %w", metricServerDuration, err)
	}

	decisions, err := meter.Int64Counter(metricRBACDecisions,
		metric.WithDescription("鉴权决策计数，按权限码与结论分组"))
	if err != nil {
		return nil, fmt.Errorf("注册指标 %s 失败: %w", metricRBACDecisions, err)
	}

	decisionTime, err := meter.Float64Histogram(metricRBACDecisionTime,
		metric.WithUnit("s"),
		metric.WithDescription("鉴权决策耗时"))
	if err != nil {
		return nil, fmt.Errorf("注册指标 %s 失败: %w", metricRBACDecisionTime, err)
	}

	return &Metrics{
		serverRequests:   requests,
		serverDuration:   duration,
		rbacDecisions:    decisions,
		rbacDecisionTime: decisionTime,
	}, nil
}

// ServerRequest 记录一次 RPC 请求。
//
// procedure 与 code 都必须是有界取值（RPC 过程名、结果码）。传入原始 URL、
// 用户输入或主体标识会让时序数量随流量增长，最终把存储打爆。
func (m *Metrics) ServerRequest(ctx context.Context, procedure, code string, elapsed time.Duration) {
	if m == nil {
		return
	}
	m.serverRequests.Add(ctx, 1, metric.WithAttributes(
		attribute.String(attrProcedure, procedure),
		attribute.String(attrCode, code),
	))
	m.serverDuration.Record(ctx, elapsed.Seconds(), metric.WithAttributes(
		attribute.String(attrProcedure, procedure),
	))
}

// RBACDecision 记录一次鉴权决策。
//
// 与决策日志配套：日志回答"这一次为什么",指标回答"整体上是不是不对劲"——
// 例如某个权限码的拒绝数突然抬升，通常意味着有人在试探。
func (m *Metrics) RBACDecision(ctx context.Context, permission, decision string, elapsed time.Duration) {
	if m == nil {
		return
	}
	m.rbacDecisions.Add(ctx, 1, metric.WithAttributes(
		attribute.String(attrPermission, permission),
		attribute.String(attrDecision, decision),
	))
	m.rbacDecisionTime.Record(ctx, elapsed.Seconds(), metric.WithAttributes(
		attribute.String(attrPermission, permission),
	))
}
