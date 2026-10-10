package galaxy

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"sync"
	"time"
)

// MemoryStore 是 MutableStore 的内存实现。
//
// **仅供测试与本地开发。** 生产路径上不得出现它：进程内的一份可写副本意味着
// 一次创作会落在一个重启就没的地方，而它不会以"丢了"的形式暴露，只会表现为
// "我昨天写的页面今天变回去了"。
//
// 它与 gormstore 的实现跑**同一套契约用例**，同一输入必须返回同样的结果与同样
// 的错误类别——"没找到"与"库出错"混淆时，契约测试必须失败。
type MemoryStore struct {
	mu sync.RWMutex

	projects      map[string]Project
	drafts        map[string]Draft
	draftSnaps    map[string]DraftSnapshot
	versions      map[string]Version
	assets        map[string]Asset
	attachments   map[string]Attachment
	publications  map[string]Publication
	previewGrants map[string]PreviewGrant
}

// NewMemoryStore 构造一个空的 galaxy 存储。
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		projects:      map[string]Project{},
		drafts:        map[string]Draft{},
		draftSnaps:    map[string]DraftSnapshot{},
		versions:      map[string]Version{},
		assets:        map[string]Asset{},
		attachments:   map[string]Attachment{},
		publications:  map[string]Publication{},
		previewGrants: map[string]PreviewGrant{},
	}
}

// cloneManifest 复制一份清单。
//
// 每个读写点都过它：清单是**不可变**的（版本一旦保存，它读回的内容逐字不变），
// 而切片是可变的——不复制的话，一次对返回值的 append 会写进已保存的版本里，
// 而那正是"版本不可变"的反面。gormstore 那边由序列化天然隔开，这里必须显式做。
func cloneManifest(manifest Manifest) Manifest {
	if manifest == nil {
		return nil
	}
	out := make(Manifest, len(manifest))
	copy(out, manifest)
	return out
}

// cloneTags 复制一份标签。
//
// 理由与 cloneManifest 相同：标签是切片，而读到的资产会离开这个存储。不复制
// 的话，一次对返回值的 append 会写进已经落库的那一份里。
func cloneTags(tags []string) []string {
	if tags == nil {
		return nil
	}
	out := make([]string, len(tags))
	copy(out, tags)
	return out
}

// sortedTags 复制一份标签并排好序。
//
// **两个存储实现必须给出同一个顺序**：SQL 那边是 `ORDER BY asset_id, tag`，
// 因此这里也排成升序。不排的话，一次写入原样读回的顺序会随后端变化，而那是
// 契约测试才能发现的那种偏差。
func sortedTags(tags []string) []string {
	out := cloneTags(tags)
	slices.Sort(out)
	return out
}

// hasAllTags 判定一个资产是不是带上了给定的全部标签（多值取交集）。
//
// 给定的标签是归一化之后的，而资产身上的也是，因此直接逐字比较即可。
func hasAllTags(assetTags, want []string) bool {
	for _, tag := range want {
		if !slices.Contains(assetTags, tag) {
			return false
		}
	}
	return true
}

// draftKey 是草稿行的键：**一个槽一行**。
//
// 草稿是"每个内容槽各一份"的，因此键必须含槽——只按工程标识存的表现是"推了
// 文档槽的草稿，站点槽的草稿被覆盖了"。
func draftKey(projectID string, slot ContentSlot) string {
	return projectID + "\x00" + string(slot)
}

// cloneSlots 复制一份内容槽。
//
// 理由与 cloneManifest 相同：切片是可变的值，而读到的工程会离开这个存储。
func cloneSlots(slots []ProjectSlot) []ProjectSlot {
	if slots == nil {
		return nil
	}
	out := make([]ProjectSlot, len(slots))
	copy(out, slots)
	return out
}

// GetProject 实现 Store。
func (s *MemoryStore) GetProject(_ context.Context, projectID string) (Project, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	project, ok := s.projects[projectID]
	if !ok {
		return Project{}, ErrProjectNotFound
	}
	project.Slots = cloneSlots(project.Slots)
	return project, nil
}

