package identity

import (
	"context"
	"errors"
	"sync"
)

// SourceGoogle 是 Google 这个渠道来源的标识。
//
// 它同时是身份别名表里的一个取值：将来接入第二个来源时，两边的身份各归
// 各位——同一个不可变标识字符串在两个来源下是两个不同的身份，而不是同一个。
const SourceGoogle = "google"

var (
	// ErrIdentityNotFound 表示这个渠道身份还不属于任何主体。
	//
	// **它不是故障，而是首次登录的正常结论**：登录遇到它就去登记一个新主体
	// （见 identity_resolver.go）。它与"存储不可用"必须分开，否则一次数据库
	// 抖动会被表现成"所有人都得重新登录一遍"。
	ErrIdentityNotFound = errors.New("身份未登记")

	// ErrIdentityTaken 表示这个身份已经属于**另一个**主体。
	//
	// 它与 ErrIdentityNotFound 必须分开：前者是"这个身份被别人占了"，绑定
	// 必须拒绝；后者是"还没人占"，可以写。混为一谈会让一次绑定把别人的
	// 进入方式夺走，而那个动作在系统里与一次正常绑定没有区别。
	ErrIdentityTaken = errors.New("身份已被其他主体绑定")

	// ErrLastIdentity 表示这次解绑会把主体的最后一个身份摘掉。
	//
	// 一个主体没有任何身份，等于它再也没有任何进入方式：它的角色绑定还在，
	// 却没有人能进来清理。因此这一步必须失败，而不是在库里留下一批
	// 无人可达的主体。
	ErrLastIdentity = errors.New("不能解绑主体的最后一个身份")
)

// Identity 是一个渠道上的身份，以及它属于谁。
//
// 它是"（来源，身份标识）→ 主体"这一条映射在内存里的样子。身份不是主体的
// 属性：一个主体可以有多个身份，一个身份只能属于一个主体。
type Identity struct {
	// Source 是这个身份由谁签发（见 SourceGoogle）。
	Source string
	// ExternalID 是该来源签发的不可变标识，即这个渠道上的身份键。
	//
	// 它**只用于定位身份**，不参与任何判定，也不出现在主体标识里：
	// 把渠道标识编进主体标识，等于让"一个人两个渠道"在模型上无法表达。
	ExternalID string
	// SubjectID 是这个身份属于谁。
	SubjectID string
	// Display 是该渠道给出的可读标识（Google 是邮箱），仅供展示与排障。
	//
	// **不参与任何判定，也不参与身份的确定。** 按它归并意味着渠道侧改名或
	// 回收邮箱的那天，另一个人直接接管这个主体的全部角色绑定。
	Display string
}

// IdentityStore 是身份别名的持久化抽象。
//
// 它保持"哑"：只按（来源，身份标识）读写事实，不判断"应该属于谁"——
// 那是 Identities 的事。把归并语义放进存储，内存实现与 SQL 实现就会
// 各写一遍，而两份判断迟早会有一份漏掉。
//
// 唯一的例外是两条**不变式**，它们由存储保证而不是由"写之前先查一下"
// 保证：归属唯一，以及 Delete 不会摘掉最后一个身份。理由与绑定表的
// 唯一性相同——先查后写在并发下不成立，而这两条正好都会在并发下被打破。
type IdentityStore interface {
	// Lookup 按（来源，身份标识）取一条身份。不存在时返回 ErrIdentityNotFound。
	Lookup(ctx context.Context, source, externalID string) (Identity, error)

	// Put 写入一条身份。
	//
	// 该身份已属于**另一个**主体时返回 ErrIdentityTaken；已属于同一主体时
	// 只更新展示信息，是幂等的。
	Put(ctx context.Context, ident Identity) error

	// Delete 删除一条**属于该主体**的身份，返回是否真的删掉了。
	//
	// 条件里带 subjectID 是刻意的：解绑必须是"从我自己的主体上摘下来"。
	// 先查归属再删，在并发下会变成"摘掉别人的身份"。
	//
	// **它不删掉该主体的最后一个身份**，此时返回 ErrLastIdentity 且一行不动。
	// 这条判断放在存储里而不是调用方，是因为它必须与删除在同一次操作里完成：
	// 分开写的话，两个并发的解绑会各查一次"还有两个身份"，然后各删一个。
	Delete(ctx context.Context, source, externalID, subjectID string) (bool, error)

	// ListBySubject 返回该主体的全部身份。顺序不作保证，排序在调用方。
	ListBySubject(ctx context.Context, subjectID string) ([]Identity, error)
}

// MemoryIdentityStore 是 IdentityStore 的内存实现。
//
// **仅供测试。** 生产装配路径上不得出现它：两份可写副本意味着同一个人
// 可能被解析成两个主体，而那正是身份别名表要消灭的那件事。
type MemoryIdentityStore struct {
	mu        sync.RWMutex
	byKey     map[identityKey]Identity
	bySubject map[string]map[identityKey]struct{}
}

type identityKey struct {
	source     string
	externalID string
}

// NewMemoryIdentityStore 构造一个空的身份别名存储。
func NewMemoryIdentityStore() *MemoryIdentityStore {
	return &MemoryIdentityStore{
		byKey:     map[identityKey]Identity{},
		bySubject: map[string]map[identityKey]struct{}{},
	}
}

// Lookup 实现 IdentityStore。
func (s *MemoryIdentityStore) Lookup(_ context.Context, source, externalID string) (Identity, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	ident, ok := s.byKey[identityKey{source: source, externalID: externalID}]
	if !ok {
		return Identity{}, ErrIdentityNotFound
	}
	return ident, nil
}

// Put 实现 IdentityStore。
func (s *MemoryIdentityStore) Put(_ context.Context, ident Identity) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := identityKey{source: ident.Source, externalID: ident.ExternalID}
	if existing, ok := s.byKey[key]; ok && existing.SubjectID != ident.SubjectID {
		return ErrIdentityTaken
	}
	s.byKey[key] = ident
	if s.bySubject[ident.SubjectID] == nil {
		s.bySubject[ident.SubjectID] = map[identityKey]struct{}{}
	}
	s.bySubject[ident.SubjectID][key] = struct{}{}
	return nil
}

// Delete 实现 IdentityStore。
func (s *MemoryIdentityStore) Delete(_ context.Context, source, externalID, subjectID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := identityKey{source: source, externalID: externalID}
	existing, ok := s.byKey[key]
	if !ok || existing.SubjectID != subjectID {
		return false, nil
	}
	if len(s.bySubject[subjectID]) <= 1 {
		return false, ErrLastIdentity
	}

	delete(s.byKey, key)
	delete(s.bySubject[subjectID], key)
	if len(s.bySubject[subjectID]) == 0 {
		delete(s.bySubject, subjectID)
	}
	return true, nil
}

// ListBySubject 实现 IdentityStore。
func (s *MemoryIdentityStore) ListBySubject(_ context.Context, subjectID string) ([]Identity, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	keys := s.bySubject[subjectID]
	list := make([]Identity, 0, len(keys))
	for key := range keys {
		list = append(list, s.byKey[key])
	}
	return list, nil
}

var _ IdentityStore = (*MemoryIdentityStore)(nil)
