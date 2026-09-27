package server

import "net/http"

// opaqueCookie 构造一份"服务端签发、浏览器不可读"的 cookie。
//
// 服务端签发的 cookie 一共四份写法——导航状态与待绑定凭据的写入与清空——
// 它们共用这一个构造器：属性集合只应有一处实现。四处各写一遍的话，将来加一个
// 属性（例如 Partitioned）或修正清空语义时漏改一处，写出的是一个"服务端认为
// 已作废、浏览器仍保留"的 cookie，而它表现为一次无法解释的绑定或登录失败
// （见 docs/ssot-registry.md）。
//
// 差异化的部分（名字、取值、作用路径、SameSite、有效期、是否要求加密传输）
// 由调用方给出：这些取值各 cookie 之间确实不同，共享的应当是构造方式。
func opaqueCookie(name, value, path string, sameSite http.SameSite, maxAge int, secure bool) *http.Cookie {
	//nolint:gosec // HttpOnly 与 SameSite 由调用方显式给出；Secure 由对外源的协议派生
	return &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     path,
		HttpOnly: true,
		Secure:   secure,
		SameSite: sameSite,
		MaxAge:   maxAge,
	}
}

// cookieValue 从 Connect 请求头里取一份 cookie 的值。
//
// Connect 的 Request 只暴露 http.Header，这里把它还原成 net/http 的取值入口：
// **cookie 的解析只应有这一处实现**。空值按不存在处理——一份空 cookie 与一份
// 缺失的 cookie 对调用方是同一件事。
func cookieValue(header http.Header, name string) (string, bool) {
	req := &http.Request{Header: header}
	cookie, err := req.Cookie(name)
	if err != nil || cookie.Value == "" {
		return "", false
	}
	return cookie.Value, true
}
