package server

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"net/http"
	"net/url"
	"time"

	"go.uber.org/zap"

	"github.com/poetlife/aladdin/internal/config"
	"github.com/poetlife/aladdin/internal/identity"
)

// GitHub 的两个浏览器直连端点。
//
// 它们**不是 RPC**：GitHub 把**浏览器**重定向回来，而浏览器导航带不了请求头，
// 因此它们不经过方法级的鉴权注解体系（见 middleware.go 的 browserEntryPaths）。
// 它们也不是第二套登录实现——凭证的**到达方式**不同而已，校验、解析主体与
// 签发会话全部复用 IdentityService 的那一份（见 resolveAndIssue）。
const (
	// FrontendCallbackPath 是回跳前端的地址，前端在那里取走会话凭证。
	//
	// 它**刻意不在 `/auth/` 下**：那一段整段属于服务端的浏览器直连端点，
	// 反向代理会把 `/auth/` 整段转发给服务端，因此前端在这里放一个页面路由
	// 会被服务端接走并返回 404。
	FrontendCallbackPath = "/login/callback"

	// githubStateCookie 承载"这次登录由本浏览器发起"的证据。
	githubStateCookie = "aladdin_github_state"
	// githubStateTTL 是一次登录从发起到回调的允许时长。
	githubStateTTL = 10 * time.Minute
	// githubStateBytes 是这份证据的随机字节数：16 字节即 128 位，
	// 猜中一份有效取值的概率可以忽略。
	githubStateBytes = 16
	// githubStateMaxPending 是服务端同时记住的未使用凭据条数上界。
	//
	// 发起登录的地址谁都能调，没有上界就等于给了一张可以随便写大的表。
	// 4096 条远超"十分钟内有四千个登录正在进行"的真实规模，因此正常使用
	// 碰不到它；碰到它时表现为某一次进行中的登录要重来。
	githubStateMaxPending = 4096

	// githubLoginFailed 是回跳前端时附在地址上的失败标记。
	//
	// 它**只有这一个取值**，不区分失败发生在哪一步：地址里的东西会进浏览器
	// 历史、Referer 与服务端访问日志，而"哪一步失败"对攻击者有用、对用户没用。
	githubLoginFailed = "github_login_failed"
	// frontendErrorFragment 与 frontendTokenFragment 是回跳地址里两个 fragment 的键。
	frontendErrorFragment = "error"
	frontendTokenFragment = "token"
)

// GithubLoginFlow 实现 GitHub 的两个浏览器直连端点。
//
// 它持有 IdentityService 而不是自己拿一套存储与校验器：重定向型与搬运型
// 共用同一段登录实现，是"改判定不会出现一条路径生效、另一条没生效"的前提。
type GithubLoginFlow struct {
	service *IdentityService
	cfg     config.ServerConfig
	logger  *zap.Logger
	// states 是"凭据用过没有"的唯一答案所在（见 login_states.go）。
	states *loginStates
}

// NewGithubLoginFlow 构造 GitHub 的重定向登录流程。
func NewGithubLoginFlow(service *IdentityService, cfg config.ServerConfig, logger *zap.Logger) *GithubLoginFlow {
	return &GithubLoginFlow{
		service: service,
		cfg:     cfg,
		logger:  logger,
		states:  newLoginStates(githubStateTTL, githubStateMaxPending, time.Now),
	}
}

// Start 实现起点端点：生成一次性凭据、把浏览器交给 GitHub。
//
// 凭据由**服务端**生成并记住，不能由客户端生成：浏览器是直接导航到回调端点的，
// 页面脚本根本没有机会参与；而且按仓库的既有约定，客户端的本地判断不是安全边界。
func (f *GithubLoginFlow) Start(w http.ResponseWriter, r *http.Request) {
	channel, ok := f.service.channels.Get(identity.SourceGithub)
	if !ok {
		// 未启用时这条路整体不存在。报"没找到"而不是"未实现"：它是浏览器
		// 直连的地址，没有 RPC 那套错误码可用。
		http.NotFound(w, r)
		return
	}

	state, err := newGithubState()
	if err != nil {
		// 取不到随机数意味着系统熵源出了问题，这不是调用方能修的。
		f.logger.Error("生成 GitHub 登录凭据失败", zap.Error(err))
		http.Error(w, "服务暂时不可用", http.StatusInternalServerError)
		return
	}
	// 先记住再写下 cookie：回调只认这里记过的凭据。"单次使用"与"cookie 相符"
	// 是两条独立的判断，缺了这条，任何字符串都能伪装成一次登录的凭据。
	f.states.issue(state)
	f.setStateCookie(w, state)

	http.Redirect(w, r, identity.GithubAuthorizeURL(channel.ClientID, f.redirectURI(), state), http.StatusFound)
}

