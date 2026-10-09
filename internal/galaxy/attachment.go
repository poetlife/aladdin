package galaxy

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/poetlife/aladdin/internal/idgen"
	"github.com/poetlife/aladdin/internal/objectstore"
)

const (
	// AttachmentMaxBytes 是**单份附件**的字节上限。
	//
	// 取值比资产最高那一档（视频 100 MiB）大得多：附件是构建产物，一个带依赖的
	// 发布包几百 MB 是常态。它由签发出去的策略在存储侧执行（与资产同一条链路），
	// 提交时再按真实字节数核一遍。
	AttachmentMaxBytes = 500 << 20

	// ProjectAttachmentQuotaBytes 是**一个工程的附件总量**上限。
	//
	// 它与单文件上限是两件事：前者回答"一次能传多大"，后者回答"这个工程一共能
	// 放多少"。只有一个的话，一份 500 MiB 的包重复传二十遍就把一个工程的存储
	// 吃完了，而每一次都合规。
	//
	// **它按库里的行算**（见 Service.attachmentUsage）：库内的元数据行是"存在
	// 哪些附件"的权威，桶上可能留下无从被引用的孤儿对象，那是清理的事。
	ProjectAttachmentQuotaBytes = 10 << 30

	// AttachmentURLTTL 是下载地址的有效期。
	//
	// 它比编辑态的资产地址（10 分钟）短：附件是**整套构建产物**，一份几百 MB 的
	// 包经一次转发就是一次实质的带宽占用。短时有效把"这个地址被贴到别处"的窗口
	// 压小，而过期只需要重新调用一次接口。
	AttachmentURLTTL = 5 * time.Minute

	// AttachmentDescriptionMaxRunes 是说明的长度上限。
	//
	// 与工程简介、资产备注同一条理由按**字符数**而不是字节数计：用户感知的长度
	// 是字数，按字节算会让"一段中文写到十几个字就被拒"成为一条需要解释的规则。
	AttachmentDescriptionMaxRunes = 512

	// attachmentIDPrefix 是附件标识的前缀。分配即冻结、永不复用——与工程、
	// 版本、资产标识同一条约定。
	attachmentIDPrefix = "atc_"
)

var (
	// ErrAttachmentUnavailable 表示这个部署没有配置私有桶。
	//
	// 与 ErrAssetUnavailable 同源：**它不是故障**，而是"这个能力没开"，前端
	// 据此不渲染入口。
	ErrAttachmentUnavailable = errors.New("附件功能未启用")

	// ErrAttachmentNotFound 表示这个工程下没有这个附件。
	//
	// 它与"附件属于别的工程"是同一个结论——与资产同一条理由：区分它们会给出
	// 一个跨工程的探测面。
	ErrAttachmentNotFound = errors.New("附件不存在")

	// ErrAttachmentTooLarge 表示附件超过单文件上限。
	ErrAttachmentTooLarge = errors.New("附件超过单文件上限")

	// ErrAttachmentQuotaExceeded 表示这个工程的附件总量已满。
	//
	// 它与 ErrAttachmentTooLarge 必须分开：前者是"这一份太大"，后者是"这个工程
	// 已经放满了"——用户要做的事不一样（换一份小的 vs 先删掉一些）。
	ErrAttachmentQuotaExceeded = errors.New("工程的附件总量已满")

	// ErrAttachmentObjectMissing 表示提交时那个键上还没有对象。
	//
	// 它是"直传没完成"这一情形的结论（网络中断、页面被关掉），不是故障，
	// 而是一次可以重来的上传（与资产同一条）。
	ErrAttachmentObjectMissing = errors.New("附件对象不存在，上传可能没有完成")

	// ErrAttachmentDescriptionTooLong 表示说明超过长度上限。
	ErrAttachmentDescriptionTooLong = errors.New("附件说明过长")
)

