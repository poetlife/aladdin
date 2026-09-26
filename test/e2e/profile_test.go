//go:build e2e

package e2e

import (
	"context"
	"net/http"
	"slices"
	"testing"
	"time"

	"connectrpc.com/connect"

	identityv1 "github.com/poetlife/aladdin/api/gen/aladdin/identity/v1"
	profilev1 "github.com/poetlife/aladdin/api/gen/aladdin/profile/v1"
	"github.com/poetlife/aladdin/api/gen/aladdin/profile/v1/profilev1connect"
	rbacv1 "github.com/poetlife/aladdin/api/gen/aladdin/rbac/v1"
	"github.com/poetlife/aladdin/internal/rbac"
)

// 本文件端到端验证档案链路：归属只取凭证、自服务不要权限码、展示名的回退
// 只在服务端发生、以及档案**不参与任何判定**。
//
// 与单元测试的分工：internal/profile 的用例验证规则本身，这里验证规则在真实
// 传输与真实持久化上的**接入**——包括零权限主体能不能用、改完之后权限集合
// 会不会变这类只有整条链路才能回答的问题。

// pngBytes 是一段能被服务端嗅探成 image/png 的字节。
//
// 用真的签名而不是"随便几个字节"：这一层要挡的正是"看起来像图片、其实不是"。
var pngBytes = append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 64)...)

// htmlBytes 是"能顶着 image/png 存进去"的那类字节。
var htmlBytes = []byte("<html><body>这不是图片</body></html>")

// connectProfile 构造一个走 Connect 协议的档案面客户端。
func connectProfile(t *testing.T, h harness, token string) profilev1connect.ProfileServiceClient {
	t.Helper()
	httpClient := &http.Client{
		Timeout:   5 * time.Second,
		Transport: &headerTransport{base: http.DefaultTransport, token: token},
	}
	return profilev1connect.NewProfileServiceClient(httpClient, "http://"+h.address)
}

// loginAsProfileUser 用给定的渠道身份登一次，返回主体标识与一个档案客户端。
//
// 每次都用 `set` 换一份渠道身份：不同的渠道身份应当解析成不同的主体，
// 而"同一个人两次登录是同一个主体"由（来源，身份标识）的唯一归属保证。
func loginAsProfileUser(t *testing.T, h harness, fake *fakeGoogleToken, subject, email string) (string, profilev1connect.ProfileServiceClient) {
	t.Helper()
	fake.set(subject, email)
	client, ctx, token := loginOverGRPC(t, h)
	who, err := client.WhoAmI(ctx, &identityv1.WhoAmIRequest{})
	if err != nil {
		t.Fatalf("WhoAmI 失败: %v", err)
	}
	return who.GetSubjectId(), connectProfile(t, h, token)
}

// 未认证不得读写任何档案：这是"未认证"与"无权限"分离在档案面上的落点。
func TestProfileRequiresAuthentication(t *testing.T) {
	h := startServer(t, rbac.RoleViewer, testScope)
	client := connectProfile(t, h, "")
	ctx := context.Background()

	_, err := client.GetProfile(ctx, connect.NewRequest(&profilev1.GetProfileRequest{}))
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Errorf("匿名读取 = %v，期望 Unauthenticated", err)
	}
	_, err = client.UpdateProfile(ctx, connect.NewRequest(&profilev1.UpdateProfileRequest{Nickname: "阿拉丁"}))
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Errorf("匿名写入 = %v，期望 Unauthenticated", err)
	}
}

