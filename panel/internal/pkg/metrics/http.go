package metrics

import (
	"strconv"
	"time"

	"github.com/aegispanel/aegis/internal/pkg/reqstats"
	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
)

// HTTP 记录 HTTP 请求与请求内 SQL、Redis 用量。
type HTTP struct {
	requests      *prometheus.CounterVec
	duration      *prometheus.HistogramVec
	sqlQueries    *prometheus.CounterVec
	sqlSeconds    *prometheus.CounterVec
	redisCommands *prometheus.CounterVec
}

// NewHTTP 在 reg 上注册 HTTP 相关指标并返回采集器。
func NewHTTP(reg prometheus.Registerer) *HTTP {
	h := &HTTP{
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: Namespace,
			Subsystem: "http",
			Name:      "requests_total",
			Help:      "HTTP 请求数",
		}, []string{"server", "method", "route", "status"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: Namespace,
			Subsystem: "http",
			Name:      "request_duration_seconds",
			Help:      "HTTP 请求耗时（秒）",
			Buckets:   []float64{0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 25},
		}, []string{"server", "method", "route", "status"}),
		sqlQueries: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: Namespace,
			Subsystem: "http",
			Name:      "sql_queries_total",
			Help:      "请求内执行的 SQL 条数，除以 requests_total 得每请求平均条数",
		}, []string{"server", "route"}),
		sqlSeconds: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: Namespace,
			Subsystem: "http",
			Name:      "sql_seconds_total",
			Help:      "请求内 SQL 的总耗时（秒）",
		}, []string{"server", "route"}),
		redisCommands: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: Namespace,
			Subsystem: "http",
			Name:      "redis_commands_total",
			Help:      "请求内的 Redis 命令数（pipeline 按命令数）",
		}, []string{"server", "route"}),
	}
	reg.MustRegister(h.requests, h.duration, h.sqlQueries, h.sqlSeconds, h.redisCommands)
	return h
}

// Middleware 在请求结束后累加指标。server 取 "public" 或 "admin"；
// 须挂在 httpx.RequestID 之后（读得到 reqstats），紧跟 httpx.AccessLog 之后最合适。
func (h *HTTP) Middleware(server string) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		st := reqstats.From(c.Request.Context())
		c.Next()
		route := routeLabel(c)
		method := methodLabel(c.Request.Method)
		status := strconv.Itoa(c.Writer.Status())
		h.requests.WithLabelValues(server, method, route, status).Inc()
		h.duration.WithLabelValues(server, method, route, status).Observe(time.Since(start).Seconds())
		h.sqlQueries.WithLabelValues(server, route).Add(float64(st.SQLCount()))
		h.sqlSeconds.WithLabelValues(server, route).Add(st.SQLDuration().Seconds())
		h.redisCommands.WithLabelValues(server, route).Add(float64(st.RedisCount()))
	}
}

// routeLabel 只写路由模板，绝不写原始路径（路径段里可能有订阅令牌）；未匹配的路径都归到 "-"，标签基数有界。
func routeLabel(c *gin.Context) string {
	if p := c.FullPath(); p != "" {
		return p
	}
	return "-"
}

// methodLabel 规范化 HTTP 方法，未知方法归为 OTHER，防客户端乱写方法撑大基数。
func methodLabel(m string) string {
	switch m {
	case "GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS":
		return m
	default:
		return "OTHER"
	}
}
