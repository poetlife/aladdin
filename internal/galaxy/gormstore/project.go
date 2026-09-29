package gormstore

import (
	"context"
	"errors"
	"sort"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/poetlife/aladdin/internal/database"
	"github.com/poetlife/aladdin/internal/galaxy"
)

// GetProject 实现 galaxy.Store。
func (s *Store) GetProject(ctx context.Context, projectID string) (galaxy.Project, error) {
	var rec database.GalaxyProjectRecord
	if err := s.db.WithContext(ctx).First(&rec, "id = ?", projectID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return galaxy.Project{}, galaxy.ErrProjectNotFound
		}
		return galaxy.Project{}, unavailable("读取工程", err)
	}
	project := toProject(rec)
	slots, err := s.loadSlots(ctx, []string{projectID})
	if err != nil {
		return galaxy.Project{}, err
	}
	project.Slots = slots[projectID]
	return project, nil
}

// ListProjectsByOwner 实现 galaxy.Store。
//
// 它按拥有者过滤，而不是返回全表再让上层筛：范围由查询本身给定，因此"忘了筛"
// 这件事在这个形状下写不出来。**槽用一次查询一起取回**，不按工程逐个查。
func (s *Store) ListProjectsByOwner(ctx context.Context, ownerSubjectID string) ([]galaxy.Project, error) {
	var recs []database.GalaxyProjectRecord
	err := s.db.WithContext(ctx).
		Where("owner_subject_id = ?", ownerSubjectID).
		Order("updated_at DESC, id ASC").
		Find(&recs).Error
	if err != nil {
		return nil, unavailable("列出工程", err)
	}
	ids := make([]string, 0, len(recs))
	for _, rec := range recs {
		ids = append(ids, rec.ID)
	}
	slots, err := s.loadSlots(ctx, ids)
	if err != nil {
		return nil, err
	}
	projects := make([]galaxy.Project, 0, len(recs))
	for _, rec := range recs {
		project := toProject(rec)
		project.Slots = slots[rec.ID]
		projects = append(projects, project)
	}
	return projects, nil
}

// loadSlots 一次读出若干工程各自启用的内容槽，按工程标识分组。
//
// **一批一次查询**：列表接口要按槽展示状态，逐个工程再查一次会让列表退化成
// N+1（"列表明显变慢"正是这一层一直要避免的）。每个工程内部按槽的固定次序排好，
// 两个实现给出一致的顺序。
func (s *Store) loadSlots(ctx context.Context, projectIDs []string) (map[string][]galaxy.ProjectSlot, error) {
	byProject := make(map[string][]galaxy.ProjectSlot, len(projectIDs))
	if len(projectIDs) == 0 {
		return byProject, nil
	}
	var recs []database.GalaxySlotRecord
	if err := s.db.WithContext(ctx).Where("project_id IN ?", projectIDs).Find(&recs).Error; err != nil {
		return nil, unavailable("读取内容槽", err)
	}
	for _, rec := range recs {
		byProject[rec.ProjectID] = append(byProject[rec.ProjectID], galaxy.ProjectSlot{
			Slot:                 galaxy.ContentSlot(rec.Slot),
			CurrentPublicationID: rec.CurrentPublicationID,
		})
	}
	for id, slots := range byProject {
		sort.Slice(slots, func(i, j int) bool { return slots[i].Slot.Order() < slots[j].Slot.Order() })
		byProject[id] = slots
	}
	return byProject, nil
}

// CreateProject 实现 galaxy.MutableStore：工程行与它的槽行在**同一个事务**里落库。
//
// 先写工程再补槽会留下一个"没有内容的工程"的中间状态，而"至少一个槽"是工程能
// 成立的前提。
func (s *Store) CreateProject(ctx context.Context, project galaxy.Project) error {
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		rec := database.GalaxyProjectRecord{
			ID:             project.ID,
			OwnerSubjectID: project.OwnerSubjectID,
			Name:           project.Name,
			Description:    project.Description,
			CreatedAt:      project.CreatedAt,
			UpdatedAt:      project.UpdatedAt,
		}
		if err := tx.Create(&rec).Error; err != nil {
			return err
		}
		for _, slot := range project.Slots {
			slotRec := database.GalaxySlotRecord{
				ProjectID:            project.ID,
				Slot:                 string(slot.Slot),
				CurrentPublicationID: slot.CurrentPublicationID,
			}
			if err := tx.Create(&slotRec).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return unavailable("写入工程", err)
	}
	return nil
}

