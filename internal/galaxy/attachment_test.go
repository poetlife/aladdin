package galaxy

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/poetlife/aladdin/internal/objectstore"
)

// 附件收任意类型：zip、无扩展名、二进制都能提交，元数据里记的是文件名。
//
// 这正是它与资产的分界线——资产那一条路要先过一个类型白名单（见 asset_test.go），
// 而附件靠的是另一条性质：下发时的响应头由签发策略固定。
func TestAttachmentAcceptsAnyType(t *testing.T) {
	f := newFixture(t)
	project := f.createProject(t, "工程")

	for _, filename := range []string{"build.zip", "deploy", "app.bin", "index.html"} {
		attachment := f.uploadAttachment(t, project.ID, "", filename, "", []byte("字节"))
		if attachment.Filename != filename {
			t.Errorf("Filename = %q，期望 %q", attachment.Filename, filename)
		}
		if attachment.SizeBytes != int64(len("字节")) {
			t.Errorf("SizeBytes = %d，期望 %d", attachment.SizeBytes, len("字节"))
		}
	}
}

// 下发的是**强制下载**那一档：响应头由签发入口固定，与上传时声明过的任何东西无关。
//
// 这一条是"类型不限"能成立的前提：浏览器拿到的是一个附件头 + 中性内容类型，
// 因此 `.html` 与 `.svg` 都不会被渲染或执行（见 docs/design/galaxy/attachments.md）。
func TestAttachmentDownloadIsForced(t *testing.T) {
	f := newFixture(t)
	project := f.createProject(t, "工程")
	// 刻意用一个"看起来能在浏览器里跑起来"的后缀。
	attachment := f.uploadAttachment(t, project.ID, "", "index.html", "", []byte("<script>alert(1)</script>"))

	ctx := context.Background()
	url, err := f.service.AttachmentDownloadURL(ctx, testOwner, project.ID, attachment.ID)
	if err != nil {
		t.Fatalf("签发下载地址失败: %v", err)
	}
	if url == "" {
		t.Fatal("下载地址为空")
	}

	downloads := f.objects.Downloads()
	if len(downloads) != 1 {
		t.Fatalf("签发记录 %d 条，期望 1 条", len(downloads))
	}
	issued := downloads[0]
	if issued.Key != AttachmentObjectKey(project.ID, attachment.ID) {
		t.Errorf("签发的键 = %q，期望附件键", issued.Key)
	}
	if issued.ContentType != objectstore.NeutralContentType {
		t.Errorf("固定的内容类型 = %q，期望 %q", issued.ContentType, objectstore.NeutralContentType)
	}
	if !strings.HasPrefix(issued.Disposition, "attachment") {
		t.Errorf("固定的 disposition = %q，期望以 attachment 开头", issued.Disposition)
	}
	if !strings.Contains(issued.Disposition, "index.html") {
		t.Errorf("disposition 里应带上文件名，实际 %q", issued.Disposition)
	}
	// 地址本身也要带着那两个参数：签名覆盖它们，因此签发出去就改不动。
	if !strings.Contains(url, "response-content-type=") ||
		!strings.Contains(url, "response-content-disposition=") {
		t.Errorf("地址里没有固定响应头的参数：%s", url)
	}
}

// 附件**不进公开区**：发布一个版本不产生任何指向附件对象的公开副本，产物清单里
// 也没有它。
func TestAttachmentNeverEntersRelease(t *testing.T) {
	f := newFixture(t)
	project := f.staticSite(t, map[string]string{"index.html": "<html></html>"})
	attachment := f.uploadAttachment(t, project.ID, "", "build.zip", "", []byte("zip"))

	version := f.saveVersion(t, project.ID)
	f.publish(t, project.ID, version.ID)

	for _, key := range f.public.Keys() {
		if strings.Contains(key, "/attachments/") {
			t.Errorf("公开区出现了附件对象：%s", key)
		}
	}
	if _, err := f.objects.Head(context.Background(), AttachmentObjectKey(project.ID, attachment.ID)); err != nil {
		t.Errorf("附件对象应仍在私有区：%v", err)
	}
}