// ListProjectsByOwner 实现 Store。**槽随工程一起返回**。
func (s *MemoryStore) ListProjectsByOwner(_ context.Context, ownerSubjectID string) ([]Project, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var projects []Project
	for _, project := range s.projects {
		if project.OwnerSubjectID == ownerSubjectID {
			project.Slots = cloneSlots(project.Slots)
			projects = append(projects, project)
		}
	}
	sort.Slice(projects, func(i, j int) bool {
		if projects[i].UpdatedAt.Equal(projects[j].UpdatedAt) {
			return projects[i].ID < projects[j].ID
		}
		return projects[i].UpdatedAt.After(projects[j].UpdatedAt)
	})
	return projects, nil
}

// GetDraft 实现 Store。
func (s *MemoryStore) GetDraft(_ context.Context, projectID string, slot ContentSlot) (Draft, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	draft, ok := s.drafts[draftKey(projectID, slot)]
	if !ok {
		return Draft{}, ErrDraftNotFound
	}
	draft.Manifest = cloneManifest(draft.Manifest)
	return draft, nil
}

// GetVersion 实现 Store。
func (s *MemoryStore) GetVersion(_ context.Context, projectID string, slot ContentSlot, versionID string) (Version, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	version, ok := s.versions[versionID]
	if !ok || version.ProjectID != projectID || version.Slot != slot {
		return Version{}, ErrVersionNotFound
	}
	version.Manifest = cloneManifest(version.Manifest)
	return version, nil
}

// GetVersionByID 实现 Store：不限槽。
func (s *MemoryStore) GetVersionByID(_ context.Context, projectID, versionID string) (Version, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	version, ok := s.versions[versionID]
	if !ok || version.ProjectID != projectID {
		return Version{}, ErrVersionNotFound
	}
	version.Manifest = cloneManifest(version.Manifest)
	return version, nil
}

// ListVersions 实现 Store。清单随行返回（它只有路径与摘要，几 KB 量级）。
func (s *MemoryStore) ListVersions(_ context.Context, projectID string, slot ContentSlot) ([]Version, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var versions []Version
	for _, version := range s.versions {
		if version.ProjectID == projectID && version.Slot == slot {
			version.Manifest = cloneManifest(version.Manifest)
			versions = append(versions, version)
		}
	}
	sort.Slice(versions, func(i, j int) bool {
		if versions[i].Seq == versions[j].Seq {
			return versions[i].ID < versions[j].ID
		}
		return versions[i].Seq < versions[j].Seq
	})
	return versions, nil
}

// GetAsset 实现 Store。
func (s *MemoryStore) GetAsset(_ context.Context, projectID, assetID string) (Asset, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	asset, ok := s.assets[assetID]
	if !ok || asset.ProjectID != projectID {
		return Asset{}, ErrAssetNotFound
	}
	asset.Tags = sortedTags(asset.Tags)
	return asset, nil
}

// ListAssets 实现 Store。
//
// tags 非空时按交集筛选：只返回同时带这些标签的资产。
func (s *MemoryStore) ListAssets(_ context.Context, projectID string, tags []string) ([]Asset, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var assets []Asset
	for _, asset := range s.assets {
		if asset.ProjectID != projectID {
			continue
		}
		if !hasAllTags(asset.Tags, tags) {
			continue
		}
		asset.Tags = sortedTags(asset.Tags)
		assets = append(assets, asset)
	}
	sort.Slice(assets, func(i, j int) bool {
		if assets[i].UploadedAt.Equal(assets[j].UploadedAt) {
			return assets[i].ID < assets[j].ID
		}
		return assets[i].UploadedAt.After(assets[j].UploadedAt)
	})
	return assets, nil
}

