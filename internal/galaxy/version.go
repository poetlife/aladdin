package galaxy

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"go.uber.org/zap"
)

var (
	// ErrVersionPublished 表示这个版本正被**它所属槽的**发布指向，因此不能删。
	//
	// 删掉它会让那条发布地址指向一个不存在的版本（对外表现是"站点突然没了"，
	// 而用户刚才看到的是"已发布"）。
	ErrVersionPublished = errors.New("版本正在被发布")

	// ErrVersionDescriptionTooLong 表示版本说明超过长度上限。
	ErrVersionDescriptionTooLong = errors.New("版本说明过长")
)

// VersionDescriptionMaxRunes 是版本说明的长度上限。
//
// 与工程简介、附件说明同一条理由按**字符数**而不是字节数计：用户感知的长度是
// 字数，按字节算会让"一段中文写到十几个字就被拒"成为一条需要解释的规则。
//
// 取值是"一句说明"的尺度：它回答的是"这一版是干什么的"，不是一篇发布公告。
const VersionDescriptionMaxRunes = 280

// NormalizeVersionDescription 归一化版本说明并校验长度（唯一入口）。
//
// 只有一条规则：去掉首尾空白。中间部分的空白与换行原样保留——说明常是一句从
// 提交信息里抄过来的话，把它折成一行是替用户改写内容。
func NormalizeVersionDescription(description string) (string, error) {
	description = strings.TrimSpace(description)
	if len([]rune(description)) > VersionDescriptionMaxRunes {
		return "", fmt.Errorf("%w: 上限 %d 个字", ErrVersionDescriptionTooLong, VersionDescriptionMaxRunes)
	}
	return description, nil
}

// SaveVersion 把**某一个槽**的草稿当前清单保存成一个不可变版本。
//
// 三条不可变性约定都由本函数的形状保证，不靠约定俗成：
//
//   - **版本的清单不可修改**：没有"编辑某个版本"这个动作——要改就改草稿、
//     再保存一个新版本。因此本包不存在 UpdateVersion 之外的内容改动（那个方法
//     只改说明，见下）。
//   - **版本不因别的记录变化而变化**：写入的是清单的一份拷贝，此后改工程
//     名称、改简介、删别的版本都不改变它读回的内容。
//   - **版本不可覆盖**：连续保存两次相同清单产生两个版本，而不是"检测到
//     重复就不新增"。两份看起来一样的清单对用户是两次不同的保存动作。
//
// **fromSnapshotID 非空时存的是那条草稿快照的清单**，而不是当前草稿：它回答的是
// "我想把那次中间态正式记下来"。那条快照引用的资产必须都还在——否则存出来的是一个
// 发布不出去的版本，而"版本不可变"的含义包括"它此后永远可以发布"。
//
// 这一次冻结**不搬运任何字节**：字节本来就是按内容摘要寻址的不可变对象，由
// 多个版本共享。**它也不碰另一个槽**。
func (s *Service) SaveVersion(ctx context.Context, subjectID, projectID string, slot ContentSlot, description, fromSnapshotID string) (Version, error) {
	if _, err := s.ownedProjectSlot(ctx, subjectID, projectID, slot); err != nil {
		return Version{}, err
	}
	description, err := NormalizeVersionDescription(description)
	if err != nil {
		return Version{}, err
	}
	manifest, err := s.versionManifest(ctx, projectID, slot, fromSnapshotID)
	if err != nil {
		return Version{}, err
	}
	// 一份没有入口文件的清单发出去是一个打不开的站点，因此这里挡住它——
	// 与 PushDraft 是同一处判断。
	if err := ValidateManifestForSlot(slot, manifest); err != nil {
		return Version{}, err
	}
	versionID, err := newVersionID()
	if err != nil {
		return Version{}, err
	}
	saved, err := s.store.CreateVersion(ctx, Version{
		ID:                 versionID,
		ProjectID:          projectID,
		Slot:               slot,
		Manifest:           manifest,
		Description:        description,
		RenderRulesVersion: RenderRulesVersion,
		SavedAt:            s.now(),
	})
	if err != nil {
		return Version{}, err
	}
	s.publish(projectID)
	if s.logger != nil {
		// **说明不进日志原文**（见 docs/observability.md）：它是用户内容，与
		// 工程简介、资产备注同级。
		s.logger.Info("已保存版本",
			zap.String("project_id", projectID),
			zap.String("slot", string(slot)),
			zap.String("version_id", saved.ID),
			zap.Int64("seq", saved.Seq),
			zap.String("subject_id", subjectID),
			zap.Int("files", len(saved.Manifest)),
			zap.Bool("from_snapshot", fromSnapshotID != ""))
	}
	return saved, nil
}