// **一个权限都没有的主体也能设自己的档案。**
//
// 这是本模块存在的理由：首次登录的主体拿到会话是成功的，只是什么都做不了；
// 而"给自己起个名字"恰恰是他唯一能做、也最需要做的事。它同时固定了准入
// 门槛是"已认证"而不是某个权限码。
func TestProfileIsSelfServiceForZeroPermissionSubject(t *testing.T) {
	_, h := startIdentityServer(t)
	_, ctx, token := loginOverGRPC(t, h)
	profile := connectProfile(t, h, token)

	// 先确认这个人确实一个权限都没有：受权限控制的方法返回"无权限"。
	// 少了这一步，下面的成功可能只是因为恰好有权限，用例就什么都没证明。
	_, err := connectClient(t, h, token, testScope).ListRoles(ctx, connect.NewRequest(&rbacv1.ListRolesRequest{Scope: testScope}))
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("零权限主体调用受控方法 = %v，期望 PermissionDenied", err)
	}

	if _, err := profile.UpdateProfile(ctx, connect.NewRequest(&profilev1.UpdateProfileRequest{
		Nickname: "阿拉丁", Bio: "一盏灯",
	})); err != nil {
		t.Fatalf("零权限主体改自己的档案失败了: %v", err)
	}

	got, err := profile.GetProfile(ctx, connect.NewRequest(&profilev1.GetProfileRequest{}))
	if err != nil {
		t.Fatalf("读取档案失败: %v", err)
	}
	if got.Msg.GetProfile().GetNickname() != "阿拉丁" {
		t.Errorf("昵称 = %q，期望 %q", got.Msg.GetProfile().GetNickname(), "阿拉丁")
	}
}

// 展示名的回退规则在服务端：昵称 → 渠道可读标识 → 主体标识。
//
// 浏览器拿到的是算好的那一个，因此这里断言的是"服务端下发什么"，而不是
// "前端拼出了什么"。
func TestProfileDisplayNameFallback(t *testing.T) {
	_, h := startIdentityServer(t)
	_, ctx, token := loginOverGRPC(t, h)
	profile := connectProfile(t, h, token)

	// 没设昵称：回退到渠道给出的可读标识（Google 是邮箱）。
	got, err := profile.GetProfile(ctx, connect.NewRequest(&profilev1.GetProfileRequest{}))
	if err != nil {
		t.Fatalf("读取档案失败: %v", err)
	}
	if got.Msg.GetProfile().GetDisplayName() != "a@example.com" {
		t.Errorf("DisplayName = %q，期望回退到渠道标识", got.Msg.GetProfile().GetDisplayName())
	}

	// 设了昵称：用昵称。
	updated, err := profile.UpdateProfile(ctx, connect.NewRequest(&profilev1.UpdateProfileRequest{Nickname: "阿拉丁"}))
	if err != nil {
		t.Fatalf("写入档案失败: %v", err)
	}
	if updated.Msg.GetProfile().GetDisplayName() != "阿拉丁" {
		t.Errorf("DisplayName = %q，期望昵称", updated.Msg.GetProfile().GetDisplayName())
	}

	// 清空昵称：回到回退序列的下一条。
	cleared, err := profile.UpdateProfile(ctx, connect.NewRequest(&profilev1.UpdateProfileRequest{Nickname: ""}))
	if err != nil {
		t.Fatalf("清空昵称失败: %v", err)
	}
	if cleared.Msg.GetProfile().GetDisplayName() != "a@example.com" {
		t.Errorf("清空后 DisplayName = %q，期望回到渠道标识", cleared.Msg.GetProfile().GetDisplayName())
	}
}

