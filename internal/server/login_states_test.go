package server

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// 这些用例拨动时间，而不是等时间过去：有效期以分钟计，真等一遍既慢又不稳。
func fixedClock(at time.Time) func() time.Time {
	return func() time.Time { return at }
}

// 凭据用过即作废：同一份凭据的第二次取出不成立。
func TestLoginStatesConsumeIsSingleUse(t *testing.T) {
	states := newLoginStates(time.Minute, 8, fixedClock(time.Unix(0, 0)))

	states.issue("one")
	if !states.consume("one") {
		t.Fatal("刚发出的凭据没有被接受")
	}
	if states.consume("one") {
		t.Error("同一份凭据被接受了第二次——单次使用没生效")
	}
}

// 服务端没发出过的取值一律不成立。
func TestLoginStatesRejectUnissued(t *testing.T) {
	states := newLoginStates(time.Minute, 8, fixedClock(time.Unix(0, 0)))

	if states.consume("never-issued") {
		t.Error("没发出过的凭据被接受了")
	}
}

// 超时即失效。
func TestLoginStatesExpire(t *testing.T) {
	now := time.Unix(0, 0)
	states := newLoginStates(time.Minute, 8, func() time.Time { return now })

	states.issue("one")
	now = now.Add(time.Minute)
	if states.consume("one") {
		t.Error("超时的凭据仍被接受")
	}
}

// 并发回调只能有一个成功：取走与判断必须在同一次加锁里完成。
func TestLoginStatesConsumeIsAtomic(t *testing.T) {
	states := newLoginStates(time.Minute, 8, fixedClock(time.Unix(0, 0)))
	states.issue("one")

	var accepted int64
	var wg sync.WaitGroup
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if states.consume("one") {
				atomic.AddInt64(&accepted, 1)
			}
		}()
	}
	wg.Wait()

	if accepted != 1 {
		t.Errorf("同一份凭据被接受了 %d 次，期望 1 次", accepted)
	}
}

// 表**有上界**：发起登录的地址谁都能调，没有上界就是一张可以随便写大的表。
func TestLoginStatesAreBounded(t *testing.T) {
	now := time.Unix(0, 0)
	states := newLoginStates(time.Minute, 2, func() time.Time { return now })

	states.issue("first")
	now = now.Add(time.Second)
	states.issue("second")
	now = now.Add(time.Second)
	states.issue("third")

	if len(states.entries) != 2 {
		t.Fatalf("记录了 %d 条，期望不超过上界 2", len(states.entries))
	}
	if states.consume("first") {
		t.Error("到上界后应当淘汰最快过期的那一条")
	}
	for _, state := range []string{"second", "third"} {
		if !states.consume(state) {
			t.Errorf("%s 不该被淘汰", state)
		}
	}
}

// 腾位置时先清超时条目，不动还有效的那一条。
func TestLoginStatesPruneBeforeEvict(t *testing.T) {
	now := time.Unix(0, 0)
	states := newLoginStates(time.Minute, 1, func() time.Time { return now })

	states.issue("stale")
	now = now.Add(2 * time.Minute) // 它已经过期
	states.issue("fresh")

	if !states.consume("fresh") {
		t.Error("还有效的凭据被淘汰了：应当先清掉超时条目")
	}
}
