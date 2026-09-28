//go:build e2e

package e2e

import (
	"context"
	"testing"

	"connectrpc.com/connect"

	identityv1 "github.com/poetlife/aladdin/api/gen/aladdin/identity/v1"
	"github.com/poetlife/aladdin/internal/rbac"
)

// 本文件端到端验证命令行登录：终端拿到的会话属于**批准它的那个人**。
//
// 两条协议都跑。设备码登录的四个方法里两个公开、两个要认证，而拒绝响应由
// ErrorWriter 按协议写出——只测一种协议，另一种协议上的编码错误会被完全
// 漏掉（见 CLAUDE.md 的传输方式约定）。
//
// **不联网**：渠道校验器是注入的替身；设备码与短码都由本进程生成。

// startDeviceLoginServer 起一个既启用了渠道、又配置了对外地址的服务端。
//
// 对外地址是这条路径的前提：批准页地址由它构造，没有它这条路径整体缺席。
func startDeviceLoginServer(t *testing.T) harness {
	t.Helper()
	fake := newFakeGoogleToken("google-sub-a", "a@example.com")
	return startServerWith(t, rbac.RoleViewer, testScope,
		withChannels(googleChannel(fake)),
		withPublicBaseURL(e2ePublicBaseURL))
}

// 完整走一遍：匿名发起、批准者批准、匿名轮询拿到**批准者**的会话。
//
// 断言"交付的会话代表同一个主体"就是断言"没有登记新主体"：解析入口若被
// 走到，它会为这次登录分配一个新主体，交付的会话就不可能是批准者的那个。
func TestDeviceLoginDeliversApproversSessionOverGRPC(t *testing.T) {
	h := startDeviceLoginServer(t)

	// 浏览器侧：某个已经登录的主体。
	approver, approverCtx, _ := loginOverGRPC(t, h)
	browserWho, err := approver.WhoAmI(approverCtx, &identityv1.WhoAmIRequest{})
	if err != nil {
		t.Fatalf("读取批准者主体失败: %v", err)
	}

	// 终端侧：匿名。发起与轮询都是公开方法。
	anon, anonCtx := grpcIdentity(t, h, "")
	start, err := anon.StartDeviceLogin(anonCtx, &identityv1.StartDeviceLoginRequest{})
	if err != nil {
		t.Fatalf("发起设备码登录失败: %v", err)
	}
	if start.GetVerificationUri() != e2ePublicBaseURL+"/device" {
		t.Fatalf("批准页地址是 %q", start.GetVerificationUri())
	}

	// 还没批准时既没有凭证，也不该被当成一次失败。
	pending, err := anon.PollDeviceLogin(anonCtx, &identityv1.PollDeviceLoginRequest{
		DeviceCode: start.GetDeviceCode(),
	})
	if err != nil {
		t.Fatalf("轮询失败: %v", err)
	}
	if pending.GetState() != identityv1.DeviceLoginState_DEVICE_LOGIN_STATE_PENDING {
		t.Fatalf("未批准时应为待批准，实际 %v", pending.GetState())
	}
	if pending.GetAccessToken() != "" {
		t.Fatal("未批准却交出了凭证")
	}

	// 人在浏览器里批准——用的是自己的凭证，请求里没有主体字段。
	if _, err := approver.ApproveDeviceLogin(approverCtx, &identityv1.ApproveDeviceLoginRequest{
		UserCode: start.GetUserCode(),
	}); err != nil {
		t.Fatalf("批准失败: %v", err)
	}

	delivered, err := anon.PollDeviceLogin(anonCtx, &identityv1.PollDeviceLoginRequest{
		DeviceCode: start.GetDeviceCode(),
	})
	if err != nil {
		t.Fatalf("轮询失败: %v", err)
	}
	if delivered.GetState() != identityv1.DeviceLoginState_DEVICE_LOGIN_STATE_APPROVED {
		t.Fatalf("批准后应为已批准，实际 %v", delivered.GetState())
	}
	if delivered.GetAccessToken() == "" {
		t.Fatal("已批准却没有交出凭证")
	}

	// 交付的会话代表浏览器里那个主体。
	device, deviceCtx := grpcIdentity(t, h, delivered.GetAccessToken())
	deviceWho, err := device.WhoAmI(deviceCtx, &identityv1.WhoAmIRequest{})
	if err != nil {
		t.Fatalf("交付的会话不可用: %v", err)
	}
	if deviceWho.GetSubjectId() != browserWho.GetSubjectId() {
		t.Fatalf("交付的会话属于 %q，应为批准者 %q",
			deviceWho.GetSubjectId(), browserWho.GetSubjectId())
	}

	// 交付恰好一次。
	again, err := anon.PollDeviceLogin(anonCtx, &identityv1.PollDeviceLoginRequest{
		DeviceCode: start.GetDeviceCode(),
	})
	if err != nil {
		t.Fatalf("轮询失败: %v", err)
	}
	if again.GetAccessToken() != "" {
		t.Fatal("同一份设备码交付了两次")
	}
	if again.GetState() != identityv1.DeviceLoginState_DEVICE_LOGIN_STATE_EXPIRED {
		t.Fatalf("已交付的设备码应为过期态，实际 %v", again.GetState())
	}
}

