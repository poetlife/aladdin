package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"

	galaxyv1 "github.com/poetlife/aladdin/api/gen/aladdin/galaxy/v1"
	"github.com/poetlife/aladdin/api/gen/aladdin/galaxy/v1/galaxyv1connect"
	"github.com/poetlife/aladdin/internal/galaxy"
)

// 回读发布态那一组命令：读回的是**访客走的那条路**上的产物。
//
// 这里起**两台**服务端：一台假的服务端（回答 GetProject / GetPublication），一台
// 假的**发布域**。两边分开是刻意的——发布态与 API 不同源，而这组命令要证明的正是
// "按访客的路径把产物取回来"，把它接到 API 上就测不到那件事。
const (
	readbackProject = "prj_readback"
	readbackBase    = "/g/" + readbackProject + "/"
	readbackText    = "<p>你好</p>"
	readbackTextDst = "index.html"
	readbackAsset   = "assets/ast_logo"
)

// fakeGalaxy 是一台只回答回读那两条方法的假服务端。
type fakeGalaxy struct {
	galaxyv1connect.UnimplementedGalaxyServiceHandler
	project     *galaxyv1.Project
	publication *galaxyv1.Publication
	entries     []*galaxyv1.FileEntry
}

func (f *fakeGalaxy) GetProject(context.Context, *connect.Request[galaxyv1.GetProjectRequest]) (*connect.Response[galaxyv1.GetProjectResponse], error) {
	return connect.NewResponse(&galaxyv1.GetProjectResponse{Project: f.project}), nil
}

func (f *fakeGalaxy) GetPublication(context.Context, *connect.Request[galaxyv1.GetPublicationRequest]) (*connect.Response[galaxyv1.GetPublicationResponse], error) {
	if f.publication == nil {
		return connect.NewResponse(&galaxyv1.GetPublicationResponse{}), nil
	}
	return connect.NewResponse(&galaxyv1.GetPublicationResponse{
		Publication: f.publication,
		Entries:     f.entries,
	}), nil
}

// readbackFixture 是这套用例的装配。
type readbackFixture struct {
	// address 是假服务端的地址（host:port）。
	address string
	// releaseURL 是假发布域的根地址。
	releaseURL string
	galaxy     *fakeGalaxy
}

// newReadbackFixture 起假服务端与假发布域。
//
// release 是发布域那边的行为：文本按路径给出、资产给一个 302。
func newReadbackFixture(t *testing.T, release http.Handler) *readbackFixture {
	t.Helper()
	f := &readbackFixture{galaxy: &fakeGalaxy{}}
	_, handler := galaxyv1connect.NewGalaxyServiceHandler(f.galaxy)
	api := httptest.NewServer(handler)
	t.Cleanup(api.Close)
	f.address = strings.TrimPrefix(api.URL, "http://")

	pages := httptest.NewServer(release)
	t.Cleanup(pages.Close)
	f.releaseURL = pages.URL
	return f
}

// published 给这台假服务端装上"这个槽已发布"的状态与那份产物清单。
func (f *readbackFixture) published(texts map[string]string, assets ...string) {
	f.galaxy.project = &galaxyv1.Project{
		Id: readbackProject,
		Slots: []*galaxyv1.ProjectSlot{{
			Slot:      galaxyv1.ContentSlot_CONTENT_SLOT_SITE,
			Published: true,
			BaseUrl:   readbackBase,
		}},
	}
	f.galaxy.publication = &galaxyv1.Publication{
		Id:          "pub_readback",
		ProjectId:   readbackProject,
		VersionId:   "ver_readback",
		PublishedAt: "2026-03-01T12:00:00Z",
		Url:         "https://app.example.com" + strings.TrimSuffix(readbackBase, "/"),
		Slot:        galaxyv1.ContentSlot_CONTENT_SLOT_SITE,
	}
	entries := make([]*galaxyv1.FileEntry, 0, len(texts)+len(assets))
	for entryPath, body := range texts {
		entries = append(entries, &galaxyv1.FileEntry{
			Path:   entryPath,
			Source: &galaxyv1.FileEntry_Digest{Digest: galaxy.ContentDigest([]byte(body))},
			Url:    f.releaseURL + readbackBase + entryPath,
		})
	}
	for _, entryPath := range assets {
		entries = append(entries, &galaxyv1.FileEntry{
			Path:   entryPath,
			Source: &galaxyv1.FileEntry_AssetId{AssetId: "ast_logo"},
			Url:    f.releaseURL + readbackBase + entryPath,
		})
	}
	f.galaxy.entries = entries
}

