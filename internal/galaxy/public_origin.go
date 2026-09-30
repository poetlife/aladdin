package galaxy

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/poetlife/aladdin/internal/loopback"
)

// PublicPathPrefix 是发布页面的路径前缀。**分享地址与内容地址共用它**：分享地址
// 是 `<主站>/g/<工程标识>`，内容是 `<发布域>` 下的同一条路径——两者只有主机不同。
//
// 这一条常量与 ContentURL / ShareURL 是**同一处派生**：登记浏览器直连入口的那一处
// 直接引用它，两边写两份必然漂移（表现是"页面地址变了，但中间件还放行着旧路径"）。
const PublicPathPrefix = "/g/"

// PublicOrigin 是发布态地址的派生入口（唯一入口）。
//
// 它同时回答三个问题，且来源是同一批配置值：
//
//   - 改写时把 `asset://<资产标识>` 换成什么地址（AssetURL）；
//   - 内容安全策略里允许从哪个来源取资源（AllowedSource），以及允许谁嵌入它
//     （FrameAncestorSource）；
//   - 一个槽对外的**分享地址**（ShareURL）与 iframe 要加载的**内容地址**
//     （ContentURL）。
//
// 两处各写一份的表现是"地址指向 A、策略允许 B"，而它的表现是"发布成功了但
// 什么都显示不出来"——一个只有拿到浏览器控制台才能归因的失败。
//
// 分享地址与内容地址是两件事：**内容地址在发布域上**（它是内容与构建产物的地址
// 空间，发布物里跑着用户写的脚本，必须与主应用不同源），而**分享地址在主站上**
// ——对外给出去的是主站上的壳，壳再用跨源沙箱 iframe 去取内容（见
// docs/design/galaxy/publication.md 的"主站壳"）。
type PublicOrigin struct {
	assets *url.URL
	page   *url.URL
	app    *url.URL
}

