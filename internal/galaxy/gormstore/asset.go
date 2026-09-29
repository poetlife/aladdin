package gormstore

import (
	"context"
	"errors"

	"gorm.io/gorm"

	"github.com/poetlife/aladdin/internal/database"
	"github.com/poetlife/aladdin/internal/galaxy"
)

// GetAsset 实现 galaxy.Store。
//
// 查询同时约束工程标识与资产标识：**"资产属于别的工程"与"资产不存在"因此从
// 查询这一层就是同一个结果**，不需要上层再判一次归属。
func (s *Store) GetAsset(ctx context.Context, projectID, assetID string) (galaxy.Asset, error) {
	var rec database.GalaxyAssetRecord
	err := s.db.WithContext(ctx).
		First(&rec, "id = ? AND project_id = ?", assetID, projectID).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return galaxy.Asset{}, galaxy.ErrAssetNotFound
		}
		return galaxy.Asset{}, unavailable("读取资产", err)
	}
	asset := toAsset(rec)
	tags, err := s.assetTags(ctx, projectID)
	if err != nil {
		return galaxy.Asset{}, err
	}
	asset.Tags = tags[assetID]
	return asset, nil
}

// ListAssets 实现 galaxy.Store。
//
// tags 非空时按**交集**筛选：只有同时带这些标签的资产才列出。做法是先在这一张
// 标签表上按 asset_id 分组、数一数命中了几个，再拿命中数等于筛选数的那些标识
// 回连资产行——不必把标签集合拼成 N 个 EXISTS 子查询。
func (s *Store) ListAssets(ctx context.Context, projectID string, tags []string) ([]galaxy.Asset, error) {
	query := s.db.WithContext(ctx).Where("project_id = ?", projectID)
	if len(tags) > 0 {
		matching := s.db.WithContext(ctx).
			Model(&database.GalaxyAssetTagRecord{}).
			Select("asset_id").
			Where("project_id = ? AND tag IN ?", projectID, tags).
			Group("asset_id").
			Having("COUNT(DISTINCT tag) = ?", len(tags))
		query = query.Where("id IN (?)", matching)
	}
	var recs []database.GalaxyAssetRecord
	if err := query.Order("uploaded_at DESC, id ASC").Find(&recs).Error; err != nil {
		return nil, unavailable("列出资产", err)
	}
	tagsByAsset, err := s.assetTags(ctx, projectID)
	if err != nil {
		return nil, err
	}
	assets := make([]galaxy.Asset, 0, len(recs))
	for _, rec := range recs {
		asset := toAsset(rec)
		asset.Tags = tagsByAsset[asset.ID]
		assets = append(assets, asset)
	}
	return assets, nil
}

// ListProjectTags 实现 galaxy.Store。
//
// 它读的是**整个工程**的标签，与任何筛选无关：筛选界面的候选不该随筛出来的
// 那几个收窄。
func (s *Store) ListProjectTags(ctx context.Context, projectID string) ([]string, error) {
	var tags []string
	err := s.db.WithContext(ctx).
		Model(&database.GalaxyAssetTagRecord{}).
		Where("project_id = ?", projectID).
		Distinct().
		Order("tag ASC").
		Pluck("tag", &tags).Error
	if err != nil {
		return nil, unavailable("列出资产标签", err)
	}
	return tags, nil
}

// CreateAsset 实现 galaxy.MutableStore。
//
// 资产不可变，因此这里只有"写入"、没有改字节那一半：换一份字节是新增一条记录
// 并改正文里的引用，不是修改这一行。**说明层（标题、标签、备注）随这次写入一
// 并落库**，标签与资产行在同一个事务里。
func (s *Store) CreateAsset(ctx context.Context, asset galaxy.Asset) error {
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		rec := database.GalaxyAssetRecord{
			ID:         asset.ID,
			ProjectID:  asset.ProjectID,
			Digest:     asset.Digest,
			MediaKind:  string(asset.MediaKind),
			MediaType:  asset.MediaType,
			SizeBytes:  asset.SizeBytes,
			Filename:   asset.Filename,
			Title:      asset.Title,
			Notes:      asset.Notes,
			UploadedAt: asset.UploadedAt,
		}
		if err := tx.Create(&rec).Error; err != nil {
			return err
		}
		return insertAssetTags(tx, asset.ProjectID, asset.ID, asset.Tags)
	})
	if err != nil {
		return unavailable("写入资产", err)
	}
	return nil
}

