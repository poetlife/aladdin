package skill

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// 本文件测的是**真实的 REST 路径**：响应长什么样、状态码怎么折成领域结论、取字节
// 的顺序与并发。在此之前那一条路径只有 github_smoke_test.go 里默认跳过的联网冒烟
// 能走到，于是"取回"这一段的核心行为在没有网络时完全没有覆盖。
//
// 替身服务由测试起，地址经 GithubRemote.apiBase 注入——**生产构造只给它常量**，
// 因此这道缝不改变"取字节的地址由服务端自己拼"这条边界（见 remote.go 的 do）。

// testCommit 是替身给出的提交标识。取值本身不重要，只要非空且稳定。
const testCommit = "0123456789abcdef0123456789abcdef01234567"

// fakeGithub 是一个本地的 GitHub API 替身。
//
// 它只实现这条路径真正用到的四个端点，并记下**并发的取字节请求数**——那是
// "取字节并发但有上限"这条性质的唯一观测点。
type fakeGithub struct {
	server *httptest.Server
	// t 只用来在替身自己出错时记一笔（写响应失败），不参与断言。
	t *testing.T

	// blobDelay 是每条取字节请求的停留时间。它把"同时在飞"变成一个可观测的量：
	// 串行实现下永远是 1，并发实现下会爬到上限。
	blobDelay time.Duration

	tree  []repoTreeEntry
	blobs map[string]string

	mu          sync.Mutex
	blobCalls   int
	treeCalls   int
	inFlight    int
	maxInFlight int
}

// serve 起替身并把一个 GithubRemote 指向它。
func (f *fakeGithub) serve(t *testing.T) *GithubRemote {
	t.Helper()
	f.t = t
	f.server = httptest.NewServer(f)
	t.Cleanup(f.server.Close)
	return &GithubRemote{
		client:  &http.Client{Timeout: remoteTimeout},
		apiBase: f.server.URL,
	}
}

// stats 取一份并发与调用次数的快照。
func (f *fakeGithub) stats() (blobCalls, treeCalls, maxInFlight int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.blobCalls, f.treeCalls, f.maxInFlight
}

func (f *fakeGithub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// 替身只认形状，不认取值：路径段数就是它的路由表。
	segments := strings.Split(strings.TrimPrefix(r.URL.Path, "/repos/"), "/")
	switch {
	case len(segments) == 2:
		// GET /repos/{owner}/{repo}：默认分支。
		f.writeJSON(w, map[string]any{"default_branch": "main"})
	case len(segments) == 4 && segments[2] == "commits":
		// GET /repos/{owner}/{repo}/commits/{ref}：引用解析成提交。
		f.writeJSON(w, map[string]any{"sha": testCommit})
	case len(segments) == 5 && segments[2] == "git" && segments[3] == "trees":
		f.mu.Lock()
		f.treeCalls++
		f.mu.Unlock()
		f.writeJSON(w, map[string]any{"tree": f.tree, "truncated": false})
	case len(segments) == 5 && segments[2] == "git" && segments[3] == "blobs":
		f.serveBlob(w, segments[4])
	default:
		http.Error(w, "替身不认这条路径", http.StatusNotFound)
	}
}

func (f *fakeGithub) serveBlob(w http.ResponseWriter, sha string) {
	f.mu.Lock()
	f.blobCalls++
	f.inFlight++
	if f.inFlight > f.maxInFlight {
		f.maxInFlight = f.inFlight
	}
	body, known := f.blobs[sha]
	f.mu.Unlock()

	if f.blobDelay > 0 {
		time.Sleep(f.blobDelay)
	}

	f.mu.Lock()
	f.inFlight--
	f.mu.Unlock()

	if !known {
		http.Error(w, "没有这个对象", http.StatusNotFound)
		return
	}
	// 取字节走的是 raw 媒体类型，回到正文的字节本身而不是 JSON 包装。
	_, _ = io.WriteString(w, body)
}

func (f *fakeGithub) writeJSON(w http.ResponseWriter, payload any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		// 替身自己坏了要说出来，否则现象是"取回失败"而原因藏在测试基建里。
		f.t.Errorf("替身写响应失败: %v", err)
	}
}

// staticTree 造一棵"20 个文本 + 1 张图 + 1 个超单文件上限的家伙"的树。
//
// 三个数量各有分工：文本条数要**多于并发上限**，好让并发爬满；图片验证"不是文本
// 的跳过并点名"；超限的那一条验证"它在计划阶段就被跳过，一个请求都不发"。
func staticTree() (tree []repoTreeEntry, blobs map[string]string) {
	blobs = map[string]string{}
	add := func(path, content string) {
		sha := fmt.Sprintf("%040x", len(blobs)+1)
		blobs[sha] = content
		tree = append(tree, repoTreeEntry{
			Path: path, Mode: "100644", Type: "blob", Size: int64(len(content)), SHA: sha,
		})
	}

	add(ManifestPath, "---\nname: demo\ndescription: 一条用来测取回的技能。\n---\n\n正文。\n")
	for i := range 20 {
		add(fmt.Sprintf("text/%02d.md", i), fmt.Sprintf("第 %d 条正文。\n", i))
	}
	add("examples/cover.png", "\x89PNG\x00\x01\x02binary")
	// 超单文件上限的条目：**它的字节不在 blobs 里**，因为根本不该有人去取。
	tree = append(tree, repoTreeEntry{
		Path: "examples/huge.bin", Mode: "100644", Type: "blob", Size: MaxFileBytes + 1,
		SHA: strings.Repeat("f", 40),
	})
	return tree, blobs
}

