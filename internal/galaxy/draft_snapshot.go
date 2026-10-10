package galaxy

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.uber.org/zap"

	"github.com/poetlife/aladdin/internal/idgen"
)

const (
	// DraftSnapshotKeep 是每个内容槽最多保留的草稿快照条数。
	//
	// 它与 DraftSnapshotRetentionDays 是**两个都要**的上限：只按天数留，一次密集
	// 的构建循环能在一个下午里堆出几百条；只按条数留，一个半年才动一次的工程会
	// 把半年前的中间态一直留着。
	DraftSnapshotKeep = 50
	// DraftSnapshotRetentionDays 是草稿快照的保留天数。
	DraftSnapshotRetentionDays = 14

	// draftSnapshotIDPrefix 是草稿快照标识的前缀。分配即冻结、永不复用——与
	// 版本、资产、附件标识同一条约定。
	//
	// 它与版本的前缀（`ver_`）**刻意不同**：两种东西都能按标识取用，而"拿一条
	// 快照的标识去发布"必须失败——标识的段位让它一眼看得出拿错了哪一种。
	draftSnapshotIDPrefix = "snp_"
)

// ErrDraftSnapshotNotFound 表示这个工程这个槽下没有这条草稿快照。
//
// 它与"这条快照刚刚过期被清掉了"给同一个结论：快照的生命周期本来就有上限，
// 对调用方来说"它已经不在了"与"它从来没有过"要做的是同一件事（重新看历史）。
var ErrDraftSnapshotNotFound = errors.New("草稿快照不存在")

// ErrDraftSnapshotUnusable 表示这条快照引用了已经不存在的资产，因此不能恢复、
// 也不能升级成版本。
//
// 它与"快照不存在"是两件事：前者是一条真实存在、只是没法用的历史，用户能看到的
// 是"这一份里有个素材已经删了"，而不是"这条历史没了"。
var ErrDraftSnapshotUnusable = errors.New("这条快照引用的资产已经不在资产库里")

// DraftSnapshot 是**被替换掉的那份**草稿清单。
//
// 它不是版本：没有序号、不能发布、会过期（见 docs/design/galaxy/project-versioning.md）。
// 它存在的理由是"只推草稿不存版本"这条最常见的误用——那种用法下，中间过程在改动
// 发生的那一刻就没有任何记录了，而它们本可以不丢。
type DraftSnapshot struct {
	// ID 由 aladdin 分配。
	ID string
	// ProjectID 与 Slot 是它所属的（工程，槽）。
	ProjectID string
	Slot      ContentSlot
	// Seq 是在槽内递增的序号，**仅用于排序**（存储分配，与版本表同一条）。
	//
	// **它存在是因为时间不够用**：两次替换可能落在同一个时间刻上（库的时间列精度
	// 有限），那时"哪一条是最近的"会变成一个由标识的随机性决定的答案。序号在写入
	// 它的那个事务里分配，因此"最近的那一条"永远是确定的。
	Seq int64
	// Manifest 是被替换掉的那份清单。**字节不搬运**：它们本来就是按内容摘要寻址
	// 的不可变对象（与版本同一条）。
	Manifest Manifest
	// Source 是替换它的那一端（`web` / `cli`）。没有带上报端标识时为空。
	//
	// **取值不在本包判定**：服务端从上报端标识那一个入口读出（已受白名单约束，
	// 见 observability.ClientFromHeader），本包只存不判——两处各判一份的表现是
	// "某一端在某条路上被记成空"。
	Source string
	// ReplacedBySubjectID 是执行那次替换的主体。留痕用。
	ReplacedBySubjectID string
	// CreatedAt 是它被替换掉的时刻。
	CreatedAt time.Time
}

// DraftSnapshotRetention 是一次草稿替换要执行的保留策略（唯一入口）。
//
// 它由 Service 按常量与"现在"算出来交给存储，而不是让存储自己取当前时间：判断
// 留在上层、存储只读写事实（与预览凭证的清理同一条），这条清理因此可测。
type DraftSnapshotRetention struct {
	// Keep 是每个槽最多保留的条数。
	Keep int
	// NotBefore 早于它的快照被清掉。
	NotBefore time.Time
}

