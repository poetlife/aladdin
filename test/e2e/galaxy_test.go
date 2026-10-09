//go:build e2e

package e2e

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	galaxyv1 "github.com/poetlife/aladdin/api/gen/aladdin/galaxy/v1"
	"github.com/poetlife/aladdin/api/gen/aladdin/galaxy/v1/galaxyv1connect"
	"github.com/poetlife/aladdin/internal/galaxy"
	"github.com/poetlife/aladdin/internal/observability"
	"github.com/poetlife/aladdin/internal/rbac"
)

// 本文件端到端验证创作链路：发布在**公开匿名**的地址上可达、一文件一地址、
// 归属不可绕过、权限码是唯一准入、以及发布物只引用本文件组里的条目。
//
// 与单元测试的分工：internal/galaxy 的用例验证规则本身，这里验证规则在真实传输、
// 真实持久化与真实 HTTP 入口上的**接入**——尤其"不带任何凭证能取到某一页"这条，
// 它只有走一遍真的 HTTP 请求才能回答。

// gifBytes 是另一段"看起来像图片"的字节。
//
// **类型不由字节决定**：直传之后服务端看不到字节，媒体类型由上传方声明
// （见 docs/design/objectstore/README.md）。这些字节只是"客户端传上去的那份内容"，
// 形状在断言里唯一有用的一处是"搬过去的正是那一份"。
var gifBytes = append([]byte("GIF89a"), make([]byte, 64)...)

func connectGalaxy(t *testing.T, h harness, token string) galaxyv1connect.GalaxyServiceClient {
	t.Helper()
	return connectGalaxyAs(t, h, token, "")
}

