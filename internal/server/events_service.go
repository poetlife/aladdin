package server

import (
	"context"
	"fmt"
	"time"

	"connectrpc.com/connect"
	"go.uber.org/zap"

	eventsv1 "github.com/poetlife/aladdin/api/gen/aladdin/events/v1"
	"github.com/poetlife/aladdin/internal/rbac"
	"github.com/poetlife/aladdin/internal/server/interceptor"
	"github.com/poetlife/aladdin/internal/watch"
)

// 一条订阅的节奏与寿命。取值与理由见 docs/design/events/README.md。
const (
	// watchHeartbeat 是心跳间隔，它存在的唯一理由是让反向代理不把空闲连接判死。
	// nginx 的 proxy_read_timeout 默认 60 秒，25 秒留出两倍余量（见
	// deploy/nginx-aladdin-site.conf）。
	watchHeartbeat = 25 * time.Second
	// watchMaxLifetime 是一条订阅的寿命上限。
	//
	// **鉴权只在开流时做一次**，因此这个值就是"凭证被吊销或过期之后，对方最多
	// 还能收多久事件"的上界。到点正常结束，客户端重连时重新鉴权。
	watchMaxLifetime = 30 * time.Minute
)

// WatchDeps 是事件通道的装配依赖。
type WatchDeps struct {
	// Hub 是进程内总线（谁订了什么、往谁推）。必填。
	Hub *watch.Hub
	// Registry 是主题类型的注册表。为空表示这个部署没有任何可订阅的资源。
	Registry *watch.Registry
	// Engine 是判定的**唯一实现**：逐主题的权限检查交给它。
	Engine *rbac.Engine
	// Shutdown 在进程开始优雅退出时被关闭。
	//
	// 订阅是长连接，而 http.Server.Shutdown 不会取消在途请求的 context：不主动
	// 结束它们，每次停服都要等满 shutdownGrace（见 server.go 的
	// RegisterOnShutdown）。
	Shutdown <-chan struct{}
	// Heartbeat / MaxLifetime 为零时取默认值；测试用小值把"心跳"与"寿命到点"
	// 压进用例的时间里。
	Heartbeat   time.Duration
	MaxLifetime time.Duration
}

// EventsService 是**连接级**事件通道的 RPC 实现：一条服务端流承载多个主题。
//
// **它不认识任何业务**：哪个主题可订阅、订阅它要什么权限、谁算这个资源的属主，
// 全在 watch.Registry 的注册项里——那些注册项由属主模块在装配处登记（见
// server.go 里 galaxy 的那一条）。这里只做四件事：逐主题解析与判权、订阅、
// 推事件、按节奏与寿命收摊。
type EventsService struct {
	deps   WatchDeps
	logger *zap.Logger
}

// NewEventsService 构造事件通道。
func NewEventsService(deps WatchDeps, logger *zap.Logger) *EventsService {
	if deps.Heartbeat <= 0 {
		deps.Heartbeat = watchHeartbeat
	}
	if deps.MaxLifetime <= 0 {
		deps.MaxLifetime = watchMaxLifetime
	}
	return &EventsService{deps: deps, logger: logger}
}

