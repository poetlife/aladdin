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
// 它同时回答两个问题，且两者的来源是同一个配置值（公开桶地址）：
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

// NewPublicOrigin 解析公开桶地址与发布域。
//
// 两者都必须是绝对 https 地址、有主机名、不带用户信息、不带查询串与 fragment、
// 路径为空或只有 "/"。配置校验也会拒绝这些取值，这里再挡一次是因为本构造函数
// 是唯一入口——绕过它就等于绕过这条约束。
func NewPublicOrigin(assetsBucketURL, publishBaseURL string) (PublicOrigin, error) {
	assets, err := parseOrigin("公开桶地址", assetsBucketURL)
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

// AssetURL 返回一个公开区对象（按内容摘要寻址）的地址。
func (o PublicOrigin) AssetURL(digest string) string {
	if o.IsZero() {
		return ""
	}
	return strings.TrimSuffix(o.assets.String(), "/") + "/" + digest
}

// AllowedSource 返回内容安全策略里允许的资源来源。
//
// 只有来源（scheme + host），不带路径：策略匹配的是来源，多写一段路径既不会
// 更严也不会更松，只会让"这条策略到底允许了什么"变得更难读。
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
