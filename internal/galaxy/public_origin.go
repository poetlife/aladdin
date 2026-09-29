package galaxy

import (
	"fmt"
	"net/url"
	"strings"
)

// PublicPathPrefix 是发布页面在发布域上的路径前缀。
//
// 对外地址形如 `<发布域>/g/<工程标识>`。这一条常量与 PageURL 是**同一处
// 派生**：登记浏览器直连入口的那一处直接引用它，两边写两份必然漂移（表现是
// "页面地址变了，但中间件还放行着旧路径"）。
const PublicPathPrefix = "/g/"

// PublicOrigin 是发布态地址的派生入口（唯一入口）。
//
// 它同时回答两个问题，且两者的来源是同一个配置值（桶地址）：
//
//   - 改写时把 `asset://<资产标识>` 换成什么地址（AssetURL）；
//   - 内容安全策略里允许从哪个来源取资源（AllowedSource）。
//
// 两处各写一份的表现是"地址指向 A、策略允许 B"，而它的表现是"发布成功了但
// 什么都显示不出来"——一个只有拿到浏览器控制台才能归因的失败。
//
// 页面地址是另一件事：它在**发布域**上，而发布域必须与主应用不同源（发布物
// 里跑着用户写的脚本）。
type PublicOrigin struct {
	assets *url.URL
	page   *url.URL
}

// NewPublicOrigin 解析桶地址与发布域。
//
// 两者都必须是绝对 https 地址、有主机名、不带用户信息、不带查询串与 fragment、
// 路径为空或只有 "/"。配置校验也会拒绝这些取值，这里再挡一次是因为本构造函数
// 是唯一入口——绕过它就等于绕过这条约束。
//
// 注意**桶地址不等于公开区的范围**：公开区是同一个桶里的一个前缀，由键规则
// 给出（ReleaseObjectKey）。这一层只管主机，因为内容安全策略的来源表达式也只
// 到主机（见 AllowedSource）。
func NewPublicOrigin(bucketURL, publishBaseURL string) (PublicOrigin, error) {
	assets, err := parseOrigin("桶地址", bucketURL)
	if err != nil {
		return PublicOrigin{}, err
	}
	page, err := parseOrigin("发布域", publishBaseURL)
	if err != nil {
		return PublicOrigin{}, err
	}
	return PublicOrigin{assets: assets, page: page}, nil
}

// parseOrigin 是"一个地址能不能当作来源"的唯一判据。
func parseOrigin(what, raw string) (*url.URL, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("%s不是合法地址: %w", what, err)
	}
	if parsed.Scheme != "https" || parsed.Host == "" {
		return nil, fmt.Errorf("%s必须是带主机名的 https 地址，当前是 %q", what, raw)
	}
	if parsed.User != nil {
		return nil, fmt.Errorf("%s不得带用户信息，当前是 %q", what, raw)
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, fmt.Errorf("%s不得带查询串或 fragment，当前是 %q", what, raw)
	}
	if path := strings.TrimSuffix(parsed.Path, "/"); path != "" {
		return nil, fmt.Errorf("%s的路径必须为空或只有 /，当前是 %q", what, raw)
	}
	parsed.Path = ""
	parsed.RawPath = ""
	return parsed, nil
}

// IsZero 表示没有配置发布域。零值不是一条可用的发布配置。
func (o PublicOrigin) IsZero() bool { return o.assets == nil || o.page == nil }

// SiteRoot 返回一个工程的**发布根路径**，形如 `/g/<工程标识>/`。
//
// 它是站点内绝对地址的基准，也是构建产物里那些绝对路径的前缀（见 cli.md 的
// `project base`）。返回**路径**而不是完整地址：产物里写完整地址会让站点绑死在
// 当前这个发布域上，换一个域就要重新构建。
func (o PublicOrigin) SiteRoot(projectID string) string {
	return PublicPathPrefix + projectID + "/"
}

// AssetURL 返回一个公开区对象的地址。
//
// 键由**内容摘要与媒体类型**共同决定（见 promote.go 的 ReleaseObjectKey）：同一
// 份字节以两种类型上架是两个对象，各自回给浏览器正确的类型。
func (o PublicOrigin) AssetURL(digest, mediaType string) string {
	if o.IsZero() {
		return ""
	}
	return strings.TrimSuffix(o.assets.String(), "/") + "/" + ReleaseObjectKey(digest, mediaType)
}

// AllowedSource 返回内容安全策略里允许的资源来源。
//
// 只有来源（scheme + host），不带路径：策略匹配的是来源，多写一段路径既不会
// 更严也不会更松，只会让"这条策略到底允许了什么"变得更难读。
//
// **代价要说清楚：这个主机上不止有公开对象。** 私有资产、草稿与头像都在同一个
// 桶里，因此这条策略允许浏览器向整个主机发起取资源请求，最终能不能取到由**对象
// 权限**决定（私有对象匿名取被拒）。同桶之前这条兜底是结构性的，现在不是了
// （见 docs/design/galaxy/asset-library.md）。
func (o PublicOrigin) AllowedSource() string {
	if o.IsZero() {
		return ""
	}
	return o.assets.Scheme + "://" + o.assets.Host
}

// PageURL 返回一个工程的发布地址。
//
// **地址由服务端算好下发**，客户端不拼：客户端再拼一份就是第三个来源。
func (o PublicOrigin) PageURL(projectID string) string {
	if o.IsZero() {
		return ""
	}
	return strings.TrimSuffix(o.page.String(), "/") + PublicPathPrefix + projectID
}

// SplitSitePath 把一条发布地址拆成（工程标识，条目路径）（唯一入口）。
//
// 入口地址（`/g/<标识>` 与 `/g/<标识>/`）的条目路径为空——它等价于入口文件
// （见 docs/design/galaxy/site-model.md 的"一文件一地址"）。
//
// 第二个返回值为假表示这条地址根本不是一次发布请求。**不做前缀之外的任何
// 猜测**：多余的分段属于条目路径（那是发布态自己的事），而不属于工程标识。
//
// 它是**形状解析，不是判定**：真伪由集合成员测试回答（见 publish.go 的
// PublishedEntry）。
func SplitSitePath(requestPath string) (projectID, entryPath string, ok bool) {
	if !strings.HasPrefix(requestPath, PublicPathPrefix) {
		return "", "", false
	}
	rest := strings.TrimPrefix(requestPath, PublicPathPrefix)
	if rest == "" {
		return "", "", false
	}
	if slash := strings.IndexByte(rest, '/'); slash >= 0 {
		return rest[:slash], rest[slash+1:], true
	}
	return rest, "", true
}
