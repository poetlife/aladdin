package gormstore

import (
	"context"
	"errors"
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
	return toProject(rec), nil
}

// ListProjectsByOwner 实现 galaxy.Store。
//
// 它按拥有者过滤，而不是返回全表再让上层筛：范围由查询本身给定，因此"忘了筛"
// 这件事在这个形状下写不出来。
func (s *Store) ListProjectsByOwner(ctx context.Context, ownerSubjectID string) ([]galaxy.Project, error) {
	var recs []database.GalaxyProjectRecord
	err := s.db.WithContext(ctx).
		Where("owner_subject_id = ?", ownerSubjectID).
		Order("updated_at DESC, id ASC").
		Find(&recs).Error
	if err != nil {
		return nil, unavailable("列出工程", err)
	}
	projects := make([]galaxy.Project, 0, len(recs))
	for _, rec := range recs {
		projects = append(projects, toProject(rec))
	}
	return projects, nil
}

// CreateProject 实现 galaxy.MutableStore。
func (s *Store) CreateProject(ctx context.Context, project galaxy.Project) error {
	rec := database.GalaxyProjectRecord{
		ID:                   project.ID,
		OwnerSubjectID:       project.OwnerSubjectID,
		Name:                 project.Name,
		Description:          project.Description,
		CurrentPublicationID: project.CurrentPublicationID,
		CreatedAt:            project.CreatedAt,
		UpdatedAt:            project.UpdatedAt,
	}
	if err := s.db.WithContext(ctx).Create(&rec).Error; err != nil {
		return unavailable("写入工程", err)
	}
	return nil
}

// PutProjectMeta 实现 galaxy.MutableStore。
//
// 只覆盖名称、简介与更新时间：**它不动发布指针**，一次改名不该影响发布态。
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

// SetCurrentPublication 实现 galaxy.MutableStore。publicationID 为空表示撤回。
//
// **它不动工程的更新时间**：发布时间与元数据变更时间是两件事，把发布算进
// "更新时间"会让工程列表在每次发布后重排。
func (s *Store) SetCurrentPublication(ctx context.Context, projectID, publicationID string, _ time.Time) error {
	result := s.db.WithContext(ctx).
		Model(&database.GalaxyProjectRecord{}).
		Where("id = ?", projectID).
		Update("current_publication_id", publicationID)
	if result.Error != nil {
		return unavailable("写入发布指针", result.Error)
	}
	if result.RowsAffected == 0 {
		return galaxy.ErrProjectNotFound
	}
	return nil
}

// DeleteProject 实现 galaxy.MutableStore：连同版本、草稿、资产与发布记录一并删除。
//
// 放在一个事务里：删到一半的工程是一个"工程还在、页面已经打不开"的中间状态，
// 而它没有任何可解释的对外含义。
func (s *Store) DeleteProject(ctx context.Context, projectID string) error {
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, model := range []any{
			&database.GalaxyVersionRecord{},
			&database.GalaxyAssetRecord{},
			&database.GalaxyPublicationRecord{},
			&database.GalaxyDraftRecord{},
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
func (s *Store) GetDraft(ctx context.Context, projectID string) (galaxy.Draft, error) {
	var rec database.GalaxyDraftRecord
	if err := s.db.WithContext(ctx).First(&rec, "project_id = ?", projectID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return galaxy.Draft{}, galaxy.ErrDraftNotFound
		}
		return galaxy.Draft{}, unavailable("读取草稿", err)
	}
	return toDraft(rec), nil
}

// PutDraft 实现 galaxy.MutableStore：行不存在时创建。
func (s *Store) PutDraft(ctx context.Context, projectID string, content galaxy.Document, at time.Time) error {
	rec := database.GalaxyDraftRecord{
		ProjectID: projectID,
		Content:   string(content),
		UpdatedAt: at,
	}
	err := s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "project_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"content", "updated_at"}),
	}).Create(&rec).Error
	if err != nil {
		return unavailable("写入草稿", err)
	}
	return nil
}

