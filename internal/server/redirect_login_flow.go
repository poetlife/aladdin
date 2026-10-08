package server

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"net/http"
	"net/url"
	"path"
	"time"

	"go.uber.org/zap"

	"github.com/poetlife/aladdin/internal/config"
	"github.com/poetlife/aladdin/internal/identity"
)

// 重定向型登录渠道的两个浏览器直连端点。
//
// 它们**不是 RPC**：渠道把**浏览器**重定向回来，而浏览器导航带不了请求头，
// 因此它们不经过方法级的鉴权注解体系（见 middleware.go 的 browserEntryPaths）。
// 它们也不是第二套登录实现——凭证的**到达方式**不同而已，校验、解析主体与
// 签发会话全部复用 IdentityService 的那一份（见 resolveAndIssue）。
//
// 同一个起点与回调同时服务**登录**与**绑定**：差别只在起点导航上的用途标记，
// 以及回调把结果做成"会话"还是"待绑定凭据"。绑定的归属只在导航结束后那次
// 已认证的兑换里决定（见 docs/design/identity/identity-linking.md）。
//
// **渠道之间只有取值不同，流程逐字相同**，因此这里只有一份实现：各写一份迟早
// 会出现"一个渠道校验了导航凭据、另一个没校验"。差异集中在 redirectChannel 里。
const (
	// FrontendCallbackPath 是回跳前端的地址，前端在那里取走会话凭证或兑换待绑定凭据。
	//
	// 它**刻意不在 `/auth/` 下**：那一段整段属于服务端的浏览器直连端点，
	// 反向代理会把 `/auth/` 整段转发给服务端，因此前端在这里放一个页面路由
	// 会被服务端接走并返回 404。
	FrontendCallbackPath = "/login/callback"

	// stateTTL 是一次导航从发起到回调的允许时长。
	stateTTL = 10 * time.Minute
	// stateBytes 是导航凭据的随机字节数：16 字节即 128 位，
	// 猜中一份有效取值的概率可以忽略。
	stateBytes = 16
	// stateMaxPending 是服务端同时记住的未使用凭据条数上界。
	//
	// 发起导航的地址谁都能调，没有上界就等于给了一张可以随便写大的表。
	// 4096 条远超"十分钟内有四千个流程正在进行"的真实规模，因此正常使用
	// 碰不到它；碰到它时表现为某一次进行中的流程要重来。
	stateMaxPending = 4096

	// frontendErrorFragment / frontendTokenFragment / frontendBindingFragment
	// 是回跳地址里三个 fragment 的键。
	frontendErrorFragment   = "error"
	frontendTokenFragment   = "token"
	frontendBindingFragment = "binding"
)

// browserFlowPurpose 是一次浏览器直连导航的用途。
type browserFlowPurpose string

const (
	// flowPurposeLogin 走完整的登录：解析主体、登记新主体、签发会话。
	flowPurposeLogin browserFlowPurpose = "login"
	// flowPurposeBind 只产出待绑定凭据：不解析主体、不登记、不签发会话。
	flowPurposeBind browserFlowPurpose = "bind"
)

// browserFlowState 是一次浏览器直连导航在服务端记下的用途与来源。
//
// 它只决定回调**把结果交给谁**（会话还是待绑定凭据），不是信任边界：
// 绑定要绑到哪个主体，只在导航结束后那次已认证的兑换里从会话取。
type browserFlowState struct {
	purpose browserFlowPurpose
	source  string
}

// redirectChannel 是一个重定向型渠道在这两个端点上的**全部差异**。
//
// 其余一切——一次性导航凭据的生成与单次消费、cookie 的属性、回跳地址的构造、
// 失败标记的语义、登录与绑定两条去向——两个渠道走的是同一段实现。
type redirectChannel struct {
	// source 是渠道来源标识（取 identity.SourceGoogle / SourceGithub）。
	source string
	// noun 是日志与错误信息里指代这个渠道的名字。
	noun string
	// startPath / callbackPath 是两个浏览器直连端点。回调地址还是**对外契约**
	// 的一部分：它要与渠道控制台里登记的取值逐字一致。
	startPath    string
	callbackPath string
	// authorizeURL 由客户端标识、回调地址与本次导航的凭据构造授权地址。
	authorizeURL func(clientID, redirectURI, state string) string
}

// 以下取值与渠道一一对应，因此**由 source 与路径派生**，不各写一份：
// 两处各写一份的话，"改了端点却忘了改 cookie 作用路径"会表现为一次
// 莫名其妙的登录失败。

