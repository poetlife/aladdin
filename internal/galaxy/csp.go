package galaxy

import "strings"

// CSPHeaderName 是承载内容安全策略的响应头名。
const CSPHeaderName = "Content-Security-Policy"

// cspDirectives 是发布物交付时附加的策略，顺序固定。
//
// 它**不改正文**：这是交付层的事，正文的每一个字节仍来自用户（见 document.go）。
// 安全头走 HTTP 响应头而不是正文，因此不违反"不注入"那一条。
//
// 两层的分工必须说清楚：**扫描（validate.go）负责"告诉用户哪里错了"，这个
// 响应头负责"让错的无论如何也做不到"。** 只有前者会让一个漏检变成一次真实的
// 越界；只有后者会让用户只看到一张裂图而不知道原因。
func cspDirectives(origin PublicOrigin) []string {
	allowed := origin.AllowedSource()
	return []string{
		// 一切未显式允许的资源类型都被挡住。新特性默认落在这一条下面，
		// 因此"枚举清单写完那天开始过期"不会变成一次越界。
		"default-src 'none'",
		// 图片、视频、音频只能来自本工程的公开资产。
		"img-src " + allowed,
		"media-src " + allowed,
		// 正文就是内联的样式与脚本，因此必须允许内联——但只允许内联：
		// 外部脚本没有来源可用，取不到任何字节。
		"script-src 'unsafe-inline'",
		"style-src 'unsafe-inline'",
		// **脚本可以跑，但发不出任何请求**：fetch / XHR / WebSocket /
		// sendBeacon 全被挡下。这是"用户可以写交互，但不能把访问者的数据
		// 送到任何地方"的落点。
		"connect-src 'none'",
		// 发布物不能嵌套别的页面或插件，也不能把访问者填的东西提交出去。
		"frame-src 'none'",
		"object-src 'none'",
		"form-action 'none'",
		// 不能用 <base> 改写正文里所有相对地址的解析基准。
		"base-uri 'none'",
	}
}

// ContentSecurityPolicy 返回发布物交付时的内容安全策略取值（唯一入口）。
func ContentSecurityPolicy(origin PublicOrigin) string {
	return strings.Join(cspDirectives(origin), "; ")
}
