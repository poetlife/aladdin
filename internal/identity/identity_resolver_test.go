package identity

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/poetlife/aladdin/internal/rbac"
)

// 解析器是"一个渠道身份属于哪个主体"的唯一入口，用例覆盖两条动作
// （登录时的查-或-建、已登录后的绑定）与它们的边界。
//
// 它跑在内存实现上：SQL 实现与它共享同一套存储契约
// （见 gormstore 的 identity_contract_test.go），这里要冻结的是
// **归并语义**，不是某个驱动的行为。

func newTestIdentities() (*Identities, rbac.MutableStore) {
	subjects := rbac.NewMemoryStore()
	return NewIdentities(NewMemoryIdentityStore(), subjects), subjects
}

// 首次登录：登记一个新主体，类型是人类用户，且默认作用域为空。
//
// "零权限"是预期路径而不是错误路径：他能登录、能看见界面框架，
// 但什么都做不了。
func TestResolveRegistersNewSubject(t *testing.T) {
	ctx := context.Background()
	identities, subjects := newTestIdentities()

	subject, err := identities.ResolveOrRegister(ctx, SourceGoogle, "sub-a", "a@example.com")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if subject.ID == "" {
		t.Error("登记出来的主体没有标识")
	}
	if subject.Type != rbac.SubjectTypeUser {
		t.Errorf("主体类型 = %q，期望 %q", subject.Type, rbac.SubjectTypeUser)
	}
	if subject.DefaultScope != rbac.GlobalScope {
		t.Errorf("新主体的默认作用域 = %q，期望为空", subject.DefaultScope)
	}

	// 登记必须真的落到了主体表：未登记的主体在判定时视为不存在。
	got, err := subjects.Subject(ctx, subject.ID)
	if err != nil {
		t.Fatalf("主体没有被登记: %v", err)
	}
	if got != subject {
		t.Errorf("主体 = %+v，期望 %+v", got, subject)
	}
}

// 同一个身份两次登录得到同一个主体——"同一个人两次登录不会变成两个主体"。
func TestResolveIsStable(t *testing.T) {
	ctx := context.Background()
	identities, _ := newTestIdentities()

	first, err := identities.ResolveOrRegister(ctx, SourceGoogle, "sub-a", "a@example.com")
	if err != nil {
		t.Fatalf("第一次解析失败: %v", err)
	}
	second, err := identities.ResolveOrRegister(ctx, SourceGoogle, "sub-a", "a@example.com")
	if err != nil {
		t.Fatalf("第二次解析失败: %v", err)
	}
	if first.ID != second.ID {
		t.Errorf("两次登录的主体不同：%q 与 %q", first.ID, second.ID)
	}
}

// 主体标识不随渠道侧的信息变化：改了邮箱、换一个展示名，还是同一个主体。
func TestResolveIgnoresDisplay(t *testing.T) {
	ctx := context.Background()
	identities, _ := newTestIdentities()

	first, err := identities.ResolveOrRegister(ctx, SourceGoogle, "sub-a", "a@example.com")
	if err != nil {
		t.Fatalf("第一次解析失败: %v", err)
	}
	renamed, err := identities.ResolveOrRegister(ctx, SourceGoogle, "sub-a", "a+renamed@example.com")
	if err != nil {
		t.Fatalf("第二次解析失败: %v", err)
	}
	if first.ID != renamed.ID {
		t.Errorf("改展示信息改变了主体：%q 与 %q", first.ID, renamed.ID)
	}
}

// 不同的身份即使邮箱相同，也是两个主体。
//
// 这是"不按邮箱归并"的落点：按邮箱归并意味着邮箱在渠道侧被回收的那天，
// 另一个人直接接管这个主体的全部角色绑定。
func TestResolveDoesNotMergeByDisplay(t *testing.T) {
	ctx := context.Background()
	identities, _ := newTestIdentities()

	first, err := identities.ResolveOrRegister(ctx, SourceGoogle, "sub-a", "same@example.com")
	if err != nil {
		t.Fatalf("第一次解析失败: %v", err)
	}
	second, err := identities.ResolveOrRegister(ctx, SourceGoogle, "sub-b", "same@example.com")
	if err != nil {
		t.Fatalf("第二次解析失败: %v", err)
	}
	if first.ID == second.ID {
		t.Error("两个不同的身份被并成了同一个主体")
	}
}

