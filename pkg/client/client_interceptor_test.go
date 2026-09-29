package client

import (
	"context"
	"net/http"
	"testing"

	"connectrpc.com/connect"

	"github.com/poetlife/aladdin/internal/observability"
	"github.com/poetlife/aladdin/internal/server/interceptor"
)

// **流式调用也必须带上凭证与作用域。**
//
// 这条用例守的是一个静默失败：注入如果只用 connect.UnaryInterceptorFunc，它对
// 流式调用是空实现——调用照发，只是不带凭证，服务端回一句"未认证"或"权限不对"，
// 而真正的原因（拦截器没覆盖这条形状）在客户端一点痕迹都没有。
func TestInterceptorInjectsHeadersOnStreamingCalls(t *testing.T) {
	c := &Client{options: Options{Token: "凭证", Scope: "作用域"}}
	conn := c.interceptor().WrapStreamingClient(
		func(_ context.Context, _ connect.Spec) connect.StreamingClientConn {
			return &fakeStreamConn{header: http.Header{}}
		},
	)(context.Background(), connect.Spec{Procedure: "/aladdin.galaxy.v1.GalaxyService/WatchProject"})

	if got := conn.RequestHeader().Get(interceptor.HeaderAuthorization); got != "Bearer 凭证" {
		t.Errorf("Authorization = %q，期望 %q", got, "Bearer 凭证")
	}
	if got := conn.RequestHeader().Get(interceptor.HeaderScope); got != "作用域" {
		t.Errorf("作用域头 = %q，期望 %q", got, "作用域")
	}
}

// unary 那条不变，这是重构拦截器之后的最低保证。
func TestInterceptorInjectsHeadersOnUnaryCalls(t *testing.T) {
	c := &Client{options: Options{Token: "凭证", Scope: "作用域"}}
	req := connect.NewRequest(&struct{}{})
	if _, err := c.interceptor().WrapUnary(
		func(_ context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
			return connect.NewResponse(&struct{}{}), nil
		},
	)(context.Background(), req); err != nil {
		t.Fatalf("调用失败: %v", err)
	}

	if got := req.Header().Get(interceptor.HeaderAuthorization); got != "Bearer 凭证" {
		t.Errorf("Authorization = %q，期望 %q", got, "Bearer 凭证")
	}
	if got := req.Header().Get(interceptor.HeaderScope); got != "作用域" {
		t.Errorf("作用域头 = %q，期望 %q", got, "作用域")
	}
}

// 凭证为空时**不写这个头**，而不是写一个空的 Bearer：后者会让服务端把一次匿名
// 调用当成一次凭证无效，两者的处理完全不同。
func TestInterceptorOmitsEmptyCredential(t *testing.T) {
	c := &Client{}
	conn := c.interceptor().WrapStreamingClient(
		func(_ context.Context, _ connect.Spec) connect.StreamingClientConn {
			return &fakeStreamConn{header: http.Header{}}
		},
	)(context.Background(), connect.Spec{Procedure: "/aladdin.galaxy.v1.GalaxyService/WatchProject"})

	if got := conn.RequestHeader().Get(interceptor.HeaderAuthorization); got != "" {
		t.Errorf("匿名调用带上了 Authorization = %q", got)
	}
}

// fakeStreamConn 只承载请求头：本用例要看的全部内容都在那上面，其余方法用不到
// （用到了会 panic，那正是"没用到"的证据）。
type fakeStreamConn struct {
	connect.StreamingClientConn
	header http.Header
}

func (c *fakeStreamConn) RequestHeader() http.Header { return c.header }

// 出站请求必须带上上报端标识：服务端请求留痕据此区分命令行与浏览器。
// **匿名也要带**：它不是凭证，不随凭证存在与否变化。
func TestInterceptorInjectsClientID(t *testing.T) {
	c := &Client{}

	req := connect.NewRequest(&struct{}{})
	if _, err := c.interceptor().WrapUnary(
		func(_ context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
			return connect.NewResponse(&struct{}{}), nil
		},
	)(context.Background(), req); err != nil {
		t.Fatalf("调用失败: %v", err)
	}
	if got := req.Header().Get(observability.HeaderClient); got != observability.ClientCLI {
		t.Errorf("unary 的 %s = %q，期望 %q", observability.HeaderClient, got, observability.ClientCLI)
	}

	conn := c.interceptor().WrapStreamingClient(
		func(_ context.Context, _ connect.Spec) connect.StreamingClientConn {
			return &fakeStreamConn{header: http.Header{}}
		},
	)(context.Background(), connect.Spec{Procedure: "/aladdin.galaxy.v1.GalaxyService/WatchProject"})
	if got := conn.RequestHeader().Get(observability.HeaderClient); got != observability.ClientCLI {
		t.Errorf("流式的 %s = %q，期望 %q", observability.HeaderClient, got, observability.ClientCLI)
	}
}
