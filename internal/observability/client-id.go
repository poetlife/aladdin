package observability

import "net/http"

// HeaderClient 是**上报端标识**在请求头中的名字。
//
// 它回答的是请求留痕里加了这一项才答得了的问题：这次 RPC 是浏览器发的还是
// 命令行发的。两者的失败模式与排障入口不同（浏览器可能是会话失效，命令行可能是
// 凭证文件不对），只按过程名与结果码分组时分不出来。
//
// 名字与 TraceparentHeader 同理：**它是协议面的一处约定**，服务端读取、Web 与
// CLI 各注入一次，前端有一份等价的常量（TypeScript 引用不了 Go 常量，见
// docs/observability.md）。
const HeaderClient = "x-aladdin-client"

// 上报端的取值。只有这两个，且必须与客户端事件里 Client 枚举的日志取值一致。
//
// 取值受白名单约束（见 ClientFromHeader）：一个任意字符串不能变成日志里的一个
// 新取值，否则"按端检索"会退化成无界取值。
const (
	ClientWeb = "web"
	ClientCLI = "cli"
)

// ClientFromHeader 读取上报端标识。
//
// 未携带、或取值不在白名单内时返回 false：调用方据此**完全不写**这个字段，
// 而不是写一个未校验的原值。留痕缺一个字段是可接受的，写入一个无界取值则会让
// 基于该字段的聚合失去上界。
func ClientFromHeader(header http.Header) (string, bool) {
	switch v := header.Get(HeaderClient); v {
	case ClientWeb, ClientCLI:
		return v, true
	default:
		return "", false
	}
}