// 声明的字节数超过单文件上限时**拒于签发**（早退，不产生凭证）。
func TestBeginAttachmentUploadRejectsOversizedDeclaration(t *testing.T) {
	f := newFixture(t)
	project := f.createProject(t, "工程")

	_, _, err := f.service.BeginAttachmentUpload(context.Background(), testOwner, project.ID, "", AttachmentMaxBytes+1)
	if !errors.Is(err, ErrAttachmentTooLarge) {
		t.Fatalf("err = %v，期望 ErrAttachmentTooLarge", err)
	}
	if issued := f.objects.Issued(); len(issued) != 0 {
		t.Errorf("被拒的签发留下了凭证：%v", issued)
	}
}

// 提交按**真实字节数**判定：声明一个小值、实际传一个超限的对象，走到提交就被挡，
// 且那个对象被删掉。
//
// 这里用一个报告了更大字节数的假存储：真实的 500 MiB 上限不该让一个离线用例去
// 分配 500 MiB 内存，而这条判断读的正是"存储说这个对象多大"。
func TestCommitAttachmentUploadRejectsOversizedObject(t *testing.T) {
	f := newFixture(t)
	f.service.assets = reportingSizeStore{MemoryStore: f.objects, sizeBytes: AttachmentMaxBytes + 1}
	project := f.createProject(t, "工程")
	ctx := context.Background()

	attachmentID, credential, err := f.service.BeginAttachmentUpload(ctx, testOwner, project.ID, "", 8)
	if err != nil {
		t.Fatalf("签发失败: %v", err)
	}
	f.objects.SimulateUpload(credential.Key, []byte("小字节"))

	_, err = f.service.CommitAttachmentUpload(ctx, testOwner, project.ID, attachmentID, "",
		ContentDigest([]byte("小字节")), "big.zip", "")
	if !errors.Is(err, ErrAttachmentTooLarge) {
		t.Fatalf("err = %v，期望 ErrAttachmentTooLarge", err)
	}
	if _, err := f.objects.Head(ctx, credential.Key); !errors.Is(err, objectstore.ErrObjectNotFound) {
		t.Error("超限的对象没有被删掉")
	}
	if attachments, err := f.store.ListAttachments(ctx, project.ID); err != nil || len(attachments) != 0 {
		t.Errorf("被拒的提交留下了元数据行: %v / %d 条", err, len(attachments))
	}
}

// 工程总量配额按**库里的行**算，且判定落在提交那一步（用真实字节数）。
//
// 20 份 × 500 MiB 的真实字节数不该在离线用例里被真的分配出来，因此这里让假存储
// 报告一个大的字节数：配额读的正是那些**行**里的 size_bytes，而它来自这一次核对。
func TestAttachmentQuotaCountsStoredBytes(t *testing.T) {
	f := newFixture(t)
	f.service.assets = reportingSizeStore{MemoryStore: f.objects, sizeBytes: AttachmentMaxBytes}
	project := f.createProject(t, "工程")
	ctx := context.Background()

	// 20 份合计 10 GiB，正好不到上限（10 GiB = 20.48 × 500 MiB）。
	for i := 0; i < 20; i++ {
		attachmentID, credential, err := f.service.BeginAttachmentUpload(ctx, testOwner, project.ID, "", 1)
		if err != nil {
			t.Fatalf("第 %d 份签发失败: %v", i+1, err)
		}
		f.objects.SimulateUpload(credential.Key, []byte("x"))
		if _, err := f.service.CommitAttachmentUpload(ctx, testOwner, project.ID, attachmentID, "",
			ContentDigest([]byte("x")), fmt.Sprintf("pkg-%d.zip", i), ""); err != nil {
			t.Fatalf("第 %d 份提交失败: %v", i+1, err)
		}
	}
	used, err := f.service.attachmentUsage(ctx, project.ID)
	if err != nil {
		t.Fatalf("统计占用失败: %v", err)
	}
	if want := int64(20) * AttachmentMaxBytes; used != want {
		t.Fatalf("占用 = %d，期望 %d", used, want)
	}

	// 再传一份就会被配额挡住——**在提交那一步**：声明一个字节时它还过得去。
	attachmentID, credential, err := f.service.BeginAttachmentUpload(ctx, testOwner, project.ID, "", 1)
	if err != nil {
		t.Fatalf("第 21 份签发失败: %v", err)
	}
	f.objects.SimulateUpload(credential.Key, []byte("x"))
	if _, err := f.service.CommitAttachmentUpload(ctx, testOwner, project.ID, attachmentID, "",
		ContentDigest([]byte("x")), "pkg-21.zip", ""); !errors.Is(err, ErrAttachmentQuotaExceeded) {
		t.Fatalf("err = %v，期望 ErrAttachmentQuotaExceeded", err)
	}
	if _, err := f.objects.Head(ctx, credential.Key); !errors.Is(err, objectstore.ErrObjectNotFound) {
		t.Error("超配额的对象没有被删掉")
	}
}

