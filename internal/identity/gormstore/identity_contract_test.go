package gormstore

import (
	"context"
	"errors"
	"sort"
	"strings"
	"testing"

	"github.com/poetlife/aladdin/internal/identity"
)

// 两个身份别名存储实现共用同一套用例。
//
// 理由与会话存储相同（见 store_contract_test.go）：**同一份契约只能有一套
// 用例**。给 gorm 实现单独写一套，两套就会各自漂移，而漂移的表现形式是
// "换后端之后，同一个人的两个渠道忽然不是一个主体了"。

// identityCase 是一个身份别名存储实现的构造方式。
type identityCase struct {
	name string
	open func(t *testing.T) identity.IdentityStore
}

func identityCases(t *testing.T) []identityCase {
	t.Helper()
	return []identityCase{
		{
			name: "内存实现",
			open: func(*testing.T) identity.IdentityStore { return identity.NewMemoryIdentityStore() },
		},
		{
			name: "关系库实现",
			open: func(t *testing.T) identity.IdentityStore { return NewIdentityStore(newTestDB(t, newTestDBPath(t))) },
		},
	}
}

// forEachIdentityStore 把同一段用例跑在两个实现上。
func forEachIdentityStore(t *testing.T, run func(t *testing.T, store identity.IdentityStore)) {
	t.Helper()
	for _, ic := range identityCases(t) {
		t.Run(ic.name, func(t *testing.T) {
			run(t, ic.open(t))
		})
	}
}

// testIdentity 造一条身份，主体与展示信息由调用方给出。
func testIdentity(subjectID, externalID, display string) identity.Identity {
	return identity.Identity{
		Source:     identity.SourceGoogle,
		ExternalID: externalID,
		SubjectID:  subjectID,
		Display:    display,
	}
}

// 未登记的身份返回 ErrIdentityNotFound。
//
// 它与"存储不可用"必须可区分：前者是首次登录的正常结论（接着就该登记），
// 后者必须让请求快速失败。
func TestIdentityContractMissing(t *testing.T) {
	forEachIdentityStore(t, func(t *testing.T, store identity.IdentityStore) {
		_, err := store.Lookup(context.Background(), identity.SourceGoogle, "从不存在的身份")
		if !errors.Is(err, identity.ErrIdentityNotFound) {
			t.Errorf("err = %v，期望 ErrIdentityNotFound", err)
		}
	})
}

// 身份必须整份原样往返：少一个字段，解析出来的主体就少一项归属信息。
func TestIdentityContractRoundTrip(t *testing.T) {
	forEachIdentityStore(t, func(t *testing.T, store identity.IdentityStore) {
		ctx := context.Background()
		want := testIdentity("usr_a", "google-sub-a", "zhang@example.com")

		if err := store.Put(ctx, want); err != nil {
			t.Fatalf("写入身份失败: %v", err)
		}
		got, err := store.Lookup(ctx, want.Source, want.ExternalID)
		if err != nil {
			t.Fatalf("读取身份失败: %v", err)
		}
		if got != want {
			t.Errorf("身份 = %+v，期望 %+v", got, want)
		}
	})
}

// 同一个身份、同一个主体，重复写入是幂等的，并刷新展示信息。
func TestIdentityContractPutIsIdempotent(t *testing.T) {
	forEachIdentityStore(t, func(t *testing.T, store identity.IdentityStore) {
		ctx := context.Background()
		first := testIdentity("usr_a", "google-sub-a", "zhang@example.com")

		if err := store.Put(ctx, first); err != nil {
			t.Fatalf("第一次写入失败: %v", err)
		}
		renamed := first
		renamed.Display = "zhang+new@example.com"
		if err := store.Put(ctx, renamed); err != nil {
			t.Fatalf("第二次写入失败: %v", err)
		}

		list, err := store.ListBySubject(ctx, "usr_a")
		if err != nil {
			t.Fatalf("列出身份失败: %v", err)
		}
		if len(list) != 1 {
			t.Fatalf("身份条数 = %d，期望 1", len(list))
		}
		if list[0].Display != "zhang+new@example.com" {
			t.Errorf("展示信息 = %q，期望被刷新", list[0].Display)
		}
	})
}

// 一个身份只能属于一个主体：属于别人时写入失败，且**原有归属一字不动**。
//
// 这条是"绑定不能夺走别人的进入方式"的落点。
func TestIdentityContractPutRejectsOtherOwner(t *testing.T) {
	forEachIdentityStore(t, func(t *testing.T, store identity.IdentityStore) {
		ctx := context.Background()
		original := testIdentity("usr_a", "google-sub-a", "zhang@example.com")
		if err := store.Put(ctx, original); err != nil {
			t.Fatalf("写入身份失败: %v", err)
		}

		steal := testIdentity("usr_b", "google-sub-a", "li@example.com")
		err := store.Put(ctx, steal)
		if !errors.Is(err, identity.ErrIdentityTaken) {
			t.Fatalf("err = %v，期望 ErrIdentityTaken", err)
		}

		got, err := store.Lookup(ctx, original.Source, original.ExternalID)
		if err != nil {
			t.Fatalf("读取身份失败: %v", err)
		}
		if got != original {
			t.Errorf("归属被改动了：%+v，期望仍是 %+v", got, original)
		}
	})
}

