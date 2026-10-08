package server

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"

	identityv1 "github.com/poetlife/aladdin/api/gen/aladdin/identity/v1"
	"github.com/poetlife/aladdin/internal/identity"
	"github.com/poetlife/aladdin/internal/rbac"
	"github.com/poetlife/aladdin/internal/server/interceptor"
)

// 认证面的行为用例：登录如何解析主体、绑定如何归属、错误如何分类。
//
// 它们直接调用 handler，因此冻结的是**分类与归属**；跨协议的同一性
// 由 test/e2e 覆盖。
//
// **登录与绑定都走渠道重定向那条形状**：所有渠道都是重定向型，服务端没有
// "客户端把渠道凭证交进来"的接口（见 identity_service.go 的 Login）。因此
// 这里的助手按回调的两段走——校验凭证，然后解析主体并签发（或记成待绑定
// 凭据再用当前会话兑换）——它们与服务端在回调里走的完全是同两段代码。

// fakeVerifier 是一个可控的渠道凭证校验器：整套用例不联网、不碰 Google。
type fakeVerifier struct {
	identity identity.VerifiedIdentity
	err      error
}

func (f fakeVerifier) Verify(context.Context, string) (identity.VerifiedIdentity, error) {
	return f.identity, f.err
}

// googleChannel 把一份校验器登记成 Google 渠道。
//
// 只关心"一条渠道"的用例用它，使渠道装配这件事不必在每个用例里重复一遍。
// 需要多条渠道（例如验证渠道之间互不干扰）的用例直接构造 identity.Channel。
func googleChannel(verifier identity.TokenVerifier) identity.Channel {
	return identity.Channel{Source: identity.SourceGoogle, ClientID: "test-google-client", Verifier: verifier}
}

// countingSessionStore 包住内存实现并统计签发次数。
//
// 会话存储没有"列出全部"的接口，而"一次失败的登录没有在库里留下会话"是一条
// 值得固定的性质：只断言"没交付凭证"漏得掉"签发了却没交付"。
type countingSessionStore struct {
	inner identity.SessionStore

	mu   sync.Mutex
	puts int
}

func newCountingSessionStore() *countingSessionStore {
	return &countingSessionStore{inner: identity.NewMemoryStore()}
}

func (s *countingSessionStore) Put(ctx context.Context, hash string, session identity.Session) error {
	s.mu.Lock()
	s.puts++
	s.mu.Unlock()
	return s.inner.Put(ctx, hash, session)
}

func (s *countingSessionStore) Get(ctx context.Context, hash string) (identity.Session, error) {
	return s.inner.Get(ctx, hash)
}

func (s *countingSessionStore) Delete(ctx context.Context, hash string) error {
	return s.inner.Delete(ctx, hash)
}

func (s *countingSessionStore) DeleteExpired(ctx context.Context, now time.Time) (int64, error) {
	return s.inner.DeleteExpired(ctx, now)
}

func (s *countingSessionStore) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.puts
}

// identityFixture 是认证面的一次装配：内存存储 + 可控校验器 + 可检查的日志。
type identityFixture struct {
	subjects      rbac.MutableStore
	identityStore identity.IdentityStore
	sessions      *identity.Sessions
	sessionStore  *countingSessionStore
	service       *IdentityService
	logs          *observer.ObservedLogs
}

// issuedSessions 返回这套存储上签发过的会话条数。
func (f identityFixture) issuedSessions() int { return f.sessionStore.count() }

func newIdentityFixture(t *testing.T, channels ...identity.Channel) identityFixture {
	t.Helper()

	subjects := rbac.NewMemoryStore()
	sessionStore := newCountingSessionStore()
	sessions := identity.NewSessions(sessionStore)
	identityStore := identity.NewMemoryIdentityStore()
	core, logs := observer.New(zapcore.DebugLevel)

	return identityFixture{
		subjects:      subjects,
		identityStore: identityStore,
		sessions:      sessions,
		sessionStore:  sessionStore,
		logs:          logs,
		service:       newIdentityServiceOn(subjects, identityStore, sessions, channels, zap.New(core)),
	}
}

