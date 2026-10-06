package skill

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
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
	// resolveErr / fetchErr 让测试指定一次远端失败。
	resolveErr error
	fetchErr   error
	// resolved 记录解析引用时收到的输入，供"只认形状"那条断言使用。
	resolved []string
	// fetched 记录取回时收到的地址三元组，供"取字节地址由服务端拼"那条断言使用。
	fetched []string
}

func newFakeRemote() *fakeRemote {
	return &fakeRemote{commit: testSHA, trees: map[string]map[string]string{}}
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

func (f *fakeRemote) FetchTree(_ context.Context, repo Repository, commit, subPath string) ([]FetchedFile, error) {
	f.fetched = append(f.fetched, fmt.Sprintf("%s/%s@%s#%s", repo.Owner, repo.Name, commit, subPath))
	if f.fetchErr != nil {
		return nil, f.fetchErr
	}
	tree, ok := f.trees[commit]
	if !ok {
		return nil, fmt.Errorf("%w: 没有这个提交", ErrRepositoryNotFound)
	}
	paths := make([]string, 0, len(tree))
	for path := range tree {
		paths = append(paths, path)
	}
	sort.Strings(paths)

	files := make([]FetchedFile, 0, len(paths))
	for _, path := range paths {
		if subPath != "" && path != subPath && !hasPrefixSegment(path, subPath) {
			continue
		}
		files = append(files, FetchedFile{Path: trimSubPath(path, subPath), Data: []byte(tree[path])})
	}
	return files, nil
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

// tarGz 造一个仓库压缩包。rootPrefix 是归档根（GitHub 用 `<owner>-<repo>-<sha>`）。
func tarGz(t *testing.T, rootPrefix string, entries []tarEntry) []byte {
	t.Helper()
	var buffer bytes.Buffer
	gz := gzip.NewWriter(&buffer)
	writer := tar.NewWriter(gz)
	for _, entry := range entries {
		header := &tar.Header{
			Name:     rootPrefix + "/" + entry.name,
			Mode:     0o644,
			Size:     int64(len(entry.data)),
			Typeflag: entry.typeflag,
			Linkname: entry.linkname,
		}
		if entry.typeflag == tar.TypeSymlink || entry.typeflag == tar.TypeDir {
			header.Size = 0
		}
		if err := writer.WriteHeader(header); err != nil {
			t.Fatalf("写压缩包头失败: %v", err)
		}
		if header.Size > 0 {
			if _, err := writer.Write([]byte(entry.data)); err != nil {
				t.Fatalf("写压缩包内容失败: %v", err)
			}
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("关闭 tar 失败: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("关闭 gzip 失败: %v", err)
	}
	return buffer.Bytes()
}

type tarEntry struct {
	name     string
	data     string
	typeflag byte
	linkname string
}

// regular 是一条普通文件条目。
func regular(name, data string) tarEntry {
	return tarEntry{name: name, data: data, typeflag: tar.TypeReg}
}

// archiveFiles 把压缩包解回一个路径到内容的映射，便于断言。
func archiveFiles(files []FetchedFile) map[string]string {
	result := make(map[string]string, len(files))
	for _, file := range files {
		result[file.Path] = string(file.Data)
	}
	return result
}
