package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"go.uber.org/zap"

	eventsv1 "github.com/poetlife/aladdin/api/gen/aladdin/events/v1"
	"github.com/poetlife/aladdin/api/gen/aladdin/events/v1/eventsv1connect"
	"github.com/poetlife/aladdin/internal/rbac"
	"github.com/poetlife/aladdin/internal/server/interceptor"
	"github.com/poetlife/aladdin/internal/watch"
)

// 本文件测的是**这条通道本身**：开流先发什么、一条流怎么承载多个主题、什么让它
// 结束、逐主题的拒绝怎么落到调用方手里。
//
// 两件事刻意不在这里测，它们各有归属：
//
//   - **真实协议与真实角色**：开流被拒（没有凭证、没有该主题的权限、不是你的资源）
//     由 test/e2e 用真实鉴权链覆盖，那里同时跑 grpc-go 与 Connect 两条协议；
//   - **总线的语义**（合并、扇出、主题退场）由 internal/watch 的用例覆盖。
//
// 于是这里可以省掉整套鉴权装配，用"中间件直接把主体放进 context"的服务把注意力
// 集中在通道上——除了那两个**刻意**要验的拒绝：未知主题类型，以及逐主题的权限
// 判定（它由引擎给出，这里用"没有任何绑定的主体"造出拒绝）。
const (
	watchTestSubject = "usr_watch"
	watchTestTopic   = "galaxy.project/prj_1"
)

// withCaller 扮演"认证中间件"：它只把主体放进 context。
//
// 认证与判权的真实链路由 test/e2e 覆盖；这里要测的是通道本身。
func withCaller(next http.Handler, subject rbac.Subject) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(interceptor.WithSubject(r.Context(), subject)))
	})
}

// eventsHarness 是一套只挂事件通道的测试服务端。
type eventsHarness struct {
	client   eventsv1connect.EventsServiceClient
	bus      *watch.Hub
	registry *watch.Registry
	store    *rbac.MemoryStore
	shutdown chan struct{}

	closeOnce sync.Once
}

// eventsOptions 是一条订阅的节奏与判定；零值取"基本不发生"（一小时），好让每个
// 用例只被它关心的那一件事打断。
type eventsOptions struct {
	heartbeat   time.Duration
	maxLifetime time.Duration
	authorize   func(ctx context.Context, subject rbac.Subject, id string) error
}

func newEventsHarness(t *testing.T, opts eventsOptions) *eventsHarness {
	t.Helper()

	if opts.heartbeat == 0 {
		opts.heartbeat = time.Hour
	}
	if opts.maxLifetime == 0 {
		opts.maxLifetime = time.Hour
	}
	if opts.authorize == nil {
		opts.authorize = func(context.Context, rbac.Subject, string) error { return nil }
	}

	h := &eventsHarness{
		bus:      watch.NewHub(),
		registry: watch.NewRegistry(),
		store:    rbac.NewMemoryStore(),
		shutdown: make(chan struct{}),
	}
	h.registry.Register("galaxy.project", watch.Kind{
		Permission: rbac.PermissionGalaxyProjectRead,
		Authorize:  opts.authorize,
	})

	// 主体要在库里：引擎对一个"不存在的主体"给的是"凭证失效"（Unauthenticated），
	// 而这里要测的是**已确认主体的权限**（PermissionDenied）。两者的区分本身是
	// 硬性约定 5，不在这里重复验。
	subject := rbac.Subject{ID: watchTestSubject, Type: rbac.SubjectTypeUser, DefaultScope: "default"}
	if err := h.store.PutSubject(context.Background(), subject); err != nil {
		t.Fatalf("注入主体失败: %v", err)
	}

	// 引擎挂在**空绑定**的存储上：默认没有任何授权，因此"没有该主题的权限"这条
	// 路径用默认夹具就能造出来（需要权限的用例自己补一条绑定）。
	svc := NewEventsService(WatchDeps{
		Hub:         h.bus,
		Registry:    h.registry,
		Engine:      rbac.NewEngine(h.store, zap.NewNop(), nil),
		Shutdown:    h.shutdown,
		Heartbeat:   opts.heartbeat,
		MaxLifetime: opts.maxLifetime,
	}, zap.NewNop())

	path, handler := eventsv1connect.NewEventsServiceHandler(svc)
	mux := http.NewServeMux()
	mux.Handle(path, handler)
	server := httptest.NewServer(withCaller(mux, subject))
	t.Cleanup(func() {
		h.close()
		server.Close()
	})
	h.client = eventsv1connect.NewEventsServiceClient(server.Client(), server.URL)
	return h
}

