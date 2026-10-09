// Package gormstore 是 galaxy.MutableStore 的关系库实现。
//
// 它持有的是连接而不是配置：方言解析、连接串归一、连接池取值都在
// internal/database 完成，迁移由 internal/database/migrate 推进，本包只消费
// 一个已经准备好表的 *gorm.DB。
//
// 五张表由四个文件分担：本文件是存储的入口、**清单的序列化**与记录到领域类型的
// 转换，project.go 管工程、草稿与版本，asset.go 管资产，publication.go 管发布
// 记录。
package gormstore

import (
	"encoding/json"
	"fmt"

	"gorm.io/gorm"

	"github.com/poetlife/aladdin/internal/database"
	"github.com/poetlife/aladdin/internal/galaxy"
)

// Store 是 galaxy.MutableStore 的关系库实现。
type Store struct {
	db *gorm.DB
}

// New 用已打开的连接构造 galaxy 存储。
//
// 它不迁移：表由启动路径上的迁移建好（见
// internal/database/migrate/migration_0007_galaxy_file_set.go），本构造函数
// 只负责把记录类型与领域类型对上。
func New(db *gorm.DB) *Store {
	return &Store{db: db}
}

// unavailable 把底层错误包装成存储不可用。
//
// 与"工程不存在"分开是关键：后者是正常状态，这个必须让请求快速失败，而不是被
// 当成"你还没有工程"。契约测试会断言两种实现的错误类别一致。
func unavailable(action string, err error) error {
	return fmt.Errorf("%w: %s: %w", galaxy.ErrStoreUnavailable, action, err)
}

// encodeManifest 把一份清单序列化成列里的取值（唯一入口）。
//
// **清单的承载方式只在这一处**：一列 JSON 而不是一组子行，因为保存草稿表达的
// 是完整状态、不是增量——整组读写在一列上是一次赋值，在一组子行上却要先删后插，
// 而"删到一半"会留下一个库里有、内容不全的草稿。列的形态本身是实现细节，spec
// 只要求整组读写。
func encodeManifest(manifest galaxy.Manifest) (string, error) {
	if len(manifest) == 0 {
		return "", nil
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		return "", unavailable("序列化文件清单", err)
	}
	return string(encoded), nil
}

// decodeManifest 把列里的取值还原成清单（唯一入口）。
func decodeManifest(encoded string) (galaxy.Manifest, error) {
	if encoded == "" {
		return nil, nil
	}
	var manifest galaxy.Manifest
	if err := json.Unmarshal([]byte(encoded), &manifest); err != nil {
		return nil, unavailable("解析文件清单", err)
	}
	return manifest, nil
}

// toProject 把记录翻译成领域类型。
//
// 转换只在这一处：记录里加一列不会悄悄改变上层看到的东西，除非这里也跟着改
// ——而那时改动是显式的。**槽不在这里翻译**：它在另一张表上，由调用方合并
// （见 project.go 的 loadSlots）。
func toProject(rec database.GalaxyProjectRecord) galaxy.Project {
	return galaxy.Project{
		ID:             rec.ID,
		OwnerSubjectID: rec.OwnerSubjectID,
		Name:           rec.Name,
		Description:    rec.Description,
		CreatedAt:      rec.CreatedAt,
		UpdatedAt:      rec.UpdatedAt,
	}
}

func toDraft(rec database.GalaxyDraftRecord) (galaxy.Draft, error) {
	manifest, err := decodeManifest(rec.Manifest)
	if err != nil {
		return galaxy.Draft{}, err
	}
	return galaxy.Draft{
		ProjectID: rec.ProjectID,
		Slot:      galaxy.ContentSlot(rec.Slot),
		Manifest:  manifest,
		UpdatedAt: rec.UpdatedAt,
	}, nil
}

func toVersion(rec database.GalaxyVersionRecord) (galaxy.Version, error) {
	manifest, err := decodeManifest(rec.Manifest)
	if err != nil {
		return galaxy.Version{}, err
	}
	return galaxy.Version{
		ID:                 rec.ID,
		ProjectID:          rec.ProjectID,
		Slot:               galaxy.ContentSlot(rec.Slot),
		Seq:                rec.Seq,
		Manifest:           manifest,
		Description:        rec.Description,
		RenderRulesVersion: rec.RenderRulesVersion,
		SavedAt:            rec.SavedAt,
	}, nil
}

func toDraftSnapshot(rec database.GalaxyDraftSnapshotRecord) (galaxy.DraftSnapshot, error) {
	manifest, err := decodeManifest(rec.Manifest)
	if err != nil {
		return galaxy.DraftSnapshot{}, err
	}
	return galaxy.DraftSnapshot{
		ID:                  rec.ID,
		ProjectID:           rec.ProjectID,
		Slot:                galaxy.ContentSlot(rec.Slot),
		Seq:                 rec.Seq,
		Manifest:            manifest,
		Source:              rec.Source,
		ReplacedBySubjectID: rec.ReplacedBySubjectID,
		CreatedAt:           rec.CreatedAt,
	}, nil
}

func toAsset(rec database.GalaxyAssetRecord) galaxy.Asset {
	return galaxy.Asset{
		ID:         rec.ID,
		ProjectID:  rec.ProjectID,
		Digest:     rec.Digest,
		MediaKind:  galaxy.MediaKind(rec.MediaKind),
		MediaType:  rec.MediaType,
		SizeBytes:  rec.SizeBytes,
		Filename:   rec.Filename,
		UploadedAt: rec.UploadedAt,
		Title:      rec.Title,
		Notes:      rec.Notes,
		// Tags 不在这里取：它们在另一张表上，由调用方合并（见 asset.go 的
		// assetTags）。
	}
}

func toPublication(rec database.GalaxyPublicationRecord) (galaxy.Publication, error) {
	manifest, err := decodeManifest(rec.Manifest)
	if err != nil {
		return galaxy.Publication{}, err
	}
	return galaxy.Publication{
		ID:                   rec.ID,
		ProjectID:            rec.ProjectID,
		VersionID:            rec.VersionID,
		Slot:                 galaxy.ContentSlot(rec.Slot),
		Manifest:             manifest,
		PublishedBySubjectID: rec.PublishedBySubjectID,
		PublishedAt:          rec.PublishedAt,
	}, nil
}

var _ galaxy.MutableStore = (*Store)(nil)
