//go:build e2e

package e2e

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/grpc/status"

	rbacv1 "github.com/poetlife/aladdin/api/gen/aladdin/rbac/v1"
	"github.com/poetlife/aladdin/api/gen/aladdin/rbac/v1/rbacv1connect"
	"github.com/poetlife/aladdin/internal/rbac"
)

// 本文件验证 Connect 协议路径。
//
// authz_test.go 走的是 gRPC 协议（CLI 的 grpc-go 客户端），这里走 Connect 协议
// （浏览器）。两者打的是**同一个地址、同一份 handler、同一套业务实现**——
// 这正是选 Connect 的理由。两条路径都过，才说明改造没有引入协议相关的分叉。

// connectClient 构造一个走 Connect 协议的客户端。
//
// 用默认的 http.Client（HTTP/1.1）即可：Connect 协议不要求 HTTP/2，
// 只有 gRPC 协议才要求。
func connectClient(t *testing.T, h harness, token, scope string) rbacv1connect.RBACServiceClient {
	t.Helper()
	httpClient := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &headerTransport{
			base:  http.DefaultTransport,
			token: token,
			scope: scope,
		},
	}
	return rbacv1connect.NewRBACServiceClient(httpClient, "http://"+h.address)
}

// headerTransport 给每个请求注入凭证与作用域。
//
// 生产前端由 Connect 的 Interceptor 承担这件事（见 web/src/api/transport.ts）；
// 这里用 Transport 是因为测试不关心前端的拦截器实现，只关心协议本身。
type headerTransport struct {
	base  http.RoundTripper
	token string
	scope string
}

func (t *headerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.token != "" {
		req.Header.Set("authorization", "Bearer "+t.token)
	}
	if t.scope != "" {
		req.Header.Set("aladdin-scope", t.scope)
	}
	return t.base.RoundTrip(req)
}

// TestConnectProtocolServesSameAuthz 验证 Connect 协议路径上的鉴权行为与 gRPC 一致。
func TestConnectProtocolServesSameAuthz(t *testing.T) {
	h := startServer(t, rbac.RoleViewer, testScope)

	tests := []struct {
		name    string
		scope   string
		wantErr int
	}{
		{"子作用域放行", "tenant/acme/project/web", codeOK},
		{"自身作用域放行", "tenant/acme", codeOK},
		{"父作用域拒绝", "tenant", codePermissionDenied},
		{"兄弟作用域拒绝", "tenant/other", codePermissionDenied},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := connectClient(t, h, testToken, tt.scope)
			_, err := client.ListRoles(context.Background(), connect.NewRequest(&rbacv1.ListRolesRequest{Scope: tt.scope}))
			if got := rpcCode(err); got != tt.wantErr {
				t.Fatalf("错误码 = %d, want %d (err=%v)", got, tt.wantErr, err)
			}
		})
	}
}

// TestConnectProtocolUnauthenticated 验证认证失败在 Connect 协议上的编码。
//
// 这一条尤其重要：认证发生在 HTTP 中间件里，拒绝响应由 ErrorWriter 按协议写出。
// 如果它写错格式，Connect 客户端拿到的会是"未知错误"而不是 Unauthenticated。
func TestConnectProtocolUnauthenticated(t *testing.T) {
	h := startServer(t, rbac.RoleViewer, testScope)

	t.Run("未携带凭证", func(t *testing.T) {
		client := connectClient(t, h, "", testScope)
		_, err := client.ListRoles(context.Background(), connect.NewRequest(&rbacv1.ListRolesRequest{Scope: testScope}))
		if got := rpcCode(err); got != codeUnauthenticated {
			t.Fatalf("错误码 = %d, want Unauthenticated (err=%v)", got, err)
		}
	})

	t.Run("凭证无效", func(t *testing.T) {
		client := connectClient(t, h, "bogus", testScope)
		_, err := client.ListRoles(context.Background(), connect.NewRequest(&rbacv1.ListRolesRequest{Scope: testScope}))
		if got := rpcCode(err); got != codeUnauthenticated {
			t.Fatalf("错误码 = %d, want Unauthenticated (err=%v)", got, err)
		}
	})
}

