// Package client 提供访问 aladdin gRPC 服务的客户端构造。
//
// 它是客户端侧所有出站请求的唯一出口：凭证与链路标识的注入只在这里实现，
// CLI 与未来的其它调用方都复用它，避免出现"某个入口忘了带 trace_id"。
package client

import (
	"context"
	"fmt"
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
	// TraceID 是链路标识。为空时自动生成——CLI 每次调用生成一个新的。
	TraceID string
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
	if opts.TraceID == "" {
		opts.TraceID = observability.NewTraceID()
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

// TraceID 返回本次会话使用的链路标识。
//
// CLI 用它把本地日志与服务端日志串联起来（见 docs/observability.md）。
func (c *Client) TraceID() string { return c.options.TraceID }

// Context 返回一个带超时与链路标识的 context。
func (c *Client) Context() (context.Context, context.CancelFunc) {
	ctx := observability.WithTraceID(context.Background(), c.options.TraceID)
	return context.WithTimeout(ctx, c.options.Timeout)
}

// Close 关闭连接。
func (c *Client) Close() error { return c.conn.Close() }

// unaryMetadataInterceptor 注入凭证、作用域与链路标识。
//
// 这是客户端侧 metadata 的唯一注入点；调用方不得自行附加这些键，
// 否则会出现"某个方法带 scope、某个方法不带"的不一致。
func unaryMetadataInterceptor(opts Options) grpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, callOpts ...grpc.CallOption) error {
		pairs := []string{observability.TraceIDHeader, opts.TraceID}
		if opts.Token != "" {
			pairs = append(pairs, interceptor.HeaderAuthorization, "Bearer "+opts.Token)
		}
		if opts.Scope != "" {
			pairs = append(pairs, interceptor.HeaderScope, opts.Scope)
		}
		return invoker(metadata.AppendToOutgoingContext(ctx, pairs...), method, req, reply, cc, callOpts...)
	}
}
