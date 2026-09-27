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
		Machine:    interceptor.NewTokenAuthenticator(),
		Identities: identity.NewIdentities(identityStore, subjects),
		Sessions:   sessions,
		Channels:   identity.NewRegistry(channels...),
		Logger:     logger,
	})
}

// verifierFor 造一个会把任何凭证都解成同一个渠道身份的校验器。
func verifierFor(externalID string) identity.TokenVerifier {
	return fakeVerifier{identity: identity.VerifiedIdentity{ExternalID: externalID}}
}

// loginAs 用一份身份令牌登录，返回一次登录的产物。
func loginAs(t *testing.T, service *IdentityService, idToken string) *identityv1.LoginResponse {
	t.Helper()
	resp, err := service.Login(context.Background(), connect.NewRequest(&identityv1.LoginRequest{
		Credential: &identityv1.LoginRequest_Google{
			Google: &identityv1.GoogleCredential{IdToken: idToken},
		},
	}))
	if err != nil {
		t.Fatalf("登录失败: %v", err)
	}
	return resp.Msg
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

// 令牌不成立时返回"未认证"，**不是**"无权限"。
//
// 混为一谈会让客户端在凭证问题时反复尝试刷新，把一次凭证问题放大成
// 一次登录风暴。
func TestLoginInvalidTokenIsUnauthenticated(t *testing.T) {
	fixture := newIdentityFixture(t, googleChannel(fakeVerifier{err: identity.ErrInvalidToken}))

	_, err := fixture.service.Login(context.Background(), connect.NewRequest(&identityv1.LoginRequest{
		Credential: &identityv1.LoginRequest_Google{Google: &identityv1.GoogleCredential{IdToken: "坏令牌"}},
	}))
	if got := connect.CodeOf(err); got != connect.CodeUnauthenticated {
		t.Fatalf("code = %v，期望 Unauthenticated", got)
	}
}

// 提供方够不着是"服务不可用"，不是"凭证无效"。
func TestLoginProviderFailureIsUnavailable(t *testing.T) {
	fixture := newIdentityFixture(t, googleChannel(fakeVerifier{err: identity.ErrProviderUnavailable}))

	_, err := fixture.service.Login(context.Background(), connect.NewRequest(&identityv1.LoginRequest{
		Credential: &identityv1.LoginRequest_Google{Google: &identityv1.GoogleCredential{IdToken: "任意"}},
	}))
	if got := connect.CodeOf(err); got != connect.CodeUnavailable {
		t.Fatalf("code = %v，期望 Unavailable", got)
	}
}

// 未启用的渠道报"未实现"，而不是"凭证无效"：前者说的是这条路没开，
// 后者会说成是调用方拿错了凭证。
func TestLoginGoogleDisabled(t *testing.T) {
	fixture := newIdentityFixture(t)

	_, err := fixture.service.Login(context.Background(), connect.NewRequest(&identityv1.LoginRequest{
		Credential: &identityv1.LoginRequest_Google{Google: &identityv1.GoogleCredential{IdToken: "任意"}},
	}))
	if got := connect.CodeOf(err); got != connect.CodeUnimplemented {
		t.Fatalf("code = %v，期望 Unimplemented", got)
	}
}

// 绑定把渠道挂到**当前凭证代表的**主体上，而不是令牌里说的任何人。
func TestBindIdentityBindsToCaller(t *testing.T) {
	first := identity.VerifiedIdentity{ExternalID: "google-sub-a", Display: "a@example.com"}
	fixture := newIdentityFixture(t, googleChannel(fakeVerifier{identity: first}))

	login := fixture.loginAs(t)
	ctx := caller(t, fixture.sessions, login.GetAccessToken())

	resp, err := fixture.service.BindIdentity(ctx, connect.NewRequest(&identityv1.BindIdentityRequest{
		Credential: &identityv1.BindIdentityRequest_Google{
			Google: &identityv1.GoogleCredential{IdToken: "第二个渠道的令牌"},
		},
	}))
	if err != nil {
		t.Fatalf("绑定失败: %v", err)
	}
	if len(resp.Msg.GetIdentities()) != 1 {
		t.Fatalf("绑定后渠道数 = %d，期望 1", len(resp.Msg.GetIdentities()))
	}
	if resp.Msg.GetIdentities()[0].GetDisplay() != "a@example.com" {
		t.Errorf("展示信息 = %q", resp.Msg.GetIdentities()[0].GetDisplay())
	}
}

// 绑定缺少凭证时是调用方的输入问题。
func TestBindIdentityRequiresCredential(t *testing.T) {
	fixture := newIdentityFixture(t, googleChannel(fakeVerifier{identity: identity.VerifiedIdentity{ExternalID: "google-sub-a"}}))
	login := fixture.loginAs(t)

	_, err := fixture.service.BindIdentity(caller(t, fixture.sessions, login.GetAccessToken()),
		connect.NewRequest(&identityv1.BindIdentityRequest{}))
	if got := connect.CodeOf(err); got != connect.CodeInvalidArgument {
		t.Fatalf("code = %v，期望 InvalidArgument", got)
	}
}

// 一个身份已经属于别人时拒绝，且**不透露占用者**。
func TestBindIdentityRejectsTakenIdentity(t *testing.T) {
	// 第一个人先登录，占住 google-sub-a。
	first := newIdentityFixture(t, googleChannel(fakeVerifier{identity: identity.VerifiedIdentity{ExternalID: "google-sub-a"}}))
	firstLogin := first.loginAs(t)
	firstSubject, err := first.service.sessions.Verify(context.Background(), firstLogin.GetAccessToken())
	if err != nil {
		t.Fatalf("会话不可用: %v", err)
	}

	// 第二个人在同一套存储上登录（占住 google-sub-b），再用第一人的令牌发起绑定：
	// 校验解出的是 google-sub-a，而归属只能落在发起者身上——于是撞上唯一归属。
	second := first.as(googleChannel(verifierFor("google-sub-b")))
	secondLogin := loginAs(t, second, "第二个人的令牌")
	callerCtx := caller(t, first.sessions, secondLogin.GetAccessToken())

	// 绑定请求里带的是**第一人**的令牌：用解出 google-sub-a 的校验器再装配一个
	// 共用同一套存储的服务，而会话仍然来自第二个人。
	binder := first.as(googleChannel(verifierFor("google-sub-a")))
	_, err = binder.BindIdentity(callerCtx, connect.NewRequest(&identityv1.BindIdentityRequest{
		Credential: &identityv1.BindIdentityRequest_Google{
			Google: &identityv1.GoogleCredential{IdToken: "第一个人的令牌"},
		},
	}))
	if got := connect.CodeOf(err); got != connect.CodeAlreadyExists {
		t.Fatalf("code = %v，期望 AlreadyExists（err=%v）", got, err)
	}
	if strings.Contains(err.Error(), firstSubject.ID) {
		t.Errorf("错误信息泄露了占用者：%q", err.Error())
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

	// 第二个渠道：用解出另一个身份的校验器再装配一个共用同一套存储的服务。
	second := fixture.as(googleChannel(fakeVerifier{identity: identity.VerifiedIdentity{
		ExternalID: "google-sub-b", Display: "b@example.com",
	}}))
	if _, err := second.BindIdentity(ctx, connect.NewRequest(&identityv1.BindIdentityRequest{
		Credential: &identityv1.BindIdentityRequest_Google{
			Google: &identityv1.GoogleCredential{IdToken: "第二个渠道的令牌"},
		},
	})); err != nil {
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
