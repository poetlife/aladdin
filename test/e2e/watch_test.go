//go:build e2e

package e2e

import (
	"context"
	"net/http"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/grpc"
	"google.golang.org/grpc/status"

	eventsv1 "github.com/poetlife/aladdin/api/gen/aladdin/events/v1"
	"github.com/poetlife/aladdin/api/gen/aladdin/events/v1/eventsv1connect"
	galaxyv1 "github.com/poetlife/aladdin/api/gen/aladdin/galaxy/v1"
	"github.com/poetlife/aladdin/internal/galaxy"
	"github.com/poetlife/aladdin/internal/rbac"
)

// 本文件端到端验证**事件通道**（本仓库唯一一条服务端流，也是唯一一条连接级的
// 通道：一条流承载多个主题）。
//
// 它与别处的分工：internal/server 的用例验证这条通道本身的形状（先发什么、什么让
// 它结束），internal/watch 验证总线与主题注册表。这里验证的是**接入**——同一个
// 承诺在 grpc-go 与 Connect 两条协议上是否给出同一个结论（见 AGENTS.md 的"传输
// 方式的既定选择"），以及真实鉴权链在这条新形状上是否照常成立。
//
// 流式方法值得单列一条的理由：它走的是与 unary 不同的 handler 与不同的拦截器入口
// （WrapUnary 之外还有 WrapStreamingHandler）。只测 unary 等于没测这条。

// watchCallTimeout 是一条订阅在用例里的时限。它不是被测对象，只是防止用例挂死。
const watchCallTimeout = 30 * time.Second

// watcher 是"一条已开的订阅"：两条协议的客户端各有各的形状，这里只留"收下一条"。
type watcher interface {
	next(t *testing.T) *eventsv1.Event
}

// watchProtocol 是事件通道在一条协议上的接入方式。
type watchProtocol struct {
	name string
	// open 开一条订阅；被拒即终止用例。
	open func(t *testing.T, h harness, token string, topics ...string) watcher
	// refuse 开一条订阅并**返回它的拒因**（归一成 connect.Code：gRPC 的
	// codes.Code 与它取值一一对应）。两条协议把错误抛在不同位置——有的在调用时
	// 返回、有的要读第一条才拿到——这个函数把两处都收成同一个返回值。
	refuse func(t *testing.T, h harness, token string, topics ...string) connect.Code
}

func watchProtocols() []watchProtocol {
	return []watchProtocol{
		{
			name: "Connect",
			open: func(t *testing.T, h harness, token string, topics ...string) watcher {
				t.Helper()
				ctx, cancel := context.WithTimeout(context.Background(), watchCallTimeout)
				t.Cleanup(cancel)
				stream, err := connectEventsClient(t, h, token).Watch(ctx, connect.NewRequest(&eventsv1.WatchRequest{Topics: topics}))
				if err != nil {
					t.Fatalf("开流失败: %v", err)
				}
				t.Cleanup(func() { _ = stream.Close() })
				return &connectWatcher{stream: stream}
			},
			refuse: func(t *testing.T, h harness, token string, topics ...string) connect.Code {
				t.Helper()
				ctx, cancel := context.WithTimeout(context.Background(), watchCallTimeout)
				defer cancel()
				stream, err := connectEventsClient(t, h, token).Watch(ctx, connect.NewRequest(&eventsv1.WatchRequest{Topics: topics}))
				if err != nil {
					return connect.CodeOf(err)
				}
				if stream.Receive() {
					t.Fatal("这条订阅本该被拒，却收到了事件")
				}
				return connect.CodeOf(stream.Err())
			},
		},
		{
			name: "gRPC",
			open: func(t *testing.T, h harness, token string, topics ...string) watcher {
				t.Helper()
				ctx, cancel := context.WithTimeout(context.Background(), watchCallTimeout)
				t.Cleanup(cancel)
				stream, err := grpcEventsClient(t, h, token).Watch(ctx, &eventsv1.WatchRequest{Topics: topics})
				if err != nil {
					t.Fatalf("开流失败: %v", err)
				}
				return &grpcWatcher{stream: stream}
			},
			refuse: func(t *testing.T, h harness, token string, topics ...string) connect.Code {
				t.Helper()
				ctx, cancel := context.WithTimeout(context.Background(), watchCallTimeout)
				defer cancel()
				stream, err := grpcEventsClient(t, h, token).Watch(ctx, &eventsv1.WatchRequest{Topics: topics})
				if err == nil {
					_, err = stream.Recv()
				}
				if err == nil {
					t.Fatal("这条订阅本该被拒，却收到了事件")
				}
				return connect.Code(status.Code(err))
			},
		},
	}
}