// Attachment 是工程里的一份**发布物**：二进制、zip 包、导出文件。
//
// 它与资产并列而不混用（见 docs/design/galaxy/attachments.md）：资产会被网页
// 引用、由浏览器渲染；附件只给工程成员下载。因此**类型不限**，而它靠另一条
// 性质立住——下发时的响应头由签发策略固定，浏览器永远只把它存下来。
//
// 字节不可变：没有"替换这一份的字节"这个动作，要换就重新上传一个。可变的只有
// Description 那一层。
type Attachment struct {
	// ID 由 aladdin 分配，不可猜、不复用。
	ID string
	// ProjectID 是所属工程：一个附件属于唯一一个工程，不做跨工程共享。
	ProjectID string
	// VersionID 是**标注**的版本（可空）。它说明"这一份是那一版的构建产物"。
	//
	// **标注不是引用**：它不参与发布，也不拦阻版本删除——删除一个版本时指向它的
	// 附件标注被清空（见 DeleteVersion）。理由见 docs/design/galaxy/attachments.md。
	VersionID string
	// Filename 是原始文件名。**仅供展示与排障**，它不进对象键，也不参与任何判断：
	// 它可能含路径分隔符、控制字符以及别人的名字。
	//
	// 它另外会进**下载响应头**——那是唯一一处它对外可见的地方，而那一处会先滤掉
	// 会破坏响应头的字符（见 objectstore.DownloadDisposition）。
	Filename string
	// SizeBytes 是字节数。它取自提交时对对象的核对，不是声明值。
	SizeBytes int64
	// Digest 是字节的 SHA-256。
	//
	// **它不是寻址键**（附件按标识寻址，与资产不同），只用于核对与展示：核对
	// 保证这一行记录说的那份字节确实是那份，展示让"这份是不是我要的"有一个
	// 可粘贴的判据。
	Digest string
	// Description 是给人看的说明，可改，可空。
	Description string
	// UploadedBySubjectID 与 UploadedAt 是留痕。
	UploadedBySubjectID string
	UploadedAt          time.Time
}

// AttachmentView 是一份附件加一条短时下载地址。
//
// 与 AssetView 同一条理由：地址每次读取都不同（它是一份会过期的凭证），而元数据
// 是附件自己的属性。
type AttachmentView struct {
	Attachment Attachment
	// DownloadURL 为空表示取不到地址（私有桶未配置或签发失败）。此时附件仍列出。
	DownloadURL string
}

// AttachmentObjectKey 返回一份附件在**私有区**的对象键（唯一入口）。
//
// 它与资产、文本条目并列在 `galaxy/<工程标识>/` 之下，靠 `attachments/` 那一段
// 分开。**不含版本段**：标注的版本是可改的（也可能随版本删除被清空），把它拼进
// 键会让"换一个标注"变成一次搬字节。
//
// 前缀是常量而不是配置项——与 assets/、text/、release/ 同一条理由：改了前缀，
// 存量对象在一瞬间全部变成孤儿。
func AttachmentObjectKey(projectID, attachmentID string) string {
	return assetKeyPrefix + projectID + "/attachments/" + attachmentID
}

// NormalizeAttachmentDescription 归一化说明并校验长度（唯一入口）。
//
// 只有一条规则：去掉首尾空白。中间部分的空白与换行原样保留——说明常是一段构建
// 命令或一句发布公告，把它折成一行是替用户改写内容。
func NormalizeAttachmentDescription(description string) (string, error) {
	description = strings.TrimSpace(description)
	if len([]rune(description)) > AttachmentDescriptionMaxRunes {
		return "", fmt.Errorf("%w: 上限 %d 个字", ErrAttachmentDescriptionTooLong, AttachmentDescriptionMaxRunes)
	}
	return description, nil
}

// newAttachmentID 分配一个附件标识（唯一入口）。
func newAttachmentID() (string, error) {
	return idgen.New(attachmentIDPrefix)
}

// requireAttachmentStore 在私有桶缺席时给出统一结论。
//
// **附件功能整体缺席**：读取、上传、删除一律如此（与资产同一条）。
func (s *Service) requireAttachmentStore() error {
	if s.assets == nil {
		return ErrAttachmentUnavailable
	}
	return nil
}

// attachmentOfProject 判定一份附件是否属于某个工程（唯一入口）。
//
// 不属于时返回 ErrAttachmentNotFound——"不存在"与"属于别的工程"是同一个结论。
func (s *Service) attachmentOfProject(ctx context.Context, projectID, attachmentID string) (Attachment, error) {
	return s.store.GetAttachment(ctx, projectID, attachmentID)
}