// 占用已经接近上限时，**声明**一个超出的值被拒于签发：那是给用户的省事
// （别白传一遍），判定仍然在提交那一步。
func TestBeginAttachmentUploadRejectsOverQuotaDeclaration(t *testing.T) {
	f := newFixture(t)
	project := f.createProject(t, "工程")
	ctx := context.Background()

	// 直接把一行"很大"的记录写进存储，比传 20 份快得多，而判定读的就是这些行。
	if err := f.store.CreateAttachment(ctx, Attachment{
		ID: "atc_big", ProjectID: project.ID, SizeBytes: ProjectAttachmentQuotaBytes,
	}); err != nil {
		t.Fatalf("写入附件行失败: %v", err)
	}
	_, _, err := f.service.BeginAttachmentUpload(ctx, testOwner, project.ID, "", 1)
	if !errors.Is(err, ErrAttachmentQuotaExceeded) {
		t.Fatalf("err = %v，期望 ErrAttachmentQuotaExceeded", err)
	}
}

// 摘要被核对：声明一个与那份字节不符的摘要时拒绝，并删掉对象。
func TestCommitAttachmentUploadRejectsWrongDigest(t *testing.T) {
	f := newFixture(t)
	project := f.createProject(t, "工程")
	ctx := context.Background()

	attachmentID, credential, err := f.service.BeginAttachmentUpload(ctx, testOwner, project.ID, "", 3)
	if err != nil {
		t.Fatalf("签发失败: %v", err)
	}
	f.objects.SimulateUpload(credential.Key, []byte("abc"))

	_, err = f.service.CommitAttachmentUpload(ctx, testOwner, project.ID, attachmentID, "",
		ContentDigest([]byte("别的字节")), "a.zip", "")
	if !errors.Is(err, ErrDigestMismatch) {
		t.Fatalf("err = %v，期望 ErrDigestMismatch", err)
	}
	if _, err := f.objects.Head(ctx, credential.Key); !errors.Is(err, objectstore.ErrObjectNotFound) {
		t.Error("摘要不符的对象没有被删掉")
	}
}

// 上传没完成时给出一个可以重来的结论，且不留元数据行。
func TestCommitAttachmentUploadRequiresObject(t *testing.T) {
	f := newFixture(t)
	project := f.createProject(t, "工程")
	ctx := context.Background()

	attachmentID, _, err := f.service.BeginAttachmentUpload(ctx, testOwner, project.ID, "", 3)
	if err != nil {
		t.Fatalf("签发失败: %v", err)
	}
	// 刻意不写对象：模拟上传中断。
	if _, err := f.service.CommitAttachmentUpload(ctx, testOwner, project.ID, attachmentID, "",
		ContentDigest([]byte("abc")), "a.zip", ""); !errors.Is(err, ErrAttachmentObjectMissing) {
		t.Errorf("err = %v，期望 ErrAttachmentObjectMissing", err)
	}
	if attachments, err := f.store.ListAttachments(ctx, project.ID); err != nil || len(attachments) != 0 {
		t.Errorf("未提交的上传留下了元数据行: %v / %d 条", err, len(attachments))
	}
}

// 标注一个不存在的版本被拒：一条悬空标注在界面上是一个打不开的版本号。
func TestAttachmentRejectsUnknownVersion(t *testing.T) {
	f := newFixture(t)
	project := f.createProject(t, "工程")
	ctx := context.Background()

	if _, _, err := f.service.BeginAttachmentUpload(ctx, testOwner, project.ID, "ver_nope", 1); !errors.Is(err, ErrVersionNotFound) {
		t.Fatalf("err = %v，期望 ErrVersionNotFound", err)
	}
}

