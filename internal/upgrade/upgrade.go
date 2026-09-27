// Package upgrade 实现命令行的自更新：从本仓库的发布取回产物、校验、替换本机
// 二进制。
//
// 功能行为见 docs/design/cli/self-update.md。三条约束在这里最要紧：
//
//   - **升级源不可配置**：它固定指向本仓库的发布。一个"从哪升级"的配置项，
//     等于一个把任意二进制装进使用者机器的开关。
//   - **只支持发布产物**：本机版本不是严格 vX.Y.Z 时一律拒绝，且拒绝发生在
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

// Decision 是一次"要不要升级、要升的话下哪个产物"的结论。
type Decision struct {
	Status  Status
	Current Version
	// Latest 是最新发布的版本。
	Latest Version
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
	// GOOS 与 GOARCH 是挑选产物的平台；留空时取运行期平台。
	GOOS   string
	GOARCH string
	// ExecutablePath 是要替换的文件；留空时取当前进程的可执行文件。
	ExecutablePath string
	// APIBaseURL 覆盖 API 基址。**仅供测试**注入一个本机假发布源。
	APIBaseURL string
	// HTTPClient 可注入；为 nil 时用带超时的默认客户端。
	HTTPClient *http.Client
}

// Updater 是自更新的入口。
type Updater struct {
	current Version
	goos    string
	goarch  string
	exePath string
	apiBase string
	client  *http.Client
}

// New 构造自更新入口。
//
// 本机版本不是严格 vX.Y.Z 时**在这里就失败**，调用方因此不会为"本机是工作
// 副本"这件事发出任何网络请求。
func New(opts Options) (Updater, error) {
	current, ok := ParseVersion(opts.Current)
	if !ok {
		return Updater{}, fmt.Errorf("%w：当前版本是 %q", ErrNotReleased, opts.Current)
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
	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: requestTimeout}
	}

	return Updater{
		current: current,
		goos:    goos,
		goarch:  goarch,
		exePath: exePath,
		apiBase: strings.TrimSuffix(apiBase, "/"),
		client:  client,
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
	release, err := u.fetchLatest(ctx)
	if err != nil {
		return Decision{}, err
	}

	latest, ok := ParseVersion(release.TagName)
	if !ok {
		return Decision{}, fmt.Errorf("发布 %q 的版本号不是 vX.Y.Z 形态", release.TagName)
	}

	decision := Decision{Current: u.current, Latest: latest}
	switch compare := u.current.Compare(latest); {
	case compare == 0:
		decision.Status = StatusUpToDate
		return decision, nil
	case compare > 0:
		decision.Status = StatusAhead
		return decision, nil
	}

	// 本平台没有产物时说清楚"有哪些"，而不是让人对着一次找不到文件去猜。
	name := assetName(release.TagName, u.goos, u.goarch)
	assetURL, ok := release.assetURL(name)
	if !ok {
		return Decision{}, fmt.Errorf("发布 %s 里没有 %s 的产物（当前只发布 %s）",
			release.TagName, u.goos+"/"+u.goarch, strings.Join(SupportedPlatforms, "、"))
	}
	checksumsURL, ok := release.assetURL(checksumsAsset)
	if !ok {
		return Decision{}, fmt.Errorf("发布 %s 里没有校验和清单 %s", release.TagName, checksumsAsset)
	}

	decision.Status = StatusOutdated
	decision.Asset = name
	decision.assetURL = assetURL
	decision.checksumsURL = checksumsURL
	return decision, nil
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