// stateCookie 是承载"这次导航由本浏览器发起"的证据的 cookie 名。
func (c redirectChannel) stateCookie() string { return "aladdin_" + c.source + "_state" }

// stateCookiePath 只覆盖这个渠道的两个端点：这份证据没有理由出现在任何别的
// 请求里。用过程名而不是 "/" 是为了把暴露面收到最小。
func (c redirectChannel) stateCookiePath() string { return path.Dir(c.startPath) }

// loginFailed 是登录失败回跳前端时附在地址上的固定标记。
func (c redirectChannel) loginFailed() string { return c.source + "_login_failed" }

// bindFailed 是绑定失败回跳前端时的固定标记。与登录失败分开，前端才知道
// 该说"登录未完成"还是"绑定未完成"。
func (c redirectChannel) bindFailed() string { return c.source + "_bind_failed" }

// newGithubRedirectChannel 是 GitHub 渠道的差异取值。
func newGithubRedirectChannel() redirectChannel {
	return redirectChannel{
		source:       identity.SourceGithub,
		noun:         "GitHub",
		startPath:    identity.GithubStartPath,
		callbackPath: identity.GithubCallbackPath,
		authorizeURL: identity.GithubAuthorizeURL,
	}
}

// newGoogleRedirectChannel 是 Google 渠道的差异取值。
func newGoogleRedirectChannel() redirectChannel {
	return redirectChannel{
		source:       identity.SourceGoogle,
		noun:         "Google",
		startPath:    identity.GoogleStartPath,
		callbackPath: identity.GoogleCallbackPath,
		authorizeURL: identity.GoogleAuthorizeURL,
	}
}

// redirectChannels 是本仓库接入的全部重定向型渠道。
//
// 服务端注册端点、中间件放行浏览器直连路径，都从这一份取值派生——两处各列一份
// 的话，"加了端点却忘了放行"会表现为一次鉴权失败，而不是"找不到页面"。
func redirectChannels() []redirectChannel {
	return []redirectChannel{newGoogleRedirectChannel(), newGithubRedirectChannel()}
}

// RedirectLoginFlow 实现一个重定向型渠道的两个浏览器直连端点。
//
// 它持有 IdentityService 而不是自己拿一套存储与校验器：重定向型与搬运型
// 共用同一段登录实现，是"改判定不会出现一条路径生效、另一条没生效"的前提。
type RedirectLoginFlow struct {
	service *IdentityService
	cfg     config.ServerConfig
	logger  *zap.Logger
	channel redirectChannel
	// states 是"这份导航凭据用过没有"的唯一答案所在（见 one_time_store.go）。
	states *oneTimeStore[browserFlowState]
}

// NewRedirectLoginFlow 构造一个渠道的重定向流程。
func NewRedirectLoginFlow(service *IdentityService, cfg config.ServerConfig, logger *zap.Logger, channel redirectChannel) *RedirectLoginFlow {
	return &RedirectLoginFlow{
		service: service,
		cfg:     cfg,
		logger:  logger,
		channel: channel,
		states:  newOneTimeStore[browserFlowState](stateTTL, stateMaxPending, time.Now),
	}
}

// Start 实现起点端点：生成一次性导航凭据、把浏览器交给渠道。
//
// 凭据由**服务端**生成并记住，不能由客户端生成：浏览器是直接导航到回调端点的，
// 页面脚本根本没有机会参与；而且按仓库的既有约定，客户端的本地判断不是安全边界。
//
// 起点导航上的用途标记不是信任边界：它只记下"这次是登录还是绑定"。真正的
// 归属判定发生在绑定回调之后那次已认证的兑换里。
func (f *RedirectLoginFlow) Start(w http.ResponseWriter, r *http.Request) {
	channel, ok := f.service.channels.Get(f.channel.source)
	if !ok {
		// 未启用时这条路整体不存在。报"没找到"而不是"未实现"：它是浏览器
		// 直连的地址，没有 RPC 那套错误码可用。
		http.NotFound(w, r)
		return
	}

	purpose := flowPurposeLogin
	if r.URL.Query().Get("purpose") == string(flowPurposeBind) {
		purpose = flowPurposeBind
	}
	// 任何一次新的起点都先作废上一次没走完的待绑定凭据：一个浏览器同时只
	// 保留一条进行中的绑定，避免一份旧凭据在用户以为已取消之后仍可兑换。
	f.clearPendingBindingCookie(w)

	state, err := newOpaqueToken(stateBytes)
	if err != nil {
		// 取不到随机数意味着系统熵源出了问题，这不是调用方能修的。
		f.logger.Error("生成导航凭据失败", zap.String("channel", f.channel.source), zap.Error(err))
		http.Error(w, "服务暂时不可用", http.StatusInternalServerError)
		return
	}
	// 先记住再写下 cookie：回调只认这里记过的凭据。"单次使用"与"cookie 相符"
	// 是两条独立的判断，缺了这条，任何字符串都能伪装成一次导航的凭据。
	f.states.issue(state, browserFlowState{purpose: purpose, source: f.channel.source})
	f.setStateCookie(w, state)

	http.Redirect(w, r, f.channel.authorizeURL(channel.ClientID, f.redirectURI(), state), http.StatusFound)
}

