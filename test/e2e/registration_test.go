//go:build e2e

package e2e

import (
	"context"
	"net/http"
	"testing"
	"time"

	"connectrpc.com/connect"

	identityv1 "github.com/poetlife/aladdin/api/gen/aladdin/identity/v1"
	"github.com/poetlife/aladdin/api/gen/aladdin/identity/v1/identityv1connect"
	rbacv1 "github.com/poetlife/aladdin/api/gen/aladdin/rbac/v1"
	"github.com/poetlife/aladdin/api/gen/aladdin/rbac/v1/rbacv1connect"
	"github.com/poetlife/aladdin/internal/identity"
	"github.com/poetlife/aladdin/internal/rbac"
)

// 本文件端到端验证注册面：谁进得来、进来拿什么。
//
// 它跑两条协议（gRPC 与 Connect），与其它端到端用例同一条理由：结论必须与协议
// 无关。它同时走**真实的持久化存储**（见 startServerWith），因此"邀请码的扣减
// 真的落盘了吗""表建出来了吗"在这条链路上是被验证的，而不是被夹具挡住的。
//
// **不联网**：渠道校验器是一个可控的替身。

// registrationCookie 是承载"已校验、等待邀请码"的渠道身份的 cookie 名。
//
// 与导航凭据、待绑定凭据同理，它是**浏览器侧的契约**：兑换 RPC 只认这个 cookie，
// 改名字会让所有停在填码页上的流程一起断掉。服务端那套派生是包内实现细节，
// 外部测试拿不到，因此在这里按字面量固定。
const registrationCookie = "aladdin_registration"

// registrationPath 是填码页在前端的路径。它同样是对外契约的一部分：服务端在回调
// 里把浏览器送到这里，前端要用同一个路径注册路由。
const registrationPath = "/register"

// registrationConnectClient 构造一个走 Connect 协议的注册面客户端。
func registrationConnectClient(t *testing.T, h harness, token, scope string) identityv1connect.RegistrationServiceClient {
	t.Helper()
	httpClient := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &headerTransport{
			base:  http.DefaultTransport,
			token: token,
			scope: scope,
		},
	}
	return identityv1connect.NewRegistrationServiceClient(httpClient, httpBase(h))
}

// registrationGRPCClient 构造一个走 gRPC 协议的注册面客户端。
func registrationGRPCClient(t *testing.T, h harness, token, scope string) identityv1.RegistrationServiceClient {
	t.Helper()
	c := h.dial(t, token, scope)
	t.Cleanup(func() { _ = c.Conn().Close() })
	return identityv1.NewRegistrationServiceClient(c.Conn())
}

// 同一个请求走两条协议，结论必须完全一致。
//
// 如果这条失败了，说明注册面的判断跑到了协议层之上或之下，而不是在协议无关的
// 同一段实现里——那正是"三端共享一份业务实现"要避免的分叉。
func TestRegistrationPolicyOverBothProtocols(t *testing.T) {
	h := startServer(t, rbac.RoleSystemAdmin, testScope)
	ctx := context.Background()

	cases := []struct {
		name   string
		mode   identityv1.RegistrationMode
		roleID string
		want   int
	}{
		{"姿态必须显式给出", identityv1.RegistrationMode_REGISTRATION_MODE_UNSPECIFIED, "", codeInvalidArgument},
		{"默认角色不能自我放大", identityv1.RegistrationMode_REGISTRATION_MODE_OPEN, "system.admin", codeInvalidArgument},
		{"合法的一组", identityv1.RegistrationMode_REGISTRATION_MODE_INVITE, "viewer", codeOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Connect 路径
			_, connectErr := registrationConnectClient(t, h, testToken, testScope).
				PutRegistrationPolicy(ctx, connect.NewRequest(&identityv1.PutRegistrationPolicyRequest{
					Scope:         testScope,
					Mode:          tc.mode,
					DefaultRoleId: tc.roleID,
				}))

			// gRPC 路径
			_, grpcErr := registrationGRPCClient(t, h, testToken, testScope).
				PutRegistrationPolicy(ctx, &identityv1.PutRegistrationPolicyRequest{
					Scope:         testScope,
					Mode:          tc.mode,
					DefaultRoleId: tc.roleID,
				})

			if rpcCode(connectErr) != tc.want || rpcCode(grpcErr) != tc.want {
				t.Fatalf("结论不是 %d：connect=%d grpc=%d (connectErr=%v, grpcErr=%v)",
					tc.want, rpcCode(connectErr), rpcCode(grpcErr), connectErr, grpcErr)
			}
		})
	}

	// 读回来的是**刚刚写下去的那一份**：写与读共用同一条存储。
	got, err := registrationConnectClient(t, h, testToken, testScope).
		GetRegistrationPolicy(ctx, connect.NewRequest(&identityv1.GetRegistrationPolicyRequest{Scope: testScope}))
	if err != nil {
		t.Fatalf("读取注册策略失败: %v", err)
	}
	if got.Msg.GetPolicy().GetMode() != identityv1.RegistrationMode_REGISTRATION_MODE_INVITE {
		t.Fatalf("姿态 = %v，期望 invite", got.Msg.GetPolicy().GetMode())
	}
	if got.Msg.GetPolicy().GetDefaultRoleId() != "viewer" {
		t.Fatalf("默认角色 = %q，期望 viewer", got.Msg.GetPolicy().GetDefaultRoleId())
	}
}

