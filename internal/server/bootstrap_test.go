package server

import (
	"context"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"

	"github.com/poetlife/aladdin/internal/config"
	"github.com/poetlife/aladdin/internal/identity"
	"github.com/poetlife/aladdin/internal/rbac"
)

// 引导的四条边界由这里固定下来。它们合起来把"引导"与"提权"分开：
// 配置是**初始化**的输入，不是判定的输入。

const (
	bootstrapSubject = "usr_bootstrap_admin"
	bootstrapEmail   = "peng@example.com"
	bootstrapScope   = rbac.Scope("tenant/acme")
)

func bootstrapConfig() config.BootstrapConfig {
	return config.BootstrapConfig{Subject: bootstrapSubject, Scope: string(bootstrapScope)}
}

// newBootstrapFixture 造两个空存储（RBAC 与身份别名），外加一个会把日志
// 记下来供断言的 logger。
func newBootstrapFixture(t *testing.T) (rbac.MutableStore, *identity.MemoryIdentityStore, *zap.Logger, *observer.ObservedLogs) {
	t.Helper()
	core, logs := observer.New(zapcore.DebugLevel)
	return rbac.NewMemoryStore(), identity.NewMemoryIdentityStore(), zap.New(core), logs
}

// registerIdentity 模拟"这个人已经登录过一次"——邮箱与主体的对应关系
// 只在那一刻才产生，而按邮箱引导依赖的正是这条已登记的记录。
func registerIdentity(t *testing.T, idents identity.IdentityStore, externalID, subjectID, email string) {
	t.Helper()
	err := idents.Put(context.Background(), identity.Identity{
		Source:     identity.SourceGoogle,
		ExternalID: externalID,
		SubjectID:  subjectID,
		Display:    email,
	})
	if err != nil {
		t.Fatalf("登记身份失败: %v", err)
	}
}

// assertNoBindings 断言存储里一条角色绑定都没有。
func assertNoBindings(t *testing.T, store rbac.MutableStore) {
	t.Helper()
	ctx := context.Background()
	roles, err := store.Roles(ctx)
	if err != nil {
		t.Fatalf("读取角色清单失败: %v", err)
	}
	for _, role := range roles {
		bindings, err := store.BindingsOfRole(ctx, role.ID)
		if err != nil {
			t.Fatalf("读取角色 %s 的绑定失败: %v", role.ID, err)
		}
		if len(bindings) != 0 {
			t.Errorf("角色 %s 上仍有绑定: %+v", role.ID, bindings)
		}
	}
}

// assertNoSubject 断言这个名字没有被登记成主体。
func assertNoSubject(t *testing.T, store rbac.MutableStore, id string) {
	t.Helper()
	if _, err := store.Subject(context.Background(), id); err == nil {
		t.Errorf("%q 被登记成了主体——它不该凭空出现", id)
	}
}

// 两个键都为空时不引导：这是默认情形，什么都不该发生。
func TestBootstrapSkippedWhenUnset(t *testing.T) {
	store, idents, _, _ := newBootstrapFixture(t)

	if err := ApplyBootstrap(context.Background(), store, idents, config.BootstrapConfig{}, zap.NewNop()); err != nil {
		t.Fatalf("引导失败: %v", err)
	}
	assertNoBindings(t, store)
}

// 存储为空且配置齐全时建立第一个管理员，并留下 warn 级痕迹。
func TestBootstrapMaterializesBinding(t *testing.T) {
	store, idents, logger, logs := newBootstrapFixture(t)

	if err := ApplyBootstrap(context.Background(), store, idents, bootstrapConfig(), logger); err != nil {
		t.Fatalf("引导失败: %v", err)
	}

	bindings, err := store.BindingsOfRole(context.Background(), rbac.RoleSystemAdmin)
	if err != nil {
		t.Fatalf("读取绑定失败: %v", err)
	}
	want := rbac.RoleBinding{SubjectID: bootstrapSubject, RoleID: rbac.RoleSystemAdmin, Scope: bootstrapScope}
	if len(bindings) != 1 || bindings[0] != want {
		t.Fatalf("绑定 = %+v，期望 %+v", bindings, want)
	}

	// 物化：写进去的是一条真实的主体与绑定，判定路径只读存储。
	if _, err := store.Subject(context.Background(), bootstrapSubject); err != nil {
		t.Errorf("引导主体没有被登记: %v", err)
	}
	if !hasWarnAbout(logs, bootstrapSubject) {
		t.Error("生效时没有以 warn 级留痕，或留痕里没有主体标识")
	}
}

