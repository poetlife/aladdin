package server

import (
	"net/http"
	"time"

	identityv1connect "github.com/poetlife/aladdin/api/gen/aladdin/identity/v1/identityv1connect"
	"github.com/poetlife/aladdin/internal/identity"
)

const (
	// registrationCookie 承载"服务端已经校验过、等待邀请码的渠道身份"。
	//
	// 它**不是会话**：不携带主体、不携带权限，也不能用于登录；只有兑换这一个
	// RPC 会读它（见 docs/design/identity/registration.md）。
	registrationCookie = "aladdin_registration"
	// registrationTTL 是从回调完成到人填完邀请码之间的允许时长。
	//
	// 它比"填一个码"该花的时间宽裕得多，但仍然是分钟量级：这份凭据换来的是一次
	// 注册，没有理由让它躺在浏览器里过夜。
	registrationTTL = 10 * time.Minute
	// registrationMaxPending 是服务端同时记住的待注册条数上界。
	//
	// 发起登录的地址谁都能调，没有上界就等于给了一张可以随便写大的表。取值与
	// 待绑定凭据、导航状态一致（见 one_time_store.go）。
	registrationMaxPending = 4096
	// registrationBytes 是这份凭据的随机字节数：16 字节即 128 位。
	registrationBytes = 16

	// RegistrationPath 是填邀请码那一页在前端的路径。
	//
	// 它**是对外契约的一部分**：服务端把浏览器送到这个地址，前端要用同一个路径
	// 注册路由（见 web/src/routes.tsx）。改一处而不改另一处，表现为一次邀请码
	// 注册的登录跳到一个打不开的地址。
	//
	// 与 /login/callback 同理，它**不在 /auth/ 下**：那一段被反向代理整段转发给
	// 服务端，前端在那里放路由会被服务端接走。
	RegistrationPath = "/register"
)

// registrationCookiePath 只覆盖兑换这一个 RPC：这份 cookie 没有理由出现在
// 任何别的请求里。用过程名而不是 "/" 是为了把暴露面收到最小。
var registrationCookiePath = identityv1connect.RegistrationServiceCompleteRegistrationProcedure

// pendingRegistration 是一份已校验、等待邀请码的渠道身份。
//
// 它与 pendingBinding 逐字同形，差别只在兑换之后做什么：那一份落到当前会话
// 主体上，这一份登记出一个**新主体**。形状相同是有意的——两者都是"渠道已经
// 确认过这个人是谁，剩下的一步需要一个已登录/已填码的动作来收尾"。
type pendingRegistration struct {
	source   string
	verified identity.VerifiedIdentity
}

// pendingRegistrations 是待注册凭据的一次性存储。
type pendingRegistrations struct {
	store *oneTimeStore[pendingRegistration]
	// secure 由对外源的协议派生，兑换成功时用它把浏览器侧的 cookie 也清掉，
	// 使删除请求的属性与写入时一致。
	secure bool
}

// newPendingRegistrations 构造一张有界的一次性待注册凭据表。
func newPendingRegistrations(now func() time.Time, secure bool) *pendingRegistrations {
	return &pendingRegistrations{
		store:  newOneTimeStore[pendingRegistration](registrationTTL, registrationMaxPending, now),
		secure: secure,
	}
}

// issue 记下一份已经校验过的身份，返回交给浏览器的凭据。
func (p *pendingRegistrations) issue(source string, verified identity.VerifiedIdentity) (string, error) {
	token, err := newOpaqueToken(registrationBytes)
	if err != nil {
		return "", err
	}
	p.store.issue(token, pendingRegistration{source: source, verified: verified})
	return token, nil
}

// consume 原子地取走一份待注册凭据。
func (p *pendingRegistrations) consume(token string) (pendingRegistration, bool) {
	return p.store.consume(token)
}

// peek 看一眼一份待注册凭据，**不取走、也不判过期**。
//
// 它只服务于"先看看这份凭据在不在"这一步（见 registration_service.go 里那三步的
// 顺序）：真正决定单次使用的仍然是 consume。**拿 peek 当判定用就丢掉了单次使用
// 与有效期**，因此调用方必须在最后一步仍然 consume 一次。
func (p *pendingRegistrations) peek(token string) (pendingRegistration, bool) {
	return p.store.peek(token)
}

// setRegistrationCookie 写下待注册凭据。
//
// SameSite=Strict 是可行的：兑换是一次**同源**的 RPC，不是跨站导航。
// HttpOnly 与作用路径确保它既不能被脚本读取，也不会出现在别的请求里。
func setRegistrationCookie(w http.ResponseWriter, token string, secure bool) {
	http.SetCookie(w, opaqueCookie(
		registrationCookie, token, registrationCookiePath,
		http.SameSiteStrictMode, int(registrationTTL.Seconds()), secure))
}

// registrationClearCookie 返回删除浏览器侧凭据的 cookie。
//
// 兑换成功之后服务端已经消费了它；这里的删除是让浏览器侧也保持一致，
// 而不是正确性的一部分。
func registrationClearCookie(secure bool) *http.Cookie {
	return opaqueCookie(
		registrationCookie, "", registrationCookiePath,
		http.SameSiteStrictMode, -1, secure)
}
