package server

import (
	"context"
	"errors"
	"time"

	"connectrpc.com/connect"
	"go.uber.org/zap"

	telemetryv1 "github.com/poetlife/aladdin/api/gen/aladdin/telemetry/v1"
	"github.com/poetlife/aladdin/internal/profile"
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
	// profiles 用于把明细里的主体标识解析成展示名与头像。为 nil 表示不做解析，
	// 事件的 subject_id 原样返回（测试与未装配的调用方用得上）。
	//
	// 它**只读**：读侧把标识换成展示信息，不写档案、更不把展示信息写回事件
	// （见 telemetry_admin.proto 的三条边界）。
	profiles *profile.Profiles
	// logger 记录"影响展示、但不值得让整个请求失败"的降级事件。为空时丢弃。
	logger *zap.Logger
	// now 可注入，测试用。
	now func() time.Time
}

// NewTelemetryAdminService 构造只读管理服务。
//
// profiles 为 nil 表示这个部署不做主体解析，logger 为 nil 表示不留痕——两者都
// 只应在测试里这么用。
func NewTelemetryAdminService(store telemetry.Store, profiles *profile.Profiles, logger *zap.Logger) *TelemetryAdminService {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &TelemetryAdminService{store: store, profiles: profiles, logger: logger, now: time.Now}
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
	subjectIDs := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, toProtoRecentEvent(e))
		// 空标识是匿名，不是"这个主体没有名字"——它不占 subjects 的键。
		if e.SubjectID != "" {
			subjectIDs = append(subjectIDs, e.SubjectID)
		}
	}
	return connect.NewResponse(&telemetryv1.ListRecentEventsResponse{
		Events:   out,
		Subjects: s.subjectProfiles(ctx, subjectIDs),
	}), nil
}

// subjectProfiles 把本页出现的主体标识解析成展示信息。
//
// **它是读侧的附加投影，不是事件的一部分**：事件仍然只存标识，昵称与头像不落库、
// 不进日志（见 docs/observability.md 的「脱敏硬规则」）。解析用档案模块那唯一的
// 一套回退规则，本包不自己拼名字。
//
// 三条边界写在这个函数里：
//
//   - 去重交给 profile.GetMany——同一页里一个人常常出现很多次，逐条查会让"打开
//     一页"变成几十次主键查；
//   - 失败**降级不失败**：档案存储抖动时事件照常返回、这个字段为空，界面上回退成
//     只显示标识。排障要看的第一件事是事件本身，不能因为一张头像把整页打成错误；
//   - 权限不在这里判：整个方法已由注解要求 `telemetry.read`，多一道判定就是多一处
//     会与注解漂移的地方。
func (s *TelemetryAdminService) subjectProfiles(ctx context.Context, subjectIDs []string) map[string]*telemetryv1.SubjectProfile {
	if s.profiles == nil || len(subjectIDs) == 0 {
		return nil
	}
	views, err := s.profiles.GetMany(ctx, subjectIDs)
	if err != nil {
		s.logger.Warn("主体展示信息解析失败，明细照常返回、主体列回退到标识",
			zap.Int("subjects", len(subjectIDs)), zap.Error(err))
		return nil
	}
	out := make(map[string]*telemetryv1.SubjectProfile, len(views))
	for subjectID, view := range views {
		out[subjectID] = &telemetryv1.SubjectProfile{
			DisplayName: view.DisplayName,
			AvatarUrl:   view.AvatarURL,
		}
	}
	return out
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