// 主体标识被**原样**使用：哪怕它长得像邮箱，也不做任何推导或规范化。
//
// 这条正是"两种指认互斥、各用各的键"的意义所在：主体那一栏不嗅探 `@`，
// 所以"填了个邮箱结果被当成主体标识"这件事在形状上就不可能发生——
// 想按邮箱指认，得写进另一个键。
func TestBootstrapUsesSubjectIDVerbatim(t *testing.T) {
	store, idents, _, _ := newBootstrapFixture(t)
	cfg := config.BootstrapConfig{Subject: "zhang@example.com", Scope: string(bootstrapScope)}

	if err := ApplyBootstrap(context.Background(), store, idents, cfg, zap.NewNop()); err != nil {
		t.Fatalf("引导失败: %v", err)
	}
	subject, err := store.Subject(context.Background(), "zhang@example.com")
	if err != nil {
		t.Fatalf("引导值没有被原样用作主体标识: %v", err)
	}
	if subject.ID != "zhang@example.com" {
		t.Errorf("主体标识 = %q，期望原样保留", subject.ID)
	}
}

// 一次性：存储里已有任何绑定时不生效，且已有绑定一字不变。
func TestBootstrapOnlyOnce(t *testing.T) {
	store, idents, _, _ := newBootstrapFixture(t)
	ctx := context.Background()

	existing := rbac.RoleBinding{SubjectID: "usr_existing", RoleID: rbac.RoleViewer, Scope: "tenant/other"}
	if err := store.PutSubject(ctx, rbac.Subject{ID: "usr_existing", Type: rbac.SubjectTypeUser}); err != nil {
		t.Fatalf("登记失败: %v", err)
	}
	if err := store.Bind(ctx, existing); err != nil {
		t.Fatalf("写入已有绑定失败: %v", err)
	}

	if err := ApplyBootstrap(ctx, store, idents, bootstrapConfig(), zap.NewNop()); err != nil {
		t.Fatalf("引导失败: %v", err)
	}

	adminBindings, err := store.BindingsOfRole(ctx, rbac.RoleSystemAdmin)
	if err != nil {
		t.Fatalf("读取绑定失败: %v", err)
	}
	if len(adminBindings) != 0 {
		t.Error("存储非空时引导仍然生效了")
	}
	if _, err := store.Subject(ctx, bootstrapSubject); err == nil {
		t.Error("存储非空时仍然登记了引导主体")
	}
	viewerBindings, err := store.BindingsOfRole(ctx, rbac.RoleViewer)
	if err != nil {
		t.Fatalf("读取绑定失败: %v", err)
	}
	if len(viewerBindings) != 1 || viewerBindings[0] != existing {
		t.Errorf("已有绑定被改动了: %+v", viewerBindings)
	}
}

// 物化：生效之后把配置删掉重启，**仍然是管理员**。
//
// 这是"配置不是判定的输入"最直接的一条验证。
func TestBootstrapSurvivesConfigRemoval(t *testing.T) {
	store, idents, _, _ := newBootstrapFixture(t)
	ctx := context.Background()

	if err := ApplyBootstrap(ctx, store, idents, bootstrapConfig(), zap.NewNop()); err != nil {
		t.Fatalf("引导失败: %v", err)
	}
	// 删掉两个键之后再启动一次。
	if err := ApplyBootstrap(ctx, store, idents, config.BootstrapConfig{}, zap.NewNop()); err != nil {
		t.Fatalf("第二次启动失败: %v", err)
	}

	bindings, err := store.BindingsOfRole(ctx, rbac.RoleSystemAdmin)
	if err != nil {
		t.Fatalf("读取绑定失败: %v", err)
	}
	if len(bindings) != 1 {
		t.Fatalf("绑定条数 = %d，期望 1——删掉配置不该撤销绑定", len(bindings))
	}
}

// 引导主体的默认作用域被设为引导作用域。
//
// 少了这一步，按"先登录读出主体标识、再填配置"的操作步骤建立出来的管理员，
// 默认作用域是全局，而绑定在更窄的作用域上：他登录后什么都看不到。
func TestBootstrapSetsDefaultScope(t *testing.T) {
	store, idents, _, _ := newBootstrapFixture(t)
	ctx := context.Background()

	// 模拟"这个人已经登录过一次"：主体已存在，类型是人类用户，默认作用域为空。
	if err := store.PutSubject(ctx, rbac.Subject{ID: bootstrapSubject, Type: rbac.SubjectTypeUser}); err != nil {
		t.Fatalf("登记失败: %v", err)
	}
	if err := ApplyBootstrap(ctx, store, idents, bootstrapConfig(), zap.NewNop()); err != nil {
		t.Fatalf("引导失败: %v", err)
	}

	subject, err := store.Subject(ctx, bootstrapSubject)
	if err != nil {
		t.Fatalf("读取主体失败: %v", err)
	}
	if subject.DefaultScope != bootstrapScope {
		t.Errorf("默认作用域 = %q，期望 %q", subject.DefaultScope, bootstrapScope)
	}
	if subject.Type != rbac.SubjectTypeUser {
		t.Errorf("主体类型被改成了 %q，引导不该重写它", subject.Type)
	}
}

