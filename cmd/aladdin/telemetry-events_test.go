package main

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	telemetryv1 "github.com/poetlife/aladdin/api/gen/aladdin/telemetry/v1"
	"github.com/poetlife/aladdin/internal/auth"
	"github.com/poetlife/aladdin/internal/config"
)

func TestLocalFailureReason(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
		ok   bool
	}{
		{"配置无效", fmt.Errorf("%w：未知的键 x", config.ErrInvalid), "config", true},
		{"未登录", auth.ErrNoCredential, "credential", true},
		{"凭证文件权限不对", auth.ErrCredentialFileInsecure, "credential", true},
		{"用法错误", usageErrorf("参数不合法"), "usage", true},
		// 走了服务端的失败由请求留痕覆盖，客户端不重复上报。
		{"服务端拒绝", connect.NewError(connect.CodePermissionDenied, errors.New("不行")), "", false},
		{"未分类错误", errors.New("boom"), "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reason, ok := localFailureReason(tc.err)
			if ok != tc.ok || reason != tc.want {
				t.Errorf("分类 = (%q, %v)，期望 (%q, %v)", reason, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestRecordLocalFailureShape(t *testing.T) {
	t.Cleanup(func() {
		pendingLocalFailure = nil
		executedCommand = ""
	})

	executedCommand = "galaxy"
	recordLocalFailure(auth.ErrNoCredential)

	ev := pendingLocalFailure
	if ev == nil {
		t.Fatal("应记下一条待上报事件")
	}
	if ev.GetClient() != telemetryv1.Client_CLIENT_CLI {
		t.Errorf("上报端 = %v，期望 cli", ev.GetClient())
	}
	if ev.GetSurface() != telemetryv1.Surface_SURFACE_CLI {
		t.Errorf("界面 = %v，期望 cli", ev.GetSurface())
	}
	if ev.GetAction() != telemetryv1.Action_ACTION_CLI_LOCAL_FAIL {
		t.Errorf("动作 = %v，期望 cli.local_fail", ev.GetAction())
	}
	if ev.GetResult() != telemetryv1.Result_RESULT_FAIL {
		t.Errorf("结局 = %v，期望 fail", ev.GetResult())
	}
	if got := ev.GetAttrs()["reason"]; got != "credential" {
		t.Errorf("原因 = %q，期望 credential", got)
	}
	if got := ev.GetAttrs()["command"]; got != "galaxy" {
		t.Errorf("命令 = %q，期望 galaxy", got)
	}
}

// 本地失败带上"从进程启动到失败"的耗时。
//
// 没走过 Execute（未开始计时）时一律报 0，而 0 的语义是"未提供"：算不出来时宁可
// 交白卷，也不截断或补一个看似合理的数字。
func TestRecordLocalFailureDuration(t *testing.T) {
	t.Cleanup(func() {
		pendingLocalFailure = nil
		commandStartedAt = time.Time{}
	})

	commandStartedAt = time.Now().Add(-150 * time.Millisecond)
	recordLocalFailure(auth.ErrNoCredential)
	if got := pendingLocalFailure.GetDurationMs(); got < 100 || got > 1000 {
		t.Errorf("耗时 = %d ms，期望量级在 150ms 上下", got)
	}

	pendingLocalFailure = nil
	commandStartedAt = time.Time{}
	recordLocalFailure(auth.ErrNoCredential)
	if got := pendingLocalFailure.GetDurationMs(); got != 0 {
		t.Errorf("未开始计时时耗时 = %d，期望 0", got)
	}
}

// 命中不了任何类别时不留事件：服务端留痕已经覆盖的失败不该在这里再报一遍。
func TestRecordLocalFailureIgnoresServerErrors(t *testing.T) {
	t.Cleanup(func() { pendingLocalFailure = nil })

	recordLocalFailure(connect.NewError(connect.CodeUnavailable, errors.New("连不上")))
	if pendingLocalFailure != nil {
		t.Error("服务端失败不应产生本地失败事件")
	}
}

func TestTopLevelCommand(t *testing.T) {
	root := &cobra.Command{Use: "aladdin"}
	galaxy := &cobra.Command{Use: "galaxy"}
	save := &cobra.Command{Use: "save"}
	galaxy.AddCommand(save)
	root.AddCommand(galaxy)

	if got := topLevelCommand(save); got != "galaxy" {
		t.Errorf("顶层命令 = %q，期望 galaxy", got)
	}
	if got := topLevelCommand(root); got != "" {
		t.Errorf("根命令下没有子命令时应为空，得到 %q", got)
	}
}

// 没有待上报事件时不做任何事——不能因为遥测去碰网络。
func TestFlushClientEventsNoopWithoutPending(t *testing.T) {
	pendingLocalFailure = nil
	flushClientEvents()
}
