// Package gormstore 是 skill.Store 的关系库实现。
//
// 它持有的是连接而不是配置：方言解析、连接串归一、连接池取值都在
// internal/database 完成，迁移由 internal/database/migrate 推进（见
// migration_0013_skill_catalog.go），本包只消费一个已经准备好表的 *gorm.DB。
//
// 六张表：技能、版本、标签、展示图集、收藏与使用日次。**字节不在其中任何一张表里**
// ——内容是一组「路径 → 内容摘要 + 字节数」，随版本行整份读写（见 encodeFiles）；
// 展示图只有键与字节数，字节在对象存储（见 internal/skill/image.go）。
package gormstore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/poetlife/aladdin/internal/database"
	"github.com/poetlife/aladdin/internal/skill"
)

// Store 是 skill.Store 的关系库实现。
type Store struct {
	db *gorm.DB
}

// New 用已打开的连接构造技能目录存储。
//
// 它不迁移：表由启动路径上的迁移建好，本构造函数只负责把记录类型与领域类型对上。
func New(db *gorm.DB) *Store { return &Store{db: db} }

// unavailable 把底层错误包装成存储不可用。
//
// 与"技能不存在"分开是关键：后者是正常状态，这个必须让请求快速失败，而不是被
// 当成"这个技能没有了"。契约测试会断言两种实现的错误类别一致。
func unavailable(action string, err error) error {
	return fmt.Errorf("%w: %s: %w", skill.ErrStoreUnavailable, action, err)
}

// ListSkills 实现 skill.Store。
func (s *Store) ListSkills(ctx context.Context) ([]skill.Skill, error) {
	var records []database.SkillRecord
	if err := s.db.WithContext(ctx).Find(&records).Error; err != nil {
		return nil, unavailable("列出技能", err)
	}
	if len(records) == 0 {
		return nil, nil
	}
	ids := make([]string, 0, len(records))
	versionIDs := make([]string, 0, len(records))
	for _, record := range records {
		ids = append(ids, record.ID)
		versionIDs = append(versionIDs, record.CurrentVersionID)
	}
	versions, err := s.versionsByID(ctx, versionIDs)
	if err != nil {
		return nil, err
	}
	tags, err := s.tagsBySkill(ctx, ids)
	if err != nil {
		return nil, err
	}
	images, err := s.imagesBySkill(ctx, ids)
	if err != nil {
		return nil, err
	}

	items := make([]skill.Skill, 0, len(records))
	for _, record := range records {
		version, ok := versions[record.CurrentVersionID]
		if !ok {
			// 指针指向一个不存在的版本：它只可能来自一次没走完的写入，而契约
			// 要求那种写入整体失败。因此这里不是"跳过这一条"——静默跳过会让
			// 目录少一条而没人看得出来。
			return nil, unavailable("读取当前版本",
				fmt.Errorf("技能 %s 的当前版本 %s 不存在", record.ID, record.CurrentVersionID))
		}
		items = append(items, toSkill(record, version, tags[record.ID], images[record.ID]))
	}
	return items, nil
}

// GetSkill 实现 skill.Store。
func (s *Store) GetSkill(ctx context.Context, skillID string) (skill.Skill, error) {
	record, err := s.skillRecord(ctx, skillID)
	if err != nil {
		return skill.Skill{}, err
	}
	version, err := s.versionRecord(ctx, skillID, record.CurrentVersionID)
	if err != nil {
		return skill.Skill{}, err
	}
	tags, err := s.tagsBySkill(ctx, []string{skillID})
	if err != nil {
		return skill.Skill{}, err
	}
	images, err := s.imagesBySkill(ctx, []string{skillID})
	if err != nil {
		return skill.Skill{}, err
	}
	return toSkill(record, version, tags[skillID], images[skillID]), nil
}

// ListVersions 实现 skill.Store（最新的在前）。
func (s *Store) ListVersions(ctx context.Context, skillID string) ([]skill.Version, error) {
	if _, err := s.skillRecord(ctx, skillID); err != nil {
		return nil, err
	}
	var records []database.SkillVersionRecord
	// 排序键里带上标识：同一秒内产生的两个版本也要有确定的先后，否则同一份数据
	// 在不同实现、不同一次查询里会给出不同的顺序。
	if err := s.db.WithContext(ctx).
		Where("skill_id = ?", skillID).
		Order("created_at DESC, id DESC").
		Find(&records).Error; err != nil {
		return nil, unavailable("列出技能版本", err)
	}
	versions := make([]skill.Version, 0, len(records))
	for _, record := range records {
		versions = append(versions, toVersion(record))
	}
	return versions, nil
}

