package server

import (
	"context"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"

	"github.com/poetlife/aladdin/internal/config"
	"github.com/poetlife/aladdin/internal/rbac"
)

// 引导的四条边界由这里固定下来。它们合起来把"引导"与"提权"分开：
// 配置是**初始化**的输入，不是判定的输入。

const (
	bootstrapSubject = "usr_bootstrap_admin"
	bootstrapScope   = rbac.Scope("tenant/acme")
)

func bootstrapConfig() config.BootstrapConfig {
	return config.BootstrapConfig{Subject: bootstrapSubject, Scope: string(bootstrapScope)}
}

// newBootstrapFixture 造一个空存储，外加一个会把日志记下来供断言的 logger。
func newBootstrapFixture(t *testing.T) (rbac.MutableStore, *zap.Logger, *observer.ObservedLogs) {
	t.Helper()
	core, logs := observer.New(zapcore.DebugLevel)
	return rbac.NewMemoryStore(), zap.New(core), logs
}

// 两个键都为空时不引导：这是默认情形，什么都不该发生。
func TestBootstrapSkippedWhenUnset(t *testing.T) {
	store, _, _ := newBootstrapFixture(t)

	if err := ApplyBootstrap(context.Background(), store, config.BootstrapConfig{}, zap.NewNop()); err != nil {
		t.Fatalf("引导失败: %v", err)
	}
	bindings, err := store.BindingsOfRole(context.Background(), rbac.RoleSystemAdmin)
	if err != nil {
		t.Fatalf("读取绑定失败: %v", err)
	}
	if len(bindings) != 0 {
		t.Errorf("未配置引导却建立了绑定: %+v", bindings)
	}
}

// 存储为空且配置齐全时建立第一个管理员，并留下 warn 级痕迹。
func TestBootstrapMaterializesBinding(t *testing.T) {
	store, logger, logs := newBootstrapFixture(t)

	if err := ApplyBootstrap(context.Background(), store, bootstrapConfig(), logger); err != nil {
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

// 引导值原样使用：不按邮箱推导，也不做任何规范化。
func TestBootstrapUsesSubjectIDVerbatim(t *testing.T) {
	store, _, _ := newBootstrapFixture(t)
	cfg := config.BootstrapConfig{Subject: "zhang@example.com", Scope: string(bootstrapScope)}

	if err := ApplyBootstrap(context.Background(), store, cfg, zap.NewNop()); err != nil {
		t.Fatalf("引导失败: %v", err)
	}
	// 取值必须被**原样**当作主体标识：本模块不做邮箱到主体的映射，
	// 哪怕运维写的是一个邮箱。真正要防的是"系统自己去按邮箱推导"，
	// 那会让"配置里能填什么"变成一条隐式的权限来源。
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
	store, _, _ := newBootstrapFixture(t)
	ctx := context.Background()

	existing := rbac.RoleBinding{SubjectID: "usr_existing", RoleID: rbac.RoleViewer, Scope: "tenant/other"}
	if err := store.PutSubject(ctx, rbac.Subject{ID: "usr_existing", Type: rbac.SubjectTypeUser}); err != nil {
		t.Fatalf("登记失败: %v", err)
	}
	if err := store.Bind(ctx, existing); err != nil {
		t.Fatalf("写入已有绑定失败: %v", err)
	}

	if err := ApplyBootstrap(ctx, store, bootstrapConfig(), zap.NewNop()); err != nil {
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
	store, _, _ := newBootstrapFixture(t)
	ctx := context.Background()

	if err := ApplyBootstrap(ctx, store, bootstrapConfig(), zap.NewNop()); err != nil {
		t.Fatalf("引导失败: %v", err)
	}
	// 删掉两个键之后再启动一次。
	if err := ApplyBootstrap(ctx, store, config.BootstrapConfig{}, zap.NewNop()); err != nil {
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
// 少了这一步，按文档"先登录读出主体标识、再填配置"的操作步骤建立出来的
// 管理员，默认作用域是全局，而绑定在更窄的作用域上：他登录后什么都看不到。
func TestBootstrapSetsDefaultScope(t *testing.T) {
	store, _, _ := newBootstrapFixture(t)
	ctx := context.Background()

	// 模拟"这个人已经登录过一次"：主体已存在，类型是人类用户，默认作用域为空。
	if err := store.PutSubject(ctx, rbac.Subject{ID: bootstrapSubject, Type: rbac.SubjectTypeUser}); err != nil {
		t.Fatalf("登记失败: %v", err)
	}
	if err := ApplyBootstrap(ctx, store, bootstrapConfig(), zap.NewNop()); err != nil {
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

// hasWarnAbout 报告日志里有没有一条含该主体标识的 warn 级记录。
func hasWarnAbout(logs *observer.ObservedLogs, subjectID string) bool {
	for _, entry := range logs.All() {
		if entry.Level != zapcore.WarnLevel {
			continue
		}
		for _, field := range entry.Context {
			if field.Key == "subject_id" && field.String == subjectID {
				return true
			}
		}
	}
	return false
}