// draftSnapshotRetention 返回当前这一刻的保留策略。
func (s *Service) draftSnapshotRetention() DraftSnapshotRetention {
	return DraftSnapshotRetention{
		Keep:      DraftSnapshotKeep,
		NotBefore: s.now().AddDate(0, 0, -DraftSnapshotRetentionDays),
	}
}

// newDraftSnapshotID 分配一条草稿快照的标识（唯一入口）。
func newDraftSnapshotID() (string, error) {
	return idgen.New(draftSnapshotIDPrefix)
}

// recordReplacedDraft 决定这次草稿替换要不要留一条快照，并给出那一条。
//
// **相同清单不留。** 连续多次推同一份清单只留一条：快照是服务端顺手记的，把两次
// 相同的结果记两遍只会让历史里全是同一份东西。这与"版本不去重"不冲突——版本的
// 两次保存是用户**显式**的两次动作（见 docs/design/galaxy/project-versioning.md）。
//
// previous 为零值（这个槽还没有草稿行）时不留：没有"被替换掉的东西"。
// 返回值的 ID 为空表示这次不留快照，存储据此跳过写入。
func (s *Service) recordReplacedDraft(subjectID, source string, previous Draft, next Manifest, exists bool) (DraftSnapshot, error) {
	if !exists || sameManifest(previous.Manifest, next) {
		return DraftSnapshot{}, nil
	}
	id, err := newDraftSnapshotID()
	if err != nil {
		return DraftSnapshot{}, err
	}
	return DraftSnapshot{
		ID:                  id,
		ProjectID:           previous.ProjectID,
		Slot:                previous.Slot,
		Manifest:            previous.Manifest,
		Source:              source,
		ReplacedBySubjectID: subjectID,
		CreatedAt:           s.now(),
	}, nil
}

// sameManifest 判定两份清单是不是同一份。
//
// 清单是**有序**的（写入时按路径排好，见 NormalizeManifest），因此逐条比较即可，
// 不需要再排一次序——两处各排一次的表现是"同一份清单有时算相同、有时不算"。
func sameManifest(left, right Manifest) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

// DraftHistory 读回某个槽的草稿历史，**最近的在前**。
//
// 它是只读的：历史由替换草稿那条路径写，这里只回答"之前那几份长什么样"。
func (s *Service) DraftHistory(ctx context.Context, subjectID, projectID string, slot ContentSlot) ([]DraftSnapshot, error) {
	if _, err := s.ownedProjectSlot(ctx, subjectID, projectID, slot); err != nil {
		return nil, err
	}
	return s.store.ListDraftSnapshots(ctx, projectID, slot)
}

// RestoreDraft 把某一个槽的草稿**整组换回**某一条快照的清单。
//
// 三件事都由这一条路径保证：
//
//   - **它走的是与 push 同一条写入路径**（同一处清单校验、同一处"替换时留快照"），
//     因此恢复**不会让你丢掉恢复前的内容**——那一份也成了一条快照。
//   - **它不碰另一个槽**。
//   - **一份引用了已删除资产的快照不能恢复**（见 snapshotUnusable）：恢复出来的
//     会是一份必然在校验阶段失败的草稿，而用户从"恢复成功"这句话里看不出问题在哪。
//
// source 是执行这次恢复的那一端（`web` / `cli`），与 push 同源。
func (s *Service) RestoreDraft(ctx context.Context, subjectID, projectID string, slot ContentSlot, snapshotID, source string) (Draft, error) {
	if _, err := s.ownedProjectSlot(ctx, subjectID, projectID, slot); err != nil {
		return Draft{}, err
	}
	snapshot, err := s.store.GetDraftSnapshot(ctx, projectID, slot, snapshotID)
	if err != nil {
		return Draft{}, err
	}
	if err := s.requireAssets(ctx, projectID, snapshot.Manifest); err != nil {
		return Draft{}, err
	}
	// 存进去的清单是**当时的**那一份，因此这里做一次形状归一：快照行里的清单本来
	// 就是归一过的，但把它再走一遍同一个入口，保证"恢复出来的草稿"与"推上去的
	// 草稿"在形状上没有第二条路径。
	manifest, err := NormalizeManifest(snapshot.Manifest)
	if err != nil {
		return Draft{}, err
	}
	if err := ValidateManifestForSlot(slot, manifest); err != nil {
		return Draft{}, err
	}
	return s.replaceDraft(ctx, subjectID, projectID, slot, manifest, source)
}

