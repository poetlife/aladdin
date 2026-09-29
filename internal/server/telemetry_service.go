package server

import (
	"context"

	"connectrpc.com/connect"

	telemetryv1 "github.com/poetlife/aladdin/api/gen/aladdin/telemetry/v1"
	"github.com/poetlife/aladdin/internal/observability"
	"github.com/poetlife/aladdin/internal/server/interceptor"
	"github.com/poetlife/aladdin/internal/telemetry"
)

// TelemetryService 是客户端事件上报的 RPC 实现。
//
// 它是一层薄壳：本包不做任何校验、脱敏与限流——那些都在 internal/telemetry，
// 因此"什么会被写进日志"可以脱离 RPC 单独测试。这里只负责把**协议层才知道的
// 事实**递进去：请求头声明的上报端、会话里的主体、来源地址。
type TelemetryService struct {
	recorder *telemetry.Recorder
}

// NewTelemetryService 构造上报服务。
func NewTelemetryService(recorder *telemetry.Recorder) *TelemetryService {
	return &TelemetryService{recorder: recorder}
}

// ReportEvents 实现 TelemetryService。
//
// **它永远返回成功。** 校验失败、超限、不允许匿名都只是丢弃，不映射成错误：
// 遥测的数据问题一旦升级成客户端的业务错误，一次清单不一致就会让前端弹框、
// 让 CLI 非零退出——那是"观测影响业务"最不该有的形状。丢弃与否由服务端日志
// 回答（见 internal/telemetry 的 DEBUG 记录）。
func (s *TelemetryService) ReportEvents(ctx context.Context, req *connect.Request[telemetryv1.ReportEventsRequest]) (*connect.Response[telemetryv1.ReportEventsResponse], error) {
	// 主体是**可选**的：公开方法在匿名时没有它，这里不按未认证拒绝。
	// 认证中间件对公开路径尽力识别，带了有效凭证才会有值（见 middleware.go）。
	var subjectID string
	if subject, ok := interceptor.SubjectFromContext(ctx); ok {
		subjectID = subject.ID
	}
	client, _ := observability.ClientFromHeader(req.Header())

	s.recorder.Report(ctx, telemetry.Request{
		HeaderClient: client,
		SubjectID:    subjectID,
		Source:       req.Peer().Addr,
		Events:       req.Msg.GetEvents(),
	})
	return connect.NewResponse(&telemetryv1.ReportEventsResponse{}), nil
}