// grantRead 给测试主体一条含读取权限的绑定。
func (h *eventsHarness) grantRead(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	if err := h.store.PutRole(ctx, rbac.RoleDefinition{
		ID: "测试角色", Permissions: []rbac.PermissionCode{rbac.PermissionGalaxyProjectRead},
	}); err != nil {
		t.Fatalf("注入角色失败: %v", err)
	}
	if err := h.store.Bind(ctx, rbac.RoleBinding{
		SubjectID: watchTestSubject, RoleID: "测试角色", Scope: "default",
	}); err != nil {
		t.Fatalf("注入绑定失败: %v", err)
	}
}

// open 开一条订阅（不等待任何消息）。
func (h *eventsHarness) open(t *testing.T, topics ...string) *connect.ServerStreamForClient[eventsv1.Event] {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	stream, err := h.client.Watch(ctx, connect.NewRequest(&eventsv1.WatchRequest{Topics: topics}))
	if err != nil {
		t.Fatalf("开流失败: %v", err)
	}
	t.Cleanup(func() { _ = stream.Close() })
	return stream
}

// next 取下一条事件；流结束则终止用例。
func next(t *testing.T, stream *connect.ServerStreamForClient[eventsv1.Event]) *eventsv1.Event {
	t.Helper()
	if !stream.Receive() {
		t.Fatalf("流提前结束：%v", stream.Err())
	}
	return stream.Msg()
}

// close 触发关停信号（幂等：多个用例清理时会各调一次）。
func (h *eventsHarness) close() {
	h.closeOnce.Do(func() { close(h.shutdown) })
}

// **订阅的每个主题各一条 RESYNC。**
//
// 少了它，调用方在连接建立之前无从知道"我已经同步到哪一刻"——"上一次拉取完成"
// 与"订阅建立"之间落下的那次改动没有别的机会被补上。
func TestWatchSendsResyncPerTopic(t *testing.T) {
	h := newEventsHarness(t, eventsOptions{})
	h.grantRead(t)
	stream := h.open(t, "galaxy.project/甲", "galaxy.project/乙")

	seen := map[string]bool{}
	for range 2 {
		event := next(t, stream)
		if event.GetControl() != eventsv1.Control_CONTROL_RESYNC {
			t.Fatalf("前两条里有非 RESYNC 的事件：%v", event.GetControl())
		}
		seen[event.GetTopic()] = true
	}
	if !seen["galaxy.project/甲"] || !seen["galaxy.project/乙"] {
		t.Errorf("RESYNC 覆盖的主题 = %v，期望两条主题各一条", seen)
	}
}

// **一条连接承载多个主题**，而事件只关于它自己那条主题。
func TestWatchCarriesEveryTopicOnOneStream(t *testing.T) {
	h := newEventsHarness(t, eventsOptions{})
	h.grantRead(t)
	stream := h.open(t, "galaxy.project/甲", "galaxy.project/乙")
	next(t, stream) // 甲 的 RESYNC
	next(t, stream) // 乙 的 RESYNC

	h.bus.Publish("galaxy.project/乙")

	event := next(t, stream)
	if event.GetTopic() != "galaxy.project/乙" {
		t.Errorf("事件的主题 = %q，期望 %q", event.GetTopic(), "galaxy.project/乙")
	}
	if event.GetControl() != eventsv1.Control_CONTROL_UNSPECIFIED {
		t.Errorf("变更事件的控制类别 = %v，期望 UNSPECIFIED（它就是「变了」）", event.GetControl())
	}
}

// 心跳周期性到达：它不属于任何主题，且它的唯一用途是让反向代理不把空闲连接判死。
func TestWatchSendsHeartbeats(t *testing.T) {
	h := newEventsHarness(t, eventsOptions{heartbeat: 20 * time.Millisecond})
	h.grantRead(t)
	stream := h.open(t, watchTestTopic)
	next(t, stream) // RESYNC

	event := next(t, stream)
	if event.GetControl() != eventsv1.Control_CONTROL_HEARTBEAT {
		t.Errorf("第二条的控制类别 = %v，期望 HEARTBEAT", event.GetControl())
	}
	if event.GetTopic() != "" {
		t.Errorf("心跳带了主题 %q，它是连接级的、不属于任何主题", event.GetTopic())
	}
}

