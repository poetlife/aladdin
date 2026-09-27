package upgrade

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const (
	// checksumsAsset 是校验和清单在发布里的名字。
	//
	// 它是**对外契约**的一部分：由 make release-build 产出、由这里消费
	// （见 docs/release.md）。
	checksumsAsset = "SHA256SUMS"

	// githubAccept 与 githubAPIVersion 是 GitHub API 要求的请求头取值。
	githubAccept       = "application/vnd.github+json"
	githubAPIVersion   = "2022-11-28"
	userAgent          = "aladdin-cli"
	maxMetadataBytes   = 1 << 20
	maxChecksumsBytes  = 1 << 20
	checksumsSeparator = "  "
)

// githubRelease 是一次发布里本模块用得到的部分。
type githubRelease struct {
	TagName string        `json:"tag_name"`
	Assets  []githubAsset `json:"assets"`
}

// githubAsset 是发布里附着的一个文件。
type githubAsset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
}

// assetURL 取某个产物的下载地址。
func (r githubRelease) assetURL(name string) (string, bool) {
	for _, asset := range r.Assets {
		if asset.Name == name {
			return asset.URL, true
		}
	}
	return "", false
}

// latestReleaseURL 是本仓库最新一次发布的元数据地址。
//
// 它固定指向 Owner/Repo，不由配置或参数给出（见包注释）。
//
// 只看 latest：发布流水线会清理更早的 Release，指定旧版本会在若干次发版之后
// 开始 404（见 docs/release.md）。
func (u Updater) latestReleaseURL() string {
	return u.apiBase + "/repos/" + Owner + "/" + Repo + "/releases/latest"
}

// newRequest 构造一次带必需请求头的出站请求。
//
// GitHub 对没有 User-Agent 的请求直接拒绝，因此 UA 不是可选的；元数据与产物
// 两条请求共用这一处，免得其中一条漏掉。
func newRequest(ctx context.Context, url, accept string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("X-GitHub-Api-Version", githubAPIVersion)
	req.Header.Set("User-Agent", userAgent)
	return req, nil
}

// fetchLatest 取最新一次发布的元数据。
//
// 失败分类的依据是"调用方该做什么"：没有发布与频率限制是两件事，前者的
// 出路是等一次发版，后者的出路是稍后重试。
func (u Updater) fetchLatest(ctx context.Context) (githubRelease, error) {
	req, err := newRequest(ctx, u.latestReleaseURL(), githubAccept)
	if err != nil {
		return githubRelease{}, err
	}
	resp, err := u.client.Do(req)
	if err != nil {
		return githubRelease{}, fmt.Errorf("读取最新发布失败：%w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return githubRelease{}, errors.New("发布源上没有找到任何发布")
	case http.StatusForbidden, http.StatusTooManyRequests:
		return githubRelease{}, errors.New("发布源拒绝了这次请求（可能是访问频率限制），请稍后重试")
	default:
		return githubRelease{}, fmt.Errorf("发布源返回 HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxMetadataBytes))
	if err != nil {
		return githubRelease{}, fmt.Errorf("读取发布元数据失败：%w", err)
	}
	var release githubRelease
	if err := json.Unmarshal(body, &release); err != nil {
		return githubRelease{}, fmt.Errorf("发布元数据不是合法 JSON：%w", err)
	}
	if release.TagName == "" {
		return githubRelease{}, errors.New("发布元数据里没有版本号")
	}
	return release, nil
}

// parseChecksums 解析校验和清单：每行是"摘要 + 两个空格 + 文件名"。
//
// 只认这一种形态。清单的格式是对外契约的一部分（见 docs/release.md），而
// 宽松解析（任意个空格、反斜杠转义、带路径前缀）会让"清单里有这一条"这个
// 判断依赖解析器的宽容度——它的失败方向恰好是**放过一次校验**。
func parseChecksums(data []byte) map[string]string {
	sums := make(map[string]string)
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimRight(line, "\r")
		sum, name, ok := strings.Cut(line, checksumsSeparator)
		if !ok || sum == "" || name == "" {
			continue
		}
		sums[name] = sum
	}
	return sums
}
