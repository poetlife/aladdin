// Package loopback 判定一个主机或目标地址是否为本地回环。
//
// 它是这个判断的唯一入口：配置层用它决定"对外地址是否允许 http"，
// 客户端用它决定"这条连接是否允许明文"。两处各写一份的后果是判定漂移——
// 一处放行的地址在另一处被拒，表现为"本地开发突然连不上"。
package loopback

import "net"

// IsHost 判断主机名或 IP 字面量是否为回环。
//
// 接受带方括号的 IPv6 字面量（如 `[::1]`）：调用方拿到的可能是从地址里
// 切出来的原始片段，剥不剥由这一处决定。
func IsHost(host string) bool {
	if len(host) > 1 && host[0] == '[' && host[len(host)-1] == ']' {
		host = host[1 : len(host)-1]
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// IsAddress 判断一个 `host:port` 目标是否为回环。
//
// 切不开的写法（缺端口、非法字符）一律按**非回环**处理：这个判断的用途是
// "是否允许明文"，遇到不认识的输入从严，不因为看不明白就放行。
func IsAddress(hostPort string) bool {
	host, _, err := net.SplitHostPort(hostPort)
	if err != nil {
		return false
	}
	return IsHost(host)
}
