package observability

import (
	"context"
	"testing"
	"time"
)

// nil 接收者必须安全：调用点因此不必为"没有指标"单独判空，
// 也就不会出现"测试里传了 nil、某条分支忘了判空"的崩溃。
func TestMetricsAreNilSafe(t *testing.T) {
	var metrics *Metrics
	metrics.ServerRequest(context.Background(), "Proc", "200", time.Millisecond)
	metrics.RBACDecision(context.Background(), "rbac.role.read", "allow", time.Millisecond)
}

func TestNewMetricsRegistersInstruments(t *testing.T) {
	newTestProvider(t)

	metrics, err := NewMetrics()
	if err != nil {
		t.Fatalf("NewMetrics: %v", err)
	}

	// 记录不应 panic。上报由 SDK 在后台批量完成，这里不断言导出结果——
	// 断言它反而会把测试绑死在 SDK 的内部行为上。
	metrics.ServerRequest(context.Background(), "Proc", "200", 5*time.Millisecond)
	metrics.RBACDecision(context.Background(), "rbac.role.read", "deny", time.Millisecond)
}