// TestGithubFetchTreeFetchesConcurrently 守住"取字节并发进行、但有上限"。
//
// 两条断言合起来才成立：只测上限，一个退化成串行的实现照样通过（上限恒不被触碰）；
// 只测"并发过"，一个不设限的实现也照样通过。**上限与并发必须同时被钉住**。
func TestGithubFetchTreeFetchesConcurrently(t *testing.T) {
	tree, blobs := staticTree()
	fake := &fakeGithub{tree: tree, blobs: blobs, blobDelay: 30 * time.Millisecond}
	remote := fake.serve(t)

	repo, err := ParseRepositoryURL("https://github.com/owner/repo")
	if err != nil {
		t.Fatalf("解析地址: %v", err)
	}
	got, err := remote.FetchTree(t.Context(), repo, testCommit, "")
	if err != nil {
		t.Fatalf("取回: %v", err)
	}

	blobCalls, treeCalls, maxInFlight := fake.stats()

	if want := 21; len(got.Files) != want {
		t.Fatalf("留下的条目数 = %d，期望 %d——并发不只是要快，还得把东西取回来", len(got.Files), want)
	}

	// **并发是有限的。** 这是远端对同一凭据的并发上限之下的一个小数字，越位的表现
	// 是远端拒绝这次访问，而它会与限频混成同一个结论。
	if maxInFlight > blobFetchConcurrency {
		t.Errorf("同时在飞的取字节请求数 = %d，超过上限 %d", maxInFlight, blobFetchConcurrency)
	}
	// 而它确实在并发：否则上面那条断言永远成立，也就什么都没守住。
	if maxInFlight < 2 {
		t.Errorf("同时在飞的取字节请求数 = %d，取字节看起来是串行的——"+
			"这条路径的耗时几乎全部来自逐个请求的往返，串行下 41 次就是 29.6 秒", maxInFlight)
	}

	// **请求数一条都没少。** 并发只压缩墙钟时间，配额消耗与限频结论都不变：先问一次
	// 目录树，再为每个要收的条目各发一次。
	if treeCalls != 1 {
		t.Errorf("目录树请求数 = %d，期望 1", treeCalls)
	}
	// 20 条正文 + 清单 + 那张图 = 22；超限的那一条**一个请求都不该发**。
	if want := 22; blobCalls != want {
		t.Errorf("取字节请求数 = %d，期望 %d（超单文件上限的条目不该被取）", blobCalls, want)
	}
}

// TestGithubFetchTreeKeepsPlanOrder 守住"并发不改变结果的顺序"。
//
// 留下与跳过的清单是留痕的一部分：同样的一棵树必须给出同样的清单，而不是一个每次
// 都不一样的顺序。跳过的那张图会被收在计划阶段就已跳过的那些之后，与逐条串行时
// 一致。
func TestGithubFetchTreeKeepsPlanOrder(t *testing.T) {
	tree, blobs := staticTree()
	fake := &fakeGithub{tree: tree, blobs: blobs, blobDelay: 5 * time.Millisecond}
	remote := fake.serve(t)

	repo, err := ParseRepositoryURL("https://github.com/owner/repo")
	if err != nil {
		t.Fatalf("解析地址: %v", err)
	}
	got, err := remote.FetchTree(t.Context(), repo, testCommit, "")
	if err != nil {
		t.Fatalf("取回: %v", err)
	}

	for i := 1; i < len(got.Files); i++ {
		if got.Files[i-1].Path >= got.Files[i].Path {
			t.Fatalf("取回的条目没有按路径升序：%q 在 %q 之前",
				got.Files[i-1].Path, got.Files[i].Path)
		}
	}
	if want := 21; len(got.Files) != want {
		t.Errorf("留下的条目数 = %d，期望 %d", len(got.Files), want)
	}
	// 超限的先（它在计划阶段就被跳过了），然后是取回来才发现不是文本的那张图。
	wantSkipped := []string{"examples/huge.bin", "examples/cover.png"}
	if strings.Join(got.Skipped, ",") != strings.Join(wantSkipped, ",") {
		t.Errorf("跳过的条目 = %v，期望 %v", got.Skipped, wantSkipped)
	}
	// 条目本身的内容也要对：并发下最容易出的错是"结果张冠李戴"。
	for _, file := range got.Files {
		if file.Path == ManifestPath {
			if !strings.HasPrefix(string(file.Data), "---\nname: demo") {
				t.Errorf("%s 的内容对不上：%q", ManifestPath, truncate(string(file.Data), 30))
			}
		}
	}
}

// TestGithubRemoteDoesNotBlameRemoteForCallerDeadline 守住"调用方走了 ≠ 远端挂了"。
//
// 两者混成一个结论的代价是具体的：一次客户端超时会在服务端留痕里长成一个 503，
// 而读日志的人会去查一个没有故障的远端（见 docs/design/skill/onboarding.md 的
// "超时与失败"）。
func TestGithubRemoteDoesNotBlameRemoteForCallerDeadline(t *testing.T) {
	tree, blobs := staticTree()
	fake := &fakeGithub{tree: tree, blobs: blobs, blobDelay: 300 * time.Millisecond}
	remote := fake.serve(t)

	repo, err := ParseRepositoryURL("https://github.com/owner/repo")
	if err != nil {
		t.Fatalf("解析地址: %v", err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()

	_, err = remote.FetchTree(ctx, repo, testCommit, "")
	if err == nil {
		t.Fatal("上下文已结束，取回不该成功")
	}
	if strings.Contains(err.Error(), ErrRemoteUnavailable.Error()) {
		t.Errorf("调用方的 deadline 被报成了远端故障：%v", err)
	}
	if !strings.Contains(err.Error(), context.DeadlineExceeded.Error()) {
		t.Errorf("错误 = %v，期望点名上下文已超时", err)
	}
}
