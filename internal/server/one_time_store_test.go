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
func TestOneTimeStoreConsumeIsSingleUse(t *testing.T) {
	states := newOneTimeStore[string](time.Minute, 8, fixedClock(time.Unix(0, 0)))

	states.issue("one", "payload")
	if got, ok := states.consume("one"); !ok || got != "payload" {
		t.Fatalf("刚发出的凭据没有被接受: %q, %v", got, ok)
	}
	if _, ok := states.consume("one"); ok {
		t.Error("同一份凭据被接受了第二次——单次使用没生效")
	}
}

// 服务端没发出过的取值一律不成立。
func TestOneTimeStoreRejectsUnissued(t *testing.T) {
	states := newOneTimeStore[string](time.Minute, 8, fixedClock(time.Unix(0, 0)))

	if _, ok := states.consume("never-issued"); ok {
		t.Error("没发出过的凭据被接受了")
	}
}

// 超时即失效，且过期的取值不会被再次取出。
func TestOneTimeStoreExpire(t *testing.T) {
	now := time.Unix(0, 0)
	states := newOneTimeStore[string](time.Minute, 8, func() time.Time { return now })

	states.issue("one", "payload")
	now = now.Add(time.Minute)
	if _, ok := states.consume("one"); ok {
		t.Error("超时的凭据仍被接受")
	}
}

// 并发取出只能有一个成功：取走与判断必须在同一次加锁里完成。
func TestOneTimeStoreConsumeIsAtomic(t *testing.T) {
	states := newOneTimeStore[string](time.Minute, 8, fixedClock(time.Unix(0, 0)))
	states.issue("one", "payload")

	var accepted int64
	var wg sync.WaitGroup
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, ok := states.consume("one"); ok {
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
func TestOneTimeStoreAreBounded(t *testing.T) {
	now := time.Unix(0, 0)
	states := newOneTimeStore[string](time.Minute, 2, func() time.Time { return now })

	states.issue("first", "payload")
	now = now.Add(time.Second)
	states.issue("second", "payload")
	now = now.Add(time.Second)
	states.issue("third", "payload")

	if len(states.entries) != 2 {
		t.Fatalf("记录了 %d 条，期望不超过上界 2", len(states.entries))
	}
	if _, ok := states.consume("first"); ok {
		t.Error("到上界后应当淘汰最快过期的那一条")
	}
	for _, key := range []string{"second", "third"} {
		if _, ok := states.consume(key); !ok {
			t.Errorf("%s 不该被淘汰", key)
		}
	}
}

// peek 只看不取：它服务于失败路径上的措辞，绝不能顶替 consume 的判定。
func TestOneTimeStorePeekDoesNotConsume(t *testing.T) {
	now := time.Unix(0, 0)
	states := newOneTimeStore[string](time.Minute, 8, func() time.Time { return now })

	states.issue("one", "payload")
	if got, ok := states.peek("one"); !ok || got != "payload" {
		t.Fatalf("peek 没有看到刚发出的凭据: %q, %v", got, ok)
	}
	// 看过之后仍然取得走——peek 不是判定。
	if got, ok := states.consume("one"); !ok || got != "payload" {
		t.Fatalf("peek 之后凭据取不到了: %q, %v", got, ok)
	}
	if _, ok := states.peek("one"); ok {
		t.Error("已取走的凭据仍被 peek 看到")
	}

	// 超时的凭据 peek 仍看得到：它要回答"这次导航本来想做什么"，而超时正是
	// 最需要把话说对的场合。但它照样取不走——判定不听 peek 的。
	states.issue("stale", "payload")
	now = now.Add(time.Minute)
	if _, ok := states.peek("stale"); !ok {
		t.Error("超时的凭据 peek 看不到了")
	}
	if _, ok := states.consume("stale"); ok {
		t.Error("超时的凭据被 consume 放行了")
	}
}

// 腾位置时先清超时条目，不动还有效的那一条。
func TestOneTimeStorePruneBeforeEvict(t *testing.T) {
	now := time.Unix(0, 0)
	states := newOneTimeStore[string](time.Minute, 1, func() time.Time { return now })

	states.issue("stale", "payload")
	now = now.Add(2 * time.Minute) // 它已经过期
	states.issue("fresh", "payload")

	if _, ok := states.consume("fresh"); !ok {
		t.Error("还有效的凭据被淘汰了：应当先清掉超时条目")
	}
}