// ListProjectTags 实现 Store。
func (s *MemoryStore) ListProjectTags(_ context.Context, projectID string) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	seen := map[string]struct{}{}
	for _, asset := range s.assets {
		if asset.ProjectID != projectID {
			continue
		}
		for _, tag := range asset.Tags {
			seen[tag] = struct{}{}
		}
	}
	tags := make([]string, 0, len(seen))
	for tag := range seen {
		tags = append(tags, tag)
	}
	sort.Strings(tags)
	return tags, nil
}

// GetAttachment 实现 Store。
func (s *MemoryStore) GetAttachment(_ context.Context, projectID, attachmentID string) (Attachment, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	attachment, ok := s.attachments[attachmentID]
	if !ok || attachment.ProjectID != projectID {
		return Attachment{}, ErrAttachmentNotFound
	}
	return attachment, nil
}

// ListAttachments 实现 Store：按上传时间倒序。
func (s *MemoryStore) ListAttachments(_ context.Context, projectID string) ([]Attachment, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var attachments []Attachment
	for _, attachment := range s.attachments {
		if attachment.ProjectID == projectID {
			attachments = append(attachments, attachment)
		}
	}
	sort.Slice(attachments, func(i, j int) bool {
		if attachments[i].UploadedAt.Equal(attachments[j].UploadedAt) {
			return attachments[i].ID < attachments[j].ID
		}
		return attachments[i].UploadedAt.After(attachments[j].UploadedAt)
	})
	return attachments, nil
}

// SumAttachmentBytes 实现 Store。
//
// 它算的是**库里的行**，与 SQL 实现那条 `SUM(size_bytes)` 同一个口径：桶上可能
// 留下无从被引用的孤儿对象，而那是回收的事，不是配额的事。
func (s *MemoryStore) SumAttachmentBytes(_ context.Context, projectID string) (int64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var total int64
	for _, attachment := range s.attachments {
		if attachment.ProjectID == projectID {
			total += attachment.SizeBytes
		}
	}
	return total, nil
}

// GetPublication 实现 Store。
func (s *MemoryStore) GetPublication(_ context.Context, publicationID string) (Publication, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	publication, ok := s.publications[publicationID]
	if !ok {
		return Publication{}, ErrPublicationNotFound
	}
	publication.Manifest = cloneManifest(publication.Manifest)
	return publication, nil
}

// CreateProject 实现 MutableStore。
func (s *MemoryStore) CreateProject(_ context.Context, project Project) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.projects[project.ID]; exists {
		return fmt.Errorf("写入工程失败: 标识 %s 已存在", project.ID)
	}
	project.Slots = cloneSlots(project.Slots)
	s.projects[project.ID] = project
	return nil
}

// PutProjectMeta 实现 MutableStore。
func (s *MemoryStore) PutProjectMeta(_ context.Context, projectID, name, description string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	project, ok := s.projects[projectID]
	if !ok {
		return ErrProjectNotFound
	}
	project.Name = name
	project.Description = description
	project.UpdatedAt = at
	s.projects[projectID] = project
	return nil
}

// AddProjectSlot 实现 MutableStore。
//
// **槽只增不删**：它只追加一行，也没有对应的删除方法。
func (s *MemoryStore) AddProjectSlot(_ context.Context, projectID string, slot ContentSlot) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	project, ok := s.projects[projectID]
	if !ok {
		return ErrProjectNotFound
	}
	if _, exists := project.FindSlot(slot); exists {
		return ErrSlotEnabled
	}
	project.Slots = append(cloneSlots(project.Slots), ProjectSlot{Slot: slot})
	s.projects[projectID] = project
	return nil
}

