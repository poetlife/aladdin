package skill

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"
)

// MemoryStore 是 Store 的内存实现。
//
// **仅供测试。** 它不模拟任何存储行为，只实现契约：两种实现的语义由同一套契约
// 测试守着（见 gormstore 的 contract_test.go），而"内存实现读回来的顺序和 SQL
// 不一样"这类偏差只有在两处都跑过同一套用例时才看得见。
type MemoryStore struct {
	mu        sync.Mutex
	skills    map[string]*Skill
	versions  map[string][]Version
	favorites map[string]map[string]bool
	// usage 是 skillID → subjectID → 日期串 → 那一天最后一次使用的时刻。
	usage map[string]map[string]map[string]time.Time
}

// NewMemoryStore 构造一个空的内存存储。
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		skills:    map[string]*Skill{},
		versions:  map[string][]Version{},
		favorites: map[string]map[string]bool{},
		usage:     map[string]map[string]map[string]time.Time{},
	}
}

// ListSkills 实现 Store。
func (m *MemoryStore) ListSkills(_ context.Context) ([]Skill, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	items := make([]Skill, 0, len(m.skills))
	for _, item := range m.skills {
		items = append(items, cloneSkill(*item))
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	return items, nil
}

// GetSkill 实现 Store。
func (m *MemoryStore) GetSkill(_ context.Context, skillID string) (Skill, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	item, ok := m.skills[skillID]
	if !ok {
		return Skill{}, ErrSkillNotFound
	}
	return cloneSkill(*item), nil
}

// ListVersions 实现 Store（最新的在前）。
func (m *MemoryStore) ListVersions(_ context.Context, skillID string) ([]Version, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.skills[skillID]; !ok {
		return nil, ErrSkillNotFound
	}
	stored := m.versions[skillID]
	versions := make([]Version, 0, len(stored))
	for i := len(stored) - 1; i >= 0; i-- {
		versions = append(versions, cloneVersion(stored[i]))
	}
	return versions, nil
}

// CreateSkill 实现 Store。
func (m *MemoryStore) CreateSkill(_ context.Context, item Skill) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.skills[item.ID]; exists {
		return fmt.Errorf("技能 %s 已存在", item.ID)
	}
	created := cloneSkill(item)
	m.skills[item.ID] = &created
	m.versions[item.ID] = []Version{cloneVersion(item.Current)}
	return nil
}

// AppendVersion 实现 Store。
func (m *MemoryStore) AppendVersion(_ context.Context, skillID string, version Version, source Source) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	item, ok := m.skills[skillID]
	if !ok {
		return ErrSkillNotFound
	}
	item.Source = source
	item.Current = cloneVersion(version)
	m.versions[skillID] = append(m.versions[skillID], cloneVersion(version))
	return nil
}

// SetCurrentVersion 实现 Store。
func (m *MemoryStore) SetCurrentVersion(_ context.Context, skillID, versionID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	item, ok := m.skills[skillID]
	if !ok {
		return ErrSkillNotFound
	}
	for _, version := range m.versions[skillID] {
		if version.ID == versionID {
			item.Current = cloneVersion(version)
			return nil
		}
	}
	return ErrVersionNotFound
}

// UpdateMetadata 实现 Store。
func (m *MemoryStore) UpdateMetadata(_ context.Context, skillID, title, summary string, tags []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	item, ok := m.skills[skillID]
	if !ok {
		return ErrSkillNotFound
	}
	item.Title = title
	item.Summary = summary
	item.Tags = cloneStrings(tags)
	return nil
}

// DeleteSkill 实现 Store。
func (m *MemoryStore) DeleteSkill(_ context.Context, skillID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.skills[skillID]; !ok {
		return ErrSkillNotFound
	}
	delete(m.skills, skillID)
	delete(m.versions, skillID)
	delete(m.usage, skillID)
	for subjectID := range m.favorites {
		delete(m.favorites[subjectID], skillID)
	}
	return nil
}

// SetFavorite 实现 Store。两个方向都是幂等的。
func (m *MemoryStore) SetFavorite(_ context.Context, skillID, subjectID string, favorited bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.skills[skillID]; !ok {
		return ErrSkillNotFound
	}
	if !favorited {
		delete(m.favorites[subjectID], skillID)
		return nil
	}
	if m.favorites[subjectID] == nil {
		m.favorites[subjectID] = map[string]bool{}
	}
	m.favorites[subjectID][skillID] = true
	return nil
}

// FavoriteIDs 实现 Store。
func (m *MemoryStore) FavoriteIDs(_ context.Context, subjectID string) (map[string]bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make(map[string]bool, len(m.favorites[subjectID]))
	for skillID := range m.favorites[subjectID] {
		result[skillID] = true
	}
	return result, nil
}

// RecordUse 实现 Store。
func (m *MemoryStore) RecordUse(_ context.Context, skillID, subjectID string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.skills[skillID]; !ok {
		return ErrSkillNotFound
	}
	if m.usage[skillID] == nil {
		m.usage[skillID] = map[string]map[string]time.Time{}
	}
	if m.usage[skillID][subjectID] == nil {
		m.usage[skillID][subjectID] = map[string]time.Time{}
	}
	day := UsageDay(at)
	if previous, seen := m.usage[skillID][subjectID][day]; !seen || at.After(previous) {
		m.usage[skillID][subjectID][day] = at
	}
	return nil
}

// Usage 实现 Store。
func (m *MemoryStore) Usage(_ context.Context, skillIDs []string, since time.Time) (map[string]Usage, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	from := UsageDay(since)
	result := map[string]Usage{}
	for _, skillID := range skillIDs {
		var (
			days   int
			users  int
			latest time.Time
		)
		for _, byDay := range m.usage[skillID] {
			usedThisSubject := false
			for day, at := range byDay {
				// 日期串是 ISO 形状，因此"早于某天"是一次字典序比较。
				if day < from {
					continue
				}
				usedThisSubject = true
				days++
				if at.After(latest) {
					latest = at
				}
			}
			if usedThisSubject {
				users++
			}
		}
		if days == 0 {
			continue
		}
		result[skillID] = Usage{UseDays: days, Users: users, LastUsedAt: latest}
	}
	return result, nil
}

// DeleteUsageBefore 实现 Store。
func (m *MemoryStore) DeleteUsageBefore(_ context.Context, before time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	limit := UsageDay(before)
	for _, bySubject := range m.usage {
		for subjectID, byDay := range bySubject {
			for day := range byDay {
				if day < limit {
					delete(byDay, day)
				}
			}
			if len(byDay) == 0 {
				delete(bySubject, subjectID)
			}
		}
	}
	return nil
}

// cloneSkill 深拷贝一条技能。
//
// **读与写都经过它**：内存实现与 SQL 实现的差别之一是前者会把同一份切片交出去，
// 而调用方（或测试）改一下它就会"改到库里"。契约要求两种实现在这一点上表现相同。
func cloneSkill(item Skill) Skill {
	cloned := item
	cloned.Tags = cloneStrings(item.Tags)
	cloned.Current = cloneVersion(item.Current)
	return cloned
}

// cloneVersion 深拷贝一份版本。
func cloneVersion(version Version) Version {
	cloned := version
	cloned.Files = make([]File, len(version.Files))
	copy(cloned.Files, version.Files)
	return cloned
}

// cloneStrings 拷贝一组字符串。
func cloneStrings(values []string) []string {
	if values == nil {
		return nil
	}
	cloned := make([]string, len(values))
	copy(cloned, values)
	return cloned
}