// connectGalaxyAs 与 connectGalaxy 相同，但**带上上报端标识**（`web` / `cli`）。
//
// 生产里前端与命令行各注入一次（见 web/src/api/transport.ts 与 pkg/client），而
// 服务端用它做两件事：请求留痕里记下是哪一端，以及草稿快照的来源。测后者只有真
// 发一个带这个头的请求才算验过。
func connectGalaxyAs(t *testing.T, h harness, token, client string) galaxyv1connect.GalaxyServiceClient {
	t.Helper()
	httpClient := &http.Client{
		Timeout:   5 * time.Second,
		Transport: &headerTransport{base: http.DefaultTransport, token: token, client: client},
	}
	return galaxyv1connect.NewGalaxyServiceClient(httpClient, "http://"+h.address)
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// pushContentOverRPC 走一遍内容对象的直传：签发 → 把字节写进假存储（这一步扮演
// 客户端）→ 提交，并返回一条文本条目。
func pushContentOverRPC(t *testing.T, h harness, client galaxyv1connect.GalaxyServiceClient, projectID, entryPath, content string) *galaxyv1.FileEntry {
	t.Helper()
	ctx := context.Background()
	data := []byte(content)
	digest := galaxy.ContentDigest(data)

	begin, err := client.BeginContentUpload(ctx, connect.NewRequest(&galaxyv1.BeginContentUploadRequest{
		ProjectId: projectID,
		Digest:    digest,
		SizeBytes: uint64(len(data)),
	}))
	if err != nil {
		t.Fatalf("签发内容对象直传失败: %v", err)
	}
	if !begin.Msg.GetAlreadyExists() {
		h.objects.SimulateUpload(begin.Msg.GetUpload().GetKey(), data)
		if _, err := client.CommitContentUpload(ctx, connect.NewRequest(&galaxyv1.CommitContentUploadRequest{
			ProjectId: projectID,
			Digest:    digest,
		})); err != nil {
			t.Fatalf("提交内容对象失败: %v", err)
		}
	}
	return &galaxyv1.FileEntry{Path: entryPath, Source: &galaxyv1.FileEntry_Digest{Digest: digest}}
}

// uploadAssetOverRPC 走一遍资产直传，并返回那个资产（**不进任何清单**）。
//
// 它给"资产在库里但没被任何文件组引用"这类用例用：上传了但没被引用的资产照常
// 存在，只是不进产物、也不被上架。
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
	h.objects.SimulateUpload(begin.Msg.GetUpload().GetKey(), data)

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

// pushAssetOverRPC 上传一个资产并把它作为一条资产条目放进文件组。
func pushAssetOverRPC(t *testing.T, h harness, client galaxyv1connect.GalaxyServiceClient, projectID, entryPath, contentType string, data []byte) *galaxyv1.FileEntry {
	t.Helper()
	asset := uploadAssetOverRPC(t, h, client, projectID, contentType, data)
	return &galaxyv1.FileEntry{
		Path:   entryPath,
		Source: &galaxyv1.FileEntry_AssetId{AssetId: asset.GetId()},
	}
}

// pushDraft 整组替换**站点槽**的草稿。
func pushDraft(t *testing.T, client galaxyv1connect.GalaxyServiceClient, projectID string, entries ...*galaxyv1.FileEntry) {
	t.Helper()
	pushDraftSlot(t, client, projectID, galaxyv1.ContentSlot_CONTENT_SLOT_SITE, entries...)
}

// pushDraftSlot 整组替换某一个内容槽的草稿。
func pushDraftSlot(t *testing.T, client galaxyv1connect.GalaxyServiceClient, projectID string, slot galaxyv1.ContentSlot, entries ...*galaxyv1.FileEntry) {
	t.Helper()
	if _, err := client.PushDraft(context.Background(), connect.NewRequest(&galaxyv1.PushDraftRequest{
		ProjectId: projectID,
		Entries:   entries,
		Slot:      slot,
	})); err != nil {
		t.Fatalf("推送草稿失败: %v", err)
	}
}

// createProject 建一个**只启用一个内容槽**的工程。
func createProject(t *testing.T, client galaxyv1connect.GalaxyServiceClient, name string, slot galaxyv1.ContentSlot) string {
	t.Helper()
	project, err := client.CreateProject(context.Background(), connect.NewRequest(&galaxyv1.CreateProjectRequest{
		Name: name, Slots: []galaxyv1.ContentSlot{slot},
	}))
	if err != nil {
		t.Fatalf("建工程失败: %v", err)
	}
	return project.Msg.GetProject().GetId()
}

// publishDraft 把**站点槽**的草稿存成版本并发布，返回**分享地址**。
func publishDraft(t *testing.T, client galaxyv1connect.GalaxyServiceClient, projectID string) string {
	t.Helper()
	return publishDraftSlot(t, client, projectID, galaxyv1.ContentSlot_CONTENT_SLOT_SITE)
}

// publishDraftSlot 把某一个内容槽的草稿存成版本并发布，返回该槽的**分享地址**。
func publishDraftSlot(t *testing.T, client galaxyv1connect.GalaxyServiceClient, projectID string, slot galaxyv1.ContentSlot) string {
	t.Helper()
	ctx := context.Background()
	version, err := client.SaveVersion(ctx, connect.NewRequest(&galaxyv1.SaveVersionRequest{
		ProjectId: projectID, Slot: slot,
	}))
	if err != nil {
		t.Fatalf("保存版本失败: %v", err)
	}
	published, err := client.Publish(ctx, connect.NewRequest(&galaxyv1.PublishRequest{
		ProjectId: projectID, VersionId: version.Msg.GetVersion().GetId(), Slot: slot,
	}))
	if err != nil {
		t.Fatalf("发布失败: %v", err)
	}
	return published.Msg.GetPublication().GetUrl()
}

// siteRoot 返回一条槽根地址的**带结尾斜杠**形式（只保留路径）。
//
// 内容地址由服务端算出来时**不带**结尾斜杠（见 galaxy.PublicOrigin.ContentURL），而
// 站点根是它带斜杠的形式：页内相对地址按文档所在的目录解析，不带斜杠的槽根会让
// 浏览器退一层目录去取 `/g/style.css`。因此入口页本身要用这个形式取（见
// internal/server/galaxy_public.go 的 slotRootTarget）。
//
// 只保留路径是因为**重定向的目标就是一条路径**：服务端不把发布域写进 `Location`
// ——那个域前面还隔着一层 nginx，绝对化会把不该出现的端口带出去。取值与它逐字比较，
// 因此这里必须是路径形式。
func siteRoot(address string) string {
	parsed, err := url.Parse(address)
	if err != nil {
		return strings.TrimSuffix(address, "/") + "/"
	}
	return strings.TrimSuffix(parsed.Path, "/") + "/"
}

// pathOf 返回一条地址的路径部分（分享地址与内容地址共用它比较）。
func pathOf(address string) string {
	parsed, err := url.Parse(address)
	if err != nil {
		return address
	}
	return parsed.Path
}

// resolveSharedPage 走一次**匿名**的解析调用：主站壳打开一条分享地址时要问的那一跳。
//
// 不带任何凭证——访客打开一条分享地址时没有会话，而"分享给没登录的人"正是这个入口
// 唯一的用途（见 docs/design/rbac/server-permissions.md 的公开方法白名单）。传的是
// **路径**（浏览器的 pathname 就是编码形态），槽的判定在服务端。
func resolveSharedPage(t *testing.T, h harness, shareAddress string) string {
	t.Helper()
	parsed, err := url.Parse(shareAddress)
	if err != nil {
		t.Fatalf("分享地址不是合法 URL: %v", err)
	}
	anonymous := connectGalaxy(t, h, "")
	resp, err := anonymous.ResolveSharedPage(context.Background(), connect.NewRequest(&galaxyv1.ResolveSharedPageRequest{
		Path: parsed.EscapedPath(),
	}))
	if err != nil {
		t.Fatalf("匿名解析分享地址失败: %v", err)
	}
	return resp.Msg.GetContentUrl()
}

// fetchPublished 取发布地址上的一个路径（**不带任何凭证**）。
//
// 发布地址的主机名是**发布域**，而它在测试里没有 DNS：请求因此打在同一台监听器
// 上、保留路径。生产上发布域经 SNI 分流到同一个服务端，路径与这里一致。
//
// **不跟随重定向**：资产条目的终点是桶，而桶在测试里没有 DNS；跟随它会让"服务端
// 只给一个地址、字节由浏览器直连"这条变成一次失败的下载。
func fetchPublished(t *testing.T, h harness, address, entryPath string, headers map[string]string) (int, string, http.Header) {
	t.Helper()
	parsed, err := url.Parse(address)
	if err != nil {
		t.Fatalf("发布地址不是合法 URL: %v", err)
	}
	if entryPath != "" {
		parsed.Path = strings.TrimSuffix(parsed.Path, "/") + "/" + entryPath
	}
	request, err := http.NewRequest(http.MethodGet, "http://"+h.address+parsed.RequestURI(), nil)
	if err != nil {
		t.Fatalf("构造请求失败: %v", err)
	}
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	client := &http.Client{
		Timeout: 5 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Do(request)
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

// **发布即公开，一文件一地址**：不带任何凭证的请求能取到每一页；文本由服务端在
// 自己的路径上给出（带 ETag 与 no-cache），媒体重定向到公开区。
func TestGalaxyPublishedSiteIsPubliclyReachable(t *testing.T) {
	h := startServer(t, rbac.RoleGalaxyAuthor, testScope)
	client := connectGalaxy(t, h, testToken)

	projectID := createProject(t, client, "我的站点", galaxyv1.ContentSlot_CONTENT_SLOT_SITE)
	const page = "<!doctype html><p>你好</p>"
	pushDraft(t, client, projectID,
		pushContentOverRPC(t, h, client, projectID, "index.html", page),
		pushContentOverRPC(t, h, client, projectID, "guide/one.html", "<p>一</p>"),
		pushAssetOverRPC(t, h, client, projectID, "logo.png", "image/png", pngBytes),
	)
	address := publishDraft(t, client, projectID)
	// **分享出去的地址落在主站上**，不落在发布域：发布域是一处裸沙箱，分享地址由
	// 主站壳包一层跨源沙箱 iframe（见 docs/design/galaxy/publication.md 的"主站壳"）。
	if !strings.HasPrefix(address, h.appBase+galaxy.PublicPathPrefix) {
		t.Fatalf("分享地址 = %q，期望落在主站的前缀下", address)
	}
	// 而匿名解析给出的**内容地址**落在发布域上，与分享地址同路径。
	content := resolveSharedPage(t, h, address)
	if !strings.HasPrefix(content, h.publishBase+galaxy.PublicPathPrefix) {
		t.Fatalf("内容地址 = %q，期望落在发布域的前缀下", content)
	}
	if pathOf(content) != pathOf(address) {
		t.Errorf("内容地址与分享地址路径不同: %q vs %q", content, address)
	}

	// **槽根带结尾斜杠，不带斜杠的先 301 过去**：页内相对地址按文档所在的目录解析，
	// 而站点根是 `/g/<标识>/`——`/g/<标识>` 会让浏览器把 `style.css` 解析成
	// `/g/style.css`，而那一条不在集合里（表现为"发布成功了但样式全丢"）。
	status, _, header := fetchPublished(t, h, address, "", nil)
	if status != http.StatusMovedPermanently {
		t.Fatalf("不带斜杠的槽根 = %d，期望 301", status)
	}
	if got := header.Get("Location"); got != siteRoot(address) {
		t.Errorf("重定向目标 = %q，期望 %q", got, siteRoot(address))
	}
	// 查询串跟着走：分享出去的地址可能带着它，重定向不该把它丢掉。
	if _, _, header := fetchPublished(t, h, address+"?from=share", "", nil); header.Get("Location") != siteRoot(address)+"?from=share" {
		t.Errorf("带查询串的重定向目标 = %q，期望查询串原样跟着", header.Get("Location"))
	}

	// 带斜杠的槽根（`entryPath` 为空）与 `index.html` 是**同一页**。
	for _, entryPath := range []string{"", "index.html"} {
		status, body, header := fetchPublished(t, h, siteRoot(address), entryPath, nil)
		if status != http.StatusOK {
			t.Fatalf("未带凭证请求 %q = %d，期望 200", entryPath, status)
		}
		if !strings.Contains(body, "<p>你好</p>") {
			t.Errorf("%q 的正文不对: %s", entryPath, body)
		}
		// 文本由服务端给出，且是**校验式**的：ETag 是内容摘要、每次向服务端校验。
		if got := header.Get("ETag"); got != `"`+galaxy.ContentDigest([]byte(page))+`"` {
			t.Errorf("%q 的 ETag = %q，期望内容摘要", entryPath, got)
		}
		if got := header.Get("Cache-Control"); got != "no-cache" {
			t.Errorf("%q 的 Cache-Control = %q，期望 no-cache", entryPath, got)
		}
		if got := header.Get("X-Content-Type-Options"); got != "nosniff" {
			t.Errorf("%q 的 X-Content-Type-Options = %q，期望 nosniff", entryPath, got)
		}
		if got := header.Get("Content-Type"); !strings.HasPrefix(got, "text/html") {
			t.Errorf("%q 的 Content-Type = %q，期望 text/html", entryPath, got)
		}
		// 发布物**不含主体信息**：它是公开匿名的。
		if strings.Contains(body, testSubject) {
			t.Error("产物里出现了主体标识")
		}
	}

	// `ETag` 相符时回 304，且**不读对象存储**。
	if status, _, _ := fetchPublished(t, h, address, "index.html", map[string]string{
		"If-None-Match": `"` + galaxy.ContentDigest([]byte(page)) + `"`,
	}); status != http.StatusNotModified {
		t.Errorf("校验命中 = %d，期望 304", status)
	}

	// 嵌套路径也要可达：地址是"一个文件一个地址"，不是"一个工程一个页面"。
	if status, body, _ := fetchPublished(t, h, address, "guide/one.html", nil); status != http.StatusOK || !strings.Contains(body, "<p>一</p>") {
		t.Errorf("guide/one.html = %d / %q", status, body)
	}

	// 资产条目**重定向**到公开区：字节由浏览器直连，服务端不代理。
	status, body, header := fetchPublished(t, h, address, "logo.png", nil)
	if status != http.StatusFound {
		t.Fatalf("资产条目 = %d，期望 302", status)
	}
	wantLocation := h.bucket + "/" + galaxy.ReleaseObjectKey(projectID, sha256Hex(pngBytes), "image/png")
	if got := header.Get("Location"); got != wantLocation {
		t.Errorf("重定向目标 = %q，期望 %q", got, wantLocation)
	}
	// 响应体里只有 Go 标准库那一小段跳转提示，**没有一个字节的资产**。
	if strings.Contains(body, string(pngBytes)) {
		t.Error("重定向的响应体里带了资产字节")
	}

	// 安全边界在响应头里：脚本跑得动，但发不出请求。
	_, _, header = fetchPublished(t, h, address, "index.html", nil)
	policy := header.Get(galaxy.CSPHeaderName)
	if policy == "" {
		t.Fatal("响应里没有内容安全策略")
	}
	if !strings.Contains(policy, "connect-src 'self'") {
		t.Errorf("策略没有把脚本的请求收在同源:\n%s", policy)
	}
	if !strings.Contains(policy, "img-src 'self' "+h.bucket) || !strings.Contains(policy, "font-src 'self' "+h.bucket) {
		t.Errorf("图片与字体的来源必须同时含发布域与桶:\n%s", policy)
	}
	if !strings.Contains(policy, "frame-src 'none'") || !strings.Contains(policy, "base-uri 'none'") {
		t.Errorf("策略缺少两条兜底:\n%s", policy)
	}
	// **只允许主站嵌它**：不同源挡住了脚本读会话，但挡不住别人把发布域嵌进自己的
	// 页面（钓鱼框）。允许的祖先因此不是 `'self'`，而是主站那一个来源。
	if !strings.Contains(policy, "frame-ancestors "+h.appBase) {
		t.Errorf("策略没有把帧祖先收成主站:\n%s", policy)
	}
	if strings.Contains(policy, "frame-ancestors 'self'") {
		t.Error("帧祖先落成了 'self'：那等于允许发布域被任何页面嵌")
	}
}

// **docs 形态**：markdown 渲染成多页、每一页有自己的地址、导航只由 markdown 派生。
// **预览通道**：草稿整组从发布域上一条带短时凭证的地址上给出，于是页面引用的样式、
// 脚本与素材**都能按路径取到**。
//
// 这条只有走一遍真的 HTTP 请求才能回答。此前预览把单份文件塞进 `srcdoc`，那份文档
// 没有自己的地址，`style.css` 这类相对引用一处也解析不了——生产上表现为"预览没有
// 样式、图片全裂"。
func TestGalaxyPreviewServesDraftSiteByPath(t *testing.T) {
	h := startServer(t, rbac.RoleGalaxyAuthor, testScope)
	client := connectGalaxy(t, h, testToken)

	projectID := createProject(t, client, "预览的站点", galaxyv1.ContentSlot_CONTENT_SLOT_SITE)
	pushDraft(t, client, projectID,
		pushContentOverRPC(t, h, client, projectID, "index.html",
			`<link rel="stylesheet" href="style.css"><span>你好</span><img src="img/pov-01.png"><script src="app.js"></script>`),
		pushContentOverRPC(t, h, client, projectID, "style.css", "body{color:#222}"),
		pushContentOverRPC(t, h, client, projectID, "app.js", "console.log(1)"),
		pushAssetOverRPC(t, h, client, projectID, "img/pov-01.png", "image/png", pngBytes),
	)

	preview, err := client.PreviewDraft(context.Background(), connect.NewRequest(&galaxyv1.PreviewDraftRequest{
		ProjectId: projectID,
		Slot:      galaxyv1.ContentSlot_CONTENT_SLOT_SITE,
	}))
	if err != nil {
		t.Fatalf("取预览地址失败: %v", err)
	}
	address := preview.Msg.GetUrl()
	previewPrefix := h.publishBase + galaxy.PublicPathPrefix + galaxy.PreviewPathSegment + "/"
	if !strings.HasPrefix(address, previewPrefix) {
		t.Fatalf("预览地址 = %q，期望落在发布域的预览前缀下", address)
	}
	if !strings.HasSuffix(address, "/index.html") {
		t.Fatalf("预览地址 = %q，期望指向入口文件", address)
	}
	// 站点根：其余文件按它解析——这正是页面里那些相对地址要做的事。
	root := strings.TrimSuffix(address, "index.html")

	// 入口：**不带任何凭证**也取得回来——凭证就在地址里（地址即凭据），而它比发布
	// 多出来的东西只有一条：短时有效。安全头与发布态逐条相同。
	status, body, header := fetchPublished(t, h, address, "", nil)
	if status != http.StatusOK {
		t.Fatalf("取预览入口 = %d，期望 200", status)
	}
	if !strings.Contains(body, "你好") {
		t.Errorf("预览入口的正文不对: %s", body)
	}
	if got := header.Get("Content-Security-Policy"); got == "" {
		t.Error("预览响应没有内容安全策略")
	}
	if got := header.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q，期望 nosniff", got)
	}
	// 草稿会变：同一个路径的字节下一刻就可能不同，因此不进任何缓存。
	if got := header.Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q，期望 no-store", got)
	}

	// 兄弟文件按路径取到，且**类型由扩展名派生**——内容对象在桶上是中性类型，
	// 只有这一条路能让浏览器把它当样式与脚本用。
	status, body, header = fetchPublished(t, h, root, "style.css", nil)
	if status != http.StatusOK || body != "body{color:#222}" {
		t.Fatalf("取 style.css = %d %q，期望 200 与原文", status, body)
	}
	if got := header.Get("Content-Type"); !strings.HasPrefix(got, "text/css") {
		t.Errorf("style.css 的 Content-Type = %q，期望 text/css", got)
	}
	if status, body, _ = fetchPublished(t, h, root, "app.js", nil); status != http.StatusOK || body != "console.log(1)" {
		t.Errorf("取 app.js = %d %q", status, body)
	}

	// 素材条目：重定向到**私有区**的短时地址（预览态不公开任何字节）。
	status, _, header = fetchPublished(t, h, root, "img/pov-01.png", nil)
	if status != http.StatusFound {
		t.Fatalf("取素材 = %d，期望 302", status)
	}
	// `release` 那一段是公开区（见 galaxy.ReleaseObjectKey）；预览的素材不走那条路。
	if location := header.Get("Location"); !strings.Contains(location, "/assets/") ||
		strings.Contains(location, "/release/") {
		t.Errorf("素材的重定向目标 = %q，期望指向私有区", location)
	}

	// 草稿里没有的路径、以及被改过的凭证：都得到与"这一页不存在"相同的否定结论。
	if status, _, _ := fetchPublished(t, h, root, "nope.css", nil); status != http.StatusNotFound {
		t.Errorf("草稿里没有的路径 = %d，期望 404", status)
	}
	tampered := strings.Replace(address, galaxy.PreviewPathSegment+"/", galaxy.PreviewPathSegment+"/x", 1)
	if status, _, _ := fetchPublished(t, h, tampered, "", nil); status != http.StatusNotFound {
		t.Errorf("被改过的凭证 = %d，期望 404", status)
	}

	// **这条通道不改发布态**：还没发布，发布地址仍然是否定的。
	unpublished := h.publishBase + galaxy.PublicPathPrefix + projectID
	if status, _, _ := fetchPublished(t, h, siteRoot(unpublished), "", nil); status != http.StatusNotFound {
		t.Errorf("未发布时发布地址 = %d，期望 404", status)
	}
}

func TestGalaxyDocsSiteIsRenderedPerPage(t *testing.T) {
	h := startServer(t, rbac.RoleGalaxyAuthor, testScope)
	client := connectGalaxy(t, h, testToken)

	projectID := createProject(t, client, "文档站", galaxyv1.ContentSlot_CONTENT_SLOT_DOCS)
	pushDraftSlot(t, client, projectID, galaxyv1.ContentSlot_CONTENT_SLOT_DOCS,
		pushContentOverRPC(t, h, client, projectID, "index.md", "# 首页\n\n看[入门](guide/intro.md)。\n"),
		pushContentOverRPC(t, h, client, projectID, "guide/intro.md", "# 入门\n\n正文。\n"),
		pushContentOverRPC(t, h, client, projectID, "theme.css", "body{color:red}"),
	)
	address := publishDraftSlot(t, client, projectID, galaxyv1.ContentSlot_CONTENT_SLOT_DOCS)

	// 每一章有自己的地址，深链可以直接分享。
	status, body, header := fetchPublished(t, h, address, "guide/intro.html", nil)
	if status != http.StatusOK {
		t.Fatalf("guide/intro.html = %d，期望 200", status)
	}
	if !strings.Contains(body, "入门") {
		t.Errorf("渲染出来的页里没有标题: %s", body)
	}
	// 渲染出来的页是 HTML，而 `.html` 并不在 docs 的**写入**白名单里。
	if got := header.Get("Content-Type"); !strings.HasPrefix(got, "text/html") {
		t.Errorf("Content-Type = %q，期望 text/html", got)
	}
	// 导航里既有首页也有这一章：它由 markdown 文件派生。
	if !strings.Contains(body, "首页") {
		t.Error("导航里没有另一份文档")
	}
	if strings.Contains(body, "theme.css\">theme.css") {
		t.Error("站点文件进了导航")
	}
	// 站点文件按**原路径**进产物，且是原样的。
	if status, css, _ := fetchPublished(t, h, address, "theme.css", nil); status != http.StatusOK || css != "body{color:red}" {
		t.Errorf("theme.css = %d / %q", status, css)
	}

	// 文档槽的槽根同样带结尾斜杠：它下面的相对地址按 `docs/` 那一层解析。
	status, _, header = fetchPublished(t, h, address, "", nil)
	if status != http.StatusMovedPermanently || header.Get("Location") != siteRoot(address) {
		t.Errorf("不带斜杠的文档槽根 = %d / %q，期望 301 到带斜杠的形式", status, header.Get("Location"))
	}
	if status, body, _ := fetchPublished(t, h, siteRoot(address), "", nil); status != http.StatusOK || !strings.Contains(body, "首页") {
		t.Errorf("带斜杠的文档槽根 = %d / %q，期望 200 与首页", status, body)
	}
}

// 未发布、已撤回、标识没被猜中、路径不在集合里：发布地址一律返回**不存在**。
func TestGalaxyNegativeConclusionsAreTheSame(t *testing.T) {
	h := startServer(t, rbac.RoleGalaxyAuthor, testScope)
	client := connectGalaxy(t, h, testToken)
	ctx := context.Background()

	projectID := createProject(t, client, "工程", galaxyv1.ContentSlot_CONTENT_SLOT_SITE)

	// 槽根一律先补斜杠：发布过、没发布过、标识根本不存在——三者**同一个**结果。
	// 否则"重定向还是 404"本身就成了"这个标识是真的"这条信号。
	unpublished := h.publishBase + galaxy.PublicPathPrefix + projectID
	missing := h.publishBase + galaxy.PublicPathPrefix + "prj_没有这个工程"
	for _, absent := range []string{unpublished, missing} {
		status, _, header := fetchPublished(t, h, absent, "", nil)
		// 非 ASCII 的路径在 `Location` 里会被转义（HTTP 头只能是 ASCII），按解码后的
		// 路径比较。
		location, unescapeErr := url.PathUnescape(header.Get("Location"))
		if status != http.StatusMovedPermanently || unescapeErr != nil || location != siteRoot(absent) {
			t.Errorf("%s 的无斜杠形式 = %d / %q，期望 301 到带斜杠的形式", absent, status, header.Get("Location"))
		}
		if status, _, _ := fetchPublished(t, h, siteRoot(absent), "", nil); status != http.StatusNotFound {
			t.Errorf("%s = %d，期望 404", absent, status)
		}
	}

	pushDraft(t, client, projectID, pushContentOverRPC(t, h, client, projectID, "index.html", "<p>发布</p>"))
	address := publishDraft(t, client, projectID)
	if status, _, _ := fetchPublished(t, h, siteRoot(address), "", nil); status != http.StatusOK {
		t.Fatalf("发布之后 = %d，期望 200", status)
	}
	// 路径不在集合里与"未发布"是同一个结论。
	if status, _, _ := fetchPublished(t, h, address, "nope.html", nil); status != http.StatusNotFound {
		t.Errorf("不存在的路径 = %d，期望 404", status)
	}

	// 撤回**立刻**生效：下一次请求就不可达，且**每一条路径都是**。
	if _, err := client.Unpublish(ctx, connect.NewRequest(&galaxyv1.UnpublishRequest{ProjectId: projectID, Slot: galaxyv1.ContentSlot_CONTENT_SLOT_SITE})); err != nil {
		t.Fatalf("撤回失败: %v", err)
	}
	for _, entryPath := range []string{"", "index.html"} {
		if status, _, _ := fetchPublished(t, h, siteRoot(address), entryPath, nil); status != http.StatusNotFound {
			t.Errorf("撤回之后 %q = %d，期望 404", entryPath, status)
		}
	}
}

// 主站壳要问的那一跳：**匿名**、按整条分享路径回答、否定结论只有一个。
//
// 它回答的事实与发布域那条直连入口完全一样（见 docs/design/rbac/server-permissions.md
// 的公开方法白名单），因此这里把"同一个空结果"在 RPC 上也断言一遍——用错误码区分
// 未发布与标识不存在，会给出一个"这个标识是真的"的第二信号。
func TestGalaxySharedPageResolutionIsAnonymousAndUniform(t *testing.T) {
	h := startServer(t, rbac.RoleGalaxyAuthor, testScope)
	client := connectGalaxy(t, h, testToken)
	anonymous := connectGalaxy(t, h, "")
	ctx := context.Background()

	projectID := createProject(t, client, "工程", galaxyv1.ContentSlot_CONTENT_SLOT_SITE)
	pushDraft(t, client, projectID,
		pushContentOverRPC(t, h, client, projectID, "index.html", "<p>你好</p>"),
		pushContentOverRPC(t, h, client, projectID, "guide/one.html", "<p>一</p>"),
	)
	shareAddress := publishDraft(t, client, projectID)

	// 已发布：给出发布域上的**同路径**地址（分享地址在主站，内容地址在发布域）。
	want := h.publishBase + galaxy.PublicPathPrefix + projectID
	if got := resolveSharedPage(t, h, shareAddress); got != want {
		t.Errorf("槽根的内容地址 = %q，期望 %q", got, want)
	}
	// 深链透传一次：分享出去的站内页面落在同一页上。
	if got, wantDeep := resolveSharedPage(t, h, shareAddress+"/guide/one.html"), want+"/guide/one.html"; got != wantDeep {
		t.Errorf("深链的内容地址 = %q，期望 %q", got, wantDeep)
	}

	// 五种情形同一个空结果：**空**而不是错误。
	unpublishedID := createProject(t, client, "没发过", galaxyv1.ContentSlot_CONTENT_SLOT_SITE)
	cases := map[string]string{
		"未发布":    galaxy.PublicPathPrefix + unpublishedID,
		"槽未启用":   galaxy.PublicPathPrefix + projectID + "/docs",
		"工程不存在":  galaxy.PublicPathPrefix + "prj_没有这个工程",
		"路径不在集合": galaxy.PublicPathPrefix + projectID + "/nope.html",
		"不是分享路径": "/galaxy",
	}
	for name, path := range cases {
		resp, err := anonymous.ResolveSharedPage(ctx, connect.NewRequest(&galaxyv1.ResolveSharedPageRequest{Path: path}))
		if err != nil {
			t.Errorf("%s：解析被当成一次错误返回: %v", name, err)
			continue
		}
		if got := resp.Msg.GetContentUrl(); got != "" {
			t.Errorf("%s：内容地址 = %q，期望空（否定结论只有一个）", name, got)
		}
	}

	// 撤回之后同一个路径也落进同一个否定结论。
	if _, err := client.Unpublish(ctx, connect.NewRequest(&galaxyv1.UnpublishRequest{
		ProjectId: projectID, Slot: galaxyv1.ContentSlot_CONTENT_SLOT_SITE,
	})); err != nil {
		t.Fatalf("撤回失败: %v", err)
	}
	if got := resolveSharedPage(t, h, shareAddress); got != "" {
		t.Errorf("撤回之后的内容地址 = %q，期望空", got)
	}
}

// **改草稿不改已发布的站点**：发布的是版本，不是草稿。
func TestGalaxyDraftChangeDoesNotAffectPublishedSite(t *testing.T) {
	h := startServer(t, rbac.RoleGalaxyAuthor, testScope)
	client := connectGalaxy(t, h, testToken)

	projectID := createProject(t, client, "工程", galaxyv1.ContentSlot_CONTENT_SLOT_SITE)
	pushDraft(t, client, projectID, pushContentOverRPC(t, h, client, projectID, "index.html", "<p>发布时那一刻</p>"))
	address := publishDraft(t, client, projectID)

	// 改草稿并存成第二个版本，但不发布。
	pushDraft(t, client, projectID, pushContentOverRPC(t, h, client, projectID, "index.html", "<p>改过之后</p>"))
	if _, err := client.SaveVersion(context.Background(), connect.NewRequest(&galaxyv1.SaveVersionRequest{ProjectId: projectID, Slot: galaxyv1.ContentSlot_CONTENT_SLOT_SITE})); err != nil {
		t.Fatalf("保存版本失败: %v", err)
	}

	_, body, _ := fetchPublished(t, h, siteRoot(address), "", nil)
	if strings.Contains(body, "改过之后") {
		t.Error("改草稿之后发布地址上的内容变了——发布的是版本，不是草稿")
	}
	if !strings.Contains(body, "发布时那一刻") {
		t.Error("发布地址上的内容不是发布那一刻的清单")
	}
}

// **归属不可绕过**：另一个主体的凭证读、改、发布别人的工程一律被拒，且拒绝的
// 结论与"工程不存在"完全一致。
func TestGalaxyOwnershipCannotBeBypassed(t *testing.T) {
	h := startServer(t, rbac.RoleGalaxyAuthor, testScope)
	owner := connectGalaxy(t, h, testToken)
	ctx := context.Background()

	projectID := createProject(t, owner, "别人的工程", galaxyv1.ContentSlot_CONTENT_SLOT_SITE)

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
		{"推送草稿", func() error {
			_, err := intruder.PushDraft(ctx, connect.NewRequest(&galaxyv1.PushDraftRequest{
				ProjectId: projectID,
			}))
			return err
		}},
		{"校验草稿", func() error {
			_, err := intruder.ValidateDraft(ctx, connect.NewRequest(&galaxyv1.ValidateDraftRequest{ProjectId: projectID, Slot: galaxyv1.ContentSlot_CONTENT_SLOT_SITE}))
			return err
		}},
		{"预览草稿", func() error {
			_, err := intruder.PreviewDraft(ctx, connect.NewRequest(&galaxyv1.PreviewDraftRequest{ProjectId: projectID, Slot: galaxyv1.ContentSlot_CONTENT_SLOT_SITE}))
			return err
		}},
		{"签发内容对象", func() error {
			_, err := intruder.BeginContentUpload(ctx, connect.NewRequest(&galaxyv1.BeginContentUploadRequest{
				ProjectId: projectID, Digest: sha256Hex([]byte("x")), SizeBytes: 1,
			}))
			return err
		}},
		{"发布", func() error {
			_, err := intruder.Publish(ctx, connect.NewRequest(&galaxyv1.PublishRequest{
				ProjectId: projectID, VersionId: "ver_随便",
				Slot: galaxyv1.ContentSlot_CONTENT_SLOT_SITE,
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

// **每一处取资源的引用都必须落在本文件组里**：外部地址被拒，且指出那一处；
// 导航链接不受此限。
func TestGalaxyRejectsExternalResourceReferences(t *testing.T) {
	h := startServer(t, rbac.RoleGalaxyAuthor, testScope)
	client := connectGalaxy(t, h, testToken)
	ctx := context.Background()

	projectID := createProject(t, client, "工程", galaxyv1.ContentSlot_CONTENT_SLOT_SITE)

	// 校验那条入口拿回的是问题清单，而不是一个失败。
	const external = `<img src="https://evil.example.com/a.png">`
	pushDraft(t, client, projectID, pushContentOverRPC(t, h, client, projectID, "index.html", external))
	report, err := client.ValidateDraft(ctx, connect.NewRequest(&galaxyv1.ValidateDraftRequest{ProjectId: projectID, Slot: galaxyv1.ContentSlot_CONTENT_SLOT_SITE}))
	if err != nil {
		t.Fatalf("校验失败: %v", err)
	}
	if len(report.Msg.GetProblems()) == 0 {
		t.Fatal("外部引用没有产生任何问题")
	}
	problems := ""
	for _, problem := range report.Msg.GetProblems() {
		problems += problem.GetPath() + " " + problem.GetMessage() + "; "
	}
	if !strings.Contains(problems, "https://evil.example.com/a.png") {
		t.Errorf("问题 %q 没有指出那一处", problems)
	}
	if !strings.Contains(problems, "index.html") {
		t.Errorf("问题 %q 没有指出是哪一份文件", problems)
	}

	// 发布那条入口用同一个结论拒绝。
	version, err := client.SaveVersion(ctx, connect.NewRequest(&galaxyv1.SaveVersionRequest{ProjectId: projectID, Slot: galaxyv1.ContentSlot_CONTENT_SLOT_SITE}))
	if err != nil {
		t.Fatalf("保存版本失败: %v", err)
	}
	_, err = client.Publish(ctx, connect.NewRequest(&galaxyv1.PublishRequest{
		ProjectId: projectID, VersionId: version.Msg.GetVersion().GetId(),
		Slot: galaxyv1.ContentSlot_CONTENT_SLOT_SITE,
	}))
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("发布一个含外部引用的版本 = %v，期望 InvalidArgument", err)
	}
	if !strings.Contains(err.Error(), "https://evil.example.com/a.png") {
		t.Errorf("发布错误 %q 没有指出那一处", err)
	}

	// 导航链接不受此限：外链是用户的意图，不是资源引用。
	pushDraft(t, client, projectID, pushContentOverRPC(t, h, client, projectID, "index.html",
		`<a href="https://example.com">去看看</a>`))
	if _, err := client.SaveVersion(ctx, connect.NewRequest(&galaxyv1.SaveVersionRequest{ProjectId: projectID, Slot: galaxyv1.ContentSlot_CONTENT_SLOT_SITE})); err != nil {
		t.Fatalf("保存版本失败: %v", err)
	}
}

// **只上架被引用的资产**：资产库里有几个，公开区只多出被引用的那些。
func TestGalaxyPromotesOnlyReferencedAssets(t *testing.T) {
	h := startServer(t, rbac.RoleGalaxyAuthor, testScope)
	client := connectGalaxy(t, h, testToken)

	projectID := createProject(t, client, "工程", galaxyv1.ContentSlot_CONTENT_SLOT_SITE)
	// 一个被引用的资产，一个只躺在库里的资产——后者不进文件组，因此不进产物。
	used := uploadAssetOverRPC(t, h, client, projectID, "image/png", pngBytes)
	uploadAssetOverRPC(t, h, client, projectID, "image/gif", gifBytes)
	pushDraft(t, client, projectID,
		pushContentOverRPC(t, h, client, projectID, "index.html", `<img src="asset://`+used.GetId()+`">`),
		&galaxyv1.FileEntry{Path: "used.png", Source: &galaxyv1.FileEntry_AssetId{AssetId: used.GetId()}},
	)
	publishDraft(t, client, projectID)

	if h.public.Count() != 1 {
		t.Fatalf("公开区对象数 = %d，期望 1（只上架被引用的）", h.public.Count())
	}
	// 键是公开区里的**完整**对象键：它按工程切分、含**内容类型**——同一份字节以两种
	// 类型上架是两个对象，而所属工程在键上就看得出来。
	key := galaxy.ReleaseObjectKey(projectID, sha256Hex(pngBytes), "image/png")
	if keys := h.public.Keys(); len(keys) != 1 || keys[0] != key {
		t.Errorf("公开区的对象键 = %v，期望只含被引用的那一个（%s）", keys, key)
	}
	// 上架的字节与私有区那份一致：直传之后服务端不接触字节，上架是让存储自己把
	// 对象复制进公开区。
	promoted, err := h.public.Object(key)
	if err != nil {
		t.Fatalf("读取公开区对象失败: %v", err)
	}
	if string(promoted) != string(pngBytes) {
		t.Error("公开区里的字节与私有区的不一致")
	}
}

// **一套产物一次性生效，撤回之后仍可重发**：撤回 → 重新发布同一版本 → 每一条
// 路径都回到可达，且产物逐字相同。
func TestGalaxyRepublishAfterUnpublish(t *testing.T) {
	h := startServer(t, rbac.RoleGalaxyAuthor, testScope)
	client := connectGalaxy(t, h, testToken)
	ctx := context.Background()

	projectID := createProject(t, client, "工程", galaxyv1.ContentSlot_CONTENT_SLOT_SITE)
	pushDraft(t, client, projectID,
		pushContentOverRPC(t, h, client, projectID, "index.html", "<p>首页</p>"),
		pushContentOverRPC(t, h, client, projectID, "style.css", "body{}"),
	)
	version, err := client.SaveVersion(ctx, connect.NewRequest(&galaxyv1.SaveVersionRequest{ProjectId: projectID, Slot: galaxyv1.ContentSlot_CONTENT_SLOT_SITE}))
	if err != nil {
		t.Fatalf("保存版本失败: %v", err)
	}
	versionID := version.Msg.GetVersion().GetId()

	first, err := client.Publish(ctx, connect.NewRequest(&galaxyv1.PublishRequest{ProjectId: projectID, VersionId: versionID, Slot: galaxyv1.ContentSlot_CONTENT_SLOT_SITE}))
	if err != nil {
		t.Fatalf("发布失败: %v", err)
	}
	address := first.Msg.GetPublication().GetUrl()
	_, firstCSS, _ := fetchPublished(t, h, address, "style.css", nil)

	if _, err := client.Unpublish(ctx, connect.NewRequest(&galaxyv1.UnpublishRequest{ProjectId: projectID, Slot: galaxyv1.ContentSlot_CONTENT_SLOT_SITE})); err != nil {
		t.Fatalf("撤回失败: %v", err)
	}
	if status, _, _ := fetchPublished(t, h, address, "style.css", nil); status != http.StatusNotFound {
		t.Fatalf("撤回之后 = %d，期望 404", status)
	}

	second, err := client.Publish(ctx, connect.NewRequest(&galaxyv1.PublishRequest{ProjectId: projectID, VersionId: versionID, Slot: galaxyv1.ContentSlot_CONTENT_SLOT_SITE}))
	if err != nil {
		t.Fatalf("重新发布失败: %v", err)
	}
	if second.Msg.GetPublication().GetId() != first.Msg.GetPublication().GetId() {
		t.Error("重新发布产生了另一条发布记录")
	}
	_, secondCSS, _ := fetchPublished(t, h, address, "style.css", nil)
	if secondCSS != firstCSS {
		t.Errorf("重新发布的产物不同：%q / %q", secondCSS, firstCSS)
	}
}

// uploadAssetWithMetaOverRPC 走一遍资产直传，并在**提交时**带上说明层元数据。
//
// 与 uploadAssetOverRPC 分开，是因为"一次提交带齐"本身就是要验的一条：它与
// "提交之后再调 UpdateAsset"必须给出同样的结果。
func uploadAssetWithMetaOverRPC(t *testing.T, h harness, client galaxyv1connect.GalaxyServiceClient,
	projectID, contentType, filename, title, notes string, tags []string, data []byte) *galaxyv1.Asset {
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
	h.objects.SimulateUpload(begin.Msg.GetUpload().GetKey(), data)

	commit, err := client.CommitAssetUpload(ctx, connect.NewRequest(&galaxyv1.CommitAssetUploadRequest{
		ProjectId:   projectID,
		AssetId:     begin.Msg.GetAssetId(),
		ContentType: contentType,
		Digest:      sha256Hex(data),
		Filename:    filename,
		Title:       title,
		Tags:        tags,
		Notes:       notes,
	}))
	if err != nil {
		t.Fatalf("提交资产失败: %v", err)
	}
	return commit.Msg.GetAsset()
}

// **资产元数据可改，字节层与已发布的产物不受影响**（issue #24 的验收）。
//
// 一次验四件事：提交时可带初始元数据；标签由服务端归一化；UpdateAsset 只动说明
// 层；改完之后公开区的键与已发布页面的字节逐字不变。
func TestGalaxyAssetMetadataIsEditableWithoutTouchingBytes(t *testing.T) {
	h := startServer(t, rbac.RoleGalaxyAuthor, testScope)
	client := connectGalaxy(t, h, testToken)
	ctx := context.Background()

	projectID := createProject(t, client, "工程", galaxyv1.ContentSlot_CONTENT_SLOT_SITE)
	// 标签故意写成大写并带空白：归一化是这条链路的一部分，不是调用方的事。
	cover := uploadAssetWithMetaOverRPC(t, h, client, projectID, "image/png", "cover.png",
		"首页封面", "给首页用", []string{" Cover ", "HERO"}, pngBytes)
	uploadAssetWithMetaOverRPC(t, h, client, projectID, "image/gif", "other.gif",
		"", "", []string{"cover"}, gifBytes)

	if !slices.Equal(cover.GetTags(), []string{"cover", "hero"}) {
		t.Fatalf("提交带上的标签 = %v，期望归一化之后的 [cover hero]", cover.GetTags())
	}
	if cover.GetTitle() != "首页封面" || cover.GetNotes() != "给首页用" {
		t.Fatalf("提交带上的说明层 = %q / %q", cover.GetTitle(), cover.GetNotes())
	}

	// 引用它并发布：此后一切关于"产物不受影响"的断言都以这一刻为基线。
	pushDraft(t, client, projectID,
		pushContentOverRPC(t, h, client, projectID, "index.html", `<img src="asset://`+cover.GetId()+`">`),
		&galaxyv1.FileEntry{Path: "cover.png", Source: &galaxyv1.FileEntry_AssetId{AssetId: cover.GetId()}},
	)
	address := publishDraft(t, client, projectID)
	status, before, _ := fetchPublished(t, h, address, "index.html", nil)
	if status != http.StatusOK {
		t.Fatalf("发布后取页面 = %d，期望 200", status)
	}
	releaseKeys := h.public.Keys()
	releaseKey := galaxy.ReleaseObjectKey(projectID, sha256Hex(pngBytes), "image/png")
	if len(releaseKeys) != 1 || releaseKeys[0] != releaseKey {
		t.Fatalf("公开区的键 = %v，期望只有 %s", releaseKeys, releaseKey)
	}
	// 上架的字节也留一份基线：改元数据之后它必须逐字不变。
	promotedBefore, err := h.public.Object(releaseKey)
	if err != nil {
		t.Fatalf("读取公开区对象失败: %v", err)
	}

	// 改元数据。返回的那一份必须与提交时的那一份在**字节层**逐字相同。
	updated, err := client.UpdateAsset(ctx, connect.NewRequest(&galaxyv1.UpdateAssetRequest{
		ProjectId: projectID,
		AssetId:   cover.GetId(),
		Title:     "改过的封面",
		Tags:      []string{"New"},
		Notes:     "改过的备注",
	}))
	if err != nil {
		t.Fatalf("改资产元数据失败: %v", err)
	}
	got := updated.Msg.GetAsset()
	if got.GetTitle() != "改过的封面" || got.GetNotes() != "改过的备注" {
		t.Errorf("说明层 = %q / %q，期望与传入的一致", got.GetTitle(), got.GetNotes())
	}
	if !slices.Equal(got.GetTags(), []string{"new"}) {
		t.Errorf("标签 = %v，期望归一化之后的 [new]", got.GetTags())
	}
	// 内容摘要不在接口面上（它是库内的那一层），因此这里比的是接口面上字节层的
	// 全部字段；摘要不变由下面"公开区键与字节不变"那两条钉住。
	if got.GetMediaType() != cover.GetMediaType() ||
		got.GetKind() != cover.GetKind() || got.GetSizeBytes() != cover.GetSizeBytes() ||
		got.GetFilename() != cover.GetFilename() || got.GetUploadedAt() != cover.GetUploadedAt() {
		t.Error("改元数据动了字节层")
	}

	// 已发布的页面与公开区**逐字不变**：说明层不进产物。
	status, after, _ := fetchPublished(t, h, address, "index.html", nil)
	if status != http.StatusOK || after != before {
		t.Errorf("改元数据之后已发布页面变了：%d / %q（原 %q）", status, after, before)
	}
	if keys := h.public.Keys(); !slices.Equal(keys, releaseKeys) {
		t.Errorf("公开区的键变了：%v", keys)
	}
	promotedAfter, err := h.public.Object(releaseKey)
	if err != nil {
		t.Fatalf("改元数据之后公开区对象没了: %v", err)
	}
	if string(promotedAfter) != string(promotedBefore) {
		t.Error("公开区里的字节变了")
	}

	// 按标签筛选：精确匹配、多值取交集；候选是整个工程的标签。
	list := func(tags ...string) *galaxyv1.ListAssetsResponse {
		t.Helper()
		resp, err := client.ListAssets(ctx, connect.NewRequest(&galaxyv1.ListAssetsRequest{
			ProjectId: projectID,
			Tags:      tags,
		}))
		if err != nil {
			t.Fatalf("列资产失败: %v", err)
		}
		return resp.Msg
	}
	if names := assetIDsOf(list("new")); !slices.Equal(names, []string{cover.GetId()}) {
		t.Errorf("带 new 的资产 = %v，期望只有改过的那一个", names)
	}
	if names := assetIDsOf(list("cover")); len(names) != 1 {
		t.Errorf("带 cover 的资产 = %v，期望只有另一个", names)
	}
	if names := assetIDsOf(list("new", "cover")); len(names) != 0 {
		t.Errorf("同时带 new 与 cover 的资产 = %v，期望空集", names)
	}
	if tags := list().GetProjectTags(); !slices.Equal(tags, []string{"cover", "new"}) {
		t.Errorf("工程标签 = %v，期望 [cover new]", tags)
	}

	// 他工程 / 不存在的资产改不了：与"不存在"是同一个结论。
	if _, err := client.UpdateAsset(ctx, connect.NewRequest(&galaxyv1.UpdateAssetRequest{
		ProjectId: projectID,
		AssetId:   "ast_nope",
		Title:     "x",
	})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("改不存在的资产 = %v，期望 NotFound", err)
	}
}

// **草稿历史**：只推不存版本的那条路上，中间过程仍然找得回来；恢复本身也留一条；
// 版本说明可以事后改，而它不影响内容。
//
// 这些断言里只有走一遍真的 RPC 才能回答的部分是：快照的**来源**是不是记对了
// （它取自上报端标识那一个请求头），以及恢复之后草稿与版本的清单是不是真的对上了。
func TestGalaxyDraftHistoryAndVersionDescription(t *testing.T) {
	h := startServer(t, rbac.RoleGalaxyAuthor, testScope)
	// 带上上报端标识：草稿历史里的"来源"正是从它读出来的。
	client := connectGalaxyAs(t, h, testToken, observability.ClientCLI)
	ctx := context.Background()

	projectID := createProject(t, client, "工程", galaxyv1.ContentSlot_CONTENT_SLOT_SITE)
	pushDraft(t, client, projectID, pushContentOverRPC(t, h, client, projectID, "index.html", "<p>第一版</p>"))
	pushDraft(t, client, projectID, pushContentOverRPC(t, h, client, projectID, "index.html", "<p>第二版</p>"))
	pushDraft(t, client, projectID, pushContentOverRPC(t, h, client, projectID, "index.html", "<p>第三版</p>"))

	// 三次推送留下前两次（第一次没有"被替换掉的东西"）。
	history, err := client.ListDraftSnapshots(ctx, connect.NewRequest(&galaxyv1.ListDraftSnapshotsRequest{
		ProjectId: projectID, Slot: galaxyv1.ContentSlot_CONTENT_SLOT_SITE,
	}))
	if err != nil {
		t.Fatalf("读草稿历史失败: %v", err)
	}
	if len(history.Msg.GetSnapshots()) != 2 {
		t.Fatalf("历史 = %d 条，期望 2 条", len(history.Msg.GetSnapshots()))
	}
	newest, oldest := history.Msg.GetSnapshots()[0], history.Msg.GetSnapshots()[1]
	if !strings.HasPrefix(newest.GetId(), "snp_") {
		t.Errorf("快照标识 = %q，期望 snp_ 前缀", newest.GetId())
	}
	// **来源取自上报端标识**：命令行发出的请求带着 x-aladdin-client: cli
	// （见 pkg/client），因此这一条必须是 cli。
	if newest.GetSource() != "cli" {
		t.Errorf("来源 = %q，期望 cli", newest.GetSource())
	}

	// 存一版并带说明，再改说明：**内容一字不动**。
	saved, err := client.SaveVersion(ctx, connect.NewRequest(&galaxyv1.SaveVersionRequest{
		ProjectId: projectID, Slot: galaxyv1.ContentSlot_CONTENT_SLOT_SITE,
		Description: "第三版",
	}))
	if err != nil {
		t.Fatalf("保存版本失败: %v", err)
	}
	versionID := saved.Msg.GetVersion().GetId()
	if saved.Msg.GetVersion().GetDescription() != "第三版" {
		t.Errorf("说明 = %q，期望随保存写入", saved.Msg.GetVersion().GetDescription())
	}
	updated, err := client.UpdateVersion(ctx, connect.NewRequest(&galaxyv1.UpdateVersionRequest{
		ProjectId: projectID, Slot: galaxyv1.ContentSlot_CONTENT_SLOT_SITE,
		VersionId: versionID, Description: "改过的说明",
	}))
	if err != nil {
		t.Fatalf("改说明失败: %v", err)
	}
	if updated.Msg.GetVersion().GetDescription() != "改过的说明" {
		t.Errorf("说明 = %q", updated.Msg.GetVersion().GetDescription())
	}
	if len(updated.Msg.GetVersion().GetEntries()) != len(saved.Msg.GetVersion().GetEntries()) ||
		updated.Msg.GetVersion().GetSeq() != saved.Msg.GetVersion().GetSeq() {
		t.Error("改说明动了清单或序号")
	}

	// 恢复到**最早**的那一条：草稿变回第一版，而恢复前的那一份也留成历史。
	restored, err := client.RestoreDraftSnapshot(ctx, connect.NewRequest(&galaxyv1.RestoreDraftSnapshotRequest{
		ProjectId: projectID, Slot: galaxyv1.ContentSlot_CONTENT_SLOT_SITE, SnapshotId: oldest.GetId(),
	}))
	if err != nil {
		t.Fatalf("恢复失败: %v", err)
	}
	if len(restored.Msg.GetDraft().GetEntries()) != 1 ||
		restored.Msg.GetDraft().GetEntries()[0].GetDigest() != oldest.GetEntries()[0].GetDigest() {
		t.Errorf("恢复后的草稿 = %+v，期望与那条快照逐字相同", restored.Msg.GetDraft().GetEntries())
	}
	history, err = client.ListDraftSnapshots(ctx, connect.NewRequest(&galaxyv1.ListDraftSnapshotsRequest{
		ProjectId: projectID, Slot: galaxyv1.ContentSlot_CONTENT_SLOT_SITE,
	}))
	if err != nil {
		t.Fatalf("读草稿历史失败: %v", err)
	}
	if len(history.Msg.GetSnapshots()) != 3 {
		t.Fatalf("历史 = %d 条，期望 3 条（恢复本身也留了一条）", len(history.Msg.GetSnapshots()))
	}

	// 把那条历史升级成版本：清单与它逐字相同，而**当前草稿不受影响**。
	fromSnapshot, err := client.SaveVersion(ctx, connect.NewRequest(&galaxyv1.SaveVersionRequest{
		ProjectId: projectID, Slot: galaxyv1.ContentSlot_CONTENT_SLOT_SITE,
		FromSnapshotId: newest.GetId(), Description: "从历史存的一版",
	}))
	if err != nil {
		t.Fatalf("从历史存版本失败: %v", err)
	}
	entries := fromSnapshot.Msg.GetVersion().GetEntries()
	if len(entries) != 1 || entries[0].GetDigest() != newest.GetEntries()[0].GetDigest() {
		t.Errorf("版本清单 = %+v，期望与那条历史逐字相同", entries)
	}
	draft, err := client.GetDraft(ctx, connect.NewRequest(&galaxyv1.GetDraftRequest{
		ProjectId: projectID, Slot: galaxyv1.ContentSlot_CONTENT_SLOT_SITE,
	}))
	if err != nil {
		t.Fatalf("读草稿失败: %v", err)
	}
	if draft.Msg.GetDraft().GetEntries()[0].GetDigest() == newest.GetEntries()[0].GetDigest() {
		t.Error("从历史存版本不该改掉当前草稿")
	}

	// **快照不是版本**：它不出现在版本列表里，也不能被发布。
	versions, err := client.ListVersions(ctx, connect.NewRequest(&galaxyv1.ListVersionsRequest{
		ProjectId: projectID, Slot: galaxyv1.ContentSlot_CONTENT_SLOT_SITE,
	}))
	if err != nil {
		t.Fatalf("列版本失败: %v", err)
	}
	for _, version := range versions.Msg.GetVersions() {
		if version.GetId() == newest.GetId() {
			t.Error("版本列表里出现了草稿快照")
		}
	}
	if _, err := client.Publish(ctx, connect.NewRequest(&galaxyv1.PublishRequest{
		ProjectId: projectID, Slot: galaxyv1.ContentSlot_CONTENT_SLOT_SITE, VersionId: newest.GetId(),
	})); err == nil {
		t.Error("用一条草稿快照的标识发布成功了")
	}
}

// assetIDsOf 取出一组资产的标识。
func assetIDsOf(resp *galaxyv1.ListAssetsResponse) []string {
	ids := make([]string, 0, len(resp.GetAssets()))
	for _, asset := range resp.GetAssets() {
		ids = append(ids, asset.GetId())
	}
	return ids
}