// as 用**同一套存储**再装配一个认证面。
//
// 用于模拟"另一个人"或"另一条渠道"：只有共用存储，身份的唯一归属才会真的
// 被撞上。
func (f identityFixture) as(channels ...identity.Channel) *IdentityService {
	return newIdentityServiceOn(f.subjects, f.identityStore, f.sessions, channels, zap.NewNop())
}

func newIdentityServiceOn(
	subjects rbac.MutableStore,
	identityStore identity.IdentityStore,
	sessions *identity.Sessions,
	channels []identity.Channel,
	logger *zap.Logger,
) *IdentityService {
	return NewIdentityService(subjects, rbac.NewEngine(subjects, logger, nil), IdentityDeps{
		Machine:         interceptor.NewTokenAuthenticator(),
		Identities:      identity.NewIdentities(identityStore, subjects),
		Sessions:        sessions,
		Channels:        identity.NewRegistry(channels...),
		Logger:          logger,
		PendingBindings: newPendingBindings(time.Now, false),
		DeviceLogins:    newDeviceLogins(time.Now),
		// 这里一律启用命令行登录：需要"未启用"的用例自己装配一个不带它的
		// 认证面（见 device_logins_test.go）。
		DeviceApprovalURL: testPublicBaseURL + DeviceApprovalPath,
		LifecycleGate:     &subjectLifecycleGate{},
	})
}

// verifierFor 造一个会把任何凭证都解成同一个渠道身份的校验器。
func verifierFor(externalID string) identity.TokenVerifier {
	return fakeVerifier{identity: identity.VerifiedIdentity{ExternalID: externalID}}
}

// loginAs 走一次登录的两段：校验渠道凭证，然后解析主体并签发会话。
//
// 它直接把服务端在回调里走的那两段接起来（见 redirect_login_flow.go 的
// Callback），而不是调某个 RPC：登录已经没有 RPC 形状了。
func loginAs(t *testing.T, service *IdentityService, credential string) *identityv1.LoginResponse {
	t.Helper()
	verified, err := service.verify(context.Background(), identity.SourceGoogle, credential)
	if err != nil {
		t.Fatalf("校验渠道凭证失败: %v", err)
	}
	issued, err := service.resolveAndIssue(context.Background(), identity.SourceGoogle, verified)
	if err != nil {
		t.Fatalf("登录失败: %v", err)
	}
	return &identityv1.LoginResponse{
		AccessToken: issued.Token,
		ExpiresAt:   issued.Session.ExpiresAt.Format(time.RFC3339),
	}
}

// loginAs 用这个装配的校验器登录。
func (f identityFixture) loginAs(t *testing.T) *identityv1.LoginResponse {
	t.Helper()
	return loginAs(t, f.service, "一份令牌")
}

// caller 造一个"已认证的调用方"上下文。
func caller(t *testing.T, sessions *identity.Sessions, token string) context.Context {
	t.Helper()
	subject, err := sessions.Verify(context.Background(), token)
	if err != nil {
		t.Fatalf("会话不可用: %v", err)
	}
	return interceptor.WithSubject(context.Background(), subject)
}

// 首次登录：登记一个新主体、签发会话，且**权限为空**。
//
// 这是预期路径而不是错误路径——他能登录，只是什么都做不了。
func TestLoginRegistersSubjectWithNoPermissions(t *testing.T) {
	fixture := newIdentityFixture(t, googleChannel(fakeVerifier{identity: identity.VerifiedIdentity{
		ExternalID: "google-sub-a", Display: "a@example.com",
	}}))

	login := fixture.loginAs(t)
	if login.GetAccessToken() == "" {
		t.Fatal("登录没有返回访问凭证")
	}

	subject, err := fixture.service.sessions.Verify(context.Background(), login.GetAccessToken())
	if err != nil {
		t.Fatalf("签发的会话不可用: %v", err)
	}
	if subject.ID == "" {
		t.Error("会话没有代表任何主体")
	}
	bindings, err := fixture.subjects.SubjectBindings(context.Background(), subject.ID)
	if err != nil {
		t.Fatalf("读取绑定失败: %v", err)
	}
	if len(bindings) != 0 {
		t.Errorf("新主体带着 %d 条绑定，期望零权限", len(bindings))
	}
}