// TestConnectProtocolReadsDenialDetail 验证拒绝原因在 Connect 协议上可被结构化读取。
//
// 前端据此决定提示文案与是否重试，因此它必须能被解码，
// 而不是只能靠匹配错误文本。
func TestConnectProtocolReadsDenialDetail(t *testing.T) {
	h := startServer(t, rbac.RoleViewer, testScope)
	// 请求父作用域：有权限但作用域不够。
	client := connectClient(t, h, testToken, "tenant")

	_, err := client.ListRoles(context.Background(), connect.NewRequest(&rbacv1.ListRolesRequest{Scope: "tenant"}))
	if rpcCode(err) != codePermissionDenied {
		t.Fatalf("期望 PermissionDenied，实际 %v", err)
	}

	var connectErr *connect.Error
	if !errors.As(err, &connectErr) {
		t.Fatalf("不是 Connect 错误: %v", err)
	}
	var reason rbac.Reason
	for _, detail := range connectErr.Details() {
		msg, valueErr := detail.Value()
		if valueErr != nil {
			continue
		}
		if d, ok := msg.(*rbacv1.DenialDetail); ok {
			reason = d.GetReason()
		}
	}
	if reason != rbac.ReasonScopeMismatch {
		t.Errorf("原因 = %s, want %s", reason, rbac.ReasonScopeMismatch)
	}
}

// TestConnectAndGRPCSeeIdenticalDecisions 是本文件的核心断言：
// 同一个请求无论走哪种协议，判定结论必须完全一致。
//
// 如果这条失败了，说明判定逻辑跑到了协议层之上或之下，
// 而不是在协议无关的拦截器里——那正是这次改造要避免的分叉。
func TestConnectAndGRPCSeeIdenticalDecisions(t *testing.T) {
	h := startServer(t, rbac.RoleViewer, testScope)

	scopes := []string{"tenant/acme", "tenant/acme/project", "tenant", "tenant/other", "unrelated"}
	for _, scope := range scopes {
		t.Run(scope, func(t *testing.T) {
			// gRPC 路径
			grpcClient := h.dial(t, testToken, scope)
			ctx, cancel := grpcClient.Context()
			defer cancel()
			_, grpcErr := rbacv1.NewRBACServiceClient(grpcClient.Conn()).
				ListRoles(ctx, &rbacv1.ListRolesRequest{Scope: scope})

			// Connect 路径
			connectCli := connectClient(t, h, testToken, scope)
			_, connectErr := connectCli.ListRoles(context.Background(),
				connect.NewRequest(&rbacv1.ListRolesRequest{Scope: scope}))

			if rpcCode(connectErr) != rpcCode(grpcErr) {
				t.Fatalf("两条协议结论不一致：connect=%d grpc=%d (connectErr=%v, grpcErr=%v)",
					rpcCode(connectErr), rpcCode(grpcErr), connectErr, grpcErr)
			}
		})
	}
}

// gRPC 与 Connect 的错误码取值一一对应，因此可以统一成整数直接比较。
const (
	codeOK               = 0
	codeUnauthenticated  = 16
	codePermissionDenied = 7
)

// rpcCode 把任意 RPC 错误归一化为状态码。
//
// 不能直接用 connect.CodeOf：它只认 *connect.Error，遇到 grpc-go 的
// status 错误会返回 unknown——而本文件恰恰要把两条协议的结论放在一起比较，
// 必须两边都能解。
func rpcCode(err error) int {
	if err == nil {
		return codeOK
	}
	var connectErr *connect.Error
	if errors.As(err, &connectErr) {
		return int(connectErr.Code())
	}
	if st, ok := status.FromError(err); ok {
		return int(st.Code())
	}
	return -1
}