// 不同的来源下，同一个不可变标识字符串是两个不同的身份。
//
// 这条是"将来接入第二个身份源时两边不会撞到一起"的落点。
func TestResolveSeparatesSources(t *testing.T) {
	ctx := context.Background()
	identities, _ := newTestIdentities()

	google, err := identities.ResolveOrRegister(ctx, SourceGoogle, "same-id", "")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	other, err := identities.ResolveOrRegister(ctx, "example", "same-id", "")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if google.ID == other.ID {
		t.Error("两个来源下的同名标识被并成了同一个主体")
	}
}

// 绑定第二个渠道之后，两个渠道登录得到同一个主体。
//
// 这是本模块存在的理由：管理员在渠道 A 上授的权，从渠道 B 登进来要看得见。
func TestBindJoinsSecondChannel(t *testing.T) {
	ctx := context.Background()
	identities, _ := newTestIdentities()

	first, err := identities.ResolveOrRegister(ctx, SourceGoogle, "sub-a", "a@example.com")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if err := identities.Bind(ctx, first.ID, "example", "ext-1", "a@other.example"); err != nil {
		t.Fatalf("绑定失败: %v", err)
	}

	second, err := identities.ResolveOrRegister(ctx, "example", "ext-1", "a@other.example")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if second.ID != first.ID {
		t.Errorf("第二个渠道登录到的主体 = %q，期望 %q", second.ID, first.ID)
	}
}

// 绑定自己已经绑过的身份是幂等的；绑别人的身份一律拒绝。
func TestBindIdempotentAndRejectsOthers(t *testing.T) {
	ctx := context.Background()
	identities, _ := newTestIdentities()

	mine, err := identities.ResolveOrRegister(ctx, SourceGoogle, "sub-a", "")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	other, err := identities.ResolveOrRegister(ctx, SourceGoogle, "sub-b", "")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}

	if err := identities.Bind(ctx, mine.ID, "example", "ext-1", ""); err != nil {
		t.Fatalf("第一次绑定失败: %v", err)
	}
	if err := identities.Bind(ctx, mine.ID, "example", "ext-1", ""); err != nil {
		t.Fatalf("重复绑定失败（应为幂等）: %v", err)
	}

	err = identities.Bind(ctx, other.ID, "example", "ext-1", "")
	if !errors.Is(err, ErrIdentityTaken) {
		t.Fatalf("err = %v，期望 ErrIdentityTaken", err)
	}
	// 归属一字不动。
	got, err := identities.ResolveOrRegister(ctx, "example", "ext-1", "")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if got.ID != mine.ID {
		t.Errorf("归属被改动：%q，期望 %q", got.ID, mine.ID)
	}
}

// 解绑只作用于自己的身份。
func TestUnbindRejectsOtherSubject(t *testing.T) {
	ctx := context.Background()
	identities, _ := newTestIdentities()

	mine, err := identities.ResolveOrRegister(ctx, SourceGoogle, "sub-a", "")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	other, err := identities.ResolveOrRegister(ctx, SourceGoogle, "sub-b", "")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}

	err = identities.Unbind(ctx, other.ID, SourceGoogle, "sub-a")
	if !errors.Is(err, ErrIdentityNotFound) {
		t.Fatalf("err = %v，期望 ErrIdentityNotFound", err)
	}
	still, err := identities.ResolveOrRegister(ctx, SourceGoogle, "sub-a", "")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if still.ID != mine.ID {
		t.Error("别人的解绑动到了这个身份")
	}
}

// 不允许摘掉最后一个身份：那会让这个主体再也进不来，
// 而它的角色绑定还在，没有人能进来清理。
func TestUnbindKeepsLastIdentity(t *testing.T) {
	ctx := context.Background()
	identities, _ := newTestIdentities()

	subject, err := identities.ResolveOrRegister(ctx, SourceGoogle, "sub-a", "")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}

	err = identities.Unbind(ctx, subject.ID, SourceGoogle, "sub-a")
	if !errors.Is(err, ErrLastIdentity) {
		t.Fatalf("err = %v，期望 ErrLastIdentity", err)
	}
	if _, err := identities.ResolveOrRegister(ctx, SourceGoogle, "sub-a", ""); err != nil {
		t.Errorf("身份被误删: %v", err)
	}
}