// Watch 实现 EventsService：订阅一组主题，之后它们上面的变化从这同一条流推过来。
//
// 顺序是刻意的：**先逐主题判权与归属、再订阅、最后才推 RESYNC**。任何一条主题
// 没通过，整条流以对应的错误结束——静默少订一条会让调用方以为自己是最新的，
// 而那是这条通道要消灭的状态。
func (s *EventsService) Watch(ctx context.Context, req *connect.Request[eventsv1.WatchRequest], stream *connect.ServerStream[eventsv1.Event]) error {
	subject, err := callerSubject(ctx)
	if err != nil {
		return err
	}
	topics, err := s.authorizeTopics(ctx, subject, req.Msg.GetTopics())
	if err != nil {
		return err
	}

	sub := s.deps.Hub.Subscribe(topics)
	defer sub.Close()
	if s.logger != nil {
		// 只记条数，不记主题标识：它们是资源标识，进日志没有诊断价值，却会
		// 让日志里出现用户资源的分布。
		s.logger.Info("已开始订阅",
			zap.String("subject_id", subject.ID),
			zap.Int("topics", len(topics)))
	}

	// **每个主题各一条 RESYNC。** 调用方在连接建立之前无从知道"我已经同步到
	// 哪一刻"：「上一次拉取完成」与「订阅建立」之间落下的那次改动没有别的机会
	// 被补上。有了它，交付语义才可以是"至多一次、不重放"。
	for _, topic := range topics {
		if err := sendEvent(stream, topic, eventsv1.Control_CONTROL_RESYNC); err != nil {
			return nil
		}
	}

	heartbeat := time.NewTicker(s.deps.Heartbeat)
	defer heartbeat.Stop()
	expiry := time.NewTimer(s.deps.MaxLifetime)
	defer expiry.Stop()

	for {
		select {
		case <-sub.Ready():
			topic, ok := sub.Take()
			if !ok {
				continue
			}
			if err := sendEvent(stream, topic, eventsv1.Control_CONTROL_UNSPECIFIED); err != nil {
				// 对端走了。这是**一条订阅的正常结局**，不是故障：一个关掉的
				// 标签页不该在服务端留下一条 error 级日志。
				return nil
			}
		case <-sub.Done():
			// 它订的主题都不存在了（资源被删）。**正常结束**：调用方重连、重新
			// 订阅，那时才会拿到"不存在"这个结论——把那条结论塞进这条流会让
			// 一次删除看起来像一次连接故障。
			return nil
		case <-heartbeat.C:
			// 心跳不是一次变更，也不属于任何主题：它是连接级的。
			if err := sendEvent(stream, "", eventsv1.Control_CONTROL_HEARTBEAT); err != nil {
				return nil
			}
		case <-expiry.C:
			// 寿命到点：正常结束。客户端重连时重新鉴权——这就是吊销窗口的上界。
			return nil
		case <-s.deps.Shutdown:
			// 进程要退出：主动收摊，别让每次停服都等满宽限时间。
			// 这个通道为 nil 时这一支永不就绪（未配置关停信号的测试）。
			return nil
		case <-ctx.Done():
			// 对端断开或主动取消。
			return nil
		}
	}
}

// authorizeTopics 逐主题解析、判权、归属，返回去重后的主题。
//
// **权限是逐主题的**：需要哪个权限取决于请求里订的是哪条主题，而那是数据，
// 方法注解表达不了（这是对"权限声明写在方法注解上"那条约定的刻意例外，见
// docs/design/events/README.md）。权限码仍取自权限目录，判定的唯一实现仍是
// rbac.Engine；"这个资源是不是他的"由属主模块的注册项判定。
func (s *EventsService) authorizeTopics(ctx context.Context, subject rbac.Subject, topics []string) ([]string, error) {
	seen := make(map[string]struct{}, len(topics))
	granted := make([]string, 0, len(topics))
	for _, topic := range topics {
		if _, duplicate := seen[topic]; duplicate {
			continue
		}
		seen[topic] = struct{}{}

		entry, id, err := s.deps.Registry.Resolve(topic)
		if err != nil {
			// 拼错的主题或没注册过的类型：改了再试，重试没用。
			return nil, connect.NewError(connect.CodeInvalidArgument,
				fmt.Errorf("无法订阅主题 %q", topic))
		}
		// 作用域取自凭证的默认作用域——与方法注解声明的来源一致。
		decision := s.deps.Engine.Check(ctx, subject, entry.Permission, subject.DefaultScope)
		if !decision.Allowed {
			return nil, interceptor.Reject(decision.Reason)
		}
		// 属主自己的判定，**返回的是能直接发给调用方的错误**（通道不认识任何
		// 领域的错误词表）。"不是他的"与"不存在"在那里是同一个结论。
		if err := entry.Authorize(ctx, subject, id); err != nil {
			return nil, err
		}
		granted = append(granted, topic)
	}
	return granted, nil
}

// sendEvent 是本流**唯一**的发送入口。
//
// 收在一处是为了让"事件里有什么"只有一个答案：主题 + 控制类别。任何往里加内容
// 的改动都必须先改这一处——以及它背后的 spec。
func sendEvent(stream *connect.ServerStream[eventsv1.Event], topic string, control eventsv1.Control) error {
	return stream.Send(&eventsv1.Event{Topic: topic, Control: control})
}
