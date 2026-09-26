// Package gormstore 是 profile.Store 的关系库实现。
//
// 它持有的是连接而不是配置：方言解析、连接串归一、连接池取值都在
// internal/database 完成，迁移由 internal/database/migrate 推进，
// 本包只消费一个已经准备好表的 *gorm.DB。
package gormstore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/poetlife/aladdin/internal/database"
	"github.com/poetlife/aladdin/internal/profile"
)

// Store 是 profile.Store 的关系库实现。
type Store struct {
	db *gorm.DB
}

// New 用已打开的连接构造档案存储。
//
// 它不迁移：表由启动路径上的迁移建好，本构造函数只负责把记录类型与
// 领域类型对上。
func New(db *gorm.DB) *Store {
	return &Store{db: db}
}

// Get 实现 profile.Store。
func (s *Store) Get(ctx context.Context, subjectID string) (profile.Profile, error) {
	var rec database.SubjectProfileRecord
	if err := s.db.WithContext(ctx).First(&rec, "subject_id = ?", subjectID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return profile.Profile{}, profile.ErrProfileNotFound
		}
		return profile.Profile{}, unavailable("读取档案", err)
	}
	return toProfile(rec), nil
}

// PutText 实现 profile.Store。
//
// 冲突时**只覆盖昵称、简介与更新时间**，不碰头像对象键。用 UpdateAll 会让
// 一次改名顺手把头像清掉，而那是两个互不相干的操作——把它写成一条会互相
// 覆盖的语句，正是 profile.Store 按字段分成两个方法的理由。
func (s *Store) PutText(ctx context.Context, subjectID, nickname, bio string, at time.Time) error {
	rec := database.SubjectProfileRecord{
		SubjectID: subjectID,
		Nickname:  nickname,
		Bio:       bio,
		UpdatedAt: at,
	}
	err := s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "subject_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"nickname", "bio", "updated_at"}),
	}).Create(&rec).Error
	if err != nil {
		return unavailable("写入档案", err)
	}
	return nil
}

// PutAvatarKey 实现 profile.Store。key 为空表示清除头像。
//
// 与 PutText 同理，冲突时只覆盖头像对象键与更新时间，不碰昵称与简介。
func (s *Store) PutAvatarKey(ctx context.Context, subjectID, key string, at time.Time) error {
	rec := database.SubjectProfileRecord{
		SubjectID: subjectID,
		AvatarKey: key,
		UpdatedAt: at,
	}
	err := s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "subject_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"avatar_key", "updated_at"}),
	}).Create(&rec).Error
	if err != nil {
		return unavailable("写入头像对象键", err)
	}
	return nil
}

// toProfile 把记录翻译成领域类型。
//
// 转换只在这一处：记录里加一列不会悄悄改变展示看到的东西，除非这里也跟着改
// ——而那时改动是显式的。
func toProfile(rec database.SubjectProfileRecord) profile.Profile {
	return profile.Profile{
		SubjectID: rec.SubjectID,
		Nickname:  rec.Nickname,
		Bio:       rec.Bio,
		AvatarKey: rec.AvatarKey,
		UpdatedAt: rec.UpdatedAt,
	}
}

// unavailable 把底层错误包装成存储不可用。
//
// 与"没有档案"分开是关键：后者是正常状态（档案行是惰性创建的），这个必须
// 让请求快速失败，而不是被当成"这个人什么都没有设过"。
func unavailable(action string, err error) error {
	return fmt.Errorf("%w: %s: %w", profile.ErrStoreUnavailable, action, err)
}

var _ profile.Store = (*Store)(nil)
