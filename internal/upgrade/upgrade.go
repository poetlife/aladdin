// Package upgrade 实现命令行的自更新：从本仓库的发布取回产物、校验、替换本机
// 二进制。
//
// 功能行为见 docs/design/cli/self-update.md。三条约束在这里最要紧：
//
//   - **升级源不可配置**：它固定指向本仓库的发布。一个"从哪升级"的配置项，
//     等于一个把任意二进制装进使用者机器的开关。
//   - **只支持发布产物**：不是发布流水线产出的二进制一律拒绝，且拒绝发生在
//     任何网络请求之前。
//   - **校验是替换的前置条件**：校验不通过的字节永远不会落到可执行的位置上。
package upgrade

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"
)

// 发布源固定指向本仓库：**不是配置项**。让"从哪升级"可配置，就等于给使用者
// 的机器留了一个安装任意二进制的开关（见 docs/design/cli/self-update.md）。
const (
	// Owner 是发布源所在仓库的所有者。
	Owner = "poetlife"
	// Repo 是发布源所在的仓库名。
	Repo = "aladdin"

	// defaultAPIBaseURL 是 GitHub API 的基址。
	defaultAPIBaseURL = "https://api.github.com"

	// requestTimeout 是单次出站请求（含产物下载）的时间上限。
	requestTimeout = 60 * time.Second
)

// Status 是本机版本与最新发布的关系。
type Status int

const (
	// StatusUpToDate 表示本机就是最新发布。
	StatusUpToDate Status = iota
	// StatusAhead 表示本机版本高于最新发布。**不降级**。
	StatusAhead
	// StatusOutdated 表示有新版本可用。
	StatusOutdated
)

// Source 是这一次决定所依据的发布源。
//
// GitHub Release 是**权威**源，主站镜像是它在"暂时取不到"时的替补——两者不是
// 并列的两个选项，因此这里记下来源只是为了说清楚"这次的材料是从哪来的"。
type Source int

const (
	// SourceGitHub 是本仓库的 GitHub Release，权威发布源。
	SourceGitHub Source = iota
	// SourceMirror 是主站上的兜底镜像，只留最新一份（见 docs/design/cli/self-update.md）。
	SourceMirror
)

// String 返回用在下行文案里的说法。
func (s Source) String() string {
	if s == SourceMirror {
		return "主站镜像"
	}
	return "发布"
}

// Slug 是给机器读的稳定取值（输出格式里用它，不用文案）。
func (s Source) Slug() string {
	if s == SourceMirror {
		return "mirror"
	}
	return "github"
}

// Decision 是一次"要不要升级、要升的话下哪个产物"的结论。
type Decision struct {
	Status  Status
	Current Version
	// Latest 是最新发布的版本。
	Latest Version
	// Source 是这次结论所依据的源。
	Source Source
	// Asset 与下面的两个地址只在 Status 为 StatusOutdated 时有意义。
	Asset        string
	assetURL     string
	checksumsURL string
}

// Result 是一次升级的结果。
type Result struct {
	From Version
	To   Version
	// Path 是被替换的那个文件（符号链接已解析）。
	Path string
}

// Options 是构造 Updater 所需的取值。
type Options struct {
	// Current 是本机版本，由构建期注入。
	Current string
	// Released 表示这份二进制来自发布产物，由发布流水线注入的标记决定。
	//
	// **这才是"能不能自更新"的判据，版本号的形态不是。** 本机构建（make build、
	// go install）注入的是同一处 -X main.version，在恰好处于某个 tag 的干净
	// 工作树上它同样是一个合法的 vX.Y.Z——按形态判断会把某人的工作副本
	// 当成发布产物替换掉。
	Released bool
	// GOOS 与 GOARCH 是挑选产物的平台；留空时取运行期平台。
	GOOS   string
	GOARCH string
	// ExecutablePath 是要替换的文件；留空时取当前进程的可执行文件。
	ExecutablePath string
	// APIBaseURL 覆盖 API 基址。**仅供测试**注入一个本机假发布源。
	APIBaseURL string
	// MirrorOrigin 是主站兜底镜像所在站点的源（形如 https://host:port），
	// 留空表示没有主站兜底。
	//
	// 产品里它由发布构建注入的官方地址推导而来（internal/config.OfficialSiteOrigin），
	// **不是配置项**：让"从哪升级"可配置，与 GitHub 那一处是同一个开关，
	// 只是换了个地址写而已（见包注释）。**仅供测试**直接注入。
	MirrorOrigin string
	// HTTPClient 可注入；为 nil 时用带超时的默认客户端。
	HTTPClient *http.Client
}

