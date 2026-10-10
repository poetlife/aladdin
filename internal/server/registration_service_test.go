package server

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"go.uber.org/zap"

	identityv1 "github.com/poetlife/aladdin/api/gen/aladdin/identity/v1"
	"github.com/poetlife/aladdin/internal/identity"
	"github.com/poetlife/aladdin/internal/rbac"
	"github.com/poetlife/aladdin/internal/registration"
	"github.com/poetlife/aladdin/internal/server/interceptor"
)

// 注册面是"谁能成为新主体"的唯一入口，用例覆盖四个方向：
//
//   - 三种姿态各自对**未登记**身份做了什么（以及**没做**什么）；
//   - 已登记身份不受策略影响；
//   - 默认角色落成一条真实的绑定，且授予失败时身份不会被登记；
//   - 管理面写策略时的三条语义校验。
//
// 全部跑在内存实现上：这里要冻结的是**闸门语义**，不是某个驱动的行为
// （存储契约由 gormstore 那一套守着）。

// registrationServiceOn 在同一套存储上装配一个注册面服务。
func registrationServiceOn(fixture identityFixture) *RegistrationService {
	return NewRegistrationService(fixture.service, fixture.registrations, fixture.subjects, zap.NewNop())
}

// setPolicy 直接写一条策略。它走的是领域入口，**不经过管理面的语义校验**——
// 那些校验由 TestPutRegistrationPolicyValidates 单独覆盖。
func setPolicy(t *testing.T, fixture identityFixture, policy registration.Policy) {
	t.Helper()
	if _, err := fixture.registrations.PutPolicy(context.Background(), policy, "usr_admin"); err != nil {
		t.Fatalf("写入注册策略失败: %v", err)
	}
}

// verifyOne 用一条渠道凭证换回一份已校验身份。
func verifyOne(t *testing.T, service *IdentityService, credential string) identity.VerifiedIdentity {
	t.Helper()
	verified, err := service.verify(context.Background(), identity.SourceGoogle, credential)
	if err != nil {
		t.Fatalf("校验渠道凭证失败: %v", err)
	}
	return verified
}

// completeRegistration 带着一份待注册凭据调用"完成注册"。
func completeRegistration(t *testing.T, svc *RegistrationService, token, code string) (*connect.Response[identityv1.CompleteRegistrationResponse], error) {
	t.Helper()
	req := connect.NewRequest(&identityv1.CompleteRegistrationRequest{InviteCode: code})
	req.Header().Set("Cookie", (&http.Cookie{Name: registrationCookie, Value: token}).String())
	return svc.CompleteRegistration(context.Background(), req)
}

// 缺少默认角色时，开放注册登记出来的仍是一个零权限、无绑定的主体。
func TestOpenRegistrationKeepsZeroPermissions(t *testing.T) {
	fixture := newIdentityFixture(t, googleChannel(verifierFor("g-a")))

	result := admitAs(t, fixture.service, "一份令牌")
	if result.kind != admissionIssued {
		t.Fatalf("开放注册应当直接签发会话，实际 %v", result.kind)
	}
	bindings, err := fixture.subjects.SubjectBindings(context.Background(), result.issued.Session.SubjectID)
	if err != nil {
		t.Fatalf("读取绑定失败: %v", err)
	}
	if len(bindings) != 0 {
		t.Fatalf("没有默认角色时不该有任何绑定，实际 %d 条", len(bindings))
	}
}

