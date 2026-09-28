package client

import (
	"context"
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	identityv1 "github.com/poetlife/aladdin/api/gen/aladdin/identity/v1"
	"github.com/poetlife/aladdin/api/gen/aladdin/identity/v1/identityv1connect"
	"github.com/poetlife/aladdin/internal/observability"
	"github.com/poetlife/aladdin/internal/server/interceptor"
)

// 指向非回环地址却不给 TLS 配置：在建立连接之前就拒绝。
//
// 这条是底线而不是判断——判断哪些地址需要 TLS 由 config.CLIConfig 做；
// 这里只是不让一次漏传把 Bearer 凭证明文发到公网。
func TestDialRejectsPlaintextToNonLoopback(t *testing.T) {
	c, err := Dial(Options{Address: "aladdin.example.test:443", Timeout: time.Second})
	if err == nil {
		_ = c.Close()
		t.Fatal("指向非回环地址却未使用 TLS，Dial 应当报错")
	}
	if !strings.Contains(err.Error(), "TLS") {
		t.Errorf("错误信息应指出缺的是 TLS，得到 %q", err)
	}
}

// 回环地址不给 TLS 配置时用明文：本机开发与 SSH 隧道都依赖这条。
func TestDialUsesPlaintextForLoopback(t *testing.T) {
	c, err := Dial(Options{Address: "127.0.0.1:9090"})
	if err != nil {
		t.Fatalf("Dial 失败: %v", err)
	}
	if !strings.HasPrefix(c.baseURL, "http://") {
		t.Errorf("回环地址应当用明文，得到 baseURL=%q", c.baseURL)
	}
}

// 给了 TLS 配置就用 https，并且能真的握上手、走完一次 Connect 调用。
//
// 服务端是测试自带的自签证书；客户端用 RootCAs 校验它，而不是跳过校验——
// 测试里也走真实路径，否则"证书其实没被校验"这类问题会连同测试一起通过。
func TestDialUsesTLSAndInjectsRequestHeaders(t *testing.T) {
	// traceparent 由拦截器在起 client span 时写入，因此要先装一个真实的
	// 遥测实现：没有它就没有有效的 span，也就不会注入链路标识。
	provider, err := observability.NewProvider(context.Background(), observability.ProviderOptions{
		ServiceName: "test",
		SampleRatio: 1,
	})
	if err != nil {
		t.Fatalf("构建遥测实现失败: %v", err)
	}
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })

	var got http.Header
	var usedTLS bool
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		usedTLS = r.TLS != nil
		// Connect 的一元调用：请求体与响应体就是消息本身，空消息即空正文。
		w.Header().Set("Content-Type", "application/proto")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c, err := Dial(Options{
		Address: strings.TrimPrefix(srv.URL, "https://"),
		TLS:     &tls.Config{RootCAs: srv.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs},
		Token:   "test-token",
		Scope:   "test-scope",
		Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("Dial 失败: %v", err)
	}
	defer func() { _ = c.Close() }()

	ctx, cancel := c.Context()
	defer cancel()
	resp, err := NewService(c, identityv1connect.NewIdentityServiceClient).
		WhoAmI(ctx, connect.NewRequest(&identityv1.WhoAmIRequest{}))
	if err != nil {
		t.Fatalf("调用失败: %v", err)
	}
	if resp.Msg == nil {
		t.Fatal("响应消息为空")
	}

	if !usedTLS {
		t.Error("服务端没有看到 TLS，连接是明文的")
	}
	if userAgent := got.Get("User-Agent"); !strings.Contains(userAgent, "connect-go") {
		t.Errorf("请求头里没有 connect-go 的标识，得到 %q", userAgent)
	}
	if auth := got.Get(interceptor.HeaderAuthorization); auth != "Bearer test-token" {
		t.Errorf("%s = %q，期望 Bearer test-token", interceptor.HeaderAuthorization, auth)
	}
	if scope := got.Get(interceptor.HeaderScope); scope != "test-scope" {
		t.Errorf("%s = %q，期望 test-scope", interceptor.HeaderScope, scope)
	}
	if tp := got.Get("Traceparent"); tp == "" {
		t.Error("请求头里没有 traceparent：拦截器没有起 client span，或没有注入")
	}
}
