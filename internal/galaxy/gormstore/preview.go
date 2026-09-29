package gormstore

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/poetlife/aladdin/internal/database"
	"github.com/poetlife/aladdin/internal/galaxy"
)

// GetPreviewGrant 实现 galaxy.Store：按凭证本体读回一条预览凭证。
//
// **它不判定过期**：读只回答"库里有没有这一条"，过期与否由调用方比较时间
// （见 galaxy.PreviewGrant.Expired）。把判定放进读里，"这条凭证此刻算不算数"
// 就会有两种说法。
func (s *Store) GetPreviewGrant(ctx context.Context, token string) (galaxy.PreviewGrant, error) {
	var rec database.GalaxyPreviewGrantRecord
	if err := s.db.WithContext(ctx).First(&rec, "token = ?", token).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return galaxy.PreviewGrant{}, galaxy.ErrPreviewGrantNotFound
		}
		return galaxy.PreviewGrant{}, unavailable("读取预览凭证", err)
	}
	return galaxy.PreviewGrant{
		Token:     rec.Token,
		ProjectID: rec.ProjectID,
		Slot:      galaxy.ContentSlot(rec.Slot),
		SubjectID: rec.SubjectID,
		ExpiresAt: rec.ExpiresAt,
		CreatedAt: rec.CreatedAt,
	}, nil
}

// PutPreviewGrant 实现 galaxy.MutableStore：写入一条凭证，并清掉该工程下已经过期
// 的那些。
//
// **两件事在同一个事务里**：清理挂在写入这一处，表的增长于是只由"还有多少条活着
// 的凭证"决定；挂在读上会让一次取图顺带写库。清理用的失效时刻由调用方给出（见
// galaxy.MutableStore 的说明），因此判断留在上层、这条清理可测。
func (s *Store) PutPreviewGrant(ctx context.Context, grant galaxy.PreviewGrant, cleanupBefore time.Time) error {
	rec := database.GalaxyPreviewGrantRecord{
		Token:     grant.Token,
		ProjectID: grant.ProjectID,
		Slot:      string(grant.Slot),
		SubjectID: grant.SubjectID,
		ExpiresAt: grant.ExpiresAt,
		CreatedAt: grant.CreatedAt,
	}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("project_id = ? AND expires_at <= ?", grant.ProjectID, cleanupBefore).
			Delete(&database.GalaxyPreviewGrantRecord{}).Error; err != nil {
			return err
		}
		return tx.Create(&rec).Error
	})
	if err != nil {
		return unavailable("写入预览凭证", err)
	}
	return nil
}