// **改档案不改变任何权限。**
//
// 档案是展示信息，一旦它进了判定路径，"把昵称改成一个字"就可能变成一次提权。
func TestProfileChangeDoesNotAffectPermissions(t *testing.T) {
	h := startServer(t, rbac.RoleViewer, testScope)
	identity := connectIdentity(t, h, testToken)
	profile := connectProfile(t, h, testToken)
	ctx := context.Background()

	before, err := identity.GetSessionPermissions(ctx, connect.NewRequest(&identityv1.GetSessionPermissionsRequest{Scope: testScope}))
	if err != nil {
		t.Fatalf("读取权限失败: %v", err)
	}
	// 权限集合为空时这条用例什么都没验证到，必须先确认它有内容。
	if len(before.Msg.GetPermissions()) == 0 {
		t.Fatal("测试主体本来就没有权限，这条用例证明不了任何事")
	}

	if _, err := profile.UpdateProfile(ctx, connect.NewRequest(&profilev1.UpdateProfileRequest{
		Nickname: "阿拉丁", Bio: "一盏灯",
	})); err != nil {
		t.Fatalf("写入档案失败: %v", err)
	}
	if _, err := profile.UpdateAvatar(ctx, connect.NewRequest(&profilev1.UpdateAvatarRequest{Image: pngBytes})); err != nil {
		t.Fatalf("上传头像失败: %v", err)
	}

	after, err := identity.GetSessionPermissions(ctx, connect.NewRequest(&identityv1.GetSessionPermissionsRequest{Scope: testScope}))
	if err != nil {
		t.Fatalf("再次读取权限失败: %v", err)
	}

	// 排序后再比：权限集合的**内容**才是契约，返回顺序不是。
	got, want := slices.Clone(before.Msg.GetPermissions()), slices.Clone(after.Msg.GetPermissions())
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("改档案前后的权限集合不同：%v → %v", got, want)
	}
}

// **昵称不是身份。** 两个主体可以同名，且此后各自的登录仍解析到各自的主体。
//
// 这条是"昵称没有唯一约束、也不能被按昵称查询"在整条链路上的落点：一旦
// 昵称成了键，"按昵称查人"就会成为一个可实现的需求，而那是邮箱那条路
// 已经踩过的坑。
func TestNicknameIsNotAnIdentityPath(t *testing.T) {
	fake, h := startIdentityServer(t)

	firstSubject, firstProfile := loginAsProfileUser(t, h, fake, "google-sub-a", "a@example.com")
	if _, err := firstProfile.UpdateProfile(context.Background(), connect.NewRequest(&profilev1.UpdateProfileRequest{
		Nickname: "同名",
	})); err != nil {
		t.Fatalf("第一个人设昵称失败: %v", err)
	}

	secondSubject, secondProfile := loginAsProfileUser(t, h, fake, "google-sub-b", "b@example.com")
	if secondSubject == firstSubject {
		t.Fatal("两个不同的渠道身份解析到了同一个主体")
	}
	// 同名必须能设成功——唯一约束会把它变成一条查找键。
	if _, err := secondProfile.UpdateProfile(context.Background(), connect.NewRequest(&profilev1.UpdateProfileRequest{
		Nickname: "同名",
	})); err != nil {
		t.Fatalf("第二个人用同一昵称失败: %v", err)
	}

	// 用昵称对应的渠道身份再登一次，仍然各自回到各自的主体。
	againFirst, _ := loginAsProfileUser(t, h, fake, "google-sub-a", "a@example.com")
	againSecond, _ := loginAsProfileUser(t, h, fake, "google-sub-b", "b@example.com")
	if againFirst != firstSubject || againSecond != secondSubject {
		t.Errorf("重新登录后主体变了：%q/%q，期望 %q/%q",
			againFirst, againSecond, firstSubject, secondSubject)
	}
}