// 默认角色写下的是一条**真实的角色绑定**，并且范围同时成为新主体的默认作用域。
//
// 这一条是"注册策略是管理面的记录、不是配置"的可验证形式：判定路径只看见那条
// 绑定，看不见策略。
func TestDefaultRoleIsWrittenAsARealBinding(t *testing.T) {
	fixture := newIdentityFixture(t, googleChannel(verifierFor("g-a")))
	setPolicy(t, fixture, registration.Policy{
		Mode:          registration.ModeOpen,
		DefaultRoleID: "viewer",
		DefaultScope:  "tenant/acme",
	})

	result := admitAs(t, fixture.service, "一份令牌")
	if result.kind != admissionIssued {
		t.Fatalf("开放注册应当直接签发会话，实际 %v", result.kind)
	}
	subjectID := result.issued.Session.SubjectID

	bindings, err := fixture.subjects.SubjectBindings(context.Background(), subjectID)
	if err != nil {
		t.Fatalf("读取绑定失败: %v", err)
	}
	if len(bindings) != 1 {
		t.Fatalf("应当恰好有一条默认绑定，实际 %d 条", len(bindings))
	}
	if bindings[0].RoleID != "viewer" || bindings[0].Scope != rbac.Scope("tenant/acme") {
		t.Fatalf("绑定的形状不对: %+v", bindings[0])
	}
	// 主体的默认作用域取自这条绑定，而不是另有来源。
	if result.issued.Session.DefaultScope != rbac.Scope("tenant/acme") {
		t.Errorf("会话的默认作用域应当取自那条绑定，实际 %q", result.issued.Session.DefaultScope)
	}
}

// 不接受新账号：**什么都不登记**——库里不该多出任何一行。
func TestClosedRefusesNewSubjectAndWritesNothing(t *testing.T) {
	fixture := newIdentityFixture(t, googleChannel(verifierFor("g-a")))
	setPolicy(t, fixture, registration.Policy{Mode: registration.ModeClosed})

	result := admitAs(t, fixture.service, "一份令牌")
	if result.kind != admissionClosed {
		t.Fatalf("应当被拒绝，实际 %v", result.kind)
	}
	if result.issued.Token != "" {
		t.Error("被拒绝时不该签发任何会话")
	}
	if _, found, err := fixture.service.identities.Resolve(context.Background(), identity.SourceGoogle, "g-a"); err != nil {
		t.Fatalf("解析身份失败: %v", err)
	} else if found {
		t.Fatal("被拒绝的身份不该被登记")
	}
	if fixture.issuedSessions() != 0 {
		t.Error("被拒绝时不该签发任何会话")
	}
}

// 需要邀请码：回调这一刻**什么都不登记**，等码通过之后才落库。
//
// 提前登记会让一个没有码的人先在库里占一个主体——那正是闸门要拦的事。
func TestInviteModeWaitsForCode(t *testing.T) {
	fixture := newIdentityFixture(t, googleChannel(verifierFor("g-a")))
	setPolicy(t, fixture, registration.Policy{Mode: registration.ModeInvite})

	result := admitAs(t, fixture.service, "一份令牌")
	if result.kind != admissionNeedsInvite {
		t.Fatalf("应当停在等邀请码这一步，实际 %v", result.kind)
	}
	if _, found, err := fixture.service.identities.Resolve(context.Background(), identity.SourceGoogle, "g-a"); err != nil {
		t.Fatalf("解析身份失败: %v", err)
	} else if found {
		t.Fatal("还没给码就不该登记主体")
	}
}

