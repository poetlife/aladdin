// Package telemetry 接收并落盘**客户端事件**：那些不经过任何 RPC 的本地动作。
//
// 它是服务端侧对协议无关的那部分：动作允许清单、字段校验、属性脱敏、限流、
// 结构化日志。RPC 那一层（谁调用的、请求头怎么读）在 internal/server 的
// TelemetryService 里，本包不认识 Connect。
//
// 行为约定见 docs/observability.md 的「客户端事件」一节。这里的表与那里的
// 清单必须一致；不一致时以文档为准并同步。
package telemetry

import (
	telemetryv1 "github.com/poetlife/aladdin/api/gen/aladdin/telemetry/v1"
	"github.com/poetlife/aladdin/internal/observability"
)

// 属性键。取值受白名单约束：只有登记在这里的键会被保留，且每个键的值还要
// 落在自己的取值域内（见 sanitize.go）。
const (
	attrChannel = "channel"
	attrCommand = "command"
	attrReason  = "reason"
)

// actionPolicy 是一个动作码在服务端的策略。
type actionPolicy struct {
	// name 是日志与聚合查询里使用的稳定取值（点分小写）。
	//
	// 它是**对外契约**：告警、看板、查询都按它分组，不要随文案调整而变。
	// 用点分小写而不是 proto 的枚举名，是为了让人能直接 grep。
	name string
	// anonymous 为真时，未识别主体的调用方也允许上报这个动作。
	//
	// 只有发生在"拿到会话之前"的动作才允许匿名：登录页上的失败、命令行
	// 未登录就退出。其余动作在匿名时一律丢弃——它们本就不该由匿名调用方产生，
	// 放行只会给日志注入噪声。
	anonymous bool
	// attrs 是这个动作允许携带的属性键。其余键一律剥除。
	attrs []string
}

// actionPolicies 是动作允许清单的策略表。
//
// **清单之外的动作一律丢弃**（见 normalize）。这里的每个键都必须能在
// telemetryv1.Action 里找到，完整性由 TestEveryActionHasPolicy 守住。
var actionPolicies = map[telemetryv1.Action]actionPolicy{
	telemetryv1.Action_ACTION_AUTH_LOGIN: {
		name:      "auth.login",
		anonymous: true,
		attrs:     []string{attrChannel},
	},
	telemetryv1.Action_ACTION_PROJECT_LIST_OPEN: {name: "project_list.open"},
	telemetryv1.Action_ACTION_EDITOR_OPEN:       {name: "editor.open"},
	telemetryv1.Action_ACTION_DRAFT_SAVE:        {name: "draft.save"},
	telemetryv1.Action_ACTION_PUBLISH:           {name: "publish"},
	telemetryv1.Action_ACTION_UNPUBLISH:         {name: "unpublish"},
	telemetryv1.Action_ACTION_PROJECT_DELETE:    {name: "project.delete"},
	telemetryv1.Action_ACTION_PREVIEW_TOGGLE:    {name: "preview.toggle"},
	telemetryv1.Action_ACTION_PREVIEW_OPEN:      {name: "preview.open"},
	telemetryv1.Action_ACTION_ASSETS_OPEN:       {name: "assets.open"},
	telemetryv1.Action_ACTION_VERSIONS_OPEN:     {name: "versions.open"},
	telemetryv1.Action_ACTION_ASSET_META_SAVE:   {name: "asset_meta.save"},
	telemetryv1.Action_ACTION_ASSET_UPLOAD:      {name: "asset.upload"},
	telemetryv1.Action_ACTION_DRAFT_RESTORE:     {name: "draft.restore"},
	telemetryv1.Action_ACTION_CLI_LOCAL_FAIL: {
		name:      "cli.local_fail",
		anonymous: true,
		attrs:     []string{attrCommand, attrReason},
	},
}

// surfaceNames 是界面/入口的日志取值。
var surfaceNames = map[telemetryv1.Surface]string{
	telemetryv1.Surface_SURFACE_WEB_AUTH:         "web.auth",
	telemetryv1.Surface_SURFACE_WEB_PROJECT_LIST: "web.project_list",
	telemetryv1.Surface_SURFACE_WEB_EDITOR:       "web.editor",
	telemetryv1.Surface_SURFACE_WEB_PREVIEW:      "web.preview",
	telemetryv1.Surface_SURFACE_CLI:              "cli",
}

// resultNames 是结局的日志取值。
var resultNames = map[telemetryv1.Result]string{
	telemetryv1.Result_RESULT_OK:      "ok",
	telemetryv1.Result_RESULT_FAIL:    "fail",
	telemetryv1.Result_RESULT_CANCEL:  "cancel",
	telemetryv1.Result_RESULT_BLOCKED: "blocked",
}

// clientName 把事件的 client 枚举折算成上报端取值。
//
// 取值与 observability 的 ClientWeb / ClientCLI 同源——它同时是请求头
// `x-aladdin-client` 的取值，两者必须一致（见 observability.HeaderClient）。
func clientName(c telemetryv1.Client) (string, bool) {
	switch c {
	case telemetryv1.Client_CLIENT_WEB:
		return observability.ClientWeb, true
	case telemetryv1.Client_CLIENT_CLI:
		return observability.ClientCLI, true
	default:
		return "", false
	}
}