// 头像往返：上传之后拿到地址，删掉之后没有地址。非图片一律被拒。
func TestAvatarRoundTripAndTypeRejection(t *testing.T) {
	h := startServer(t, rbac.RoleViewer, testScope)
	profile := connectProfile(t, h, testToken)
	ctx := context.Background()

	// 测试装配注入的是内存实现，因此这个部署**启用了**头像功能。
	got, err := profile.GetProfile(ctx, connect.NewRequest(&profilev1.GetProfileRequest{}))
	if err != nil {
		t.Fatalf("读取档案失败: %v", err)
	}
	if !got.Msg.GetProfile().GetAvatarUploadEnabled() {
		t.Fatal("这个装配应当启用头像")
	}
	if got.Msg.GetProfile().GetAvatarMaxBytes() == 0 {
		t.Error("上限没有下发：客户端于是只能自己写一份，而那一份必然漂移")
	}
	if got.Msg.GetProfile().GetAvatarUrl() != "" {
		t.Error("还没上传就已经有头像地址了")
	}

	// 非图片被拒。请求里没有"声明类型"这个字段，因此这里能钻的空子只有
	// "字节本身不是图片"这一种。
	if _, err := profile.UpdateAvatar(ctx, connect.NewRequest(&profilev1.UpdateAvatarRequest{Image: htmlBytes})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("上传 HTML = %v，期望 InvalidArgument", err)
	}
	// 被拒之后不留痕迹。
	afterReject, err := profile.GetProfile(ctx, connect.NewRequest(&profilev1.GetProfileRequest{}))
	if err != nil {
		t.Fatalf("读取档案失败: %v", err)
	}
	if afterReject.Msg.GetProfile().GetAvatarUrl() != "" {
		t.Error("被拒的字节仍然产生了头像")
	}

	// 真实图片往返。
	uploaded, err := profile.UpdateAvatar(ctx, connect.NewRequest(&profilev1.UpdateAvatarRequest{Image: pngBytes}))
	if err != nil {
		t.Fatalf("上传 PNG 失败: %v", err)
	}
	if uploaded.Msg.GetProfile().GetAvatarUrl() == "" {
		t.Error("上传成功后没有拿到地址")
	}

	deleted, err := profile.DeleteAvatar(ctx, connect.NewRequest(&profilev1.DeleteAvatarRequest{}))
	if err != nil {
		t.Fatalf("删除头像失败: %v", err)
	}
	if deleted.Msg.GetProfile().GetAvatarUrl() != "" {
		t.Error("删除之后仍有地址")
	}
}

// 超过**请求体读上限**时给出一个明确的错误。
//
// 头像上传是本服务端唯一一个由客户端决定请求体大小的入口，而 connect 的默认
// 是**不限制大小**。没有这条上限，一个几百 MB 的请求会在被档案层拒绝之前
// 就已经整份读进内存了（见 internal/server 的 profileReadMaxBytes）。
//
// 它与"超过头像字节上限"是两回事：后者由档案层判定（见上一条用例），
// 这一条发生在更早的传输层。
func TestAvatarUploadBeyondReadLimitIsRejected(t *testing.T) {
	h := startServer(t, rbac.RoleViewer, testScope)
	profile := connectProfile(t, h, testToken)

	// 明显超过读上限（领域上限的两倍）的那一档。
	huge := make([]byte, 8*1024*1024)
	copy(huge, pngBytes)

	_, err := profile.UpdateAvatar(context.Background(), connect.NewRequest(&profilev1.UpdateAvatarRequest{Image: huge}))
	if connect.CodeOf(err) != connect.CodeResourceExhausted {
		t.Errorf("超大请求体 = %v，期望 ResourceExhausted", err)
	}
}

// 头像字节**不得进日志**。
//
// 它与令牌同级：日志会被采集、转发、长期保留。这条断言不看"日志里写了什么"，
// 而是看"有没有任何一条字段承载了二进制内容"——那是更直接、也更难绕过的判据。
func TestAvatarBytesNeverReachLogs(t *testing.T) {
	h := startServer(t, rbac.RoleViewer, testScope)
	profile := connectProfile(t, h, testToken)

	if _, err := profile.UpdateAvatar(context.Background(), connect.NewRequest(&profilev1.UpdateAvatarRequest{Image: pngBytes})); err != nil {
		t.Fatalf("上传头像失败: %v", err)
	}

	for _, entry := range h.logs.All() {
		for key, value := range entry.ContextMap() {
			if _, isBinary := value.([]byte); isBinary {
				t.Errorf("日志字段 %q 承载了二进制内容", key)
			}
		}
	}
}