// releaseFrom 是一个假发布域：按路径给出文本，按资产路径给出 302。
//
// 资产那一跳指向**本服务端**上的 `/bucket/...`：生产上它指向对象存储的公开区，
// 而这里要验的是"顺着访客那条路能把字节取回来"，因此把终点也放在同一台假服务上
// （相对地址，按 RFC 9110 由客户端对着请求地址解析）。
func releaseFrom(texts map[string]string, assets ...string) http.Handler {
	assetPaths := make(map[string]bool, len(assets))
	for _, entryPath := range assets {
		assetPaths[entryPath] = true
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if entryPath, ok := strings.CutPrefix(r.URL.Path, "/bucket/"); ok {
			if assetPaths[entryPath] {
				_, _ = w.Write([]byte("媒体字节：" + entryPath))
				return
			}
			http.NotFound(w, r)
			return
		}
		entryPath := strings.TrimPrefix(r.URL.Path, readbackBase)
		if body, ok := texts[entryPath]; ok {
			_, _ = w.Write([]byte(body))
			return
		}
		if assetPaths[entryPath] {
			w.Header().Set("Location", "/bucket/"+entryPath)
			w.WriteHeader(http.StatusFound)
			return
		}
		http.NotFound(w, r)
	})
}

// runReadbackCLI 在进程内跑一条命令，返回 stdout 与错误。
func runReadbackCLI(t *testing.T, fixture *readbackFixture, args ...string) (string, error) {
	t.Helper()
	root := newRootCommand()
	var stdout bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stdout)
	root.SetArgs(append([]string{
		"--address", fixture.address,
		"--token", "test-token",
		"--yes",
	}, args...))
	err := root.Execute()
	return stdout.String(), err
}

// 清单与单份字节都按**发布态的那条地址**取回来。
func TestPublicationGetReadsBackWhatVisitorsSee(t *testing.T) {
	fixture := newReadbackFixture(t, releaseFrom(
		map[string]string{readbackTextDst: readbackText}, readbackAsset))
	fixture.published(map[string]string{readbackTextDst: readbackText}, readbackAsset)

	listed, err := runReadbackCLI(t, fixture, "galaxy", "publication", "get", readbackProject)
	if err != nil {
		t.Fatalf("publication get 失败: %v\n%s", err, listed)
	}
	for _, want := range []string{readbackTextDst, readbackAsset, "https://app.example.com" + strings.TrimSuffix(readbackBase, "/")} {
		if !strings.Contains(listed, want) {
			t.Errorf("清单里没有 %q:\n%s", want, listed)
		}
	}

	body, err := runReadbackCLI(t, fixture, "galaxy", "publication", "get", readbackProject, "--path", readbackTextDst)
	if err != nil {
		t.Fatalf("取单份失败: %v", err)
	}
	if body != readbackText {
		t.Errorf("取回的字节 = %q，期望 %q", body, readbackText)
	}
}

// 整组取回到本地目录：取的是发布态，不是"我推上去的那份源"。
func TestPublicationPullWritesThePublishedArtifacts(t *testing.T) {
	fixture := newReadbackFixture(t, releaseFrom(
		map[string]string{readbackTextDst: readbackText}, readbackAsset))
	fixture.published(map[string]string{readbackTextDst: readbackText}, readbackAsset)

	dir := filepath.Join(t.TempDir(), "published")
	out, err := runReadbackCLI(t, fixture, "galaxy", "publication", "pull", readbackProject, dir)
	if err != nil {
		t.Fatalf("publication pull 失败: %v\n%s", err, out)
	}
	//nolint:gosec // 读的是本用例自己刚写出来的文件
	got, err := os.ReadFile(filepath.Join(dir, readbackTextDst))
	if err != nil {
		t.Fatalf("读回写出的文件失败: %v", err)
	}
	if string(got) != readbackText {
		t.Errorf("写出的字节 = %q，期望 %q", got, readbackText)
	}
	// 资产条目也取回来：访客那条路给出的是一跳，跟到底才是媒体字节。
	//nolint:gosec // 读的是本用例自己刚写出来的文件
	media, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(readbackAsset)))
	if err != nil {
		t.Fatalf("读回写出的资产失败: %v", err)
	}
	if want := "媒体字节：" + readbackAsset; string(media) != want {
		t.Errorf("写出的资产字节 = %q，期望 %q", media, want)
	}
}

// 一份自洽的发布态：复核通过，退出码为零。
func TestPublicationVerifyAcceptsAConsistentRelease(t *testing.T) {
	fixture := newReadbackFixture(t, releaseFrom(
		map[string]string{readbackTextDst: readbackText}, readbackAsset))
	fixture.published(map[string]string{readbackTextDst: readbackText}, readbackAsset)

	out, err := runReadbackCLI(t, fixture, "galaxy", "publication", "verify", readbackProject)
	if err != nil {
		t.Fatalf("复核失败: %v\n%s", err, out)
	}
	if !strings.Contains(out, "无问题") {
		t.Errorf("通过时没有给出结论：\n%s", out)
	}
	// 通过时也把"核对了几份"说出来——那正是"发布成功了"变成可核对事实的地方。
	if !strings.Contains(out, "1 份文本产物") || !strings.Contains(out, "1 条资产引用") {
		t.Errorf("没有给出核对的数量：\n%s", out)
	}
}

