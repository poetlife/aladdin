package skill

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// 本文件是**远端拉取**：把（仓库、引用）变成一个提交标识，再把那个提交下的一段
// 文件树取回来。
//
// 它是本模块的**一个出口**，不是"另一个模块"：只有本模块消费它，而它要暴露给
// 测试一个注入点（"不联网即可测"是硬要求，见 docs/design/skill/onboarding.md 的
// 可验证性表）。因此它在这里是一个接口加一个生产实现，假的实现出现在测试里。
//
// **取字节的地址由本文件自己拼。** 接口收的是（仓库、引用、提交、子路径）四项，
// 收不到"去哪里取"——调用方给的那串地址早在解析阶段就变成了这几项（见
// source.go）。这不是风格：允许传进来一个地址，就等于校验的是一个值、请求的是
// 另一个。

// Remote 是远端拉取的出口。
type Remote interface {
	// ResolveCommit 把引用解析成一个提交标识。ref 为空表示仓库的默认分支。
	//
	// 仓库不存在与引用不存在是同一个结论（ErrRepositoryNotFound）；远端不可达、
	// 限频是另一个（ErrRemoteUnavailable）——两者的应对不同：改引用，或者稍后重试。
	ResolveCommit(ctx context.Context, repo Repository, ref string) (string, error)

	// FetchTree 取回该提交下 subPath 之内的文件。subPath 为空表示仓库根。
	//
	// 返回的是**字节**，不是路径：远端实现把内容读进内存交出来，全程不落盘。
	// 超出上限的压缩包与解压结果在这里就被截断并拒绝——上限的意义是让一次纳管的
	// 最坏内存占用与最坏耗时都是有界的，而不是"看看远端有多大"。
	FetchTree(ctx context.Context, repo Repository, commit, subPath string) ([]FetchedFile, error)
}

const (
	// MaxArchiveBytes 是远端压缩包的字节上限。
	MaxArchiveBytes = 32 << 20
	// MaxExpandedBytes 是解压累计的字节上限。
	MaxExpandedBytes = 8 << 20
	// remoteTimeout 是单次远端请求的超时。
	//
	// 它管的是**一次请求**，不是整条纳管流程：给整条流程设总超时会把"远端慢"与
	// "远端挂了"变成同一个结论，而两者的应对不同。
	remoteTimeout = 30 * time.Second
	// githubAPIBase 是 GitHub REST API 的根。
	githubAPIBase = "https://api.github.com"
	// githubUserAgent 是请求头里的标识。GitHub 拒绝没有 User-Agent 的请求。
	githubUserAgent = "aladdin"
)

// GithubRemote 是从 github.com 拉取的实现。
//
// token 可空：它带来更高的限频额度与读私有仓库的能力，**不是权限**——拿不到它
// 不影响任何人读目录、取用技能，只影响"平台能不能把某一份远端内容拿进来"。
// 它只从环境变量读，不进任何日志与错误信息（见
// docs/design/config/credentials.md）。
type GithubRemote struct {
	client *http.Client
	token  string
}

// NewGithubRemote 构造一个 GitHub 拉取实现。token 为空表示匿名访问。
func NewGithubRemote(token string) *GithubRemote {
	return &GithubRemote{
		client: &http.Client{Timeout: remoteTimeout},
		token:  token,
	}
}

// ResolveCommit 实现 Remote。
func (g *GithubRemote) ResolveCommit(ctx context.Context, repo Repository, ref string) (string, error) {
	if ref == "" {
		// 引用为空时先问仓库的默认分支。**不猜 `main`**：猜错的部署会在一个
		// 约定俗成的名字上失败，而那个名字完全由远端决定。
		defaultBranch, err := g.defaultBranch(ctx, repo)
		if err != nil {
			return "", err
		}
		ref = defaultBranch
	}
	var payload struct {
		SHA string `json:"sha"`
	}
	path := fmt.Sprintf("/repos/%s/%s/commits/%s",
		url.PathEscape(repo.Owner), url.PathEscape(repo.Name), url.PathEscape(ref))
	if err := g.getJSON(ctx, path, &payload); err != nil {
		return "", err
	}
	if payload.SHA == "" {
		return "", fmt.Errorf("%w: %s 的引用 %q 没有解析出提交",
			ErrRepositoryNotFound, repo.Owner+"/"+repo.Name, ref)
	}
	return payload.SHA, nil
}

