//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"runtime"
	"testing"
	"time"

	"connectrpc.com/connect"

	opsv1 "github.com/poetlife/aladdin/api/gen/aladdin/ops/v1"
	"github.com/poetlife/aladdin/api/gen/aladdin/ops/v1/opsv1connect"
	"github.com/poetlife/aladdin/internal/buildinfo"
	"github.com/poetlife/aladdin/internal/rbac"
)

// 部署信息：这个进程是哪次构建、在什么平台上跑、什么时候起来的。
//
// 这组用例钉三件事：有权限的人拿到的字段与构建期注入值**同源**（不是各算一份）、
// 没有权限的人真的被拒、以及"只读"这个标注在这条方法上确实生效。
//
// 它同时是 `aladdin version` 与页面同源的守门人：两边读的都是
// internal/buildinfo，谁哪天自己算了一份版本号，这里就会对不上。

// opsClient 构造走 Connect 协议的客户端。
func opsClient(t *testing.T, h harness, token, scope string) opsv1connect.OpsServiceClient {
	t.Helper()
	httpClient := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &headerTransport{
			base:  http.DefaultTransport,
			token: token,
			scope: scope,
		},
	}
	return opsv1connect.NewOpsServiceClient(httpClient, "http://"+h.address)
}

// 审计员读到的字段与 internal/buildinfo 报的**逐项一致**。
//
// 断言方式是有意的：拿 buildinfo.Read() 当期望值，而不是在用例里写死 "dev" 之类
// 的字面量。测试进程没有 -ldflags 注入，因此这样断言同时覆盖了两件事——服务端
// 确实读了那个包（不是自己拼了一个版本号），以及各字段没有被摆错位置（os 与 arch
// 互换这类错误在"两个都非空"的断言下是看不见的）。
//
// 本机构建的取值本身也是要钉住的：没有注入就该是 dev / 空提交号 / 空构建时间，
// 而不是编一个看起来像真的取值出来。
func TestDeploymentInfoReportsBuildAndProcess(t *testing.T) {
	h := startServer(t, rbac.RoleAuditor, testScope)
	client := opsClient(t, h, testToken, testScope)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resp, err := client.GetDeploymentInfo(ctx,
		connect.NewRequest(&opsv1.GetDeploymentInfoRequest{Scope: testScope}))
	if err != nil {
		t.Fatalf("读部署信息失败: %v", err)
	}
	got := resp.Msg
	want := buildinfo.Read()

	if got.GetVersion() != want.Version {
		t.Errorf("version = %q, want %q（部署信息必须与构建期注入同源）", got.GetVersion(), want.Version)
	}
	if got.GetCommit() != want.Commit {
		t.Errorf("commit = %q, want %q", got.GetCommit(), want.Commit)
	}
	if got.GetReleased() != want.Released {
		t.Errorf("released = %v, want %v", got.GetReleased(), want.Released)
	}
	if got.GetGoVersion() != runtime.Version() {
		t.Errorf("go_version = %q, want %q", got.GetGoVersion(), runtime.Version())
	}
	if got.GetOs() != runtime.GOOS || got.GetArch() != runtime.GOARCH {
		t.Errorf("平台 = %s/%s, want %s/%s", got.GetOs(), got.GetArch(), runtime.GOOS, runtime.GOARCH)
	}

	// 构建时间没有注入时必须是**空串**：给一个零值时间的时间戳（0001-01-01）会让
	// 界面只能靠模式匹配去猜它其实表示"没有"。
	if want.BuildTime.IsZero() && got.GetBuiltAt() != "" {
		t.Errorf("未注入构建时间时应为空串，实际 %q", got.GetBuiltAt())
	}

	// 启动时刻必须是可解析的、过去的、且离现在不远的时刻。三者缺一，
	// 页面上的 uptime 就会是一个要么负数、要么离谱的数。
	startedAt, err := time.Parse(time.RFC3339, got.GetStartedAt())
	if err != nil {
		t.Fatalf("started_at %q 不是 RFC3339: %v", got.GetStartedAt(), err)
	}
	if since := time.Since(startedAt); since < 0 || since > time.Hour {
		t.Errorf("started_at 距现在 %v，不像一个刚起来的测试进程", since)
	}

	// 实例标识取不到主机名时可以是空串，但那应当在真实环境里不发生——
	// 这一项正是多副本时"我看的是哪一个"的答案。
	if got.GetInstance() == "" {
		t.Error("instance 为空：拿不到主机名，多副本时这一项就没用了")
	}
}

// 没有 ops.deployment.read 的主体读不到部署信息。
//
// 两种协议各打一次。判定只有一处实现，但那处实现要能在多条协议上都被走到
// （见 AGENTS.md 的"改判定必须三种协议都对"）。
func TestDeploymentInfoDeniesWithoutPermission(t *testing.T) {
	h := startServer(t, rbac.RoleViewer, testScope)

	t.Run("Connect", func(t *testing.T) {
		client := opsClient(t, h, testToken, testScope)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, err := client.GetDeploymentInfo(ctx,
			connect.NewRequest(&opsv1.GetDeploymentInfoRequest{Scope: testScope}))
		if connect.CodeOf(err) != connect.CodePermissionDenied {
			t.Fatalf("错误码 = %v，期望 PermissionDenied", connect.CodeOf(err))
		}
	})

	t.Run("gRPC", func(t *testing.T) {
		c := h.dial(t, testToken, testScope)
		ctx, cancel := c.Context()
		defer cancel()
		_, err := opsv1.NewOpsServiceClient(c.Conn()).
			GetDeploymentInfo(ctx, &opsv1.GetDeploymentInfoRequest{Scope: testScope})
		if err == nil {
			t.Fatal("无权限主体竟然读到了部署信息")
		}
	})
}

// 这条方法标了 NO_SIDE_EFFECTS，因此**同一条路径**也接受 GET。
//
// 用裸 GET 而不是客户端：客户端会自己挑动词，证明不了服务端那道闸是开的
// （同 test/e2e/connect_get_test.go 的理由）。这条标注的是与不标的是刻意区分的
// ——ValidateDraft 与 PollDeviceLogin 同样只读却刻意没标，因此"是不是接受 GET"
// 只能逐方法钉，不能靠"只读方法都接受 GET"去推。
func TestDeploymentInfoAcceptsGet(t *testing.T) {
	h := startServer(t, rbac.RoleAuditor, testScope)

	// 作用域写在 message 里，URL 由 url.Values 拼——手工转义会引入与本用例无关的
	// 失败面（`%2F` 写错一次，看到的就是一个与标注无关的 400）。
	query := url.Values{}
	query.Set("encoding", "json")
	query.Set("message", fmt.Sprintf(`{"scope":%q}`, testScope))

	resp, err := rawGet(t, "http://"+h.address+
		"/aladdin.ops.v1.OpsService/GetDeploymentInfo?"+query.Encode(), testToken)
	if err != nil {
		t.Fatalf("裸 GET: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusMethodNotAllowed {
		t.Fatalf("状态码 = %d：只读方法应当接受 GET", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("状态码 = %d, want 200", resp.StatusCode)
	}
}