// CreateSkill 实现 skill.Store。技能行、第一个版本行与标签行**一个整体**。
func (s *Store) CreateSkill(ctx context.Context, item skill.Skill) error {
	files, err := encodeFiles(item.Current.Files)
	if err != nil {
		return err
	}
	now := time.Now()
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		record := database.SkillRecord{
			ID:               item.ID,
			Title:            item.Title,
			Summary:          item.Summary,
			SourceOwner:      item.Source.Owner,
			SourceName:       item.Source.Name,
			SourceRef:        item.Source.Ref,
			SourceSubPath:    item.Source.SubPath,
			SourceCommit:     item.Source.Commit,
			CurrentVersionID: item.Current.ID,
			CreatedAt:        now,
			UpdatedAt:        now,
		}
		if err := tx.Create(&record).Error; err != nil {
			return err
		}
		version := database.SkillVersionRecord{
			ID:           item.Current.ID,
			SkillID:      item.ID,
			Commit:       item.Current.Commit,
			Name:         item.Current.Name,
			Description:  item.Current.Description,
			Files:        files,
			SkippedFiles: item.Current.SkippedFiles,
			CreatedAt:    item.Current.CreatedAt,
		}
		if err := tx.Create(&version).Error; err != nil {
			return err
		}
		if err := insertImages(tx, item.ID, item.Images); err != nil {
			return err
		}
		return insertTags(tx, item.ID, item.Tags)
	})
	if err != nil {
		return unavailable("创建技能", err)
	}
	return nil
}

// AppendVersion 实现 skill.Store：落新版本并切指针、更新来源里的提交，一个整体。
func (s *Store) AppendVersion(ctx context.Context, skillID string, version skill.Version, source skill.Source) error {
	files, err := encodeFiles(version.Files)
	if err != nil {
		return err
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		record := database.SkillVersionRecord{
			ID:           version.ID,
			SkillID:      skillID,
			Commit:       version.Commit,
			Name:         version.Name,
			Description:  version.Description,
			Files:        files,
			SkippedFiles: version.SkippedFiles,
			CreatedAt:    version.CreatedAt,
		}
		if err := tx.Create(&record).Error; err != nil {
			return err
		}
		result := tx.Model(&database.SkillRecord{}).
			Where("id = ?", skillID).
			Updates(map[string]any{
				"current_version_id": version.ID,
				"source_commit":      source.Commit,
				"updated_at":         time.Now(),
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return skill.ErrSkillNotFound
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, skill.ErrSkillNotFound) {
			return err
		}
		return unavailable("追加技能版本", err)
	}
	return nil
}

// SetCurrentVersion 实现 skill.Store。
//
// 归属判定放在同一个事务里：**版本属于这个技能**是一次原子比较与写入，先查再写会
// 多一个"查完之后它被删了"的窗口。
func (s *Store) SetCurrentVersion(ctx context.Context, skillID, versionID string) error {
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var count int64
		if err := tx.Model(&database.SkillVersionRecord{}).
			Where("id = ? AND skill_id = ?", versionID, skillID).
			Count(&count).Error; err != nil {
			return err
		}
		if count == 0 {
			// 先确认技能本身在不在：**技能不存在**与**版本不属于它**是两句不同的
			// 话，虽然都不该暴露"别人的版本存在吗"这个信息。
			var skills int64
			if err := tx.Model(&database.SkillRecord{}).
				Where("id = ?", skillID).Count(&skills).Error; err != nil {
				return err
			}
			if skills == 0 {
				return skill.ErrSkillNotFound
			}
			return skill.ErrVersionNotFound
		}
		result := tx.Model(&database.SkillRecord{}).
			Where("id = ?", skillID).
			Updates(map[string]any{"current_version_id": versionID, "updated_at": time.Now()})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return skill.ErrSkillNotFound
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, skill.ErrSkillNotFound) || errors.Is(err, skill.ErrVersionNotFound) {
			return err
		}
		return unavailable("切换技能版本", err)
	}
	return nil
}

