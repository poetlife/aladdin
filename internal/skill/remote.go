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

	// FetchTree 取回该提交下 subPath 之内的文本条目。subPath 为空表示仓库根。
	//
	// 返回的是**字节**，不是路径：远端实现把内容读进内存交出来，全程不落盘。
	// **成本只随平台要收的东西增长**：目录树先给一遍（一次请求），只有要收的
	// 条目才去取字节，因此"上游有多大"不影响这次纳管的内存与请求数（见 Tree）。
	FetchTree(ctx context.Context, repo Repository, commit, subPath string) (Tree, error)
}

const (
	// remoteTimeout 是单次远端请求的超时。
	//
	// 它管的是**一次请求**，不是整条纳管流程：给整条流程设总超时会把"远端慢"与
	// "远端挂了"变成同一个结论，而两者的应对不同。
	remoteTimeout = 30 * time.Second
	// githubAPIBase 是 GitHub REST API 的根。
	githubAPIBase = "https://api.github.com"
	// jsonResponseMaxBytes 是**元数据响应**的读上限。
	//
	// 1 MiB 是给仓库、提交这类小响应留的余量；唯一一个大的是目录树——`recursive=1`
	// 会把整棵树的每一条都回过来，一个近四千文件的仓库轻松越过 1 MiB。**那个上限
	// 太小，表现是 "unexpected EOF"**：JSON 读到一半被截断，而错误信息指向解析，
	// 看不出是"响应比我们以为的大"。
	//
	// 取 16 MiB 仍然是个界：GitHub 自己对递归目录树有 7 MB / 十万条的上限，超过就
	// 回一个 `truncated`（那条由本文件当成拒绝处理），所以真实响应到不了这里。
	jsonResponseMaxBytes = 16 << 20
	// githubUserAgent 是请求头里的标识。GitHub 拒绝没有 User-Agent 的请求。
	githubUserAgent = "aladdin"
	// githubAccept 是**元数据请求**（仓库、提交、目录树）的媒体类型。
	githubAccept = "application/vnd.github+json"
	// githubRawAccept 是**取对象字节**时的媒体类型。
	//
	// **少了它，拿回来的是 JSON 而不是字节**：那一端点在默认媒体类型下回一个
	// `{"content": "<base64>", ...}`，把那个当内容读下去的表现是"每一份文件都被
	// 判成不是文本"——而错误信息会指向文件本身，看不出问题出在请求头上。
	githubRawAccept = "application/vnd.github.raw"
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

// FetchTree 实现 Remote：先问目录树，再逐条取要收的字节。
//
// 两步的理由见 repo_tree.go：成本只随**平台要收的东西**增长，而不是随上游仓库
// 的大小增长。
func (g *GithubRemote) FetchTree(ctx context.Context, repo Repository, commit, subPath string) (Tree, error) {
	plan, err := g.planTree(ctx, repo, commit, subPath)
	if err != nil {
		return Tree{}, err
	}
	tree := Tree{Skipped: plan.Skipped}
	var expanded int64
	for _, blob := range plan.Files {
		if len(tree.Files) >= MaxFiles {
			return Tree{}, fmt.Errorf("%w: 文件数超过上限 %d", ErrPackageInvalid, MaxFiles)
		}
		data, err := g.fetchBlob(ctx, repo, blob.SHA)
		if err != nil {
			return Tree{}, err
		}
		// **"是不是文本"要到这一步才知道**：目录树只给大小，不给内容。不是文本
		// 的跳过并点名，而不是让整份包作废（见 docs/design/skill/onboarding.md）。
		if !isText(data) {
			tree.Skipped = append(tree.Skipped, blob.Path)
			continue
		}
		if expanded+int64(len(data)) > MaxPackageBytes {
			return Tree{}, fmt.Errorf("%w: 取回的文本累计超过 %d 字节",
				ErrPackageInvalid, MaxPackageBytes)
		}
		expanded += int64(len(data))
		tree.Files = append(tree.Files, FetchedFile{Path: blob.Path, Data: data})
	}
	return tree, nil
}

// planTree 问出目录树并折成一份取字节的计划。
func (g *GithubRemote) planTree(ctx context.Context, repo Repository, commit, subPath string) (repoPlan, error) {
	var payload struct {
		Tree      []repoTreeEntry `json:"tree"`
		Truncated bool            `json:"truncated"`
	}
	path := fmt.Sprintf("/repos/%s/%s/git/trees/%s?recursive=1",
		url.PathEscape(repo.Owner), url.PathEscape(repo.Name), url.PathEscape(commit))
	if err := g.getJSON(ctx, path, &payload); err != nil {
		return repoPlan{}, err
	}
	// `truncated` 表示这棵树大到一次给不完。**它是拒绝而不是尽力而为**：只拿到
	// 一半的目录树会让纳管进来的包悄悄少一批文件，而那个"少"没有任何地方说得清。
	if payload.Truncated {
		return repoPlan{}, fmt.Errorf(
			"%w: 仓库的目录树一次给不完（超过 GitHub 的上限），请用来源的子路径收窄范围",
			ErrRemoteUnavailable)
	}
	return planRepoTree(payload.Tree, subPath)
}

// fetchBlob 取一条对象的字节。
//
// **按 sha 取，不按路径取**：sha 是固定长度的十六进制，拼不出别的东西来；而路径
// 要拼进 URL，多一处转义就多一处可能拼错的地方。
func (g *GithubRemote) fetchBlob(ctx context.Context, repo Repository, sha string) ([]byte, error) {
	path := fmt.Sprintf("/repos/%s/%s/git/blobs/%s",
		url.PathEscape(repo.Owner), url.PathEscape(repo.Name), url.PathEscape(sha))
	resp, err := g.do(ctx, path, githubRawAccept)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if err := classifyStatus(resp.StatusCode, g.token != ""); err != nil {
		return nil, err
	}
	// 单条字节的上限在这里就生效：读多一个字节即判超限，而不是先把整条收下来。
	data, err := io.ReadAll(io.LimitReader(resp.Body, MaxFileBytes+1))
	if err != nil {
		return nil, fmt.Errorf("%w: 读取对象失败: %w", ErrRemoteUnavailable, err)
	}
	if len(data) > MaxFileBytes {
		return nil, fmt.Errorf("%w: 取回的对象超过单文件上限 %d 字节", ErrPackageInvalid, MaxFileBytes)
	}
	return data, nil
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
	resp, err := g.do(ctx, path, githubAccept)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if err := classifyStatus(resp.StatusCode, g.token != ""); err != nil {
		return err
	}
	decoder := json.NewDecoder(io.LimitReader(resp.Body, jsonResponseMaxBytes))
	if err := decoder.Decode(out); err != nil {
		return fmt.Errorf("%w: 远端返回的不是预期的 JSON: %w", ErrRemoteUnavailable, err)
	}
	return nil
}

// do 发起一次请求。**地址由这里拼**：只有 API 根、路径段与认证，没有别的来源。
//
// gosec 会在下面两行报 SSRF（G704：请求地址由调用方输入汇入）。**这个结论在这里
// 是已知且被处置的**，而不是误报——本模块的整条设计就是为了它：
//
//   - 调用方给的那个地址**从来不会**成为请求目标：它只在 ParseRepositoryURL 里被
//     拆成（owner，repo）两段，每段走字符白名单（见 source.go）；
//   - 拼进这里的路径只有那两段、一个引用、一个提交标识或一个 git 对象标识，全部
//     经 url.PathEscape；
//   - 前缀是常量 githubAPIBase，别的任何主机都到不了这一行。
//
// 抑制写在被报的那两行上，而不是在配置文件里关掉整条规则：后者会让这个仓库里
// **将来**真正需要看见的同类问题一起消失。
func (g *GithubRemote) do(ctx context.Context, path, accept string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, githubAPIBase+path, nil) //nolint:gosec // G704：地址由白名单过的两段拼成，见上
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrRemoteUnavailable, err)
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", githubUserAgent)
	if g.token != "" {
		req.Header.Set("Authorization", "Bearer "+g.token)
	}
	resp, err := g.client.Do(req) //nolint:gosec // G704：同上
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrRemoteUnavailable, err)
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
