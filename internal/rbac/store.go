package rbac

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// ErrRoleNotFound 表示指定角色不存在。
var ErrRoleNotFound = errors.New("角色不存在")

// ErrSubjectNotFound 表示主体不存在或已被停用。
var ErrSubjectNotFound = errors.New("主体不存在")

// ErrScopeNotFound 表示指定范围未登记。
//
// 它与"登记表里没有这条"是一回事：范围目录是登记制的，没登记过就是不存在
// （见 docs/design/rbac/scopes.md）。
var ErrScopeNotFound = errors.New("范围未登记")

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

	// Subject 返回已登记主体在**当前**的属性，未登记时返回 ErrSubjectNotFound。
	//
	// 判定路径不需要它：判定消费的是凭证里那个已确认的主体。它是**签发**
	// 路径要的——会话在签发那一刻把主体的类型与默认作用域冻结下来，
	// 从此不再回头查（见 docs/design/identity/session-token.md）。
	// 因此这个读必须走本抽象，而不是由认证模块自己查一次库。
	Subject(ctx context.Context, subjectID string) (Subject, error)
}

// MutableStore 是管理接口所需的可写存储。
//
// 它与 Store 分开，使判定路径只依赖只读能力——Engine 拿不到写方法，
// 从类型上排除了"判定过程中顺手改数据"这类问题。
//
// 按角色反查绑定这类管理面才需要的查询也放在这一侧：判定路径用不到它，
// 放进来只会让"判定依赖什么"变得不那么一目了然。
//
// 写方法一律**只执行、不判定**：约束校验（继承成环、静态互斥、内置角色
// 不可删除、角色是否仍被占用）的唯一入口是 constraints.go 里的 Validate*。
// 存储实现里再判一次，就是把同一份规则写成两份，而判定路径只有一个——
// 这是本仓库最不能出现漂移的地方。
type MutableStore interface {
	Store

	// PutRole 写入或覆盖角色定义。调用方需先完成约束校验。
	PutRole(ctx context.Context, role RoleDefinition) error

	// DeleteRole 删除角色。调用方需先完成约束校验；角色不存在时返回
	// ErrRoleNotFound。
	DeleteRole(ctx context.Context, roleID string) error

	// PutSubject 写入或覆盖主体。未登记的主体在判定时返回 ErrSubjectNotFound。
	PutSubject(ctx context.Context, subject Subject) error

	// Bind 记录一次角色授予。已存在的同一绑定不重复写入，保证幂等。
	Bind(ctx context.Context, binding RoleBinding) error

	// Unbind 撤销一次角色授予。绑定不存在时不报错——重复撤销与撤销
	// 一个本来就没有的绑定，结果一致，不该让调用方为此写判断。
	Unbind(ctx context.Context, binding RoleBinding) error

	// BindingsOfRole 返回持有该角色的全部绑定，用于删除角色前的占用校验。
	BindingsOfRole(ctx context.Context, roleID string) ([]RoleBinding, error)

	// Scopes 返回全部已登记的范围（不含全局：它不是登记记录）。
	Scopes(ctx context.Context) ([]ScopeDefinition, error)

	// Scope 返回指定路径的登记记录，未登记时返回 ErrScopeNotFound。
	Scope(ctx context.Context, path string) (ScopeDefinition, error)

	// PutScope 登记一个范围，或改它的显示名。调用方需先确认路径不是全局
	// （全局不是登记记录，见 docs/design/rbac/scopes.md）。
	PutScope(ctx context.Context, scope ScopeDefinition) error

	// DeleteScope 删除一条登记记录。调用方需先完成约束校验；未登记时返回
	// ErrScopeNotFound。
	DeleteScope(ctx context.Context, path string) error

	// BindingsUnderScope 返回落在 scope 上**以及其全部后代上**的绑定，
	// 用于删除范围前的引用校验。
	//
	// 包含语义复用 Scope.Contains（唯一实现），实现里不得另写一套前缀匹配：
	// 范围路径是自由文本，SQL 里的 LIKE 还要处理 % 与 _ 的转义，而两份匹配
	// 迟早会有一份漏掉边界。
	BindingsUnderScope(ctx context.Context, scope Scope) ([]RoleBinding, error)
}

// MemoryStore 是 Store 的内存实现，用于测试与本地开发。
type MemoryStore struct {
	mu       sync.RWMutex
	roles    map[string]RoleDefinition
	scopes   map[string]ScopeDefinition
	subjects map[string]Subject
	bindings []RoleBinding
}