// Updater 是自更新的入口。
type Updater struct {
	current    Version
	goos       string
	goarch     string
	exePath    string
	apiBase    string
	mirrorBase string
	client     *http.Client
}

// New 构造自更新入口。
//
// 不是发布产物时**在这里就失败**，调用方因此不会为"本机是工作副本"这件事
// 发出任何网络请求。
func New(opts Options) (Updater, error) {
	if !opts.Released {
		return Updater{}, fmt.Errorf("%w：当前版本是 %q", ErrNotReleased, opts.Current)
	}

	current, ok := ParseVersion(opts.Current)
	if !ok {
		// 发布产物里注入了不可解析的版本号：流水线出了问题。不猜，也不更新。
		return Updater{}, fmt.Errorf("%w：发布产物注入了不可解析的版本号 %q", ErrNotReleased, opts.Current)
	}

	goos, goarch := opts.GOOS, opts.GOARCH
	if goos == "" {
		goos = runtime.GOOS
	}
	if goarch == "" {
		goarch = runtime.GOARCH
	}

	exePath := opts.ExecutablePath
	if exePath == "" {
		resolved, err := os.Executable()
		if err != nil {
			return Updater{}, fmt.Errorf("无法定位当前可执行文件：%w", err)
		}
		exePath = resolved
	}

	apiBase := opts.APIBaseURL
	if apiBase == "" {
		apiBase = defaultAPIBaseURL
	}
	// 空串表示没有主站兜底（源码构建恒为此，见 internal/config.OfficialSiteOrigin）。
	mirrorBase := ""
	if opts.MirrorOrigin != "" {
		mirrorBase = strings.TrimSuffix(opts.MirrorOrigin, "/") + mirrorPrefix
	}
	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: requestTimeout}
	}

	return Updater{
		current:    current,
		goos:       goos,
		goarch:     goarch,
		exePath:    exePath,
		apiBase:    strings.TrimSuffix(apiBase, "/"),
		mirrorBase: mirrorBase,
		client:     client,
	}, nil
}

// Current 返回本机版本。
func (u Updater) Current() Version { return u.current }

// ExecutablePath 返回升级会替换的那个文件。
func (u Updater) ExecutablePath() string { return u.exePath }

// Resolve 取最新发布，与本机版本比较，并在需要升级时挑好产物。
//
// 它只读元数据、不下载产物：只报告的形态到这里就结束。
func (u Updater) Resolve(ctx context.Context) (Decision, error) {
	rel, source, err := u.resolveSource(ctx)
	if err != nil {
		return Decision{}, err
	}

	latest, ok := ParseVersion(rel.Tag)
	if !ok {
		return Decision{}, fmt.Errorf("%s %q 的版本号不是 vX.Y.Z 形态", source, rel.Tag)
	}

	decision := Decision{Current: u.current, Latest: latest, Source: source}
	switch compare := u.current.Compare(latest); {
	case compare == 0:
		decision.Status = StatusUpToDate
		return decision, nil
	case compare > 0:
		decision.Status = StatusAhead
		return decision, nil
	}

	// 本平台没有产物时说清楚"有哪些"，而不是让人对着一次找不到文件去猜。
	name := assetName(rel.Tag, u.goos, u.goarch)
	assetURL, ok := rel.assetURL(name)
	if !ok {
		return Decision{}, fmt.Errorf("%s %s 里没有 %s 的产物（当前只发布 %s）",
			source, rel.Tag, u.goos+"/"+u.goarch, strings.Join(SupportedPlatforms, "、"))
	}
	checksumsURL, ok := rel.assetURL(checksumsAsset)
	if !ok {
		return Decision{}, fmt.Errorf("%s %s 里没有校验和清单 %s", source, rel.Tag, checksumsAsset)
	}

	decision.Status = StatusOutdated
	decision.Asset = name
	decision.assetURL = assetURL
	decision.checksumsURL = checksumsURL
	return decision, nil
}

