package app

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/pkg/db"
	"github.com/aegispanel/aegis/internal/pkg/metrics"
	"github.com/aegispanel/aegis/internal/pkg/realtime"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestEnginesExposeHTTPMetrics(t *testing.T) {
	cfg := testConfig(t)
	rdb := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"})
	t.Cleanup(func() { _ = rdb.Close() })
	r := &runtime{cfg: cfg, log: zap.NewNop(), redis: rdb}

	svc, err := buildServices(r)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.close)
	hub := realtime.NewHub(nil, nil, 100)
	reg := metrics.NewRegistry()
	publicEng, adminEng := newEngines(r, svc, hub, metrics.NewHTTP(reg))

	pubW := httptest.NewRecorder()
	publicEng.ServeHTTP(pubW, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if pubW.Code != http.StatusOK {
		t.Fatalf("public /healthz: %d", pubW.Code)
	}
	admW := httptest.NewRecorder()
	adminEng.ServeHTTP(admW, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if admW.Code != http.StatusOK {
		t.Fatalf("admin /healthz: %d", admW.Code)
	}

	rec := httptest.NewRecorder()
	metrics.NewServer("127.0.0.1:0", reg).Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := rec.Body.String()
	want := []string{
		`pandora_http_requests_total{method="GET",route="/healthz",server="public",status="200"} 1`,
		`pandora_http_requests_total{method="GET",route="/healthz",server="admin",status="200"} 1`,
	}
	for _, s := range want {
		if !strings.Contains(body, s) {
			t.Errorf("/metrics 缺 %q\n%s", s, body)
		}
	}
}

func TestMetricsRegistryPools(t *testing.T) {
	g, err := gorm.Open(postgres.New(postgres.Config{
		DSN: "host=127.0.0.1 port=1 user=u dbname=d sslmode=disable",
	}), &gorm.Config{DisableAutomaticPing: true})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := g.DB()
	if err == nil {
		t.Cleanup(func() { _ = sqlDB.Close() })
	}
	rdb := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"})
	t.Cleanup(func() { _ = rdb.Close() })

	r := &runtime{cfg: testConfig(t), log: zap.NewNop(), db: db.Wrap(g), redis: rdb}
	reg, err := newMetricsRegistry(r)
	if err != nil {
		t.Fatal(err)
	}
	newGRPCMetrics(reg)

	mfs, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, mf := range mfs {
		names[mf.GetName()] = true
	}
	for _, n := range []string{"go_sql_open_connections", "pandora_redis_pool_hit_total", "go_goroutines"} {
		if !names[n] {
			t.Errorf("缺指标 %s", n)
		}
	}
}