// 登录留痕里含**主体标识**，不含令牌。
//
// 主体标识是建立第一个管理员时人工搬运的那个值，因此它必须可检索；
// 而令牌与口令同级，进了日志就等于泄露。记的是主体标识而不是渠道标识：
// 渠道标识随渠道而变，排障时要定位的是"库里这个人"。
func TestLoginLogsSubjectWithoutToken(t *testing.T) {
	fixture := newIdentityFixture(t, googleChannel(fakeVerifier{identity: identity.VerifiedIdentity{ExternalID: "google-sub-a"}}))
	login := fixture.loginAs(t)

	subject, err := fixture.service.sessions.Verify(context.Background(), login.GetAccessToken())
	if err != nil {
		t.Fatalf("会话不可用: %v", err)
	}

	logged := ""
	for _, entry := range fixture.logs.All() {
		logged += entry.Message
		for _, field := range entry.Context {
			logged += field.Key + "=" + field.String
		}
	}
	if logged == "" {
		t.Fatal("登录没有留下任何痕迹")
	}
	if !strings.Contains(logged, subject.ID) {
		t.Errorf("留痕里没有主体标识 %q：%s", subject.ID, logged)
	}
	if strings.Contains(logged, login.GetAccessToken()) {
		t.Error("访问凭证进了日志")
	}
}

// 解绑只作用于自己的主体：拿别人的身份标识来解绑，一行不动。
func TestUnbindIdentityScopedToCaller(t *testing.T) {
	fixture := newIdentityFixture(t, googleChannel(fakeVerifier{identity: identity.VerifiedIdentity{ExternalID: "google-sub-a"}}))
	login := fixture.loginAs(t)

	_, err := fixture.service.UnbindIdentity(caller(t, fixture.sessions, login.GetAccessToken()),
		connect.NewRequest(&identityv1.UnbindIdentityRequest{
			Source: identity.SourceGoogle, ExternalId: "google-sub-other",
		}))
	if got := connect.CodeOf(err); got != connect.CodeNotFound {
		t.Fatalf("code = %v，期望 NotFound", got)
	}
}

// 摘掉最后一个渠道会被拒绝：那会让这个主体再也进不来。
func TestUnbindIdentityKeepsLast(t *testing.T) {
	fixture := newIdentityFixture(t, googleChannel(fakeVerifier{identity: identity.VerifiedIdentity{ExternalID: "google-sub-a"}}))
	login := fixture.loginAs(t)

	_, err := fixture.service.UnbindIdentity(caller(t, fixture.sessions, login.GetAccessToken()),
		connect.NewRequest(&identityv1.UnbindIdentityRequest{
			Source: identity.SourceGoogle, ExternalId: "google-sub-a",
		}))
	if got := connect.CodeOf(err); got != connect.CodeFailedPrecondition {
		t.Fatalf("code = %v，期望 FailedPrecondition", got)
	}
}

