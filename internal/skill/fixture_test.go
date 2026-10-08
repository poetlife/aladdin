package skill

import (
	"context"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/poetlife/aladdin/internal/objectstore"
)

// 夹具：一个可编程的假远端、一个内存存储、一个内存对象存储与一个受控的时钟。
//
// **测试不访问任何网络**（见 docs/design/skill/README.md 的可验证性表）：远端
// 拉取藏在 Remote 接口后面，这里注入一个假的；真实实现另有一条默认跳过的冒烟。

const (
	testOwner = "yanliudesign"
	testRepo  = "mono-color-skill"
	testRef   = "main"
	testSHA   = "1f4a9c2d3e5b6a7089abcdef1234567890abcdef"
)

// fakeRemote 是一个按提交脚本化返回的远端。
type fakeRemote struct {
	// commit 是当前"远端指向"的提交。
	commit string
	// files 是按提交存的树。
	trees map[string]map[string]string
	// blobs 是 FetchTree 走过的对象（假的标识 → 字节），供 FetchBlob 反查。
	blobs map[string][]byte
	// resolveErr / fetchErr 让测试指定一次远端失败。
	resolveErr error
	fetchErr   error
	// resolved 记录解析引用时收到的输入，供"只认形状"那条断言使用。
	resolved []string
	// fetched 记录取回时收到的地址三元组，供"取字节地址由服务端拼"那条断言使用。
	fetched []string
}

func newFakeRemote() *fakeRemote {
	return &fakeRemote{
		commit: testSHA,
		trees:  map[string]map[string]string{},
		blobs:  map[string][]byte{},
	}
}

func (f *fakeRemote) setTree(commit string, files map[string]string) {
	f.commit = commit
	f.trees[commit] = files
}

func (f *fakeRemote) ResolveCommit(_ context.Context, repo Repository, ref string) (string, error) {
	f.resolved = append(f.resolved, fmt.Sprintf("%s/%s@%s", repo.Owner, repo.Name, ref))
	if f.resolveErr != nil {
		return "", f.resolveErr
	}
	return f.commit, nil
}

// FetchTree 按与真实实现**同一条规则**取材：留下文本，跳过二进制与超单文件上限的。
//
// 假实现与真实现分家的表现是"单元测试全绿、真纳管一个也进不来"——而那正是这条
// 规则最该被守住的地方。
func (f *fakeRemote) FetchTree(_ context.Context, repo Repository, commit, subPath string) (Tree, error) {
	f.fetched = append(f.fetched, fmt.Sprintf("%s/%s@%s#%s", repo.Owner, repo.Name, commit, subPath))
	if f.fetchErr != nil {
		return Tree{}, f.fetchErr
	}
	tree, ok := f.trees[commit]
	if !ok {
		return Tree{}, fmt.Errorf("%w: 没有这个提交", ErrRepositoryNotFound)
	}
	paths := make([]string, 0, len(tree))
	for path := range tree {
		paths = append(paths, path)
	}
	sort.Strings(paths)

	var out Tree
	for _, path := range paths {
		if subPath != "" && path != subPath && !hasPrefixSegment(path, subPath) {
			continue
		}
		relative := trimSubPath(path, subPath)
		data := []byte(tree[path])
		// 假的对象标识按**仓库内的路径**派生，与子路径无关：同一份字节在带子路径与
		// 不带子路径的两次取回里是同一个对象，与真实实现一致（那边用的是 git 的 sha）。
		sha := "sha-" + path
		f.blobs[sha] = data
		out.Blobs = append(out.Blobs, Blob{Path: relative, SHA: sha, Size: int64(len(data))})
		if len(data) > MaxFileBytes || !isText(data) {
			out.Skipped = append(out.Skipped, relative)
			continue
		}
		out.Files = append(out.Files, FetchedFile{Path: relative, Data: data})
	}
	sort.Slice(out.Blobs, func(i, j int) bool { return out.Blobs[i].Path < out.Blobs[j].Path })
	return out, nil
}

// FetchBlob 按 FetchTree 给过的标识取字节。
//
// 它存在的理由是**包外的东西要能按路径找**——目前唯一的消费方是展示图：那几张多半
// 正是被跳过的那类二进制，而它们仍然得能被取回来（见 image.go）。
func (f *fakeRemote) FetchBlob(_ context.Context, _ Repository, sha string, maxBytes int64) ([]byte, error) {
	data, ok := f.blobs[sha]
	if !ok {
		return nil, fmt.Errorf("%w: 没有这个对象", ErrRepositoryNotFound)
	}
	// 上限由调用方给，假实现照它执行：不执行的话，"取展示图时错用了包里的单文件上限"
	// 这条就漏过去了。
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("%w: 取回的对象超过 %d 字节", ErrPackageInvalid, maxBytes)
	}
	return data, nil
}

func hasPrefixSegment(path, prefix string) bool {
	return len(path) > len(prefix) && path[:len(prefix)] == prefix && path[len(prefix)] == '/'
}

func trimSubPath(path, subPath string) string {
	if subPath == "" {
		return path
	}
	if path == subPath {
		return ""
	}
	return path[len(subPath)+1:]
}

// fixture 把一次测试要用的四样东西凑在一起。
type fixture struct {
	service *Service
	store   *MemoryStore
	objects *objectstore.MemoryStore
	remote  *fakeRemote
	now     time.Time
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	store := NewMemoryStore()
	objects := objectstore.NewMemoryStore()
	remote := newFakeRemote()
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	f := &fixture{store: store, objects: objects, remote: remote, now: now}
	f.service = NewService(Deps{
		Store:   store,
		Objects: objects,
		Remote:  remote,
		Now:     func() time.Time { return f.now },
	})
	return f
}

// manifest 造一份合法的 SKILL.md。
func manifest(name, description string) string {
	return fmt.Sprintf("---\nname: %s\ndescription: %s\n---\n\n正文。\n", name, description)
}

// validTree 造一棵最小但完整的包。
func validTree() map[string]string {
	return map[string]string{
		ManifestPath:     manifest("mono-color", "单色印刷风出图。当用户要一张克制的海报时使用。"),
		"palette.md":     "# 色板\n",
		"assets/ink.css": "body { color: #111; }\n",
	}
}

// importOne 纳管一个技能，失败即终止用例。
func (f *fixture) importOne(t *testing.T) Skill {
	t.Helper()
	f.remote.setTree(testSHA, validTree())
	item, err := f.service.Import(context.Background(), ImportParams{
		RepositoryURL: "https://github.com/" + testOwner + "/" + testRepo,
		Ref:           testRef,
		Tags:          []string{"出图", "排版"},
	})
	if err != nil {
		t.Fatalf("纳管失败: %v", err)
	}
	return item
}
