// Package server 装配 aladdin 的 RPC 服务端。
//
// 传输层用 Connect（connect-go）：它的 handler 在同一个端口上同时支持
// Connect / gRPC / gRPC-Web 三种协议，因此
//   - 浏览器走 Connect（web/src/api/transport.ts）
//   - CLI 也走 Connect（pkg/client）
//
// 两者打同一个地址，不存在两套服务端，也不存在两份业务实现。命令行不走原生
// gRPC 是有依据的：反向代理转发 gRPC 时会丢掉空正文响应的 trailers，也就是
// 所有错误响应（见 docs/debugging/registry.md）。
//
// 参考实现：usememos/memos。
package server

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"connectrpc.com/connect"
	"connectrpc.com/grpchealth"
	"connectrpc.com/grpcreflect"
	"go.uber.org/zap"

	eventsv1connect "github.com/poetlife/aladdin/api/gen/aladdin/events/v1/eventsv1connect"
	galaxyv1connect "github.com/poetlife/aladdin/api/gen/aladdin/galaxy/v1/galaxyv1connect"
	identityv1connect "github.com/poetlife/aladdin/api/gen/aladdin/identity/v1/identityv1connect"
	opsv1connect "github.com/poetlife/aladdin/api/gen/aladdin/ops/v1/opsv1connect"
	profilev1connect "github.com/poetlife/aladdin/api/gen/aladdin/profile/v1/profilev1connect"
	rbacv1connect "github.com/poetlife/aladdin/api/gen/aladdin/rbac/v1/rbacv1connect"
	skillv1connect "github.com/poetlife/aladdin/api/gen/aladdin/skill/v1/skillv1connect"
	telemetryv1connect "github.com/poetlife/aladdin/api/gen/aladdin/telemetry/v1/telemetryv1connect"
	"github.com/poetlife/aladdin/internal/config"
	"github.com/poetlife/aladdin/internal/galaxy"
	"github.com/poetlife/aladdin/internal/identity"
	"github.com/poetlife/aladdin/internal/objectstore"
	"github.com/poetlife/aladdin/internal/observability"
	"github.com/poetlife/aladdin/internal/profile"
	"github.com/poetlife/aladdin/internal/rbac"
	"github.com/poetlife/aladdin/internal/registration"
	"github.com/poetlife/aladdin/internal/server/interceptor"
	"github.com/poetlife/aladdin/internal/skill"
	"github.com/poetlife/aladdin/internal/telemetry"
	"github.com/poetlife/aladdin/internal/watch"
)

// shutdownGrace 是收到停止信号后等待在途请求完成的上限。
//
// 超过它就强制关闭：宁可中断个别长请求，也不能让进程卡在关不掉的循环里——
// 那会让编排系统走到 SIGKILL，连日志都来不及落盘。
const shutdownGrace = 10 * time.Second

// Server 是 aladdin 的 RPC 服务端。
type Server struct {
	cfg      config.ServerConfig
	logger   *zap.Logger
	store    rbac.MutableStore
	engine   *rbac.Engine
	machine  *interceptor.TokenAuthenticator
	sessions *identity.Sessions
	// clientEvents 保留在服务端上不是为了读接口（那个服务自己拿着它），而是为了
	// **启动时的保留期回收**：那件事发生在监听之前，早于任何请求，因此拿不到
	// 通过请求路径装配的实例。
	clientEvents telemetry.Store
	// skillCatalog 与 clientEvents 同理：保留在服务端上不是为了读接口（那个服务
	// 自己拿着它），而是为了**启动时的保留期回收**——那件事发生在监听之前。
	skillCatalog *skill.Service

	httpServer *http.Server
}

// IdentityStores 是认证模块的入口，由入口进程用同一条连接构造后传入。
//
// 与 rbac 的 store 同理：连接的打开与结构迁移属于**启动顺序**的一部分，
// 只能发生在入口进程里，这里拿到的是已经可用的实例。
//
// 三个字段都可以为零值：一个只服务机器凭证、不做人类登录的部署不需要它们，
// 此时会话路径整体缺席，而不是退化成一个"什么都通过"的校验。
type IdentityStores struct {
	// Identities 是身份归属的唯一入口（登录时解析、绑定时写入）。
	Identities *identity.Identities
	// Sessions 是会话凭证的签发与失效入口。
	Sessions *identity.Sessions
	// Channels 是**已启用**的登录渠道。它可以是空的——一个只服务机器凭证
	// 的部署不需要任何人类登录方式，此时这些路径整体缺席，而不是退化成一个
	// "什么都通过"的校验。
	Channels *identity.Registry
	// Registrations 是注册策略与邀请码的持久化存储（见
	// internal/registration）。**它不是可选的**：注册闸门在每一条渠道登录路径
	// 上，缺了它就没有"谁能进来"这个结论。
	Registrations registration.Store
}

