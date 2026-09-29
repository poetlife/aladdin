package main

import (
	"context"
	"errors"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	telemetryv1 "github.com/poetlife/aladdin/api/gen/aladdin/telemetry/v1"
	"github.com/poetlife/aladdin/api/gen/aladdin/telemetry/v1/telemetryv1connect"
	"github.com/poetlife/aladdin/internal/auth"
	"github.com/poetlife/aladdin/internal/config"
	"github.com/poetlife/aladdin/pkg/client"
)

// eventsFlushTimeout 是退出前上报本地失败的时间上限。
//
// 与遥测的冲刷预算同量级：它必须短到不会让用户觉得命令卡住，又要长到一次
// 同网内的往返能跑完。上报失败不影响退出码。
const eventsFlushTimeout = 3 * time.Second

// executedCommand 是本次进程实际执行到的**顶层**命令名（galaxy、login…）。
//
// 它在 PersistentPreRunE 里记录：退出路径（Execute）只看得到错误，看不到是哪条
// 命令失败的。没有它就退化成"某条命令行失败过一次"，而"哪条命令在反复失败"
// 正是这里要回答的。标志解析失败、命令不存在这两种情况到不了 PersistentPreRunE，
// 此时它为空，事件里就不带这个属性。
var executedCommand string

// pendingLocalFailure 是本次进程要上报的本地失败。
//
// 至多一条：一个短命进程只有一个结局。留在内存里，退出前统一发出去（P0 不做
// 批处理与离线队列，见 docs/observability.md 的「客户端事件」）。
var pendingLocalFailure *telemetryv1.Event

// recordLocalFailure 记下一次**本地失败**：配置无效、未登录、用法错误。
//
// 它们都在发出任何请求之前结束，服务端请求留痕里没有它们——这正是本文件存在的
// 理由。带 Connect 错误码的失败不在此列：那些已经由服务端留痕覆盖，且带上
// client=cli 之后可按端归因，客户端再报一遍只会把同一件事数两遍。
func recordLocalFailure(err error) {
	reason, ok := localFailureReason(err)
	if !ok {
		return
	}
	attrs := map[string]string{"reason": reason}
	// command 的取值受服务端形状约束（小写字母/数字/短横线）。顶层命令名天然
	// 落在其中；空值时干脆不带这个属性，而不是塞一个空串。
	if executedCommand != "" {
		attrs["command"] = executedCommand
	}
	pendingLocalFailure = &telemetryv1.Event{
		Client:  telemetryv1.Client_CLIENT_CLI,
		Surface: telemetryv1.Surface_SURFACE_CLI,
		Action:  telemetryv1.Action_ACTION_CLI_LOCAL_FAIL,
		Result:  telemetryv1.Result_RESULT_FAIL,
		Attrs:   attrs,
	}
}

// localFailureReason 判定一个错误是不是"本地失败"，并给出原因类别。
//
// 分类与 exitCodeFor 的本地分支一一对应：错误的归类只有一处实现，退出码与上报
// 原因不会出现"这条命令退出码说未登录、上报说用法错误"这种自相矛盾。
func localFailureReason(err error) (string, bool) {
	switch {
	case errors.Is(err, config.ErrInvalid):
		return "config", true
	case errors.Is(err, auth.ErrNoCredential), errors.Is(err, auth.ErrCredentialFileInsecure):
		return "credential", true
	}
	var ue *usageError
	if errors.As(err, &ue) {
		return "usage", true
	}
	return "", false
}

// flushClientEvents 在退出前把待上报的事件发出去。
//
// 四种情况都**静默放弃**，且都不影响退出码：配置读不出来（连目标地址都没有，
// 无处可报）、遥测构建失败、地址不可达、服务端拒收。P0 没有离线队列，"纯离线
// 失败只留在本地"是写进文档的取舍，不是遗漏。
func flushClientEvents() {
	event := pendingLocalFailure
	pendingLocalFailure = nil
	if event == nil {
		return
	}

	cfg, err := resolvedConfig()
	if err != nil {
		return
	}
	// 遥测实现必须早于客户端构造：client span 由客户端的拦截器起。
	if err := ensureTelemetry(cfg); err != nil {
		return
	}

	opts := client.Options{Address: cfg.Address, TLS: cfg.TLSConfig()}
	// **允许匿名上报**：未登录本身就是这里要记的场景之一，要求先登录才能上报会
	// 把它挡在门外。取不到凭证就按匿名发（服务端的动作白名单接受这一条）。
	if cred, err := auth.Resolve(flags.token, flags.scope); err == nil {
		opts.Token = cred.Token
		opts.Scope = cred.Scope
	}

	c, err := client.Dial(opts)
	if err != nil {
		return
	}
	defer func() { _ = c.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), eventsFlushTimeout)
	defer cancel()

	svc := client.NewService(c, telemetryv1connect.NewTelemetryServiceClient)
	_, _ = svc.ReportEvents(ctx, connect.NewRequest(&telemetryv1.ReportEventsRequest{
		Events: []*telemetryv1.Event{event},
	}))
}

// topLevelCommand 取命令路径里的顶层命令名：`aladdin galaxy project save` → galaxy。
//
// 取顶层而不是叶子，是为了让取值集合小而稳定：叶子名会在不同子树里重名
// （好几个子树都有 list），而"哪一类命令在失败"用顶层就答得了。
func topLevelCommand(cmd *cobra.Command) string {
	fields := strings.Fields(cmd.CommandPath())
	if len(fields) < 2 {
		return ""
	}
	return fields[1]
}
