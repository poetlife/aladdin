package server

import (
	"context"
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

// testClock 是一个可以拨动的时间源。
type testClock struct{ at time.Time }

func newTestClock() *testClock { return &testClock{at: time.Now()} }

func (c *testClock) now() time.Time { return c.at }

func (c *testClock) advance(d time.Duration) { c.at = c.at.Add(d) }

func testSubject(id string) rbac.Subject {
	return rbac.Subject{ID: id, Type: rbac.SubjectTypeUser}
}

// ------------------------------------------------------------ 记录表本身

func TestDeviceLoginDeliversApprovedSubjectExactlyOnce(t *testing.T) {
	clock := newTestClock()
	logins := newDeviceLogins(clock.now)

	issued, err := logins.start()
	if err != nil {
		t.Fatalf("发起失败: %v", err)
	}
	if issued.deviceCode == "" || issued.userCode == "" {
		t.Fatal("发起没有给出设备码或短码")
	}

	// 未批准时只报"待批准"，永远取不到凭证。
	if state, _ := logins.poll(issued.deviceCode); state != deviceLoginPending {
		t.Fatalf("未批准时应为待批准，实际 %v", state)
	}

	if !logins.approve(issued.userCode, testSubject("usr_a")) {
		t.Fatal("批准未生效")
	}

	state, subject := logins.poll(issued.deviceCode)
	if state != deviceLoginApproved {
		t.Fatalf("批准后应为已批准，实际 %v", state)
	}
	if subject.ID != "usr_a" {
		t.Fatalf("交付的主体不是批准者：%q", subject.ID)
	}

	// 第二次轮询不再交付——这是"只交付一次"的落点。
	if state, _ := logins.poll(issued.deviceCode); state != deviceLoginNone {
		t.Fatalf("已交付的设备码应查不到，实际 %v", state)
	}
}

// 两个并发轮询只允许一个拿到批准结论。
//
// 分开写"查到已批准"与"取走"的话，两个并发轮询会各查到一次已批准，然后
// 各领走一份会话。
func TestDeviceLoginConcurrentPollsDeliverOnce(t *testing.T) {
	logins := newDeviceLogins(time.Now)
	issued, err := logins.start()
	if err != nil {
		t.Fatalf("发起失败: %v", err)
	}
	if !logins.approve(issued.userCode, testSubject("usr_a")) {
		t.Fatal("批准未生效")
	}

	const pollers = 16
	var (
		wg        sync.WaitGroup
		mu        sync.Mutex
		delivered int
	)
	for range pollers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if state, _ := logins.poll(issued.deviceCode); state == deviceLoginApproved {
				mu.Lock()
				delivered++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if delivered != 1 {
		t.Fatalf("批准结论被交付了 %d 次，应为 1 次", delivered)
	}
}

func TestDeviceLoginDeny(t *testing.T) {
	logins := newDeviceLogins(time.Now)
	issued, err := logins.start()
	if err != nil {
		t.Fatalf("发起失败: %v", err)
	}

	if !logins.deny(issued.userCode) {
		t.Fatal("拒绝未生效")
	}
	// 拒绝的记录不消费：调用方多读到几次同一个结论，比只读到一次更可靠。
	for range 2 {
		if state, _ := logins.poll(issued.deviceCode); state != deviceLoginDenied {
			t.Fatalf("拒绝后应为已拒绝，实际 %v", state)
		}
	}
	if logins.approve(issued.userCode, testSubject("usr_a")) {
		t.Fatal("已拒绝的短码不该还能被批准")
	}
}

func TestDeviceLoginUnknownCodesAreNotDeliverable(t *testing.T) {
	logins := newDeviceLogins(time.Now)

	if state, _ := logins.poll("not-a-device-code"); state != deviceLoginNone {
		t.Fatalf("未知设备码应为查不到，实际 %v", state)
	}
	if logins.approve("BCDFGHJK", testSubject("usr_a")) {
		t.Fatal("未知短码不该能批准")
	}
	if logins.deny("BCDFGHJK") {
		t.Fatal("未知短码不该能拒绝")
	}
	if logins.approve("", testSubject("usr_a")) || logins.deny("") {
		t.Fatal("空短码不该能批准或拒绝")
	}
}

func TestDeviceLoginExpires(t *testing.T) {
	clock := newTestClock()
	logins := newDeviceLogins(clock.now)
	issued, err := logins.start()
	if err != nil {
		t.Fatalf("发起失败: %v", err)
	}

	clock.advance(deviceLoginTTL)

	if state, _ := logins.poll(issued.deviceCode); state != deviceLoginNone {
		t.Fatalf("过期后应为查不到，实际 %v", state)
	}
	if logins.approve(issued.userCode, testSubject("usr_a")) {
		t.Fatal("过期的短码不该能批准")
	}
}

// 第一次批准决定了归属，后来者不得改写它。
func TestDeviceLoginApproveIsNotOverwrittenByAnotherSubject(t *testing.T) {
	logins := newDeviceLogins(time.Now)
	issued, err := logins.start()
	if err != nil {
		t.Fatalf("发起失败: %v", err)
	}

	if !logins.approve(issued.userCode, testSubject("usr_a")) {
		t.Fatal("首次批准未生效")
	}
	if logins.approve(issued.userCode, testSubject("usr_b")) {
		t.Fatal("另一个人不该能改写已经决定的归属")
	}
	// 同一个人重复按（页面重试、双击）幂等成功。
	if !logins.approve(issued.userCode, testSubject("usr_a")) {
		t.Fatal("同一个人重复批准应幂等成功")
	}

	if _, subject := logins.poll(issued.deviceCode); subject.ID != "usr_a" {
		t.Fatalf("交付的主体被改写成了 %q", subject.ID)
	}
}

// 短码是给人打的：大小写、连字符、空格都随人怎么写。
func TestDeviceLoginUserCodeInputIsNormalized(t *testing.T) {
	logins := newDeviceLogins(time.Now)
	issued, err := logins.start()
	if err != nil {
		t.Fatalf("发起失败: %v", err)
	}

	// 发起时给出的是给人看的形式（中间一个连字符）。
	display := formatUserCode(issued.userCode)
	if display == issued.userCode {
		t.Fatalf("给人看的短码应带连字符：%q", display)
	}

	// 用小写、去掉连字符、前后加空格的写法批准，应当命中同一条记录。
	typed := "  " + strings.ToLower(strings.ReplaceAll(display, "-", "")) + "\t"
	if !logins.approve(typed, testSubject("usr_a")) {
		t.Fatalf("规范化后的短码没有命中：%q", typed)
	}
}

func TestDeviceLoginUserCodesAreUnique(t *testing.T) {
	logins := newDeviceLogins(time.Now)

	const n = 512
	seen := make(map[string]bool, n)
	for range n {
		issued, err := logins.start()
		if err != nil {
			t.Fatalf("发起失败: %v", err)
		}
		if seen[issued.userCode] {
			t.Fatalf("短码重复：%q", issued.userCode)
		}
		seen[issued.userCode] = true
	}
}

// 上界必须成立：发起接口谁都能调，一张没有上界的表会被打进内存。
func TestDeviceLoginTableStaysBounded(t *testing.T) {
	clock := newTestClock()
	logins := newDeviceLogins(clock.now)

	for range deviceLoginMax + 32 {
		if _, err := logins.start(); err != nil {
			t.Fatalf("发起失败: %v", err)
		}
	}

	logins.mu.Lock()
	defer logins.mu.Unlock()
	if len(logins.byDeviceCode) > deviceLoginMax {
		t.Fatalf("表超过了上界：%d > %d", len(logins.byDeviceCode), deviceLoginMax)
	}
	if len(logins.byUserCode) != len(logins.byDeviceCode) {
		t.Fatalf("两张表失去了同步：短码 %d 条、设备码 %d 条",
			len(logins.byUserCode), len(logins.byDeviceCode))
	}
}

// ------------------------------------------------------------ 接口层

// startDeviceLogin 走一次发起，返回设备码与给用户看的短码。
func startDeviceLogin(t *testing.T, service *IdentityService) (deviceCode, userCode string) {
	t.Helper()
	resp, err := service.StartDeviceLogin(context.Background(),
		connect.NewRequest(&identityv1.StartDeviceLoginRequest{}))
	if err != nil {
		t.Fatalf("发起设备码登录失败: %v", err)
	}
	if resp.Msg.GetVerificationUri() == "" {
		t.Fatal("发起没有给出批准页地址")
	}
	if resp.Msg.GetIntervalSeconds() <= 0 {
		t.Fatal("发起没有给出轮询间隔")
	}
	if resp.Msg.GetExpiresAt() == "" {
		t.Fatal("发起没有给出有效期")
	}
	return resp.Msg.GetDeviceCode(), resp.Msg.GetUserCode()
}

func pollDeviceLogin(t *testing.T, service *IdentityService, deviceCode string) *identityv1.PollDeviceLoginResponse {
	t.Helper()
	resp, err := service.PollDeviceLogin(context.Background(),
		connect.NewRequest(&identityv1.PollDeviceLoginRequest{DeviceCode: deviceCode}))
	if err != nil {
		t.Fatalf("轮询设备码登录失败: %v", err)
	}
	return resp.Msg
}

// 交付的会话属于**批准者**，且不登记主体、不新增身份。
//
// 断言"同一个主体"就是"没有登记新主体"：解析入口若被走到，它会给这次登录
// 分配一个新主体，交付的会话就不可能是批准者的那个。
func TestDeviceLoginDeliversSessionForApprover(t *testing.T) {
	fixture := newIdentityFixture(t, googleChannel(fakeVerifier{
		identity: identity.VerifiedIdentity{ExternalID: "google-sub-a", Display: "a@example.com"},
	}))
	login := fixture.loginAs(t)
	approver, err := fixture.sessions.Verify(context.Background(), login.GetAccessToken())
	if err != nil {
		t.Fatalf("会话不可用: %v", err)
	}
	before, err := fixture.identityStore.ListBySubject(context.Background(), approver.ID)
	if err != nil {
		t.Fatalf("读渠道清单失败: %v", err)
	}

	deviceCode, userCode := startDeviceLogin(t, fixture.service)

	if state := pollDeviceLogin(t, fixture.service, deviceCode).GetState(); state != identityv1.DeviceLoginState_DEVICE_LOGIN_STATE_PENDING {
		t.Fatalf("未批准时应为待批准，实际 %v", state)
	}

	ctx := caller(t, fixture.sessions, login.GetAccessToken())
	if _, err := fixture.service.ApproveDeviceLogin(ctx,
		connect.NewRequest(&identityv1.ApproveDeviceLoginRequest{UserCode: userCode})); err != nil {
		t.Fatalf("批准失败: %v", err)
	}

	resp := pollDeviceLogin(t, fixture.service, deviceCode)
	if resp.GetState() != identityv1.DeviceLoginState_DEVICE_LOGIN_STATE_APPROVED {
		t.Fatalf("批准后应为已批准，实际 %v", resp.GetState())
	}
	if resp.GetAccessToken() == "" || resp.GetExpiresAt() == "" {
		t.Fatal("已批准却没有交出凭证")
	}

	delivered, err := fixture.sessions.Verify(context.Background(), resp.GetAccessToken())
	if err != nil {
		t.Fatalf("交付的会话不可用: %v", err)
	}
	if delivered.ID != approver.ID {
		t.Fatalf("交付的会话属于 %q，应为批准者 %q", delivered.ID, approver.ID)
	}
	// 作用域是批准者主体既有的默认作用域快照，不随这次登录新算。
	if delivered.DefaultScope != approver.DefaultScope {
		t.Fatalf("交付的作用域是 %q，应为 %q", delivered.DefaultScope, approver.DefaultScope)
	}

	after, err := fixture.identityStore.ListBySubject(context.Background(), approver.ID)
	if err != nil {
		t.Fatalf("读渠道清单失败: %v", err)
	}
	if len(after) != len(before) {
		t.Fatalf("渠道清单变了：%d → %d 条", len(before), len(after))
	}

	// 再轮询同一份设备码：不再交付。
	if state := pollDeviceLogin(t, fixture.service, deviceCode).GetState(); state != identityv1.DeviceLoginState_DEVICE_LOGIN_STATE_EXPIRED {
		t.Fatalf("已交付的设备码应为过期态，实际 %v", state)
	}
}

func TestDeviceLoginDenyIsReportedToTheCLI(t *testing.T) {
	fixture := newIdentityFixture(t, googleChannel(fakeVerifier{
		identity: identity.VerifiedIdentity{ExternalID: "google-sub-a"},
	}))
	login := fixture.loginAs(t)
	deviceCode, userCode := startDeviceLogin(t, fixture.service)

	ctx := caller(t, fixture.sessions, login.GetAccessToken())
	if _, err := fixture.service.DenyDeviceLogin(ctx,
		connect.NewRequest(&identityv1.DenyDeviceLoginRequest{UserCode: userCode})); err != nil {
		t.Fatalf("拒绝失败: %v", err)
	}

	resp := pollDeviceLogin(t, fixture.service, deviceCode)
	if resp.GetState() != identityv1.DeviceLoginState_DEVICE_LOGIN_STATE_DENIED {
		t.Fatalf("拒绝后应为已拒绝，实际 %v", resp.GetState())
	}
	if resp.GetAccessToken() != "" {
		t.Fatal("被拒绝的登录交出了凭证")
	}
}

// 未认证不能批准，也不能拒绝。
func TestDeviceLoginApprovalRequiresAuthentication(t *testing.T) {
	fixture := newIdentityFixture(t)
	_, userCode := startDeviceLogin(t, fixture.service)

	_, err := fixture.service.ApproveDeviceLogin(context.Background(),
		connect.NewRequest(&identityv1.ApproveDeviceLoginRequest{UserCode: userCode}))
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("未认证批准应返回未认证，实际 %v", err)
	}
	_, err = fixture.service.DenyDeviceLogin(context.Background(),
		connect.NewRequest(&identityv1.DenyDeviceLoginRequest{UserCode: userCode}))
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("未认证拒绝应返回未认证，实际 %v", err)
	}
}

