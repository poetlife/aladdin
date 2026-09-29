package telemetry

import (
	telemetryv1 "github.com/poetlife/aladdin/api/gen/aladdin/telemetry/v1"
	"github.com/poetlife/aladdin/internal/observability"
)

// maxDurationMS 是一条事件允许声明的耗时上限（1 小时）。
//
// 超过它的取值按"未提供"处理，而不是截断到上限：一个荒谬的耗时说明客户端算错
// 了，记成 1 小时只会让聚合结果多一个看似合理的假点。
const maxDurationMS = 60 * 60 * 1000

// 丢弃原因。它们是服务端 DEBUG 日志里的取值，也是测试的断言点——
// 让"为什么这条被丢了"可回答，而不是只剩一个静默的减少。
const (
	reasonUnknownAction      = "unknown_action"
	reasonAnonymousForbidden = "anonymous_forbidden"
	reasonUnknownSurface     = "unknown_surface"
	reasonUnknownResult      = "unknown_result"
	reasonUnknownClient      = "unknown_client"
	reasonClientMismatch     = "client_mismatch"
)

// Record 是一条通过校验与脱敏、可以落盘的事件。
//
// 所有字段都是**有界取值**：枚举折算出的名字、校验过的 trace_id、来自会话的
// subject_id，以及通过白名单与取值域筛过的属性。没有任何一处来自用户输入。
type Record struct {
	Client     string
	Surface    string
	Action     string
	Result     string
	DurationMS uint32
	TraceID    string
	SubjectID  string
	Attrs      map[string]string
}

// normalize 校验并脱敏一条事件。
//
// 返回 ok=false 时第二项是丢弃原因（见上面那组常量），调用方据此记一行 DEBUG。
// **任何不合规都不返回错误**：遥测的数据问题绝不能升级成客户端的业务错误。
//
// headerClient 是本次请求头里声明的上报端（可能为空）。事件自报的 client 与它
// 不一致时丢弃整条：两处都写着"我是谁"，对不上说明上报端有缺陷，与其猜一个
// 不如不记——记录一个不可信的归属比缺一条数据更坏。
func normalize(ev *telemetryv1.Event, subjectID, headerClient string) (Record, string, bool) {
	if ev == nil {
		return Record{}, reasonUnknownAction, false
	}

	policy, ok := actionPolicies[ev.GetAction()]
	if !ok {
		return Record{}, reasonUnknownAction, false
	}
	if subjectID == "" && !policy.anonymous {
		return Record{}, reasonAnonymousForbidden, false
	}

	surface, ok := surfaceNames[ev.GetSurface()]
	if !ok {
		return Record{}, reasonUnknownSurface, false
	}
	result, ok := resultNames[ev.GetResult()]
	if !ok {
		return Record{}, reasonUnknownResult, false
	}
	client, ok := clientName(ev.GetClient())
	if !ok {
		return Record{}, reasonUnknownClient, false
	}
	if headerClient != "" && headerClient != client {
		return Record{}, reasonClientMismatch, false
	}

	rec := Record{
		Client:    client,
		Surface:   surface,
		Action:    policy.name,
		Result:    result,
		SubjectID: subjectID,
		TraceID:   ev.GetTraceId(),
		Attrs:     sanitizeAttrs(policy, ev.GetAttrs()),
	}

	// trace_id 不合规按"未提供"处理（同 docs/observability.md 对入站 traceparent
	// 的校验）：不校验就写进日志，会破坏所有基于它的检索。
	if !observability.ValidTraceID(rec.TraceID) {
		rec.TraceID = ""
	}
	// 耗时越界按未提供处理，理由见 maxDurationMS。
	if ms := ev.GetDurationMs(); ms <= maxDurationMS {
		rec.DurationMS = ms
	}
	return rec, "", true
}

// sanitizeAttrs 按白名单键与取值域筛出可保留的属性。
//
// 它是**允许清单**而不是禁用清单：没登记过的键一律不保留。禁用清单在这里是
// 错的做法——"不许写邮箱"挡不住明天新加的字段，而允许清单默认拒绝任何没被
// 显式放进来的东西。
func sanitizeAttrs(policy actionPolicy, attrs map[string]string) map[string]string {
	if len(attrs) == 0 || len(policy.attrs) == 0 {
		return nil
	}
	allowed := make(map[string]bool, len(policy.attrs))
	for _, key := range policy.attrs {
		allowed[key] = true
	}

	out := make(map[string]string, len(attrs))
	for key, value := range attrs {
		if !allowed[key] {
			continue
		}
		normalized, ok := validators[key](value)
		if !ok {
			continue
		}
		out[key] = normalized
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// attrValidator 校验并归一化一个属性取值，第二项为假表示丢弃该键。
type attrValidator func(string) (string, bool)

// validators 是属性键到取值域的映射。**每个键都必须在这里有一条**，否则
// sanitizeAttrs 会取到 nil 而 panic——那正是"新增一个白名单键必须同时给它
// 一条取值域"这条约束的执行方式。
var validators = map[string]attrValidator{
	attrChannel: oneOf("password", "google", "github"),
	attrReason:  oneOf("config", "credential", "usage", "other"),
	attrCommand: cliCommandName,
}

// oneOf 构造一个"取值必须在给定集合内"的校验器。
func oneOf(values ...string) attrValidator {
	set := make(map[string]bool, len(values))
	for _, v := range values {
		set[v] = true
	}
	return func(value string) (string, bool) {
		if !set[value] {
			return "", false
		}
		return value, true
	}
}

// cliCommandName 校验命令行的顶层命令名。
//
// 它不是自由文本：取值只能来自命令行自己的命令表（galaxy、login、whoami…），
// 因此只需一个形状约束就能把它钉在有界集合里。校验失败即丢弃，不做任何截断——
// 一个被截断的命令名会变成另一个合法命令。
func cliCommandName(value string) (string, bool) {
	if len(value) == 0 || len(value) > 32 {
		return "", false
	}
	for i := 0; i < len(value); i++ {
		c := value[i]
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' {
			continue
		}
		return "", false
	}
	return value, true
}
