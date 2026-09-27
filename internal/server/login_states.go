package server

import (
	"sync"
	"time"
)

// loginStates 是**已发出、尚未使用**的浏览器直连登录凭据。
//
// 它把"凭据单次使用"落在服务端：回调时原子地取走一份凭据，同一份凭据再来一次
// 就查不到（见 docs/design/identity/github-login.md）。只靠 cookie 记不住这件事
// ——cookie 由浏览器保管，而"这份凭据用过没有"是服务端的事实。
//
// 它**必须有上界**：发起登录的地址谁都能调，一张没有上界的表会被打进内存。超时
// 的条目会先被清掉，到上界时淘汰最快过期的那条——淘汰的代价是一次进行中的登录
// 要重来，比无界增长小得多。
//
// 它**不跨重启**：进程重启后进行中的登录需要重来。这是有意的取舍——这份记录的
// 有效期以分钟计，为它建一张表并写一次迁移不划算。
type loginStates struct {
	mu      sync.Mutex
	entries map[string]time.Time
	ttl     time.Duration
	max     int

	// now 允许测试拨动时间；生产上就是 time.Now。
	now func() time.Time
}

// newLoginStates 构造一张有界的一次性凭据表。
func newLoginStates(ttl time.Duration, max int, now func() time.Time) *loginStates {
	return &loginStates{
		entries: make(map[string]time.Time),
		ttl:     ttl,
		max:     max,
		now:     now,
	}
}

// issue 记住一份刚刚发出的凭据。
func (s *loginStates) issue(state string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now()
	s.pruneLocked(now)
	if len(s.entries) >= s.max {
		s.evictSoonestLocked()
	}
	s.entries[state] = now.Add(s.ttl)
}

// consume 取走一份凭据，并报告它是否成立。
//
// **取走与判断必须在同一次加锁里完成**：分开写的话，两次并发的回调会各查到一次
// "这份凭据还在"，然后各签发一份会话——那正是"单次使用"要消灭的形状。过期的
// 凭据按不存在处理，并且照样被清掉。
func (s *loginStates) consume(state string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	expiresAt, ok := s.entries[state]
	if !ok {
		return false
	}
	delete(s.entries, state)
	return s.now().Before(expiresAt)
}

// pruneLocked 清掉已过期的条目。
func (s *loginStates) pruneLocked(now time.Time) {
	for state, expiresAt := range s.entries {
		if !now.Before(expiresAt) {
			delete(s.entries, state)
		}
	}
}

// evictSoonestLocked 淘汰最快过期的一条。
//
// 它只在"上界已被占满"时发生，因此这次扫描罕见且有界。
func (s *loginStates) evictSoonestLocked() {
	var soonest string
	var soonestAt time.Time
	for state, expiresAt := range s.entries {
		if soonest == "" || expiresAt.Before(soonestAt) {
			soonest, soonestAt = state, expiresAt
		}
	}
	delete(s.entries, soonest)
}
