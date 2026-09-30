package server

import (
	"context"
	"errors"
	"time"

	"connectrpc.com/connect"

	telemetryv1 "github.com/poetlife/aladdin/api/gen/aladdin/telemetry/v1"
	"github.com/poetlife/aladdin/internal/telemetry"
)

// 明细一页的默认与上限条数。
//
// 上限存在的意义不是省资源，而是**让"最近发生了什么"这个问题的答案有界**：
// 管理页不是日志检索界面，没有上限的话，一次误操作就能把整张表拉进浏览器。
// 需要翻完历史请去日志系统（见 docs/observability.md）。
const (
	defaultRecentLimit = 50
	maxRecentLimit     = 200
)

// TelemetryAdminService 是客户端事件**只读管理面**的 RPC 实现。
//
// 它是一层薄壳：时间窗怎么折算、事件按什么分组都在 internal/telemetry，本包只做
// 协议层的取数与回填。这样"某个窗口对应哪一段时间"是可以脱离 RPC 单独测试的。
//
// 权限与作用域由方法注解声明（见 telemetry_admin.proto），拦截器统一执行，
// 这里不重复判定。
type TelemetryAdminService struct {
	store telemetry.Store
	// now 可注入，测试用。
	now func() time.Time
}

// NewTelemetryAdminService 构造只读管理服务。
func NewTelemetryAdminService(store telemetry.Store) *TelemetryAdminService {
	return &TelemetryAdminService{store: store, now: time.Now}
}

// ListEventStats 实现 TelemetryAdminService。
func (s *TelemetryAdminService) ListEventStats(ctx context.Context, req *connect.Request[telemetryv1.ListEventStatsRequest]) (*connect.Response[telemetryv1.ListEventStatsResponse], error) {
	from, to, err := s.window(req.Msg.GetWindow())
	if err != nil {
		return nil, err
	}
	stats, err := s.store.Stats(ctx, from, to)
	if err != nil {
		return nil, toTelemetryAdminError(err)
	}

	out := make([]*telemetryv1.EventStat, 0, len(stats))
	for _, stat := range stats {
		out = append(out, &telemetryv1.EventStat{
			Client: telemetry.ClientEnum(stat.Client),
			Action: stat.Action,
			Result: telemetry.ResultEnum(stat.Result),
			Count:  stat.Count,
		})
	}
	return connect.NewResponse(&telemetryv1.ListEventStatsResponse{Stats: out}), nil
}

// ListRecentEvents 实现 TelemetryAdminService。
func (s *TelemetryAdminService) ListRecentEvents(ctx context.Context, req *connect.Request[telemetryv1.ListRecentEventsRequest]) (*connect.Response[telemetryv1.ListRecentEventsResponse], error) {
	from, to, err := s.window(req.Msg.GetWindow())
	if err != nil {
		return nil, err
	}
	entries, err := s.store.Recent(ctx, from, to, clampLimit(req.Msg.GetLimit()))
	if err != nil {
		return nil, toTelemetryAdminError(err)
	}

	out := make([]*telemetryv1.RecentEvent, 0, len(entries))
	for _, e := range entries {
		out = append(out, toProtoRecentEvent(e))
	}
	return connect.NewResponse(&telemetryv1.ListRecentEventsResponse{Events: out}), nil
}

// window 把请求里的时间窗折算成区间。
//
// 未知取值**拒绝**而不是回落到默认窗口：静默回落会让一个拼错的取值看起来像是
// 生效了，而"页面上的数字是哪个窗口的"这类问题不该靠猜。
func (s *TelemetryAdminService) window(w telemetryv1.TimeWindow) (from, to time.Time, err error) {
	from, to, ok := telemetry.WindowRange(w, s.now())
	if !ok {
		return time.Time{}, time.Time{}, connect.NewError(
			connect.CodeInvalidArgument, errors.New("未知的时间窗"))
	}
	return from, to, nil
}

// clampLimit 把请求的条数收敛到有界区间：0 表示未指定，取默认值；超过上限按上限。
//
// 不报错：条数是一个"想多要一点"的偏好，不是语义错误，为它打回整个请求没有意义。
func clampLimit(limit uint32) int {
	switch {
	case limit == 0:
		return defaultRecentLimit
	case limit > maxRecentLimit:
		return maxRecentLimit
	default:
		return int(limit)
	}
}

// toProtoRecentEvent 把一条落库事件回填成协议类型。
//
// 字段一一对应，**不多不少**：读侧不得放大字段面（见 telemetry_admin.proto）。
// 时间按仓库既有约定给 RFC3339(UTC)，与 galaxy 各接口一致。
func toProtoRecentEvent(e telemetry.Entry) *telemetryv1.RecentEvent {
	return &telemetryv1.RecentEvent{
		OccurredAt:    e.OccurredAt.UTC().Format(time.RFC3339),
		Client:        telemetry.ClientEnum(e.Client),
		Surface:       telemetry.SurfaceEnum(e.Surface),
		Action:        e.Action,
		Result:        telemetry.ResultEnum(e.Result),
		DurationMs:    e.DurationMS,
		ClientTraceId: e.TraceID,
		SubjectId:     e.SubjectID,
		Attrs:         e.Attrs,
	}
}

// toTelemetryAdminError 把领域错误映射为 Connect 错误码。
//
// 本模块的读取都是集合查询，"查不到"不是一类结果，因此只有"库用不了"要单独
// 映射——它必须让请求快速失败，而不是被读成"最近没有事件"。
func toTelemetryAdminError(err error) error {
	if errors.Is(err, telemetry.ErrStoreUnavailable) {
		return connect.NewError(connect.CodeUnavailable, err)
	}
	return connect.NewError(connect.CodeInternal, err)
}
