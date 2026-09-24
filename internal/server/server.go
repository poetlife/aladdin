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
	cfg        config.Config
	logger     *zap.Logger
	store      *rbac.MemoryStore
	engine     *rbac.Engine
	authn      interceptor.Authenticator
	httpServer *http.Server
}

// New 按配置装配服务端。
func New(cfg config.Config, logger *zap.Logger) *Server {
	store := rbac.NewMemoryStore()
	engine := rbac.NewEngine(store, logger)
	authenticator := interceptor.NewTokenAuthenticator()
	authorizer := &interceptor.Authorizer{Engine: engine, Logger: logger}

	identitySrv := NewIdentityService(store, engine)
	identitySrv.SetAuthenticator(authenticator)

	mux := http.NewServeMux()

	// 两个业务服务都挂鉴权拦截器。拦截器同时适用于三种协议。
	opts := []connect.HandlerOption{
		connect.WithInterceptors(authorizer.Interceptor()),
	}
	rbacPath, rbacHandler := rbacv1connect.NewRBACServiceHandler(NewRBACService(store, engine), opts...)
	mux.Handle(rbacPath, rbacHandler)

	identityPath, identityHandler := identityv1connect.NewIdentityServiceHandler(identitySrv, opts...)
	mux.Handle(identityPath, identityHandler)

	// 健康检查与反射：不参与业务鉴权，由 authMiddleware 的
	// infraProcedurePrefixes 显式放行。
	serviceNames := []string{
		rbacv1connect.RBACServiceName,
		identityv1connect.IdentityServiceName,
	}
	healthPath, healthHandler := grpchealth.NewHandler(grpchealth.NewStaticChecker(serviceNames...))
	mux.Handle(healthPath, healthHandler)

	reflector := grpcreflect.NewStaticReflector(serviceNames...)
	for _, newHandler := range []func(*grpcreflect.Reflector, ...connect.HandlerOption) (string, http.Handler){
		grpcreflect.NewHandlerV1,
		grpcreflect.NewHandlerV1Alpha,
	} {
		path, handler := newHandler(reflector)
		mux.Handle(path, handler)
	}

	// 中间件顺序固定：链路标识 → 认证。
	// 认证之后才交给 handler 读请求体，未认证的请求不会被完整读取一遍。
	authn := &authMiddleware{
		authn:       authenticator,
		errorWriter: connect.NewErrorWriter(),
		logger:      logger,
	}
	handler := withTraceID(authn.wrap(mux))

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
		authn:      authenticator,
		httpServer: httpServer,
	}
}

// ListenAndServe 在配置的地址上阻塞服务，直到 ctx 被取消或服务出错。
//
// ctx 必须同时覆盖监听与运行：只用它做监听会导致进程收到 SIGTERM 后
// 套接字已关闭、服务却仍在运行的假死状态。
func (s *Server) ListenAndServe(ctx context.Context) error {
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
func (s *Server) Store() *rbac.MemoryStore { return s.store }

// Authenticator 暴露认证器，供本地开发签发开发用 token。
func (s *Server) Authenticator() *interceptor.TokenAuthenticator {
	auth, ok := s.authn.(*interceptor.TokenAuthenticator)
	if !ok {
		return nil
	}
	return auth
}