// 走完邀请码那一条路：登记、授默认角色、签发会话，一次交回。
func TestCompleteRegistrationWithInvite(t *testing.T) {
	fixture := newIdentityFixture(t, googleChannel(verifierFor("g-a")))
	setPolicy(t, fixture, registration.Policy{
		Mode:          registration.ModeInvite,
		DefaultRoleID: "viewer",
	})
	svc := registrationServiceOn(fixture)

	invite, code, err := fixture.registrations.Issue(context.Background(),
		registration.IssueParams{MaxUses: 1}, "usr_admin")
	if err != nil {
		t.Fatalf("签发邀请码失败: %v", err)
	}
	verified := verifyOne(t, fixture.service, "一份令牌")
	token, err := fixture.service.pendingRegistrations.issue(identity.SourceGoogle, verified)
	if err != nil {
		t.Fatalf("记下待注册凭据失败: %v", err)
	}

	resp, err := completeRegistration(t, svc, token, code)
	if err != nil {
		t.Fatalf("完成注册失败: %v", err)
	}
	if resp.Msg.GetAccessToken() == "" {
		t.Fatal("完成注册应当交回一份会话")
	}

	subject, found, err := fixture.service.identities.Resolve(context.Background(), identity.SourceGoogle, "g-a")
	if err != nil {
		t.Fatalf("解析身份失败: %v", err)
	}
	if !found {
		t.Fatal("完成注册之后这个身份应当已经登记")
	}
	bindings, err := fixture.subjects.SubjectBindings(context.Background(), subject.ID)
	if err != nil {
		t.Fatalf("读取绑定失败: %v", err)
	}
	if len(bindings) != 1 || bindings[0].RoleID != "viewer" {
		t.Fatalf("走邀请码进来也应当拿到默认角色，实际 %+v", bindings)
	}

	// 码被消费了一次，精确到那一条记录上。
	list, err := fixture.registrations.List(context.Background())
	if err != nil {
		t.Fatalf("读取邀请码失败: %v", err)
	}
	if len(list) != 1 || list[0].UsedCount != 1 || list[0].ID != invite.ID {
		t.Fatalf("邀请码的用量没被推进: %+v", list)
	}
}

// 四种"兑换不动"返回**同一句话**：不区分它们，才不会把一次失败的注册变成对
// "这个码是否曾经有效"的探测。
func TestCompleteRegistrationRejectsUnusableCodes(t *testing.T) {
	fixture := newIdentityFixture(t, googleChannel(verifierFor("g-a")))
	setPolicy(t, fixture, registration.Policy{Mode: registration.ModeInvite})
	svc := registrationServiceOn(fixture)
	ctx := context.Background()

	spent, spentCode, err := fixture.registrations.Issue(ctx, registration.IssueParams{MaxUses: 1}, "usr_admin")
	if err != nil {
		t.Fatalf("签发邀请码失败: %v", err)
	}
	if _, err := fixture.registrations.Redeem(ctx, spentCode); err != nil {
		t.Fatalf("预先把码用掉失败: %v", err)
	}
	revoked, revokedCode, err := fixture.registrations.Issue(ctx, registration.IssueParams{}, "usr_admin")
	if err != nil {
		t.Fatalf("签发邀请码失败: %v", err)
	}
	if _, err := fixture.registrations.Revoke(ctx, revoked.ID); err != nil {
		t.Fatalf("撤销失败: %v", err)
	}
	if spent.ID == "" || revokedCode == "" {
		t.Fatal("测试数据没造出来")
	}

	for _, tc := range []struct{ name, code string }{
		{"形状不对", "短"},
		{"从未签发过", strings.Repeat("A", 16)},
		{"次数用尽", spentCode},
		{"已撤销", revokedCode},
	} {
		t.Run(tc.name, func(t *testing.T) {
			verified := verifyOne(t, fixture.service, "一份令牌")
			token, err := fixture.service.pendingRegistrations.issue(identity.SourceGoogle, verified)
			if err != nil {
				t.Fatalf("记下待注册凭据失败: %v", err)
			}
			_, err = completeRegistration(t, svc, token, tc.code)
			if connect.CodeOf(err) != connect.CodeFailedPrecondition {
				t.Fatalf("应当是一个前置条件失败，实际 %v", err)
			}
			if got := err.Error(); !strings.Contains(got, "邀请码无效或已用尽") {
				t.Fatalf("四种情形应当给同一句话，实际 %q", got)
			}
			if _, found, _ := fixture.service.identities.Resolve(context.Background(), identity.SourceGoogle, "g-a"); found {
				t.Fatal("码不成立时不该登记任何主体")
			}
		})
	}
}

