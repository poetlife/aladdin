package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"connectrpc.com/connect"

	"github.com/poetlife/aladdin/internal/config"
)

// TestDescribeErrorNamesTheEffectiveTimeout 守住"超时要在报错里认得出是超时"。
//
// 裸的 `deadline_exceeded` 等于没说：看不出这是超时设置的问题，也看不出该动哪个
// 参数——而纳管与同步的耗时本来就随远端仓库的文件数增长，一次超时既可能是参数给
// 小了，也可能只是这个仓库文件多（见 docs/design/skill/onboarding.md 的"超时与
// 失败"）。判据因此是具体的：提示里必须同时出现**生效的值**与**那个参数名**。
func TestDescribeErrorNamesTheEffectiveTimeout(t *testing.T) {
	original := effectiveTimeout
	t.Cleanup(func() { effectiveTimeout = original })

	deadline := connect.NewError(connect.CodeDeadlineExceeded, context.DeadlineExceeded)

	effectiveTimeout = config.DefaultSkillCatalogTimeout
	msg := describeError(deadline)
	for _, want := range []string{config.DefaultSkillCatalogTimeout.String(), "--timeout"} {
		if !strings.Contains(msg, want) {
			t.Errorf("提示里没有 %q：%s", want, msg)
		}
	}

	// 配置没解析成功时也必须落在一个数字上。一句本该给数字的提示里留一个空白，
	// 比给一个内置默认值更糟。
	effectiveTimeout = 0
	if msg := describeError(deadline); !strings.Contains(msg, config.DefaultTimeout.String()) {
		t.Errorf("兜底提示里没有内置默认值：%s", msg)
	}

	// 反过来也要成立：别的失败不能被套上"调大超时"的建议，那会把一次远端故障
	// 指到一个不相干的参数上。
	unavailable := connect.NewError(connect.CodeUnavailable, errors.New("远端不可达"))
	if msg := describeError(unavailable); strings.Contains(msg, "--timeout") {
		t.Errorf("非超时的错误被套上了超时的建议：%s", msg)
	}
}
