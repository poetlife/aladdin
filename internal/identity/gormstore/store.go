// Package gormstore 是 identity.SessionStore 的关系库实现。
//
// 它持有的是连接而不是配置：方言解析、连接串归一、连接池取值都在
// internal/database 完成，迁移由 internal/database/migrate 推进，
// 本包只消费一个已经准备好表的 *gorm.DB。
package gormstore

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/poetlife/aladdin/internal/database"
	"github.com/poetlife/aladdin/internal/identity"
	"github.com/poetlife/aladdin/internal/rbac"
)

// Store 是会话的关系库实现。
type Store struct {
	db *gorm.DB
}

// New 用已打开的连接构造存储。
//
// 它不迁移：表由启动路径上的迁移建好，本构造函数只负责把记录类型与
// 领域类型对上。
func New(db *gorm.DB) *Store {
	return &Store{db: db}
}

// Put 实现 identity.SessionStore。
//
// 冲突时覆盖全部非主键列。摘要重复在当前设计下不会发生（摘要是随机凭证的
// 哈希），但覆盖语义让"写入"是幂等的，不必让调用方去保证唯一性。
// 表结构的唯一信源是 internal/database/schema.go。
func (s *Store) Put(ctx context.Context, tokenHash string, session identity.Session) error {
	rec := database.SessionRecord{
		TokenHash:    tokenHash,
		SubjectID:    session.SubjectID,
		SubjectType:  string(session.SubjectType),
		DefaultScope: string(session.DefaultScope),
		IssuedAt:     session.IssuedAt,
		ExpiresAt:    session.ExpiresAt,
	}
	err := s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "token_hash"}},
		UpdateAll: true,
	}).Create(&rec).Error
	if err != nil {
		return unavailable("写入会话", err)
	}
	return nil
}

// Get 实现 identity.SessionStore。
//
// **不按过期时间过滤**：过期与否由 identity.Sessions 判断，那是"这份凭证
// 还能不能用"的唯一实现处。在这里再写一次 WHERE，内存实现与 SQL 实现就
// 会各有一份判断，而两份判断迟早会有一份漏掉——漏掉的那份就是"过期了
// 还能用"。
func (s *Store) Get(ctx context.Context, tokenHash string) (identity.Session, error) {
	var rec database.SessionRecord
	if err := s.db.WithContext(ctx).First(&rec, "token_hash = ?", tokenHash).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return identity.Session{}, identity.ErrSessionNotFound
		}
		return identity.Session{}, unavailable("读取会话", err)
	}
	return toSession(rec), nil
}

// Delete 实现 identity.SessionStore。
//
// 删掉零行不是错误：重复登出、登出一个早已过期的会话，结果一致，
// 不该让调用方为此写判断。
func (s *Store) Delete(ctx context.Context, tokenHash string) error {
	err := s.db.WithContext(ctx).
		Where("token_hash = ?", tokenHash).
		Delete(&database.SessionRecord{}).Error
	if err != nil {
		return unavailable("撤销会话", err)
	}
	return nil
}

// DeleteExpired 实现 identity.SessionStore。
//
// 边界条件必须与 identity.Session.Expired 一致：那边是 now >= ExpiresAt
// 即过期，这边就是 expires_at <= now。差一个等号，两个实现在"恰好到点"
// 的那一瞬间会给出不同答案，而契约测试正是为了挡住这种事。
func (s *Store) DeleteExpired(ctx context.Context, now time.Time) (int64, error) {
	result := s.db.WithContext(ctx).
		Where("expires_at <= ?", now).
		Delete(&database.SessionRecord{})
	if result.Error != nil {
		return 0, unavailable("回收过期会话", result.Error)
	}
	return result.RowsAffected, nil
}

// toSession 把记录翻译成领域类型。
//
// 转换只在这一处：记录里加一列不会悄悄改变认证看到的东西，
// 除非这里也跟着改——而那时改动是显式的。
func toSession(rec database.SessionRecord) identity.Session {
	return identity.Session{
		SubjectID:    rec.SubjectID,
		SubjectType:  rbac.SubjectType(rec.SubjectType),
		DefaultScope: rbac.Scope(rec.DefaultScope),
		IssuedAt:     rec.IssuedAt,
		ExpiresAt:    rec.ExpiresAt,
	}
}

var _ identity.SessionStore = (*Store)(nil)
