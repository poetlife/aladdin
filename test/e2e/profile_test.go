//go:build e2e

package e2e

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	identityv1 "github.com/poetlife/aladdin/api/gen/aladdin/identity/v1"
	profilev1 "github.com/poetlife/aladdin/api/gen/aladdin/profile/v1"
	"github.com/poetlife/aladdin/api/gen/aladdin/profile/v1/profilev1connect"
	rbacv1 "github.com/poetlife/aladdin/api/gen/aladdin/rbac/v1"
	"github.com/poetlife/aladdin/internal/objectstore"
	"github.com/poetlife/aladdin/internal/profile"
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

// htmlBytes 是"能顶着 image/png 存进去"的那类字节：直传之后类型由上传方声明，
// 因此"声明一个白名单外的类型"就是这条路径上能钻的那个空子。
var htmlBytes = []byte("<html><body>这不是图片</body></html>")

// svgBytes 是一段 SVG：它是 XML，可以内嵌脚本，因此不在白名单里。
var svgBytes = []byte(`<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg"/>`)

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
	if _, err := uploadAvatar(t, h, profile, "image/png", pngBytes); err != nil {
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

// uploadAvatar 走一遍头像直传：签发 → 把字节写进假存储（这一步扮演浏览器）
// → 提交。
//
// **服务端在整条路径上不接触字节**（见 docs/design/objectstore/README.md），
// 因此这个夹具里的 Put 不是"帮服务端省一步"，而是替代了浏览器的那个角色。
func uploadAvatar(t *testing.T, h harness, client profilev1connect.ProfileServiceClient, contentType string, data []byte) (*profilev1.Profile, error) {
	t.Helper()
	ctx := context.Background()
	begin, err := client.BeginAvatarUpload(ctx, connect.NewRequest(&profilev1.BeginAvatarUploadRequest{
		ContentType: contentType,
		SizeBytes:   uint64(len(data)),
	}))
	if err != nil {
		return nil, err
	}
	upload := begin.Msg.GetUpload()
	h.objects.SimulateUpload(upload.GetKey(), data)
	commit, err := client.CommitAvatarUpload(ctx, connect.NewRequest(&profilev1.CommitAvatarUploadRequest{}))
	if err != nil {
		return nil, err
	}
	return commit.Msg.GetProfile(), nil
}

// 头像往返：签发 → 直传 → 提交之后拿到地址，删掉之后没有地址。
//
// 类型的判定也在这一条里：**不在白名单的声明被拒于签发那一步**，因为直传之后
// 服务端根本看不到字节。
func TestAvatarRoundTripAndDeclaredType(t *testing.T) {
	h := startServer(t, rbac.RoleViewer, testScope)
	client := connectProfile(t, h, testToken)
	ctx := context.Background()

	// 测试装配注入的是内存实现，因此这个部署**启用了**头像功能。
	got, err := client.GetProfile(ctx, connect.NewRequest(&profilev1.GetProfileRequest{}))
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

	// 不在白名单的声明被拒，且**不签发任何凭证**。
	if _, err := client.BeginAvatarUpload(ctx, connect.NewRequest(&profilev1.BeginAvatarUploadRequest{
		ContentType: "text/html", SizeBytes: uint64(len(htmlBytes)),
	})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("声明 text/html = %v，期望 InvalidArgument", err)
	}
	// SVG 同样不在白名单里：它是 XML，可以内嵌脚本。
	if _, err := client.BeginAvatarUpload(ctx, connect.NewRequest(&profilev1.BeginAvatarUploadRequest{
		ContentType: "image/svg+xml", SizeBytes: uint64(len(svgBytes)),
	})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("声明 image/svg+xml = %v，期望 InvalidArgument", err)
	}
	// 声明超过上限即拒于签发，用户因此不必把几十 MB 整个传上来。
	if _, err := client.BeginAvatarUpload(ctx, connect.NewRequest(&profilev1.BeginAvatarUploadRequest{
		ContentType: "image/png", SizeBytes: profile.AvatarMaxBytes + 1,
	})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("声明超限 = %v，期望 InvalidArgument", err)
	}

	// 被拒之后不留痕迹。
	afterReject, err := client.GetProfile(ctx, connect.NewRequest(&profilev1.GetProfileRequest{}))
	if err != nil {
		t.Fatalf("读取档案失败: %v", err)
	}
	if afterReject.Msg.GetProfile().GetAvatarUrl() != "" {
		t.Error("被拒的声明仍然产生了头像")
	}

	// 真实往返：签发拿到的凭证必须指向这个主体的头像键。
	begin, err := client.BeginAvatarUpload(ctx, connect.NewRequest(&profilev1.BeginAvatarUploadRequest{
		ContentType: "image/png", SizeBytes: uint64(len(pngBytes)),
	}))
	if err != nil {
		t.Fatalf("签发失败: %v", err)
	}
	upload := begin.Msg.GetUpload()
	if upload.GetKey() != profile.AvatarKey(testSubject) {
		t.Errorf("凭证的键 = %q，期望 %q", upload.GetKey(), profile.AvatarKey(testSubject))
	}
	if upload.GetBucket() == "" || upload.GetRegion() == "" ||
		upload.GetSecretId() == "" || upload.GetSecretKey() == "" || upload.GetSessionToken() == "" {
		t.Errorf("凭证不完整: %+v", upload)
	}
	if upload.GetExpiresAt() == "" {
		t.Error("凭证没有失效时刻")
	}

	h.objects.SimulateUpload(upload.GetKey(), pngBytes)
	committed, err := client.CommitAvatarUpload(ctx, connect.NewRequest(&profilev1.CommitAvatarUploadRequest{}))
	if err != nil {
		t.Fatalf("提交失败: %v", err)
	}
	if committed.Msg.GetProfile().GetAvatarUrl() == "" {
		t.Error("提交成功后没有拿到地址")
	}

	deleted, err := client.DeleteAvatar(ctx, connect.NewRequest(&profilev1.DeleteAvatarRequest{}))
	if err != nil {
		t.Fatalf("删除头像失败: %v", err)
	}
	if deleted.Msg.GetProfile().GetAvatarUrl() != "" {
		t.Error("删除之后仍有地址")
	}
}

// 提交时核对**真实**字节数：声明合法、实际超限的对象被拒，且被删掉。
//
// 存储侧会按声明的类型卡一次长度（那条策略由 internal/objectstore 构造），
// 这里验的是服务端自己的那一次核对——它是写进元数据的那个数字的来源。
func TestAvatarCommitRejectsOversizedObject(t *testing.T) {
	h := startServer(t, rbac.RoleViewer, testScope)
	client := connectProfile(t, h, testToken)
	ctx := context.Background()

	begin, err := client.BeginAvatarUpload(ctx, connect.NewRequest(&profilev1.BeginAvatarUploadRequest{
		ContentType: "image/png", SizeBytes: 1024,
	}))
	if err != nil {
		t.Fatalf("签发失败: %v", err)
	}
	key := begin.Msg.GetUpload().GetKey()
	h.objects.SimulateUpload(key, make([]byte, profile.AvatarMaxBytes+1))

	if _, err := client.CommitAvatarUpload(ctx, connect.NewRequest(&profilev1.CommitAvatarUploadRequest{})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("提交超限对象 = %v，期望 InvalidArgument", err)
	}
	if _, err := h.objects.Head(ctx, key); !errors.Is(err, objectstore.ErrObjectNotFound) {
		t.Error("超限的对象没有被删掉")
	}
}

// 上传没完成时提交报"可以重来"，而不是把它当成一次成功。
func TestAvatarCommitWithoutObjectIsRetryable(t *testing.T) {
	h := startServer(t, rbac.RoleViewer, testScope)
	client := connectProfile(t, h, testToken)
	ctx := context.Background()

	if _, err := client.BeginAvatarUpload(ctx, connect.NewRequest(&profilev1.BeginAvatarUploadRequest{
		ContentType: "image/png", SizeBytes: 1024,
	})); err != nil {
		t.Fatalf("签发失败: %v", err)
	}
	// 刻意不写对象：模拟上传中断。
	if _, err := client.CommitAvatarUpload(ctx, connect.NewRequest(&profilev1.CommitAvatarUploadRequest{})); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("提交一个不存在的对象 = %v，期望 FailedPrecondition", err)
	}
}

