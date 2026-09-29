package server

import (
	"errors"
	"net/http"
	"strings"

	"go.uber.org/zap"

	"github.com/poetlife/aladdin/internal/galaxy"
)

// publicPageNotFound 是发布地址在"没有可返回的产物"时的响应体。
//
// 它是一句话，**不是**一个空页或默认页：路径不在集合里、未发布、已撤回、工程
// 不存在、标识没被猜中——五种情形返回同一个否定结果。区分它们等于告诉一个猜
// 地址的人"这个标识是真的"或"这一页是真的，只是还没发布"，那是一个比"猜不到"
// 更有用的信号。
const publicPageNotFound = "<!doctype html><title>404</title><p>页面不存在</p>"

// PublicProjectHandler 是发布地址这条**浏览器直连的非 RPC 入口**。
//
// 它对外地址形如 `<发布域>/g/<工程标识>/<路径>`，入口是 `<发布域>/g/<工程标识>`
// （与 `.../index.html` 同一页）。同一段前缀下还有一条**预览通道**
// （`<发布域>/g/p/<凭证>/<工程标识>/<路径>`，见 galaxy/preview.go）：两条路在
// 这里分岔，各自解析形状，其余的取字节方式逐条对应。
//
// 三点必须说清楚，因为它们与仓库里其它入口都不同：
//
//   - **它不走鉴权拦截器。** 发布态是公开匿名的："地址即凭据"——拿到地址的人
//     能看，猜不出地址的人看不到。因此它不做任何权限判定，也**不能**有"调用者"
//     这个概念（见 galaxy.PublishedEntry 的说明）。
//   - **它不同源。** 发布域与主应用必须不同源，且不同注册域，这一点由配置校验
//     在启动时强制——发布物里跑着用户写的脚本。
//   - **安全边界由响应头承担，不由内容扫描承担。** 内容里的外部资源引用由发布
//     流程尽力扫描（给用户可操作的错误），而"取不到本文件组之外的任何字节"由
//     内容安全策略让浏览器执行（见 galaxy csp.go）。
//
// 取字节分两条路（见 docs/design/galaxy/site-model.md）：
//
//   - **文本条目**由服务端在自己的路径上给出，带 `ETag = 内容摘要` 与
//     `no-cache`。**不能重定向到公开区**——内容寻址会抹掉路径，文档落在摘要
//     地址上之后它内部的 `/assets/index.js` 与相对链接就都指不到地方了。
//   - **资产条目**返回一个重定向：字节由浏览器直连公开区，服务端不代理。能重定向
//     的只有自身不含引用的东西，而媒体恰好是字节量的大头，CDN 的收益仍然拿到。
func PublicProjectHandler(service *galaxy.Service, logger *zap.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead:
		default:
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "方法不允许", http.StatusMethodNotAllowed)
			return
		}

		// 预览通道的形状先判：它的第一段（`p`）在发布态的形状里也是一个合法的
		// 工程标识位置，因此顺序反过来会让预览路径落进发布那条路（见
		// galaxy/preview.go 的 PreviewPathSegment）。
		if token, previewProjectID, previewSlot, previewPath, ok := galaxy.SplitPreviewPath(r.URL.Path); ok {
			servePreview(w, r, token, previewProjectID, previewSlot, previewPath, service, logger)
			return
		}

		projectID, slot, entryPath, ok := galaxy.SplitSitePath(r.URL.Path)
		if !ok {
			writePublicNotFound(w)
			return
		}
		entry, err := service.PublishedEntry(r.Context(), projectID, slot, entryPath)
		if err != nil {
			if !errors.Is(err, galaxy.ErrPublicationNotFound) {
				// 存储故障不是"这个页面不存在"。但对访问者而言，两者都只能
				// 看到"打不开"，因此响应相同、留痕不同。
				logger.Warn("读取发布产物失败",
					zap.String("project_id", projectID),
					zap.String("slot", string(slot)),
					zap.String("path", entryPath),
					zap.Error(err))
			}
			writePublicNotFound(w)
			return
		}

		// 内容安全策略与类型嗅探的关闭对所有取到东西的响应都生效：**发布物的
		// 安全边界**。脚本可以跑（内容里跑的是用户自己的东西），但只能从本
		// 发布域与公开区取媒体，且发不出任何请求（见 galaxy.ContentSecurityPolicy）。
		w.Header().Set(galaxy.CSPHeaderName, galaxy.ContentSecurityPolicy(service.Origin()))
		w.Header().Set("X-Content-Type-Options", "nosniff")
		// 发布物不该带着来源信息跳走。
		w.Header().Set("Referrer-Policy", "no-referrer")

		if entry.Kind == galaxy.EntryKindAsset {
			redirectURL, err := service.PublishedAssetURL(r.Context(), projectID, entry.AssetID)
			if err != nil {
				if !errors.Is(err, galaxy.ErrPublicationNotFound) {
					logger.Warn("读取公开区地址失败",
						zap.String("project_id", projectID),
						zap.String("asset_id", entry.AssetID),
						zap.Error(err))
				}
				writePublicNotFound(w)
				return
			}
			// **302 而不是 301**：撤回之后这个地址就不再给出，而一个被永久缓存
			// 的重定向会让访问者在很久以后仍被送到公开区的那个对象上。
			//
			// 目标不是请求输入：它由配置里的桶地址与资产的内容摘要拼出来
			// （见 galaxy.PublishedAssetURL），因此这里不存在开放重定向。
			//nolint:gosec // 目标来自配置派生，不是请求输入
			http.Redirect(w, r, redirectURL, http.StatusFound)
			return
		}

		// 文本条目：`ETag` 是内容摘要，`no-cache` 让它"存得住、但每次都向服务端
		// 校验"。于是撤回（改指针）在下一次请求就生效，不必失效任何缓存；而校验
		// 命中时服务端只读库里的清单，**不碰对象存储**。
		w.Header().Set("ETag", `"`+entry.Digest+`"`)
		w.Header().Set("Cache-Control", "no-cache")
		if etagMatches(r.Header.Get("If-None-Match"), entry.Digest) {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		contentType, ok := galaxy.ArtifactContentType(slot, entry.Path)
		if !ok {
			// 产物清单里的每一条都来自文件组，而文件组的路径都过了白名单；走到
			// 这里说明库里的数据不是这个版本写进去的。给一个中性类型而不是
			// 猜一个——猜错的表现是浏览器按 HTML 渲染一段别的字节。
			contentType = "application/octet-stream"
		}
		w.Header().Set("Content-Type", contentType)
		data, err := service.ReadPublishedText(r.Context(), projectID, entry.Digest)
		if err != nil {
			if !errors.Is(err, galaxy.ErrPublicationNotFound) {
				logger.Warn("读取发布文本失败",
					zap.String("project_id", projectID),
					zap.String("path", entry.Path),
					zap.Error(err))
			}
			writePublicNotFound(w)
			return
		}
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusOK)
			return
		}
		// 这里写的就是**用户自己的内容**：发布物的意义正是把它原样交给浏览器。
		// 隔离不靠转义，靠不同源加内容安全策略（见 galaxy csp.go）。
		//nolint:gosec // 交付用户内容正是这个入口的职责
		_, _ = w.Write(data)
	})
}