// attachmentVersion 校验标注的版本确实属于这个工程（唯一入口）。
//
// 空串是合法的（表示不标注）。标注指向一个不存在的版本时**拒绝**，而不是把它
// 记下来：那会留下一条悬空标注，而它的表现形式是界面上一个打不开的版本号。
func (s *Service) attachmentVersion(ctx context.Context, projectID, versionID string) error {
	if versionID == "" {
		return nil
	}
	if _, err := s.store.GetVersionByID(ctx, projectID, versionID); err != nil {
		return err
	}
	return nil
}

// attachmentUsage 返回这个工程已经占用的附件字节数。
//
// 它算的是**库里的行**：库内的元数据行是"存在哪些附件"的权威，桶上可能留下
// 无从被引用的孤儿对象，而那是回收的事，不是配额的事。
func (s *Service) attachmentUsage(ctx context.Context, projectID string) (int64, error) {
	return s.store.SumAttachmentBytes(ctx, projectID)
}

// checkAttachmentQuota 判定"再放进一份 declaredSize 的附件会不会超过总量上限"。
//
// 它在**签发前**与**提交后**各调用一次，两次的理由不同：
//
//   - 签发前那次是**给用户的省事**：别把一个几百 MB 的包整个传上来只为了被拒。
//     它的输入是不可信的声明值，因此不是安全边界。
//   - 提交后那次是**真正的判定**：输入是核对出来的真实字节数，而且那时元数据行
//     还没写——一次超配额的提交因此不会留下任何痕迹。
func (s *Service) checkAttachmentQuota(ctx context.Context, projectID string, declaredSize int64) error {
	used, err := s.attachmentUsage(ctx, projectID)
	if err != nil {
		return err
	}
	if used+declaredSize > ProjectAttachmentQuotaBytes {
		return fmt.Errorf("%w: 已用 %d 字节，本次 %d 字节，上限 %d 字节",
			ErrAttachmentQuotaExceeded, used, declaredSize, ProjectAttachmentQuotaBytes)
	}
	return nil
}

// BeginAttachmentUpload 开始一次附件上传：分配附件标识并签发一份直传凭证。
//
// 与 BeginAssetUpload 逐字同构，两处差别只有三样：
//
//   - **类型不由调用方给**：下发的类型是固定的，因此上传声明的类型固定为中性的
//     那一档（见 docs/design/galaxy/attachments.md）。策略里仍要带一条类型规则
//     ——上限绑在它上面；
//   - **上限是一档**（500 MiB），不分类别：除了"多大算大"没有第二档；
//   - **还多一道总量配额**（见 checkAttachmentQuota）。
//
// versionID 是可选标注，为空表示不标注。文件名**不在这里给**：它不参与签发，
// 只在上传成功之后写进元数据（与资产同一条）。
func (s *Service) BeginAttachmentUpload(ctx context.Context, subjectID, projectID, versionID string, declaredSize int64) (string, objectstore.Credential, error) {
	if err := s.requireAttachmentStore(); err != nil {
		return "", objectstore.Credential{}, err
	}
	if _, err := OwnedProject(ctx, s.store, projectID, subjectID); err != nil {
		return "", objectstore.Credential{}, err
	}
	if err := s.attachmentVersion(ctx, projectID, versionID); err != nil {
		return "", objectstore.Credential{}, err
	}
	// 按**声明**的大小早退。它**不是安全边界**：声明是不可信输入，真正的判定在
	// 提交那一步（两条都是）。
	if declaredSize > AttachmentMaxBytes {
		return "", objectstore.Credential{}, fmt.Errorf("%w: 声明 %d 字节，上限 %d 字节",
			ErrAttachmentTooLarge, declaredSize, AttachmentMaxBytes)
	}
	if err := s.checkAttachmentQuota(ctx, projectID, declaredSize); err != nil {
		return "", objectstore.Credential{}, err
	}
	attachmentID, err := newAttachmentID()
	if err != nil {
		return "", objectstore.Credential{}, err
	}
	// 一次上传就是这个键的第一次也是唯一一次写入：附件不可变，覆盖写不是它的语义。
	credential, err := s.assets.IssueUpload(ctx,
		AttachmentObjectKey(projectID, attachmentID),
		[]objectstore.TypeRule{attachmentTypeRule()},
		false)
	if err != nil {
		return "", objectstore.Credential{}, err
	}
	return attachmentID, credential, nil
}