// NewMemoryStore 构造一个内存存储，并载入内置角色。
//
// **不预置任何范围**：范围是部署的登记数据，全局不是登记记录，因此一个空库
// 的范围目录本来就是空的（见 docs/design/rbac/scopes.md）。
func NewMemoryStore() *MemoryStore {
	s := &MemoryStore{
		roles:    map[string]RoleDefinition{},
		scopes:   map[string]ScopeDefinition{},
		subjects: map[string]Subject{},
	}
	for _, r := range BuiltinRoles {
		s.roles[r.ID] = r
	}
	return s
}

// PutSubject 实现 MutableStore。未登记的主体在判定时返回 ErrSubjectNotFound。
func (s *MemoryStore) PutSubject(_ context.Context, subject Subject) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.subjects[subject.ID] = subject
	return nil
}

// PutRole 实现 MutableStore。
func (s *MemoryStore) PutRole(_ context.Context, role RoleDefinition) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.roles[role.ID] = role
	return nil
}

// DeleteRole 实现 MutableStore。
//
// 与 PutRole 一样，它只执行、不判定：内置角色不可删除、角色仍被继承或
// 仍被持有，都是**领域约束**，唯一入口是 ValidateRoleDeletion。
// 在这里再判一次就是把同一份规则写成两份，而两份迟早会有一份漏掉。
func (s *MemoryStore) DeleteRole(_ context.Context, roleID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.roles[roleID]; !ok {
		return ErrRoleNotFound
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

// Unbind 实现 MutableStore。绑定不存在时不报错：重复撤销与撤销一个本来
// 就没有的绑定，结果一致，不该让调用方为此写判断。
func (s *MemoryStore) Unbind(_ context.Context, b RoleBinding) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, existing := range s.bindings {
		if existing == b {
			s.bindings = append(s.bindings[:i], s.bindings[i+1:]...)
			return nil
		}
	}
	return nil
}

// BindingsOfRole 实现 MutableStore。
func (s *MemoryStore) BindingsOfRole(_ context.Context, roleID string) ([]RoleBinding, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []RoleBinding
	for _, b := range s.bindings {
		if b.RoleID == roleID {
			out = append(out, b)
		}
	}
	return SortBindings(out), nil
}

// BindingsUnderScope 实现 MutableStore。
func (s *MemoryStore) BindingsUnderScope(_ context.Context, scope Scope) ([]RoleBinding, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []RoleBinding
	for _, b := range s.bindings {
		// 包含语义只有一处实现：这里不自己比字符串前缀。
		if scope.Contains(b.Scope) {
			out = append(out, b)
		}
	}
	return SortBindings(out), nil
}

// Scopes 实现 Store。
func (s *MemoryStore) Scopes(_ context.Context) ([]ScopeDefinition, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]ScopeDefinition, 0, len(s.scopes))
	for _, sc := range s.scopes {
		out = append(out, sc)
	}
	return SortScopes(out), nil
}

// Scope 实现 Store。
func (s *MemoryStore) Scope(_ context.Context, path string) (ScopeDefinition, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sc, ok := s.scopes[path]
	if !ok {
		return ScopeDefinition{}, fmt.Errorf("%w: %s", ErrScopeNotFound, path)
	}
	return sc, nil
}

// PutScope 实现 MutableStore。
//
// 与 PutRole 一样只执行、不判定：路径是不是全局由调用方先校验
// （见 docs/design/rbac/scopes.md）。
func (s *MemoryStore) PutScope(_ context.Context, scope ScopeDefinition) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.scopes[scope.Path] = scope
	return nil
}

// DeleteScope 实现 MutableStore。
//
// 删除不存在的范围返回 ErrScopeNotFound，而不是静默成功——多半意味着调用方
// 拼错了路径，与删除角色同一个判据。
func (s *MemoryStore) DeleteScope(_ context.Context, path string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.scopes[path]; !ok {
		return fmt.Errorf("%w: %s", ErrScopeNotFound, path)
	}
	delete(s.scopes, path)
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
	return SortRoles(out), nil
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

// Subject 实现 Store。
func (s *MemoryStore) Subject(_ context.Context, subjectID string) (Subject, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	subject, ok := s.subjects[subjectID]
	if !ok {
		return Subject{}, ErrSubjectNotFound
	}
	return subject, nil
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
	return SortBindings(out), nil
}

var (
	_ Store        = (*MemoryStore)(nil)
	_ MutableStore = (*MemoryStore)(nil)
)
