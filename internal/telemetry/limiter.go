package telemetry

import (
	"sync"
	"time"
)

// Limits 是上报的两层上限与内存上限。
//
// 两层分别防两件事：调用次数防"用上报接口刷请求"，事件条数防"一次调用塞一大
// 批"。只有一层的话，前者能靠批量绕过（少次多量），后者能靠拆批绕过（多次少量）。
type Limits struct {
	// CallsPerWindow 是每个键在每个窗口内允许的 ReportEvents 调用次数。
	CallsPerWindow int
	// EventsPerWindow 是每个键在每个窗口内允许的事件总条数。
	EventsPerWindow int
	// MaxKeys 是同时跟踪的键数上限，用来给内存封顶。
	MaxKeys int
	// Window 是窗口长度。
	Window time.Duration
}

// DefaultLimits 是默认上限。
//
// 取值对齐客户端缓冲的节奏：条数阈值 20、时间窗 3 秒，一个键一分钟最多约
// 20 次调用、400 条事件，因此 30 / 600 留了足够余量，同时又远小于"把它当
// 免费日志通道"所需的量级。MaxKeys 取 4096：键是主体标识或来源地址，正常
// 部署里远达不到，达到即说明有人在批量制造主体。
var DefaultLimits = Limits{
	CallsPerWindow:  30,
	EventsPerWindow: 600,
	MaxKeys:         4096,
	Window:          time.Minute,
}

// Limiter 是按键的固定窗口限流器。
//
// 固定窗口而不是滑动窗口：它能给出确定的内存上界（每个键一个计数器），而
// 滑动窗口要么为每个键保留时间序列、要么引入近似，两者对"防止被当日志通道"
// 这个目的都过度了。代价是窗口边界处的突发，而这里的后果只是多放行一小批
// 遥测，不影响任何业务判定。
type Limiter struct {
	limits Limits
	// now 可注入，测试用它把窗口推进到需要的位置。
	now     func() time.Time
	mu      sync.Mutex
	entries map[string]*counter
}

// counter 是一个键在当前窗口内的计数。
type counter struct {
	start  time.Time
	calls  int
	events int
}

// NewLimiter 构造限流器。limits 为零值时取 DefaultLimits。
func NewLimiter(limits Limits) *Limiter {
	if limits == (Limits{}) {
		limits = DefaultLimits
	}
	return &Limiter{
		limits:  limits,
		now:     time.Now,
		entries: make(map[string]*counter),
	}
}

// Allow 判定一次调用能不能放行，并计入本次调用与条数。
//
// 它对 nil 接收者安全：测试与"未装配"的调用方不必为它判空。
//
// 被拒绝时**仍然记一次调用**：否则一个反复超限的客户端会永远停在"刚好不超"
// 的位置上，而限流的意义正是让它持续被拒到窗口过去。
func (l *Limiter) Allow(key string, events int) bool {
	if l == nil {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	c, ok := l.entries[key]
	if !ok {
		if len(l.entries) >= l.limits.MaxKeys {
			l.sweep(now)
			if len(l.entries) >= l.limits.MaxKeys {
				// 内存有界优先于"这条能过"：丢的只是遥测，不影响任何业务。
				return false
			}
		}
		c = &counter{start: now}
		l.entries[key] = c
	} else if now.Sub(c.start) >= l.limits.Window {
		c.start, c.calls, c.events = now, 0, 0
	}

	c.calls++
	if c.calls > l.limits.CallsPerWindow {
		return false
	}
	if c.events+events > l.limits.EventsPerWindow {
		return false
	}
	c.events += events
	return true
}

// sweep 清掉过期的键。只在键数触顶时调用，因此它的开销被限制在真正需要的时刻。
func (l *Limiter) sweep(now time.Time) {
	for key, c := range l.entries {
		if now.Sub(c.start) >= l.limits.Window {
			delete(l.entries, key)
		}
	}
}