// 产物里残留没解开的记号：复核报出来，并以非零状态结束。
//
// 这正是 #52/#54 那一类失败：发布域上交付的是字面量记号，而 validate 与 publish
// 都说成功了。
func TestPublicationVerifyRejectsUnresolvedMarker(t *testing.T) {
	const broken = `<img src="asset://ast_logo">`
	fixture := newReadbackFixture(t, releaseFrom(map[string]string{readbackTextDst: broken}))
	fixture.published(map[string]string{readbackTextDst: broken})

	out, err := runReadbackCLI(t, fixture, "galaxy", "publication", "verify", readbackProject)
	if err == nil {
		t.Fatalf("残留的记号被放过了:\n%s", out)
	}
	if !strings.Contains(out, "没有被解开") {
		t.Errorf("没有指出那是一处没解开的记号：\n%s", out)
	}
}

// 取回的字节与发布记录里的摘要不符：那是内容被动过，不是"看起来一样"。
func TestPublicationVerifyRejectsDigestMismatch(t *testing.T) {
	fixture := newReadbackFixture(t, releaseFrom(map[string]string{readbackTextDst: "<p>被改过</p>"}))
	fixture.published(map[string]string{readbackTextDst: readbackText})

	out, err := runReadbackCLI(t, fixture, "galaxy", "publication", "verify", readbackProject)
	if err == nil {
		t.Fatalf("摘要不符被放过了:\n%s", out)
	}
	if !strings.Contains(out, "摘要不符") {
		t.Errorf("没有指出摘要不符：\n%s", out)
	}
}

// 资产引用的发布态地址不可达：复核报出来（它没跟着重定向下载整份媒体）。
func TestPublicationVerifyRejectsUnreachableAsset(t *testing.T) {
	fixture := newReadbackFixture(t, releaseFrom(map[string]string{readbackTextDst: readbackText}))
	fixture.published(map[string]string{readbackTextDst: readbackText}, readbackAsset)

	out, err := runReadbackCLI(t, fixture, "galaxy", "publication", "verify", readbackProject)
	if err == nil {
		t.Fatalf("不可达的资产引用被放过了:\n%s", out)
	}
	if !strings.Contains(out, "不可达") {
		t.Errorf("没有指出资产不可达：\n%s", out)
	}
}

// 未发布的槽：回读不是"空清单"，而是一条明确的非零结论。
func TestPublicationGetOnUnpublishedSlotFails(t *testing.T) {
	fixture := newReadbackFixture(t, releaseFrom(nil))
	fixture.galaxy.project = &galaxyv1.Project{
		Id: readbackProject,
		Slots: []*galaxyv1.ProjectSlot{{
			Slot:    galaxyv1.ContentSlot_CONTENT_SLOT_SITE,
			BaseUrl: readbackBase,
		}},
	}

	out, err := runReadbackCLI(t, fixture, "galaxy", "publication", "get", readbackProject)
	if err == nil {
		t.Fatalf("未发布时回读成功了:\n%s", out)
	}
	if !strings.Contains(err.Error(), "还没有发布过") {
		t.Errorf("错误信息 %q 没有说清是未发布", err)
	}
}

// --output json 给出每一条资产引用的最终地址与状态，供脚本逐条读。
func TestPublicationVerifyJSONListsAssets(t *testing.T) {
	fixture := newReadbackFixture(t, releaseFrom(
		map[string]string{readbackTextDst: readbackText}, readbackAsset))
	fixture.published(map[string]string{readbackTextDst: readbackText}, readbackAsset)

	// --output json 走的是 os.Stdout（与其余命令一致），因此这里接管它。
	original := os.Stdout
	tmp, err := os.CreateTemp(t.TempDir(), "stdout")
	if err != nil {
		t.Fatalf("建临时文件失败: %v", err)
	}
	os.Stdout = tmp
	_, runErr := runReadbackCLI(t, fixture, "galaxy", "publication", "verify", readbackProject, "--output", "json")
	os.Stdout = original
	_ = tmp.Close()
	data, err := os.ReadFile(tmp.Name())
	if err != nil {
		t.Fatalf("读 stdout 失败: %v", err)
	}
	out := string(data)
	if runErr != nil {
		t.Fatalf("复核失败: %v\n%s", runErr, out)
	}
	for _, want := range []string{`"checked_texts": 1`, `"checked_assets": 1`,
		`"path": "` + readbackAsset + `"`, `"status": "ok"`, `"problems": []`} {
		if !strings.Contains(out, want) {
			t.Errorf("JSON 里没有 %s：\n%s", want, out)
		}
	}
}
