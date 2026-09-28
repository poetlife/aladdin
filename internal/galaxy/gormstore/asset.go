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
	return toAsset(rec), nil
}

// ListAssets 实现 galaxy.Store。
func (s *Store) ListAssets(ctx context.Context, projectID string) ([]galaxy.Asset, error) {
	var recs []database.GalaxyAssetRecord
	err := s.db.WithContext(ctx).
		Where("project_id = ?", projectID).
		Order("uploaded_at DESC, id ASC").
		Find(&recs).Error
	if err != nil {
		return nil, unavailable("列出资产", err)
	}
	assets := make([]galaxy.Asset, 0, len(recs))
	for _, rec := range recs {
		assets = append(assets, toAsset(rec))
	}
	return assets, nil
}

// CreateAsset 实现 galaxy.MutableStore。
//
// 资产不可变，因此这里只有"写入"、没有"更新"：换一份字节是新增一条记录并改
// 正文里的引用，不是修改这一行。
func (s *Store) CreateAsset(ctx context.Context, asset galaxy.Asset) error {
	rec := database.GalaxyAssetRecord{
		ID:         asset.ID,
		ProjectID:  asset.ProjectID,
		Digest:     asset.Digest,
		MediaKind:  string(asset.MediaKind),
		MediaType:  asset.MediaType,
		SizeBytes:  asset.SizeBytes,
		Filename:   asset.Filename,
		UploadedAt: asset.UploadedAt,
	}
	if err := s.db.WithContext(ctx).Create(&rec).Error; err != nil {
		return unavailable("写入资产", err)
	}
	return nil
}

// DeleteAsset 实现 galaxy.MutableStore。
//
// 它只删元数据行。私有区的对象由上层删除，而对象删除失败不改变"资产已删除"
// 这一结论——库内的行是权威。
func (s *Store) DeleteAsset(ctx context.Context, projectID, assetID string) error {
	result := s.db.WithContext(ctx).
		Where("id = ? AND project_id = ?", assetID, projectID).
		Delete(&database.GalaxyAssetRecord{})
	if result.Error != nil {
		return unavailable("删除资产", result.Error)
	}
	if result.RowsAffected == 0 {
		return galaxy.ErrAssetNotFound
	}
	return nil
}
