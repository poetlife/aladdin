package server

import (
	"net/http"
	"time"

	identityv1connect "github.com/poetlife/aladdin/api/gen/aladdin/identity/v1/identityv1connect"
	"github.com/poetlife/aladdin/internal/identity"
)

const (
	// pendingBindingCookie 承载"服务端已经校验过、等待绑定的渠道身份"。
	//
	// 它**不是会话**：不携带主体、不携带权限，也不能用于登录；只有兑换这一个
	// RPC 会读它（见 docs/design/identity/identity-linking.md）。
	pendingBindingCookie = "aladdin_identity_binding"
	// pendingBindingTTL 是从回调完成到前端兑换之间的允许时长。
	pendingBindingTTL = 10 * time.Minute
	// pendingBindingMaxPending 是服务端同时记住的待绑定条数上界。
	pendingBindingMaxPending = 4096
	// pendingBindingBytes 是这份凭据的随机字节数：16 字节即 128 位。
	pendingBindingBytes = 16
)

// pendingBindingCookiePath 只覆盖兑换这一个 RPC：这份 cookie 没有理由
// 出现在任何别的请求里。用过程名而不是 "/" 是为了把暴露面收到最小。
var pendingBindingCookiePath = identityv1connect.IdentityServiceCompleteIdentityBindingProcedure

// pendingBinding 是一份已校验、等待绑定的渠道身份。
//
// 它**指的是身份，不是主体**：里面没有"目标主体"这一项。目标主体只在兑换
// 时从当前会话取，客户端无法指定——这正是"不存在把身份绑到任意主体"的
// 形状的落点。
type pendingBinding struct {
	source   string
	verified identity.VerifiedIdentity
}

// pendingBindings 是待绑定凭据的一次性存储。
type pendingBindings struct {
	store *oneTimeStore[pendingBinding]
	// secure 由对外源的协议派生，兑换成功时用它把浏览器侧的 cookie 也清掉，
	// 使删除请求的属性与写入时一致。
	secure bool
}

// newPendingBindings 构造一张有界的一次性待绑定凭据表。
func newPendingBindings(now func() time.Time, secure bool) *pendingBindings {
	return &pendingBindings{
		store:  newOneTimeStore[pendingBinding](pendingBindingTTL, pendingBindingMaxPending, now),
		secure: secure,
	}
}

// issue 记下一份已经校验过的身份，返回交给浏览器的凭据。
func (p *pendingBindings) issue(source string, verified identity.VerifiedIdentity) (string, error) {
	token, err := newOpaqueToken(pendingBindingBytes)
	if err != nil {
		return "", err
	}
	p.store.issue(token, pendingBinding{source: source, verified: verified})
	return token, nil
}

// consume 原子地取走一份待绑定凭据。
func (p *pendingBindings) consume(token string) (pendingBinding, bool) {
	return p.store.consume(token)
}

// setPendingBindingCookie 写下待绑定凭据。
//
// SameSite=Strict 是可行的：兑换是一次**同源**的 RPC，不是跨站导航。
// HttpOnly 与作用路径确保它既不能被脚本读取，也不会出现在别的请求里。
func setPendingBindingCookie(w http.ResponseWriter, token string, secure bool) {
	http.SetCookie(w, opaqueCookie(
		pendingBindingCookie, token, pendingBindingCookiePath,
		http.SameSiteStrictMode, int(pendingBindingTTL.Seconds()), secure))
}

// clearPendingBindingCookie 作废浏览器侧的待绑定凭据。
func clearPendingBindingCookie(w http.ResponseWriter, secure bool) {
	http.SetCookie(w, pendingBindingClearCookie(secure))
}

// pendingBindingClearCookie 返回删除浏览器侧凭据的 cookie。
//
// 兑换成功之后服务端已经消费了它；这里的删除是让浏览器侧也保持一致，
// 而不是正确性的一部分。
func pendingBindingClearCookie(secure bool) *http.Cookie {
	return opaqueCookie(
		pendingBindingCookie, "", pendingBindingCookiePath,
		http.SameSiteStrictMode, -1, secure)
}
