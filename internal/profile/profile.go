// Package profile 是"这个主体叫什么、长什么样"这一问题的唯一实现。
//
// 边界：它不回答"你是谁"（认证，见 internal/identity），也不回答"你能做什么"
// （RBAC，见 internal/rbac）。**档案不参与任何一次判定**，也不是一条身份
// 路径——不能按昵称查人，昵称也不唯一（见 docs/design/profile/README.md）。
package profile

import (
	"context"
	"errors"
	"sync"
	"time"
)

// 字段长度上限，按**字符数**而不是字节数计。
//
// 按字节算会让"一个中文昵称写到十几个字就被拒"成为一条需要向用户解释的规则；
// 用户感知的长度是字数。
const (
	// NicknameMaxRunes 是昵称的长度上限。
	NicknameMaxRunes = 32
	// BioMaxRunes 是简介的长度上限。
	BioMaxRunes = 280
)

var (
	// ErrProfileNotFound 表示这个主体还没有档案行。
	//
	// **它不是故障。** 档案行是惰性创建的：本人第一次保存之前就没有这一行，
	// 而"没有这一行"与"有一行但每个字段都为空"在展示上完全等价。它与"存储
	// 不可用"必须分开，否则一次数据库抖动会被表现成"所有人的昵称都丢了"。
	ErrProfileNotFound = errors.New("档案不存在")

	// ErrNicknameTooLong 表示昵称超过长度上限。
	ErrNicknameTooLong = errors.New("昵称过长")

	// ErrBioTooLong 表示简介超过长度上限。
	ErrBioTooLong = errors.New("简介过长")

	// ErrStoreUnavailable 表示档案存储不可用。
	//
	// 与"没有档案"必须分开：前者是故障，后者是正常状态。混为一谈会让一次
	// 存储故障表现为"所有人的昵称都还在，只是显示不出来"。
	ErrStoreUnavailable = errors.New("档案存储不可用")
)

// Profile 是一个主体的展示信息。
//
// 三个字段**都不参与任何判定**，也都**不是身份键**：不能按昵称查人，昵称也
// 不唯一（见 docs/design/profile/README.md 的"边界与约束"）。
type Profile struct {
	// SubjectID 是主体标识，也是本档案的主键。
	SubjectID string
	// Nickname 是主体自己设置的昵称。空表示未设置，展示时回退。
	Nickname string
	// Bio 是简介。空表示未填写。
	Bio string
	// AvatarKey 是头像在对象存储里的对象键。空表示没有头像。
	//
	// **存的是键，不是字节。** 字节在对象存储；本字段是"这个主体有没有头像"
	// 的权威，对象存储上可能留下无从被引用的孤儿对象（见
	// docs/design/profile/avatar-storage.md）。
	AvatarKey string
	// UpdatedAt 是最后一次变更的时间。只用于展示与排障，不参与判定。
	UpdatedAt time.Time
}

// Store 是档案的持久化抽象。
//
// 它保持"哑"：只读写事实，**不做校验**。长度上限、类型白名单、大小上限的
// 唯一入口都在 Profiles。把校验放进存储，内存实现与 SQL 实现就会各写一遍，
// 而两份判断迟早会有一份漏掉。
//
// 写入刻意**按字段分成两个方法**，而不是一个覆盖整行的 Put：改名与换头像
// 是两件互不相干的操作，一个覆盖整行的接口会逼调用方先读后写，从而让两次
// 并发的修改互相覆盖——而"先查再写"在并发下不成立这件事，本仓库已经在绑定
// 表与身份别名表上踩过。
type Store interface {
	// Get 返回该主体的档案，不存在时返回 ErrProfileNotFound。
	Get(ctx context.Context, subjectID string) (Profile, error)

	// PutText 写入或覆盖该主体的昵称与简介，行不存在时创建。
	// 它**不动**头像对象键：一次改名不该顺手把头像清掉。
	PutText(ctx context.Context, subjectID, nickname, bio string, at time.Time) error

	// PutAvatarKey 写入或清除该主体的头像对象键，行不存在时创建。
	// key 为空表示清除。它**不动**昵称与简介。
	PutAvatarKey(ctx context.Context, subjectID, key string, at time.Time) error
}

// MemoryStore 是 Store 的内存实现。
//
// **仅供测试与本地开发。** 生产路径上不得出现它：进程内的一份可写副本意味着
// 一次变更会落在一个重启就没的地方，而它不会以"丢了"的形式暴露，只会表现为
// "我昨天改的昵称今天变回去了"。
type MemoryStore struct {
	mu       sync.RWMutex
	profiles map[string]Profile
}

// NewMemoryStore 构造一个空的档案存储。
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{profiles: map[string]Profile{}}
}

// Get 实现 Store。
func (s *MemoryStore) Get(_ context.Context, subjectID string) (Profile, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	stored, ok := s.profiles[subjectID]
	if !ok {
		return Profile{}, ErrProfileNotFound
	}
	return stored, nil
}

// PutText 实现 Store。
func (s *MemoryStore) PutText(_ context.Context, subjectID, nickname, bio string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	stored := s.profiles[subjectID]
	stored.SubjectID = subjectID
	stored.Nickname = nickname
	stored.Bio = bio
	stored.UpdatedAt = at
	s.profiles[subjectID] = stored
	return nil
}

// PutAvatarKey 实现 Store。
func (s *MemoryStore) PutAvatarKey(_ context.Context, subjectID, key string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	stored := s.profiles[subjectID]
	stored.SubjectID = subjectID
	stored.AvatarKey = key
	stored.UpdatedAt = at
	s.profiles[subjectID] = stored
	return nil
}

var _ Store = (*MemoryStore)(nil)