// attachmentTypeRule 返回签发附件直传凭证用的**那一条**类型规则（唯一入口）。
//
// 类型是中性的那一档（与内容对象同一个常量）：附件的下发类型恒为它，因此上传
// 时那一次声明不产生任何对外可见的后果。规则存在的理由是**上限绑在它上面**——
// 策略的最小单位就是"一种类型 + 它那一档的上限"。
func attachmentTypeRule() objectstore.TypeRule {
	return objectstore.TypeRule{ContentType: objectstore.NeutralContentType, MaxBytes: AttachmentMaxBytes}
}

// CommitAttachmentUpload 提交一次附件上传：核对对象确实到了，写入附件元数据。
//
// 与 CommitAssetUpload 同构，核对顺序也一样：先校验声明的形状与标注的版本，
// 再 Head 取真实字节数并按**真实值**核对单文件上限与工程配额，最后核对摘要。
// 任一核对不通过时删掉那个对象（它无从被引用，还占着配额）。
//
// 标注的版本在这里**重新校验**：两次调用之间服务端不保留任何状态，那正是"未提交
// 的上传不留痕迹"的实现方式。
func (s *Service) CommitAttachmentUpload(ctx context.Context, subjectID, projectID, attachmentID, versionID, declaredDigest, filename, description string) (Attachment, error) {
	if err := s.requireAttachmentStore(); err != nil {
		return Attachment{}, err
	}
	if _, err := OwnedProject(ctx, s.store, projectID, subjectID); err != nil {
		return Attachment{}, err
	}
	description, err := NormalizeAttachmentDescription(description)
	if err != nil {
		return Attachment{}, err
	}
	if err := s.attachmentVersion(ctx, projectID, versionID); err != nil {
		return Attachment{}, err
	}
	// 摘要在这一步核对，因此它的形状先要合法：一个含分隔符或控制字符的取值会被
	// 写进库、也会进日志与界面。
	if !IsContentDigest(declaredDigest) {
		return Attachment{}, ErrDigestInvalid
	}
	key := AttachmentObjectKey(projectID, attachmentID)
	stat, err := objectstore.VerifyUploaded(ctx, s.assets, key, AttachmentMaxBytes)
	if err != nil {
		switch {
		case errors.Is(err, objectstore.ErrObjectNotFound):
			return Attachment{}, ErrAttachmentObjectMissing
		case errors.Is(err, objectstore.ErrUploadTooLarge):
			// 真实字节数才是判定输入：声明值是不可信的一方。
			return Attachment{}, fmt.Errorf("%w: 上限 %d 字节", ErrAttachmentTooLarge, AttachmentMaxBytes)
		default:
			return Attachment{}, err
		}
	}
	// **配额用真实字节数复核**：声明一个小的值、传一个大的上来，走到这里就被挡住。
	// 上层那一次早退只省了一次往返，判定在这一处。
	if err := s.checkAttachmentQuota(ctx, projectID, stat.SizeBytes); err != nil {
		s.discardAttachmentObject(ctx, key, projectID, attachmentID, "超配额")
		return Attachment{}, err
	}
	if err := s.verifyAttachmentDigest(ctx, key, declaredDigest); err != nil {
		s.discardAttachmentObject(ctx, key, projectID, attachmentID, "摘要不符")
		return Attachment{}, err
	}
	attachment := Attachment{
		ID:                  attachmentID,
		ProjectID:           projectID,
		VersionID:           versionID,
		Filename:            truncateRunes(filename, filenameMaxRunes),
		SizeBytes:           stat.SizeBytes,
		Digest:              declaredDigest,
		Description:         description,
		UploadedBySubjectID: subjectID,
		UploadedAt:          s.now(),
	}
	if err := s.store.CreateAttachment(ctx, attachment); err != nil {
		// 元数据写不进去时把对象删掉：留着它就留下一个**无从被引用**的对象，
		// 而"存在哪些附件"的权威是库内的行。
		s.discardAttachmentObject(ctx, key, projectID, attachmentID, "写入元数据失败")
		return Attachment{}, err
	}
	if s.logger != nil {
		// **文件名与说明不进日志原文**（见 docs/observability.md）：它们与资产
		// 的文件名、备注同级，是用户内容。
		s.logger.Info("已提交附件上传",
			zap.String("project_id", projectID),
			zap.String("attachment_id", attachment.ID),
			zap.String("subject_id", subjectID),
			zap.Int64("bytes", attachment.SizeBytes))
	}
	s.publish(projectID)
	return attachment, nil
}