// Callback 实现回调端点：确认这次导航由本浏览器发起，再完成登录或产出待绑定凭据。
func (f *RedirectLoginFlow) Callback(w http.ResponseWriter, r *http.Request) {
	// cookie 先作废：它只在这一次回调上有用。
	f.clearStateCookie(w)

	if _, ok := f.service.channels.Get(f.channel.source); !ok {
		http.NotFound(w, r)
		return
	}

	query := r.URL.Query()
	code, state := query.Get("code"), query.Get("state")
	cookieState, hasCookie := cookieValue(r.Header, f.channel.stateCookie())
	// 失败标记要按用途选，否则一次绑定的失败会被说成"登录未完成"。用途只在
	// 服务端的记录里，因此在消费之前先看一眼；看不到就按登录报——这是唯一
	// 诚实的默认（缺凭据时本来就无从知道）。这一眼**不参与任何判定**。
	purpose := f.purposeOf(cookieState, state)
	if !hasCookie || code == "" || state == "" ||
		subtle.ConstantTimeCompare([]byte(state), []byte(cookieState)) != 1 {
		// 凭据不符意味着这次回调不是本浏览器发起的那一次。若不在这里拦住，
		// 攻击者可以把自己账号的授权码塞进受害者的浏览器，让服务端为**攻击者
		// 的主体**签发会话——受害者此后的一切操作都落在攻击者能登录的账号上。
		f.fail(w, r, purpose, "导航凭据缺失或不符")
		return
	}
	// 凭据单次使用：无论接下来成败，先把它从服务端的记录里取走。取走之后同一份
	// 凭据再来一次就查不到，因此不会第二次签发会话或产出第二份待绑定凭据。
	flowState, ok := f.states.consume(state)
	if !ok {
		// cookie 相符却没有记录：它要么已经用过（重放），要么过期了，要么根本不是
		// 本服务发出的。三种情况都不该走到下一步。
		f.fail(w, r, purpose, "导航凭据已失效或已被使用")
		return
	}

	verified, err := f.service.verify(r.Context(), flowState.source, code)
	if err != nil {
		f.fail(w, r, flowState.purpose, "校验渠道凭证失败: "+err.Error())
		return
	}

	if flowState.purpose == flowPurposeBind {
		token, err := f.service.stagePendingBinding(flowState.source, verified)
		if err != nil {
			// 取不到随机数意味着系统熵源出了问题，这不是调用方能修的。
			f.fail(w, r, flowState.purpose, "生成待绑定凭据失败: "+err.Error())
			return
		}
		setPendingBindingCookie(w, token, f.secureCookie())
		http.Redirect(w, r, f.frontendBindingURL(flowState.source), http.StatusFound)
		return
	}

	issued, err := f.service.resolveAndIssue(r.Context(), flowState.source, verified)
	if err != nil {
		f.fail(w, r, flowState.purpose, "解析主体或签发会话失败: "+err.Error())
		return
	}

	http.Redirect(w, r, f.frontendURL(issued.Token), http.StatusFound)
}

// redirectURI 是交给渠道的回调地址。
func (f *RedirectLoginFlow) redirectURI() string {
	return f.cfg.PublicURL(f.channel.callbackPath)
}

// frontendURL 构造回跳前端的地址，会话凭证放在 fragment 里。
//
// fragment **不会发往服务端**：不进服务端访问日志，也不进跳转来源（Referer）。
// 放查询串则三处都会留下这份凭证。
//
// 目标**只能由配置构造，绝不能用请求头**（Host / X-Forwarded-Host）：用请求头
// 构造跳转目标等于给攻击者一个把会话凭证送到任意主机、并把浏览器导向任意站点的
// 原语。这是本文件里最要紧的一条。
func (f *RedirectLoginFlow) frontendURL(token string) string {
	return f.cfg.PublicURL(FrontendCallbackPath) + "#" +
		frontendTokenFragment + "=" + url.QueryEscape(token)
}