// 寿命到点，流**正常结束**（不是出错）：客户端会重连，而重连时重新鉴权——这就是
// "凭证被吊销后暴露窗口不超过寿命上限"那条保证的落点。
func TestWatchEndsAtMaxLifetime(t *testing.T) {
	h := newEventsHarness(t, eventsOptions{maxLifetime: 50 * time.Millisecond})
	h.grantRead(t)
	stream := h.open(t, watchTestTopic)
	next(t, stream) // RESYNC

	deadline := time.Now().Add(5 * time.Second)
	for stream.Receive() {
		if time.Now().After(deadline) {
			t.Fatal("寿命到点后流仍未结束")
		}
	}
	if err := stream.Err(); err != nil {
		t.Errorf("寿命到点的流以 %v 结束，期望正常结束（客户端据此安静地重连）", err)
	}
}

// 停服时订阅主动收摊：不这样做，每次停服都要等满宽限时间才把长连接掐掉。
func TestWatchEndsOnShutdown(t *testing.T) {
	h := newEventsHarness(t, eventsOptions{})
	h.grantRead(t)
	stream := h.open(t, watchTestTopic)
	next(t, stream) // RESYNC

	h.close()

	deadline := time.Now().Add(5 * time.Second)
	for stream.Receive() {
		if time.Now().After(deadline) {
			t.Fatal("关停后流仍未结束")
		}
	}
	if err := stream.Err(); err != nil {
		t.Errorf("关停时流以 %v 结束，期望正常结束", err)
	}
}

// 订阅一个没注册过的主题类型：**改了再试，重试没用**（InvalidArgument）。
func TestWatchRejectsUnknownTopicKind(t *testing.T) {
	h := newEventsHarness(t, eventsOptions{})
	h.grantRead(t)
	stream := h.open(t, "identity.device/dev_1")

	if stream.Receive() {
		t.Fatal("订阅一个没注册过的主题类型却成功了")
	}
	if err := stream.Err(); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("拒因 = %v（码 %v），期望 InvalidArgument", err, connect.CodeOf(err))
	}
}

// **没有该主题的权限**：判定的唯一实现仍然是引擎，码是 PermissionDenied（不是
// Unauthenticated——把两者混起来会让一次权限配置错误变成一次登录风暴）。
func TestWatchRejectsWithoutTopicPermission(t *testing.T) {
	// 空存储：这个主体没有任何绑定，因此没有 galaxy.project.read。
	h := newEventsHarness(t, eventsOptions{})
	stream := h.open(t, watchTestTopic)

	if stream.Receive() {
		t.Fatal("没有权限却订阅成功了")
	}
	if err := stream.Err(); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("拒因 = %v（码 %v），期望 PermissionDenied", err, connect.CodeOf(err))
	}
}

// 属主判定失败时，**通道原样把属主的错误交给调用方**：它不认识任何领域的错误
// 词表，因此"不是你的"与"不存在"那些结论全由属主自己给出（这里用一个自定义错误
// 证明它没有被改写）。
func TestWatchPassesTheOwnersErrorThrough(t *testing.T) {
	ownerErr := errors.New("这件东西不存在")
	h := newEventsHarness(t, eventsOptions{
		authorize: func(context.Context, rbac.Subject, string) error {
			return connect.NewError(connect.CodeNotFound, ownerErr)
		},
	})
	h.grantRead(t)
	stream := h.open(t, watchTestTopic)

	if stream.Receive() {
		t.Fatal("归属判定拒绝了，订阅却成功了")
	}
	err := stream.Err()
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("拒因 = %v（码 %v），期望属主给的 NotFound", err, connect.CodeOf(err))
	}
	if message := err.Error(); !strings.Contains(message, ownerErr.Error()) {
		t.Errorf("拒因 %q 没有原样带上属主的说明 %q", message, ownerErr.Error())
	}
}

// 同一个主题订两次没有意义：它只该得到一条 RESYNC。
func TestWatchDeduplicatesRepeatedTopics(t *testing.T) {
	h := newEventsHarness(t, eventsOptions{})
	h.grantRead(t)
	stream := h.open(t, watchTestTopic, watchTestTopic)

	if event := next(t, stream); event.GetTopic() != watchTestTopic {
		t.Fatalf("第一条的主题 = %q，期望 %q", event.GetTopic(), watchTestTopic)
	}
	// 用"推一条变更"来判断有没有第二条 RESYNC。
	h.bus.Publish(watchTestTopic)
	if event := next(t, stream); event.GetControl() != eventsv1.Control_CONTROL_UNSPECIFIED {
		t.Errorf("第二条的控制类别 = %v，期望直接是变更事件（重复的主题不该带来第二条 RESYNC）", event.GetControl())
	}
}
