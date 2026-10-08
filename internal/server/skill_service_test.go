package server

import (
	"errors"
	"fmt"
	"testing"

	"connectrpc.com/connect"

	"github.com/poetlife/aladdin/internal/objectstore"
	"github.com/poetlife/aladdin/internal/skill"
)

// **"库坏了"、"桶这次坏了"、"没配桶"是三句话。**
//
// 它们决定客户端该重试、排障的人该去哪一头查：库与桶都是"稍后重试"但查的地方不同；
// "没配桶"根本不是故障，是部署形态的事实。混成同一句话的表现是**照着它去查一个
// 根本没错的地方**——技能图集那条路原来正是这样（一次存储故障被包成"没有配置对象
// 存储"的包装，于是先判到的是通用那句）。
func TestSkillConnectErrorSeparatesUnavailableCauses(t *testing.T) {
	cases := []struct {
		name    string
		err     error
		code    connect.Code
		message string
	}{
		{
			name:    "关系库不可用",
			err:     skill.ErrStoreUnavailable,
			code:    connect.CodeUnavailable,
			message: "服务暂时不可用，请稍后重试",
		},
		{
			name:    "对象存储这次失败了",
			err:     fmt.Errorf("%w: 核对上传失败: connection reset by peer", objectstore.ErrStoreUnavailable),
			code:    connect.CodeUnavailable,
			message: "对象存储暂时不可用，请稍后重试",
		},
		{
			// 这一条钉的是"先判到哪一句"：链上同时挂着"没配置"的包装时，结论
			// 仍然必须是"失败了"。
			name: "链上还挂着\"没配置\"的包装",
			err: fmt.Errorf("%w: %w",
				skill.ErrObjectStoreUnavailable, objectstore.ErrStoreUnavailable),
			code:    connect.CodeUnavailable,
			message: "对象存储暂时不可用，请稍后重试",
		},
		{
			name:    "这个部署没有配桶",
			err:     skill.ErrObjectStoreUnavailable,
			code:    connect.CodeFailedPrecondition,
			message: "这个部署没有配置对象存储，技能目录不可用",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var mapped *connect.Error
			if !errors.As(toSkillConnectError(tc.err), &mapped) {
				t.Fatalf("映射结果不是 connect 错误: %T", toSkillConnectError(tc.err))
			}
			if mapped.Code() != tc.code {
				t.Errorf("错误码 = %v，期望 %v", mapped.Code(), tc.code)
			}
			// 这三句必须逐字分开：它们决定排障的人去哪一头查。
			if mapped.Message() != tc.message {
				t.Errorf("错误文案 = %q，期望 %q", mapped.Message(), tc.message)
			}
		})
	}
}
