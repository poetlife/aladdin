package watch

// Subscription 是 Hub 上的一条订阅：它持有一组主题，交付的是"哪个主题变了"。
//
// **交付是合并后的**：同一主题上的连续变化在待取集合里合成一项，因此订阅者再慢
// 也不会丢变更（它只是晚一点知道），也不会因为"积压"而被断开——待取集合的大小
// 以主题集合为上限。
//
// 全部可变状态由 Hub 的那把锁保护（见 Hub.mu 的说明）；这里不为它们另开一把锁。
type Subscription struct {
	hub *Hub
	// topics 是这条订阅当前订着的主题。Hub.Close 会把一个主题从这里摘掉。
	topics map[string]struct{}
	// pending 是**还没被取走**的、变过的主题（同一主题只留一项）。
	pending map[string]struct{}
	// ready 缓冲 1：有东西可取了。订阅方取走一项之后，若还有剩余会再摇一次。
	ready chan struct{}
	// done 在订阅结束时关闭。
	done chan struct{}
	// finished 由 retire 置位。
	finished bool
}

// Ready 在有待取的主题时可就绪。
//
// 取用顺序是**先 Ready 再 Take**：Ready 只是"有事可做"的铃，Take 才给出是哪条
// 主题。中间没有竞态——Take 拿到的永远是当前待取集合里的一项，而 Ready 与待取
// 集合是否为空始终保持一致。
func (s *Subscription) Ready() <-chan struct{} { return s.ready }

// Done 在这条订阅结束时关闭：调用方退订，或它订的主题全部被 Hub.Close 摘光。
//
// 订阅方应当把它放进 select 里：它可读即意味着这条订阅不会再交付任何东西。
func (s *Subscription) Done() <-chan struct{} { return s.done }

// Take 取走一条待重拉的主题；没有待取的返回 ok 为假。
//
// 它取一条就走，调用方通常循环回来再看一次 Ready。为了让"**Ready 可读 ⟺ 有待取
// 项**"这条不变量真的成立（否则订阅方会收到没有对应内容的假就绪），取走之后要么
// 把铃重新摇响（还有剩余），要么把铃清掉（取空了）。
func (s *Subscription) Take() (string, bool) {
	s.hub.mu.Lock()
	defer s.hub.mu.Unlock()
	for topic := range s.pending {
		delete(s.pending, topic)
		if len(s.pending) > 0 {
			s.signal()
		} else {
			s.clear()
		}
		return topic, true
	}
	return "", false
}

// Close 退订：结束这条订阅（幂等）。
//
// 它是调用方的责任——总线无从知道一条订阅什么时候不再被使用；一条流结束时必须
// 调用它。
func (s *Subscription) Close() {
	s.hub.mu.Lock()
	defer s.hub.mu.Unlock()
	s.hub.retire(s)
}

// signal 摇一次就绪铃。
//
// 铃是缓冲 1 的，重复摇没有额外含义（"还有东西可取"这一件事只需要表达一次）。
// 调用方必须持有 Hub 的锁。已结束的订阅不再摇铃：它的 ready 不会再被读。
func (s *Subscription) signal() {
	if s.finished {
		return
	}
	select {
	case s.ready <- struct{}{}:
	default:
	}
}

// clear 把铃清掉（待取集合已经空了）。调用方必须持有 Hub 的锁。
func (s *Subscription) clear() {
	select {
	case <-s.ready:
	default:
	}
}
