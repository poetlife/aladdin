package gormstore

import (
	"context"
	"errors"

	"gorm.io/gorm"

	"github.com/poetlife/aladdin/internal/database"
	"github.com/poetlife/aladdin/internal/galaxy"
)

// GetAttachment 实现 galaxy.Store。
//
// 查询同时约束工程标识与附件标识：**"附件属于别的工程"与"附件不存在"因此从查询
// 这一层就是同一个结果**（与 GetAsset 同源）。
func (s *Store) GetAttachment(ctx context.Context, projectID, attachmentID string) (galaxy.Attachment, error) {
	var rec database.GalaxyAttachmentRecord
	err := s.db.WithContext(ctx).
		First(&rec, "id = ? AND project_id = ?", attachmentID, projectID).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return galaxy.Attachment{}, galaxy.ErrAttachmentNotFound
		}
		return galaxy.Attachment{}, unavailable("读取附件", err)
	}
	return toAttachment(rec), nil
}

// ListAttachments 实现 galaxy.Store：按上传时间倒序。
func (s *Store) ListAttachments(ctx context.Context, projectID string) ([]galaxy.Attachment, error) {
	var recs []database.GalaxyAttachmentRecord
	err := s.db.WithContext(ctx).
		Where("project_id = ?", projectID).
		Order("uploaded_at DESC, id ASC").
		Find(&recs).Error
	if err != nil {
		return nil, unavailable("列出附件", err)
	}
	attachments := make([]galaxy.Attachment, 0, len(recs))
	for _, rec := range recs {
		attachments = append(attachments, toAttachment(rec))
	}
	return attachments, nil
}

// SumAttachmentBytes 实现 galaxy.Store：配额那一处的判定输入。
//
// 求和落在数据库里而不是把行拉回来再加：一次配额检查要为每一次上传做，而把几百
// 行搬过网络只为了加一个数，是这条路径上最没必要的一笔开销。
func (s *Store) SumAttachmentBytes(ctx context.Context, projectID string) (int64, error) {
	var total int64
	err := s.db.WithContext(ctx).
		Model(&database.GalaxyAttachmentRecord{}).
		Where("project_id = ?", projectID).
		Select("COALESCE(SUM(size_bytes), 0)").
		Row().Scan(&total)
	if err != nil {
		return 0, unavailable("统计附件占用", err)
	}
	return total, nil
}

// CreateAttachment 实现 galaxy.MutableStore。
//
// 附件不可变，因此这里只有"写入"、没有改字节那一半：换一份字节是重新上传一个。
func (s *Store) CreateAttachment(ctx context.Context, attachment galaxy.Attachment) error {
	rec := database.GalaxyAttachmentRecord{
		ID:                  attachment.ID,
		ProjectID:           attachment.ProjectID,
		VersionID:           attachment.VersionID,
		Filename:            attachment.Filename,
		SizeBytes:           attachment.SizeBytes,
		Digest:              attachment.Digest,
		Description:         attachment.Description,
		UploadedBySubjectID: attachment.UploadedBySubjectID,
		UploadedAt:          attachment.UploadedAt,
	}
	if err := s.db.WithContext(ctx).Create(&rec).Error; err != nil {
		return unavailable("写入附件", err)
	}
	return nil
}

// UpdateAttachmentDescription 实现 galaxy.MutableStore。
//
// **它只动说明**：文件名、字节数、摘要与标注的版本逐字不变——已经发出去的下载
// 地址指向的还是同一份字节（与 UpdateAssetMeta 同源）。
func (s *Store) UpdateAttachmentDescription(ctx context.Context, projectID, attachmentID, description string) error {
	result := s.db.WithContext(ctx).
		Model(&database.GalaxyAttachmentRecord{}).
		Where("id = ? AND project_id = ?", attachmentID, projectID).
		Update("description", description)
	if result.Error != nil {
		return unavailable("更新附件说明", result.Error)
	}
	if result.RowsAffected == 0 {
		// 行不存在，或者值本来就相同。后者是合法的空改动，前者不是——因此再确认
		// 一次这一行是否真的在（与 UpdateAssetMeta 同源）。
		var count int64
		if err := s.db.WithContext(ctx).
			Model(&database.GalaxyAttachmentRecord{}).
			Where("id = ? AND project_id = ?", attachmentID, projectID).
			Count(&count).Error; err != nil {
			return unavailable("更新附件说明", err)
		}
		if count == 0 {
			return galaxy.ErrAttachmentNotFound
		}
	}
	return nil
}

// DeleteAttachment 实现 galaxy.MutableStore：只删元数据行。
//
// 私有区的对象由上层删除，而对象删除失败不改变"附件已删除"这一结论——库内的行
// 是权威（与 DeleteAsset 同源）。
func (s *Store) DeleteAttachment(ctx context.Context, projectID, attachmentID string) error {
	result := s.db.WithContext(ctx).
		Where("id = ? AND project_id = ?", attachmentID, projectID).
		Delete(&database.GalaxyAttachmentRecord{})
	if result.Error != nil {
		return unavailable("删除附件", result.Error)
	}
	if result.RowsAffected == 0 {
		return galaxy.ErrAttachmentNotFound
	}
	return nil
}

// clearAttachmentVersions 把指向某个版本的附件标注清空（唯一入口）。
//
// 它与版本的删除**在同一个事务里**（见 DeleteVersion）：清到一半的表现是一条指向
// 不存在版本的悬空标注，而它在界面上是一个打不开的版本号。
func clearAttachmentVersions(tx *gorm.DB, projectID, versionID string) error {
	return tx.Model(&database.GalaxyAttachmentRecord{}).
		Where("project_id = ? AND version_id = ?", projectID, versionID).
		Update("version_id", "").Error
}