// versionManifest 定出这次要存的是哪一份清单（唯一入口）。
//
// 两条来源：当前草稿（缺省），或某一条草稿快照。**快照那条要先检查它引用的资产
// 还在不在**——理由见 requireAssets。
func (s *Service) versionManifest(ctx context.Context, projectID string, slot ContentSlot, fromSnapshotID string) (Manifest, error) {
	if fromSnapshotID == "" {
		// 草稿行不存在时按空清单处理：这与"推送过一次空清单"在编辑上等价，
		// 而把"还没推过"表现成一个错误会让新工程上的第一次保存先失败一次。
		draft, err := s.store.GetDraft(ctx, projectID, slot)
		if errors.Is(err, ErrDraftNotFound) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		return draft.Manifest, nil
	}
	snapshot, err := s.store.GetDraftSnapshot(ctx, projectID, slot, fromSnapshotID)
	if err != nil {
		return nil, err
	}
	if err := s.requireAssets(ctx, projectID, snapshot.Manifest); err != nil {
		return nil, err
	}
	return snapshot.Manifest, nil
}

// UpdateVersionDescription 覆盖一个版本的说明。
//
// **它只动说明那一层**：清单、序号、保存时间与渲染规则版本在改动前后逐字不变，
// 因此已经发出去的页面不受影响，产物里也没有说明这一项。这与资产的
// UpdateAsset（只改说明层）是同一条取舍：不可变说的是**内容**，而"这一版是干什么
// 的"是给人看的元数据（见 docs/design/galaxy/project-versioning.md）。
//
// 请求表达**期望的完整状态**：空串清空说明。
func (s *Service) UpdateVersionDescription(ctx context.Context, subjectID, projectID string, slot ContentSlot, versionID, description string) (Version, error) {
	if _, err := s.ownedProjectSlot(ctx, subjectID, projectID, slot); err != nil {
		return Version{}, err
	}
	description, err := NormalizeVersionDescription(description)
	if err != nil {
		return Version{}, err
	}
	if err := s.store.UpdateVersionDescription(ctx, projectID, slot, versionID, description); err != nil {
		return Version{}, err
	}
	if s.logger != nil {
		// **说明不进日志原文**。
		s.logger.Info("已更新版本说明",
			zap.String("project_id", projectID),
			zap.String("slot", string(slot)),
			zap.String("version_id", versionID),
			zap.String("subject_id", subjectID))
	}
	return s.store.GetVersion(ctx, projectID, slot, versionID)
}

// ListVersions 列出**某一个槽**的版本，按序号升序。清单随行返回，但不带读取地址。
func (s *Service) ListVersions(ctx context.Context, subjectID, projectID string, slot ContentSlot) ([]Version, error) {
	if _, err := s.ownedProjectSlot(ctx, subjectID, projectID, slot); err != nil {
		return nil, err
	}
	return s.store.ListVersions(ctx, projectID, slot)
}