// frontendBindingURL 构造绑定回跳前端的地址。
//
// 地址里**不带待绑定凭据**：它只带"这次是一条绑定、来源是谁"这个固定标记，
// 凭据本身由 HttpOnly cookie 承载。前端据此调用已认证的兑换 RPC。
func (f *RedirectLoginFlow) frontendBindingURL(source string) string {
	return f.cfg.PublicURL(FrontendCallbackPath) + "#" +
		frontendBindingFragment + "=" + url.QueryEscape(source)
}

// fail 把一次失败送回前端。
//
// purpose 只决定措辞：前端据此说"登录未完成"还是"绑定未完成"。地址里只带一个
// 固定标记，不透露失败在哪一步、更不含任何凭据——地址会被浏览器历史、Referer
// 与服务端访问日志留存下来，reason 只进服务端日志。
func (f *RedirectLoginFlow) fail(w http.ResponseWriter, r *http.Request, purpose browserFlowPurpose, reason string) {
	event, marker := f.channel.noun+" 登录未完成", f.channel.loginFailed()
	if purpose == flowPurposeBind {
		event, marker = f.channel.noun+" 绑定未完成", f.channel.bindFailed()
	}
	f.logger.Warn(event, zap.String("channel", f.channel.source), zap.String("reason", reason))
	f.clearPendingBindingCookie(w)
	http.Redirect(w, r, f.cfg.PublicURL(FrontendCallbackPath)+"#"+
		frontendErrorFragment+"="+marker, http.StatusFound)
}

// purposeOf 尽力判断一次导航的用途，只用于选失败标记与日志用语。
//
// 它**不参与任何判定**：能不能兑换只由消费到的记录决定。因此"看不到用途时
// 按登录报"是安全的——最坏情况是把一次绑定的失败说成登录未完成，而缺凭据时
// 本来就无从知道用途。
func (f *RedirectLoginFlow) purposeOf(tokens ...string) browserFlowPurpose {
	for _, token := range tokens {
		if token == "" {
			continue
		}
		if state, ok := f.states.peek(token); ok {
			return state.purpose
		}
	}
	return flowPurposeLogin
}

// setStateCookie 写下承载导航凭据的 cookie。
func (f *RedirectLoginFlow) setStateCookie(w http.ResponseWriter, state string) {
	// 作用路径限于这个渠道的两个端点：它不该出现在任何别的请求里。
	// SameSite=Lax 是**必需**而非可选：回调是一次来自渠道方的跨站顶层导航，
	// Strict 会让 cookie 恰好在这次导航上不发送，于是每一次登录都会失败。
	http.SetCookie(w, opaqueCookie(
		f.channel.stateCookie(), state, f.channel.stateCookiePath(),
		http.SameSiteLaxMode, int(stateTTL.Seconds()), f.secureCookie()))
}

// clearStateCookie 作废导航凭据。
func (f *RedirectLoginFlow) clearStateCookie(w http.ResponseWriter) {
	http.SetCookie(w, opaqueCookie(
		f.channel.stateCookie(), "", f.channel.stateCookiePath(),
		http.SameSiteLaxMode, -1, f.secureCookie()))
}

// clearPendingBindingCookie 作废浏览器侧的待绑定凭据。
func (f *RedirectLoginFlow) clearPendingBindingCookie(w http.ResponseWriter) {
	clearPendingBindingCookie(w, f.secureCookie())
}

// secureCookie 报告 cookie 是否要求加密传输。
//
// 它由**对外源的协议**决定，而不是写死：写死 true 会让本地回环上的 http 开发
// 无法登录，写死 false 会让线上少一道防护。
func (f *RedirectLoginFlow) secureCookie() bool {
	// 从解析后的协议判，而不是按原始字符串的前缀：协议名大小写不敏感，用前缀比较
	// 会把 `HTTPS://…` 判成不要求加密传输，与取值校验的结论相反。
	return f.cfg.PublicScheme() == "https"
}

// newOpaqueToken 生成一份密码学随机、URL 安全、无填充的一次性凭据。
//
// 编码用 URL 安全的无填充 base64：它要出现在地址栏与 cookie 里，标准 base64
// 的 '+' '/' '=' 会在各种中转环节被转义或截断。
func newOpaqueToken(nBytes int) (string, error) {
	buf := make([]byte, nBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
