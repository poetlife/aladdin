//go:build e2e

package e2e

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	galaxyv1 "github.com/poetlife/aladdin/api/gen/aladdin/galaxy/v1"
	"github.com/poetlife/aladdin/api/gen/aladdin/galaxy/v1/galaxyv1connect"
	"github.com/poetlife/aladdin/internal/galaxy"
	"github.com/poetlife/aladdin/internal/rbac"
)

// 本文件端到端验证创作链路：发布在**公开匿名**的地址上可达、归属不可绕过、
// 权限码是唯一准入、以及发布物只引用本工程资产库里的资产。
//
// 与单元测试的分工：internal/galaxy 的用例验证规则本身，这里验证规则在真实传输、
// 真实持久化与真实 HTTP 入口上的**接入**——尤其"不带任何凭证能取到产物"这条，
// 它只有走一遍真的 HTTP 请求才能回答。

// gifBytes 是另一段"看起来像图片"的字节。
//
// **类型不再由字节决定**：直传之后服务端看不到字节，媒体类型由上传方声明
// （见 docs/design/objectstore/README.md）。这些字节只是"客户端传上去的那份内容"，
// 形状在断言里唯一有用的一处是"搬过去的正是那一份"。
var gifBytes = append([]byte("GIF89a"), make([]byte, 64)...)

func connectGalaxy(t *testing.T, h harness, token string) galaxyv1connect.GalaxyServiceClient {
	t.Helper()
	httpClient := &http.Client{
		Timeout:   5 * time.Second,
		Transport: &headerTransport{base: http.DefaultTransport, token: token},
	}
	return galaxyv1connect.NewGalaxyServiceClient(httpClient, "http://"+h.address)
}

