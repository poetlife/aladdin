// Package watch 是订阅通道的机制：**一条连接上按主题扇出"这里变了"**，以及哪些
// 主题可订阅、订阅它要什么权限。
//
// 它服务的是"某个资源变了，别处该重新看一眼"这一件事（见
// docs/design/events/README.md）。四条性质是它存在的理由：
//
//   - **事件没有载荷**。它只说"这条主题变了"，不说变成了什么——因此它的代价与
//     资源的内容体积无关，也不会过期；收到它的人自己回去读现状。
//   - **发布方永不阻塞**。写入是别人正在等的操作，订阅者只是看客。
//   - **变更按主题合并**。同一主题上的连续变化对订阅方是同一件事：再拉一次。
//     于是"订阅者慢"既不丢变更也不需要把连接掐掉——它只是让重拉晚一点发生。
//   - **与本仓库的领域类型无关**。键是一个字符串（主题），主题类型由属主模块
//     注册，因此下一个消费方不必为了复用这里而依赖 galaxy。
//
// 它不持久化、不重放、不重试（见设计文档的"交付语义"）。
package watch

import "sync"

// Hub 是按主题扇出的事件总线。零值不可用，用 NewHub 构造。
//
// 所有可导出的方法都可以并发调用。
type Hub struct {
	// mu 保护本类型的全部可变状态：订阅表，以及每条订阅的主题集合与待取集合。
	// **只有一把锁**是刻意的——这些状态一起变（发布要同时改"谁在这个主题上"与
	// "他还有什么没取走"），拆成两把锁只会多出一条必须记住的次序规则。
	mu   sync.Mutex
	subs map[string]map[*Subscription]struct{}
}

// NewHub 构造一个空的总线。
func NewHub() *Hub {
	return &Hub{subs: make(map[string]map[*Subscription]struct{})}
}

// Subscribe 订阅一组主题，返回这条连接的订阅句柄。
//
// 主题是字符串（形如 `galaxy.project/prj_…`），**这里不解析它**：合法性由
// Registry 在 RPC 层判定，总线只把它们当作键。
//
// 调用方必须在结束使用时调用 Subscription.Close：总线无从知道一条订阅什么时候
// 不再被使用。
func (h *Hub) Subscribe(topics []string) *Subscription {
	sub := &Subscription{
		hub:     h,
		topics:  make(map[string]struct{}, len(topics)),
		pending: make(map[string]struct{}),
		ready:   make(chan struct{}, 1),
		done:    make(chan struct{}),
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.register(sub, topics)
	return sub
}

// register 把一条订阅挂到它的每个主题上。调用方必须持有 h.mu。
func (h *Hub) register(sub *Subscription, topics []string) {
	for _, topic := range topics {
		if _, duplicate := sub.topics[topic]; duplicate {
			continue
		}
		sub.topics[topic] = struct{}{}
		if h.subs[topic] == nil {
			h.subs[topic] = make(map[*Subscription]struct{})
		}
		h.subs[topic][sub] = struct{}{}
	}
}

// Publish 报告"这个主题变了"。
//
// 投递是**非阻塞**的：它只把主题记进订阅者的待取集合（同一主题重复报到会被合并）
// 并摇一次就绪铃，因此发布方永不因为订阅者而等待。
func (h *Hub) Publish(topic string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for sub := range h.subs[topic] {
		sub.pending[topic] = struct{}{}
		sub.signal()
	}
}

// Close 表示这个主题背后的资源不存在了：把它从每条订阅的主题集合里摘掉。
//
// 摘掉之后不会再有人从这个主题发布（资源已删），留着它只会让连接挂在一件永远
// 不会发生的事上。调用方通常**先 Publish 再 Close**：让订阅者先知道"它变了"、
// 因此去读一次、读到"不存在"，然后这条主题安静退场。
//
// 某条订阅的主题集合因此变空时，这条订阅也结束（见 Subscription.Done）。
//
// **待取集合不会在这里清空。** Done 与 Ready 可能同时就绪；只看 Done 就会丢掉
// 还没取走的变更。结束这条订阅的那一侧要先把待取的取完再收摊（见事件通道的
// Watch：主题退场后重连会被拒绝，没有下一次 RESYNC 可补）。
func (h *Hub) Close(topic string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for sub := range h.subs[topic] {
		delete(sub.topics, topic)
		if len(sub.topics) == 0 {
			h.retire(sub)
		}
	}
	delete(h.subs, topic)
}

// retire 结束一条订阅：从它登记过的每个主题上摘掉它，并关闭它的结束通道。
//
// 调用方必须持有 h.mu。幂等：重复调用不会重复关闭通道。
//
// **只关 done，不关 ready。** 关掉 ready 会让订阅方的 select 永远选中它（已关闭
// 的通道恒可就绪），于是变成一次忙等；而 done 已经足够回答"这条订阅结束了"。
func (h *Hub) retire(sub *Subscription) {
	if sub.finished {
		return
	}
	sub.finished = true
	for topic := range sub.topics {
		if subs := h.subs[topic]; subs != nil {
			delete(subs, sub)
			if len(subs) == 0 {
				delete(h.subs, topic)
			}
		}
	}
	close(sub.done)
}