// 走完一条完整的邀请码注册：回调停在填码页 → 填码 → 新账号拿到默认角色。
//
// 这条用例把闸门的两侧接了起来，而两侧单独看都是"正常的"：真正的风险在中间——
// 比如回调顺手把主体登记了、或者兑换那条路径忘了授默认角色。
func TestInviteRegistrationEndToEnd(t *testing.T) {
	verifier := newFakeGoogleToken("google-sub-invite", "invite@example.com")
	h := startServerWith(t, rbac.RoleSystemAdmin, testScope,
		withChannels(identity.Channel{
			Source: identity.SourceGoogle, ClientID: "google-client.example", Verifier: verifier,
		}),
		withPublicBaseURL(e2ePublicBaseURL),
	)
	ctx := context.Background()
	admin := registrationConnectClient(t, h, testToken, testScope)

	// 1. 准入姿态：需要邀请码；新账号默认给只读用户，绑定在全局范围上
	//    （全局是模型的根，不需要登记）。
	if _, err := admin.PutRegistrationPolicy(ctx, connect.NewRequest(&identityv1.PutRegistrationPolicyRequest{
		Scope:         testScope,
		Mode:          identityv1.RegistrationMode_REGISTRATION_MODE_INVITE,
		DefaultRoleId: "viewer",
	})); err != nil {
		t.Fatalf("设置注册策略失败: %v", err)
	}

	// 2. 签发一份一次性邀请码。**明文只在这一个返回值里出现一次。**
	created, err := admin.CreateInvite(ctx, connect.NewRequest(&identityv1.CreateInviteRequest{
		Scope:   testScope,
		Label:   "端到端",
		MaxUses: 1,
	}))
	if err != nil {
		t.Fatalf("签发邀请码失败: %v", err)
	}
	code := created.Msg.GetCode()
	if code == "" {
		t.Fatal("签发没有返回明文")
	}

	// 列表读不回明文——这是"服务端只存摘要"在端到端层面的体现。
	listed, err := admin.ListInvites(ctx, connect.NewRequest(&identityv1.ListInvitesRequest{Scope: testScope}))
	if err != nil {
		t.Fatalf("列出邀请码失败: %v", err)
	}
	if len(listed.Msg.GetInvites()) != 1 {
		t.Fatalf("应当有一条邀请码，实际 %d 条", len(listed.Msg.GetInvites()))
	}

	// 3. 走一次渠道登录：回调应当把浏览器送到填码页，并写下待注册凭据。
	client := noFollowClient()
	state := startRedirect(t, client, httpBase(h), googleRedirectCase(), "")
	result := redirectCallback(t, client, httpBase(h), googleRedirectCase(), state, "the-code", state.Value)
	if result.location != e2ePublicBaseURL+registrationPath {
		t.Fatalf("跳转目标 = %q，期望填码页", result.location)
	}
	staged := cookieNamed(result.cookies, registrationCookie)
	if staged == nil {
		t.Fatal("回调没有写下待注册凭据")
	}

	// 这一刻**没有交付任何会话**：停在这一步的人还没注册，回调不该顺手把他登进去。
	if fragmentParam(t, result.location, "token") != "" {
		t.Fatalf("等邀请码这一步不该交付会话: %q", result.location)
	}

	// 4. 用那份凭据 + 邀请码完成注册。请求里**只有码**：要登记哪个渠道身份，
	//    只由 cookie 里那份凭据决定。
	done, err := completeRegistrationConnect(t, h, staged, code)
	if err != nil {
		t.Fatalf("完成注册失败: %v", err)
	}
	token := done.GetAccessToken()
	if token == "" {
		t.Fatal("完成注册没有交回会话")
	}

	// 5. 新账号确实拿到了默认角色——一条真实的绑定，不是一句声明。
	subjectID := whoAmISubject(t, h, token)
	bindings := subjectBindings(t, h, token, subjectID)
	if len(bindings) != 1 || bindings[0].GetRoleId() != "viewer" {
		t.Fatalf("新账号应当恰好持有 viewer，实际 %+v", bindings)
	}

	// 6. 凭据与码都是一次性的：同一份 cookie 再来一次换不出第二个账号。
	if _, err := completeRegistrationConnect(t, h, staged, code); rpcCode(err) != codeFailedPrecondition {
		t.Fatalf("同一份凭据不该能兑换第二次，实际 %v", err)
	}
}