// 列出渠道返回当前主体的全部绑定。
func TestListIdentitiesReturnsCallersChannels(t *testing.T) {
	fixture := newIdentityFixture(t, googleChannel(fakeVerifier{identity: identity.VerifiedIdentity{
		ExternalID: "google-sub-a", Display: "a@example.com",
	}}))
	login := fixture.loginAs(t)
	ctx := caller(t, fixture.sessions, login.GetAccessToken())

	// 第二个渠道：先用解出另一个身份的校验器把身份记成待绑定凭据（服务端在
	// 回调里做的事），再用当前会话兑换它。
	second := fixture.as(googleChannel(fakeVerifier{identity: identity.VerifiedIdentity{
		ExternalID: "google-sub-b", Display: "b@example.com",
	}}))
	if _, err := bindOverRedirect(t, second, ctx, identity.SourceGoogle, "第二个渠道的令牌"); err != nil {
		t.Fatalf("绑定失败: %v", err)
	}

	resp, err := fixture.service.ListIdentities(ctx, connect.NewRequest(&identityv1.ListIdentitiesRequest{}))
	if err != nil {
		t.Fatalf("列出失败: %v", err)
	}
	got := resp.Msg.GetIdentities()
	if len(got) != 2 {
		t.Fatalf("渠道数 = %d，期望 2", len(got))
	}
	// 顺序稳定：按（来源，身份标识）。
	if got[0].GetExternalId() != "google-sub-a" || got[1].GetExternalId() != "google-sub-b" {
		t.Errorf("渠道顺序 = %q, %q", got[0].GetExternalId(), got[1].GetExternalId())
	}
}

// 用签发的会话打受保护方法：会话必须真的被认证路径认下来。
//
// 这条把"登录签发"与"请求认证"两侧接起来——只测其中一侧，
// 会漏掉"登录拿到的凭证在下一个请求里不认"这类断裂。
func TestIssuedSessionAuthenticatesRequests(t *testing.T) {
	fixture := newIdentityFixture(t, googleChannel(fakeVerifier{identity: identity.VerifiedIdentity{ExternalID: "google-sub-a"}}))
	login := fixture.loginAs(t)

	// 认证入口：机器凭证集合为空，因此这次认证只能走会话路径。
	authenticator := &credentialAuthenticator{
		machine:  interceptor.NewTokenAuthenticator(),
		sessions: fixture.service.sessions,
	}
	header := http.Header{}
	header.Set(interceptor.HeaderAuthorization, "Bearer "+login.GetAccessToken())

	subject, err := authenticator.Authenticate(context.Background(), header)
	if err != nil {
		t.Fatalf("签发的会话没有被认证路径接受: %v", err)
	}
	if subject.ID == "" {
		t.Error("认证出来的主体没有标识")
	}
}

// 存储故障必须与"凭证不成立"分开。混为一谈会把一次数据库抖动表现成
// 一次全站登录失效。
func TestAuthenticatorDistinguishesStoreFailure(t *testing.T) {
	broken := identity.NewSessions(failingSessionStore{})
	authenticator := &credentialAuthenticator{
		machine:  interceptor.NewTokenAuthenticator(),
		sessions: broken,
	}
	header := http.Header{}
	header.Set(interceptor.HeaderAuthorization, "Bearer 任意凭证")

	_, err := authenticator.Authenticate(context.Background(), header)
	if !errors.Is(err, interceptor.ErrStoreUnavailable) {
		t.Fatalf("err = %v，期望 ErrStoreUnavailable", err)
	}
	if errors.Is(err, interceptor.ErrInvalidCredential) {
		t.Error("存储故障被当成了凭证无效")
	}
}

// failingSessionStore 是一个永远失败的会话存储。
type failingSessionStore struct{}

var errSessionStoreBoom = errors.New("库炸了")

func (failingSessionStore) Put(context.Context, string, identity.Session) error {
	return errSessionStoreBoom
}

func (failingSessionStore) Get(context.Context, string) (identity.Session, error) {
	return identity.Session{}, errSessionStoreBoom
}

func (failingSessionStore) Delete(context.Context, string) error { return errSessionStoreBoom }

func (failingSessionStore) DeleteExpired(context.Context, time.Time) (int64, error) {
	return 0, errSessionStoreBoom
}