// resolveSource 取最新发布的元数据，返回它是从哪个源取到的。
//
// 顺序是刻意的：**先试权威发布源**，只有当它的失败属于暂时性时才落到主站镜像。
// 两个理由：GitHub 才是"最新"的定义处，镜像只是它的副本；而且镜像的字节来自同
// 一次 GitHub 发布，先问 GitHub 拿到的永远不会比镜像旧。
//
// 回退**只发生在这一步**。一旦选定了源，产物与校验和清单都从同一个源取——tag、
// 清单、字节三者同代由构造保证，而不是靠两次请求之间"应该没变"的假设。
func (u Updater) resolveSource(ctx context.Context) (release, Source, error) {
	rel, err := u.fetchLatest(ctx)
	if err == nil {
		return rel, SourceGitHub, nil
	}
	// 没有主站兜底（源码构建），或这个失败本来就不该重试：原样报出去。
	if u.mirrorBase == "" || !isTransient(err) {
		return release{}, SourceGitHub, err
	}

	mirror, mirrorErr := u.fetchMirrorLatest(ctx)
	if mirrorErr != nil {
		// 两路都要交代。只说"请稍后重试"会让人以为等一等就好了，而这里
		// 是两路同时不可用——其中一路还是本就为了兜底才存在的那一路。
		return release{}, SourceGitHub, fmt.Errorf("%w；主站镜像也不可用：%w", err, mirrorErr)
	}
	return mirror, SourceMirror, nil
}

// Upgrade 下载产物、校验、解包并替换本机二进制。
//
// 顺序是刻意的：**校验通过是替换的前置条件**。摘要不符、清单里没有本产物、
// 或解包失败时，本机二进制一个字节都不会变。
func (u Updater) Upgrade(ctx context.Context, decision Decision) (Result, error) {
	if decision.Status != StatusOutdated {
		return Result{}, errors.New("没有可升级的版本")
	}

	archive, err := u.fetch(ctx, decision.assetURL, maxArchiveBytes)
	if err != nil {
		return Result{}, fmt.Errorf("下载产物失败：%w", err)
	}

	sums, err := u.fetch(ctx, decision.checksumsURL, maxChecksumsBytes)
	if err != nil {
		return Result{}, fmt.Errorf("下载校验和失败：%w", err)
	}
	want, ok := parseChecksums(sums)[decision.Asset]
	if !ok {
		// **不退化成"找不到校验和就跳过校验"**：那会让校验在最需要它的
		// 时候（产物被掉包、被截断）恰好不生效。
		return Result{}, fmt.Errorf("校验和清单里没有 %s", decision.Asset)
	}
	got := sha256.Sum256(archive)
	if !strings.EqualFold(hex.EncodeToString(got[:]), want) {
		return Result{}, fmt.Errorf("%s 的校验和不符，产物可能已损坏或被替换", decision.Asset)
	}

	binary, err := extractBinary(bytes.NewReader(archive))
	if err != nil {
		return Result{}, err
	}

	if err := Replace(u.exePath, binary); err != nil {
		return Result{}, err
	}
	return Result{From: u.current, To: decision.Latest, Path: u.exePath}, nil
}

// fetch 取一份发布里的文件，读取长度有上界。
//
// 上界防的不是"产物本来很大"，而是"响应的长度不是我期待的那样"：一个被接管
// 的响应可以是无限的，而它的代价落在使用者的机器上。
func (u Updater) fetch(ctx context.Context, url string, limit int64) ([]byte, error) {
	req, err := newRequest(ctx, url, "application/octet-stream")
	if err != nil {
		return nil, err
	}
	resp, err := u.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, limit))
}
