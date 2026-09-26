// 本文件是身份别名的关系库实现。它属于 gormstore 包，包级说明见 store.go。
package gormstore

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/poetlife/aladdin/internal/database"
	"github.com/poetlife/aladdin/internal/identity"
)

// IdentityStore 是 identity.IdentityStore 的关系库实现。
type IdentityStore struct {
	db *gorm.DB
}

// NewIdentityStore 用已打开的连接构造身份别名存储。
func NewIdentityStore(db *gorm.DB) *IdentityStore {
	return &IdentityStore{db: db}
}

// Lookup 实现 identity.IdentityStore。
func (s *IdentityStore) Lookup(ctx context.Context, source, externalID string) (identity.Identity, error) {
	var rec database.IdentityRecord
	err := s.db.WithContext(ctx).
		First(&rec, "source = ? AND external_id = ?", source, externalID).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return identity.Identity{}, identity.ErrIdentityNotFound
		}
		return identity.Identity{}, identityUnavailable("读取身份", err)
	}
	return toIdentity(rec), nil
}

// Put 实现 identity.IdentityStore。
//
// 归属唯一由"插入被忽略"与"读到的那一行属于谁"两个事实共同保证，而不是
// 先查后写：先查后写在并发下不成立——两个请求可以同时读到"没人占"。
//
// 判定刻意**不看 UPDATE 影响的行数**：MySQL 在"新值与旧值相同"时报 0 行，
// 拿它当归属依据会把一次幂等的重复绑定误报成"已被别人占用"。
func (s *IdentityStore) Put(ctx context.Context, ident identity.Identity) error {
	rec := fromIdentity(ident)

	inserted := s.db.WithContext(ctx).
		Clauses(clause.OnConflict{DoNothing: true}).
		Create(&rec)
	if inserted.Error != nil {
		return identityUnavailable("写入身份", inserted.Error)
	}
	if inserted.RowsAffected > 0 {
		// 这个身份本来不存在，现在归这个主体了。
		return nil
	}

	var existing database.IdentityRecord
	err := s.db.WithContext(ctx).
		First(&existing, "source = ? AND external_id = ?", ident.Source, ident.ExternalID).Error
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		// 这一行在两步之间被解绑了。再插一次，这次的结果就是最终结果：
		// 它与判断之间不再有第三件事发生。插不进去只能说明又有人抢先，
		// 那时报"已被占用"是保守的一侧——宁可不绑，也不夺走别人的身份。
		retry := s.db.WithContext(ctx).
			Clauses(clause.OnConflict{DoNothing: true}).
			Create(&rec)
		if retry.Error != nil {
			return identityUnavailable("写入身份", retry.Error)
		}
		if retry.RowsAffected > 0 {
			return nil
		}
		return identityTaken(ident)
	case err != nil:
		return identityUnavailable("写入身份", err)
	case existing.SubjectID != ident.SubjectID:
		return identityTaken(ident)
	}

	// 已经属于同一个主体：只刷新展示信息，重复绑定是幂等的。
	// 条件里仍带 subject_id——即使归属在上一步之后发生了变化，
	// 这次更新也只会落空，而不会改到别人的行上。
	err = s.db.WithContext(ctx).Model(&database.IdentityRecord{}).
		Where("source = ? AND external_id = ? AND subject_id = ?",
			ident.Source, ident.ExternalID, ident.SubjectID).
		Update("display", ident.Display).Error
	if err != nil {
		return identityUnavailable("写入身份", err)
	}
	return nil
}

// Delete 实现 identity.IdentityStore。
//
// 删除与"是不是最后一个"必须在同一次事务里完成。分开写的话，两个并发的
// 解绑会各查一次"还有两个身份"，然后各删一个——留下一个再也没有任何进入
// 方式的主体，而它的角色绑定还在，没有人能进来清理。
func (s *IdentityStore) Delete(ctx context.Context, source, externalID, subjectID string) (bool, error) {
	removed := false
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Where("source = ? AND external_id = ? AND subject_id = ?",
			source, externalID, subjectID).
			Delete(&database.IdentityRecord{})
		if result.Error != nil {
			return identityUnavailable("解绑身份", result.Error)
		}
		if result.RowsAffected == 0 {
			// 不存在，或者不是这个主体的。两者对调用方是同一件事。
			return nil
		}

		var remaining int64
		if err := tx.Model(&database.IdentityRecord{}).
			Where("subject_id = ?", subjectID).Count(&remaining).Error; err != nil {
			return identityUnavailable("解绑身份", err)
		}
		if remaining == 0 {
			// 返回错误即回滚，因此这一行不会被真的删掉。
			return fmt.Errorf("%w: %s", identity.ErrLastIdentity, subjectID)
		}
		removed = true
		return nil
	})
	return removed, err
}

// ListBySubject 实现 identity.IdentityStore。
//
// 不排序：顺序由调用方定，否则内存实现（按 map 遍历）与这里会各有一套，
// 而"列出来的渠道每次顺序都不一样"会让界面看起来在抖动。
func (s *IdentityStore) ListBySubject(ctx context.Context, subjectID string) ([]identity.Identity, error) {
	var recs []database.IdentityRecord
	if err := s.db.WithContext(ctx).
		Where("subject_id = ?", subjectID).Find(&recs).Error; err != nil {
		return nil, identityUnavailable("读取主体的身份", err)
	}
	list := make([]identity.Identity, 0, len(recs))
	for _, rec := range recs {
		list = append(list, toIdentity(rec))
	}
	return list, nil
}

// toIdentity 把记录翻译成领域类型。
//
// 转换只在这一处：记录里加一列不会悄悄改变认证看到的东西，
// 除非这里也跟着改——而那时改动是显式的。
func toIdentity(rec database.IdentityRecord) identity.Identity {
	return identity.Identity{
		Source:     rec.Source,
		ExternalID: rec.ExternalID,
		SubjectID:  rec.SubjectID,
		Display:    rec.Display,
	}
}

func fromIdentity(ident identity.Identity) database.IdentityRecord {
	return database.IdentityRecord{
		Source:     ident.Source,
		ExternalID: ident.ExternalID,
		SubjectID:  ident.SubjectID,
		Display:    ident.Display,
	}
}

// identityUnavailable 把底层错误包装成存储不可用。
//
// 与"身份未登记""身份已被占用"分开是关键：后两者是合格的业务结论，
// 这个必须让请求快速失败，而不是被当成"这次登录可以用"或"这个身份不能用"。
func identityUnavailable(action string, err error) error {
	return fmt.Errorf("%w: %s: %w", identity.ErrStoreUnavailable, action, err)
}

// identityTaken 构造"这个身份已属于另一个主体"的错误。
//
// 错误信息里**不带占用者的标识**：透露它等于把一次失败的绑定变成一次
// "这个渠道对应哪个主体"的探测。
func identityTaken(ident identity.Identity) error {
	return fmt.Errorf("%w: %s", identity.ErrIdentityTaken, ident.Source)
}

var _ identity.IdentityStore = (*IdentityStore)(nil)