// 同一件事走 Connect：结论必须与 gRPC 一致。
func TestDeviceLoginDeliversApproversSessionOverConnect(t *testing.T) {
	h := startDeviceLoginServer(t)
	ctx := context.Background()

	browserClient := connectIdentity(t, h, "")
	login, err := browserClient.Login(ctx, connect.NewRequest(&identityv1.LoginRequest{
		Credential: &identityv1.LoginRequest_Google{
			Google: &identityv1.GoogleCredential{IdToken: "一份身份令牌"},
		},
	}))
	if err != nil {
		t.Fatalf("浏览器侧登录失败: %v", err)
	}
	browserToken := login.Msg.GetAccessToken()

	browserWho, err := connectIdentity(t, h, browserToken).WhoAmI(ctx,
		connect.NewRequest(&identityv1.WhoAmIRequest{}))
	if err != nil {
		t.Fatalf("读取批准者主体失败: %v", err)
	}

	anon := connectIdentity(t, h, "")
	start, err := anon.StartDeviceLogin(ctx, connect.NewRequest(&identityv1.StartDeviceLoginRequest{}))
	if err != nil {
		t.Fatalf("发起设备码登录失败: %v", err)
	}

	if _, err := connectIdentity(t, h, browserToken).ApproveDeviceLogin(ctx,
		connect.NewRequest(&identityv1.ApproveDeviceLoginRequest{
			UserCode: start.Msg.GetUserCode(),
		})); err != nil {
		t.Fatalf("批准失败: %v", err)
	}

	delivered, err := anon.PollDeviceLogin(ctx, connect.NewRequest(&identityv1.PollDeviceLoginRequest{
		DeviceCode: start.Msg.GetDeviceCode(),
	}))
	if err != nil {
		t.Fatalf("轮询失败: %v", err)
	}
	if delivered.Msg.GetState() != identityv1.DeviceLoginState_DEVICE_LOGIN_STATE_APPROVED {
		t.Fatalf("批准后应为已批准，实际 %v", delivered.Msg.GetState())
	}

	deviceWho, err := connectIdentity(t, h, delivered.Msg.GetAccessToken()).WhoAmI(ctx,
		connect.NewRequest(&identityv1.WhoAmIRequest{}))
	if err != nil {
		t.Fatalf("交付的会话不可用: %v", err)
	}
	if deviceWho.Msg.GetSubjectId() != browserWho.Msg.GetSubjectId() {
		t.Fatalf("交付的会话属于 %q，应为批准者 %q",
			deviceWho.Msg.GetSubjectId(), browserWho.Msg.GetSubjectId())
	}
}

// 拒绝之后永远取不到凭证——两条协议各走完整的一遍。
func TestDeviceLoginDenyYieldsNoCredentialOverBothProtocols(t *testing.T) {
	h := startDeviceLoginServer(t)
	ctx := context.Background()

	t.Run("gRPC", func(t *testing.T) {
		approver, approverCtx, _ := loginOverGRPC(t, h)
		anon, anonCtx := grpcIdentity(t, h, "")
		start, err := anon.StartDeviceLogin(anonCtx, &identityv1.StartDeviceLoginRequest{})
		if err != nil {
			t.Fatalf("发起设备码登录失败: %v", err)
		}
		if _, err := approver.DenyDeviceLogin(approverCtx, &identityv1.DenyDeviceLoginRequest{
			UserCode: start.GetUserCode(),
		}); err != nil {
			t.Fatalf("拒绝失败: %v", err)
		}

		denied, err := anon.PollDeviceLogin(anonCtx, &identityv1.PollDeviceLoginRequest{
			DeviceCode: start.GetDeviceCode(),
		})
		if err != nil {
			t.Fatalf("轮询失败: %v", err)
		}
		if denied.GetState() != identityv1.DeviceLoginState_DEVICE_LOGIN_STATE_DENIED {
			t.Fatalf("拒绝后应为已拒绝，实际 %v", denied.GetState())
		}
		if denied.GetAccessToken() != "" {
			t.Fatal("被拒绝的登录交出了凭证")
		}
	})

	t.Run("Connect", func(t *testing.T) {
		login, err := connectIdentity(t, h, "").Login(ctx, connect.NewRequest(&identityv1.LoginRequest{
			Credential: &identityv1.LoginRequest_Google{
				Google: &identityv1.GoogleCredential{IdToken: "一份身份令牌"},
			},
		}))
		if err != nil {
			t.Fatalf("登录失败: %v", err)
		}
		approver := connectIdentity(t, h, login.Msg.GetAccessToken())
		anon := connectIdentity(t, h, "")

		start, err := anon.StartDeviceLogin(ctx, connect.NewRequest(&identityv1.StartDeviceLoginRequest{}))
		if err != nil {
			t.Fatalf("发起设备码登录失败: %v", err)
		}
		if _, err := approver.DenyDeviceLogin(ctx,
			connect.NewRequest(&identityv1.DenyDeviceLoginRequest{
				UserCode: start.Msg.GetUserCode(),
			})); err != nil {
			t.Fatalf("拒绝失败: %v", err)
		}

		denied, err := anon.PollDeviceLogin(ctx, connect.NewRequest(&identityv1.PollDeviceLoginRequest{
			DeviceCode: start.Msg.GetDeviceCode(),
		}))
		if err != nil {
			t.Fatalf("轮询失败: %v", err)
		}
		if denied.Msg.GetState() != identityv1.DeviceLoginState_DEVICE_LOGIN_STATE_DENIED {
			t.Fatalf("拒绝后应为已拒绝，实际 %v", denied.Msg.GetState())
		}
		if denied.Msg.GetAccessToken() != "" {
			t.Fatal("被拒绝的登录交出了凭证")
		}
	})
}