// 无效短码的结论统一：不区分从未存在、已过期、已交付、已被别人批准。
func TestDeviceLoginInvalidUserCodeGivesOneAnswer(t *testing.T) {
	fixture := newIdentityFixture(t, googleChannel(fakeVerifier{
		identity: identity.VerifiedIdentity{ExternalID: "google-sub-a"},
	}))
	login := fixture.loginAs(t)
	ctx := caller(t, fixture.sessions, login.GetAccessToken())

	_, err := fixture.service.ApproveDeviceLogin(ctx,
		connect.NewRequest(&identityv1.ApproveDeviceLoginRequest{UserCode: "ZZZZZZZZ"}))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("无效短码应返回前置条件不满足，实际 %v", err)
	}
}

func TestPollDeviceLoginRejectsEmptyDeviceCode(t *testing.T) {
	fixture := newIdentityFixture(t)

	_, err := fixture.service.PollDeviceLogin(context.Background(),
		connect.NewRequest(&identityv1.PollDeviceLoginRequest{}))
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("空设备码应返回参数不合法，实际 %v", err)
	}
}

// 未配置对外地址时这条路径整体缺席：说"这条路没开"，不说"设备码无效"。
func TestDeviceLoginDisabledWithoutPublicURL(t *testing.T) {
	fixture := newIdentityFixture(t)
	subjects := rbac.NewMemoryStore()
	identityStore := identity.NewMemoryIdentityStore()
	sessions := identity.NewSessions(newCountingSessionStore())
	service := NewIdentityService(subjects, rbac.NewEngine(subjects, zap.NewNop(), nil), IdentityDeps{
		Machine:         interceptor.NewTokenAuthenticator(),
		Identities:      identity.NewIdentities(identityStore, subjects),
		Sessions:        sessions,
		Channels:        identity.NewRegistry(),
		Logger:          zap.NewNop(),
		PendingBindings: newPendingBindings(time.Now, false),
		DeviceLogins:    newDeviceLogins(time.Now),
		// 关键：没有批准页地址。
		DeviceApprovalURL: "",
		LifecycleGate:     &subjectLifecycleGate{},
	})

	_, err := service.StartDeviceLogin(context.Background(),
		connect.NewRequest(&identityv1.StartDeviceLoginRequest{}))
	if connect.CodeOf(err) != connect.CodeUnimplemented {
		t.Fatalf("未启用时应返回未实现，实际 %v", err)
	}

	// 四个方法都要说"这条路没开"，一个都不能说成"设备码无效"：后者会让人去查
	// 终端拿的码，而真正的原因在服务端配置上。
	disabled := []struct {
		name string
		call func() error
	}{
		{"发起", func() error {
			_, err := service.StartDeviceLogin(context.Background(),
				connect.NewRequest(&identityv1.StartDeviceLoginRequest{}))
			return err
		}},
		{"轮询", func() error {
			_, err := service.PollDeviceLogin(context.Background(),
				connect.NewRequest(&identityv1.PollDeviceLoginRequest{DeviceCode: "device-code"}))
			return err
		}},
		{"批准", func() error {
			_, err := service.ApproveDeviceLogin(context.Background(),
				connect.NewRequest(&identityv1.ApproveDeviceLoginRequest{UserCode: "BCDF-GHJK"}))
			return err
		}},
		{"拒绝", func() error {
			_, err := service.DenyDeviceLogin(context.Background(),
				connect.NewRequest(&identityv1.DenyDeviceLoginRequest{UserCode: "BCDF-GHJK"}))
			return err
		}},
	}
	for _, call := range disabled {
		if got := connect.CodeOf(call.call()); got != connect.CodeUnimplemented {
			t.Errorf("%s：未启用时应返回未实现，实际 %v", call.name, got)
		}
	}

	methods, err := service.GetAuthMethods(context.Background(),
		connect.NewRequest(&identityv1.GetAuthMethodsRequest{}))
	if err != nil {
		t.Fatalf("查询登录方式失败: %v", err)
	}
	if methods.Msg.GetDeviceLoginEnabled() {
		t.Fatal("未启用时不该宣称命令行登录可用")
	}

	// 已启用的那个认证面则应当宣称可用。
	enabled, err := fixture.service.GetAuthMethods(context.Background(),
		connect.NewRequest(&identityv1.GetAuthMethodsRequest{}))
	if err != nil {
		t.Fatalf("查询登录方式失败: %v", err)
	}
	if !enabled.Msg.GetDeviceLoginEnabled() {
		t.Fatal("已启用时应宣称命令行登录可用")
	}
}