// 拒绝不透露占用者：错误信息里不得出现别人的主体标识。
func TestIdentityContractTakenDoesNotLeakOwner(t *testing.T) {
	forEachIdentityStore(t, func(t *testing.T, store identity.IdentityStore) {
		ctx := context.Background()
		if err := store.Put(ctx, testIdentity("usr_owner", "google-sub-a", "")); err != nil {
			t.Fatalf("写入身份失败: %v", err)
		}

		err := store.Put(ctx, testIdentity("usr_other", "google-sub-a", ""))
		if err == nil {
			t.Fatal("期望写入失败")
		}
		if got := err.Error(); strings.Contains(got, "usr_owner") {
			t.Errorf("错误信息泄露了占用者：%q", got)
		}
	})
}

// 解绑只作用于自己的身份：拿别人的主体标识来删，一行不动。
func TestIdentityContractDeleteScopedToSubject(t *testing.T) {
	forEachIdentityStore(t, func(t *testing.T, store identity.IdentityStore) {
		ctx := context.Background()
		ident := testIdentity("usr_a", "google-sub-a", "")
		other := testIdentity("usr_a", "google-sub-b", "")
		if err := store.Put(ctx, ident); err != nil {
			t.Fatalf("写入身份失败: %v", err)
		}
		if err := store.Put(ctx, other); err != nil {
			t.Fatalf("写入身份失败: %v", err)
		}

		removed, err := store.Delete(ctx, ident.Source, ident.ExternalID, "usr_b")
		if err != nil {
			t.Fatalf("解绑失败: %v", err)
		}
		if removed {
			t.Error("解绑了不属于该主体的身份")
		}
		if _, err := store.Lookup(ctx, ident.Source, ident.ExternalID); err != nil {
			t.Errorf("身份被误删：%v", err)
		}
	})
}

// 解绑不能摘掉最后一个身份，且此时**一行都不动**。
//
// 留下一个没有任何进入方式的主体，等于留下一批无人可达的角色绑定。
func TestIdentityContractDeleteKeepsLast(t *testing.T) {
	forEachIdentityStore(t, func(t *testing.T, store identity.IdentityStore) {
		ctx := context.Background()
		only := testIdentity("usr_a", "google-sub-a", "")
		if err := store.Put(ctx, only); err != nil {
			t.Fatalf("写入身份失败: %v", err)
		}

		removed, err := store.Delete(ctx, only.Source, only.ExternalID, "usr_a")
		if !errors.Is(err, identity.ErrLastIdentity) {
			t.Fatalf("err = %v，期望 ErrLastIdentity", err)
		}
		if removed {
			t.Error("最后一个身份被真的删掉了")
		}
		if _, err := store.Lookup(ctx, only.Source, only.ExternalID); err != nil {
			t.Errorf("身份被误删：%v", err)
		}
	})
}

// 还有别的身份时，解绑成功且之后查不到这一条。
func TestIdentityContractDeleteRemoves(t *testing.T) {
	forEachIdentityStore(t, func(t *testing.T, store identity.IdentityStore) {
		ctx := context.Background()
		target := testIdentity("usr_a", "google-sub-a", "")
		keep := testIdentity("usr_a", "google-sub-b", "")
		for _, ident := range []identity.Identity{target, keep} {
			if err := store.Put(ctx, ident); err != nil {
				t.Fatalf("写入身份失败: %v", err)
			}
		}

		removed, err := store.Delete(ctx, target.Source, target.ExternalID, "usr_a")
		if err != nil {
			t.Fatalf("解绑失败: %v", err)
		}
		if !removed {
			t.Error("解绑没有被执行")
		}
		if _, err := store.Lookup(ctx, target.Source, target.ExternalID); !errors.Is(err, identity.ErrIdentityNotFound) {
			t.Errorf("err = %v，期望 ErrIdentityNotFound", err)
		}
	})
}

// 解绑不存在的身份不是错误：重复解绑与解绑一个本来就没有的身份，结果一致。
func TestIdentityContractDeleteMissing(t *testing.T) {
	forEachIdentityStore(t, func(t *testing.T, store identity.IdentityStore) {
		removed, err := store.Delete(context.Background(), identity.SourceGoogle, "从不存在", "usr_a")
		if err != nil {
			t.Fatalf("解绑不存在的身份报错: %v", err)
		}
		if removed {
			t.Error("解绑不存在的身份却报告删掉了")
		}
	})
}

