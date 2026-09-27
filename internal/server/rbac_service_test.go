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
