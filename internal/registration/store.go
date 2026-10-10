package registration

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"
)

var (
	// ErrPolicyInvalid 表示一份策略的形状不自洽（模式取值未定义、默认角色与
	// 默认范围只给了一半）。它是**调用方的输入问题**，不是存储故障。
	ErrPolicyInvalid = errors.New("注册策略不合法")

	// ErrInviteNotFound 表示这份邀请码标识不存在。
	ErrInviteNotFound = errors.New("邀请码不存在")

	// ErrInviteUnusable 表示这份邀请码兑换不动：不存在、已过期、次数用尽、
	// 已被撤销。
	//
	// **四者刻意合并成一个结论。** 区分它们只会把一次失败的注册变成对"这个码
	// 是否曾经有效"的探测，与设备码登录把四种"不能交付"合并成同一个状态是
	// 同一条理由（见 docs/design/identity/registration.md）。
	ErrInviteUnusable = errors.New("邀请码不可兑换")

	// ErrStoreUnavailable 表示存储不可用。
	ErrStoreUnavailable = errors.New("注册存储不可用")
)

// Store 是注册策略与邀请码的持久化抽象。
//
// 它与 internal/rbac 的 Store 同一条取向：**只负责读写事实，不承担任何判定**。
// "这个角色能不能当默认角色""这个码还换不换得动"的结论要么在领域的唯一入口里
// （见 Registrations），要么在授权面的约束校验里（见 internal/rbac/constraints.go）。
type Store interface {
	// Policy 返回当前注册策略。**没有记录时返回零值**（模式为空），由
	// Policy.Effective 归一成开放注册——因此这里不代它做归一化。
	Policy(ctx context.Context) (Policy, error)

	// PutPolicy 写入策略（一行，站点级）。
	PutPolicy(ctx context.Context, policy Policy) error

	// Invites 返回全部邀请码，顺序由实现返回、由领域层统一排序。
	Invites(ctx context.Context) ([]Invite, error)

	// PutInvite 写入一份新签发的邀请码。标识重复时报错。
	PutInvite(ctx context.Context, invite Invite) error

	// RevokeInvite 撤销一份邀请码，返回撤销之后的记录。
	//
	// **只撤销，不删除**：删掉会连用量与留痕一起丢掉，"这个码被谁用过、用过
	// 几次"就再也答不上来。已撤销的再撤一次是**幂等的成功**（时间戳不覆盖），
	// 标识不存在时返回 ErrInviteNotFound。
	RevokeInvite(ctx context.Context, id string, at time.Time) (Invite, error)

	// Redeem 按码摘要**原子地**吃掉一次可用次数，返回兑换之后的记录。
	//
	// "原子"是它的全部要点：不可兑换时返回 ErrInviteUnusable，而且**不产生
	// 任何写入**。先读后写在并发下会超发——两个请求可以同时读到"还剩一次"。
	Redeem(ctx context.Context, codeHash string, at time.Time) (Invite, error)
}

// MemoryStore 是 Store 的内存实现，用于测试与本地开发。
type MemoryStore struct {
	mu      sync.RWMutex
	policy  Policy
	hasPol  bool
	invites map[string]Invite
}

// NewMemoryStore 构造一个空的注册存储。
//
// **它不预置策略**：没有记录就是缺省姿态（开放注册、无默认角色），与一个
// 刚迁移完的库一致。预置一条 open 记录会让"迁移只建表、不写行"这条约定在
// 测试里得不到检验。
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{invites: map[string]Invite{}}
}

// Policy 实现 Store。
func (s *MemoryStore) Policy(context.Context) (Policy, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !s.hasPol {
		return Policy{}, nil
	}
	return s.policy, nil
}

// PutPolicy 实现 Store。
func (s *MemoryStore) PutPolicy(_ context.Context, policy Policy) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.policy, s.hasPol = policy, true
	return nil
}

// Invites 实现 Store。
func (s *MemoryStore) Invites(context.Context) ([]Invite, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Invite, 0, len(s.invites))
	for _, inv := range s.invites {
		out = append(out, inv)
	}
	return out, nil
}

// PutInvite 实现 Store。
func (s *MemoryStore) PutInvite(_ context.Context, invite Invite) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, taken := s.invites[invite.ID]; taken {
		return errors.New("邀请码标识重复")
	}
	s.invites[invite.ID] = invite
	return nil
}

// RevokeInvite 实现 Store。
func (s *MemoryStore) RevokeInvite(_ context.Context, id string, at time.Time) (Invite, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	inv, ok := s.invites[id]
	if !ok {
		return Invite{}, ErrInviteNotFound
	}
	if inv.RevokedAt.IsZero() {
		inv.RevokedAt = at
		s.invites[id] = inv
	}
	return inv, nil
}

// Redeem 实现 Store。
//
// **判断与写入在同一次加锁里完成**：分开写的话，两次并发的兑换会各读到一次
// "还剩一次"，然后各自扣一次——那正是"不超发"要消灭的形状。
func (s *MemoryStore) Redeem(_ context.Context, codeHash string, at time.Time) (Invite, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, inv := range s.invites {
		if inv.CodeHash != codeHash {
			continue
		}
		if !inv.Redeemable(at) {
			return Invite{}, ErrInviteUnusable
		}
		inv.UsedCount++
		s.invites[id] = inv
		return inv, nil
	}
	return Invite{}, ErrInviteUnusable
}

// sortInvites 让同一份数据在任何实现下得到同一个顺序：签发时间倒序（最近的
// 在前），同一时刻按标识定序。
//
// 顺序由这里定，不由存储的返回顺序定：内存实现按 map 遍历、SQL 实现按它自己的
// 次序返回，两者天然不同，而"列表每次顺序都不一样"会让界面看起来在抖动。
func sortInvites(list []Invite) {
	sort.Slice(list, func(a, b int) bool {
		if !list[a].CreatedAt.Equal(list[b].CreatedAt) {
			return list[a].CreatedAt.After(list[b].CreatedAt)
		}
		return list[a].ID < list[b].ID
	})
}