// replaceDraft 是"整组换掉草稿"的唯一实现：写入新清单，并把被换掉的那一份留成
// 一条草稿快照。
//
// push 与 restore 都走它。两处各写一份的表现是"从历史恢复的那一份不留快照"——
// 而那是"恢复会丢掉恢复前的内容"，正是这套东西要消掉的那个问题。
func (s *Service) replaceDraft(ctx context.Context, subjectID, projectID string, slot ContentSlot, manifest Manifest, source string) (Draft, error) {
	previous, err := s.store.GetDraft(ctx, projectID, slot)
	exists := true
	if errors.Is(err, ErrDraftNotFound) {
		// 草稿行是惰性创建的：没有这一行就是"没有可替换的东西"，而不是错误。
		previous = Draft{ProjectID: projectID, Slot: slot}
		exists = false
	} else if err != nil {
		return Draft{}, err
	}
	snapshot, err := s.recordReplacedDraft(subjectID, source, previous, manifest, exists)
	if err != nil {
		return Draft{}, err
	}
	now := s.now()
	if err := s.store.PutDraft(ctx, projectID, slot, manifest, now, snapshot, s.draftSnapshotRetention()); err != nil {
		return Draft{}, err
	}
	s.logDraftSnapshot(projectID, slot, snapshot, subjectID)
	return Draft{ProjectID: projectID, Slot: slot, Manifest: manifest, UpdatedAt: now}, nil
}

// requireAssets 判定一份清单里的资产条目**都还在**。
//
// 它只在"从历史里取出一份旧清单"那两条路上调用（恢复到草稿、把快照存成版本）。
// 一条引用了已删除资产的旧清单本身没有问题——资产删除只被**版本**拦阻（见
// asset-library.md），快照会过期，不该拿去锁住一次删除。问题在于**用它**：恢复
// 出来会是一份必然失败的草稿，存成版本会是一个发布不出去的版本，而那都要到发布
// 那一步才暴露，用户看到一句"资产不存在"却没法从内容里知道该怎么做。
//
// 因此这里如实拒绝并指出是哪一条，让"那份历史用不了"在用户动手的那一刻就成立。
func (s *Service) requireAssets(ctx context.Context, projectID string, manifest Manifest) error {
	for _, entry := range manifest.Assets() {
		if _, err := s.assetOfProject(ctx, projectID, entry.AssetID); err != nil {
			if errors.Is(err, ErrAssetNotFound) {
				return fmt.Errorf("%w: %s 引用的 %s", ErrDraftSnapshotUnusable, entry.Path, entry.AssetID)
			}
			return err
		}
	}
	return nil
}

// logDraftSnapshot 记录这一次替换留下的快照。
//
// **只记标识与文件数，不记路径清单**（见 docs/observability.md）：清单是用户内容，
// 全文进日志既无必要也可能含隐私。
func (s *Service) logDraftSnapshot(projectID string, slot ContentSlot, snapshot DraftSnapshot, subjectID string) {
	if s.logger == nil || snapshot.ID == "" {
		return
	}
	s.logger.Info("已留下草稿快照",
		zap.String("project_id", projectID),
		zap.String("slot", string(slot)),
		zap.String("snapshot_id", snapshot.ID),
		zap.String("subject_id", subjectID),
		zap.String("source", snapshot.Source),
		zap.Int("files", len(snapshot.Manifest)))
}