// ProfileStores 是个人档案模块的存储，由入口进程构造后传入。
//
// 与认证模块同理：档案表由同一份迁移建好，因此它的实现必须长在同一条连接上。
type ProfileStores struct {
	// Profiles 是档案的持久化存储。
	Profiles profile.Store
	// Avatars 是**头像字节的直传存储**。**为 nil 表示这个部署没有配置对象存储**：
	// 头像功能整体缺席，而不是退化成一个"什么都存不下"的实现——前端据此
	// 不渲染上传区（见 docs/design/profile/avatar-storage.md）。
	Avatars objectstore.Store
}

// GalaxyStores 是 galaxy 模块的存储与地址派生，由入口进程构造后传入。
//
// 与档案模块同理：四张表由同一份迁移建好，因此它的实现必须长在同一条连接上。
// 资产字节的存储与头像是**同一个对象存储的同一套机制**（见
// docs/design/objectstore/README.md），入口进程把同一个实例交给两处。
type GalaxyStores struct {
	// Projects 是工程、草稿、版本、资产元数据与发布记录的持久化存储。
	Projects galaxy.MutableStore
	// Assets 是私有区对象的直传入口。**为 nil 表示没有配置私有桶**：资产
	// 功能整体缺席，而工程与版本照常可用。
	Assets objectstore.Store
	// Public 是公开区的写入口。**为 nil 表示没有配置发布**：发布不可用。
	// 它写的是与 Assets 同一个桶，区别在写下去的对象权限（公开读）。
	Public galaxy.PublicStore
	// Origin 是发布态地址的派生入口。零值表示没有配置发布域。
	Origin galaxy.PublicOrigin
}

// TelemetryStores 是遥测模块的存储，由入口进程构造后传入。
//
// 与其它模块同理：事件表由同一份迁移建好，因此它的实现必须长在同一条连接上。
type TelemetryStores struct {
	// Events 是客户端事件的落库与只读查询入口。
	//
	// 它同时被两侧使用：写侧（TelemetryService 背后的 Recorder）往里记，读侧
	// （TelemetryAdminService）从里查。**同一个实例**是有意的——两边各建一份
	// 不会出错，但会让"读到的就是写下的"这件事需要额外确认。
	Events telemetry.Store
}

// SkillStores 是技能目录的存储与远端拉取，由入口进程构造后传入。
//
// 与其它模块同理：五张表由同一份迁移建好，因此它的实现必须长在同一条连接上。
// 远端拉取在这里是一个**接口**：测试注入假实现，因此契约测试不访问网络（见
// docs/design/skill/onboarding.md 的可验证性表）。
type SkillStores struct {
	// Catalog 是技能、版本、标签、收藏与使用记录的持久化存储。
	Catalog skill.Store
	// Objects 是内容对象的读写入口。**为 nil 表示没有配置对象存储**：技能目录
	// 整体不可用——它的字节没有地方放（与 galaxy 的资产同一条降级取向）。
	Objects objectstore.Store
	// Remote 是远端拉取的出口。为 nil 表示装配缺失（生产上总是有）。
	Remote skill.Remote
}

// galaxyReadMaxBytes 是创作服务的单条消息读上限。
//
// 必须显式设：connect-go 的默认是**不限制大小**，而请求体由客户端决定大小。
// 没有它，一个几百 MB 的请求体在被领域层拒绝之前就已经整份读进内存了。
//
// **内容不走请求体**：草稿是一组「路径 → 摘要」的清单，字节由客户端直传进对象
// 存储。因此这个上限对的是清单本身——它的最坏情况是文件数上限乘单条路径的长度
// 上限（1000 × 512 字节 ≈ 500 KiB），取 1 MiB 足够，且离"任何一份内容的大小"
// 都无关。
const galaxyReadMaxBytes = 1 << 20