// 按邮箱指认：恰好命中一个已登记身份时取用它的主体，且作用域写成 <global>
// 时落的是全局作用域。
func TestBootstrapResolvesEmailToSubject(t *testing.T) {
	store, idents, logger, logs := newBootstrapFixture(t)
	ctx := context.Background()
	registerIdentity(t, idents, "google-sub-1", bootstrapSubject, bootstrapEmail)

	cfg := config.BootstrapConfig{Email: bootstrapEmail, Scope: rbac.GlobalScopeLiteral}
	if err := ApplyBootstrap(ctx, store, idents, cfg, logger); err != nil {
		t.Fatalf("引导失败: %v", err)
	}

	bindings, err := store.BindingsOfRole(ctx, rbac.RoleSystemAdmin)
	if err != nil {
		t.Fatalf("读取绑定失败: %v", err)
	}
	want := rbac.RoleBinding{SubjectID: bootstrapSubject, RoleID: rbac.RoleSystemAdmin, Scope: rbac.GlobalScope}
	if len(bindings) != 1 || bindings[0] != want {
		t.Fatalf("绑定 = %+v，期望 %+v", bindings, want)
	}

	// 邮箱本身不得成为主体：解析的产物是主体标识，邮箱只在这条路径上出现过一次。
	assertNoSubject(t, store, bootstrapEmail)

	// 留痕里要能回答"当初是谁"——主体标识是不透明的，邮箱是唯一的线索。
	if !hasWarnField(logs, "email", bootstrapEmail) {
		t.Error("留痕里没有邮箱，事后无从回答'这个管理员当初是谁'")
	}
}

// 邮箱没有命中任何已登记身份时拒绝启动，且不留下任何东西。
//
// 映射是**登录时**产生的，所以还没登录过就按邮箱引导注定查不到。这里把
// "不得凭空造主体"钉死：写下邮箱不等于授权给一个还不存在的身份。
func TestBootstrapEmailWithoutMatchFails(t *testing.T) {
	store, idents, _, _ := newBootstrapFixture(t)
	cfg := config.BootstrapConfig{Email: bootstrapEmail, Scope: rbac.GlobalScopeLiteral}

	if err := ApplyBootstrap(context.Background(), store, idents, cfg, zap.NewNop()); err == nil {
		t.Fatal("没有命中任何已登记身份，引导却成功了")
	}
	assertNoBindings(t, store)
	assertNoSubject(t, store, bootstrapEmail)
}

// 同一个邮箱挂在两个身份上时命中不唯一，必须拒绝启动而不是挑一个。
//
// 本库刻意允许同一个邮箱字符串属于两个身份（见 internal/identity 的测试），
// 所以这条是真实可能发生的，不是防御性代码。挑一个等于把一次配置错误
// 变成一次无声授权。
func TestBootstrapAmbiguousEmailFails(t *testing.T) {
	store, idents, _, _ := newBootstrapFixture(t)
	ctx := context.Background()
	registerIdentity(t, idents, "google-sub-1", "usr_a", bootstrapEmail)
	registerIdentity(t, idents, "google-sub-2", "usr_b", bootstrapEmail)

	cfg := config.BootstrapConfig{Email: bootstrapEmail, Scope: rbac.GlobalScopeLiteral}
	if err := ApplyBootstrap(ctx, store, idents, cfg, zap.NewNop()); err == nil {
		t.Fatal("命中两个身份，引导却成功了——它挑了一个")
	}
	assertNoBindings(t, store)
	assertNoSubject(t, store, "usr_a")
	assertNoSubject(t, store, "usr_b")
}

// hasWarnAbout 报告日志里有没有一条含该主体标识的 warn 级记录。
func hasWarnAbout(logs *observer.ObservedLogs, subjectID string) bool {
	return hasWarnField(logs, "subject_id", subjectID)
}

// hasWarnField 报告日志里有没有一条 warn 级记录带着给定的字段取值。
func hasWarnField(logs *observer.ObservedLogs, key, value string) bool {
	for _, entry := range logs.All() {
		if entry.Level != zapcore.WarnLevel {
			continue
		}
		for _, field := range entry.Context {
			if field.Key == key && field.String == value {
				return true
			}
		}
	}
	return false
}