// NewPublicOrigin 解析桶地址、发布域与主站的对外地址。
//
// 三者都必须是绝对地址、有主机名、不带用户信息、不带查询串与 fragment、路径为空
// 或只有 "/"。配置校验也会拒绝这些取值，这里再挡一次是因为本构造函数是唯一入口
// ——绕过它就等于绕过这条约束。
//
// 主站地址（`public_base_url`）为空是合法的：那表示"未启用发布"这条常态路径上
// 的零配置。**发布启用时它必须非空**，由配置校验保证——没有它分享地址拼不出来。
//
// 注意**桶地址不等于公开区的范围**：公开区是同一个桶里的一个前缀，由键规则
// 给出（ReleaseObjectKey）。这一层只管主机，因为内容安全策略的来源表达式也只
// 到主机（见 AllowedSource）。
func NewPublicOrigin(bucketURL, publishBaseURL, appBaseURL string) (PublicOrigin, error) {
	assets, err := parseOrigin("桶地址", bucketURL)
	if err != nil {
		return PublicOrigin{}, err
	}
	page, err := parseOrigin("发布域", publishBaseURL)
	if err != nil {
		return PublicOrigin{}, err
	}
	app, err := parseAppOrigin("主站对外地址", appBaseURL)
	if err != nil {
		return PublicOrigin{}, err
	}
	return PublicOrigin{assets: assets, page: page, app: app}, nil
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

// parseAppOrigin 解析主站的对外地址，空串返回 nil（未配置发布时的常态）。
//
// 它比 parseOrigin 宽一档：**本地回环主机允许 http**。这与配置里
// `public_base_url` 的取值要求是同一条（见 internal/config 的 checkOriginShape），
// 那一处允许它是因为本地开发没有证书。这里跟着放宽不是松懈——主站地址只用来
// 拼分享地址与写 `frame-ancestors`，两者在 http 下都成立；而"发布域与主站不同源"
// 这条安全约束因此也没有被放松（两张地址仍然必须落在不同的站点上）。
func parseAppOrigin(what, raw string) (*url.URL, error) {
	if raw == "" {
		return nil, nil
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("%s不是合法地址: %w", what, err)
	}
	if parsed.Host == "" {
		return nil, fmt.Errorf("%s必须是带主机名的绝对地址，当前是 %q", what, raw)
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
	// 与 internal/config 的 checkOriginShape 同形：先算出"这是回环上的 http"，
	// 再判它是不是唯一被放行的非 https 情形。
	loopbackHTTP := parsed.Scheme == "http" && loopback.IsHost(parsed.Hostname())
	if parsed.Scheme != "https" && !loopbackHTTP {
		return nil, fmt.Errorf("%s必须是 https（仅本地回环主机允许 http），当前是 %q", what, raw)
	}
	parsed.Path = ""
	parsed.RawPath = ""
	return parsed, nil
}

// SiteRoot 返回**某一个内容槽**的**发布根路径**。
//
// site 槽是 `/g/<工程标识>/`，docs 槽是它下面的 `/g/<工程标识>/docs/`。它是站点
// 内绝对地址的基准，也是构建产物里那些绝对路径的前缀（见 cli.md 的
// `project base`）。返回**路径**而不是完整地址：产物里写完整地址会让站点绑死在
// 当前这个发布域上，换一个域就要重新构建。
func (o PublicOrigin) SiteRoot(projectID string, slot ContentSlot) string {
	return slotRootPath(projectID, slot)
}

// slotRootPath 返回一个槽在 `/g/` 之下的根路径（含结尾斜杠）。
//
// **两个槽各占一段，与另一个槽是否存在无关。** 这是"槽可加"的前提：文档的地址
// 若是随站点槽有无而变，后加一个槽就会挪走一条已经发出去的地址。
func slotRootPath(projectID string, slot ContentSlot) string {
	return PublicPathPrefix + projectID + "/" + slot.RootSuffix()
}

// AssetURL 返回一个公开区对象的地址。
//
// 键由**工程标识、内容摘要与媒体类型**共同决定（见 promote.go 的 ReleaseObjectKey）：
// 公开区按工程切分，段内按（内容摘要，媒体类型）寻址——同一份字节以两种类型上架是
// 两个对象，各自回给浏览器正确的类型。
func (o PublicOrigin) AssetURL(projectID, digest, mediaType string) string {
	if o.IsZero() {
		return ""
	}
	return strings.TrimSuffix(o.assets.String(), "/") + "/" + ReleaseObjectKey(projectID, digest, mediaType)
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

// ContentURL 返回**某一个内容槽**在发布域上的内容地址（槽根，不带结尾斜杠）。
//
// 它是主站壳里 iframe 的 `src`、也是发布域那条直连入口的入口地址。**它不是对外
// 分享出去的那一条**——分享地址是 ShareURL（见下）。
//
// **地址由服务端算好下发**，客户端不拼：客户端再拼一份就是第三个来源。
func (o PublicOrigin) ContentURL(projectID string, slot ContentSlot) string {
	if o.IsZero() {
		return ""
	}
	return o.pageBase() + strings.TrimSuffix(slotRootPath(projectID, slot), "/")
}

// ShareURL 返回**某一个内容槽**对外分享的地址（主站包装）。
//
// 它落在主站上，路径与内容地址同形：打开它得到的是主站的壳，壳再把跨源沙箱
// iframe 指向发布域上的同一条路径（见 docs/design/galaxy/publication.md 的
// "主站壳"）。**贴给别人的是这一条**：发布域是一处裸沙箱，把它当分享入口等于
// 把沙箱主机名交给用户。
//
// 主站对外地址没配置时为空（发布未启用，或那条半套配置——后者由配置校验拒绝
// 启动，因此走到这里只可能是前者）。
func (o PublicOrigin) ShareURL(projectID string, slot ContentSlot) string {
	if o.IsZero() || o.app == nil {
		return ""
	}
	return strings.TrimSuffix(o.app.String(), "/") + strings.TrimSuffix(slotRootPath(projectID, slot), "/")
}

// FrameAncestorSource 返回内容安全策略里允许嵌入发布物的来源（主站）。
//
// 只有来源（scheme + host），与 AllowedSource 同一条理由：策略匹配的是来源。
// 发布域与主站必须不同源（配置校验），因此本函数给出的**不是** `'self'`——发布
// 物不该能被任何别的站点嵌进页面，只该能被主站的壳嵌。
func (o PublicOrigin) FrameAncestorSource() string {
	if o.app == nil {
		return ""
	}
	return o.app.Scheme + "://" + o.app.Host
}

// PreviewBase 返回发布域的根地址（不带结尾斜杠）。预览通道的地址也落在它下面
// ——预览必须与发布同源，否则构建产物里那些指向发布域的绝对地址在预览里会取到
// 已发布的那一版（见 docs/design/galaxy/site-model.md 的"预览"）。
func (o PublicOrigin) PreviewBase() string { return o.pageBase() }

// pageBase 是发布域的根地址（不带结尾斜杠），ContentURL 与预览地址共用它。
//
// 两处共用而不是各拼一份：它们必须落在**同一个源**上，两处各写一份的表现是
// "预览与发布差了一个主机名"，而那个差异只有在构建产物带绝对地址时才暴露。
func (o PublicOrigin) pageBase() string {
	if o.IsZero() {
		return ""
	}
	return strings.TrimSuffix(o.page.String(), "/")
}

// SplitSitePath 把一条发布地址拆成（工程标识，内容槽，条目路径）（唯一入口）。
//
// 首段是**工程标识**，次段判槽：**恰好是 `docs` 或以 `docs/` 开头的落文档槽**
// （`/g/<标识>/docs` 与 `/g/<标识>/docs/` 的条目路径都为空，等价于该槽的入口
// 文件），其余一律落站点槽。
//
// **次段的判定就是保留段的判定**——同一件事只有这一处定义（见 content_slot.go
// 的 ValidateSlotPath），写入期拒掉的路径与这里认下的槽因此不会打架。判定
// **区分大小写**，与"集合成员测试不处理编码与大小写差异"同一条取向。
//
// 第二个返回值为假表示这条地址根本不是一次发布请求。**不做前缀之外的任何
// 猜测**：多余的分段属于条目路径（那是发布态自己的事），而不属于工程标识。
//
// 它是**形状解析，不是判定**：真伪由集合成员测试回答（见 publish.go 的
// PublishedEntry）——包括"这个工程其实没有文档槽"。
func SplitSitePath(requestPath string) (projectID string, slot ContentSlot, entryPath string, ok bool) {
	if !strings.HasPrefix(requestPath, PublicPathPrefix) {
		return "", "", "", false
	}
	rest := strings.TrimPrefix(requestPath, PublicPathPrefix)
	if rest == "" {
		return "", "", "", false
	}
	projectID, rest, ok = cutFirstSegment(rest)
	if !ok {
		return "", "", "", false
	}
	slot, entryPath = splitSlotPath(rest)
	return projectID, slot, entryPath, true
}

// cutFirstSegment 切下第一个 `/` 分隔的段，返回它与其余部分。
func cutFirstSegment(rest string) (first, remainder string, ok bool) {
	if rest == "" {
		return "", "", false
	}
	if slash := strings.IndexByte(rest, '/'); slash >= 0 {
		return rest[:slash], rest[slash+1:], true
	}
	return rest, "", true
}