// New 按配置装配服务端。
//
// store 由调用方提供而不是在这里构造：存储的打开与迁移属于**启动顺序**
// 的一部分（要在监听之前完成，失败即拒绝启动），而那件事只能发生在入口
// 进程里。服务端拿到的是一个已经可用、已经迁移完毕的存储。ident 同理。
//
// metrics 为 nil 时不记录请求指标；遥测的 provider 生命周期由调用方
// （入口进程）管理，服务端只消费它建好的全局实现。
func New(cfg config.ServerConfig, logger *zap.Logger, metrics *observability.Metrics, store rbac.MutableStore, ident IdentityStores, prof ProfileStores, gal GalaxyStores, tel TelemetryStores, skills SkillStores) *Server {
	engine := rbac.NewEngine(store, logger, metrics)
	machine := interceptor.NewTokenAuthenticator()
	authorizer := &interceptor.Authorizer{Engine: engine, Logger: logger}

	// 注册面的领域入口。它由这里构造而不是入口进程传进来：它不含配置、也没有
	// 生命周期，只是一层套在存储上的领域语义（见 internal/registration）。
	registrations := registration.New(ident.Registrations)

	// 生命周期锁同时交给"空主体认领"与"角色授予"：两件事必须对同一个
	// 主体串行，否则会出现身份已移走、角色还留在原主体的搁浅（见
	// subject_lifecycle_gate.go）。
	lifecycleGate := &subjectLifecycleGate{}

	// 命令行登录的批准页地址。两个前提合起来才是"这条路在不在"：
	//
	//   1. 服务端知道自己的对外地址——地址由它构造，不知道就给不出一个能用
	//      浏览器打开的地址；
	//   2. 有会话签发能力——交付的就是一份会话（只服务机器凭证的部署没有它）。
	//
	// 缺任一条时留空，认证面据此让这条路径整体缺席，而不是发起得了、交付不了。
	deviceApprovalURL := ""
	if ident.Sessions != nil && cfg.PublicBaseURL != "" {
		deviceApprovalURL = cfg.PublicURL(DeviceApprovalPath)
	}

	identitySrv := NewIdentityService(store, engine, IdentityDeps{
		Machine:              machine,
		Identities:           ident.Identities,
		Sessions:             ident.Sessions,
		Channels:             ident.Channels,
		Logger:               logger,
		Registrations:        registrations,
		PendingBindings:      newPendingBindings(time.Now, cfg.PublicScheme() == "https"),
		PendingRegistrations: newPendingRegistrations(time.Now, cfg.PublicScheme() == "https"),
		DeviceLogins:         newDeviceLogins(time.Now),
		DeviceApprovalURL:    deviceApprovalURL,
		LifecycleGate:        lifecycleGate,
	})

	// 注册面与认证面共用同一个 IdentityService：那一段"登记一个新主体"的实现是
	// 开放注册的回调与"完成注册"两条路共用的那一份（见 registration_service.go）。
	registrationSrv := NewRegistrationService(identitySrv, registrations, store, logger)

	// 档案对身份模块的依赖是**只读**的：展示名回退的第二步要取该主体的渠道
	// 列表。身份模块不感知档案，依赖方向单向。
	//
	// 身份模块缺席时（只服务机器凭证的部署）不设这个字段，而不是塞一个空实现：
	// 回退的第二步没有数据可用，展示名直接落到主体标识。
	profileDeps := profile.ProfilesDeps{
		Store:    prof.Profiles,
		Subjects: store,
		Avatars:  prof.Avatars,
		Logger:   logger,
	}
	if ident.Identities != nil {
		profileDeps.Identities = ident.Identities
	}
	// 同一个档案入口也交给下面的遥测读侧：它要用**同一套**展示名回退规则把事件里的
	// 主体标识换成展示信息（见 telemetry_admin_service.go）。
	profiles := profile.NewProfiles(profileDeps)
	profileSrv := NewProfileService(profiles, logger)

	// 请求凭证的认证入口：机器凭证与会话凭证两条路径合到一处，
	// 见 credential_authenticator.go。
	authenticator := &credentialAuthenticator{machine: machine, sessions: ident.Sessions}

	mux := http.NewServeMux()

	// registeredPaths 收集所有注册到 mux 的路径。
	// 遥测中间件用它把请求路径归一成有界的指标属性值——没有它，
	// 任意一条路径都会各占一个时序。
	var registeredPaths []string
	register := func(path string, handler http.Handler) {
		mux.Handle(path, handler)
		registeredPaths = append(registeredPaths, path)
	}

	// 两个业务服务都挂鉴权拦截器。拦截器同时适用于三种协议。
	opts := []connect.HandlerOption{
		connect.WithInterceptors(authorizer.Interceptor()),
	}
	rbacPath, rbacHandler := rbacv1connect.NewRBACServiceHandler(NewRBACService(store, engine, lifecycleGate), opts...)
	register(rbacPath, rbacHandler)

	identityPath, identityHandler := identityv1connect.NewIdentityServiceHandler(identitySrv, opts...)
	register(identityPath, identityHandler)

	registrationPath, registrationHandler := identityv1connect.NewRegistrationServiceHandler(registrationSrv, opts...)
	register(registrationPath, registrationHandler)

	// 重定向型登录渠道的浏览器直连端点。它们不是 RPC（浏览器导航带不了请求头），
	// 因此不走 Connect handler，但要经同一个 register 注册——这样遥测的路径
	// 归一保持有界，且 authMiddleware 的放行清单与这里注册的地址不会漂移（见
	// middleware.go 的 browserEntryPaths）。
	//
	// 渠道清单只有一份（见 redirect_login_flow.go 的 redirectChannels）：加一个
	// 渠道时端点与放行清单一起变，不会出现"注册了但没放行"。
	for _, channel := range redirectChannels() {
		flow := NewRedirectLoginFlow(identitySrv, cfg, logger, channel)
		register(channel.startPath, http.HandlerFunc(flow.Start))
		register(channel.callbackPath, http.HandlerFunc(flow.Callback))
	}

	// 档案服务不再需要单独一组 handler options：头像改为**直传**之后，它没有
	// 任何一个由客户端决定大小的入口——字节根本不经过这里（见
	// docs/design/objectstore/README.md）。
	profilePath, profileHandler := profilev1connect.NewProfileServiceHandler(profileSrv, opts...)
	register(profilePath, profileHandler)

	// 事件通道的地基：一条连接级的总线（谁订了什么、往谁推）加一张主题类型
	// 注册表（哪些资源可订阅、订阅它要什么权限、谁算属主）。**两者都是进程内
	// 的**，因此 P0 是单副本形态（见 docs/design/events/README.md 的待定决策）。
	bus := watch.NewHub()
	topics := watch.NewRegistry()

	// 订阅是长连接，而 http.Server.Shutdown **不会取消在途请求的 context**：
	// 给它一个明确的收摊信号，否则每次停服都要等满 shutdownGrace 才把它掐掉。
	shuttingDown := make(chan struct{})

	// 创作服务：正文由客户端决定大小（草稿与校验请求），因此显式设了读上限
	// （见 galaxyReadMaxBytes）。头像那种"字节在请求体里"的入口已经没有了——
	// 两条上传链路都改成了直传。
	galaxyCore := galaxy.NewService(galaxy.Deps{
		Store:  gal.Projects,
		Assets: gal.Assets,
		Public: gal.Public,
		Origin: gal.Origin,
		Events: bus,
		Logger: logger,
	})
	galaxySrv := NewGalaxyService(galaxyCore, logger)
	galaxyOpts := append([]connect.HandlerOption{
		connect.WithReadMaxBytes(galaxyReadMaxBytes),
	}, opts...)
	galaxyPath, galaxyHandler := galaxyv1connect.NewGalaxyServiceHandler(galaxySrv, galaxyOpts...)
	register(galaxyPath, galaxyHandler)

	// galaxy 往事件通道里注册它的主题类型。**属主自己给权限码与归属判定**，
	// 通道不认识"工程"：权限码取自权限目录，归属判定复用读工程的那一处入口
	// （`GetProject`），因此"不是他的"与"不存在"在订阅上也是同一个结论。
	topics.Register(galaxy.ProjectTopicKind, watch.Kind{
		Permission: rbac.PermissionGalaxyProjectRead,
		Authorize: func(ctx context.Context, subject rbac.Subject, projectID string) error {
			if _, err := galaxyCore.GetProject(ctx, subject.ID, projectID); err != nil {
				return toGalaxyConnectError(err)
			}
			return nil
		},
	})

	// 事件通道。**它是唯一一条服务端流**，且不属于任何一个业务服务——下一个
	// 要推送的业务只往 topics 里注册一个类型，通道本身不动。
	eventsSrv := NewEventsService(WatchDeps{
		Hub:      bus,
		Registry: topics,
		Engine:   engine,
		Shutdown: shuttingDown,
	}, logger)
	eventsPath, eventsHandler := eventsv1connect.NewEventsServiceHandler(eventsSrv, opts...)
	register(eventsPath, eventsHandler)

	// 客户端事件上报。**它是一条公开方法**（见 telemetry.proto 与
	// docs/design/rbac/server-permissions.md）：登录页上的失败、命令行未登录就退出
	// 都发生在拿到会话之前，要求先认证才能上报是循环依赖。
	//
	// 校验、脱敏、限流都在 internal/telemetry——那一层不认识 Connect，因此"什么会
	// 被写进日志"可以脱离服务端单独测试。这里只做装配与协议层的取数。
	//
	// 同一个 store 实例也交给下面的只读管理面：写侧记进去的、读侧查出来的必须是
	// 同一份数据，两处各建一份不会有错，只是多一个需要确认的前提。
	telemetryRecorder := telemetry.NewRecorder(logger, telemetry.NewLimiter(telemetry.DefaultLimits), tel.Events)
	telemetryPath, telemetryHandler := telemetryv1connect.NewTelemetryServiceHandler(
		NewTelemetryService(telemetryRecorder), opts...)
	register(telemetryPath, telemetryHandler)

	// 客户端事件的只读管理面。它与上面的上报服务**准入条件正相反**（那边公开、
	// 这边要 telemetry.read），因此是两个服务而不是一个服务的两个方法。
	//
	// 它拿到档案入口是为了把明细里的主体标识**在读侧**换成展示名与头像：昵称与
	// 头像不进事件、不进日志、不落库，写侧的脱敏规则一个字不改（见
	// docs/observability.md）。
	telemetryAdminPath, telemetryAdminHandler := telemetryv1connect.NewTelemetryAdminServiceHandler(
		NewTelemetryAdminService(tel.Events, profiles, logger), opts...)
	register(telemetryAdminPath, telemetryAdminHandler)

	// 部署实例自述。它与遥测管理面同属运维分组，但**没有共同状态**：那个查的是
	// 落库的事件，这个读的是构建期注入 + 运行期常量，因此不共用服务、也不共用
	// 存储。两者都是只读，准入条件各写在各自的 proto 注解里。
	opsPath, opsHandler := opsv1connect.NewOpsServiceHandler(NewOpsService(), opts...)
	register(opsPath, opsHandler)

	// 技能目录。**它的字节与 galaxy 的资产在同一个桶里**，靠 `skills/` 那一段
	// 前缀并排；远端拉取是本模块唯一一条出站请求路径，目标由来源的形状白名单
	// 决定（见 skill/source.go）。
	//
	// 没有配置对象存储时它与 galaxy 的资产一样整体不可用，而其余模块照常——前端
	// 据此不渲染入口（见 GetCapabilities），而不是渲染一个点了报错的控件。
	skillCore := skill.NewService(skill.Deps{
		Store:   skills.Catalog,
		Objects: skills.Objects,
		Remote:  skills.Remote,
		Logger:  logger,
	})
	skillPath, skillHandler := skillv1connect.NewSkillServiceHandler(NewSkillService(skillCore, logger), opts...)
	register(skillPath, skillHandler)
	skillAdminPath, skillAdminHandler := skillv1connect.NewSkillAdminServiceHandler(
		NewSkillAdminService(skillCore, logger), opts...)
	register(skillAdminPath, skillAdminHandler)

	// 发布地址：浏览器直连的非 RPC 入口。它**不经过鉴权**（发布态公开匿名，
	// 地址即凭据），因此必须登记在 middleware 的浏览器直连清单里，否则会在
	// rbac.Resolve 之前被当成一个没有注解的方法拦下。
	//
	// 它按前缀注册，而清单里的判定也按同一个前缀——两处引用同一个常量。
	register(galaxy.PublicPathPrefix, PublicProjectHandler(galaxyCore, logger))

	// 健康检查与反射：不参与业务鉴权，由 authMiddleware 的
	// infraProcedurePrefixes 显式放行。
	serviceNames := []string{
		rbacv1connect.RBACServiceName,
		identityv1connect.IdentityServiceName,
		profilev1connect.ProfileServiceName,
		galaxyv1connect.GalaxyServiceName,
		eventsv1connect.EventsServiceName,
		telemetryv1connect.TelemetryServiceName,
		telemetryv1connect.TelemetryAdminServiceName,
		skillv1connect.SkillServiceName,
		skillv1connect.SkillAdminServiceName,
		opsv1connect.OpsServiceName,
	}
	healthPath, healthHandler := grpchealth.NewHandler(grpchealth.NewStaticChecker(serviceNames...))
	register(healthPath, healthHandler)

	reflector := grpcreflect.NewStaticReflector(serviceNames...)
	for _, newHandler := range []func(*grpcreflect.Reflector, ...connect.HandlerOption) (string, http.Handler){
		grpcreflect.NewHandlerV1,
		grpcreflect.NewHandlerV1Alpha,
	} {
		path, handler := newHandler(reflector)
		register(path, handler)
	}

	// 中间件顺序固定：链路追踪 → 认证。
	// 认证之后才交给 handler 读请求体，未认证的请求不会被完整读取一遍。
	telemetry := &telemetryMiddleware{metrics: metrics, logger: logger, registeredPaths: registeredPaths}
	authn := &authMiddleware{
		authn:       authenticator,
		errorWriter: connect.NewErrorWriter(),
		logger:      logger,
	}
	handler := telemetry.wrap(authn.wrap(mux))

	httpServer := &http.Server{
		Addr:    cfg.Address,
		Handler: handler,
		// 明文 gRPC 需要 HTTP/2（h2c）。Go 1.24 起 http.Server.Protocols
		// 直接支持非加密 HTTP/2，不再需要 x/net/http2/h2c 包装。
		Protocols: func() *http.Protocols {
			p := new(http.Protocols)
			p.SetHTTP1(true)
			p.SetUnencryptedHTTP2(true)
			return p
		}(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	// 订阅是长连接，而 http.Server.Shutdown **不会取消在途请求的 context**：
	// 给它一个明确的收摊信号，否则每次停服都要等满 shutdownGrace 才把它掐掉。
	// sync.Once 是因为这个回调随每次 Shutdown 各跑一次，而重复 close 会 panic
	// ——在一个关停路径上 panic 是最坏的时机。
	var stopWatchOnce sync.Once
	httpServer.RegisterOnShutdown(func() {
		stopWatchOnce.Do(func() { close(shuttingDown) })
	})

	return &Server{
		cfg:          cfg,
		logger:       logger,
		store:        store,
		engine:       engine,
		machine:      machine,
		sessions:     ident.Sessions,
		clientEvents: tel.Events,
		skillCatalog: skillCore,
		httpServer:   httpServer,
	}
}

// ListenAndServe 在配置的地址上阻塞服务，直到 ctx 被取消或服务出错。
//
// ctx 必须同时覆盖监听与运行：只用它做监听会导致进程收到 SIGTERM 后
// 套接字已关闭、服务却仍在运行的假死状态。
func (s *Server) ListenAndServe(ctx context.Context) error {
	// 回收已过期的会话行，然后才开门。
	//
	// 正确性**不依赖**它：过期的凭证本来就校验不过。它存在的唯一目的是
	// 不让会话表无限增长，因此失败只记日志、不拒绝启动——一次回收失败
	// 没有理由让整个权限平台起不来（见 docs/design/identity/session-token.md）。
	s.reclaimExpiredSessions(ctx)

	// 回收超出保留期的客户端事件行，然后才开门。与上面同理：正确性不依赖它
	// （读取一律带时间条件），它只负责不让事件表无限增长。
	s.reclaimOldClientEvents(ctx)

	// 回收超出保留期的技能使用日次，然后才开门。与上面两处同形同理由：使用量
	// 读侧本来就带 30 天窗口，因此正确性不依赖它，它只负责不让那张表无限增长。
	s.reclaimSkillUsage(ctx)

	var lc net.ListenConfig
	lis, err := lc.Listen(ctx, "tcp", s.cfg.Address)
	if err != nil {
		return fmt.Errorf("监听 %s 失败: %w", s.cfg.Address, err)
	}

	errCh := make(chan error, 1)
	go func() { errCh <- s.Serve(lis) }()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		// ctx 已被取消，用它做关闭超时会立刻超时；
		// WithoutCancel 保留 context 上的取值但丢掉取消信号。
		return s.Shutdown(context.WithoutCancel(ctx))
	}
}

// Serve 在给定监听器上阻塞服务，直到关闭或出错。
//
// 与 ListenAndServe 分开是为了让测试可以传入 127.0.0.1:0 拿到随机端口，
// 从而并行运行端到端测试而不抢占固定端口。
func (s *Server) Serve(lis net.Listener) error {
	s.logger.Info("RPC 服务端已启动",
		zap.String("address", lis.Addr().String()),
		zap.Strings("protocols", []string{"connect", "grpc", "grpc-web"}),
	)
	if err := s.httpServer.Serve(lis); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// Shutdown 优雅关闭，超过 shutdownGrace 则强制停止。
func (s *Server) Shutdown(ctx context.Context) error {
	s.logger.Info("收到停止信号，正在优雅退出")

	shutdownCtx, cancel := context.WithTimeout(ctx, shutdownGrace)
	defer cancel()

	if err := s.httpServer.Shutdown(shutdownCtx); err != nil {
		// 宁可中断个别长请求，也不能让进程卡在关不掉的循环里——
		// 那会让编排系统走到 SIGKILL，连日志都来不及落盘。
		s.logger.Warn("优雅退出超时，强制关闭", zap.Error(err))
		return s.httpServer.Close()
	}
	s.logger.Info("已优雅退出")
	return nil
}

// Store 暴露存储，供本地开发与测试注入初始数据。
func (s *Server) Store() rbac.MutableStore { return s.store }

// Authenticator 暴露机器凭证的认证器，供本地开发签发开发用 token。
//
// 返回的是**机器凭证那一侧**，不是请求认证的入口：会话凭证是登录时签发的，
// 没有任何本地注入的入口。
func (s *Server) Authenticator() *interceptor.TokenAuthenticator { return s.machine }

// reclaimExpiredSessions 回收已过期的会话行。
//
// 它只在启动时跑一次，不引入后台循环：那会新增一个需要被监控、被优雅关闭、
// 异常时可能静默死掉的进程内任务，而它换来的只是"表在两次重启之间增长得
// 慢一点"。真到了单次运行周期内就涨到需要中途清理的量级，那时该重新审视
// 有效期长度，而不是加一个循环（见 docs/design/identity/session-token.md）。
func (s *Server) reclaimExpiredSessions(ctx context.Context) {
	if s.sessions == nil {
		return
	}
	removed, err := s.sessions.Cleanup(ctx)
	if err != nil {
		s.logger.Warn("回收过期会话失败，服务照常启动", zap.Error(err))
		return
	}
	if removed > 0 {
		s.logger.Info("已回收过期会话", zap.Int64("removed", removed))
	}
}

// reclaimOldClientEvents 回收超出保留期的客户端事件行。
//
// 与上面的会话回收同形，理由也一样：只在启动时跑一次，不引入后台循环。差别在
// 于这里的"过期"是**保留策略**而不是有效性——事件一旦记下就是有效的事实，回收
// 只是决定它还能被站内的只读页面看到多久（读侧另有 30 天的窗口）。因此一次回收
// 失败更不该让启动失败：丢的只可能是页面上更早的那一段。
//
// 保留期的取值在 internal/telemetry（与最长查询窗口共用一处判断），这里只负责
// 在正确时刻调用它。
func (s *Server) reclaimOldClientEvents(ctx context.Context) {
	if s.clientEvents == nil {
		return
	}
	cutoff := time.Now().Add(-telemetry.Retention)
	removed, err := s.clientEvents.DeleteBefore(ctx, cutoff)
	if err != nil {
		s.logger.Warn("回收超期客户端事件失败，服务照常启动", zap.Error(err))
		return
	}
	if removed > 0 {
		s.logger.Info("已回收超期客户端事件", zap.Int64("removed", removed))
	}
}

// reclaimSkillUsage 回收超出保留期的技能使用日次。
//
// 与上面两处同形，理由也一样：只在启动时跑一次，不引入后台循环。**保留期多留
// 一天**（见 skill.UsageRetention）——"最近 30 天"这个口径不该在日界上少算一格。
func (s *Server) reclaimSkillUsage(ctx context.Context) {
	if s.skillCatalog == nil {
		return
	}
	if err := s.skillCatalog.PurgeUsage(ctx); err != nil {
		s.logger.Warn("回收超期技能使用记录失败，服务照常启动", zap.Error(err))
	}
}