// PutProjectMeta 实现 galaxy.MutableStore。
//
// 只覆盖名称、简介与更新时间：**它不动发布指针、也不动内容槽**，一次改名不该
// 影响发布态，而"有哪些槽"由槽表回答。
func (s *Store) PutProjectMeta(ctx context.Context, projectID, name, description string, at time.Time) error {
	result := s.db.WithContext(ctx).
		Model(&database.GalaxyProjectRecord{}).
		Where("id = ?", projectID).
		Updates(map[string]any{"name": name, "description": description, "updated_at": at})
	if result.Error != nil {
		return unavailable("写入工程元数据", result.Error)
	}
	if result.RowsAffected == 0 {
		return galaxy.ErrProjectNotFound
	}
	return nil
}

// AddProjectSlot 实现 galaxy.MutableStore。槽已经启用时返回 ErrSlotEnabled。
func (s *Store) AddProjectSlot(ctx context.Context, projectID string, slot galaxy.ContentSlot) error {
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var project database.GalaxyProjectRecord
		if err := tx.First(&project, "id = ?", projectID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return galaxy.ErrProjectNotFound
			}
			return err
		}
		var existing int64
		if err := tx.Model(&database.GalaxySlotRecord{}).
			Where("project_id = ? AND slot = ?", projectID, string(slot)).
			Count(&existing).Error; err != nil {
			return err
		}
		if existing > 0 {
			return galaxy.ErrSlotEnabled
		}
		return tx.Create(&database.GalaxySlotRecord{ProjectID: projectID, Slot: string(slot)}).Error
	})
	if err != nil {
		if errors.Is(err, galaxy.ErrProjectNotFound) || errors.Is(err, galaxy.ErrSlotEnabled) {
			return err
		}
		return unavailable("加入内容槽", err)
	}
	return nil
}

// SetCurrentPublication 实现 galaxy.MutableStore：**只改这一个槽的指针**。
//
// **它不动工程的更新时间**：发布时间与元数据变更时间是两件事，把发布算进
// "更新时间"会让工程列表在每次发布后重排。
func (s *Store) SetCurrentPublication(ctx context.Context, projectID string, slot galaxy.ContentSlot, publicationID string, _ time.Time) error {
	result := s.db.WithContext(ctx).
		Model(&database.GalaxySlotRecord{}).
		Where("project_id = ? AND slot = ?", projectID, string(slot)).
		Update("current_publication_id", publicationID)
	if result.Error != nil {
		return unavailable("写入发布指针", result.Error)
	}
	if result.RowsAffected == 0 {
		// 槽不存在与工程不存在给同一个结论：两者对外都只表现为"没有这个东西"。
		return galaxy.ErrProjectNotFound
	}
	return nil
}

// DeleteProject 实现 galaxy.MutableStore：连同内容槽、版本、草稿、资产与发布
// 记录一并删除。
//
// 放在一个事务里：删到一半的工程是一个"工程还在、站点已经打不开"的中间状态，
// 而它没有任何可解释的对外含义。
func (s *Store) DeleteProject(ctx context.Context, projectID string) error {
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, model := range []any{
			&database.GalaxyVersionRecord{},
			&database.GalaxyAssetTagRecord{},
			&database.GalaxyAssetRecord{},
			&database.GalaxyPublicationRecord{},
			&database.GalaxyDraftRecord{},
			// 预览凭证也是工程的东西：工程没了，它的凭证一条都不该留下。
			&database.GalaxyPreviewGrantRecord{},
			&database.GalaxySlotRecord{},
		} {
			if err := tx.Where("project_id = ?", projectID).Delete(model).Error; err != nil {
				return err
			}
		}
		result := tx.Where("id = ?", projectID).Delete(&database.GalaxyProjectRecord{})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return galaxy.ErrProjectNotFound
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, galaxy.ErrProjectNotFound) {
			return err
		}
		return unavailable("删除工程", err)
	}
	return nil
}

// GetDraft 实现 galaxy.Store。
func (s *Store) GetDraft(ctx context.Context, projectID string, slot galaxy.ContentSlot) (galaxy.Draft, error) {
	var rec database.GalaxyDraftRecord
	err := s.db.WithContext(ctx).
		First(&rec, "project_id = ? AND slot = ?", projectID, string(slot)).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return galaxy.Draft{}, galaxy.ErrDraftNotFound
		}
		return galaxy.Draft{}, unavailable("读取草稿", err)
	}
	return toDraft(rec)
}

