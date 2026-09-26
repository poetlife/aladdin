// Package identity 负责回答"你是谁"。
//
// 边界：本包只做认证。判定（你能做什么）全部在 internal/rbac，
// 本包不读角色、不参与判定、不缓存任何判定结果
// （见 docs/design/identity/README.md）。
package identity

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/poetlife/aladdin/internal/rbac"
)

var (
	// ErrSessionNotFound 表示会话不存在、已被撤销，或已过期。
	//
	// 三者共用一个错误是刻意的：对调用方它们是同一件事——"这份凭证不能用了"。
	// 分开会让探测者能从响应里读出"这个字符串曾经有效"
	// （见 docs/design/identity/session-token.md）。
	ErrSessionNotFound = errors.New("会话不存在或已失效")

	// ErrStoreUnavailable 表示会话存储不可用。
	//
	// 它与 ErrSessionNotFound 是**两个类别**：前者是"库出问题了"，
	// 必须让请求快速失败；后者是"这份凭证不成立"，是一次正常的否定结论。
	// 混为一谈会把一次数据库故障表现成一次登录失败，运维看到的是
	// "所有人都登不上了"，于是去查认证配置。
	ErrStoreUnavailable = errors.New("会话存储不可用")
)

// Session 是一份已签发的会话。
//
// 它**不含凭证本身，也不含凭证的摘要**：摘要是存储的查找键，
// 不是会话的属性。会话记录的是"这次确认的结果"——谁、到什么时候为止，
// 以及签发那一刻这个主体的两项属性。
//
// 主体类型与默认作用域是**签发时冻结的快照**，不是每次请求重新推导：
// 凭证一旦签发，它的含义就固定下来。这不是权限——请求声明的宽作用域
// 最终仍要与主体的绑定关系比对（见 docs/design/identity/session-token.md）。
type Session struct {
	// SubjectID 是这个会话代表谁。
	SubjectID string
	// SubjectType 是签发时该主体的类型。
	SubjectType rbac.SubjectType
	// DefaultScope 是签发时该主体的默认作用域。
	DefaultScope rbac.Scope
	// IssuedAt 是签发时间。只用于排障与审计，不参与判定。
	IssuedAt time.Time
	// ExpiresAt 是过期时间。
	ExpiresAt time.Time
}

// Expired 报告该会话在给定时刻是否已失效。
//
// 时刻由调用方给出而不是在这里取 time.Now：判断"过期没有"必须只有一处实现，
// 而"现在几点"必须是可注入的，否则契约测试只能靠 sleep 来推进时间。
func (s Session) Expired(now time.Time) bool {
	return !now.Before(s.ExpiresAt)
}

// Subject 把会话还原成判定消费的主体。
func (s Session) Subject() rbac.Subject {
	return rbac.Subject{
		ID:           s.SubjectID,
		Type:         s.SubjectType,
		DefaultScope: s.DefaultScope,
	}
}

// SessionStore 是会话的持久化抽象。
//
// 它刻意保持"哑"：只按摘要存取事实，**不做过期判断**。过期与否由
// Sessions 判定——那是"这份凭证还能不能用"的唯一实现处。放在存储里，
// 内存实现与 SQL 实现就会各写一遍（一个比时间、一个写 WHERE），
// 而两份判断迟早会有一份漏掉。
//
// 它只支持按凭证摘要查找，不支持按主体查找：当前没有任何"撤销某主体
// 全部会话"的路径。真出现那条路径时再扩接口，而不是先给一个没人调的
// 方法配上索引。
type SessionStore interface {
	// Put 写入一条会话。摘要相同则覆盖（重新签发同一份摘要不可能发生，
	// 但覆盖语义让"写入"是幂等的，不依赖调用方保证唯一性）。
	Put(ctx context.Context, tokenHash string, session Session) error

	// Get 按摘要取会话。不存在时返回 ErrSessionNotFound。
	// **不过滤已过期的行**——那是调用方的事。
	Get(ctx context.Context, tokenHash string) (Session, error)

	// Delete 删除一条会话，即撤销。删除不存在的会话不是错误：
	// 重复登出与登出一个早已过期的会话，结果一致，不该让调用方为此写判断。
	Delete(ctx context.Context, tokenHash string) error

	// DeleteExpired 删除全部在 now 之前过期的会话，返回删除条数。
	//
	// 它只是回收空间，**不是正确性的一部分**：过期的会话本来就校验不过。
	// 因此它的失败不该让启动失败，它删除的条数也不参与任何判定。
	DeleteExpired(ctx context.Context, now time.Time) (int64, error)
}

// MemoryStore 是 SessionStore 的内存实现。
//
// **仅供测试。** 生产装配路径上不得出现它：会话存储只能有一个可写副本，
// 两份副本意味着一次撤销可能只落到其中一份上，而"撤销了但还能用"
// 是认证里后果最重的一类错（见 docs/design/identity/session-token.md）。
type MemoryStore struct {
	mu       sync.RWMutex
	sessions map[string]Session
}

// NewMemoryStore 构造一个空的会话存储。
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{sessions: map[string]Session{}}
}

// Put 实现 SessionStore。
func (s *MemoryStore) Put(_ context.Context, tokenHash string, session Session) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[tokenHash] = session
	return nil
}

// Get 实现 SessionStore。
func (s *MemoryStore) Get(_ context.Context, tokenHash string) (Session, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	session, ok := s.sessions[tokenHash]
	if !ok {
		return Session{}, ErrSessionNotFound
	}
	return session, nil
}

// Delete 实现 SessionStore。
func (s *MemoryStore) Delete(_ context.Context, tokenHash string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, tokenHash)
	return nil
}

// DeleteExpired 实现 SessionStore。
func (s *MemoryStore) DeleteExpired(_ context.Context, now time.Time) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var removed int64
	for hash, session := range s.sessions {
		if session.Expired(now) {
			delete(s.sessions, hash)
			removed++
		}
	}
	return removed, nil
}

var _ SessionStore = (*MemoryStore)(nil)
