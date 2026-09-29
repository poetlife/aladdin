// Package client 提供访问 aladdin RPC 服务的客户端构造。
//
// 它是客户端侧所有出站请求的唯一出口：传输方式、凭证与链路标识的注入只在这里
// 实现，CLI 与未来的其它调用方都复用它，避免出现"某个入口忘了带链路标识"
// 或"某个入口忘了加密"。
//
// **协议是 Connect**，与浏览器一致。命令行不走原生 gRPC 是有依据的：它要经反向
// 代理出网，而反向代理转发 gRPC 时会丢掉空正文响应的 trailers——也就是**所有
// 错误响应**（见 docs/debugging/registry.md）。Connect 把错误放在 HTTP 状态与
// 响应体里，不依赖 trailers，走的正是浏览器每天在用的那条路径。
package client

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"sort"
	"time"

	"connectrpc.com/connect"

	"github.com/poetlife/aladdin/internal/loopback"
	"github.com/poetlife/aladdin/internal/observability"
	"github.com/poetlife/aladdin/internal/server/interceptor"
)

// Options 是客户端构造参数。
type Options struct {
	// Address 是目标地址，形如 host:port，不含 scheme。
	Address string
	// TLS 非空时走 https；为空表示 http，**只允许回环地址**。
	//
	// 是否使用 TLS 由调用方按目标地址推导（见 config.CLIConfig.TLSConfig），
	// 本包不重复那个判断——但会把"非回环不得明文"这条底线守住，见 Dial。
	// 测试注入的传输配置也只经此字段进入，因此不存在面向用户的
	// "跳过证书校验"开关。
	TLS *tls.Config
	// Token 是访问凭证。为空表示匿名调用（只能访问公开方法）。
	Token string
	// Scope 是本次调用声明的作用域。为空时由服务端使用凭证的默认作用域。
	Scope string
	// Timeout 是单次调用的默认超时。
	Timeout time.Duration
}

// Client 是 RPC 客户端的构造源。
type Client struct {
	httpClient *http.Client
	baseURL    string
	options    Options
}

// Dial 建立客户端的传输部分。
//
// 明文只允许通向回环地址（本机开发、SSH 隧道）：指向别处却不给 TLS 配置时
// 在这里就失败。凭证是 Authorization: Bearer，明文过境等于把它交出去，
// 因此这条底线由本包兜住——调用方漏传一次也不会悄悄发出去。
func Dial(opts Options) (*Client, error) {
	if opts.Address == "" {
		return nil, fmt.Errorf("目标地址不能为空")
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 30 * time.Second
	}

	scheme := "http"
	transport := &http.Transport{}
	switch {
	case opts.TLS != nil:
		scheme = "https"
		transport.TLSClientConfig = opts.TLS
		// 设了 TLSClientConfig 就会关掉自动 HTTP/2，必须显式打开：
		// 服务端与反向代理都按 h2 提供服务。
		transport.ForceAttemptHTTP2 = true
	case loopback.IsAddress(opts.Address):
	default:
		return nil, fmt.Errorf("目标地址 %s 不是回环地址，必须使用 TLS", opts.Address)
	}

	return &Client{
		httpClient: &http.Client{Transport: transport, Timeout: opts.Timeout},
		baseURL:    scheme + "://" + opts.Address,
		options:    opts,
	}, nil
}

// NewService 用本客户端的传输与注入参数构造一个服务客户端。
//
// 生成代码里的构造函数签名统一是 (connect.HTTPClient, baseURL, ...ClientOption)，
// 因此这里能泛型地接住它们。**调用方不得自己拼这几个参数**：漏掉拦截器就等于
// 这次调用不带凭证与链路标识，而它不会报错，只会表现为"权限不对"或"链路断了"。
func NewService[T any](c *Client, ctor func(connect.HTTPClient, string, ...connect.ClientOption) T) T {
	return ctor(c.httpClient, c.baseURL, connect.WithInterceptors(c.interceptor()))
}

