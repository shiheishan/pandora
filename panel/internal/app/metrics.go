package app

import (
	"fmt"

	grpcprom "github.com/grpc-ecosystem/go-grpc-middleware/providers/prometheus"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/redis/go-redis/extra/redisprometheus/v9"

	nodesvc "github.com/aegispanel/aegis/internal/module/node/service"
	"github.com/aegispanel/aegis/internal/pkg/metrics"
)

// newMetricsRegistry 建 serve 与 worker 共用的指标 registry：Go runtime、进程、
// PG 连接池（go_sql_*，db_name="pandora"）、Redis 连接池（pandora_redis_pool_*）。
func newMetricsRegistry(r *runtime) (*prometheus.Registry, error) {
	reg := metrics.NewRegistry()
	sqlDB, err := r.db.Base().DB()
	if err != nil {
		return nil, fmt.Errorf("取数据库连接池: %w", err)
	}
	reg.MustRegister(
		collectors.NewDBStatsCollector(sqlDB, "pandora"),
		redisprometheus.NewCollector(metrics.Namespace, "redis", r.redis),
	)
	return reg, nil
}

// newGRPCMetrics 是节点 gRPC 的服务端指标（pandora_grpc_server_*：流的开关、消息收发、处理耗时），已注册进 reg。
func newGRPCMetrics(reg prometheus.Registerer) *grpcprom.ServerMetrics {
	m := grpcprom.NewServerMetrics(
		grpcprom.WithServerCounterOptions(grpcprom.WithNamespace(metrics.Namespace)),
		grpcprom.WithServerHandlingTimeHistogram(grpcprom.WithHistogramNamespace(metrics.Namespace)),
	)
	reg.MustRegister(m)
	return m
}

// registerSessionsGauge 注册 pandora_node_sessions：当前在线的节点会话数（每台服务器一条）。
func registerSessionsGauge(reg prometheus.Registerer, sess *nodesvc.Sessions) {
	reg.MustRegister(prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Namespace: metrics.Namespace, Subsystem: "node", Name: "sessions",
		Help: "当前在线的节点会话数",
	}, func() float64 { return float64(sess.Len()) }))
}
