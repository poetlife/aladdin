// Package gormstore 是 galaxy.MutableStore 的关系库实现。
//
// 它持有的是连接而不是配置：方言解析、连接串归一、连接池取值都在
// internal/database 完成，迁移由 internal/database/migrate 推进，本包只消费
// 一个已经准备好表的 *gorm.DB。
//
// 五张表由四个文件分担：本文件是存储的入口与记录到领域类型的转换，project.go
// 管工程、草稿与版本，asset.go 管资产，publication.go 管发布记录。
package gormstore

import (
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
// internal/database/migrate/migration_0006_galaxy_tables.go），本构造函数只
// 负责把记录类型与领域类型对上。
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

// toProject 把记录翻译成领域类型。
//
// 转换只在这一处：记录里加一列不会悄悄改变上层看到的东西，除非这里也跟着改
// ——而那时改动是显式的。
func toProject(rec database.GalaxyProjectRecord) galaxy.Project {
	return galaxy.Project{
		ID:                   rec.ID,
		OwnerSubjectID:       rec.OwnerSubjectID,
		Name:                 rec.Name,
		Description:          rec.Description,
		CurrentPublicationID: rec.CurrentPublicationID,
		CreatedAt:            rec.CreatedAt,
		UpdatedAt:            rec.UpdatedAt,
	}
}

func toDraft(rec database.GalaxyDraftRecord) galaxy.Draft {
	return galaxy.Draft{
		ProjectID: rec.ProjectID,
		Content:   galaxy.Document(rec.Content),
		UpdatedAt: rec.UpdatedAt,
	}
}

func toVersion(rec database.GalaxyVersionRecord) galaxy.Version {
	return galaxy.Version{
		ID:        rec.ID,
		ProjectID: rec.ProjectID,
		Seq:       rec.Seq,
		Content:   galaxy.Document(rec.Content),
		SavedAt:   rec.SavedAt,
	}
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
	}
}

func toPublication(rec database.GalaxyPublicationRecord) galaxy.Publication {
	return galaxy.Publication{
		ID:                   rec.ID,
		ProjectID:            rec.ProjectID,
		VersionID:            rec.VersionID,
		Content:              galaxy.Document(rec.Content),
		PublishedBySubjectID: rec.PublishedBySubjectID,
		PublishedAt:          rec.PublishedAt,
	}
}

var _ galaxy.MutableStore = (*Store)(nil)
