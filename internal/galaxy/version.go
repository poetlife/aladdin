package galaxy

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"go.uber.org/zap"
)

var (
	// ErrVersionPublished 表示这个版本正被当前发布指向，因此不能删。
	//
	// 删掉它会让发布地址指向一个不存在的版本（对外表现是"站点突然没了"，
	// 而用户刚才看到的是"已发布"）。
	ErrVersionPublished = errors.New("版本正在被发布")
)

// SaveVersion 把草稿的当前清单保存成一个不可变版本。
//
// 三条不可变性约定都由本函数的形状保证，不靠约定俗成：
//
//   - **版本的清单不可修改**：没有"编辑某个版本"这个动作——要改就改草稿、
//     再保存一个新版本。因此本包不存在 UpdateVersion。
//   - **版本不因别的记录变化而变化**：写入的是清单的一份拷贝，此后改工程
//     名称、改简介、删别的版本都不改变它读回的内容。
//   - **版本不可覆盖**：连续保存两次相同清单产生两个版本，而不是"检测到
//     重复就不新增"。两份看起来一样的清单对用户是两次不同的保存动作。
//
// 这一次冻结**不搬运任何字节**：字节本来就是按内容摘要寻址的不可变对象，由
// 多个版本共享。
func (s *Service) SaveVersion(ctx context.Context, subjectID, projectID string) (Version, error) {
	project, err := OwnedProject(ctx, s.store, projectID, subjectID)
	if err != nil {
		return Version{}, err
	}
	// 草稿行不存在时按空清单处理：这与"推送过一次空清单"在编辑上等价，
	// 而把"还没推过"表现成一个错误会让新工程上的第一次保存先失败一次。
	draft, err := s.store.GetDraft(ctx, projectID)
	if errors.Is(err, ErrDraftNotFound) {
		draft = Draft{ProjectID: projectID}
	} else if err != nil {
		return Version{}, err
	}
	// 一份没有入口文件的清单发出去是一个打不开的站点，因此这里挡住它——
	// 与 PushDraft 是同一处判断。
	if err := ValidateManifestForForm(project.Form, draft.Manifest); err != nil {
		return Version{}, err
	}
	versionID, err := newVersionID()
	if err != nil {
		return Version{}, err
	}
	saved, err := s.store.CreateVersion(ctx, Version{
		ID:                 versionID,
		ProjectID:          projectID,
		Manifest:           draft.Manifest,
		RenderRulesVersion: RenderRulesVersion,
		SavedAt:            s.now(),
	})
	if err != nil {
		return Version{}, err
	}
	s.publish(projectID)
	if s.logger != nil {
		s.logger.Info("已保存版本",
			zap.String("project_id", projectID),
			zap.String("version_id", saved.ID),
			zap.Int64("seq", saved.Seq),
			zap.String("subject_id", subjectID),
			zap.Int("files", len(saved.Manifest)))
	}
	return saved, nil
}

// ListVersions 列出工程的版本，按序号升序。清单随行返回，但不带读取地址。
func (s *Service) ListVersions(ctx context.Context, subjectID, projectID string) ([]Version, error) {
	if _, err := OwnedProject(ctx, s.store, projectID, subjectID); err != nil {
		return nil, err
	}
	return s.store.ListVersions(ctx, projectID)
}

// GetVersion 读取一个版本，并给每一条条目附上短时读取地址。
func (s *Service) GetVersion(ctx context.Context, subjectID, projectID, versionID string) (Version, []EntryView, error) {
	if _, err := OwnedProject(ctx, s.store, projectID, subjectID); err != nil {
		return Version{}, nil, err
	}
	version, err := s.store.GetVersion(ctx, projectID, versionID)
	if err != nil {
		return Version{}, nil, err
	}
	return version, s.attachURLs(ctx, projectID, version.Manifest), nil
}

// DeleteVersion 删除一个版本。
//
// **被当前发布指向的版本不可删**：那会让发布地址指向一个不存在的版本。删别
// 的版本时，草稿与其它版本不受影响，序号也不重排（因此会留下空洞）。
func (s *Service) DeleteVersion(ctx context.Context, subjectID, projectID, versionID string) error {
	project, err := OwnedProject(ctx, s.store, projectID, subjectID)
	if err != nil {
		return err
	}
	if _, err := s.store.GetVersion(ctx, projectID, versionID); err != nil {
		return err
	}
	published, err := s.versionIsPublished(ctx, project, versionID)
	if err != nil {
		return err
	}
	if published {
		return ErrVersionPublished
	}
	if err := s.store.DeleteVersion(ctx, projectID, versionID); err != nil {
		return err
	}
	s.publish(projectID)
	if s.logger != nil {
		s.logger.Info("已删除版本",
			zap.String("project_id", projectID),
			zap.String("version_id", versionID),
			zap.String("subject_id", subjectID))
	}
	return nil
}

// versionIsPublished 判定一个版本是不是当前发布指向的那一个。
//
// 判据是**发布记录里的版本标识**，而不是"重新算一遍发布标识再比较"：指针指向
// 的是记录，记录里写着它发布的是哪个版本，这条链只有一个来源。
func (s *Service) versionIsPublished(ctx context.Context, project Project, versionID string) (bool, error) {
	if project.CurrentPublicationID == "" {
		return false, nil
	}
	publication, err := s.store.GetPublication(ctx, project.CurrentPublicationID)
	if err != nil {
		if errors.Is(err, ErrPublicationNotFound) {
			// 指针指向一条不存在的记录是数据不一致，不把它当成"未发布"。
			return false, fmt.Errorf("%w: 发布指针指向的记录不存在", ErrStoreUnavailable)
		}
		return false, err
	}
	return publication.VersionID == versionID, nil
}

// referencingVersions 返回引用了某个资产的版本，按序号升序。
//
// **它的输入只有版本的文件清单**：一个版本引用了哪些资产由清单里的资产条目
// 直接读出，不解析任何文本——清单与文本会漂移，而漂移的表现是"发布时校验通过
// 但页面上一张图裂开"，或者反过来，把一个不再被引用的资产也搬上公开区。
func (s *Service) referencingVersions(ctx context.Context, projectID, assetID string) ([]Version, error) {
	versions, err := s.store.ListVersions(ctx, projectID)
	if err != nil {
		return nil, err
	}
	var referencing []Version
	for _, version := range versions {
		for _, referenced := range version.Manifest.AssetIDs() {
			if referenced == assetID {
				// 只留展示需要的字段：清单不该被带进错误信息与日志。
				referencing = append(referencing, Version{
					ID:        version.ID,
					ProjectID: version.ProjectID,
					Seq:       version.Seq,
				})
				break
			}
		}
	}
	return referencing, nil
}

// describeVersions 把一串版本描述成一句可操作的提示。
//
// 带上版本的标识：序号是给人看的，标识才是拿去调用接口时要用的那一个。
func describeVersions(versions []Version) string {
	parts := make([]string, 0, len(versions))
	for _, version := range versions {
		parts = append(parts, fmt.Sprintf("#%d(%s)", version.Seq, version.ID))
	}
	return "被版本 " + strings.Join(parts, "、") + " 引用"
}