// 站点不接受新账号时，回调把浏览器送回前端并附一个专用标记；而**已经在册的
// 账号照常登录**。
//
// 后半句比前半句更要紧：把老用户一起关在门外，是一次准入收紧变成一次全站事故。
func TestClosedRegistrationRefusesNewButKeepsExisting(t *testing.T) {
	verifier := newFakeGoogleToken("google-sub-existing", "existing@example.com")
	h := startServerWith(t, rbac.RoleSystemAdmin, testScope,
		withChannels(identity.Channel{
			Source: identity.SourceGoogle, ClientID: "google-client.example", Verifier: verifier,
		}),
		withPublicBaseURL(e2ePublicBaseURL),
	)
	ctx := context.Background()
	admin := registrationConnectClient(t, h, testToken, testScope)

	setMode := func(mode identityv1.RegistrationMode) {
		t.Helper()
		if _, err := admin.PutRegistrationPolicy(ctx, connect.NewRequest(&identityv1.PutRegistrationPolicyRequest{
			Scope: testScope, Mode: mode,
		})); err != nil {
			t.Fatalf("设置注册策略失败: %v", err)
		}
	}

	// 先在开放注册下进来一个人。
	setMode(identityv1.RegistrationMode_REGISTRATION_MODE_OPEN)
	client := noFollowClient()
	verifier.set("google-sub-existing", "existing@example.com")
	first := redirectLoginToken(t, client, httpBase(h), googleRedirectCase())
	firstSubject := whoAmISubject(t, h, first)

	// 关掉注册，再让同一个人登录一次。
	setMode(identityv1.RegistrationMode_REGISTRATION_MODE_CLOSED)
	again := redirectLoginToken(t, client, httpBase(h), googleRedirectCase())
	if got := whoAmISubject(t, h, again); got != firstSubject {
		t.Fatalf("在册账号应当照常登录并落到同一个主体：%q → %q", firstSubject, got)
	}

	// 一个新身份则被拦下，且库里不会多出任何主体。
	verifier.set("google-sub-newcomer", "newcomer@example.com")
	state := startRedirect(t, client, httpBase(h), googleRedirectCase(), "")
	result := redirectCallback(t, client, httpBase(h), googleRedirectCase(), state, "the-code", state.Value)
	if got := fragmentParam(t, result.location, "error"); got != "google_registration_closed" {
		t.Fatalf("失败标记 = %q，期望 google_registration_closed", got)
	}
}

// completeRegistrationConnect 带着一份待注册凭据调用"完成注册"。
func completeRegistrationConnect(t *testing.T, h harness, cookie *http.Cookie, code string) (*identityv1.CompleteRegistrationResponse, error) {
	t.Helper()
	req := connect.NewRequest(&identityv1.CompleteRegistrationRequest{InviteCode: code})
	req.Header().Set("Cookie", cookie.String())
	resp, err := registrationConnectClient(t, h, "", "").CompleteRegistration(context.Background(), req)
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

// subjectBindings 读回一个主体的全部绑定。
func subjectBindings(t *testing.T, h harness, token, subjectID string) []*rbacv1.RoleBinding {
	t.Helper()
	client := rbacv1connect.NewRBACServiceClient(&http.Client{
		Timeout: 5 * time.Second,
		Transport: &headerTransport{
			base:  http.DefaultTransport,
			token: token,
		},
	}, httpBase(h))
	resp, err := client.ListSubjectBindings(context.Background(),
		connect.NewRequest(&rbacv1.ListSubjectBindingsRequest{SubjectId: subjectID}))
	if err != nil {
		t.Fatalf("读取绑定失败: %v", err)
	}
	return resp.Msg.GetBindings()
}

// cookieNamed 从一次响应写下的 cookie 里按名字取一个。
func cookieNamed(cookies []*http.Cookie, name string) *http.Cookie {
	for _, cookie := range cookies {
		if cookie.Name == name {
			return cookie
		}
	}
	return nil
}
