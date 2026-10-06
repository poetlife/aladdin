// 本文件是 rbac 的外部测试包：只依赖 rbac 的公开 API。
package rbac_test

import (
	"errors"
	"testing"

	"github.com/poetlife/aladdin/internal/rbac"
)

// "地址不对应任何 RPC 方法"必须是一个**可辨认的错误**，不能与"方法存在但漏写
// 注解"混作一团：前者是调用方把地址打错了，后者是服务端缺陷，两者给出的错误码
// 不同（见 docs/design/rbac/server-permissions.md 的"拒绝语义"）。
func TestUnknownProcedureIsDistinguishable(t *testing.T) {
	cases := []struct {
		name      string
		procedure string
	}{
		{"根路径", "/"},
		{"不是方法形状", "/nope"},
		{"服务不存在", "/aladdin.nope.v1.Missing/Do"},
		{"方法不存在", "/aladdin.rbac.v1.RBACService/Nope"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := rbac.Resolve(tc.procedure)
			if err == nil {
				t.Fatalf("%q 被解析成功了", tc.procedure)
			}
			if !errors.Is(err, rbac.ErrUnknownProcedure) {
				t.Errorf("%q 的错误是 %v，不是 ErrUnknownProcedure", tc.procedure, err)
			}
		})
	}
}

// 反向用例：真实方法仍解析得出规则。没有它，上面那一条可能只是"什么都报未知"。
func TestKnownProcedureStillResolves(t *testing.T) {
	if _, err := rbac.Resolve("/aladdin.rbac.v1.RBACService/GetRole"); err != nil {
		t.Fatalf("已知方法被拒：%v", err)
	}
}
