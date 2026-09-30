// Package gormstore 是 telemetry.Store 的关系库实现。
//
// 它持有的是连接而不是配置：方言解析、连接串归一与连接池取值都在
// internal/database 完成，表结构由 internal/database/migrate 推进，
// 本包只消费一个已经准备好表的 *gorm.DB。
package gormstore

import (
	"context"
	"time"

	"gorm.io/gorm"

	"github.com/poetlife/aladdin/internal/database"
	"github.com/poetlife/aladdin/internal/telemetry"
)

// Store 是客户端事件的关系库实现。
type Store struct {
	db *gorm.DB
}

// New 用已打开的连接构造存储。
//
// 它不迁移：表由启动路径上的迁移建好，本构造函数只负责把记录类型与领域类型对上。
func New(db *gorm.DB) *Store {
	return &Store{db: db}
}

// Append 实现 telemetry.Store。
//
// 批量插入一次完成：上报本就是批量的，逐条写会在最忙的那条路径上多出 N 次往返。
// 冲突不做处理——事件没有自然键，两条相同的事件是两条合法记录。
func (s *Store) Append(ctx context.Context, entries []telemetry.Entry) error {
	if len(entries) == 0 {
		return nil
	}
	recs := make([]database.ClientEventRecord, 0, len(entries))
	for _, e := range entries {
		recs = append(recs, toRecord(e))
	}
	if err := s.db.WithContext(ctx).Create(&recs).Error; err != nil {
		return unavailable("写入客户端事件", err)
	}
	return nil
}

// Stats 实现 telemetry.Store。
//
// 区间与内存实现逐字一致：occurred_at 落在 **[from, to)**。区间用绑定参数比较，
// 不用任何 SQL 时间函数——那样两个后端（sqlite / MySQL）才会给出同一结果。
//
// 三个非聚合列都在 GROUP BY 里，因此 MySQL 的 ONLY_FULL_GROUP_BY 也满足；
// 全句是 ANSI SQL，没有方言分支。
func (s *Store) Stats(ctx context.Context, from, to time.Time) ([]telemetry.Stat, error) {
	var rows []statRow
	err := s.db.WithContext(ctx).
		Model(&database.ClientEventRecord{}).
		Select("client, action, result, COUNT(*) AS count").
		Where("occurred_at >= ? AND occurred_at < ?", from, to).
		Group("client, action, result").
		Order("client, action, result").
		Scan(&rows).Error
	if err != nil {
		return nil, unavailable("统计客户端事件", err)
	}

	out := make([]telemetry.Stat, 0, len(rows))
	for _, r := range rows {
		out = append(out, telemetry.Stat{
			Client: r.Client,
			Action: r.Action,
			Result: r.Result,
			Count:  r.Count,
		})
	}
	return out, nil
}

// Recent 实现 telemetry.Store。
//
// 排序是 `occurred_at DESC, id DESC`：同一批写进来的事件共享一个时间戳，只按
// 时间排会让先后变成一个由驱动决定的值。以 id 兜底之后，两组实现给出同一顺序，
// 契约测试才能逐字比对。
//
// 区间与 Stats 同源（半开、绑定参数），否则页面上的计数与明细会对不上。
func (s *Store) Recent(ctx context.Context, from, to time.Time, limit int) ([]telemetry.Entry, error) {
	if limit <= 0 {
		return nil, nil
	}
	var recs []database.ClientEventRecord
	err := s.db.WithContext(ctx).
		Where("occurred_at >= ? AND occurred_at < ?", from, to).
		Order("occurred_at DESC, id DESC").
		Limit(limit).
		Find(&recs).Error
	if err != nil {
		return nil, unavailable("读取最近客户端事件", err)
	}

	out := make([]telemetry.Entry, 0, len(recs))
	for _, rec := range recs {
		out = append(out, toEntry(rec))
	}
	return out, nil
}

// DeleteBefore 实现 telemetry.Store。
//
// 严格早于 cutoff 才删。它只是回收空间，不是正确性的一部分：读取一律带时间
// 条件，一条没被清掉的超窗行不会被任何查询看见。
func (s *Store) DeleteBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	result := s.db.WithContext(ctx).
		Where("occurred_at < ?", cutoff).
		Delete(&database.ClientEventRecord{})
	if result.Error != nil {
		return 0, unavailable("回收过期客户端事件", result.Error)
	}
	return result.RowsAffected, nil
}

// statRow 是分组计数查询的落点。
//
// 字段名必须与 SELECT 里的别名对上（count 别名 → Count），否则 Scan 不会报错，
// 只会留下一个恒为零的计数——那是最坏的一种静默偏差。
type statRow struct {
	Client string
	Action string
	Result string
	Count  int64
}

// toRecord 把领域类型翻译成记录。
func toRecord(e telemetry.Entry) database.ClientEventRecord {
	return database.ClientEventRecord{
		OccurredAt:    e.OccurredAt,
		Client:        e.Client,
		Surface:       e.Surface,
		Action:        e.Action,
		Result:        e.Result,
		DurationMS:    e.DurationMS,
		ClientTraceID: e.TraceID,
		SubjectID:     e.SubjectID,
		Attrs:         e.Attrs,
	}
}

// toEntry 把记录翻译回领域类型。
//
// 转换只在这一处：记录里加一列不会悄悄改变读侧看到的东西，除非这里也跟着改。
func toEntry(rec database.ClientEventRecord) telemetry.Entry {
	return telemetry.Entry{
		Record: telemetry.Record{
			Client:     rec.Client,
			Surface:    rec.Surface,
			Action:     rec.Action,
			Result:     rec.Result,
			DurationMS: rec.DurationMS,
			TraceID:    rec.ClientTraceID,
			SubjectID:  rec.SubjectID,
			Attrs:      rec.Attrs,
		},
		OccurredAt: rec.OccurredAt,
	}
}

var _ telemetry.Store = (*Store)(nil)