// uploadAssetOverRPC 走一遍资产直传：签发 → 把字节写进假存储（这一步扮演浏览器）
// → 提交。
func uploadAssetOverRPC(t *testing.T, h harness, client galaxyv1connect.GalaxyServiceClient, projectID, contentType string, data []byte) *galaxyv1.Asset {
	t.Helper()
	ctx := context.Background()
	begin, err := client.BeginAssetUpload(ctx, connect.NewRequest(&galaxyv1.BeginAssetUploadRequest{
		ProjectId:   projectID,
		ContentType: contentType,
		SizeBytes:   uint64(len(data)),
	}))
	if err != nil {
		t.Fatalf("签发资产直传失败: %v", err)
	}
	h.objects.Put(begin.Msg.GetUpload().GetKey(), data)

	commit, err := client.CommitAssetUpload(ctx, connect.NewRequest(&galaxyv1.CommitAssetUploadRequest{
		ProjectId:   projectID,
		AssetId:     begin.Msg.GetAssetId(),
		ContentType: contentType,
		Digest:      sha256Hex(data),
		Filename:    "a.png",
	}))
	if err != nil {
		t.Fatalf("提交资产失败: %v", err)
	}
	return commit.Msg.GetAsset()
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// publishProject 保存草稿、保存版本并发布，返回发布地址。
func publishProject(t *testing.T, client galaxyv1connect.GalaxyServiceClient, projectID, content string) string {
	t.Helper()
	ctx := context.Background()
	if _, err := client.SaveDraft(ctx, connect.NewRequest(&galaxyv1.SaveDraftRequest{
		ProjectId: projectID, Content: content,
	})); err != nil {
		t.Fatalf("保存草稿失败: %v", err)
	}
	version, err := client.SaveVersion(ctx, connect.NewRequest(&galaxyv1.SaveVersionRequest{ProjectId: projectID}))
	if err != nil {
		t.Fatalf("保存版本失败: %v", err)
	}
	published, err := client.Publish(ctx, connect.NewRequest(&galaxyv1.PublishRequest{
		ProjectId: projectID, VersionId: version.Msg.GetVersion().GetId(),
	}))
	if err != nil {
		t.Fatalf("发布失败: %v", err)
	}
	return published.Msg.GetProject().GetPublishedUrl()
}

// fetchPublishAddress 获取发布地址的内容（**不带任何凭证**）。
//
// 发布地址的主机名是**发布域**，而它在测试里没有 DNS：请求因此打在同一台监听器
// 上、保留路径。生产上发布域经 SNI 分流到同一个服务端，路径与这里一致。
func fetchPublishAddress(t *testing.T, h harness, publishedURL string) (int, string, http.Header) {
	t.Helper()
	parsed, err := url.Parse(publishedURL)
	if err != nil {
		t.Fatalf("发布地址不是合法 URL: %v", err)
	}
	resp, err := http.Get("http://" + h.address + parsed.Path)
	if err != nil {
		t.Fatalf("请求发布地址失败: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("读取响应失败: %v", err)
	}
	return resp.StatusCode, string(body), resp.Header
}

// **发布即公开**：不带任何凭证的请求能取到产物，而产物只引用本工程资产库里的
// 资产（占位符已经换成公开区地址），安全边界由内容安全策略响应头承担。
func TestGalaxyPublishedPageIsPubliclyReachable(t *testing.T) {
	h := startServer(t, rbac.RoleGalaxyAuthor, testScope)
	client := connectGalaxy(t, h, testToken)
	ctx := context.Background()

	project, err := client.CreateProject(ctx, connect.NewRequest(&galaxyv1.CreateProjectRequest{Name: "我的页面"}))
	if err != nil {
		t.Fatalf("建工程失败: %v", err)
	}
	projectID := project.Msg.GetProject().GetId()

	asset := uploadAssetOverRPC(t, h, client, projectID, "image/png", pngBytes)
	content := `<!doctype html><p>你好</p><img src="asset://` + asset.GetId() + `">`
	address := publishProject(t, client, projectID, content)
	if !strings.HasPrefix(address, h.publishBase+galaxy.PublicPathPrefix) {
		t.Fatalf("发布地址 = %q，期望落在发布域的前缀下", address)
	}

	// **不带任何凭证**取一次。
	status, body, header := fetchPublishAddress(t, h, address)
	if status != http.StatusOK {
		t.Fatalf("未带凭证请求发布地址 = %d，期望 200", status)
	}
	// 产物 = 用户正文 + 占位符被替换：正文原样在，占位符不在了。
	if !strings.Contains(body, "<p>你好</p>") {
		t.Error("产物里没有用户写的正文")
	}
	if strings.Contains(body, galaxy.PlaceholderScheme) {
		t.Error("产物里仍留着未改写的占位符")
	}
	// 公开区是同一个桶里的一个前缀：地址由键规则给出。
	wantAddress := h.bucket + "/" + galaxy.ReleaseObjectKey(sha256Hex(pngBytes))
	if !strings.Contains(body, wantAddress) {
		t.Errorf("产物里没有公开区地址 %q", wantAddress)
	}
	// 安全边界在响应头里：脚本发不出请求，媒体只能来自那个桶。
	policy := header.Get(galaxy.CSPHeaderName)
	if policy == "" {
		t.Fatal("响应里没有内容安全策略")
	}
	if !strings.Contains(policy, "connect-src 'none'") {
		t.Errorf("策略没有挡住脚本发请求:\n%s", policy)
	}
	// 策略里的来源只到主机——公开区与私有区共用它，区分靠对象权限。
	if !strings.Contains(policy, "img-src "+h.bucket) {
		t.Errorf("策略允许的来源不是发布物素材所在的桶:\n%s", policy)
	}
	if policyLine := header.Get("X-Content-Type-Options"); policyLine != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q，期望 nosniff", policyLine)
	}

	// 发布物**不含主体信息**：它是公开匿名的。
	if strings.Contains(body, testSubject) {
		t.Error("产物里出现了主体标识")
	}
}

// 未发布与撤回之后，发布地址返回**不存在**，而不是空页。
//
// 而且它与"标识没被猜中"是同一个结论：区分它们等于告诉猜地址的人"这个标识是真的"。
func TestGalaxyUnpublishedAndMissingAreTheSameNegative(t *testing.T) {
	h := startServer(t, rbac.RoleGalaxyAuthor, testScope)
	client := connectGalaxy(t, h, testToken)
	ctx := context.Background()

	project, err := client.CreateProject(ctx, connect.NewRequest(&galaxyv1.CreateProjectRequest{Name: "工程"}))
	if err != nil {
		t.Fatalf("建工程失败: %v", err)
	}
	projectID := project.Msg.GetProject().GetId()

	// 还没发布。
	unpublished := h.publishBase + galaxy.PublicPathPrefix + projectID
	status, _, _ := fetchPublishAddress(t, h, unpublished)
	if status != http.StatusNotFound {
		t.Errorf("未发布的地址 = %d，期望 404", status)
	}
	// 标识没被猜中：同一个结论。
	missing := h.publishBase + galaxy.PublicPathPrefix + "prj_没有这个工程"
	if status, _, _ := fetchPublishAddress(t, h, missing); status != http.StatusNotFound {
		t.Errorf("不存在的地址 = %d，期望 404", status)
	}

	address := publishProject(t, client, projectID, `<p>发布</p>`)
	if status, _, _ := fetchPublishAddress(t, h, address); status != http.StatusOK {
		t.Fatalf("发布之后 = %d，期望 200", status)
	}

	// 撤回**立刻**生效：下一次请求就不可达，不依赖缓存过期。
	if _, err := client.Unpublish(ctx, connect.NewRequest(&galaxyv1.UnpublishRequest{ProjectId: projectID})); err != nil {
		t.Fatalf("撤回失败: %v", err)
	}
	if status, _, _ := fetchPublishAddress(t, h, address); status != http.StatusNotFound {
		t.Errorf("撤回之后 = %d，期望 404", status)
	}
}

// **改草稿不改已发布的页面**：发布的是版本，不是草稿。
//
// 这是"版本不可变"在对外可见性上的落点，也是"发布过的页面内容变了"这件事不会
// 发生的原因。
func TestGalaxyDraftChangeDoesNotAffectPublishedPage(t *testing.T) {
	h := startServer(t, rbac.RoleGalaxyAuthor, testScope)
	client := connectGalaxy(t, h, testToken)
	ctx := context.Background()

	project, err := client.CreateProject(ctx, connect.NewRequest(&galaxyv1.CreateProjectRequest{Name: "工程"}))
	if err != nil {
		t.Fatalf("建工程失败: %v", err)
	}
	projectID := project.Msg.GetProject().GetId()
	address := publishProject(t, client, projectID, `<p>发布时那一刻</p>`)

	// 改草稿，保存成第二个版本，但不发布。
	if _, err := client.SaveDraft(ctx, connect.NewRequest(&galaxyv1.SaveDraftRequest{
		ProjectId: projectID, Content: `<p>改过之后</p>`,
	})); err != nil {
		t.Fatalf("改草稿失败: %v", err)
	}
	if _, err := client.SaveVersion(ctx, connect.NewRequest(&galaxyv1.SaveVersionRequest{ProjectId: projectID})); err != nil {
		t.Fatalf("保存版本失败: %v", err)
	}

	_, body, _ := fetchPublishAddress(t, h, address)
	if strings.Contains(body, "改过之后") {
		t.Error("改草稿之后发布地址上的内容变了——发布的是版本，不是草稿")
	}
	if !strings.Contains(body, "发布时那一刻") {
		t.Error("发布地址上的内容不是发布时那一刻的正文")
	}
}

// **归属不可绕过**：另一个主体的凭证读、改、发布别人的工程一律被拒，且拒绝的
// 结论与"工程不存在"完全一致。
func TestGalaxyOwnershipCannotBeBypassed(t *testing.T) {
	h := startServer(t, rbac.RoleGalaxyAuthor, testScope)
	owner := connectGalaxy(t, h, testToken)
	ctx := context.Background()

	project, err := owner.CreateProject(ctx, connect.NewRequest(&galaxyv1.CreateProjectRequest{Name: "别人的工程"}))
	if err != nil {
		t.Fatalf("建工程失败: %v", err)
	}
	projectID := project.Msg.GetProject().GetId()

	// 注入第二个主体，并给它同一个角色：它能创作，但只能碰自己的东西。
	const otherToken = "e2e-other-token"
	const otherSubject = "e2e-other-user"
	h.srv.Authenticator().Add(otherToken, rbac.Subject{
		ID: otherSubject, Type: rbac.SubjectTypeUser, DefaultScope: testScope,
	})
	if err := h.srv.Store().PutSubject(ctx, rbac.Subject{
		ID: otherSubject, Type: rbac.SubjectTypeUser, DefaultScope: testScope,
	}); err != nil {
		t.Fatalf("注入第二个主体失败: %v", err)
	}
	if err := h.srv.Store().Bind(ctx, rbac.RoleBinding{
		SubjectID: otherSubject, RoleID: rbac.RoleGalaxyAuthor, Scope: testScope,
	}); err != nil {
		t.Fatalf("注入第二个绑定失败: %v", err)
	}
	intruder := connectGalaxy(t, h, otherToken)

	cases := []struct {
		name string
		call func() error
	}{
		{"读工程", func() error {
			_, err := intruder.GetProject(ctx, connect.NewRequest(&galaxyv1.GetProjectRequest{ProjectId: projectID}))
			return err
		}},
		{"改元数据", func() error {
			_, err := intruder.UpdateProject(ctx, connect.NewRequest(&galaxyv1.UpdateProjectRequest{
				ProjectId: projectID, Name: "改掉",
			}))
			return err
		}},
		{"保存草稿", func() error {
			_, err := intruder.SaveDraft(ctx, connect.NewRequest(&galaxyv1.SaveDraftRequest{
				ProjectId: projectID, Content: "<p>覆盖</p>",
			}))
			return err
		}},
		{"发布", func() error {
			_, err := intruder.Publish(ctx, connect.NewRequest(&galaxyv1.PublishRequest{
				ProjectId: projectID, VersionId: "ver_随便",
			}))
			return err
		}},
		{"列资产", func() error {
			_, err := intruder.ListAssets(ctx, connect.NewRequest(&galaxyv1.ListAssetsRequest{ProjectId: projectID}))
			return err
		}},
		{"删工程", func() error {
			_, err := intruder.DeleteProject(ctx, connect.NewRequest(&galaxyv1.DeleteProjectRequest{ProjectId: projectID}))
			return err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.call()
			if connect.CodeOf(err) != connect.CodeNotFound {
				t.Errorf("err = %v，期望 NotFound（与\"工程不存在\"同一个结论）", err)
			}
		})
	}

	// 反过来：它自己的工程列表是空的，而不是被拒——"没有数据"与"没有权限"是两件事。
	list, err := intruder.ListProjects(ctx, connect.NewRequest(&galaxyv1.ListProjectsRequest{}))
	if err != nil {
		t.Fatalf("列出自己的工程失败: %v", err)
	}
	if len(list.Msg.GetProjects()) != 0 {
		t.Errorf("工程数 = %d，期望空集", len(list.Msg.GetProjects()))
	}
}

// **权限码是唯一准入**：没有 galaxy.* 的角色调用创作方法被拒，且拒绝类别是
// "权限不足"，不是"未认证"——后者会把一次权限配置错误放大成一次登录风暴。
func TestGalaxyPermissionGateIsEnforced(t *testing.T) {
	h := startServer(t, rbac.RoleViewer, testScope)
	client := connectGalaxy(t, h, testToken)
	ctx := context.Background()

	_, err := client.ListProjects(ctx, connect.NewRequest(&galaxyv1.ListProjectsRequest{}))
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("无 galaxy 权限的调用 = %v，期望 PermissionDenied", err)
	}

	// 能力下发只要求已认证：只读用户也要能知道"这个部署有没有发布功能"，
	// 否则界面无法决定渲染什么。
	if _, err := client.GetCapabilities(ctx, connect.NewRequest(&galaxyv1.GetCapabilitiesRequest{})); err != nil {
		t.Errorf("读取能力失败: %v", err)
	}
}