// 短码、设备码与交付的令牌都不得进日志。
func TestDeviceLoginLogsContainNoSecrets(t *testing.T) {
	subjects := rbac.NewMemoryStore()
	identityStore := identity.NewMemoryIdentityStore()
	sessions := identity.NewSessions(newCountingSessionStore())
	core, logs := observer.New(zapcore.DebugLevel)
	service := newIdentityServiceOn(subjects, identityStore, sessions, nil, zap.New(core))

	deviceCode, userCode := startDeviceLogin(t, service)

	// 造一个已认证的调用方：先登记一个主体并签发会话。
	subject, err := identity.NewIdentities(identityStore, subjects).
		ResolveOrRegister(context.Background(), identity.SourceGoogle, "google-sub-a", "a@example.com")
	if err != nil {
		t.Fatalf("登记主体失败: %v", err)
	}
	issued, err := sessions.Issue(context.Background(), subject)
	if err != nil {
		t.Fatalf("签发会话失败: %v", err)
	}
	ctx := caller(t, sessions, issued.Token)

	if _, err := service.ApproveDeviceLogin(ctx,
		connect.NewRequest(&identityv1.ApproveDeviceLoginRequest{UserCode: userCode})); err != nil {
		t.Fatalf("批准失败: %v", err)
	}
	delivered := pollDeviceLogin(t, service, deviceCode)
	if delivered.GetAccessToken() == "" {
		t.Fatal("已批准却没有交出凭证")
	}

	blob := logsBlob(logs)
	for _, secret := range []string{
		// 大小写与连字符都不论：日志里出现的任何形态都不行。
		userCode,
		normalizeUserCode(userCode),
		deviceCode,
		issued.Token,
		delivered.GetAccessToken(),
	} {
		if secret == "" {
			continue
		}
		if strings.Contains(blob, secret) {
			t.Fatalf("日志里出现了凭证：%q", secret)
		}
	}
}

// logsBlob 把捕获到的日志拼成一整块文本，便于做"有没有出现某个串"的判断。
func logsBlob(logs *observer.ObservedLogs) string {
	var b strings.Builder
	for _, entry := range logs.All() {
		b.WriteString(entry.Message)
		b.WriteByte(' ')
		for _, field := range entry.Context {
			b.WriteString(field.Key)
			b.WriteByte('=')
			b.WriteString(field.String)
			b.WriteByte(' ')
		}
		b.WriteByte('\n')
	}
	return b.String()
}