// pendingBindingRequest 造一份"带着待绑定 cookie"的兑换请求。
//
// source 是要兑换的渠道来源：它只用于与凭据里记下的来源互相印证。
func pendingBindingRequest(source, token string) *connect.Request[identityv1.CompleteIdentityBindingRequest] {
	req := connect.NewRequest(&identityv1.CompleteIdentityBindingRequest{Source: source})
	req.Header().Set("Cookie", (&http.Cookie{Name: pendingBindingCookie, Value: token}).String())
	return req
}

// bindOverRedirect 走一次完整的绑定：校验渠道凭证、把它记成待绑定凭据
// （服务端在回调里做的两件事），再用**当前会话**兑换它。
//
// 绑定只有这一条形状：凭证经浏览器导航到达服务端，归属只由兑换时那次已认证的
// 调用决定。用例里要"绑一个渠道上去"时走它，而不是自己拼 request。
func bindOverRedirect(t *testing.T, service *IdentityService, ctx context.Context, source, credential string) (*connect.Response[identityv1.CompleteIdentityBindingResponse], error) {
	t.Helper()
	verified, err := service.verify(context.Background(), source, credential)
	if err != nil {
		return nil, err
	}
	token, err := service.stagePendingBinding(source, verified)
	if err != nil {
		t.Fatalf("记下待绑定凭据失败: %v", err)
	}
	return service.CompleteIdentityBinding(ctx, pendingBindingRequest(source, token))
}

// registerGithubThrowaway 直接在存储上登记一个只有 GitHub 身份的零权限主体，
// 模拟"先用 GitHub 单独登录过一次"。
func registerGithubThrowaway(t *testing.T, fixture identityFixture, externalID string) rbac.Subject {
	t.Helper()
	resolver := identity.NewIdentities(fixture.identityStore, fixture.subjects)
	subject, err := resolver.ResolveOrRegister(context.Background(), identity.SourceGithub, externalID, "octo")
	if err != nil {
		t.Fatalf("登记 GitHub 主体失败: %v", err)
	}
	return subject
}

// 兑换一份已校验身份，把它绑到**当前会话**代表的主体上。
func TestCompleteIdentityBindingBindsToCaller(t *testing.T) {
	fixture := newIdentityFixture(t, googleChannel(verifierFor("g-a")))
	login := fixture.loginAs(t)
	ctx := caller(t, fixture.sessions, login.GetAccessToken())
	subject, err := fixture.sessions.Verify(context.Background(), login.GetAccessToken())
	if err != nil {
		t.Fatalf("会话不可用: %v", err)
	}

	token, err := fixture.service.stagePendingBinding(identity.SourceGithub, identity.VerifiedIdentity{
		ExternalID: "gh-a",
		Display:    "octo",
	})
	if err != nil {
		t.Fatalf("登记待绑定凭据失败: %v", err)
	}
	resp, err := fixture.service.CompleteIdentityBinding(ctx, pendingBindingRequest(identity.SourceGithub, token))
	if err != nil {
		t.Fatalf("兑换失败: %v", err)
	}
	if resp.Msg.GetReclaimed() {
		t.Error("没有发生认领，reclaimed 却为 true")
	}
	if cookies := resp.Header().Values("Set-Cookie"); len(cookies) == 0 || !strings.Contains(cookies[0], pendingBindingCookie+"=") {
		t.Errorf("兑换成功后没有清掉浏览器侧凭据: %v", cookies)
	}

	owner, err := fixture.identityStore.Lookup(context.Background(), identity.SourceGithub, "gh-a")
	if err != nil {
		t.Fatalf("读取归属失败: %v", err)
	}
	if owner.SubjectID != subject.ID {
		t.Errorf("归属 = %q，期望 %q", owner.SubjectID, subject.ID)
	}
}

