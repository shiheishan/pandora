package app

import (
	"context"
	"fmt"

	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/zap"
	"google.golang.org/grpc"

	nodegrpc "github.com/aegispanel/aegis/internal/module/node/grpc"
	nodesvc "github.com/aegispanel/aegis/internal/module/node/service"
)

// nodeChannel 是节点通道：节点 CA、会话管理器与 gRPC 服务端，只在 serve 里起。
// server 为 nil 表示没配 grpc.node.public_host（开发环境），不监听；签安装令牌回 503。
type nodeChannel struct {
	sessions *nodesvc.Sessions
	server   *nodegrpc.Server
}

// setupNodeChannel 加载（首次启动时生成）节点 CA，注入接入与会话，构造 gRPC 服务端（不监听）。
// 要在 HTTP 开始服务之前调：后台签安装令牌、吊销身份都依赖这里的注入。
func setupNodeChannel(ctx context.Context, r *runtime, svc *services, reg prometheus.Registerer) (*nodeChannel, error) {
	ca, err := svc.node.LoadOrCreateCA(ctx, svc.sealer)
	if err != nil {
		return nil, fmt.Errorf("节点 CA: %w", err)
	}
	gc := r.cfg.GRPC.Node
	svc.node.UseEnrollment(ca, nodesvc.InstallTarget{
		BaseURL: r.cfg.PublicBaseURL, Host: gc.PublicHost, Port: gc.EffectivePublicPort(),
	})
	sess := svc.node.NewSessions(nodesvc.SessionOptions{})
	svc.node.UseSessions(sess)
	registerSessionsGauge(reg, sess)
	nc := &nodeChannel{sessions: sess}
	if gc.PublicHost == "" {
		r.log.Warn("grpc.node.public_host 为空，节点 gRPC 不启动")
		return nc, nil
	}
	gm := newGRPCMetrics(reg)
	gs, err := nodegrpc.NewServer(nodegrpc.Options{
		Addr: gc.Addr, PublicHost: gc.PublicHost, CA: ca, Service: svc.node,
		Limiter: svc.limiter, Logger: r.log,
		UnaryInterceptors:  []grpc.UnaryServerInterceptor{gm.UnaryServerInterceptor()},
		StreamInterceptors: []grpc.StreamServerInterceptor{gm.StreamServerInterceptor()},
	})
	if err != nil {
		return nil, fmt.Errorf("节点 gRPC: %w", err)
	}
	nc.server = gs
	return nc, nil
}

// run 跑会话管理器（纪元轮次）与 gRPC 监听，直到 ctx 结束或监听失败。
func (nc *nodeChannel) run(ctx context.Context, goFn func(func() error)) {
	goFn(func() error {
		nc.sessions.Run(ctx)
		return nil
	})
	if nc.server != nil {
		goFn(func() error {
			if err := nc.server.Serve(ctx); err != nil {
				return fmt.Errorf("节点 gRPC 监听: %w", err)
			}
			return nil
		})
	}
}

// shutdown 先让会话层收尾（发错峰重连、关流），再优雅停 gRPC；长连接不先收尾 GracefulStop 会一直等到超时。
func (nc *nodeChannel) shutdown(ctx context.Context, log *zap.Logger) {
	if err := nc.sessions.Shutdown(ctx); err != nil {
		log.Warn("关闭节点会话", zap.Error(err))
	}
	if nc.server != nil {
		if err := nc.server.Shutdown(ctx); err != nil {
			log.Warn("关闭节点 gRPC", zap.Error(err))
		}
	}
}