// 标注可以指向本工程的版本，**也可以被清空**：删除那个版本时标注被清空，附件本身
// 与它的字节不受影响（标注不是引用，见 docs/design/galaxy/attachments.md）。
func TestDeletingVersionClearsAttachmentLabel(t *testing.T) {
	f := newFixture(t)
	project := f.staticSite(t, map[string]string{"index.html": "<html></html>"})
	version := f.saveVersion(t, project.ID)
	attachment := f.uploadAttachment(t, project.ID, version.ID, "build.zip", "", []byte("zip"))

	if attachment.VersionID != version.ID {
		t.Fatalf("VersionID = %q，期望 %q", attachment.VersionID, version.ID)
	}

	if err := f.service.DeleteVersion(context.Background(), testOwner, project.ID, SlotSite, version.ID); err != nil {
		t.Fatalf("删版本失败: %v", err)
	}
	got, err := f.store.GetAttachment(context.Background(), project.ID, attachment.ID)
	if err != nil {
		t.Fatalf("读附件失败: %v", err)
	}
	if got.VersionID != "" {
		t.Errorf("标注 = %q，期望被清空", got.VersionID)
	}
	if got.Digest != attachment.Digest || got.SizeBytes != attachment.SizeBytes {
		t.Error("清空标注不该动附件自己的任何字段")
	}
	// 字节仍在：标注不是引用，因此它不参与任何清理。
	if _, err := f.objects.Head(context.Background(), AttachmentObjectKey(project.ID, attachment.ID)); err != nil {
		t.Errorf("附件对象应仍在私有区：%v", err)
	}
}

// 改说明只动说明那一层：文件名、字节数、摘要与标注的版本逐字不变。
func TestUpdateAttachmentDescriptionKeepsBytes(t *testing.T) {
	f := newFixture(t)
	project := f.createProject(t, "工程")
	attachment := f.uploadAttachment(t, project.ID, "", "build.zip", "第一版", []byte("zip"))

	updated, err := f.service.UpdateAttachment(context.Background(), testOwner, project.ID, attachment.ID, "第二版")
	if err != nil {
		t.Fatalf("改说明失败: %v", err)
	}
	if updated.Description != "第二版" {
		t.Errorf("Description = %q，期望 %q", updated.Description, "第二版")
	}
	if updated.Filename != attachment.Filename || updated.Digest != attachment.Digest ||
		updated.SizeBytes != attachment.SizeBytes || updated.UploadedAt != attachment.UploadedAt {
		t.Error("改说明不该动字节层的任何一项")
	}

	tooLong := strings.Repeat("字", AttachmentDescriptionMaxRunes+1)
	if _, err := f.service.UpdateAttachment(context.Background(), testOwner, project.ID, attachment.ID, tooLong); !errors.Is(err, ErrAttachmentDescriptionTooLong) {
		t.Errorf("err = %v，期望 ErrAttachmentDescriptionTooLong", err)
	}
}

// 删除一份附件：元数据行与对象一并清掉，且**不被任何引用拦阻**。
func TestDeleteAttachmentRemovesRowAndObject(t *testing.T) {
	f := newFixture(t)
	project := f.staticSite(t, map[string]string{"index.html": "<html></html>"})
	version := f.saveVersion(t, project.ID)
	attachment := f.uploadAttachment(t, project.ID, version.ID, "build.zip", "", []byte("zip"))
	ctx := context.Background()

	if err := f.service.DeleteAttachment(ctx, testOwner, project.ID, attachment.ID); err != nil {
		t.Fatalf("删除附件失败: %v", err)
	}
	if _, err := f.store.GetAttachment(ctx, project.ID, attachment.ID); !errors.Is(err, ErrAttachmentNotFound) {
		t.Errorf("err = %v，期望 ErrAttachmentNotFound", err)
	}
	if _, err := f.objects.Head(ctx, AttachmentObjectKey(project.ID, attachment.ID)); !errors.Is(err, objectstore.ErrObjectNotFound) {
		t.Error("附件对象没有被删掉")
	}
}

// 归属：另一个主体读不到、也删不了这份附件，且结论与"不存在"相同。
func TestAttachmentIsOwnedByProjectOwner(t *testing.T) {
	f := newFixture(t)
	project := f.createProject(t, "工程")
	attachment := f.uploadAttachment(t, project.ID, "", "build.zip", "", []byte("zip"))
	ctx := context.Background()

	if _, err := f.service.ListAttachments(ctx, testOther, project.ID); !errors.Is(err, ErrProjectNotFound) {
		t.Errorf("他人读取 err = %v，期望 ErrProjectNotFound", err)
	}
	if _, err := f.service.AttachmentDownloadURL(ctx, testOther, project.ID, attachment.ID); !errors.Is(err, ErrProjectNotFound) {
		t.Errorf("他人取地址 err = %v，期望 ErrProjectNotFound", err)
	}
	if err := f.service.DeleteAttachment(ctx, testOther, project.ID, attachment.ID); !errors.Is(err, ErrProjectNotFound) {
		t.Errorf("他人删除 err = %v，期望 ErrProjectNotFound", err)
	}
}