// UpdateMetadata 实现 skill.Store：改说明层，**它不碰内容层任何一项**。
func (s *Store) UpdateMetadata(ctx context.Context, skillID, title, summary string, tags []string) error {
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&database.SkillRecord{}).
			Where("id = ?", skillID).
			Updates(map[string]any{"title": title, "summary": summary, "updated_at": time.Now()})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return skill.ErrSkillNotFound
		}
		// 标签是**整体替换**：先删后插。先插后删会让归一化之后重复的取值在两步
		// 之间撞上主键。
		if err := tx.Where("skill_id = ?", skillID).
			Delete(&database.SkillTagRecord{}).Error; err != nil {
			return err
		}
		return insertTags(tx, skillID, tags)
	})
	if err != nil {
		if errors.Is(err, skill.ErrSkillNotFound) {
			return err
		}
		return unavailable("更新技能说明层", err)
	}
	return nil
}

// PutImage 实现 skill.Store。
//
// **标识已经在图集里时是换图**：只更新对象键与字节数，位置不动。判断与写入在同一个
// 事务里（与 SetCurrentVersion 同一条理由）：先查再写会多一个"查完之后它被删了"的
// 窗口，而那种偏差的表现在界面上是"换了一张图，它自己跳到了末尾"。
//
// 这里刻意不用 `Updates` 的 RowsAffected 判"这一张在不在"：MySQL 在新值与旧值完全
// 相同时返回 0 行，于是"换上一张一模一样的图"会被误判成不存在。
func (s *Store) PutImage(ctx context.Context, skillID string, image skill.Image) error {
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var skills int64
		if err := tx.Model(&database.SkillRecord{}).
			Where("id = ?", skillID).Count(&skills).Error; err != nil {
			return err
		}
		if skills == 0 {
			return skill.ErrSkillNotFound
		}
		var existing database.SkillImageRecord
		err := tx.Where("id = ? AND skill_id = ?", image.ID, skillID).Take(&existing).Error
		switch {
		case err == nil:
			return tx.Model(&database.SkillImageRecord{}).
				Where("id = ?", image.ID).
				Updates(map[string]any{
					"object_key": image.ObjectKey,
					"size_bytes": image.SizeBytes,
				}).Error
		case errors.Is(err, gorm.ErrRecordNotFound):
			// 新增的那一张排在末尾：位置是 `max + 1`，因此**已有的图一张都不动**。
			var next int
			if err := tx.Model(&database.SkillImageRecord{}).
				Where("skill_id = ?", skillID).
				Select("COALESCE(MAX(position), -1) + 1").
				Scan(&next).Error; err != nil {
				return err
			}
			return tx.Create(&database.SkillImageRecord{
				ID:        image.ID,
				SkillID:   skillID,
				Position:  next,
				ObjectKey: image.ObjectKey,
				SizeBytes: image.SizeBytes,
				CreatedAt: time.Now(),
			}).Error
		default:
			return err
		}
	})
	if err != nil {
		if errors.Is(err, skill.ErrSkillNotFound) {
			return err
		}
		return unavailable("写入技能展示图", err)
	}
	return nil
}

// DeleteImage 实现 skill.Store。**其余图的位置不动**：顺序与对象地址无关。
func (s *Store) DeleteImage(ctx context.Context, skillID, imageID string) error {
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Where("id = ? AND skill_id = ?", imageID, skillID).
			Delete(&database.SkillImageRecord{})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected > 0 {
			return nil
		}
		// 先确认技能本身在不在：**技能不存在**与**这一张不在它的图集里**是两句
		// 不同的话，虽然都不该暴露"别人的图存在吗"这个信息。
		var skills int64
		if err := tx.Model(&database.SkillRecord{}).
			Where("id = ?", skillID).Count(&skills).Error; err != nil {
			return err
		}
		if skills == 0 {
			return skill.ErrSkillNotFound
		}
		return skill.ErrImageNotFound
	})
	if err != nil {
		if errors.Is(err, skill.ErrSkillNotFound) || errors.Is(err, skill.ErrImageNotFound) {
			return err
		}
		return unavailable("删除技能展示图", err)
	}
	return nil
}