// connectEventsClient 构造一个**不带整体超时**的 Connect 客户端。
//
// 其它 helper 的客户端设了 5 秒的 http.Client.Timeout，而它覆盖整条响应体的读取
// ——用在长连接上会在 5 秒时把流掐断。订阅的时限交给 context。
func connectEventsClient(t *testing.T, h harness, token string) eventsv1connect.EventsServiceClient {
	t.Helper()
	return eventsv1connect.NewEventsServiceClient(
		&http.Client{Transport: &headerTransport{base: http.DefaultTransport, token: token}},
		"http://"+h.address,
	)
}

// grpcEventsClient 构造一个走原生 gRPC 的通道客户端（凭证由连接上的两个拦截器注入）。
func grpcEventsClient(t *testing.T, h harness, token string) eventsv1.EventsServiceClient {
	t.Helper()
	return eventsv1.NewEventsServiceClient(h.dial(t, token, "").Conn())
}

type connectWatcher struct {
	stream *connect.ServerStreamForClient[eventsv1.Event]
}

func (w *connectWatcher) next(t *testing.T) *eventsv1.Event {
	t.Helper()
	if !w.stream.Receive() {
		t.Fatalf("流提前结束: %v", w.stream.Err())
	}
	return w.stream.Msg()
}

type grpcWatcher struct {
	stream grpc.ServerStreamingClient[eventsv1.Event]
}

func (w *grpcWatcher) next(t *testing.T) *eventsv1.Event {
	t.Helper()
	event, err := w.stream.Recv()
	if err != nil {
		t.Fatalf("流提前结束: %v", err)
	}
	return event
}

// **两条协议给出同一个结论**：订阅之后先收到该主题的 RESYNC，别处改完这个工程
// 再收到一条变更事件。
//
// 这正是验收标准里那条"工作台开着时命令行 push，不刷新页面就能看见"在服务端的
// 一半：事件真的从写路径推到了订阅者手上。
func TestWatchSeesChangesOverBothProtocols(t *testing.T) {
	for _, protocol := range watchProtocols() {
		t.Run(protocol.name, func(t *testing.T) {
			h := startServer(t, rbac.RoleGalaxyAuthor, testScope)
			client := connectGalaxy(t, h, testToken)
			projectID := createProject(t, client, "订阅", galaxyv1.SiteForm_SITE_FORM_STATIC)
			topic := galaxy.ProjectTopic(projectID)

			watch := protocol.open(t, h, testToken, topic)

			// 第一条必定是该主题的 RESYNC：调用方靠它做一次全量重拉。
			first := watch.next(t)
			if first.GetTopic() != topic {
				t.Errorf("事件的主题 = %q，期望 %q", first.GetTopic(), topic)
			}
			if first.GetControl() != eventsv1.Control_CONTROL_RESYNC {
				t.Fatalf("订阅后的第一条 = %v，期望 RESYNC", first.GetControl())
			}

			// 别处（这里扮演命令行）改了这个工程。
			entry := pushContentOverRPC(t, h, client, projectID, "index.html", "<h1>改过了</h1>")
			pushDraft(t, client, projectID, entry)

			event := watch.next(t)
			if event.GetTopic() != topic {
				t.Errorf("变更事件的主题 = %q，期望 %q", event.GetTopic(), topic)
			}
			if event.GetControl() != eventsv1.Control_CONTROL_UNSPECIFIED {
				t.Errorf("变更事件的控制类别 = %v，期望 UNSPECIFIED", event.GetControl())
			}
		})
	}
}

// **一条连接承载多个主题**：订两个工程，改其中一个，只有那一个主题的事件过来。
func TestWatchCarriesMultipleTopicsOnOneStream(t *testing.T) {
	h := startServer(t, rbac.RoleGalaxyAuthor, testScope)
	client := connectGalaxy(t, h, testToken)
	first := createProject(t, client, "甲", galaxyv1.SiteForm_SITE_FORM_STATIC)
	second := createProject(t, client, "乙", galaxyv1.SiteForm_SITE_FORM_STATIC)

	watch := watchProtocols()[0].open(t, h, testToken, galaxy.ProjectTopic(first), galaxy.ProjectTopic(second))
	watch.next(t) // 甲 的 RESYNC
	watch.next(t) // 乙 的 RESYNC

	// 只改第二个：这条流该收到第二个主题的事实。
	entry := pushContentOverRPC(t, h, client, second, "index.html", "<h1>乙</h1>")
	pushDraft(t, client, second, entry)

	event := watch.next(t)
	if event.GetTopic() != galaxy.ProjectTopic(second) {
		t.Errorf("事件的主题 = %q，期望 %q", event.GetTopic(), galaxy.ProjectTopic(second))
	}
}