// 列出某个主体的身份时，只返回它自己的，顺序不参与断言（排序在调用方）。
func TestIdentityContractListBySubject(t *testing.T) {
	forEachIdentityStore(t, func(t *testing.T, store identity.IdentityStore) {
		ctx := context.Background()
		mine := []identity.Identity{
			testIdentity("usr_a", "google-sub-b", "b@example.com"),
			testIdentity("usr_a", "google-sub-a", "a@example.com"),
		}
		for _, ident := range append(mine, testIdentity("usr_b", "google-sub-c", "c@example.com")) {
			if err := store.Put(ctx, ident); err != nil {
				t.Fatalf("写入身份失败: %v", err)
			}
		}

		list, err := store.ListBySubject(ctx, "usr_a")
		if err != nil {
			t.Fatalf("列出身份失败: %v", err)
		}
		sort.Slice(list, func(a, b int) bool { return list[a].ExternalID < list[b].ExternalID })
		sort.Slice(mine, func(a, b int) bool { return mine[a].ExternalID < mine[b].ExternalID })
		if len(list) != len(mine) {
			t.Fatalf("身份条数 = %d，期望 %d", len(list), len(mine))
		}
		for i := range mine {
			if list[i] != mine[i] {
				t.Errorf("第 %d 条 = %+v，期望 %+v", i, list[i], mine[i])
			}
		}
	})
}

// 按展示值查身份：**可能命中多条**。
//
// 这条是引导那条"恰好一个才生效"的前提——本库刻意允许同一个邮箱字符串分别
// 挂在两个身份上，所以"按邮箱找主体"在形状上就无法保证唯一。存储这一层只
// 如实返回全部命中，不挑；挑不挑是上层的事，而那里定的是"不挑，拒绝启动"。
func TestIdentityContractListByDisplayMayMatchSeveral(t *testing.T) {
	forEachIdentityStore(t, func(t *testing.T, store identity.IdentityStore) {
		ctx := context.Background()
		shared := "same@example.com"
		both := []identity.Identity{
			testIdentity("usr_a", "google-sub-a", shared),
			testIdentity("usr_b", "google-sub-b", shared),
		}
		for _, ident := range append(both, testIdentity("usr_c", "google-sub-c", "other@example.com")) {
			if err := store.Put(ctx, ident); err != nil {
				t.Fatalf("写入身份失败: %v", err)
			}
		}

		list, err := store.ListByDisplay(ctx, shared)
		if err != nil {
			t.Fatalf("按展示值读取身份失败: %v", err)
		}
		sort.Slice(list, func(a, b int) bool { return list[a].ExternalID < list[b].ExternalID })
		sort.Slice(both, func(a, b int) bool { return both[a].ExternalID < both[b].ExternalID })
		if len(list) != len(both) {
			t.Fatalf("命中条数 = %d，期望 %d", len(list), len(both))
		}
		for i := range both {
			if list[i] != both[i] {
				t.Errorf("第 %d 条 = %+v，期望 %+v", i, list[i], both[i])
			}
		}
	})
}

// 按展示值查不到时返回空列表，而不是错误。
//
// "没有这个人"是正常结论，怎么处理由调用方定（引导那边是拒绝启动并说明
// 原因）。把它做成错误会让"存储不可用"与"查无此人"混成同一类。
func TestIdentityContractListByDisplayNoMatch(t *testing.T) {
	forEachIdentityStore(t, func(t *testing.T, store identity.IdentityStore) {
		ctx := context.Background()
		if err := store.Put(ctx, testIdentity("usr_a", "google-sub-a", "a@example.com")); err != nil {
			t.Fatalf("写入身份失败: %v", err)
		}

		list, err := store.ListByDisplay(ctx, "nobody@example.com")
		if err != nil {
			t.Fatalf("按展示值读取身份失败: %v", err)
		}
		if len(list) != 0 {
			t.Errorf("命中条数 = %d，期望 0", len(list))
		}
	})
}

// 没有任何身份的主体返回空列表，而不是错误。
//
// 它对应的是"这个人还没绑过任何渠道"，属于正常状态。
func TestIdentityContractListEmpty(t *testing.T) {
	forEachIdentityStore(t, func(t *testing.T, store identity.IdentityStore) {
		list, err := store.ListBySubject(context.Background(), "从未登记的主体")
		if err != nil {
			t.Fatalf("列出身份失败: %v", err)
		}
		if len(list) != 0 {
			t.Errorf("身份条数 = %d，期望 0", len(list))
		}
	})
}