// SetCurrentPublication 实现 MutableStore：**只改这一个槽的指针**。
//
// **它不动工程的更新时间**：发布时间与元数据变更时间是两件事，把发布算进
// "更新时间"会让工程列表在每次发布后重排，而列表要回答的是"我最近改的是哪个
// 工程"。它也不动另一个槽。
func (s *MemoryStore) SetCurrentPublication(_ context.Context, projectID string, slot ContentSlot, publicationID string, _ time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	project, ok := s.projects[projectID]
	if !ok {
		return ErrProjectNotFound
	}
	slots := cloneSlots(project.Slots)
	for i := range slots {
		if slots[i].Slot == slot {
			slots[i].CurrentPublicationID = publicationID
			project.Slots = slots
			s.projects[projectID] = project
			return nil
		}
	}
	return ErrProjectNotFound
}

// DeleteProject 实现 MutableStore：连同内容槽、版本、资产、附件与发布记录一并删除。
func (s *MemoryStore) DeleteProject(_ context.Context, projectID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.projects[projectID]; !ok {
		return ErrProjectNotFound
	}
	delete(s.projects, projectID)
	// 草稿按（工程，槽）分行，因此按前缀清掉这个工程的全部草稿行。
	prefix := projectID + "\x00"
	for key := range s.drafts {
		if len(key) >= len(prefix) && key[:len(prefix)] == prefix {
			delete(s.drafts, key)
		}
	}
	for id, version := range s.versions {
		if version.ProjectID == projectID {
			delete(s.versions, id)
		}
	}
	// 草稿历史也是工程的东西：工程没了，它的快照一条都不该留下。
	for id, snapshot := range s.draftSnaps {
		if snapshot.ProjectID == projectID {
			delete(s.draftSnaps, id)
		}
	}
	for id, asset := range s.assets {
		if asset.ProjectID == projectID {
			delete(s.assets, id)
		}
	}
	for id, attachment := range s.attachments {
		if attachment.ProjectID == projectID {
			delete(s.attachments, id)
		}
	}
	for id, publication := range s.publications {
		if publication.ProjectID == projectID {
			delete(s.publications, id)
		}
	}
	// 预览凭证也是工程的东西：工程没了，它的凭证一条都不该留下。
	for token, grant := range s.previewGrants {
		if grant.ProjectID == projectID {
			delete(s.previewGrants, token)
		}
	}
	return nil
}

// PutDraft 实现 MutableStore：整组替换**某一个槽**的草稿，并把被替换掉的旧清单
// 留成一条快照。
//
// 写入与清理在**同一个临界区**里完成（SQL 那边是同一个事务）：清理到一半的表现是
// "这条快照既不在历史里、又还占着名额"。
func (s *MemoryStore) PutDraft(_ context.Context, projectID string, slot ContentSlot, manifest Manifest, at time.Time,
	snapshot DraftSnapshot, retention DraftSnapshotRetention) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.drafts[draftKey(projectID, slot)] = Draft{
		ProjectID: projectID,
		Slot:      slot,
		Manifest:  cloneManifest(manifest),
		UpdatedAt: at,
	}
	if snapshot.ID == "" {
		return nil
	}
	// 序号在设计里由存储分配（与版本表同一条），因此内存实现也在这里给。
	snapshot.Manifest = cloneManifest(snapshot.Manifest)
	snapshot.Seq = s.nextSnapshotSeq(projectID, slot)
	s.draftSnaps[snapshot.ID] = snapshot
	s.pruneDraftSnapshots(projectID, slot, retention)
	return nil
}

// nextSnapshotSeq 返回这个槽下一个可用的快照序号。
//
// 它在写锁里被调用，因此"查最大值"与"写进去"之间没有别人插进来——这正是序号
// 不能由调用方计算的那条理由。
func (s *MemoryStore) nextSnapshotSeq(projectID string, slot ContentSlot) int64 {
	var maxSeq int64
	for _, snapshot := range s.draftSnaps {
		if snapshot.ProjectID == projectID && snapshot.Slot == slot && snapshot.Seq > maxSeq {
			maxSeq = snapshot.Seq
		}
	}
	return maxSeq + 1
}

