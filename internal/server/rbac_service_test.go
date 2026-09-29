package server

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"go.uber.org/zap"

	rbacv1 "github.com/poetlife/aladdin/api/gen/aladdin/rbac/v1"
	"github.com/poetlife/aladdin/internal/rbac"
)

// signalingStore 让"授予角色正处在临界区里"成为可观察的事实。
//
// AssignRole 在**取得生命周期锁之后**才读 SubjectBindings，因此读到它就说明
// 锁已被持有；再让它停在那里等放行，就能稳定地观察"锁被持有期间别人进不来"。
// 包住接口而不是逐个实现方法：需要被观察的只有这一个入口。
type signalingStore struct {
	rbac.MutableStore
	entered     chan struct{}
	release     chan struct{}
	enteredOnce sync.Once
	releaseOnce sync.Once
}

func newSignalingStore(inner rbac.MutableStore) *signalingStore {
	return &signalingStore{
		MutableStore: inner,
		entered:      make(chan struct{}),
		release:      make(chan struct{}),
	}
}

// waitUntilEntered 等到授予进入临界区。
func (s *signalingStore) waitUntilEntered() <-chan struct{} { return s.entered }

// allow 放行，使授予可以走完。
func (s *signalingStore) allow() { s.releaseOnce.Do(func() { close(s.release) }) }

func (s *signalingStore) SubjectBindings(ctx context.Context, subjectID string) ([]rbac.RoleBinding, error) {
	s.enteredOnce.Do(func() { close(s.entered) })
	<-s.release
	return s.MutableStore.SubjectBindings(ctx, subjectID)
}

// 授予角色必须在生命周期锁**之内**读"这个主体有没有角色绑定"并写入。
//
// 空主体认领靠这把锁与授予串行：少了它，认领可能在"确认没有角色"之后、
// 移动身份之前被插进一次授予，留下"身份已移走、角色还在原主体"的搁浅。
// 也就是说，锁是认领唯一的正确性依据——这条测试断言它确实被取到了。
//
// 它走的是**生产路径**（RBACService.AssignRole），不是直接调存储：直接调
// 存储的写法下，"AssignRole 忘了取锁"不会有任何测试失败（见
// docs/design/identity/identity-linking.md 的可验证性表）。
func TestAssignRoleHoldsLifecycleGate(t *testing.T) {
	ctx := context.Background()
	store := rbac.NewMemoryStore()
	if err := store.PutRole(ctx, rbac.RoleDefinition{ID: rbac.RoleViewer}); err != nil {
		t.Fatalf("写入角色失败: %v", err)
	}
	const subjectID = "usr_a"
	if err := store.PutSubject(ctx, rbac.Subject{ID: subjectID}); err != nil {
		t.Fatalf("登记主体失败: %v", err)
	}

	sig := newSignalingStore(store)
	defer sig.allow()

	gate := &subjectLifecycleGate{}
	svc := NewRBACService(sig, rbac.NewEngine(store, zap.NewNop(), nil), gate)

	granted := make(chan error, 1)
	go func() {
		_, err := svc.AssignRole(ctx, connect.NewRequest(&rbacv1.AssignRoleRequest{
			SubjectId: subjectID,
			RoleId:    rbac.RoleViewer,
			Grant:     true,
		}))
		granted <- err
	}()

	select {
	case <-sig.waitUntilEntered():
	case <-time.After(2 * time.Second):
		t.Fatal("AssignRole 没有走到读取角色绑定的那一步")
	}

	// 授予此刻停在临界区里。第二次获取必须进不来。
	acquired := make(chan struct{})
	go func() {
		unlock := gate.lock()
		close(acquired)
		unlock()
	}()
	select {
	case <-acquired:
		t.Fatal("AssignRole 在读取角色绑定时没有持有生命周期锁")
	case <-time.After(20 * time.Millisecond):
	}

	sig.allow()
	if err := <-granted; err != nil {
		t.Fatalf("授予角色失败: %v", err)
	}
	select {
	case <-acquired:
	case <-time.After(time.Second):
		t.Fatal("AssignRole 结束后生命周期锁没有释放")
	}
}

