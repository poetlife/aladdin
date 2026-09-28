//go:build e2e

package e2e

import (
	"context"
	"net/http"
	"testing"
	"time"

	"connectrpc.com/connect"

	profilev1 "github.com/poetlife/aladdin/api/gen/aladdin/profile/v1"
	"github.com/poetlife/aladdin/api/gen/aladdin/profile/v1/profilev1connect"
	rbacv1 "github.com/poetlife/aladdin/api/gen/aladdin/rbac/v1"
	"github.com/poetlife/aladdin/api/gen/aladdin/rbac/v1/rbacv1connect"
	"github.com/poetlife/aladdin/internal/rbac"
)

// 本文件验证只读方法在 Connect 协议下同时接受 GET 与 POST。
//
// 它是 proto 上 idempotency_level = NO_SIDE_EFFECTS 唯一可见的行为后果，而这条
// 行为由**生成代码**决定：服务端没有任何手写的"接受 GET"开关，判定只看
// connect.WithIdempotency 是否被生成出来。因此它只能靠真跑一个服务端来守——
// 单测看不见它，`go build` 与 make lint 也看不见它。

// methodRecorder 记录出站请求实际使用的 HTTP 方法。
//
// 必须真记下来："Connect 调用成功了"证明不了走的是 GET——客户端在 URL 超长或
// 方法不够幂等时会回落成 POST。只有请求本身是 GET，才说明标注生效了。
type methodRecorder struct {
	base http.RoundTripper
	used []string
}

func (m *methodRecorder) RoundTrip(req *http.Request) (*http.Response, error) {
	m.used = append(m.used, req.Method)
	return m.base.RoundTrip(req)
}

// lastMethod 返回最近一次请求用的 HTTP 方法。
func (m *methodRecorder) lastMethod() string {
	if len(m.used) == 0 {
		return ""
	}
	return m.used[len(m.used)-1]
}

// getClient 构造一个被要求优先使用 GET 的 Connect 客户端。
func getClient(t *testing.T, h harness, token, scope string) (rbacv1connect.RBACServiceClient, *methodRecorder) {
	t.Helper()
	rec := &methodRecorder{base: &headerTransport{
		base:  http.DefaultTransport,
		token: token,
		scope: scope,
	}}
	httpClient := &http.Client{Timeout: 5 * time.Second, Transport: rec}
	return rbacv1connect.NewRBACServiceClient(
		httpClient,
		"http://"+h.address,
		connect.WithHTTPGet(),
	), rec
}

// TestGetPostAndGRPCSeeTheSameDecision 是本文件的核心断言：同一个只读方法在
// GET、POST、gRPC 三种形状下结论完全一致——GET 只是同一条路径的另一个动词，
// 不是一条绕过判定的旁路。
func TestGetPostAndGRPCSeeTheSameDecision(t *testing.T) {
	h := startServer(t, rbac.RoleViewer, testScope)

	scopes := []string{testScope, "tenant", "unrelated"}
	for _, scope := range scopes {
		t.Run(scope, func(t *testing.T) {
			getCli, rec := getClient(t, h, testToken, scope)
			_, getErr := getCli.ListRoles(context.Background(),
				connect.NewRequest(&rbacv1.ListRolesRequest{Scope: scope}))
			if got := rec.lastMethod(); got != http.MethodGet {
				t.Fatalf("期望真实请求走 GET，实际用了 %q（used=%v）", got, rec.used)
			}

			postCli := connectClient(t, h, testToken, scope)
			_, postErr := postCli.ListRoles(context.Background(),
				connect.NewRequest(&rbacv1.ListRolesRequest{Scope: scope}))

			grpcCli := h.dial(t, testToken, scope)
			ctx, cancel := grpcCli.Context()
			defer cancel()
			_, grpcErr := rbacv1.NewRBACServiceClient(grpcCli.Conn()).
				ListRoles(ctx, &rbacv1.ListRolesRequest{Scope: scope})

			if rpcCode(getErr) != rpcCode(postErr) || rpcCode(getErr) != rpcCode(grpcErr) {
				t.Fatalf("三种形状结论不一致：get=%d post=%d grpc=%d (getErr=%v)",
					rpcCode(getErr), rpcCode(postErr), rpcCode(grpcErr), getErr)
			}
		})
	}
}