// 解绑之后，该渠道不再通向这个主体：再登录会登记出一个新的、零权限的主体。
//
// 这是预期行为而不是权限丢失，但它是用户最容易误解的一步。
func TestUnbindLeavesChannelOnNewSubject(t *testing.T) {
	ctx := context.Background()
	identities, _ := newTestIdentities()

	original, err := identities.ResolveOrRegister(ctx, SourceGoogle, "sub-a", "")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if err := identities.Bind(ctx, original.ID, "example", "ext-1", ""); err != nil {
		t.Fatalf("绑定失败: %v", err)
	}
	if err := identities.Unbind(ctx, original.ID, "example", "ext-1"); err != nil {
		t.Fatalf("解绑失败: %v", err)
	}

	again, err := identities.ResolveOrRegister(ctx, "example", "ext-1", "")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if again.ID == original.ID {
		t.Error("解绑后该渠道仍然通向原主体")
	}
	if again.DefaultScope != rbac.GlobalScope {
		t.Errorf("重新登记出的主体默认作用域 = %q，期望为空", again.DefaultScope)
	}
}

// 列出的身份顺序稳定：内存实现按 map 遍历、SQL 实现按自己的次序返回，
// 顺序只能由这一处定，否则界面看起来在抖动。
func TestListIsSorted(t *testing.T) {
	ctx := context.Background()
	identities, _ := newTestIdentities()

	subject, err := identities.ResolveOrRegister(ctx, SourceGoogle, "sub-z", "")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	for _, id := range []string{"sub-c", "sub-a", "sub-b"} {
		if err := identities.Bind(ctx, subject.ID, SourceGoogle, id, ""); err != nil {
			t.Fatalf("绑定 %s 失败: %v", id, err)
		}
	}
	for _, source := range []string{"example", "auth0"} {
		if err := identities.Bind(ctx, subject.ID, source, "sub-a", ""); err != nil {
			t.Fatalf("绑定 %s 失败: %v", source, err)
		}
	}

	list, err := identities.List(ctx, subject.ID)
	if err != nil {
		t.Fatalf("列出失败: %v", err)
	}
	want := []Identity{
		{Source: "auth0", ExternalID: "sub-a", SubjectID: subject.ID},
		{Source: "example", ExternalID: "sub-a", SubjectID: subject.ID},
		{Source: SourceGoogle, ExternalID: "sub-a", SubjectID: subject.ID},
		{Source: SourceGoogle, ExternalID: "sub-b", SubjectID: subject.ID},
		{Source: SourceGoogle, ExternalID: "sub-c", SubjectID: subject.ID},
		{Source: SourceGoogle, ExternalID: "sub-z", SubjectID: subject.ID},
	}
	if len(list) != len(want) {
		t.Fatalf("身份条数 = %d，期望 %d", len(list), len(want))
	}
	for i := range want {
		if list[i] != want[i] {
			t.Errorf("第 %d 条 = %+v，期望 %+v", i, list[i], want[i])
		}
	}
}

// 主体标识由 aladdin 分配：它不含渠道取值，也不等于渠道标识。
//
// 把渠道编进主体标识，等于替这个人选定了一个渠道，
// 而"一个人两个渠道"正是要支持的。
func TestSubjectIDIsOpaque(t *testing.T) {
	ctx := context.Background()
	identities, _ := newTestIdentities()

	subject, err := identities.ResolveOrRegister(ctx, SourceGoogle, "sub-a", "")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if subject.ID == "sub-a" {
		t.Error("主体标识直接用了渠道标识")
	}
	if !strings.HasPrefix(subject.ID, subjectIDPrefix) {
		t.Errorf("主体标识 %q 缺少 %q 前缀", subject.ID, subjectIDPrefix)
	}
}