// 待注册凭据**一次性**：兑换过的再来一次得到"这次注册已经过期"，
// 而不是第二次登记出一个新主体。
func TestRegistrationCredentialIsSingleUse(t *testing.T) {
	fixture := newIdentityFixture(t, googleChannel(verifierFor("g-a")))
	setPolicy(t, fixture, registration.Policy{Mode: registration.ModeInvite})
	svc := registrationServiceOn(fixture)

	_, code, err := fixture.registrations.Issue(context.Background(), registration.IssueParams{}, "usr_admin")
	if err != nil {
		t.Fatalf("签发邀请码失败: %v", err)
	}
	verified := verifyOne(t, fixture.service, "一份令牌")
	token, err := fixture.service.pendingRegistrations.issue(identity.SourceGoogle, verified)
	if err != nil {
		t.Fatalf("记下待注册凭据失败: %v", err)
	}

	if _, err := completeRegistration(t, svc, token, code); err != nil {
		t.Fatalf("第一次应当成功: %v", err)
	}
	_, err = completeRegistration(t, svc, token, code)
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("第二次应当被拒绝，实际 %v", err)
	}
	if !strings.Contains(err.Error(), "重新登录") {
		t.Fatalf("凭据失效与码无效是两件事，措辞应当分开，实际 %q", err.Error())
	}
}

// 模式在"完成注册"这一刻**重读**：管理员改成不接受新账号之后，一个停在填码页上
// 的流程不该还能把账号建出来。
func TestCompleteRegistrationRechecksMode(t *testing.T) {
	fixture := newIdentityFixture(t, googleChannel(verifierFor("g-a")))
	setPolicy(t, fixture, registration.Policy{Mode: registration.ModeInvite})
	svc := registrationServiceOn(fixture)

	verified := verifyOne(t, fixture.service, "一份令牌")
	token, err := fixture.service.pendingRegistrations.issue(identity.SourceGoogle, verified)
	if err != nil {
		t.Fatalf("记下待注册凭据失败: %v", err)
	}
	setPolicy(t, fixture, registration.Policy{Mode: registration.ModeClosed})

	_, err = completeRegistration(t, svc, token, "whatever")
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("应当被拒绝，实际 %v", err)
	}
	if !strings.Contains(err.Error(), "不接受新账号") {
		t.Fatalf("应当说清是不接受新账号，实际 %q", err.Error())
	}
	if _, found, _ := fixture.service.identities.Resolve(context.Background(), identity.SourceGoogle, "g-a"); found {
		t.Fatal("站点已关闭注册时不该登记任何主体")
	}
}

// 码填错了不该把人打回登录页：凭据在失败的兑换之后仍然可用，改一个码再提交就行。
//
// 填错码是使用者在这一页上最常犯的错，而"先取走凭据再兑换"会让每一次填错都要重新
// 登录一遍。
func TestBadCodeKeepsTheRegistrationCredential(t *testing.T) {
	fixture := newIdentityFixture(t, googleChannel(verifierFor("g-a")))
	setPolicy(t, fixture, registration.Policy{Mode: registration.ModeInvite})
	svc := registrationServiceOn(fixture)

	_, goodCode, err := fixture.registrations.Issue(context.Background(), registration.IssueParams{}, "usr_admin")
	if err != nil {
		t.Fatalf("签发邀请码失败: %v", err)
	}
	verified := verifyOne(t, fixture.service, "一份令牌")
	token, err := fixture.service.pendingRegistrations.issue(identity.SourceGoogle, verified)
	if err != nil {
		t.Fatalf("记下待注册凭据失败: %v", err)
	}

	if _, err := completeRegistration(t, svc, token, "打错的码"); err == nil {
		t.Fatal("错码应当被拒绝")
	}
	// 同一份凭据再来一次，这次给对的码——它必须还能用。
	if _, err := completeRegistration(t, svc, token, goodCode); err != nil {
		t.Fatalf("改对码之后应当成功，实际 %v", err)
	}
}