// **逐主题的拒绝，三条路径分清。**
//
// 无权限是 PermissionDenied、不是你的与不存在同结论（NotFound）、没凭证是
// Unauthenticated——把前两者混成后者会让一次权限配置错误被放大成一次登录风暴
// （见 AGENTS.md 硬性约定 5），而"你的还是别人的"这条区分会变成一条工程枚举通道。
func TestWatchRefusesPerTopic(t *testing.T) {
	cases := []struct {
		name  string
		role  string
		token string
		want  connect.Code
	}{
		{
			name:  "没有 galaxy.project.read",
			role:  rbac.RoleViewer,
			token: testToken,
			want:  connect.CodePermissionDenied,
		},
		{
			name:  "没有凭证",
			role:  rbac.RoleGalaxyAuthor,
			token: "",
			want:  connect.CodeUnauthenticated,
		},
	}

	for _, protocol := range watchProtocols() {
		for _, tc := range cases {
			t.Run(protocol.name+"/"+tc.name, func(t *testing.T) {
				h := startServer(t, tc.role, testScope)
				// 工程由一个**有创作权限**的主体建：这两个用例验的都是"订阅被拒"，
				// 而一个不存在的工程会引入另一条拒因（NotFound），把断言搅浑。
				ownerToken := injectGalaxyAuthor(t, h)
				client := connectGalaxy(t, h, ownerToken)
				projectID := createProject(t, client, "订阅", galaxyv1.SiteForm_SITE_FORM_STATIC)

				got := protocol.refuse(t, h, tc.token, galaxy.ProjectTopic(projectID))
				if got != tc.want {
					t.Errorf("订阅的拒因 = %v，期望 %v", got, tc.want)
				}
			})
		}
	}
}

// **不是你的工程，与它不存在返回同一个结论。**
//
// 区分它们等于提供一条工程枚举通道：拿着别人的工程标识反复订阅，"不存在"与
// "不是你的"的差集就能把一个不可猜标识试出来，而工程标识是发布地址的一部分。
func TestWatchRefusesSomeoneElsesProject(t *testing.T) {
	for _, protocol := range watchProtocols() {
		t.Run(protocol.name, func(t *testing.T) {
			h := startServer(t, rbac.RoleGalaxyAuthor, testScope)
			owner := connectGalaxy(t, h, testToken)
			projectID := createProject(t, owner, "别人的工程", galaxyv1.SiteForm_SITE_FORM_STATIC)

			// 第二个主体有同一个角色：它能创作，但只能碰自己的东西。
			const otherToken = "e2e-watch-other-token"
			const otherSubject = "e2e-watch-other-user"
			h.srv.Authenticator().Add(otherToken, rbac.Subject{
				ID: otherSubject, Type: rbac.SubjectTypeUser, DefaultScope: testScope,
			})
			ctx := context.Background()
			if err := h.srv.Store().PutSubject(ctx, rbac.Subject{
				ID: otherSubject, Type: rbac.SubjectTypeUser, DefaultScope: testScope,
			}); err != nil {
				t.Fatalf("注入第二个主体失败: %v", err)
			}
			if err := h.srv.Store().Bind(ctx, rbac.RoleBinding{
				SubjectID: otherSubject, RoleID: rbac.RoleGalaxyAuthor, Scope: testScope,
			}); err != nil {
				t.Fatalf("注入第二个绑定失败: %v", err)
			}

			got := protocol.refuse(t, h, otherToken, galaxy.ProjectTopic(projectID))
			if got != connect.CodeNotFound {
				t.Errorf("订阅别人的工程 = %v，期望 NotFound（与\"工程不存在\"同一个结论）", got)
			}
		})
	}
}

// 订阅一个没注册过的主题类型：**改了再试，重试没用**。
func TestWatchRefusesUnknownTopicKind(t *testing.T) {
	for _, protocol := range watchProtocols() {
		t.Run(protocol.name, func(t *testing.T) {
			h := startServer(t, rbac.RoleGalaxyAuthor, testScope)

			got := protocol.refuse(t, h, testToken, "identity.device/dev_1")
			if got != connect.CodeInvalidArgument {
				t.Errorf("未知主题类型的拒因 = %v，期望 InvalidArgument", got)
			}
		})
	}
}

// injectGalaxyAuthor 注入一个持创作者角色的主体，返回它的凭证。
//
// 夹具自己那个主体按用例绑定的角色可能**没有**创作权限，而"被拒"的用例需要一个
// 建得出工程的人先把工程建出来，再换另一个身份去订阅。
func injectGalaxyAuthor(t *testing.T, h harness) string {
	t.Helper()
	const subjectID = "e2e-watch-owner"
	const token = "e2e-watch-owner-token"

	ctx := context.Background()
	subject := rbac.Subject{ID: subjectID, Type: rbac.SubjectTypeUser, DefaultScope: testScope}
	h.srv.Authenticator().Add(token, subject)
	if err := h.srv.Store().PutSubject(ctx, subject); err != nil {
		t.Fatalf("注入主体失败: %v", err)
	}
	if err := h.srv.Store().Bind(ctx, rbac.RoleBinding{
		SubjectID: subjectID, RoleID: rbac.RoleGalaxyAuthor, Scope: testScope,
	}); err != nil {
		t.Fatalf("注入绑定失败: %v", err)
	}
	return token
}
