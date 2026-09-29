package galaxy

import (
	"context"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/poetlife/aladdin/internal/objectstore"
	"github.com/poetlife/aladdin/internal/watch"
)

const (
	testOwner = "usr_owner"
	testOther = "usr_other"

	// 桶地址与发布域。前者同时是私有区与公开区的主机（两者共用一个桶，靠键
	// 前缀与对象权限区分），后者是站点在哪；断言对外地址与内容安全策略时都用得上。
	testBucketOrigin = "https://aladdin-1250000000.cos.ap-guangzhou.myqcloud.com"
	testPageOrigin   = "https://pages.example.com"
)

// fixture 是一套接在内存实现上的用例夹具。
//
// 内存实现与关系库实现跑**同一套契约用例**（见 gormstore 的契约测试），因此这里
// 覆盖到的语义在两种实现上是同一个结论。
type fixture struct {
	service *Service
	store   *MemoryStore
	objects *objectstore.MemoryStore
	public  *MemoryPublicStore
	events  *watch.Hub
	now     time.Time
}

func newFixture(t *testing.T) *fixture {
	t.Helper()

	f := &fixture{
		store:   NewMemoryStore(),
		objects: objectstore.NewMemoryStore(),
		public:  NewMemoryPublicStore(),
		events:  watch.NewHub(),
		now:     time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC),
	}
	f.service = NewService(Deps{
		Store:  f.store,
		Assets: f.objects,
		Public: f.public,
		Origin: mustOrigin(t),
		Events: f.events,
		Logger: zap.NewNop(),
		Now:    func() time.Time { return f.now },
	})
	return f
}

// mustOrigin 返回测试用的发布地址派生入口。
func mustOrigin(t *testing.T) PublicOrigin {
	t.Helper()
	origin, err := NewPublicOrigin(testBucketOrigin, testPageOrigin)
	if err != nil {
		t.Fatalf("构造发布地址失败: %v", err)
	}
	return origin
}

// advance 把夹具的时钟往前推，用来让"两次操作的时间不同"这类断言有意义。
func (f *fixture) advance(d time.Duration) { f.now = f.now.Add(d) }

// createProject 建一个 **static** 形态的工程，断言失败即终止用例。
func (f *fixture) createProject(t *testing.T, name string) Project {
	t.Helper()
	return f.createProjectForm(t, name, SiteFormStatic)
}

// createProjectForm 建一个指定形态的工程。
func (f *fixture) createProjectForm(t *testing.T, name string, form SiteForm) Project {
	t.Helper()
	project, err := f.service.CreateProject(context.Background(), testOwner, name, "", form)
	if err != nil {
		t.Fatalf("建工程失败: %v", err)
	}
	return project
}

// textEntry 走一遍内容对象直传，并返回一条文本条目。
//
// **服务端在整条路径上不接触字节**，因此这里的 SimulateUpload 不是"帮服务端省
// 一步"，而是替代了客户端的那个角色（见 docs/design/objectstore/README.md）。
func (f *fixture) textEntry(t *testing.T, projectID, entryPath, content string) Entry {
	t.Helper()
	digest, err := f.tryUploadText(t, testOwner, projectID, content)
	if err != nil {
		t.Fatalf("上传内容对象失败: %v", err)
	}
	return Entry{Path: entryPath, Kind: EntryKindText, Digest: digest}
}

// tryUploadText 走一遍内容对象的签发 → 直传 → 提交。
func (f *fixture) tryUploadText(t *testing.T, subjectID, projectID, content string) (string, error) {
	t.Helper()
	ctx := context.Background()
	data := []byte(content)
	digest := ContentDigest(data)
	exists, credential, err := f.service.BeginContentUpload(ctx, subjectID, projectID, digest, int64(len(data)))
	if err != nil {
		return "", err
	}
	if exists {
		return digest, nil
	}
	f.objects.SimulateUpload(credential.Key, data)
	if err := f.service.CommitContentUpload(ctx, subjectID, projectID, digest); err != nil {
		return "", err
	}
	return digest, nil
}

// assetEntry 走一遍资产直传，并返回一条资产条目。
func (f *fixture) assetEntry(t *testing.T, projectID, entryPath, declaredType, filename string, data []byte) Entry {
	t.Helper()
	asset := f.uploadAsset(t, projectID, declaredType, filename, data)
	return Entry{Path: entryPath, Kind: EntryKindAsset, AssetID: asset.ID}
}

// uploadAsset 走一遍资产直传：签发 → 把字节写进假存储（扮演浏览器）→ 提交。
func (f *fixture) uploadAsset(t *testing.T, projectID, declaredType, filename string, data []byte) Asset {
	t.Helper()
	asset, err := f.tryUploadAsset(t, testOwner, projectID, declaredType, filename, data)
	if err != nil {
		t.Fatalf("上传资产失败: %v", err)
	}
	return asset
}