// Context 返回一个带超时的 context。
//
// 链路标识**不在这里**注入：它由拦截器起 client span 时写进请求头。放在这里
// 意味着"忘了调用本方法就丢了链路"，而调用点有很多个；放在拦截器里则每个方法
// 都必然带上。
func (c *Client) Context() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), c.options.Timeout)
}

// Close 释放空闲连接。
func (c *Client) Close() error {
	c.httpClient.CloseIdleConnections()
	return nil
}

// interceptor 是本客户端的拦截器：注入链路标识与凭证，并为调用起 client span。
//
// **它必须同时覆盖 unary 与流式调用。** 生成出来的客户端把两条形状都暴露出来
// （例如 galaxy 的订阅通道），而 connect.UnaryInterceptorFunc 对**流式调用是
// 空实现**——用它的话，一次流式调用会静默地不带凭证与作用域，表现为"权限不对"
// 而不是一个看得见的错误。
func (c *Client) interceptor() connect.Interceptor { return clientInterceptor{client: c} }

// clientInterceptor 把请求头的注入接到 Connect 的两个客户端入口上。
type clientInterceptor struct {
	client *Client
}

// WrapUnary 实现 connect.Interceptor。
func (i clientInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		ctx, span := observability.StartClientSpan(ctx, req.Spec().Procedure)
		defer span.End()
		i.client.injectHeaders(ctx, req.Header())
		return next(ctx, req)
	}
}

// WrapStreamingClient 实现 connect.Interceptor：注入同一批请求头。
//
// **流式调用不起 client span。** 一条流是一个长连接，"这次调用"的结束点得挂在
// 连接关闭上，而把 span 的结束点挂错（或漏挂）比没有 span 更坏——它会留下一批
// 永不结束的 span。命令行目前不消费任何流（订阅通道由浏览器消费）；真要给它接上
// 链路，这里该补一个包住连接的实现，而不是随手起一个不结束的 span。
func (i clientInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return func(ctx context.Context, spec connect.Spec) connect.StreamingClientConn {
		conn := next(ctx, spec)
		// 请求头是**懒发送**的：连接建好之后、第一次发送之前仍然可以改。
		i.client.injectHeaders(ctx, conn.RequestHeader())
		return conn
	}
}

// WrapStreamingHandler 是空实现：本类型只用在客户端，服务端那一侧不经过它。
func (i clientInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return next
}

// injectHeaders 注入凭证、作用域与链路标识（客户端侧请求头的**唯一注入点**）。
//
// 调用方不得自行附加这些键，否则会出现"某个方法带 scope、某个方法不带"的不一致。
func (c *Client) injectHeaders(ctx context.Context, header http.Header) {
	// 请求头就是传播载体，包一层适配器即可（TextMapCarrier 比
	// http.Header 多一个 Keys，见下面的 headerCarrier）。
	observability.InjectTraceparent(ctx, headerCarrier{header: header})
	// 上报端标识：服务端请求留痕据此区分命令行与浏览器。写死在这里，
	// 与客户端事件的 Client.CLI 同源（见 observability.HeaderClient）。
	header.Set(observability.HeaderClient, observability.ClientCLI)
	if c.options.Token != "" {
		header.Set(interceptor.HeaderAuthorization, "Bearer "+c.options.Token)
	}
	if c.options.Scope != "" {
		header.Set(interceptor.HeaderScope, c.options.Scope)
	}
}

// headerCarrier 把 http.Header 适配成 observability 的传播载体。
//
// 只承载传播头；凭证与作用域由拦截器显式附加——混在一起会让"哪些键是传播头"
// 变成需要读实现才能回答的问题。
//
// Keys 是接口里比 http.Header 多出来的那一个方法：注入只用得上 Set，它只为
// 满足接口。实现成排序结果，不把 map 的随机遍历顺序暴露出去。
type headerCarrier struct{ header http.Header }

func (c headerCarrier) Get(key string) string { return c.header.Get(key) }

func (c headerCarrier) Set(key, value string) { c.header.Set(key, value) }

func (c headerCarrier) Keys() []string {
	keys := make([]string, 0, len(c.header))
	for key := range c.header {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
