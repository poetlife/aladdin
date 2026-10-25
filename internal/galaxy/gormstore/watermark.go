package gormstore

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/poetlife/aladdin/internal/database/entity"
	"github.com/poetlife/aladdin/internal/galaxy"
)

// GetWatermarkVisit 实现 galaxy.Store：按码读回一条水印登记。
//
// **它不判定保留期**：读只回答"库里有没有这一条"，过期与否由调用方比较时间
// （见 galaxy.WatermarkVisit.Expired）。把判定放进读里，"这枚码此刻算不算数"
// 就会有两种说法。
func (s *Store) GetWatermarkVisit(ctx context.Context, code string) (galaxy.WatermarkVisit, error) {
	var rec entity.GalaxyWatermarkVisitRecord
	if err := s.db.WithContext(ctx).First(&rec, "code = ?", code).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return galaxy.WatermarkVisit{}, galaxy.ErrWatermarkNotFound
		}
		return galaxy.WatermarkVisit{}, unavailable("读取水印登记", err)
	}
	return toWatermarkVisit(rec), nil
}

// ReuseOrPutWatermarkVisit 实现 galaxy.MutableStore：同一访客在窗口内已有登记时返回
// 那一条、不写入；否则写入并顺带清掉超出保留期的那些。
//
// **查找与写入在同一个事务里**：分成两步的话，同一访客同时打开两次会各签一枚、
// 各写一行，"窗口内复用同一枚码"当场失效——而它的失效方式（登记表按页面浏览量
// 增长）要到线上跑了几个月才看得出来。
//
// 清理用 `visited_at < cleanupBefore` 而不是"删这个工程的"：保留期是全局的，一条
// 过期的登记对任何工程都没有用处，而一次只清一个工程会让"没人再打开的工程"把
// 它们的旧行永远留着。
func (s *Store) ReuseOrPutWatermarkVisit(ctx context.Context, visit galaxy.WatermarkVisit, reuseSince, cleanupBefore time.Time) (galaxy.WatermarkVisit, error) {
	var reused galaxy.WatermarkVisit
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("visited_at < ?", cleanupBefore).
			Delete(&entity.GalaxyWatermarkVisitRecord{}).Error; err != nil {
			return err
		}

		var existing entity.GalaxyWatermarkVisitRecord
		err := tx.Where("project_id = ? AND slot = ? AND identity_key = ? AND visited_at >= ?",
			visit.ProjectID, string(visit.Slot), visit.IdentityKey, reuseSince).
			Order("visited_at DESC").
			First(&existing).Error
		switch {
		case err == nil:
			reused = toWatermarkVisit(existing)
			return nil
		case !errors.Is(err, gorm.ErrRecordNotFound):
			return err
		}

		rec := entity.GalaxyWatermarkVisitRecord{
			Code:            visit.Code,
			ProjectID:       visit.ProjectID,
			Slot:            string(visit.Slot),
			SubjectID:       visit.SubjectID,
			IdentityKey:     visit.IdentityKey,
			SourceAddress:   visit.SourceAddress,
			UserAgentDigest: visit.UserAgentDigest,
			VisitedAt:       visit.VisitedAt,
		}
		if err := tx.Create(&rec).Error; err != nil {
			return err
		}
		reused = visit
		return nil
	})
	if err != nil {
		return galaxy.WatermarkVisit{}, unavailable("写入水印登记", err)
	}
	return reused, nil
}

// toWatermarkVisit 把库里的行翻译成领域取值。
func toWatermarkVisit(rec entity.GalaxyWatermarkVisitRecord) galaxy.WatermarkVisit {
	return galaxy.WatermarkVisit{
		Code:            rec.Code,
		ProjectID:       rec.ProjectID,
		Slot:            galaxy.ContentSlot(rec.Slot),
		SubjectID:       rec.SubjectID,
		IdentityKey:     rec.IdentityKey,
		SourceAddress:   rec.SourceAddress,
		UserAgentDigest: rec.UserAgentDigest,
		VisitedAt:       rec.VisitedAt,
	}
}
