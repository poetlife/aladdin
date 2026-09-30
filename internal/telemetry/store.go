package telemetry

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"
)

// Retention 是客户端事件在库里保留多久。
//
// 取 31 天而不是 30：**回收边界与最长查询窗口不能重合**。读侧最长的窗口是
// LAST_30_DAYS（同样以"此刻"为锚），若按 30 天回收，窗口最早那一小段会落在
// 回收线之外——"近 30 天"于是永远看不到它的开头。多留一天让两者错开。
//
// 它对外的口径仍是「保留 30 天」：任何一条行至少活 30 天、至多 31 天。
const Retention = 31 * 24 * time.Hour

// ErrStoreUnavailable 表示存储用不了（而不是"没有数据"）。
//
// 这个区分是必须的：前者要让请求快速失败，后者是正常的业务结论（"这个窗口内
// 没有事件"是一个合法的空答案）。混成一个会让一次数据库故障表现成"最近没人
// 用"，而排障的人会因此去看一个根本没坏的东西。上层据此映射 Connect 错误码。
var ErrStoreUnavailable = errors.New("telemetry: 存储不可用")

// Entry 是一条**已落库**的事件。
//
// 它在 Record（校验与脱敏后的协议形状）之上只多一件事：发生时间。时间由服务端
// 盖章，不由上报端提供——让上报端提供它，等于允许把事件挪到自己喜欢的时间窗里。
type Entry struct {
	Record
	// OccurredAt 是服务端收到并落库的时刻。
	OccurredAt time.Time
}

// Stat 是一个 (client, action, result) 组合在时间窗内的计数。
type Stat struct {
	Client string
	Action string
	Result string
	Count  int64
}

// Store 是客户端事件的持久化。
//
// 写入与读取分开摆在一个接口上，是因为这个模块**只有一个消费方**（管理页）与
// 一个生产方（Recorder），拆成读写两个接口不会增加任何一处约束，只会多一层
// 类型。判定路径与本模块无关，因此不存在"只读接口"那样的安全边界要表达。
type Store interface {
	// Append 批量写入一批已通过校验的事件。
	//
	// 一批一次写入而不是逐条：上报本就是批量的，逐条写会让最忙的那条路径上
	// 多出 N 次往返。
	Append(ctx context.Context, entries []Entry) error

	// Stats 返回 occurred_at 落在 [from, to) 的按 (client, action, result)
	// 分组的计数。没有事件的组合不出现在结果里。
	Stats(ctx context.Context, from, to time.Time) ([]Stat, error)

	// Recent 返回 **occurred_at 落在 [from, to)** 的、时间最新的至多 limit 条
	// 事件，从新到旧。
	//
	// 它带时间区间而不是"全局最新"：明细与计数在同一个页面上并排显示，两者若
	// 用不同的时间口径，会出现"计数说这个窗口内没有失败、明细里却列着一条失败"
	// 这种自相矛盾。
	Recent(ctx context.Context, from, to time.Time, limit int) ([]Entry, error)

	// DeleteBefore 删除 occurred_at 早于 cutoff 的行，返回删除条数。
	//
	// 它只负责不让表无限增长，**不是正确性的一部分**：读取一律带时间条件，
	// 一条没被清掉的超窗行也不会被任何查询看见。因此它的失败不该让启动失败。
	DeleteBefore(ctx context.Context, cutoff time.Time) (int64, error)
}

// MemoryStore 是 Store 的内存实现。
//
// **仅供测试。** 生产装配路径上不得出现它：事件只落内存意味着一次重启就读不回来，
// 而"管理页看到的与日志里记的对不上"是最难解释的一类偏差。
type MemoryStore struct {
	mu      sync.Mutex
	entries []Entry
}

// NewMemoryStore 构造一个空的进程内存储。
func NewMemoryStore() *MemoryStore { return &MemoryStore{} }

// Append 实现 Store。
func (s *MemoryStore) Append(_ context.Context, entries []Entry) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = append(s.entries, entries...)
	return nil
}

// Stats 实现 Store。语义必须与 SQL 实现逐字一致：[from, to) 半开区间，
// 只返回出现过的组合，排序由 Stat 的三元组决定（让两组实现在同一输入下
// 给出同一顺序，契约测试才能逐字比对）。
func (s *MemoryStore) Stats(_ context.Context, from, to time.Time) ([]Stat, error) {
	if s == nil {
		return nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	counts := make(map[Stat]int64)
	for _, e := range s.entries {
		if e.OccurredAt.Before(from) || !e.OccurredAt.Before(to) {
			continue
		}
		counts[Stat{Client: e.Client, Action: e.Action, Result: e.Result}]++
	}

	out := make([]Stat, 0, len(counts))
	for stat, count := range counts {
		stat.Count = count
		out = append(out, stat)
	}
	sortStats(out)
	return out, nil
}

// Recent 实现 Store。
//
// 排序必须与 SQL 实现的 `occurred_at DESC, id DESC` 逐字一致：同一批写进来的
// 事件共享一个时间戳，只按时间排会让先后变成一个由实现决定的偶然值，而两组
// 实现的结果就无法比对了。这里的"id"就是写入次序——下标越大越新。
func (s *MemoryStore) Recent(_ context.Context, from, to time.Time, limit int) ([]Entry, error) {
	if s == nil || limit <= 0 {
		return nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	// 不能假设写入次序就是时间次序：一批里的若干条可能来自不同上报端，
	// 而时间由服务端逐批盖章。
	order := make([]int, 0, len(s.entries))
	for i, e := range s.entries {
		if e.OccurredAt.Before(from) || !e.OccurredAt.Before(to) {
			continue
		}
		order = append(order, i)
	}
	sort.SliceStable(order, func(i, j int) bool {
		a, b := order[i], order[j]
		if !s.entries[a].OccurredAt.Equal(s.entries[b].OccurredAt) {
			return s.entries[a].OccurredAt.After(s.entries[b].OccurredAt)
		}
		return a > b
	})
	if len(order) > limit {
		order = order[:limit]
	}

	out := make([]Entry, 0, len(order))
	for _, i := range order {
		out = append(out, s.entries[i])
	}
	return out, nil
}

// DeleteBefore 实现 Store。
func (s *MemoryStore) DeleteBefore(_ context.Context, cutoff time.Time) (int64, error) {
	if s == nil {
		return 0, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	kept := s.entries[:0]
	var removed int64
	for _, e := range s.entries {
		if e.OccurredAt.Before(cutoff) {
			removed++
			continue
		}
		kept = append(kept, e)
	}
	s.entries = kept
	return removed, nil
}

// sortStats 按三元组排序，让两组实现的结果顺序可逐字比对。
func sortStats(stats []Stat) {
	sort.SliceStable(stats, func(i, j int) bool {
		a, b := stats[i], stats[j]
		if a.Client != b.Client {
			return a.Client < b.Client
		}
		if a.Action != b.Action {
			return a.Action < b.Action
		}
		return a.Result < b.Result
	})
}