// 批准必须已认证：请求里没有任何主体字段，"以谁的名义"只能来自凭证。
func TestDeviceLoginApprovalRequiresAuthenticationOverBothProtocols(t *testing.T) {
	h := startDeviceLoginServer(t)

	anon, anonCtx := grpcIdentity(t, h, "")
	start, err := anon.StartDeviceLogin(anonCtx, &identityv1.StartDeviceLoginRequest{})
	if err != nil {
		t.Fatalf("发起设备码登录失败: %v", err)
	}

	// gRPC：匿名批准 → 未认证（而不是"短码无效"，那是两类结论）。
	if _, err := anon.ApproveDeviceLogin(anonCtx, &identityv1.ApproveDeviceLoginRequest{
		UserCode: start.GetUserCode(),
	}); rpcCode(err) != codeUnauthenticated {
		t.Fatalf("匿名批准应返回未认证，实际 %v", err)
	}

	// Connect：同一个结论。
	_, err = connectIdentity(t, h, "").ApproveDeviceLogin(context.Background(),
		connect.NewRequest(&identityv1.ApproveDeviceLoginRequest{
			UserCode: start.GetUserCode(),
		}))
	if rpcCode(err) != codeUnauthenticated {
		t.Fatalf("匿名批准应返回未认证，实际 %v", err)
	}

	// 拒绝同样要求已认证：不要求的话，"猜一个短码把它拒掉"就成了谁都能做的
	// 一次打断。
	if _, err := anon.DenyDeviceLogin(anonCtx, &identityv1.DenyDeviceLoginRequest{
		UserCode: start.GetUserCode(),
	}); rpcCode(err) != codeUnauthenticated {
		t.Fatalf("匿名拒绝应返回未认证，实际 %v", err)
	}

	// 这几次失败都不该让这次登录作废：真正的批准者仍然能用。
	approver, approverCtx, _ := loginOverGRPC(t, h)
	if _, err := approver.ApproveDeviceLogin(approverCtx, &identityv1.ApproveDeviceLoginRequest{
		UserCode: start.GetUserCode(),
	}); err != nil {
		t.Fatalf("批准者应当仍能批准: %v", err)
	}
}

// 未配置对外地址时这条路径整体缺席：说"这条路没开"，不说"设备码无效"。
func TestDeviceLoginAbsentWithoutPublicBaseURL(t *testing.T) {
	h := startServer(t, rbac.RoleViewer, testScope)

	methods, err := connectIdentity(t, h, "").GetAuthMethods(context.Background(),
		connect.NewRequest(&identityv1.GetAuthMethodsRequest{}))
	if err != nil {
		t.Fatalf("查询登录方式失败: %v", err)
	}
	if methods.Msg.GetDeviceLoginEnabled() {
		t.Fatal("未配置对外地址时不该宣称命令行登录可用")
	}

	_, err = connectIdentity(t, h, "").StartDeviceLogin(context.Background(),
		connect.NewRequest(&identityv1.StartDeviceLoginRequest{}))
	if rpcCode(err) != codeUnimplemented {
		t.Fatalf("未启用时应返回未实现，实际 %v", err)
	}
}