// PutDraft 实现 galaxy.MutableStore：整组替换**某一个槽**的草稿，行不存在时创建。
func (s *Store) PutDraft(ctx context.Context, projectID string, slot galaxy.ContentSlot, manifest galaxy.Manifest, at time.Time) error {
	encoded, err := encodeManifest(manifest)
	if err != nil {
		return err
	}
	rec := database.GalaxyDraftRecord{
		ProjectID: projectID,
		Slot:      string(slot),
		Manifest:  encoded,
		UpdatedAt: at,
	}
	err = s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "project_id"}, {Name: "slot"}},
		DoUpdates: clause.AssignmentColumns([]string{"manifest", "updated_at"}),
	}).Create(&rec).Error
	if err != nil {
		return unavailable("写入草稿", err)
	}
	return nil
}

// GetVersion 实现 galaxy.Store。
func (s *Store) GetVersion(ctx context.Context, projectID string, slot galaxy.ContentSlot, versionID string) (galaxy.Version, error) {
	var rec database.GalaxyVersionRecord
	err := s.db.WithContext(ctx).
		First(&rec, "id = ? AND project_id = ? AND slot = ?", versionID, projectID, string(slot)).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return galaxy.Version{}, galaxy.ErrVersionNotFound
		}
		return galaxy.Version{}, unavailable("读取版本", err)
	}
	return toVersion(rec)
}

// ListVersions 实现 galaxy.Store。**清单随行返回**：它只有路径与摘要，几 KB
// 量级，而"哪些版本引用了这个资产"正是靠它回答的。
func (s *Store) ListVersions(ctx context.Context, projectID string, slot galaxy.ContentSlot) ([]galaxy.Version, error) {
	var recs []database.GalaxyVersionRecord
	err := s.db.WithContext(ctx).
		Where("project_id = ? AND slot = ?", projectID, string(slot)).
		Order("seq ASC, id ASC").
		Find(&recs).Error
	if err != nil {
		return nil, unavailable("列出版本", err)
	}
	versions := make([]galaxy.Version, 0, len(recs))
	for _, rec := range recs {
		version, err := toVersion(rec)
		if err != nil {
			return nil, err
		}
		versions = append(versions, version)
	}
	return versions, nil
}

// CreateVersion 实现 galaxy.MutableStore：**在槽内**分配序号。
//
// 查最大值与写入必须在**同一个事务**里：两次并发的保存各自查到同一个最大值时，
// 会写出两个相同的序号，而"查到了什么"与"写进去了什么"必须一致。
//
// 序号以**同一工程、同一个槽**的现存版本的最大值为基准，删除留下的空洞不填补。
// 两个槽各数各的，因此它们各自的第一个版本序号都是 1。
func (s *Store) CreateVersion(ctx context.Context, version galaxy.Version) (galaxy.Version, error) {
	encoded, err := encodeManifest(version.Manifest)
	if err != nil {
		return galaxy.Version{}, err
	}
	var created galaxy.Version
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var maxSeq int64
		row := tx.Model(&database.GalaxyVersionRecord{}).
			Where("project_id = ? AND slot = ?", version.ProjectID, string(version.Slot)).
			Select("COALESCE(MAX(seq), 0)").
			Row()
		if err := row.Scan(&maxSeq); err != nil {
			return err
		}
		rec := database.GalaxyVersionRecord{
			ID:                 version.ID,
			ProjectID:          version.ProjectID,
			Slot:               string(version.Slot),
			Seq:                maxSeq + 1,
			Manifest:           encoded,
			RenderRulesVersion: version.RenderRulesVersion,
			SavedAt:            version.SavedAt,
		}
		if err := tx.Create(&rec).Error; err != nil {
			return err
		}
		var err error
		created, err = toVersion(rec)
		return err
	})
	if err != nil {
		return galaxy.Version{}, unavailable("保存版本", err)
	}
	return created, nil
}

// DeleteVersion 实现 galaxy.MutableStore。
func (s *Store) DeleteVersion(ctx context.Context, projectID string, slot galaxy.ContentSlot, versionID string) error {
	result := s.db.WithContext(ctx).
		Where("id = ? AND project_id = ? AND slot = ?", versionID, projectID, string(slot)).
		Delete(&database.GalaxyVersionRecord{})
	if result.Error != nil {
		return unavailable("删除版本", result.Error)
	}
	if result.RowsAffected == 0 {
		return galaxy.ErrVersionNotFound
	}
	return nil
}