// tryUploadAsset 与 uploadAsset 相同，但把错误交给调用方断言。
func (f *fixture) tryUploadAsset(t *testing.T, subjectID, projectID, declaredType, filename string, data []byte) (Asset, error) {
	t.Helper()
	ctx := context.Background()
	assetID, credential, err := f.service.BeginAssetUpload(ctx, subjectID, projectID, declaredType, int64(len(data)))
	if err != nil {
		return Asset{}, err
	}
	f.objects.SimulateUpload(credential.Key, data)
	return f.service.CommitAssetUpload(ctx, subjectID, projectID, assetID, declaredType, ContentDigest(data), filename, "", "", nil)
}

// uploadAssetWithMeta 与 uploadAsset 相同，但顺带带上说明层元数据。
func (f *fixture) uploadAssetWithMeta(t *testing.T, projectID, declaredType, filename, title, notes string, tags []string, data []byte) Asset {
	t.Helper()
	ctx := context.Background()
	assetID, credential, err := f.service.BeginAssetUpload(ctx, testOwner, projectID, declaredType, int64(len(data)))
	if err != nil {
		t.Fatalf("签发资产失败: %v", err)
	}
	f.objects.SimulateUpload(credential.Key, data)
	asset, err := f.service.CommitAssetUpload(ctx, testOwner, projectID, assetID, declaredType,
		ContentDigest(data), filename, title, notes, tags)
	if err != nil {
		t.Fatalf("上传资产失败: %v", err)
	}
	return asset
}

// pushDraft 整组替换草稿，断言失败即终止用例。
func (f *fixture) pushDraft(t *testing.T, projectID string, entries []Entry) Draft {
	t.Helper()
	draft, err := f.service.PushDraft(context.Background(), testOwner, projectID, entries)
	if err != nil {
		t.Fatalf("推送草稿失败: %v", err)
	}
	return draft
}

// staticSite 建一个 static 工程并推一组「路径 → 内容」进去。
//
// 它覆盖最常见的用例形状：手写单页就是"文件组里恰好一个 index.html"的特例。
func (f *fixture) staticSite(t *testing.T, files map[string]string) Project {
	t.Helper()
	project := f.createProject(t, "站点")
	entries := make([]Entry, 0, len(files))
	for entryPath, content := range files {
		entries = append(entries, f.textEntry(t, project.ID, entryPath, content))
	}
	f.pushDraft(t, project.ID, entries)
	return project
}

// saveVersion 保存一个版本。
func (f *fixture) saveVersion(t *testing.T, projectID string) Version {
	t.Helper()
	version, err := f.service.SaveVersion(context.Background(), testOwner, projectID)
	if err != nil {
		t.Fatalf("保存版本失败: %v", err)
	}
	return version
}

// publish 发布一个版本，断言失败即终止用例。
func (f *fixture) publish(t *testing.T, projectID, versionID string) Publication {
	t.Helper()
	publication, err := f.service.Publish(context.Background(), testOwner, projectID, versionID)
	if err != nil {
		t.Fatalf("发布失败: %v", err)
	}
	return publication
}

// publishSite 是"建站 → 存版本 → 发布"的一次编排，返回工程与发布记录。
func (f *fixture) publishSite(t *testing.T, files map[string]string) (Project, Version, Publication) {
	t.Helper()
	project := f.staticSite(t, files)
	version := f.saveVersion(t, project.ID)
	return project, version, f.publish(t, project.ID, version.ID)
}

// buildArtifacts 走一次校验并取回改写好的产物。
func (f *fixture) buildArtifacts(t *testing.T, projectID string) map[string][]byte {
	t.Helper()
	project, err := f.store.GetProject(context.Background(), projectID)
	if err != nil {
		t.Fatalf("读工程失败: %v", err)
	}
	draft, err := f.store.GetDraft(context.Background(), projectID)
	if err != nil {
		t.Fatalf("读草稿失败: %v", err)
	}
	artifacts, report, err := f.service.buildArtifacts(context.Background(), project, draft.Manifest)
	if err != nil {
		t.Fatalf("校验出错（期望是一个结论）: %v", err)
	}
	if len(report.Problems) > 0 {
		t.Fatalf("期望通过校验，实际被拒: %v", report.Messages())
	}
	return artifacts
}

// report 走一次草稿校验并取回结论。
func (f *fixture) report(t *testing.T, projectID string) Report {
	t.Helper()
	report, err := f.service.ValidateDraft(context.Background(), testOwner, projectID)
	if err != nil {
		t.Fatalf("校验出错（期望是一个结论）: %v", err)
	}
	return report
}

// problems 把结论拼成一段文本，供断言"错误信息里有没有这个标识/位置"。
func problems(report Report) string { return strings.Join(report.Messages(), "; ") }
