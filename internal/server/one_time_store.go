package server

import (
	"sync"
	"time"
)

// oneTimeEntry 是一条已发出、尚未使用的凭据。
type oneTimeEntry[T any] struct {
	value     T
	expiresAt time.Time
}

// oneTimeStore 是一张**已发出、尚未使用**的一次性凭据表。
//
// 登录导航状态与待绑定凭据都长在它上面：两者的"发出 / 原子取走 / 超时 /
// 上界 / 淘汰"语义完全相同，分开写两份迟早会有一份漏掉（见
// docs/ssot-registry.md）。
//
// 它把"凭据单次使用"落在服务端：取走一份凭据，同一份再来一次就查不到。
// 只靠 cookie 记不住这件事——cookie 由浏览器保管，而"这份凭据用过没有"
// 是服务端的事实。
//
// 它**必须有上界**：发起登录的地址谁都能调，一张没有上界的表会被打进内存。
// 超时的条目会先被清掉，到上界时淘汰最快过期的那条——淘汰的代价是一次进行
// 中的流程要重来，比无界增长小得多。
//
// 它**不跨重启**：进程重启后进行中的流程需要重来。这是有意的取舍——这些
// 记录的有效期以分钟计，为它建一张表并写一次迁移不划算。
type oneTimeStore[T any] struct {
	mu      sync.Mutex
	entries map[string]oneTimeEntry[T]
	ttl     time.Duration
	max     int

	// now 允许测试拨动时间；生产上就是 time.Now。
	now func() time.Time
}

// newOneTimeStore 构造一张有界的一次性凭据表。
func newOneTimeStore[T any](ttl time.Duration, max int, now func() time.Time) *oneTimeStore[T] {
	return &oneTimeStore[T]{
		entries: make(map[string]oneTimeEntry[T]),
		ttl:     ttl,
		max:     max,
		now:     now,
	}
}

// issue 记住一份刚刚发出的凭据。
func (s *oneTimeStore[T]) issue(key string, value T) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now()
	s.pruneLocked(now)
	if len(s.entries) >= s.max {
		s.evictSoonestLocked()
	}
	s.entries[key] = oneTimeEntry[T]{value: value, expiresAt: now.Add(s.ttl)}
}

// consume 取走一份凭据，并报告它是否成立。
//
// **取走与判断必须在同一次加锁里完成**：分开写的话，两次并发的兑换会各查到
// 一次"这份凭据还在"，然后各自继续——那正是"单次使用"要消灭的形状。过期的
// 凭据按不存在处理，并且照样被清掉。
func (s *oneTimeStore[T]) consume(key string) (T, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	entry, ok := s.entries[key]
	if !ok {
		var zero T
		return zero, false
	}
	delete(s.entries, key)
	if !s.now().Before(entry.expiresAt) {
		var zero T
		return zero, false
	}
	return entry.value, true
}

// peek 看一眼一份凭据，**不取走、也不判过期**。
//
// 它只服务于失败路径上的措辞（失败标记与日志用语）：判定仍然只走 consume，
// 单次使用与有效期都只由 consume 说了算——**拿 peek 当判定用就丢掉了这些性质**。
//
// 正因为不判过期，它才能回答"这次导航本来想做什么"：一份超时的导航恰恰是
// 最需要把话说对的场合。已被取走的凭据查不到（consume 已把它删掉）。
func (s *oneTimeStore[T]) peek(key string) (T, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	entry, ok := s.entries[key]
	if !ok {
		var zero T
		return zero, false
	}
	return entry.value, true
}

// pruneLocked 清掉已过期的条目。
func (s *oneTimeStore[T]) pruneLocked(now time.Time) {
	for key, entry := range s.entries {
		if !now.Before(entry.expiresAt) {
			delete(s.entries, key)
		}
	}
}

// evictSoonestLocked 淘汰最快过期的一条。
//
// 它只在"上界已被占满"时发生，因此这次扫描罕见且有界。
func (s *oneTimeStore[T]) evictSoonestLocked() {
	var soonest string
	var soonestAt time.Time
	for key, entry := range s.entries {
		if soonest == "" || entry.expiresAt.Before(soonestAt) {
			soonest, soonestAt = key, entry.expiresAt
		}
	}
	delete(s.entries, soonest)
}
