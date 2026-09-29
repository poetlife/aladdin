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
		Form:                 string(project.Form),
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
// 只覆盖名称、简介与更新时间：**它不动发布指针、也不动形态**，一次改名不该影响
// 发布态，而形态本来就改不了。
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
// 放在一个事务里：删到一半的工程是一个"工程还在、站点已经打不开"的中间状态，
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
	return toDraft(rec)
}

// PutDraft 实现 galaxy.MutableStore：整组替换，行不存在时创建。
func (s *Store) PutDraft(ctx context.Context, projectID string, manifest galaxy.Manifest, at time.Time) error {
	encoded, err := encodeManifest(manifest)
	if err != nil {
		return err
	}
	rec := database.GalaxyDraftRecord{
		ProjectID: projectID,
		Manifest:  encoded,
		UpdatedAt: at,
	}
	err = s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "project_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"manifest", "updated_at"}),
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
	return toVersion(rec)
}

// ListVersions 实现 galaxy.Store。**清单随行返回**：它只有路径与摘要，几 KB
// 量级，而"哪些版本引用了这个资产"正是靠它回答的。
func (s *Store) ListVersions(ctx context.Context, projectID string) ([]galaxy.Version, error) {
	var recs []database.GalaxyVersionRecord
	err := s.db.WithContext(ctx).
		Where("project_id = ?", projectID).
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

// CreateVersion 实现 galaxy.MutableStore：在工程内分配序号。
//
// 查最大值与写入必须在**同一个事务**里：两次并发的保存各自查到同一个最大值时，
// 会写出两个相同的序号，而"查到了什么"与"写进去了什么"必须一致。
//
// 序号以现存版本的最大值为基准，删除留下的空洞不填补。
func (s *Store) CreateVersion(ctx context.Context, version galaxy.Version) (galaxy.Version, error) {
	encoded, err := encodeManifest(version.Manifest)
	if err != nil {
		return galaxy.Version{}, err
	}
	var created galaxy.Version
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var maxSeq int64
		row := tx.Model(&database.GalaxyVersionRecord{}).
			Where("project_id = ?", version.ProjectID).
			Select("COALESCE(MAX(seq), 0)").
			Row()
		if err := row.Scan(&maxSeq); err != nil {
			return err
		}
		rec := database.GalaxyVersionRecord{
			ID:                 version.ID,
			ProjectID:          version.ProjectID,
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
