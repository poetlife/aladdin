package telemetry

import (
	"testing"
	"time"
)

func TestLimiterEnforcesCallsAndEvents(t *testing.T) {
	lim := NewLimiter(Limits{CallsPerWindow: 2, EventsPerWindow: 5, MaxKeys: 10, Window: time.Minute})

	if !lim.Allow("k", 1) {
		t.Fatal("第一次调用应放行")
	}
	if !lim.Allow("k", 4) {
		t.Fatal("累计 5 条事件仍在事件上限内，应放行")
	}
	if lim.Allow("k", 0) {
		t.Error("第三次调用超过调用次数上限，应拒绝")
	}

	// 事件条数的分子是**本批的条数**：它防的是"少次多量"。
	other := NewLimiter(Limits{CallsPerWindow: 10, EventsPerWindow: 5, MaxKeys: 10, Window: time.Minute})
	if !other.Allow("k", 5) {
		t.Fatal("恰好触到事件上限应放行")
	}
	if other.Allow("k", 1) {
		t.Error("超过事件上限应拒绝")
	}
}

func TestLimiterResetsPerWindow(t *testing.T) {
	now := time.Unix(0, 0)
	lim := NewLimiter(Limits{CallsPerWindow: 1, EventsPerWindow: 1, MaxKeys: 10, Window: time.Minute})
	lim.now = func() time.Time { return now }

	if !lim.Allow("k", 1) {
		t.Fatal("窗口内第一次应放行")
	}
	if lim.Allow("k", 1) {
		t.Fatal("窗口内第二次应拒绝")
	}
	now = now.Add(time.Minute)
	if !lim.Allow("k", 1) {
		t.Error("跨过窗口后应重新放行")
	}
}

func TestLimiterBoundsTrackedKeys(t *testing.T) {
	now := time.Unix(0, 0)
	lim := NewLimiter(Limits{CallsPerWindow: 5, EventsPerWindow: 5, MaxKeys: 2, Window: time.Minute})
	lim.now = func() time.Time { return now }

	if !lim.Allow("a", 1) || !lim.Allow("b", 1) {
		t.Fatal("容量内的键应放行")
	}
	if lim.Allow("c", 1) {
		t.Error("键数触顶且没有可回收的过期键时，新键应被拒绝（内存有界优先）")
	}

	// 跨窗口后过期的键被回收，新键重新可用。
	now = now.Add(2 * time.Minute)
	if !lim.Allow("c", 1) {
		t.Error("过期键被回收后，新键应放行")
	}
}

func TestLimiterNilReceiverAllows(t *testing.T) {
	var lim *Limiter
	if !lim.Allow("k", 1) {
		t.Error("nil 限流器应无条件放行")
	}
}