// GetVersion 实现 galaxy.Store。
func (s *Store) GetVersion(ctx context.Context, projectID, versionID string) (galaxy.Version, error) {
	var rec database.GalaxyVersionRecord
	err := s.db.WithContext(ctx).
		First(&rec, "id = ? AND project_id = ?", versionID, projectID).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return galaxy.Version{}, galaxy.ErrVersionNotFound
		}
		return galaxy.Version{}, unavailable("读取版本", err)
	}
	return toVersion(rec), nil
}

// ListVersions 实现 galaxy.Store。**不带正文。**
//
// Select 显式列出要读的列：不这么做的话，一次"列出版本"会把每个版本的正文都
// 拉进内存，而列表接口根本不显示它们。
func (s *Store) ListVersions(ctx context.Context, projectID string) ([]galaxy.Version, error) {
	var recs []database.GalaxyVersionRecord
	err := s.db.WithContext(ctx).
		Select("id", "project_id", "seq", "saved_at").
		Where("project_id = ?", projectID).
		Order("seq ASC, id ASC").
		Find(&recs).Error
	if err != nil {
		return nil, unavailable("列出版本", err)
	}
	versions := make([]galaxy.Version, 0, len(recs))
	for _, rec := range recs {
		versions = append(versions, toVersion(rec))
	}
	return versions, nil
}

// ListVersionContents 实现 galaxy.Store。**带正文**：只有"哪些版本引用了这个
// 资产"这一个判断需要它。
func (s *Store) ListVersionContents(ctx context.Context, projectID string) ([]galaxy.Version, error) {
	var recs []database.GalaxyVersionRecord
	err := s.db.WithContext(ctx).
		Where("project_id = ?", projectID).
		Order("seq ASC, id ASC").
		Find(&recs).Error
	if err != nil {
		return nil, unavailable("列出版本正文", err)
	}
	versions := make([]galaxy.Version, 0, len(recs))
	for _, rec := range recs {
		versions = append(versions, toVersion(rec))
	}
	return versions, nil
}

// CreateVersion 实现 galaxy.MutableStore：在工程内分配序号。
//
// 查最大值与写入必须在**同一个事务**里：两次并发的保存各自查到同一个最大值时，
// 会写出两个相同的序号，而"查到了什么"与"写进去了什么"必须一致。
//
// 序号以现存版本的最大值为基准，删除留下的空洞不填补。
func (s *Store) CreateVersion(ctx context.Context, version galaxy.Version) (galaxy.Version, error) {
	var created galaxy.Version
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var maxSeq int64
		row := tx.Model(&database.GalaxyVersionRecord{}).
			Where("project_id = ?", version.ProjectID).
			Select("COALESCE(MAX(seq), 0)").
			Row()
		if err := row.Scan(&maxSeq); err != nil {
			return err
		}
		rec := database.GalaxyVersionRecord{
			ID:        version.ID,
			ProjectID: version.ProjectID,
			Seq:       maxSeq + 1,
			Content:   string(version.Content),
			SavedAt:   version.SavedAt,
		}
		if err := tx.Create(&rec).Error; err != nil {
			return err
		}
		created = toVersion(rec)
		return nil
	})
	if err != nil {
		return galaxy.Version{}, unavailable("保存版本", err)
	}
	return created, nil
}

// DeleteVersion 实现 galaxy.MutableStore。
func (s *Store) DeleteVersion(ctx context.Context, projectID, versionID string) error {
	result := s.db.WithContext(ctx).
		Where("id = ? AND project_id = ?", versionID, projectID).
		Delete(&database.GalaxyVersionRecord{})
	if result.Error != nil {
		return unavailable("删除版本", result.Error)
	}
	if result.RowsAffected == 0 {
		return galaxy.ErrVersionNotFound
	}
	return nil
}