// TestServerAcceptsRawGetOnReadOnlyMethod 用**裸 GET** 打一个只读方法。
//
// 不走客户端是有意的：客户端会自己挑动词，因此它证明不了服务端那道闸是开的。
// 这里手工拼出 Connect 的 GET 形状（encoding + message 两个查询参数，缺一即
// InvalidArgument），失败原因才是唯一的。
func TestServerAcceptsRawGetOnReadOnlyMethod(t *testing.T) {
	h := startServer(t, rbac.RoleViewer, testScope)

	// GetAuthMethods 是公开方法，因此这一步不掺入任何凭证因素。
	url := "http://" + h.address +
		"/aladdin.identity.v1.IdentityService/GetAuthMethods?encoding=json&message=%7B%7D"
	resp, err := rawGet(t, url, "")
	if err != nil {
		t.Fatalf("裸 GET: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("状态码 = %d, want 200（只读方法应当接受 GET）", resp.StatusCode)
	}
}

// TestServerRejectsGetOnWriteMethod 证明"接受 GET"是逐方法的。
//
// 同样必须用裸 GET：客户端不会对非幂等方法使用 GET，走客户端就永远测不到这道闸。
// 选 DeleteAvatar 是因为它是 authenticated_only 的写方法——不掺权限因素，
// 405 只能来自"这个方法没被标注为无副作用"。
func TestServerRejectsGetOnWriteMethod(t *testing.T) {
	h := startServer(t, rbac.RoleViewer, testScope)

	url := "http://" + h.address +
		"/aladdin.profile.v1.ProfileService/DeleteAvatar?encoding=json&message=%7B%7D"
	resp, err := rawGet(t, url, testToken)
	if err != nil {
		t.Fatalf("裸 GET: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("状态码 = %d, want 405（写方法不因加注解以外的方式接受 GET）", resp.StatusCode)
	}
	if allow := resp.Header.Get("Allow"); allow != http.MethodPost {
		t.Errorf("Allow = %q, want POST", allow)
	}
}

// rawGet 发一个裸 GET，可选带会话凭证。
func rawGet(t *testing.T, url, token string) (*http.Response, error) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("构造请求: %v", err)
	}
	if token != "" {
		req.Header.Set("authorization", "Bearer "+token)
	}
	return (&http.Client{Timeout: 5 * time.Second}).Do(req)
}

// TestClientWithHttpGetStillPostsWriteMethods 固定客户端侧的取舍：**即使显式开了
// WithHTTPGet**，客户端也不会对写方法使用 GET。
//
// 这条是"GET 是额外许可、不是默认形状"的证据：两端判断是否用 GET 依据的都是
// 同一份生成代码里的幂等标注，而写方法没有它。
func TestClientWithHttpGetStillPostsWriteMethods(t *testing.T) {
	h := startServer(t, rbac.RoleViewer, testScope)

	rec := &methodRecorder{base: &headerTransport{
		base:  http.DefaultTransport,
		token: testToken,
		scope: testScope,
	}}
	httpClient := &http.Client{Timeout: 5 * time.Second, Transport: rec}
	client := profilev1connect.NewProfileServiceClient(
		httpClient,
		"http://"+h.address,
		connect.WithHTTPGet(),
	)

	if _, err := client.DeleteAvatar(context.Background(),
		connect.NewRequest(&profilev1.DeleteAvatarRequest{})); err != nil {
		t.Fatalf("DeleteAvatar: %v", err)
	}
	if got := rec.lastMethod(); got != http.MethodPost {
		t.Errorf("写方法即使用户端开了 GET 也应当走 POST，实际 %q", got)
	}
}