// **已登记身份不看策略**：把站点改成不接受新账号之后，既有用户照常登录。
//
// 这是本模块最要紧的一条：把老用户一起关在门外，是把一次准入收紧变成一次全站事故。
func TestPolicyDoesNotAffectRegisteredSubjects(t *testing.T) {
	fixture := newIdentityFixture(t, googleChannel(verifierFor("g-a")))

	first := admitAs(t, fixture.service, "一份令牌")
	if first.kind != admissionIssued {
		t.Fatalf("首次登录应当直接签发会话，实际 %v", first.kind)
	}

	for _, mode := range []registration.Mode{
		registration.ModeInvite,
		registration.ModeClosed,
	} {
		t.Run(string(mode), func(t *testing.T) {
			setPolicy(t, fixture, registration.Policy{Mode: mode})
			again := admitAs(t, fixture.service, "一份令牌")
			if again.kind != admissionIssued {
				t.Fatalf("已登记身份应当照常登录，实际 %v", again.kind)
			}
			if again.issued.Session.SubjectID != first.issued.Session.SubjectID {
				t.Fatal("两次登录应当落到同一个主体")
			}
		})
	}
}

// 授予失败时**身份不会被登记**，因此下一次登录会重新走一遍登记（含那次授予）。
//
// 这是三步顺序（主体 → 授予 → 身份别名）的可验证形式：反过来先写别名，一次授予
// 失败会留下一个"已登记、永远拿不到默认角色、且已经绕过闸门"的主体，而它的表现
// 只是"这个账号权限不对"。
func TestGrantFailureLeavesIdentityUnregistered(t *testing.T) {
	subjects := &failingBindStore{MutableStore: rbac.NewMemoryStore()}
	identityStore := identity.NewMemoryIdentityStore()
	sessions := identity.NewSessions(newCountingSessionStore())
	registrations := registration.New(registration.NewMemoryStore())
	if _, err := registrations.PutPolicy(context.Background(), registration.Policy{
		Mode:          registration.ModeOpen,
		DefaultRoleID: "viewer",
	}, "usr_admin"); err != nil {
		t.Fatalf("写入注册策略失败: %v", err)
	}
	service := newIdentityServiceOn(subjects, identityStore, sessions, registrations,
		[]identity.Channel{googleChannel(verifierFor("g-a"))}, zap.NewNop())

	subjects.fail = true
	if _, err := service.admit(context.Background(), identity.SourceGoogle,
		identity.VerifiedIdentity{ExternalID: "g-a"}); err == nil {
		t.Fatal("授予失败时这次登录应当失败")
	}
	if _, found, err := service.identities.Resolve(context.Background(), identity.SourceGoogle, "g-a"); err != nil {
		t.Fatalf("解析身份失败: %v", err)
	} else if found {
		t.Fatal("授予失败时不该把身份登记下来")
	}

	// 故障恢复之后重来一次：登记与授予一起补上。
	subjects.fail = false
	result, err := service.admit(context.Background(), identity.SourceGoogle,
		identity.VerifiedIdentity{ExternalID: "g-a"})
	if err != nil {
		t.Fatalf("恢复后应当成功: %v", err)
	}
	bindings, err := subjects.SubjectBindings(context.Background(), result.issued.Session.SubjectID)
	if err != nil {
		t.Fatalf("读取绑定失败: %v", err)
	}
	if len(bindings) != 1 {
		t.Fatalf("恢复后应当拿到默认角色，实际 %d 条绑定", len(bindings))
	}
}

