package galaxy

import "strings"

// CSPHeaderName 是承载内容安全策略的响应头名。
const CSPHeaderName = "Content-Security-Policy"

// cspDirectives 是发布物交付时附加的策略，顺序固定。
//
// 它**不改内容**：这是交付层的事。`static` 形态的文本逐字来自用户；`docs` 形态
// 的那些页是渲染出来的，而渲染规则按版本钉住（见 doc_render.go）。安全头走
// HTTP 响应头而不是正文，因此不违反"不注入"。
//
// 两层机制的分工必须说清楚：**扫描（validate.go）负责"告诉用户哪里错了"，
// 这个响应头负责"让错的无论如何也做不到"。** 只有前者会让一个漏检变成一次真实
// 的越界；只有后者会让用户只看到一张裂图而不知道原因。
func cspDirectives(origin PublicOrigin) []string {
	// 媒体来源必须**同时**含 `'self'` 与桶主机：取一张图要走两步——先请求发布域
	// 上的那个路径（得到一次重定向），再取桶上的对象。只允许桶的表现是浏览器在
	// 跳转前那一次请求就被拦下。
	mediaSources := "'self'"
	if allowed := origin.AllowedSource(); allowed != "" {
		mediaSources += " " + allowed
	}
	return []string{
		// 一切未显式允许的资源类型都被挡住。新特性默认落在这一条下面，
		// 因此"枚举清单写完那天开始过期"不会变成一次越界。
		"default-src 'none'",
		// 图片、音视频与字体：本工程在发布域上的路径（一次重定向）与桶上的
		// 公开对象（终点）。**两个来源都要允许。**
		"img-src " + mediaSources,
		"media-src " + mediaSources,
		"font-src " + mediaSources,
		// 允许内联的样式与脚本，也允许产物里以文件形式存在的 JS 与 CSS
		// ——它们与页面同源。
		"style-src 'self' 'unsafe-inline'",
		"script-src 'self' 'unsafe-inline'",
		// **脚本可以跑，但发不到别处**：同源请求可用，而指向任何外部地址的
		// fetch / XHR / WebSocket 都被挡下。
		//
		// `'self'` 是**发布域整体，不是本工程**：一个产物因此可以取另一个工程的
		// 公开脚本或图——但那本来就是公开内容，不构成越权；私有对象一个都取不到
		// （靠对象权限，见 docs/design/galaxy/asset-library.md）。
		"connect-src 'self'",
		// 发布物不能嵌套别的页面或插件，也不能把访问者填的东西提交出去。
		"frame-src 'none'",
		"object-src 'none'",
		"form-action 'none'",
		// 不能用 <base> 改写正文里所有相对地址的解析基准。
		"base-uri 'none'",
		// **谁能把发布物嵌进页面：只有主站。** 不同源挡住了脚本读主站的会话，
		// 但挡不住别人把发布域嵌进自己的页面（钓鱼框）。允许的祖先因此不是
		// `'self'`，而是主站那一个来源——发布物只该被主站的壳嵌。
		//
		// 这一条**不影响直接打开**：frame-ancestors 管的是"能不能被嵌"，不是
		// "能不能访问"，因此高级用户照旧可以直连发布域。
		//
		// 主站来源取不到时给出 `'none'`（什么都不许嵌），而不是省掉这一条：省掉
		// 等于回到"谁都能嵌"。发布启用时主站地址必然有值（配置校验），因此这一档
		// 只会在半套配置下面临——而那种配置启动就被拒了。
		frameAncestorsDirective(origin),
	}
}

// frameAncestorsDirective 给出 `frame-ancestors` 那一条。
func frameAncestorsDirective(origin PublicOrigin) string {
	if source := origin.FrameAncestorSource(); source != "" {
		return "frame-ancestors " + source
	}
	return "frame-ancestors 'none'"
}

// ContentSecurityPolicy 返回发布物交付时的内容安全策略取值（唯一入口）。
func ContentSecurityPolicy(origin PublicOrigin) string {
	return strings.Join(cspDirectives(origin), "; ")
}