// **只引用本工程资产库里的资产**：正文里出现外部资源引用时发布被拒，且指出那一处。
func TestGalaxyRejectsExternalResourceReferences(t *testing.T) {
	h := startServer(t, rbac.RoleGalaxyAuthor, testScope)
	client := connectGalaxy(t, h, testToken)
	ctx := context.Background()

	project, err := client.CreateProject(ctx, connect.NewRequest(&galaxyv1.CreateProjectRequest{Name: "工程"}))
	if err != nil {
		t.Fatalf("建工程失败: %v", err)
	}
	projectID := project.Msg.GetProject().GetId()

	// 编辑器那条入口拿回的是问题清单，而不是一个失败。
	external := `<img src="https://evil.example.com/a.png">`
	report, err := client.ValidateContent(ctx, connect.NewRequest(&galaxyv1.ValidateContentRequest{
		ProjectId: projectID, Content: external,
	}))
	if err != nil {
		t.Fatalf("校验失败: %v", err)
	}
	if len(report.Msg.GetProblems()) == 0 {
		t.Fatal("外部引用没有产生任何问题")
	}
	problems := ""
	for _, problem := range report.Msg.GetProblems() {
		problems += problem.GetMessage() + "; "
	}
	if !strings.Contains(problems, "https://evil.example.com/a.png") {
		t.Errorf("问题 %q 没有指出那一处", problems)
	}

	// 发布那条入口用同一个结论拒绝。
	if _, err := client.SaveDraft(ctx, connect.NewRequest(&galaxyv1.SaveDraftRequest{
		ProjectId: projectID, Content: external,
	})); err != nil {
		t.Fatalf("保存草稿失败: %v", err)
	}
	version, err := client.SaveVersion(ctx, connect.NewRequest(&galaxyv1.SaveVersionRequest{ProjectId: projectID}))
	if err != nil {
		t.Fatalf("保存版本失败: %v", err)
	}
	_, err = client.Publish(ctx, connect.NewRequest(&galaxyv1.PublishRequest{
		ProjectId: projectID, VersionId: version.Msg.GetVersion().GetId(),
	}))
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("发布一个含外部引用的版本 = %v，期望 InvalidArgument", err)
	}
	if !strings.Contains(err.Error(), "https://evil.example.com/a.png") {
		t.Errorf("发布错误 %q 没有指出那一处", err)
	}

	// 导航链接不受此限：外链是用户的意图，不是资源引用。
	if _, err := client.SaveDraft(ctx, connect.NewRequest(&galaxyv1.SaveDraftRequest{
		ProjectId: projectID, Content: `<a href="https://example.com">去看看</a>`,
	})); err != nil {
		t.Fatalf("保存草稿失败: %v", err)
	}
	ok, err := client.SaveVersion(ctx, connect.NewRequest(&galaxyv1.SaveVersionRequest{ProjectId: projectID}))
	if err != nil {
		t.Fatalf("保存版本失败: %v", err)
	}
	if _, err := client.Publish(ctx, connect.NewRequest(&galaxyv1.PublishRequest{
		ProjectId: projectID, VersionId: ok.Msg.GetVersion().GetId(),
	})); err != nil {
		t.Errorf("一个只有外部链接的页面发布失败: %v", err)
	}
}

