// Package client 提供访问 aladdin gRPC 服务的客户端构造。
//
// 它是客户端侧所有出站请求的唯一出口：凭证与链路标识的注入只在这里实现，
// CLI 与未来的其它调用方都复用它，避免出现"某个入口忘了带链路标识"。
package client

import (
	"context"
	"fmt"
	"sort"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"

	"github.com/poetlife/aladdin/internal/observability"
	"github.com/poetlife/aladdin/internal/server/interceptor"
)

// Options 是客户端构造参数。
type Options struct {
	// Address 是目标 gRPC 地址。
	Address string
	// Token 是访问凭证。为空表示匿名调用（只能访问公开方法）。
	Token string
	// Scope 是本次调用声明的作用域。为空时由服务端使用凭证的默认作用域。
	Scope string
	// Timeout 是单次调用的默认超时。
	Timeout time.Duration
}

// Client 是对内部 gRPC 连接的封装。
type Client struct {
	conn    *grpc.ClientConn
	options Options
}

// Dial 建立连接。
func Dial(opts Options) (*Client, error) {
	if opts.Address == "" {
		return nil, fmt.Errorf("目标地址不能为空")
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 30 * time.Second
	}

	conn, err := grpc.NewClient(opts.Address,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithChainUnaryInterceptor(unaryMetadataInterceptor(opts)),
	)
	if err != nil {
		return nil, fmt.Errorf("连接 %s 失败: %w", opts.Address, err)
	}
	return &Client{conn: conn, options: opts}, nil
}

// Conn 返回底层连接，用于构造各服务的客户端。
func (c *Client) Conn() *grpc.ClientConn { return c.conn }

// Context 返回一个带超时的 context。
//
// 链路标识**不在这里**注入：它由每个 RPC 的拦截器起 client span 时写入
// metadata。放在这里意味着"忘了调用本方法就丢了链路"，而调用点有很多个；
// 放在拦截器里则每个方法都必然带上。
func (c *Client) Context() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), c.options.Timeout)
}

// Close 关闭连接。
func (c *Client) Close() error { return c.conn.Close() }

// unaryMetadataInterceptor 注入链路标识与凭证，并为本次 RPC 起 client span。
//
// 这是客户端侧 metadata 的**唯一注入点**；调用方不得自行附加这些键，
// 否则会出现"某个方法带 scope、某个方法不带"的不一致。
func unaryMetadataInterceptor(opts Options) grpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, callOpts ...grpc.CallOption) error {
		ctx, span := observability.StartClientSpan(ctx, method)
		defer span.End()

		carrier := newMetadataCarrier()
		observability.InjectTraceparent(ctx, carrier)

		pairs := carrier.pairs()
		if opts.Token != "" {
			pairs = append(pairs, interceptor.HeaderAuthorization, "Bearer "+opts.Token)
		}
		if opts.Scope != "" {
			pairs = append(pairs, interceptor.HeaderScope, opts.Scope)
		}
		return invoker(metadata.AppendToOutgoingContext(ctx, pairs...), method, req, reply, cc, callOpts...)
	}
}

// metadataCarrier 把 gRPC metadata 适配成传播载体。
//
// 只承载传播头；凭证与作用域仍由调用点显式附加——混在一起会让
// "哪些键是传播头"变成需要读实现才能回答的问题。
type metadataCarrier struct {
	md metadata.MD
}

func newMetadataCarrier() *metadataCarrier {
	return &metadataCarrier{md: metadata.MD{}}
}

func (c *metadataCarrier) Get(key string) string {
	if values := c.md.Get(key); len(values) > 0 {
		return values[0]
	}
	return ""
}

func (c *metadataCarrier) Set(key, value string) {
	c.md.Set(key, value)
}

// Keys 返回已写入的键，排序后返回。
//
// 排序是为了让发出的 metadata 顺序稳定：map 遍历顺序随机，
// 会让同一份输入产生不一致的请求，测试与抓包对比都变得难做。
func (c *metadataCarrier) Keys() []string {
	keys := make([]string, 0, len(c.md))
	for key := range c.md {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// pairs 把已写入的传播头摊平成 metadata 的键值对序列。
func (c *metadataCarrier) pairs() []string {
	pairs := make([]string, 0, len(c.md)*2)
	for _, key := range c.Keys() {
		values := c.md.Get(key)
		if len(values) == 0 {
			continue
		}
		pairs = append(pairs, key, values[0])
	}
	return pairs
}