// 渠道清单带出准入姿态：登录页据此说清"这里需要邀请码"。
func TestGetAuthMethodsCarriesRegistrationMode(t *testing.T) {
	fixture := newIdentityFixture(t, googleChannel(verifierFor("g-a")))

	call := func() identityv1.RegistrationMode {
		t.Helper()
		resp, err := fixture.service.GetAuthMethods(context.Background(),
			connect.NewRequest(&identityv1.GetAuthMethodsRequest{}))
		if err != nil {
			t.Fatalf("读取登录方式失败: %v", err)
		}
		return resp.Msg.GetRegistrationMode()
	}

	if got := call(); got != identityv1.RegistrationMode_REGISTRATION_MODE_OPEN {
		t.Errorf("缺省姿态应当是开放注册，实际 %v", got)
	}
	setPolicy(t, fixture, registration.Policy{Mode: registration.ModeInvite})
	if got := call(); got != identityv1.RegistrationMode_REGISTRATION_MODE_INVITE {
		t.Errorf("应当带出 invite，实际 %v", got)
	}
	setPolicy(t, fixture, registration.Policy{Mode: registration.ModeClosed})
	if got := call(); got != identityv1.RegistrationMode_REGISTRATION_MODE_CLOSED {
		t.Errorf("应当带出 closed，实际 %v", got)
	}
}

// 管理面写策略时的三条语义校验：角色必须存在、范围必须已登记、角色不能自我放大。
func TestPutRegistrationPolicyValidates(t *testing.T) {
	fixture := newIdentityFixture(t)
	svc := registrationServiceOn(fixture)
	ctx := interceptor.WithSubject(context.Background(), rbac.Subject{ID: "usr_admin"})

	put := func(mode identityv1.RegistrationMode, roleID, scope string) error {
		t.Helper()
		_, err := svc.PutRegistrationPolicy(ctx, connect.NewRequest(&identityv1.PutRegistrationPolicyRequest{
			Mode:          mode,
			DefaultRoleId: roleID,
			DefaultScope:  scope,
		}))
		return err
	}

	// 姿态必须显式给出：落成开放注册会让一次漏传把站点悄悄打开。
	if connect.CodeOf(put(identityv1.RegistrationMode_REGISTRATION_MODE_UNSPECIFIED, "", "")) != connect.CodeInvalidArgument {
		t.Error("未指定姿态应当被拒绝")
	}
	// 角色必须存在。
	if connect.CodeOf(put(identityv1.RegistrationMode_REGISTRATION_MODE_OPEN, "nope", "")) != connect.CodeNotFound {
		t.Error("不存在的角色应当被拒绝")
	}
	// 范围必须已登记（全局除外）。
	if connect.CodeOf(put(identityv1.RegistrationMode_REGISTRATION_MODE_OPEN, "viewer", "tenant/nope")) != connect.CodeNotFound {
		t.Error("未登记的范围应当被拒绝")
	}
	// 不能自我放大：系统管理员持有 `*`。
	if connect.CodeOf(put(identityv1.RegistrationMode_REGISTRATION_MODE_OPEN, "system.admin", "")) != connect.CodeInvalidArgument {
		t.Error("能自我放大的角色应当被拒绝")
	}
	// 只给范围不给角色是笔误。
	if connect.CodeOf(put(identityv1.RegistrationMode_REGISTRATION_MODE_OPEN, "", "tenant/acme")) != connect.CodeInvalidArgument {
		t.Error("只给范围应当被拒绝")
	}

	// 合法的一组：写下去，并留痕改动人。
	if err := put(identityv1.RegistrationMode_REGISTRATION_MODE_INVITE, "viewer", ""); err != nil {
		t.Fatalf("合法取值应当被接受，实际 %v", err)
	}
	policy, err := fixture.registrations.Policy(context.Background())
	if err != nil {
		t.Fatalf("读取策略失败: %v", err)
	}
	if policy.Mode != registration.ModeInvite || policy.DefaultRoleID != "viewer" {
		t.Fatalf("写下去的取值不对: %+v", policy)
	}
	if policy.UpdatedBySubjectID != "usr_admin" {
		t.Errorf("应当留下改动人，实际 %q", policy.UpdatedBySubjectID)
	}
}

// failingBindStore 让授予这一步可控地失败，其余照常。
type failingBindStore struct {
	rbac.MutableStore
	fail bool
}

func (s *failingBindStore) Bind(ctx context.Context, binding rbac.RoleBinding) error {
	if s.fail {
		return errors.New("授予失败")
	}
	return s.MutableStore.Bind(ctx, binding)
}