// Callback 实现回调端点：确认这次登录由本浏览器发起，再完成登录。
func (f *GithubLoginFlow) Callback(w http.ResponseWriter, r *http.Request) {
	// cookie 先作废：它只在这一次回调上有用。
	f.clearStateCookie(w)

	if _, ok := f.service.channels.Get(identity.SourceGithub); !ok {
		http.NotFound(w, r)
		return
	}

	query := r.URL.Query()
	code, state := query.Get("code"), query.Get("state")
	cookie, err := r.Cookie(githubStateCookie)
	if err != nil || code == "" || state == "" ||
		subtle.ConstantTimeCompare([]byte(state), []byte(cookie.Value)) != 1 {
		// 凭据不符意味着这次回调不是本浏览器发起的那一次。若不在这里拦住，
		// 攻击者可以把自己账号的授权码塞进受害者的浏览器，让服务端为**攻击者
		// 的主体**签发会话——受害者此后的一切操作都落在攻击者能登录的账号上。
		f.fail(w, r, "登录凭据缺失或不符")
		return
	}
	// 凭据单次使用：无论接下来成败，先把它从服务端的记录里取走。取走之后同一份
	// 凭据再来一次就查不到，因此不会第二次签发会话——即便 cookie 还在。
	if !f.states.consume(state) {
		// cookie 相符却没有记录：它要么已经用过（重放），要么过期了，要么根本不是
		// 本服务发出的。三种情况都不该走到签发。
		f.fail(w, r, "登录凭据已失效或已被使用")
		return
	}

	verified, err := f.service.verify(r.Context(), identity.SourceGithub, code)
	if err != nil {
		f.fail(w, r, "校验授权码失败: "+err.Error())
		return
	}
	issued, err := f.service.resolveAndIssue(r.Context(), identity.SourceGithub, verified)
	if err != nil {
		f.fail(w, r, "解析主体或签发会话失败: "+err.Error())
		return
	}

	http.Redirect(w, r, f.frontendURL(issued.Token), http.StatusFound)
}

// redirectURI 是交给 GitHub 的回调地址。
func (f *GithubLoginFlow) redirectURI() string {
	return f.cfg.PublicURL(identity.GithubCallbackPath)
}

// frontendURL 构造回跳前端的地址，会话凭证放在 fragment 里。
//
// fragment **不会发往服务端**：不进服务端访问日志，也不进跳转来源（Referer）。
// 放查询串则三处都会留下这份凭证。
//
// 目标**只能由配置构造，绝不能用请求头**（Host / X-Forwarded-Host）：用请求头
// 构造跳转目标等于给攻击者一个把会话凭证送到任意主机、并把浏览器导向任意站点的
// 原语。这是本文件里最要紧的一条。
func (f *GithubLoginFlow) frontendURL(token string) string {
	return f.cfg.PublicURL(FrontendCallbackPath) + "#" +
		frontendTokenFragment + "=" + url.QueryEscape(token)
}

// fail 把一次失败的登录送回前端。
//
// reason 只进日志。地址里只带一个固定的失败标记，不含"哪一步失败"、更不含
// 任何凭据——地址会被浏览器历史、Referer 与服务端访问日志留存下来。
func (f *GithubLoginFlow) fail(w http.ResponseWriter, r *http.Request, reason string) {
	f.logger.Warn("GitHub 登录未完成", zap.String("reason", reason))
	http.Redirect(w, r, f.cfg.PublicURL(FrontendCallbackPath)+"#"+
		frontendErrorFragment+"="+githubLoginFailed, http.StatusFound)
}

// setStateCookie 写下承载登录凭据的 cookie。
func (f *GithubLoginFlow) setStateCookie(w http.ResponseWriter, state string) {
	//nolint:gosec // HttpOnly 与 SameSite 已显式给足；Secure 由对外源的协议派生（见 secureCookie），非字面量故 gosec 看不出来
	http.SetCookie(w, &http.Cookie{
		Name:  githubStateCookie,
		Value: state,
		// 作用路径限于这两个端点：它不该出现在任何别的请求里。
		Path: "/auth/github",
		// 不可被脚本读取：否则一次脚本注入就能读走它，防护归零。
		HttpOnly: true,
		Secure:   f.secureCookie(),
		// SameSite=Lax 是**必需**而非可选：回调是一次来自 github.com 的跨站
		// 顶层导航，Strict 会让 cookie 恰好在这次导航上不发送，于是每一次
		// 登录都会失败。
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(githubStateTTL.Seconds()),
	})
}

// clearStateCookie 作废登录凭据。
func (f *GithubLoginFlow) clearStateCookie(w http.ResponseWriter) {
	//nolint:gosec // 同上：属性齐备，Secure 由配置派生
	http.SetCookie(w, &http.Cookie{
		Name:     githubStateCookie,
		Value:    "",
		Path:     "/auth/github",
		HttpOnly: true,
		Secure:   f.secureCookie(),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

// secureCookie 报告 cookie 是否要求加密传输。
//
// 它由**对外源的协议**决定，而不是写死：写死 true 会让本地回环上的 http 开发
// 无法登录，写死 false 会让线上少一道防护。
func (f *GithubLoginFlow) secureCookie() bool {
	// 从解析后的协议判，而不是按原始字符串的前缀：协议名大小写不敏感，用前缀比较
	// 会把 `HTTPS://…` 判成不要求加密传输，与取值校验的结论相反。
	return f.cfg.PublicScheme() == "https"
}

// newGithubState 生成一份密码学随机的一次性凭据。
//
// 编码用 URL 安全的无填充 base64：它要出现在地址栏与 cookie 里，标准 base64
// 的 '+' '/' '=' 会在各种中转环节被转义或截断。
func newGithubState() (string, error) {
	buf := make([]byte, githubStateBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