// discardAttachmentObject 尽力删掉一份刚上传、但没能成为附件的对象。
//
// 它刻意不返回错误，也不改变调用方的结论：库内的行才是权威，删不掉只会留下一个
// 无从被引用的孤儿对象。
func (s *Service) discardAttachmentObject(ctx context.Context, key, projectID, attachmentID, reason string) {
	if s.assets == nil {
		return
	}
	if err := s.assets.Delete(ctx, key); err != nil && s.logger != nil {
		s.logger.Warn("回滚附件对象失败，桶上可能留下错误的对象",
			zap.String("reason", reason),
			zap.String("project_id", projectID),
			zap.String("attachment_id", attachmentID),
			zap.Error(err))
	}
}

// verifyAttachmentDigest 核对一份刚上传的附件：那份字节的摘要是不是它声明的那个。
//
// 两条路与资产逐字相同（让存储侧算；存储侧给不出时读回自己算），理由也一样：
// 直传之后服务端没有字节可以自己算它。
//
// **附件的摘要不是寻址键**，因此核对它的意义与资产不同：它不是"地址会不会指到
// 别处"，而是"这一行记录说的那份字节是不是真的那份"。仍然核对，是因为展示与
// 排障全靠它，而一个从不核对的值没有任何理由被相信。
func (s *Service) verifyAttachmentDigest(ctx context.Context, key, declaredDigest string) error {
	actual, err := s.assets.SHA256(ctx, key)
	switch {
	case err == nil:
		if actual != declaredDigest {
			return fmt.Errorf("%w: 附件声明 %s，实际 %s", ErrDigestMismatch, declaredDigest, actual)
		}
		return nil
	case !errors.Is(err, objectstore.ErrHashUnavailable):
		return err
	}

	data, err := s.assets.Read(ctx, key)
	if err != nil {
		return err
	}
	if actual := ContentDigest(data); actual != declaredDigest {
		return fmt.Errorf("%w: 附件声明 %s，实际 %s", ErrDigestMismatch, declaredDigest, actual)
	}
	if s.logger != nil {
		s.logger.Warn("存储侧不提供内容摘要，已回退为读回核对", zap.String("object_key", key))
	}
	return nil
}

// ListAttachments 列出工程的附件，并逐个签发下载地址。
//
// 它**不按任何东西筛选**：附件是构建产物，一个工程几十份就到头了，筛选是资产
// 那边的问题（那里有几百张图与标签）。
func (s *Service) ListAttachments(ctx context.Context, subjectID, projectID string) ([]AttachmentView, error) {
	if err := s.requireAttachmentStore(); err != nil {
		return nil, err
	}
	if _, err := OwnedProject(ctx, s.store, projectID, subjectID); err != nil {
		return nil, err
	}
	attachments, err := s.store.ListAttachments(ctx, projectID)
	if err != nil {
		return nil, err
	}
	views := make([]AttachmentView, 0, len(attachments))
	for _, attachment := range attachments {
		views = append(views, AttachmentView{
			Attachment:  attachment,
			DownloadURL: s.listAttachmentURL(ctx, attachment),
		})
	}
	return views, nil
}

// UpdateAttachment 覆盖一份附件的说明。
//
// **它不碰字节层**：文件名、字节数、内容摘要与对象键在调用前后逐字不变，因此
// 已经发给别人的下载地址指向的还是同一份字节。与 UpdateAsset 同一条。
//
// 请求表达**期望的完整状态**：空串清空说明。
func (s *Service) UpdateAttachment(ctx context.Context, subjectID, projectID, attachmentID, description string) (Attachment, error) {
	if err := s.requireAttachmentStore(); err != nil {
		return Attachment{}, err
	}
	description, err := NormalizeAttachmentDescription(description)
	if err != nil {
		return Attachment{}, err
	}
	if _, err := OwnedProject(ctx, s.store, projectID, subjectID); err != nil {
		return Attachment{}, err
	}
	if err := s.store.UpdateAttachmentDescription(ctx, projectID, attachmentID, description); err != nil {
		return Attachment{}, err
	}
	// 写入已经成功，事件就发出去（与 UpdateAsset 同源）。
	s.publish(projectID)
	if s.logger != nil {
		// **说明原文不进日志**，与文件名同级。
		s.logger.Info("已更新附件说明",
			zap.String("project_id", projectID),
			zap.String("attachment_id", attachmentID),
			zap.String("subject_id", subjectID))
	}
	return s.store.GetAttachment(ctx, projectID, attachmentID)
}