// UpdateAssetMeta 实现 galaxy.MutableStore。
//
// **它只动说明层**：内容摘要、媒体类型、类别、字节数与上传时间逐字不变。
// 标签是整体替换（删掉旧的、写入新的），与资产行的更新在同一个事务里——否则
// 一次失败的写入会留下"标题变了、标签没变"这种两边都说不清的中间状态。
func (s *Store) UpdateAssetMeta(ctx context.Context, projectID, assetID, title, notes string, tags []string) error {
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&database.GalaxyAssetRecord{}).
			Where("id = ? AND project_id = ?", assetID, projectID).
			Updates(map[string]any{"title": title, "notes": notes})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			// 行不存在，或者值本来就相同。后者是合法的空改动，前者不是——
			// 因此再确认一次这一行是否真的在，免得把"资产属于别的工程"报成成功。
			var count int64
			if err := tx.Model(&database.GalaxyAssetRecord{}).
				Where("id = ? AND project_id = ?", assetID, projectID).
				Count(&count).Error; err != nil {
				return err
			}
			if count == 0 {
				return galaxy.ErrAssetNotFound
			}
		}
		if err := tx.Where("project_id = ? AND asset_id = ?", projectID, assetID).
			Delete(&database.GalaxyAssetTagRecord{}).Error; err != nil {
			return err
		}
		return insertAssetTags(tx, projectID, assetID, tags)
	})
	if err != nil {
		if errors.Is(err, galaxy.ErrAssetNotFound) {
			return err
		}
		return unavailable("更新资产元数据", err)
	}
	return nil
}

// DeleteAsset 实现 galaxy.MutableStore。
//
// 它删元数据行**连同它的标签**。私有区的对象由上层删除，而对象删除失败不改变
// "资产已删除"这一结论——库内的行是权威。
func (s *Store) DeleteAsset(ctx context.Context, projectID, assetID string) error {
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Where("id = ? AND project_id = ?", assetID, projectID).
			Delete(&database.GalaxyAssetRecord{})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return galaxy.ErrAssetNotFound
		}
		return tx.Where("project_id = ? AND asset_id = ?", projectID, assetID).
			Delete(&database.GalaxyAssetTagRecord{}).Error
	})
	if err != nil {
		if errors.Is(err, galaxy.ErrAssetNotFound) {
			return err
		}
		return unavailable("删除资产", err)
	}
	return nil
}

// assetTags 取回一个工程下每个资产的标签，按资产标识分组。
//
// 一次读整个工程而不是逐个资产查：列表页要的是全部资产，逐个查就是 N+1 次
// 往返。标签行在一张独立的窄表上，一次全取与只取几行差别很小。
func (s *Store) assetTags(ctx context.Context, projectID string) (map[string][]string, error) {
	var recs []database.GalaxyAssetTagRecord
	err := s.db.WithContext(ctx).
		Where("project_id = ?", projectID).
		Order("asset_id ASC, tag ASC").
		Find(&recs).Error
	if err != nil {
		return nil, unavailable("读取资产标签", err)
	}
	byAsset := make(map[string][]string, len(recs))
	for _, rec := range recs {
		byAsset[rec.AssetID] = append(byAsset[rec.AssetID], rec.Tag)
	}
	return byAsset, nil
}

// insertAssetTags 写入一个资产的标签行。
//
// 传进来的标签已经归一化并去重（见 galaxy.NormalizeTags），因此这里不会再
// 撞上唯一约束；真撞上了说明上游漏了一次归一化，让它以错误的形式暴露出来，
// 而不是静默吞掉。
func insertAssetTags(tx *gorm.DB, projectID, assetID string, tags []string) error {
	if len(tags) == 0 {
		return nil
	}
	recs := make([]database.GalaxyAssetTagRecord, 0, len(tags))
	for _, tag := range tags {
		recs = append(recs, database.GalaxyAssetTagRecord{
			ProjectID: projectID,
			AssetID:   assetID,
			Tag:       tag,
		})
	}
	return tx.Create(&recs).Error
}
