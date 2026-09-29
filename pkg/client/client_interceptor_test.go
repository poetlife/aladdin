package client

import (
	"context"
	"net/http"
	"testing"
	"time"

	"connectrpc.com/connect"

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
	)(context.Background(), connect.Spec{Procedure: "/aladdin.events.v1.EventsService/Watch"})

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
	)(context.Background(), connect.Spec{Procedure: "/aladdin.events.v1.EventsService/Watch"})

	if got := conn.RequestHeader().Get(interceptor.HeaderAuthorization); got != "" {
		t.Errorf("匿名调用带上了 Authorization = %q", got)
	}
}

// **http.Client 不带整体超时。**
//
// Timeout 覆盖读完整个响应体。设上它，一条订阅会在几十秒后被客户端自己掐断，
// 而调用方传入的更长 context 救不了——超时发生在传输层。unary 的时限改由
// boundCall 补到还没有截止时间的 context 上。
func TestDialDoesNotTimeOutTheResponseBody(t *testing.T) {
	c, err := Dial(Options{Address: "127.0.0.1:9", Timeout: time.Second})
	if err != nil {
		t.Fatalf("Dial 失败: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	if c.httpClient.Timeout != 0 {
		t.Fatalf("http.Client.Timeout = %s，长连接会被这个时限掐断", c.httpClient.Timeout)
	}
}

// 没有截止时间的 unary 调用仍受 Timeout 约束：拿掉 http.Client.Timeout 之后，
// 这条不能一起消失，否则一次忘了传 context 的调用会一直挂着。
func TestUnaryCallWithoutDeadlineInheritsTimeout(t *testing.T) {
	c := &Client{options: Options{Timeout: time.Hour}}
	_, err := c.interceptor().WrapUnary(
		func(ctx context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
			deadline, ok := ctx.Deadline()
			if !ok {
				t.Fatal("unary 调用没有截止时间")
			}
			remaining := time.Until(deadline)
			if remaining < 30*time.Minute || remaining > time.Hour {
				t.Fatalf("剩余时间 = %s，期望接近 Timeout（1 小时）", remaining)
			}
			return connect.NewResponse(&struct{}{}), nil
		},
	)(context.Background(), connect.NewRequest(&struct{}{}))
	if err != nil {
		t.Fatalf("调用失败: %v", err)
	}
}

// 调用方已经给了截止时间时，不拿默认超时去覆盖它。
func TestUnaryCallKeepsExistingDeadline(t *testing.T) {
	c := &Client{options: Options{Timeout: time.Millisecond}}
	ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
	defer cancel()

	_, err := c.interceptor().WrapUnary(
		func(ctx context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
			deadline, ok := ctx.Deadline()
			if !ok {
				t.Fatal("已有的截止时间丢了")
			}
			if time.Until(deadline) < 30*time.Minute {
				t.Fatalf("已有的截止时间被收成了默认超时，还剩 %s", time.Until(deadline))
			}
			return connect.NewResponse(&struct{}{}), nil
		},
	)(ctx, connect.NewRequest(&struct{}{}))
	if err != nil {
		t.Fatalf("调用失败: %v", err)
	}
}

// 流式调用不套 unary 的短超时：寿命由调用方的 context 决定。
func TestStreamingCallDoesNotInheritUnaryTimeout(t *testing.T) {
	c := &Client{options: Options{Timeout: time.Millisecond}}
	var hasDeadline bool
	_ = c.interceptor().WrapStreamingClient(
		func(ctx context.Context, _ connect.Spec) connect.StreamingClientConn {
			_, hasDeadline = ctx.Deadline()
			return &fakeStreamConn{header: http.Header{}}
		},
	)(context.Background(), connect.Spec{Procedure: "/aladdin.events.v1.EventsService/Watch"})
	if hasDeadline {
		t.Fatal("流式调用被套上了 unary 的超时")
	}
}

// fakeStreamConn 只承载请求头：本用例要看的全部内容都在那上面，其余方法用不到
// （用到了会 panic，那正是"没用到"的证据）。
type fakeStreamConn struct {
	connect.StreamingClientConn
	header http.Header
}

func (c *fakeStreamConn) RequestHeader() http.Header { return c.header }