// pruneDraftSnapshots 按保留策略清掉这个槽过期的快照（只在写路径上调用）。
func (s *MemoryStore) pruneDraftSnapshots(projectID string, slot ContentSlot, retention DraftSnapshotRetention) {
	kept := s.snapshotIDsNewestFirst(projectID, slot)
	for index, id := range kept {
		snapshot := s.draftSnaps[id]
		if index >= retention.Keep || snapshot.CreatedAt.Before(retention.NotBefore) {
			delete(s.draftSnaps, id)
		}
	}
}

// snapshotIDsNewestFirst 返回这个槽的快照标识，最近的在前。
//
// **按序号倒序**（与 SQL 实现同一条）：时间不能当排序键，两次替换可以落在同一个
// 时间刻上，那时"哪一条最近"会变成一个由标识的随机性决定的答案。
func (s *MemoryStore) snapshotIDsNewestFirst(projectID string, slot ContentSlot) []string {
	ids := make([]string, 0, len(s.draftSnaps))
	for id, snapshot := range s.draftSnaps {
		if snapshot.ProjectID == projectID && snapshot.Slot == slot {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool {
		return s.draftSnaps[ids[i]].Seq > s.draftSnaps[ids[j]].Seq
	})
	return ids
}

// ListDraftSnapshots 实现 Store。
func (s *MemoryStore) ListDraftSnapshots(_ context.Context, projectID string, slot ContentSlot) ([]DraftSnapshot, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	ids := s.snapshotIDsNewestFirst(projectID, slot)
	snapshots := make([]DraftSnapshot, 0, len(ids))
	for _, id := range ids {
		snapshot := s.draftSnaps[id]
		snapshot.Manifest = cloneManifest(snapshot.Manifest)
		snapshots = append(snapshots, snapshot)
	}
	return snapshots, nil
}

// GetDraftSnapshot 实现 Store。
func (s *MemoryStore) GetDraftSnapshot(_ context.Context, projectID string, slot ContentSlot, snapshotID string) (DraftSnapshot, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	snapshot, ok := s.draftSnaps[snapshotID]
	if !ok || snapshot.ProjectID != projectID || snapshot.Slot != slot {
		return DraftSnapshot{}, ErrDraftSnapshotNotFound
	}
	snapshot.Manifest = cloneManifest(snapshot.Manifest)
	return snapshot, nil
}

// CreateVersion 实现 MutableStore：**在槽内**分配序号。
func (s *MemoryStore) CreateVersion(_ context.Context, version Version) (Version, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var maxSeq int64
	// 只以**同一工程、同一个槽**的现存版本的最大值为基准：删除一个版本会让
	// 序号出现空洞，而空洞是可接受的（见 Version 的注释）。重排会把一次删除变成
	// 对所有更大序号的改动，那正是"序号不是标识"要避免的事。**两个槽各数各的**。
	for _, existing := range s.versions {
		if existing.ProjectID == version.ProjectID && existing.Slot == version.Slot && existing.Seq > maxSeq {
			maxSeq = existing.Seq
		}
	}
	version.Seq = maxSeq + 1
	version.Manifest = cloneManifest(version.Manifest)
	s.versions[version.ID] = version
	return version, nil
}

// UpdateVersionDescription 实现 MutableStore：**只改说明那一列**。
func (s *MemoryStore) UpdateVersionDescription(_ context.Context, projectID string, slot ContentSlot, versionID, description string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	version, ok := s.versions[versionID]
	if !ok || version.ProjectID != projectID || version.Slot != slot {
		return ErrVersionNotFound
	}
	version.Description = description
	s.versions[versionID] = version
	return nil
}

// DeleteVersion 实现 MutableStore：删版本，并清空指向它的附件标注。
//
// 清空与删除在**同一个临界区**里完成（SQL 那边是同一个事务）：清到一半的表现是
// 一条指向不存在版本的悬空标注。
func (s *MemoryStore) DeleteVersion(_ context.Context, projectID string, slot ContentSlot, versionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	version, ok := s.versions[versionID]
	if !ok || version.ProjectID != projectID || version.Slot != slot {
		return ErrVersionNotFound
	}
	delete(s.versions, versionID)
	for id, attachment := range s.attachments {
		if attachment.ProjectID == projectID && attachment.VersionID == versionID {
			attachment.VersionID = ""
			s.attachments[id] = attachment
		}
	}
	return nil
}

// CreateAttachment 实现 MutableStore。
func (s *MemoryStore) CreateAttachment(_ context.Context, attachment Attachment) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.attachments[attachment.ID] = attachment
	return nil
}