// 没有会话时不能兑换；而且这份凭据不会被这次未认证的尝试消耗。
func TestCompleteIdentityBindingRequiresSession(t *testing.T) {
	fixture := newIdentityFixture(t, googleChannel(verifierFor("g-a")))
	token, err := fixture.service.stagePendingBinding(identity.SourceGithub, identity.VerifiedIdentity{ExternalID: "gh-a"})
	if err != nil {
		t.Fatalf("登记待绑定凭据失败: %v", err)
	}

	if _, err := fixture.service.CompleteIdentityBinding(context.Background(), pendingBindingRequest(identity.SourceGithub, token)); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("code = %v，期望 Unauthenticated（err=%v）", connect.CodeOf(err), err)
	}

	// 未认证的尝试没有读走凭据：拿到会话之后仍可兑换。
	login := fixture.loginAs(t)
	if _, err := fixture.service.CompleteIdentityBinding(
		caller(t, fixture.sessions, login.GetAccessToken()), pendingBindingRequest(identity.SourceGithub, token)); err != nil {
		t.Fatalf("认证后兑换失败: %v", err)
	}
}

// 凭据缺失、已用过、来源不符都归为同一个拒绝，且一次性。
func TestCompleteIdentityBindingRejectsBadCredential(t *testing.T) {
	fixture := newIdentityFixture(t, googleChannel(verifierFor("g-a")))
	login := fixture.loginAs(t)
	ctx := caller(t, fixture.sessions, login.GetAccessToken())

	if _, err := fixture.service.CompleteIdentityBinding(ctx, pendingBindingRequest(identity.SourceGithub, "never-issued")); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("缺失凭据 code = %v，期望 FailedPrecondition（err=%v）", connect.CodeOf(err), err)
	}

	token, err := fixture.service.stagePendingBinding(identity.SourceGithub, identity.VerifiedIdentity{ExternalID: "gh-a"})
	if err != nil {
		t.Fatalf("登记待绑定凭据失败: %v", err)
	}
	if _, err := fixture.service.CompleteIdentityBinding(ctx, pendingBindingRequest(identity.SourceGithub, token)); err != nil {
		t.Fatalf("第一次兑换失败: %v", err)
	}
	if _, err := fixture.service.CompleteIdentityBinding(ctx, pendingBindingRequest(identity.SourceGithub, token)); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("重放 code = %v，期望 FailedPrecondition（err=%v）", connect.CodeOf(err), err)
	}

	// 来源不符也算这份凭据不能兑换，且同样一次性。
	otherToken, err := fixture.service.stagePendingBinding(identity.SourceGithub, identity.VerifiedIdentity{ExternalID: "gh-b"})
	if err != nil {
		t.Fatalf("登记待绑定凭据失败: %v", err)
	}
	mismatch := connect.NewRequest(&identityv1.CompleteIdentityBindingRequest{Source: identity.SourceGoogle})
	mismatch.Header().Set("Cookie", (&http.Cookie{Name: pendingBindingCookie, Value: otherToken}).String())
	if _, err := fixture.service.CompleteIdentityBinding(ctx, mismatch); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("来源不符 code = %v，期望 FailedPrecondition（err=%v）", connect.CodeOf(err), err)
	}
}