// **只上架被引用的资产**：资产库里有几个，公开区只多出被引用的那些。
func TestGalaxyPromotesOnlyReferencedAssets(t *testing.T) {
	h := startServer(t, rbac.RoleGalaxyAuthor, testScope)
	client := connectGalaxy(t, h, testToken)
	ctx := context.Background()

	project, err := client.CreateProject(ctx, connect.NewRequest(&galaxyv1.CreateProjectRequest{Name: "工程"}))
	if err != nil {
		t.Fatalf("建工程失败: %v", err)
	}
	projectID := project.Msg.GetProject().GetId()

	used := uploadAssetOverRPC(t, h, client, projectID, "image/png", pngBytes)
	uploadAssetOverRPC(t, h, client, projectID, "image/gif", gifBytes)
	publishProject(t, client, projectID, `<img src="asset://`+used.GetId()+`">`)

	if h.public.Count() != 1 {
		t.Fatalf("公开区对象数 = %d，期望 1（只上架被引用的）", h.public.Count())
	}
	// 键是公开区里的**完整**对象键：公开区与私有区在同一个桶里，区分靠这段前缀。
	key := galaxy.ReleaseObjectKey(sha256Hex(pngBytes))
	if keys := h.public.Keys(); len(keys) != 1 || keys[0] != key {
		t.Errorf("公开区的对象键 = %v，期望只含被引用的那一个", keys)
	}
	// 上架的字节与私有区那份一致：直传之后服务端不接触字节，上架是唯一一次读回。
	promoted, err := h.public.Object(key)
	if err != nil {
		t.Fatalf("读取公开区对象失败: %v", err)
	}
	if string(promoted) != string(pngBytes) {
		t.Error("公开区里的字节与私有区的不一致")
	}
}
