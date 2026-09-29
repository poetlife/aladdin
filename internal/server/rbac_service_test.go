package server

import (
	"context"
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
