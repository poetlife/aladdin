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

	// mirrorPrefix 是主站上 CLI 镜像的固定前缀。
	//
	// 路径里**没有版本号字面量**：tag 只出现在元数据的内容里。否则每发一版都要
	// 有一个新的入口，而客户端里写死的那一个会立刻过期。
	mirrorPrefix = "/cli/latest"

	// mirrorMetadataAsset 是主站镜像里版本元数据的文件名。
	//
	// 它与 SHA256SUMS、与产物的命名一样是**对外契约**：由部署步骤写出、由这里
	// 消费（见 docs/release.md 与 docs/deploy.md）。
	mirrorMetadataAsset = "version.json"

	// githubAccept 与 githubAPIVersion 是 GitHub API 要求的请求头取值。
	githubAccept       = "application/vnd.github+json"
	githubAPIVersion   = "2022-11-28"
	userAgent          = "aladdin-cli"
	maxMetadataBytes   = 1 << 20
	maxChecksumsBytes  = 1 << 20
	checksumsSeparator = "  "
)

// githubRelease 是一次 GitHub 发布里本模块用得到的部分（线上格式）。
type githubRelease struct {
	TagName string        `json:"tag_name"`
	Assets  []githubAsset `json:"assets"`
}

// githubAsset 是发布里附着的一个文件。
type githubAsset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
}

// mirrorMetadata 是主站镜像的 version.json（线上格式）。
//
// assets 只列该镜像**确实提供**的产物名，且只能**收窄**可用集合：客户端仍然按
// 对外契约从 tag 推出自己要的那个名字，再要求它出现在这里。命名权威只有一处。
type mirrorMetadata struct {
	Tag    string   `json:"tag"`
	Assets []string `json:"assets"`
}

// release 是一次发布里本模块用得到的部分，已从具体发布源归一化。
//
// 归一化是为了让"挑产物"只写一遍：GitHub 的地址来自 release 的 assets 列表，
// 主站镜像的地址由固定前缀拼出来，两个源的差异止步于这里。
type release struct {
	Tag string
	// assets 把产物名映射到下载地址，只含该源确实提供的那几个名字。
	assets map[string]string
}

// assetURL 取某个产物的下载地址。
func (r release) assetURL(name string) (string, bool) {
	url, ok := r.assets[name]
	return url, ok
}

// transientError 标记"换一路再试可能就成"的失败。
//
// 失败分类的依据仍然是"调用方该做什么"：暂时性失败的出路是换一路重试，其余失败的
// 出路是把问题原样报给人看。**分类只决定要不要回退，不改变校验语义**——回退之后
// 走的仍是同一套"清单里必须有本产物、摘要必须相符"。
type transientError struct{ err error }

func (e transientError) Error() string { return e.err.Error() }
func (e transientError) Unwrap() error { return e.err }

// isTransient 报告一次失败是否值得换一路重试。
func isTransient(err error) bool {
	var t transientError
	return errors.As(err, &t)
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

// fetchLatest 取权威发布源（GitHub Release）的最新元数据。
func (u Updater) fetchLatest(ctx context.Context) (release, error) {
	req, err := newRequest(ctx, u.latestReleaseURL(), githubAccept)
	if err != nil {
		return release{}, err
	}
	resp, err := u.client.Do(req)
	if err != nil {
		// 网络层失败（DNS、连接、超时）也算暂时性：换一路常常就通了。
		return release{}, transientError{fmt.Errorf("读取最新发布失败：%w", err)}
	}
	defer func() { _ = resp.Body.Close() }()

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		// "这里确实没有发布"是一个确定的答案，不该拿镜像去盖过它。
		return release{}, errors.New("发布源上没有找到任何发布")
	case http.StatusForbidden, http.StatusTooManyRequests:
		return release{}, transientError{
			errors.New("发布源拒绝了这次请求（可能是访问频率限制），请稍后重试"),
		}
	default:
		err := fmt.Errorf("发布源返回 HTTP %d", resp.StatusCode)
		if resp.StatusCode >= 500 {
			return release{}, transientError{err}
		}
		return release{}, err
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxMetadataBytes))
	if err != nil {
		return release{}, fmt.Errorf("读取发布元数据失败：%w", err)
	}
	var wire githubRelease
	if err := json.Unmarshal(body, &wire); err != nil {
		return release{}, fmt.Errorf("发布元数据不是合法 JSON：%w", err)
	}
	if wire.TagName == "" {
		return release{}, errors.New("发布元数据里没有版本号")
	}

	assets := make(map[string]string, len(wire.Assets))
	for _, asset := range wire.Assets {
		assets[asset.Name] = asset.URL
	}
	return release{Tag: wire.TagName, assets: assets}, nil
}

// fetchMirrorLatest 取主站镜像的版本元数据。
//
// 主站上只有"当前最新"这一份（覆盖写，不滚历史），因此这里没有、也不需要任何
// 历史版本的概念。
func (u Updater) fetchMirrorLatest(ctx context.Context) (release, error) {
	body, err := u.fetch(ctx, u.mirrorBase+"/"+mirrorMetadataAsset, maxMetadataBytes)
	if err != nil {
		return release{}, fmt.Errorf("读取主站镜像元数据失败：%w", err)
	}

	var wire mirrorMetadata
	if err := json.Unmarshal(body, &wire); err != nil {
		return release{}, fmt.Errorf("主站镜像的元数据不是合法 JSON：%w", err)
	}
	if wire.Tag == "" {
		return release{}, errors.New("主站镜像的元数据里没有版本号")
	}

	assets := make(map[string]string, len(wire.Assets))
	for _, name := range wire.Assets {
		assets[name] = u.mirrorBase + "/" + name
	}
	return release{Tag: wire.Tag, assets: assets}, nil
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