// ListRoles 不按请求范围过滤——角色定义是全局的，全库只有一份。
//
// 请求里的 scope 只参与鉴权（能不能在这个范围读角色），不参与结果裁剪。
// 这条约定容易在"顺手加个过滤"的改动里被打破，而打破它的表现是：管理员
// 切了管理范围，角色列表凭空少了一半，却没有任何一条文案解释为什么。
// 因此它由测试固定，而不是只写在 proto 注释里。
func TestListRolesReturnsAllRolesRegardlessOfScope(t *testing.T) {
	ctx := context.Background()
	store := rbac.NewMemoryStore()
	svc := NewRBACService(store, rbac.NewEngine(store, zap.NewNop(), nil), &subjectLifecycleGate{})

	idsAt := func(scope string) []string {
		t.Helper()
		resp, err := svc.ListRoles(ctx, connect.NewRequest(&rbacv1.ListRolesRequest{Scope: scope}))
		if err != nil {
			t.Fatalf("ListRoles(scope=%q) 失败: %v", scope, err)
		}
		ids := make([]string, 0, len(resp.Msg.GetRoles()))
		for _, r := range resp.Msg.GetRoles() {
			ids = append(ids, r.GetId())
		}
		return ids
	}

	global := idsAt("")
	scoped := idsAt("tenant/acme")

	// 两次调用的顺序由 rbac.SortRoles 定，是同一份规则，因此可以直接比。
	if len(global) != len(scoped) {
		t.Fatalf("全局范围与 tenant/acme 拿到的角色数不同：%d vs %d", len(global), len(scoped))
	}
	for i := range global {
		if global[i] != scoped[i] {
			t.Fatalf("两个范围下第 %d 个角色不同：%q vs %q", i, global[i], scoped[i])
		}
	}

	// 与存储里的全集比对：确认不是"两个范围都返回了同一个子集"。
	all, err := store.Roles(ctx)
	if err != nil {
		t.Fatalf("读取角色失败: %v", err)
	}
	if len(global) != len(all) {
		t.Fatalf("ListRoles 返回 %d 个角色，存储里有 %d 个——列表被裁剪了", len(global), len(all))
	}
}

// newTestRBACService 构造一个跑在内存存储上的管理面服务。
func newTestRBACService(store rbac.MutableStore) *RBACService {
	return NewRBACService(store, rbac.NewEngine(store, zap.NewNop(), nil), &subjectLifecycleGate{})
}

// putScope 登记一个范围，失败即终止。
func putScope(t *testing.T, ctx context.Context, svc *RBACService, path string) {
	t.Helper()
	if _, err := svc.PutScope(ctx, connect.NewRequest(&rbacv1.PutScopeRequest{Path: path})); err != nil {
		t.Fatalf("登记范围 %s 失败: %v", path, err)
	}
}

// assign 走服务的授予/回收路径。
func assign(ctx context.Context, svc *RBACService, scope, subjectID, roleID string, grant bool) error {
	_, err := svc.AssignRole(ctx, connect.NewRequest(&rbacv1.AssignRoleRequest{
		Scope: scope, SubjectId: subjectID, RoleId: roleID, Grant: grant,
	}))
	return err
}

// 绑定必须指向已登记的范围——与"角色必须已定义才能授予"同构。
// 全局是唯一的例外：它是模型的根，不是目录里的一条。
func TestAssignRoleRequiresRegisteredScope(t *testing.T) {
	ctx := context.Background()
	store := rbac.NewMemoryStore()
	svc := newTestRBACService(store)
	if err := store.PutSubject(ctx, rbac.Subject{ID: "u1", Type: rbac.SubjectTypeUser}); err != nil {
		t.Fatalf("登记主体失败: %v", err)
	}

	// 未登记：拒绝。提示要能直接照做，否则管理员只知道"不行"。
	err := assign(ctx, svc, "tenant/acme", "u1", rbac.RoleViewer, true)
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("未登记范围授予 err = %v（码 %v），期望 NotFound", err, connect.CodeOf(err))
	}
	if !strings.Contains(err.Error(), "先登记") {
		t.Errorf("拒绝时没有告诉调用方该怎么办：%v", err)
	}

	putScope(t, ctx, svc, "tenant/acme")
	if err := assign(ctx, svc, "tenant/acme", "u1", rbac.RoleViewer, true); err != nil {
		t.Fatalf("已登记范围授予失败: %v", err)
	}

	// 全局不需要登记。
	if err := assign(ctx, svc, "", "u1", rbac.RoleViewer, true); err != nil {
		t.Fatalf("全局授予失败: %v", err)
	}
}