// ReorderImages 实现 skill.Store。
//
// 排列判定与写入在同一个事务里：给出的标识必须是这个技能当前图集的**一个排列**，
// 否则整个拒绝、顺序原样（不做"部分重排"）。
func (s *Store) ReorderImages(ctx context.Context, skillID string, imageIDs []string) error {
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var skills int64
		if err := tx.Model(&database.SkillRecord{}).
			Where("id = ?", skillID).Count(&skills).Error; err != nil {
			return err
		}
		if skills == 0 {
			return skill.ErrSkillNotFound
		}
		var records []database.SkillImageRecord
		if err := tx.Where("skill_id = ?", skillID).
			Order("position ASC, id ASC").Find(&records).Error; err != nil {
			return err
		}
		// 判据与内存实现**共用同一处**：各写一遍的表现是"内存实现通过、SQL 实现
		// 拒绝"这类只在一边出现的偏差。
		if !skill.IsImagePermutation(toImages(records), imageIDs) {
			return skill.ErrImageOrderMismatch
		}
		for position, imageID := range imageIDs {
			if err := tx.Model(&database.SkillImageRecord{}).
				Where("id = ? AND skill_id = ?", imageID, skillID).
				Update("position", position).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, skill.ErrSkillNotFound) || errors.Is(err, skill.ErrImageOrderMismatch) {
			return err
		}
		return unavailable("重排技能展示图", err)
	}
	return nil
}

// DeleteSkill 实现 skill.Store。
//
// **它不删桶上的字节**：内容对象按摘要全局共享，同一份字节可能正被别处引用
// （见 skill.ContentObjectKey）。
func (s *Store) DeleteSkill(ctx context.Context, skillID string) error {
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, model := range []any{
			&database.SkillVersionRecord{}, &database.SkillTagRecord{},
			&database.SkillImageRecord{},
			&database.SkillFavoriteRecord{}, &database.SkillUsageDailyRecord{},
		} {
			if err := tx.Where("skill_id = ?", skillID).Delete(model).Error; err != nil {
				return err
			}
		}
		result := tx.Where("id = ?", skillID).Delete(&database.SkillRecord{})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return skill.ErrSkillNotFound
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, skill.ErrSkillNotFound) {
			return err
		}
		return unavailable("删除技能", err)
	}
	return nil
}

// SetFavorite 实现 skill.Store。两个方向都是幂等的。
func (s *Store) SetFavorite(ctx context.Context, skillID, subjectID string, favorited bool) error {
	if _, err := s.skillRecord(ctx, skillID); err != nil {
		return err
	}
	if !favorited {
		if err := s.db.WithContext(ctx).
			Where("skill_id = ? AND subject_id = ?", skillID, subjectID).
			Delete(&database.SkillFavoriteRecord{}).Error; err != nil {
			return unavailable("取消收藏", err)
		}
		return nil
	}
	record := database.SkillFavoriteRecord{
		SkillID: skillID, SubjectID: subjectID, CreatedAt: time.Now(),
	}
	// 已收藏时什么都不做：它是幂等的，而"再收藏一次改一下时间"会让"什么时候
	// 收的"变成一个会自己变的字段。
	if err := s.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).
		Create(&record).Error; err != nil {
		return unavailable("收藏技能", err)
	}
	return nil
}

// FavoriteIDs 实现 skill.Store。
func (s *Store) FavoriteIDs(ctx context.Context, subjectID string) (map[string]bool, error) {
	var records []database.SkillFavoriteRecord
	if err := s.db.WithContext(ctx).
		Where("subject_id = ?", subjectID).
		Find(&records).Error; err != nil {
		return nil, unavailable("读取收藏", err)
	}
	result := make(map[string]bool, len(records))
	for _, record := range records {
		result[record.SkillID] = true
	}
	return result, nil
}

// RecordUse 实现 skill.Store。
func (s *Store) RecordUse(ctx context.Context, skillID, subjectID string, at time.Time) error {
	if _, err := s.skillRecord(ctx, skillID); err != nil {
		return err
	}
	record := database.SkillUsageDailyRecord{
		SkillID:   skillID,
		SubjectID: subjectID,
		Day:       skill.UsageDay(at),
		UsedAt:    at,
	}
	// 冲突时取两者里更晚的时刻：两次取用的请求可能并发，而"最近一次使用"不该
	// 因为一条晚到的写而倒退。
	err := s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "skill_id"}, {Name: "subject_id"}, {Name: "day"}},
		DoUpdates: clause.Assignments(map[string]any{
			"used_at": gorm.Expr("MAX(used_at, ?)", at),
		}),
	}).Create(&record).Error
	if err != nil {
		return unavailable("记录技能使用", err)
	}
	return nil
}

