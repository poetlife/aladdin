package gormstore

import (
	"context"
	"errors"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/poetlife/aladdin/internal/database"
	"github.com/poetlife/aladdin/internal/galaxy"
)

// GetPublication 实现 galaxy.Store：按发布标识读回产物清单。
func (s *Store) GetPublication(ctx context.Context, publicationID string) (galaxy.Publication, error) {
	var rec database.GalaxyPublicationRecord
	if err := s.db.WithContext(ctx).First(&rec, "id = ?", publicationID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return galaxy.Publication{}, galaxy.ErrPublicationNotFound
		}
		return galaxy.Publication{}, unavailable("读取发布记录", err)
	}
	return toPublication(rec)
}

// PutPublication 实现 galaxy.MutableStore：**按发布标识幂等写入**。
//
// 冲突时覆盖：发布标识只由工程与版本决定，所以"同一条记录"是同一件事的第二次
// 留痕，而它的产物是确定性的（同样的清单与同样的资产 → 逐字相同的产物清单）。
// 覆盖而不是插入第二条，是"落库"这个检查点可续跑的前提——重启后重试一次发布，
// 库里不会出现两条记录。
func (s *Store) PutPublication(ctx context.Context, publication galaxy.Publication) error {
	encoded, err := encodeManifest(publication.Manifest)
	if err != nil {
		return err
	}
	rec := database.GalaxyPublicationRecord{
		ID:                   publication.ID,
		ProjectID:            publication.ProjectID,
		VersionID:            publication.VersionID,
		Manifest:             encoded,
		PublishedBySubjectID: publication.PublishedBySubjectID,
		PublishedAt:          publication.PublishedAt,
	}
	err = s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "id"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"project_id", "version_id", "manifest", "published_by_subject_id", "published_at",
		}),
	}).Create(&rec).Error
	if err != nil {
		return unavailable("写入发布记录", err)
	}
	return nil
}