// etagMatches 判定 If-None-Match 是否命中一个内容摘要。
//
// 它按 RFC 9110 的形状处理：`*` 命中一切、取值可以带逗号、可以带 `W/` 前缀。
// 不做完整实现的是**弱比较的语义细节**——这里的 ETag 是从字节算出来的强校验
// 值，弱标记只影响比较强度，不影响"要不要回 304"这个结论。
func etagMatches(header, digest string) bool {
	if header == "" {
		return false
	}
	for _, candidate := range strings.Split(header, ",") {
		value := strings.TrimSpace(candidate)
		if value == "*" {
			return true
		}
		value = strings.TrimPrefix(value, "W/")
		if strings.Trim(value, `"`) == digest {
			return true
		}
	}
	return false
}

func writePublicNotFound(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// 否定结论不该被缓存：撤回之后它要靠"下一次请求即不可达"，而一份被缓存的
	// 404 会把"曾经不存在"与"现在不存在"混起来。
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNotFound)
	_, _ = w.Write([]byte(publicPageNotFound))
}

// servePreview 交付一次预览请求：草稿里的文本由服务端给出，资产给一个短时私有地址。
//
// 与发布态那条路逐条对应，差别只有三处：取草稿而不是取当前发布的那一版、凭证而
// 不是匿名、资产指向私有区而不是公开区。安全头逐条相同——**内容仍然跑在发布域
// 上**，因此隔离靠的是这一条（与主应用不同源）加上 iframe 的沙箱属性（见
// docs/design/galaxy/authoring.md）。
func servePreview(w http.ResponseWriter, r *http.Request, token, projectID string, slot galaxy.ContentSlot, entryPath string, service *galaxy.Service, logger *zap.Logger) {
	target, err := service.OpenPreview(r.Context(), token, projectID, slot, entryPath)
	if err != nil {
		if !errors.Is(err, galaxy.ErrPreviewNotFound) {
			// 存储故障不是"这一页不存在"。但对浏览器而言两者都只能看到"打不开"，
			// 因此响应相同、留痕不同。
			logger.Warn("读取预览内容失败",
				zap.String("project_id", projectID),
				zap.String("slot", string(slot)),
				zap.String("path", entryPath),
				zap.Error(err))
		}
		writePublicNotFound(w)
		return
	}

	w.Header().Set(galaxy.CSPHeaderName, galaxy.ContentSecurityPolicy(service.Origin()))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// 预览物也不该带着来源信息跳走（地址里就有凭证）。
	w.Header().Set("Referrer-Policy", "no-referrer")

	if target.RedirectURL != "" {
		// 与发布态那条一样用 302：凭证过期之后这个地址就不再给出，而一个被永久
		// 缓存的重定向会把浏览器继续送到公开区那个对象上。
		//
		// 目标不是请求输入：它由桶地址、资产标识与短时签名派生。
		//nolint:gosec // 目标来自服务端派生，不是请求输入
		http.Redirect(w, r, target.RedirectURL, http.StatusFound)
		return
	}

	// **`no-store`，不是 `no-cache`。** 发布态那次校验式缓存靠的是 `ETag` 等于
	// 内容摘要，而草稿是会变的：同一个路径的字节下一刻就可能不同，摘要因此不是
	// 一个可以拿来做校验的稳定值。草稿不进任何缓存。
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", target.ContentType)
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	// 交付的正是用户自己的内容：预览的意义就是把草稿原样交给浏览器。
	//nolint:gosec // 交付用户内容正是这条入口的职责
	_, _ = w.Write(target.Data)
}