// **直传凭证不得进日志。**
//
// 它与令牌同级：日志会被采集、转发、长期保留，而一份还在有效期内的写入凭证
// 落在日志里，就等于把一次写入的能力留了下来。这条断言不看"日志里写了什么"，
// 而是看"有没有任何一条字段承载了凭证"——那是更直接、也更难绕过的判据。
func TestUploadCredentialNeverReachesLogs(t *testing.T) {
	h := startServer(t, rbac.RoleViewer, testScope)
	client := connectProfile(t, h, testToken)

	begin, err := client.BeginAvatarUpload(context.Background(), connect.NewRequest(&profilev1.BeginAvatarUploadRequest{
		ContentType: "image/png", SizeBytes: uint64(len(pngBytes)),
	}))
	if err != nil {
		t.Fatalf("签发失败: %v", err)
	}
	upload := begin.Msg.GetUpload()

	secrets := []string{upload.GetSecretKey(), upload.GetSessionToken()}
	for _, secret := range secrets {
		if secret == "" {
			t.Fatal("凭证里没有密钥，这条用例证明不了任何事")
		}
	}
	for _, entry := range h.logs.All() {
		for key, value := range entry.ContextMap() {
			text, ok := value.(string)
			if !ok {
				continue
			}
			for _, secret := range secrets {
				if strings.Contains(text, secret) {
					t.Errorf("日志字段 %q 承载了直传凭证", key)
				}
			}
		}
	}
}