// 回收不做登记校验：撤收窄永远要能做，哪怕那条绑定落在的范围已经不在目录里。
func TestRevokeDoesNotRequireRegisteredScope(t *testing.T) {
	ctx := context.Background()
	store := rbac.NewMemoryStore()
	svc := newTestRBACService(store)
	if err := store.PutSubject(ctx, rbac.Subject{ID: "u1", Type: rbac.SubjectTypeUser}); err != nil {
		t.Fatalf("登记主体失败: %v", err)
	}
	// 直接往存储里写一条"历史遗留"的绑定：它的范围从未登记过。
	if err := store.Bind(ctx, rbac.RoleBinding{SubjectID: "u1", RoleID: rbac.RoleViewer, Scope: "legacy/scope"}); err != nil {
		t.Fatalf("写入绑定失败: %v", err)
	}

	if err := assign(ctx, svc, "legacy/scope", "u1", rbac.RoleViewer, false); err != nil {
		t.Fatalf("回收未登记范围上的绑定应成功: %v", err)
	}
	got, err := store.SubjectBindings(ctx, "u1")
	if err != nil {
		t.Fatalf("读取绑定失败: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("回收后仍有绑定: %+v", got)
	}
}

// 删除范围要有干净的引用：范围内或其后代上还有绑定就拒绝，并说明还有几条。
func TestDeleteScopeRefusesWhileBindingsRemain(t *testing.T) {
	ctx := context.Background()
	store := rbac.NewMemoryStore()
	svc := newTestRBACService(store)
	putScope(t, ctx, svc, "tenant/acme")
	putScope(t, ctx, svc, "tenant/acme/project")

	// 绑定落在**后代**上：父范围因此也删不掉——层级就是路径前缀。
	if err := store.Bind(ctx, rbac.RoleBinding{SubjectID: "u1", RoleID: rbac.RoleViewer, Scope: "tenant/acme/project"}); err != nil {
		t.Fatalf("写入绑定失败: %v", err)
	}

	for _, path := range []string{"tenant/acme", "tenant/acme/project"} {
		_, err := svc.DeleteScope(ctx, connect.NewRequest(&rbacv1.DeleteScopeRequest{Path: path}))
		if connect.CodeOf(err) != connect.CodeFailedPrecondition {
			t.Fatalf("删除仍有引用的 %s err = %v（码 %v），期望 FailedPrecondition",
				path, err, connect.CodeOf(err))
		}
		if !strings.Contains(err.Error(), "1 条绑定") {
			t.Errorf("拒绝理由应说明还有几条，得到: %v", err)
		}
	}

	// 摘掉引用之后两个都删得掉。
	if err := store.Unbind(ctx, rbac.RoleBinding{SubjectID: "u1", RoleID: rbac.RoleViewer, Scope: "tenant/acme/project"}); err != nil {
		t.Fatalf("回收绑定失败: %v", err)
	}
	for _, path := range []string{"tenant/acme/project", "tenant/acme"} {
		if _, err := svc.DeleteScope(ctx, connect.NewRequest(&rbacv1.DeleteScopeRequest{Path: path})); err != nil {
			t.Fatalf("引用摘掉后删除 %s 应成功: %v", path, err)
		}
	}
}

// 全局不是目录里的一条：登记它、删除它都要被拒，而不是留下一条特例记录。
func TestGlobalScopeIsNotARecord(t *testing.T) {
	ctx := context.Background()
	svc := newTestRBACService(rbac.NewMemoryStore())

	if _, err := svc.PutScope(ctx, connect.NewRequest(&rbacv1.PutScopeRequest{Path: ""})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("登记全局 err = %v（码 %v），期望 InvalidArgument", err, connect.CodeOf(err))
	}
	if _, err := svc.DeleteScope(ctx, connect.NewRequest(&rbacv1.DeleteScopeRequest{Path: ""})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("删除全局 err = %v（码 %v），期望 InvalidArgument", err, connect.CodeOf(err))
	}
}

// 登记一个已存在的路径 = 改显示名，路径不变——路径是标识。
func TestPutScopeRenamesDisplayNameOnly(t *testing.T) {
	ctx := context.Background()
	svc := newTestRBACService(rbac.NewMemoryStore())

	if _, err := svc.PutScope(ctx, connect.NewRequest(&rbacv1.PutScopeRequest{Path: "tenant/acme", DisplayName: "Acme"})); err != nil {
		t.Fatalf("登记范围失败: %v", err)
	}
	if _, err := svc.PutScope(ctx, connect.NewRequest(&rbacv1.PutScopeRequest{Path: "tenant/acme", DisplayName: "改过的名字"})); err != nil {
		t.Fatalf("改显示名失败: %v", err)
	}

	resp, err := svc.ListScopes(ctx, connect.NewRequest(&rbacv1.ListScopesRequest{}))
	if err != nil {
		t.Fatalf("列出范围失败: %v", err)
	}
	if len(resp.Msg.GetScopes()) != 1 {
		t.Fatalf("范围数 = %d，期望 1：%+v", len(resp.Msg.GetScopes()), resp.Msg.GetScopes())
	}
	got := resp.Msg.GetScopes()[0]
	if got.GetPath() != "tenant/acme" || got.GetDisplayName() != "改过的名字" {
		t.Errorf("读回 = %+v", got)
	}
}
