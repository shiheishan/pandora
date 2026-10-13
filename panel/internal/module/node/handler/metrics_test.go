package handler

// 节点指标接口的 handler 测试：不连库，只测路由与登录、路径参数，以及 DTO 形状（对照前端 metricsSchema）。

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/aegispanel/aegis/internal/module/node/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/httpx"
	"github.com/aegispanel/aegis/internal/pkg/route"
)

// newMetricsEngine 用一个没有数据库的 service 只挂指标路由；loggedIn 为假时登录中间件一律回 401。
func newMetricsEngine(loggedIn bool) *gin.Engine {
	gin.SetMode(gin.TestMode)
	e := gin.New()
	e.Use(httpx.RequestID(zap.NewNop()), httpx.Errors())
	pass := func(c *gin.Context) { c.Next() }
	auth := pass
	if !loggedIn {
		auth = func(c *gin.Context) { httpx.Fail(c, errs.New(errs.Unauthorized)) }
	}
	kit := route.Kit{
		RequireAuth:  auth,
		RecentReauth: pass,
		Idempotency:  func(string) gin.HandlerFunc { return pass },
		Switch:       func(string) gin.HandlerFunc { return pass },
	}
	New(service.New(nil, nil, nil, nil)).registerMetrics(e.Group("/v1"), kit)
	return e
}

func TestMetricsRouteRequiresLogin(t *testing.T) {
	w := doPool(newMetricsEngine(false), http.MethodGet, "/v1/nodes/0199a3b4-0000-7000-8000-000000000001/metrics?minutes=30", "")
	poolExpectErr(t, w, errs.Unauthorized, "")
}

func TestMetricsBadNodeID(t *testing.T) {
	w := doPool(newMetricsEngine(true), http.MethodGet, "/v1/nodes/not-a-uuid/metrics", "")
	poolExpectErr(t, w, errs.NotFound, "")
}

func TestMetricsEmptyShape(t *testing.T) {
	raw, err := json.Marshal(metricsResponseFrom(service.NodeMetrics{}))
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"points":[],"latest":null}` {
		t.Fatalf("空曲线 %s", raw)
	}
}

func TestMetricsShape(t *testing.T) {
	at := time.Date(2026, 10, 12, 8, 0, 0, 0, time.UTC)
	resp := metricsResponseFrom(service.NodeMetrics{
		Points: []service.MetricPoint{{At: at, CPUPercent: 12.34, MemPercent: 25, Load1: 0.5, RxSpeed: 100, TxSpeed: 50, TCPConns: 7}},
		Latest: &service.MetricsLatest{CPUPercent: 12.34, MemUsedMB: 1024, MemTotalMB: 4096, DiskUsedGB: 10, DiskTotalGB: 40,
			Load1: 0.5, Load5: 0.4, Load15: 0.3, TCPConns: 7, UptimeSec: 3600, RxTotal: 6000, TxTotal: 3000},
	})
	keys, m := poolJSONKeys(t, resp)
	poolExpectKeys(t, keys, "points", "latest")
	points, ok := m["points"].([]any)
	if !ok || len(points) != 1 {
		t.Fatalf("points %v", m["points"])
	}
	pk := poolSortedKeys(points[0].(map[string]any))
	poolExpectKeys(t, pk, "at", "cpu_percent", "mem_percent", "load1", "rx_speed", "tx_speed", "tcp_conns")
	if got := points[0].(map[string]any)["at"]; got != "2026-10-12T08:00:00Z" {
		t.Fatalf("at %v", got)
	}
	lk := poolSortedKeys(m["latest"].(map[string]any))
	poolExpectKeys(t, lk, "cpu_percent", "mem_used_mb", "mem_total_mb", "disk_used_gb", "disk_total_gb",
		"load1", "load5", "load15", "tcp_conns", "uptime_sec", "rx_total", "tx_total")
}
