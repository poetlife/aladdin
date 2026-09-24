package rbac

import (
	"context"
	"errors"
	"sort"
	"sync"
)

// ErrRoleNotFound 表示指定角色不存在。
var ErrRoleNotFound = errors.New("角色不存在")

// ErrSubjectNotFound 表示主体不存在或已被停用。
var ErrSubjectNotFound = errors.New("主体不存在")

// ErrStoreUnavailable 表示存储不可用。
//
// 判定路径遇到该错误时应快速失败，由上层决定是否重试，
// **不得**退化为允许或拒绝全部请求（见 docs/design/rbac/enforcement.md）。
var ErrStoreUnavailable = errors.New("权限存储不可用")

// Store 是角色与绑定关系的持久化抽象。
//
// 它刻意保持"哑"：只负责读写事实，不承担任何判定职责。
// 作用域包含、继承展开、通配匹配全部在 Engine 中完成——
// 判定逻辑只能有一处实现（见 docs/ssot-registry.md）。
type Store interface {
	// Role 返回指定角色的定义，不存在时返回 ErrRoleNotFound。
	Role(ctx context.Context, roleID string) (RoleDefinition, error)

	// Roles 返回全部角色定义。
	Roles(ctx context.Context) ([]RoleDefinition, error)

	// SubjectBindings 返回主体的全部角色绑定，不做作用域过滤。
	//
	// 不过滤是刻意的：Engine 需要看到作用域之外的绑定，
	// 才能区分"确实没有这条权限"与"有但作用域不够"。
	// 主体不存在时返回 ErrSubjectNotFound。
	SubjectBindings(ctx context.Context, subjectID string) ([]RoleBinding, error)
}

// MutableStore 是管理接口所需的可写存储。
//
// 它与 Store 分开，使判定路径只依赖只读能力——Engine 拿不到写方法，
// 从类型上排除了"判定过程中顺手改数据"这类问题。
type MutableStore interface {
	Store

	// PutRole 写入或覆盖角色定义。调用方需先完成约束校验。
	PutRole(ctx context.Context, role RoleDefinition) error

	// DeleteRole 删除角色。内置角色不可删除。
	DeleteRole(ctx context.Context, roleID string) error

	// Bind 记录一次角色授予。
	Bind(ctx context.Context, binding RoleBinding) error
}

// MemoryStore 是 Store 的内存实现，用于测试与本地开发。
type MemoryStore struct {
	mu       sync.RWMutex
	roles    map[string]RoleDefinition
	subjects map[string]Subject
	bindings []RoleBinding
}

// NewMemoryStore 构造一个内存存储，并载入内置角色。
func NewMemoryStore() *MemoryStore {
	s := &MemoryStore{
		roles:    map[string]RoleDefinition{},
		subjects: map[string]Subject{},
	}
	for _, r := range BuiltinRoles {
		s.roles[r.ID] = r
	}
	return s
}

// RegisterSubject 登记一个主体。未登记的主体在判定时返回 ErrSubjectNotFound。
func (s *MemoryStore) RegisterSubject(subject Subject) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.subjects[subject.ID] = subject
}

// PutRole 实现 MutableStore。
func (s *MemoryStore) PutRole(_ context.Context, role RoleDefinition) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.roles[role.ID] = role
	return nil
}

// DeleteRole 实现 MutableStore。内置角色不可删除。
func (s *MemoryStore) DeleteRole(_ context.Context, roleID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.roles[roleID]
	if !ok {
		return ErrRoleNotFound
	}
	if r.Builtin {
		return errors.New("内置角色不可删除")
	}
	delete(s.roles, roleID)
	return nil
}

// Bind 实现 MutableStore。已存在的同一绑定不重复写入，保证幂等。
func (s *MemoryStore) Bind(_ context.Context, b RoleBinding) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.bindings {
		if existing == b {
			return nil
		}
	}
	s.bindings = append(s.bindings, b)
	return nil
}

// Roles 实现 Store。
func (s *MemoryStore) Roles(_ context.Context) ([]RoleDefinition, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]RoleDefinition, 0, len(s.roles))
	for _, r := range s.roles {
		out = append(out, r)
	}
	sortRoles(out)
	return out, nil
}

// Role 实现 Store。
func (s *MemoryStore) Role(_ context.Context, roleID string) (RoleDefinition, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.roles[roleID]
	if !ok {
		return RoleDefinition{}, ErrRoleNotFound
	}
	return r, nil
}

// SubjectBindings 实现 Store。
func (s *MemoryStore) SubjectBindings(_ context.Context, subjectID string) ([]RoleBinding, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, ok := s.subjects[subjectID]; !ok {
		return nil, ErrSubjectNotFound
	}
	var out []RoleBinding
	for _, b := range s.bindings {
		if b.SubjectID == subjectID {
			out = append(out, b)
		}
	}
	return out, nil
}

var (
	_ Store        = (*MemoryStore)(nil)
	_ MutableStore = (*MemoryStore)(nil)
)

// sortRoles 使角色列表顺序稳定，便于测试断言与前端差分。
func sortRoles(roles []RoleDefinition) {
	sort.Slice(roles, func(i, j int) bool { return roles[i].ID < roles[j].ID })
}