// Usage 实现 skill.Store。
func (s *Store) Usage(ctx context.Context, skillIDs []string, since time.Time) (map[string]skill.Usage, error) {
	if len(skillIDs) == 0 {
		return map[string]skill.Usage{}, nil
	}
	var records []database.SkillUsageDailyRecord
	if err := s.db.WithContext(ctx).
		Where("skill_id IN ? AND day >= ?", skillIDs, skill.UsageDay(since)).
		Find(&records).Error; err != nil {
		return nil, unavailable("读取技能使用量", err)
	}
	// 聚合在内存里做：行数有天然上界（技能数 × 主体数 × 天数），而"最近 30 天
	// 有多少人在用"本来就要去重主体，交给 SQL 也只是把同一件事换个地方写。
	result := map[string]skill.Usage{}
	users := map[string]map[string]bool{}
	for _, record := range records {
		current := result[record.SkillID]
		current.UseDays++
		if record.UsedAt.After(current.LastUsedAt) {
			current.LastUsedAt = record.UsedAt
		}
		result[record.SkillID] = current
		if users[record.SkillID] == nil {
			users[record.SkillID] = map[string]bool{}
		}
		users[record.SkillID][record.SubjectID] = true
	}
	for skillID, current := range result {
		current.Users = len(users[skillID])
		result[skillID] = current
	}
	return result, nil
}

// DeleteUsageBefore 实现 skill.Store。
func (s *Store) DeleteUsageBefore(ctx context.Context, before time.Time) error {
	if err := s.db.WithContext(ctx).
		Where("day < ?", skill.UsageDay(before)).
		Delete(&database.SkillUsageDailyRecord{}).Error; err != nil {
		return unavailable("回收技能使用记录", err)
	}
	return nil
}

// skillRecord 读一个技能行。
func (s *Store) skillRecord(ctx context.Context, skillID string) (database.SkillRecord, error) {
	var record database.SkillRecord
	err := s.db.WithContext(ctx).Where("id = ?", skillID).Take(&record).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return database.SkillRecord{}, skill.ErrSkillNotFound
	}
	if err != nil {
		return database.SkillRecord{}, unavailable("读取技能", err)
	}
	return record, nil
}

// versionRecord 读一个技能下的某一份版本。
func (s *Store) versionRecord(ctx context.Context, skillID, versionID string) (database.SkillVersionRecord, error) {
	var record database.SkillVersionRecord
	err := s.db.WithContext(ctx).
		Where("id = ? AND skill_id = ?", versionID, skillID).
		Take(&record).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return database.SkillVersionRecord{}, unavailable("读取当前版本",
			fmt.Errorf("技能 %s 的版本 %s 不存在", skillID, versionID))
	}
	if err != nil {
		return database.SkillVersionRecord{}, unavailable("读取技能版本", err)
	}
	return record, nil
}

// versionsByID 一次读出多份版本。
func (s *Store) versionsByID(ctx context.Context, versionIDs []string) (map[string]database.SkillVersionRecord, error) {
	var records []database.SkillVersionRecord
	if err := s.db.WithContext(ctx).Where("id IN ?", versionIDs).Find(&records).Error; err != nil {
		return nil, unavailable("读取技能版本", err)
	}
	result := make(map[string]database.SkillVersionRecord, len(records))
	for _, record := range records {
		result[record.ID] = record
	}
	return result, nil
}

// tagsBySkill 一次读出多个技能的标签。
func (s *Store) tagsBySkill(ctx context.Context, skillIDs []string) (map[string][]string, error) {
	var records []database.SkillTagRecord
	// 按标签排序：读取侧不做二次排序，两种实现给出的顺序必须逐字一致。
	if err := s.db.WithContext(ctx).
		Where("skill_id IN ?", skillIDs).
		Order("tag ASC").
		Find(&records).Error; err != nil {
		return nil, unavailable("读取技能标签", err)
	}
	result := map[string][]string{}
	for _, record := range records {
		result[record.SkillID] = append(result[record.SkillID], record.Tag)
	}
	return result, nil
}