// GetVersion 读取一个版本，并给每一条条目附上短时读取地址。
func (s *Service) GetVersion(ctx context.Context, subjectID, projectID string, slot ContentSlot, versionID string) (Version, []EntryView, error) {
	if _, err := s.ownedProjectSlot(ctx, subjectID, projectID, slot); err != nil {
		return Version{}, nil, err
	}
	version, err := s.store.GetVersion(ctx, projectID, slot, versionID)
	if err != nil {
		return Version{}, nil, err
	}
	return version, s.attachURLs(ctx, projectID, version.Manifest), nil
}

// DeleteVersion 删除**某一个槽**的一个版本。
//
// **被它所属槽的发布指向的版本不可删**：那会让那条发布地址指向一个不存在的
// 版本。删别的版本时，草稿与其它版本不受影响，序号也不重排（因此会留下空洞）。
// **另一个槽的发布指针不影响这个判定**。
func (s *Service) DeleteVersion(ctx context.Context, subjectID, projectID string, slot ContentSlot, versionID string) error {
	project, err := s.ownedProjectSlot(ctx, subjectID, projectID, slot)
	if err != nil {
		return err
	}
	if _, err := s.store.GetVersion(ctx, projectID, slot, versionID); err != nil {
		return err
	}
	published, err := s.versionIsPublished(ctx, project, slot, versionID)
	if err != nil {
		return err
	}
	if published {
		return ErrVersionPublished
	}
	if err := s.store.DeleteVersion(ctx, projectID, slot, versionID); err != nil {
		return err
	}
	s.publish(projectID)
	if s.logger != nil {
		s.logger.Info("已删除版本",
			zap.String("project_id", projectID),
			zap.String("slot", string(slot)),
			zap.String("version_id", versionID),
			zap.String("subject_id", subjectID))
	}
	return nil
}

// versionIsPublished 判定**某一个槽**的版本是不是该槽当前发布指向的那一个。
//
// 判据是**发布记录里的版本标识**，而不是"重新算一遍发布标识再比较"：指针指向
// 的是记录，记录里写着它发布的是哪个版本，这条链只有一个来源。**另一个槽的
// 指针不参与这个判定**。
func (s *Service) versionIsPublished(ctx context.Context, project Project, slot ContentSlot, versionID string) (bool, error) {
	enabled, ok := project.FindSlot(slot)
	if !ok || enabled.CurrentPublicationID == "" {
		return false, nil
	}
	publication, err := s.store.GetPublication(ctx, enabled.CurrentPublicationID)
	if err != nil {
		if errors.Is(err, ErrPublicationNotFound) {
			// 指针指向一条不存在的记录是数据不一致，不把它当成"未发布"。
			return false, fmt.Errorf("%w: 发布指针指向的记录不存在", ErrStoreUnavailable)
		}
		return false, err
	}
	return publication.VersionID == versionID, nil
}

// referencingVersions 返回引用了某个资产的版本，按（槽，序号）升序。
//
// **它的输入只有版本的文件清单**：一个版本引用了哪些资产由清单里的资产条目
// 直接读出，不解析任何文本——清单与文本会漂移，而漂移的表现是"发布时校验通过
// 但页面上一张图裂开"，或者反过来，把一个不再被引用的资产也搬上公开区。
//
// **资产是工程级的，因此两个槽都要查**：一个资产可能只被站点槽引用、只被文档槽
// 引用，或两个槽都引用它。漏查一个槽的表现是"删掉了还在用的资产"，而那要到
// 发布时才会被发现。
func (s *Service) referencingVersions(ctx context.Context, projectID, assetID string) ([]Version, error) {
	var referencing []Version
	for _, slot := range ContentSlots() {
		versions, err := s.store.ListVersions(ctx, projectID, slot)
		if err != nil {
			return nil, err
		}
		for _, version := range versions {
			for _, referenced := range version.Manifest.AssetIDs() {
				if referenced == assetID {
					// 只留展示需要的字段：清单不该被带进错误信息与日志。
					referencing = append(referencing, Version{
						ID:        version.ID,
						ProjectID: version.ProjectID,
						Slot:      version.Slot,
						Seq:       version.Seq,
					})
					break
				}
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
