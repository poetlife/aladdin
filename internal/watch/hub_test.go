package watch

import (
	"sync"
	"testing"
)

// ready 非阻塞地报告"有事可做"。
func ready(sub *Subscription) bool {
	select {
	case <-sub.Ready():
		return true
	default:
		return false
	}
}

// ended 非阻塞地报告这条订阅是否已结束。
func ended(sub *Subscription) bool {
	select {
	case <-sub.Done():
		return true
	default:
		return false
	}
}

// 发布到达订了这条主题的订阅者。
func TestPublishReachesSubscribersOfThatTopic(t *testing.T) {
	hub := NewHub()
	sub := hub.Subscribe([]string{"galaxy.project/甲"})
	t.Cleanup(sub.Close)

	hub.Publish("galaxy.project/甲")

	if !ready(sub) {
		t.Fatal("发布之后没有就绪")
	}
	topic, ok := sub.Take()
	if !ok || topic != "galaxy.project/甲" {
		t.Errorf("取到 (%q, %v)，期望 (%q, true)", topic, ok, "galaxy.project/甲")
	}
}

// 主题之间互不串台：订了别人的人不该收到。
func TestPublishIsScopedToTheTopic(t *testing.T) {
	hub := NewHub()
	other := hub.Subscribe([]string{"galaxy.project/乙"})
	t.Cleanup(other.Close)

	hub.Publish("galaxy.project/甲")

	if ready(other) {
		t.Error("订了别的主题却就绪了")
	}
}

// **同一主题上的连续变化合并成一次**。
//
// 这是"订阅者慢"不成问题的根据：它最多晚一点知道，不会因为积压而丢变更，也不会
// 因为积压而被断开。
func TestRepeatedChangesCoalesce(t *testing.T) {
	hub := NewHub()
	sub := hub.Subscribe([]string{"galaxy.project/甲"})
	t.Cleanup(sub.Close)

	for i := 0; i < 100; i++ {
		hub.Publish("galaxy.project/甲")
	}

	if !ready(sub) {
		t.Fatal("发布之后没有就绪")
	}
	if topic, ok := sub.Take(); !ok || topic != "galaxy.project/甲" {
		t.Fatalf("取到 (%q, %v)，期望取到那条主题", topic, ok)
	}
	if _, ok := sub.Take(); ok {
		t.Error("同一主题的连续变化没有合并，还能再取一项")
	}
}

// **一条订阅承载多个主题**——这正是"一条连接、各业务只加主题"的实现面。
func TestOneSubscriptionCarriesEveryTopic(t *testing.T) {
	hub := NewHub()
	sub := hub.Subscribe([]string{"galaxy.project/甲", "galaxy.project/乙"})
	t.Cleanup(sub.Close)

	hub.Publish("galaxy.project/乙")
	hub.Publish("galaxy.project/甲")

	got := map[string]bool{}
	for range 2 {
		if !ready(sub) {
			t.Fatal("还有待取的主题，却没有就绪")
		}
		topic, ok := sub.Take()
		if !ok {
			t.Fatal("就绪了却取不到东西")
		}
		got[topic] = true
	}
	if !got["galaxy.project/甲"] || !got["galaxy.project/乙"] {
		t.Errorf("取到的主题 = %v，期望两条都在", got)
	}
	// 取空之后就不再就绪：Ready 与"有待取项"始终一致。
	if ready(sub) {
		t.Error("取空之后仍然就绪")
	}
}

// 退订是调用方的责任且幂等；退订之后这个主题不再投给它。
func TestSubscriptionCloseIsIdempotent(t *testing.T) {
	hub := NewHub()
	sub := hub.Subscribe([]string{"galaxy.project/甲"})

	sub.Close()
	sub.Close()

	hub.Publish("galaxy.project/甲")

	if ready(sub) {
		t.Error("退订之后仍然收到事件")
	}
	if !ended(sub) {
		t.Error("退订之后 Done 没有关闭")
	}
}

// Hub.Close 表示"这个主题背后的资源不存在了"：那条主题退场，**其余主题照常**。
//
// 一条订阅只在它的主题集合被摘空时才结束——多路复用下，一个资源被删不该把整条
// 连接掐掉。
func TestCloseTopicKeepsTheRestOfTheSubscription(t *testing.T) {
	hub := NewHub()
	sub := hub.Subscribe([]string{"galaxy.project/甲", "galaxy.project/乙"})
	t.Cleanup(sub.Close)

	hub.Close("galaxy.project/甲")

	if ended(sub) {
		t.Fatal("还有主题活着，订阅不该结束")
	}
	hub.Publish("galaxy.project/乙")
	if topic, ok := sub.Take(); !ok || topic != "galaxy.project/乙" {
		t.Errorf("取到 (%q, %v)，期望另一条主题仍然可达", topic, ok)
	}
	// 已经不存在的主题再发布也不该投过来。
	hub.Publish("galaxy.project/甲")
	if ready(sub) {
		t.Error("被 Close 的主题仍在投递")
	}
}

// 主题集合被摘空时，订阅随之结束（调用方据此收摊，见 Done 的说明）。
func TestSubscriptionEndsWhenItsLastTopicCloses(t *testing.T) {
	hub := NewHub()
	sub := hub.Subscribe([]string{"galaxy.project/甲", "galaxy.project/乙"})

	hub.Close("galaxy.project/甲")
	hub.Close("galaxy.project/乙")

	if !ended(sub) {
		t.Error("主题集合空了，订阅却没有结束")
	}
}

// 发布、订阅、退订、取用同时发生：要证明的是"没有竞态、没有向已关闭的通道发送、
// 没有重复关闭"，而不是某个具体结果。用例本身给 `-race` 用。
func TestHubIsRaceFree(t *testing.T) {
	hub := NewHub()
	const topics = 8
	const rounds = 500

	names := make([]string, 0, topics)
	for i := range topics {
		names = append(names, Topic("galaxy.project", string(rune('a'+i))))
	}

	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range rounds {
				sub := hub.Subscribe(names)
				go func() {
					for {
						select {
						case <-sub.Done():
							return
						case <-sub.Ready():
							sub.Take()
						}
					}
				}()
				sub.Close()
				sub.Close()
			}
		}()
	}
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range rounds {
				hub.Publish(names[i%topics])
			}
		}()
	}
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range rounds {
				hub.Close(names[i%topics])
			}
		}()
	}
	wg.Wait()
}