// insertTags 写入一组标签行。
func insertTags(tx *gorm.DB, skillID string, tags []string) error {
	if len(tags) == 0 {
		return nil
	}
	records := make([]database.SkillTagRecord, 0, len(tags))
	for _, tag := range tags {
		records = append(records, database.SkillTagRecord{SkillID: skillID, Tag: tag})
	}
	return tx.Create(&records).Error
}

// imagesBySkill 一次读出多个技能的展示图集，**按图集顺序**。
func (s *Store) imagesBySkill(ctx context.Context, skillIDs []string) (map[string][]skill.Image, error) {
	var records []database.SkillImageRecord
	// 排序键里带上标识：位置只保证"后写入的图更大"，同一个位置上的两行（并发追加）
	// 也要有确定的先后，否则同一份数据在不同一次查询里会给出不同的顺序。
	if err := s.db.WithContext(ctx).
		Where("skill_id IN ?", skillIDs).
		Order("position ASC, id ASC").
		Find(&records).Error; err != nil {
		return nil, unavailable("读取技能展示图", err)
	}
	result := map[string][]skill.Image{}
	for _, record := range records {
		result[record.SkillID] = append(result[record.SkillID], toImage(record))
	}
	return result, nil
}

// insertImages 写入一个技能初始的展示图集，**位置即给定顺序**。
func insertImages(tx *gorm.DB, skillID string, images []skill.Image) error {
	if len(images) == 0 {
		return nil
	}
	now := time.Now()
	records := make([]database.SkillImageRecord, 0, len(images))
	for position, image := range images {
		records = append(records, database.SkillImageRecord{
			ID:        image.ID,
			SkillID:   skillID,
			Position:  position,
			ObjectKey: image.ObjectKey,
			SizeBytes: image.SizeBytes,
			CreatedAt: now,
		})
	}
	return tx.Create(&records).Error
}

// encodeFiles 把一份文件清单序列化成列里的取值（唯一入口）。
//
// **清单的承载方式只在这一处**：一列 JSON 而不是一组子行，因为它从来是整份读写的
// ——校验、取用与界面上的文件列表都要整棵树，没有"按某一条路径反查版本"的查询。
// 整份读写在一列上是一次赋值，在一组子行上却要先删后插，而"删到一半"会留下一个
// 库里有、内容不全的版本。列的形态本身是实现细节，spec 只要求整份读写。
func encodeFiles(files []skill.File) ([]database.SkillFileRecord, error) {
	if len(files) == 0 {
		return nil, nil
	}
	records := make([]database.SkillFileRecord, 0, len(files))
	for _, file := range files {
		records = append(records, database.SkillFileRecord{
			Path: file.Path, Digest: file.Digest, SizeBytes: file.SizeBytes,
		})
	}
	return records, nil
}

// toSkill 把记录折成领域类型。
func toSkill(record database.SkillRecord, version database.SkillVersionRecord, tags []string, images []skill.Image) skill.Skill {
	return skill.Skill{
		ID:      record.ID,
		Title:   record.Title,
		Summary: record.Summary,
		Images:  images,
		Tags:    tags,
		Source: skill.Source{
			Owner:   record.SourceOwner,
			Name:    record.SourceName,
			Ref:     record.SourceRef,
			SubPath: record.SourceSubPath,
			Commit:  record.SourceCommit,
		},
		Current: toVersion(version),
	}
}

// toImage 把图集记录折成领域类型。
func toImage(record database.SkillImageRecord) skill.Image {
	return skill.Image{
		ID:        record.ID,
		ObjectKey: record.ObjectKey,
		SizeBytes: record.SizeBytes,
	}
}

// toImages 折一整组图集记录。
func toImages(records []database.SkillImageRecord) []skill.Image {
	images := make([]skill.Image, 0, len(records))
	for _, record := range records {
		images = append(images, toImage(record))
	}
	return images
}

// toVersion 把版本记录折成领域类型。
func toVersion(record database.SkillVersionRecord) skill.Version {
	files := make([]skill.File, 0, len(record.Files))
	for _, file := range record.Files {
		files = append(files, skill.File{
			Path: file.Path, Digest: file.Digest, SizeBytes: file.SizeBytes,
		})
	}
	return skill.Version{
		ID:           record.ID,
		SkillID:      record.SkillID,
		Commit:       record.Commit,
		Name:         record.Name,
		Description:  record.Description,
		Files:        files,
		SkippedFiles: record.SkippedFiles,
		CreatedAt:    record.CreatedAt,
	}
}
