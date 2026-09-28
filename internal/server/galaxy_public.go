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
// 它是一句话，**不是**一个空页或默认页：未发布、已撤回、工程不存在、标识没被
// 猜中——四种情形返回同一个否定结果。区分它们等于告诉一个猜地址的人"这个标识
// 是真的，只是还没发布"，那是一个比"猜不到"更有用的信号。
const publicPageNotFound = "<!doctype html><title>404</title><p>页面不存在</p>"

// PublicProjectHandler 是发布地址这条**浏览器直连的非 RPC 入口**。
//
// 它对外地址形如 `<发布域>/g/<工程标识>`，返回当前发布产物（一个 HTML 文档）。
//
// 三点必须说清楚，因为它们与仓库里其它入口都不同：
//
//   - **它不走鉴权拦截器。** 发布态是公开匿名的："地址即凭据"——拿到地址的人
//     能看，猜不出地址的人看不到。因此它不做任何权限判定，也**不能**有"调用者"
//     这个概念（见 galaxy.CurrentArtifact 的说明）。
//   - **它不同源。** 发布域与主应用必须不同源，且不同注册域，这一点由配置校验
//     在启动时强制——发布物里跑着用户写的脚本。
//   - **安全边界由响应头承担，不由内容扫描承担。** 正文里的资产引用由发布流程
//     尽力扫描（给用户可操作的错误），而"取不到本工程资产库之外的任何字节"由
//     内容安全策略让浏览器执行（见 galaxy csp.go）。
//
// 它只承担 HTML 本身（几 KB 量级）：媒体字节由访问者从公开区直连，服务端不代理。
func PublicProjectHandler(service *galaxy.Service, logger *zap.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead:
		default:
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "方法不允许", http.StatusMethodNotAllowed)
			return
		}

		projectID := strings.TrimPrefix(r.URL.Path, galaxy.PublicPathPrefix)
		// 多一段路径就不是一个工程标识。不猜、不做前缀匹配：前缀匹配会把
		// "/g/<标识>/任意后缀"也当成同一个页面，而那是一个可被利用的缓存键。
		if projectID == "" || strings.Contains(projectID, "/") {
			writePublicNotFound(w)
			return
		}

		publication, err := service.CurrentArtifact(r.Context(), projectID)
		if err != nil {
			if !errors.Is(err, galaxy.ErrPublicationNotFound) {
				// 存储故障不是"这个页面不存在"。但对访问者而言，两者都只能
				// 看到"打不开"，因此响应相同、留痕不同。
				logger.Warn("读取发布产物失败",
					zap.String("project_id", projectID), zap.Error(err))
			}
			writePublicNotFound(w)
			return
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		// 内容安全策略：**发布物的安全边界**。脚本可以跑（正文是用户写的），
		// 但只能从公开区取媒体，且发不出任何请求（见 galaxy.ContentSecurityPolicy）。
		w.Header().Set(galaxy.CSPHeaderName, galaxy.ContentSecurityPolicy(service.Origin()))
		// 类型嗅探关掉：公开区对象以声明的类型下发，而这里返回的正文是
		// text/html——两者都不该让浏览器再猜一次。
		w.Header().Set("X-Content-Type-Options", "nosniff")
		// 发布物不该被搜索引擎当作可枚举的入口，也不该带着来源信息跳走。
		w.Header().Set("Referrer-Policy", "no-referrer")
		_, _ = w.Write([]byte(publication.Content))
	})
}

func writePublicNotFound(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusNotFound)
	_, _ = w.Write([]byte(publicPageNotFound))
}
