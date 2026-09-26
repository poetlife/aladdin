// Package server 装配 aladdin 的 RPC 服务端。
//
// 传输层用 Connect（connect-go）：它的 handler 在同一个端口上同时支持
// Connect / gRPC / gRPC-Web 三种协议，因此
//   - 浏览器走 Connect（web/src/api/transport.ts）
//   - CLI 走原生 gRPC（pkg/client 的 grpc-go 客户端）
//
// 两者打同一个地址，不存在两套服务端，也不存在两份业务实现。
//
// 参考实现：usememos/memos。
package server

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	"connectrpc.com/connect"
	"connectrpc.com/grpchealth"
	"connectrpc.com/grpcreflect"
	"go.uber.org/zap"

	identityv1connect "github.com/poetlife/aladdin/api/gen/aladdin/identity/v1/identityv1connect"
	rbacv1connect "github.com/poetlife/aladdin/api/gen/aladdin/rbac/v1/rbacv1connect"
	"github.com/poetlife/aladdin/internal/config"
	"github.com/poetlife/aladdin/internal/identity"
	"github.com/poetlife/aladdin/internal/observability"
	"github.com/poetlife/aladdin/internal/rbac"
	"github.com/poetlife/aladdin/internal/server/interceptor"
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
	// Verifier 校验渠道签发的身份令牌；为 nil 表示该登录方式未启用。
	Verifier identity.TokenVerifier
}

// New 按配置装配服务端。
//
// store 由调用方提供而不是在这里构造：存储的打开与迁移属于**启动顺序**
// 的一部分（要在监听之前完成，失败即拒绝启动），而那件事只能发生在入口
// 进程里。服务端拿到的是一个已经可用、已经迁移完毕的存储。ident 同理。
//
// metrics 为 nil 时不记录请求指标；遥测的 provider 生命周期由调用方
// （入口进程）管理，服务端只消费它建好的全局实现。
func New(cfg config.ServerConfig, logger *zap.Logger, metrics *observability.Metrics, store rbac.MutableStore, ident IdentityStores) *Server {
	engine := rbac.NewEngine(store, logger, metrics)
	machine := interceptor.NewTokenAuthenticator()
	authorizer := &interceptor.Authorizer{Engine: engine, Logger: logger}

	identitySrv := NewIdentityService(store, engine, IdentityDeps{
		Machine:        machine,
		Identities:     ident.Identities,
		Sessions:       ident.Sessions,
		Verifier:       ident.Verifier,
		GoogleClientID: cfg.GoogleClientID,
		Logger:         logger,
	})

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
	rbacPath, rbacHandler := rbacv1connect.NewRBACServiceHandler(NewRBACService(store, engine), opts...)
	register(rbacPath, rbacHandler)

	identityPath, identityHandler := identityv1connect.NewIdentityServiceHandler(identitySrv, opts...)
	register(identityPath, identityHandler)

	// 健康检查与反射：不参与业务鉴权，由 authMiddleware 的
	// infraProcedurePrefixes 显式放行。
	serviceNames := []string{
		rbacv1connect.RBACServiceName,
		identityv1connect.IdentityServiceName,
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

	return &Server{
		cfg:        cfg,
		logger:     logger,
		store:      store,
		engine:     engine,
		machine:    machine,
		sessions:   ident.Sessions,
		httpServer: httpServer,
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