// UpdateAttachmentDescription 实现 MutableStore。
//
// **只改说明**：文件名、字节数、摘要、标注的版本与对象键逐字不变。
func (s *MemoryStore) UpdateAttachmentDescription(_ context.Context, projectID, attachmentID, description string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	attachment, ok := s.attachments[attachmentID]
	if !ok || attachment.ProjectID != projectID {
		return ErrAttachmentNotFound
	}
	attachment.Description = description
	s.attachments[attachmentID] = attachment
	return nil
}

// DeleteAttachment 实现 MutableStore。
func (s *MemoryStore) DeleteAttachment(_ context.Context, projectID, attachmentID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	attachment, ok := s.attachments[attachmentID]
	if !ok || attachment.ProjectID != projectID {
		return ErrAttachmentNotFound
	}
	delete(s.attachments, attachmentID)
	return nil
}

// CreateAsset 实现 MutableStore。
func (s *MemoryStore) CreateAsset(_ context.Context, asset Asset) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	asset.Tags = cloneTags(asset.Tags)
	s.assets[asset.ID] = asset
	return nil
}

// UpdateAssetMeta 实现 MutableStore。
//
// **只改说明层**：摘要、媒体类型、类别与字节数逐字不变。
func (s *MemoryStore) UpdateAssetMeta(_ context.Context, projectID, assetID, title, notes string, tags []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	asset, ok := s.assets[assetID]
	if !ok || asset.ProjectID != projectID {
		return ErrAssetNotFound
	}
	asset.Title = title
	asset.Notes = notes
	asset.Tags = cloneTags(tags)
	s.assets[assetID] = asset
	return nil
}

// DeleteAsset 实现 MutableStore。
func (s *MemoryStore) DeleteAsset(_ context.Context, projectID, assetID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	asset, ok := s.assets[assetID]
	if !ok || asset.ProjectID != projectID {
		return ErrAssetNotFound
	}
	delete(s.assets, assetID)
	return nil
}

// PutPublication 实现 MutableStore：按发布标识幂等写入。
func (s *MemoryStore) PutPublication(_ context.Context, publication Publication) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// 同一条发布记录重复写入不产生第二条，也不改标识——这正是"落库"这个
	// 检查点可续跑的前提。
	publication.Manifest = cloneManifest(publication.Manifest)
	s.publications[publication.ID] = publication
	return nil
}

// GetPreviewGrant 实现 Store。
func (s *MemoryStore) GetPreviewGrant(_ context.Context, token string) (PreviewGrant, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	grant, ok := s.previewGrants[token]
	if !ok {
		return PreviewGrant{}, ErrPreviewGrantNotFound
	}
	return grant, nil
}

// PutPreviewGrant 实现 MutableStore：写入一条凭证，并清掉该工程下已过期的那些。
//
// 清理只影响表的增长，不影响任何一次判定：过期与否始终由读取侧比较时间得出
// （见 galaxy.PreviewGrant.Expired）。
func (s *MemoryStore) PutPreviewGrant(_ context.Context, grant PreviewGrant, cleanupBefore time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for token, existing := range s.previewGrants {
		if existing.ProjectID == grant.ProjectID && existing.Expired(cleanupBefore) {
			delete(s.previewGrants, token)
		}
	}
	s.previewGrants[grant.Token] = grant
	return nil
}

var _ MutableStore = (*MemoryStore)(nil)