// 身份已属于一个空主体时，兑换把它认领到当前主体；原主体只剩零身份、零角色。
func TestCompleteIdentityBindingReclaimsVacantSubject(t *testing.T) {
	fixture := newIdentityFixture(t, googleChannel(verifierFor("g-a")))
	login := fixture.loginAs(t)
	ctx := caller(t, fixture.sessions, login.GetAccessToken())
	target, err := fixture.sessions.Verify(context.Background(), login.GetAccessToken())
	if err != nil {
		t.Fatalf("会话不可用: %v", err)
	}
	throwaway := registerGithubThrowaway(t, fixture, "gh-a")

	token, err := fixture.service.stagePendingBinding(identity.SourceGithub, identity.VerifiedIdentity{
		ExternalID: "gh-a",
		Display:    "octo-new",
	})
	if err != nil {
		t.Fatalf("登记待绑定凭据失败: %v", err)
	}
	resp, err := fixture.service.CompleteIdentityBinding(ctx, pendingBindingRequest(identity.SourceGithub, token))
	if err != nil {
		t.Fatalf("认领失败: %v", err)
	}
	if !resp.Msg.GetReclaimed() {
		t.Error("发生了认领，reclaimed 却为 false")
	}

	owner, err := fixture.identityStore.Lookup(context.Background(), identity.SourceGithub, "gh-a")
	if err != nil {
		t.Fatalf("读取归属失败: %v", err)
	}
	if owner.SubjectID != target.ID {
		t.Errorf("认领后归属 = %q，期望 %q", owner.SubjectID, target.ID)
	}
	if owner.Display != "octo-new" {
		t.Errorf("展示信息 = %q，期望刷新为 octo-new", owner.Display)
	}
	left, err := fixture.service.identities.List(context.Background(), throwaway.ID)
	if err != nil {
		t.Fatalf("列出原主体失败: %v", err)
	}
	if len(left) != 0 {
		t.Errorf("原主体还剩 %d 条身份，期望 0", len(left))
	}
	bindings, err := fixture.subjects.SubjectBindings(context.Background(), throwaway.ID)
	if err != nil {
		t.Fatalf("读取原主体角色失败: %v", err)
	}
	if len(bindings) != 0 {
		t.Errorf("原主体带着 %d 条角色绑定，期望 0", len(bindings))
	}

	resolver := identity.NewIdentities(fixture.identityStore, fixture.subjects)
	again, err := resolver.ResolveOrRegister(context.Background(), identity.SourceGithub, "gh-a", "")
	if err != nil {
		t.Fatalf("再次解析失败: %v", err)
	}
	if again.ID != target.ID {
		t.Errorf("再次登录得到 %q，期望 %q", again.ID, target.ID)
	}
}

// 原主体有角色绑定时不认领：那是非空主体，认领会把角色搁浅。
func TestCompleteIdentityBindingRejectsNonVacantByRole(t *testing.T) {
	fixture := newIdentityFixture(t, googleChannel(verifierFor("g-a")))
	login := fixture.loginAs(t)
	ctx := caller(t, fixture.sessions, login.GetAccessToken())
	throwaway := registerGithubThrowaway(t, fixture, "gh-a")
	if err := fixture.subjects.Bind(context.Background(), rbac.RoleBinding{
		SubjectID: throwaway.ID,
		RoleID:    rbac.RoleViewer,
		Scope:     rbac.GlobalScope,
	}); err != nil {
		t.Fatalf("授予角色失败: %v", err)
	}

	token, err := fixture.service.stagePendingBinding(identity.SourceGithub, identity.VerifiedIdentity{ExternalID: "gh-a"})
	if err != nil {
		t.Fatalf("登记待绑定凭据失败: %v", err)
	}
	_, err = fixture.service.CompleteIdentityBinding(ctx, pendingBindingRequest(identity.SourceGithub, token))
	if connect.CodeOf(err) != connect.CodeAlreadyExists {
		t.Fatalf("code = %v，期望 AlreadyExists（err=%v）", connect.CodeOf(err), err)
	}
	if strings.Contains(err.Error(), throwaway.ID) {
		t.Errorf("错误信息泄露了原主体：%q", err.Error())
	}

	owner, err := fixture.identityStore.Lookup(context.Background(), identity.SourceGithub, "gh-a")
	if err != nil {
		t.Fatalf("读取归属失败: %v", err)
	}
	if owner.SubjectID != throwaway.ID {
		t.Errorf("归属被改动了：%q，期望 %q", owner.SubjectID, throwaway.ID)
	}
}