// DeleteAttachment 删除一份附件：删私有区的对象与元数据行。
//
// **它不被任何引用拦阻**：附件不被文件组引用、标注也不构成引用（标注在版本删除
// 时被清空，而不是反过来拦住删除）。这与删除资产那条"被引用的资产不可删"是两件
// 事——那条的理由是版本不可变意味着"它此后永远可以发布"，而附件不参与发布。
func (s *Service) DeleteAttachment(ctx context.Context, subjectID, projectID, attachmentID string) error {
	if err := s.requireAttachmentStore(); err != nil {
		return err
	}
	if _, err := OwnedProject(ctx, s.store, projectID, subjectID); err != nil {
		return err
	}
	attachment, err := s.attachmentOfProject(ctx, projectID, attachmentID)
	if err != nil {
		return err
	}
	if err := s.store.DeleteAttachment(ctx, projectID, attachmentID); err != nil {
		return err
	}
	// 对象删除失败不影响"附件已删除"这一结论（库内是权威），与资产同源。
	key := AttachmentObjectKey(projectID, attachment.ID)
	if err := s.assets.DeleteMany(ctx, []string{key}); err != nil && s.logger != nil {
		s.logger.Warn("删除附件对象失败，元数据已清空",
			zap.String("project_id", projectID),
			zap.String("attachment_id", attachmentID),
			zap.Error(err))
	}
	s.publish(projectID)
	if s.logger != nil {
		s.logger.Info("已删除附件",
			zap.String("project_id", projectID),
			zap.String("attachment_id", attachmentID),
			zap.String("subject_id", subjectID))
	}
	return nil
}

// AttachmentDownloadURL 签发一份附件的下载地址（唯一入口）。
//
// 它要求调用者是该工程的拥有者：这条地址在有效期内**谁拿到都能用**，因此签发
// 这一步要判权（与编辑态的资产地址同一条）。附件没有发布态，所以不存在"另一条
// 不判权的读取路径"。
func (s *Service) AttachmentDownloadURL(ctx context.Context, subjectID, projectID, attachmentID string) (string, error) {
	if _, err := OwnedProject(ctx, s.store, projectID, subjectID); err != nil {
		return "", err
	}
	attachment, err := s.attachmentOfProject(ctx, projectID, attachmentID)
	if err != nil {
		return "", err
	}
	return s.presignAttachment(ctx, attachment)
}

// presignAttachment 签发一份附件的下载地址。
//
// 走的是**强制下载**那一档：响应头由签发入口固定（中性的内容类型 + attachment），
// 文件名只是它带上的展示信息。这是"附件收任意类型"能成立的前提。
func (s *Service) presignAttachment(ctx context.Context, attachment Attachment) (string, error) {
	if s.assets == nil {
		return "", ErrAttachmentUnavailable
	}
	return s.assets.PresignDownload(ctx,
		AttachmentObjectKey(attachment.ProjectID, attachment.ID),
		attachment.Filename,
		AttachmentURLTTL)
}

// listAttachmentURL 为清单签发下载地址。
//
// 签发失败只留痕、不返错：清单本身仍然有意义，而一个取不到地址的条目表现出来
// 就是"这一份暂时下不了"（与资产同源）。
func (s *Service) listAttachmentURL(ctx context.Context, attachment Attachment) string {
	url, err := s.presignAttachment(ctx, attachment)
	if err != nil {
		if s.logger != nil {
			s.logger.Warn("签发附件下载地址失败",
				zap.String("project_id", attachment.ProjectID),
				zap.String("attachment_id", attachment.ID),
				zap.Error(err))
		}
		return ""
	}
	return url
}
