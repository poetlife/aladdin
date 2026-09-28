package galaxy

import (
	"context"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/poetlife/aladdin/internal/objectstore"
)

const (
	testOwner = "usr_owner"
	testOther = "usr_other"

	// 发布态的公开桶与发布域。两者分别是"资源从哪来"与"页面在哪"，
	// 断言对外地址与内容安全策略时都用得上。
	testAssetsOrigin = "https://aladdin-public-1250000000.cos.ap-guangzhou.myqcloud.com"
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
	now     time.Time
}

func newFixture(t *testing.T) *fixture {
	t.Helper()

	f := &fixture{
		store:   NewMemoryStore(),
		objects: objectstore.NewMemoryStore(),
		public:  NewMemoryPublicStore(),
		now:     time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC),
	}
	f.service = NewService(Deps{
		Store:  f.store,
		Assets: f.objects,
		Public: f.public,
		Origin: mustOrigin(t),
		Logger: zap.NewNop(),
		Now:    func() time.Time { return f.now },
	})
	return f
}

// mustOrigin 返回测试用的发布地址派生入口。
func mustOrigin(t *testing.T) PublicOrigin {
	t.Helper()
	origin, err := NewPublicOrigin(testAssetsOrigin, testPageOrigin)
	if err != nil {
		t.Fatalf("构造发布地址失败: %v", err)
	}
	return origin
}

// advance 把夹具的时钟往前推，用来让"两次操作的时间不同"这类断言有意义。
func (f *fixture) advance(d time.Duration) { f.now = f.now.Add(d) }

// createProject 建一个工程，断言失败即终止用例。
func (f *fixture) createProject(t *testing.T, name string) Project {
	t.Helper()
	project, err := f.service.CreateProject(context.Background(), testOwner, name, "")
	if err != nil {
		t.Fatalf("建工程失败: %v", err)
	}
	return project
}

// saveDraft 写一次草稿。
func (f *fixture) saveDraft(t *testing.T, projectID string, content string) {
	t.Helper()
	if _, err := f.service.SaveDraft(context.Background(), testOwner, projectID, Document(content)); err != nil {
		t.Fatalf("保存草稿失败: %v", err)
	}
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

// uploadAsset 走一遍资产直传：签发 → 把字节写进假存储（扮演浏览器）→ 提交。
//
// **服务端在整条路径上不接触字节**，因此这一步里的 Put 不是"帮服务端省一步"，
// 而是替代了浏览器的那个角色（见 docs/design/objectstore/README.md）。
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
	f.objects.Put(credential.Key, data)
	return f.service.CommitAssetUpload(ctx, subjectID, projectID, assetID, declaredType, ContentDigest(data), filename)
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

// artifact 走一次校验并取回改写好的产物。
func (f *fixture) artifact(t *testing.T, projectID, content string) Document {
	t.Helper()
	artifact, report, err := f.service.validateDocument(context.Background(), projectID, Document(content))
	if err != nil {
		t.Fatalf("校验出错（期望是一个结论）: %v", err)
	}
	if len(report.Problems) > 0 {
		t.Fatalf("期望通过校验，实际被拒: %v", report.Messages())
	}
	return artifact
}

// report 走一次校验并取回结论。
func (f *fixture) report(t *testing.T, subjectID, projectID, content string) Report {
	t.Helper()
	report, err := f.service.ValidateContent(context.Background(), subjectID, projectID, Document(content))
	if err != nil {
		t.Fatalf("校验出错（期望是一个结论）: %v", err)
	}
	return report
}

// problems 把结论拼成一段文本，供断言"错误信息里有没有这个标识/位置"。
func problems(report Report) string { return strings.Join(report.Messages(), "; ") }