// FetchTree 实现 Remote。
func (g *GithubRemote) FetchTree(ctx context.Context, repo Repository, commit, subPath string) ([]FetchedFile, error) {
	path := fmt.Sprintf("/repos/%s/%s/tarball/%s",
		url.PathEscape(repo.Owner), url.PathEscape(repo.Name), url.PathEscape(commit))
	resp, err := g.do(ctx, path, "application/octet-stream")
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if err := classifyStatus(resp.StatusCode, g.token != ""); err != nil {
		return nil, err
	}
	// 压缩包的上限在这里就生效：读多一个字节即判超限，而不是先把整包收下来再数。
	limited := io.LimitReader(resp.Body, MaxArchiveBytes+1)
	files, err := readRepoArchive(limited, subPath)
	if err != nil {
		return nil, err
	}
	return files, nil
}

// defaultBranch 问出仓库的默认分支。
func (g *GithubRemote) defaultBranch(ctx context.Context, repo Repository) (string, error) {
	var payload struct {
		DefaultBranch string `json:"default_branch"`
	}
	path := fmt.Sprintf("/repos/%s/%s", url.PathEscape(repo.Owner), url.PathEscape(repo.Name))
	if err := g.getJSON(ctx, path, &payload); err != nil {
		return "", err
	}
	if payload.DefaultBranch == "" {
		return "", fmt.Errorf("%w: %s 没有默认分支（空仓库？）",
			ErrRepositoryNotFound, repo.Owner+"/"+repo.Name)
	}
	return payload.DefaultBranch, nil
}

// getJSON 取一段 JSON。
func (g *GithubRemote) getJSON(ctx context.Context, path string, out any) error {
	resp, err := g.do(ctx, path, "application/vnd.github+json")
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if err := classifyStatus(resp.StatusCode, g.token != ""); err != nil {
		return err
	}
	decoder := json.NewDecoder(io.LimitReader(resp.Body, 1<<20))
	if err := decoder.Decode(out); err != nil {
		return fmt.Errorf("%w: 远端返回的不是预期的 JSON: %v", ErrRemoteUnavailable, err)
	}
	return nil
}

// do 发起一次请求。**地址由这里拼**：只有 API 根、路径段与认证，没有别的来源。
func (g *GithubRemote) do(ctx context.Context, path, accept string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, githubAPIBase+path, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrRemoteUnavailable, err)
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", githubUserAgent)
	if g.token != "" {
		req.Header.Set("Authorization", "Bearer "+g.token)
	}
	resp, err := g.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrRemoteUnavailable, err)
	}
	return resp, nil
}

// classifyStatus 把一个 HTTP 状态折成领域结论。
//
// 三种结局必须分开：**远端没有这个东西**（改引用或改来源）、**远端说不**（限频、
// 私有仓库没凭据）、**远端坏了**（可重试）。它们混成一句"拉取失败"的表现是管理员
// 完全不知道该动哪一头。
func classifyStatus(status int, authenticated bool) error {
	switch {
	case status >= 200 && status < 300:
		return nil
	case status == http.StatusNotFound:
		return ErrRepositoryNotFound
	case status == http.StatusUnauthorized, status == http.StatusForbidden:
		if authenticated {
			// 带着凭据仍然被拒：凭据无效，或者它没有读这个仓库的权限。
			return fmt.Errorf("%w: 远端拒绝了这次访问（凭据无效或无权读取该仓库）", ErrRemoteUnavailable)
		}
		return fmt.Errorf("%w: 远端拒绝了这次访问（限频，或该仓库是私有的；配置远端凭据可提高额度）",
			ErrRemoteUnavailable)
	default:
		return fmt.Errorf("%w: 远端返回 %d", ErrRemoteUnavailable, status)
	}
}