// 原主体还有别的身份时也不认领：空主体的两个条件缺一不可。
func TestCompleteIdentityBindingRejectsNonVacantByIdentity(t *testing.T) {
	fixture := newIdentityFixture(t, googleChannel(verifierFor("g-a")))
	login := fixture.loginAs(t)
	ctx := caller(t, fixture.sessions, login.GetAccessToken())
	throwaway := registerGithubThrowaway(t, fixture, "gh-a")
	resolver := identity.NewIdentities(fixture.identityStore, fixture.subjects)
	if err := resolver.Bind(context.Background(), throwaway.ID, identity.SourceGithub, "gh-b", ""); err != nil {
		t.Fatalf("绑定第二个身份失败: %v", err)
	}

	token, err := fixture.service.stagePendingBinding(identity.SourceGithub, identity.VerifiedIdentity{ExternalID: "gh-a"})
	if err != nil {
		t.Fatalf("登记待绑定凭据失败: %v", err)
	}
	_, err = fixture.service.CompleteIdentityBinding(ctx, pendingBindingRequest(identity.SourceGithub, token))
	if connect.CodeOf(err) != connect.CodeAlreadyExists {
		t.Fatalf("code = %v，期望 AlreadyExists（err=%v）", connect.CodeOf(err), err)
	}
	if strings.Contains(err.Error(), throwaway.ID) {
		t.Errorf("错误信息泄露了原主体：%q", err.Error())
	}
}

// 认领必须先看到"原主体没有角色"这条事实。测试持有生命周期锁再给原主体
// 授角色，重放"授予与认领并发"的时序：认领必须落在拒绝一侧。
//
// 这里直接写存储，验的是**认领这一侧**；授予那一侧是否真的持锁由
// TestAssignRoleHoldsLifecycleGate 走生产路径断言。两条缺一不可。
func TestCompleteIdentityBindingHonorsLifecycleGate(t *testing.T) {
	fixture := newIdentityFixture(t, googleChannel(verifierFor("g-a")))
	login := fixture.loginAs(t)
	ctx := caller(t, fixture.sessions, login.GetAccessToken())
	throwaway := registerGithubThrowaway(t, fixture, "gh-a")

	token, err := fixture.service.stagePendingBinding(identity.SourceGithub, identity.VerifiedIdentity{ExternalID: "gh-a"})
	if err != nil {
		t.Fatalf("登记待绑定凭据失败: %v", err)
	}

	unlock := fixture.service.gate.lock()
	done := make(chan error, 1)
	go func() {
		_, err := fixture.service.CompleteIdentityBinding(ctx, pendingBindingRequest(identity.SourceGithub, token))
		done <- err
	}()

	// 认领要拿同一把锁；在它拿到之前授上角色，它必须看到这条事实。
	if err := fixture.subjects.Bind(context.Background(), rbac.RoleBinding{
		SubjectID: throwaway.ID,
		RoleID:    rbac.RoleViewer,
		Scope:     rbac.GlobalScope,
	}); err != nil {
		t.Fatalf("授予角色失败: %v", err)
	}
	unlock()

	if err := <-done; connect.CodeOf(err) != connect.CodeAlreadyExists {
		t.Fatalf("code = %v，期望 AlreadyExists（err=%v）", connect.CodeOf(err), err)
	}
	owner, err := fixture.identityStore.Lookup(context.Background(), identity.SourceGithub, "gh-a")
	if err != nil {
		t.Fatalf("读取归属失败: %v", err)
	}
	if owner.SubjectID != throwaway.ID {
		t.Errorf("归属被改动了：%q，期望 %q", owner.SubjectID, throwaway.ID)
	}
}

// 生命周期锁本身是互斥的：第二个获取者必须等第一个释放。
func TestSubjectLifecycleGateIsExclusive(t *testing.T) {
	gate := &subjectLifecycleGate{}
	unlock := gate.lock()

	acquired := make(chan struct{})
	go func() {
		release := gate.lock()
		close(acquired)
		release()
	}()

	select {
	case <-acquired:
		t.Fatal("第二个获取者在第一把锁释放前就拿到了锁")
	case <-time.After(10 * time.Millisecond):
	}
	unlock()
	select {
	case <-acquired:
	case <-time.After(time.Second):
		t.Fatal("第一把锁释放后第二个获取者仍拿不到锁")
	}
}