// 删除工程连带清掉附件：行与对象都不在。
func TestDeleteProjectRemovesAttachments(t *testing.T) {
	f := newFixture(t)
	project := f.createProject(t, "工程")
	attachment := f.uploadAttachment(t, project.ID, "", "build.zip", "", []byte("zip"))
	ctx := context.Background()

	if err := f.service.DeleteProject(ctx, testOwner, project.ID); err != nil {
		t.Fatalf("删除工程失败: %v", err)
	}
	if _, err := f.store.GetAttachment(ctx, project.ID, attachment.ID); !errors.Is(err, ErrAttachmentNotFound) {
		t.Errorf("err = %v，期望 ErrAttachmentNotFound", err)
	}
	if _, err := f.objects.Head(ctx, AttachmentObjectKey(project.ID, attachment.ID)); !errors.Is(err, objectstore.ErrObjectNotFound) {
		t.Error("附件对象没有被删掉")
	}
}

// 未配置对象存储时附件整体缺席：四个动作给同一个结论，而工程元数据照常可用。
func TestAttachmentUnavailableWithoutBucket(t *testing.T) {
	f := newFixture(t)
	project := f.createProject(t, "工程")
	f.service.assets = nil
	ctx := context.Background()

	if _, err := f.service.ListAttachments(ctx, testOwner, project.ID); !errors.Is(err, ErrAttachmentUnavailable) {
		t.Errorf("列表 err = %v，期望 ErrAttachmentUnavailable", err)
	}
	if _, _, err := f.service.BeginAttachmentUpload(ctx, testOwner, project.ID, "", 1); !errors.Is(err, ErrAttachmentUnavailable) {
		t.Errorf("签发 err = %v，期望 ErrAttachmentUnavailable", err)
	}
	if err := f.service.DeleteAttachment(ctx, testOwner, project.ID, "atc_x"); !errors.Is(err, ErrAttachmentUnavailable) {
		t.Errorf("删除 err = %v，期望 ErrAttachmentUnavailable", err)
	}
	if _, err := f.service.GetProject(ctx, testOwner, project.ID); err != nil {
		t.Errorf("工程元数据应当照常可读: %v", err)
	}
}

// 能力下发把附件的三项边界一并给出：能力开关、单文件上限与工程总量配额。
func TestCapabilitiesReportAttachments(t *testing.T) {
	f := newFixture(t)
	caps := f.service.Capabilities()
	if !caps.AttachmentEnabled {
		t.Error("配了桶时附件应可用")
	}
	if caps.MaxAttachmentBytes != AttachmentMaxBytes {
		t.Errorf("MaxAttachmentBytes = %d，期望 %d", caps.MaxAttachmentBytes, AttachmentMaxBytes)
	}
	if caps.ProjectAttachmentQuotaBytes != ProjectAttachmentQuotaBytes {
		t.Errorf("ProjectAttachmentQuotaBytes = %d，期望 %d",
			caps.ProjectAttachmentQuotaBytes, ProjectAttachmentQuotaBytes)
	}

	f.service.assets = nil
	if f.service.Capabilities().AttachmentEnabled {
		t.Error("没配桶时附件应不可用")
	}
}

// reportingSizeStore 把对象报告成另一个字节数，其余行为照旧。
//
// 它存在的理由只有一条：单文件上限是 500 MiB、工程配额是 10 GiB，而"提交按真实
// 字节数判定"这条要验的是**服务端读到的那个数**。让离线用例真的分配几百 MB 只为
// 了撞上一条判断，是把可测性建立在内存够大上；这里报告的正是服务端会用的那个值。
type reportingSizeStore struct {
	*objectstore.MemoryStore
	sizeBytes int64
}

func (s reportingSizeStore) Head(ctx context.Context, key string) (objectstore.ObjectStat, error) {
	if _, err := s.MemoryStore.Head(ctx, key); err != nil {
		return objectstore.ObjectStat{}, err
	}
	return objectstore.ObjectStat{SizeBytes: s.sizeBytes}, nil
}
