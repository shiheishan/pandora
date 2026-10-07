package admin

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/redis/go-redis/v9"

	"github.com/aegispanel/aegis/internal/platform/config"
)

// countingRedis 截住全部命令、不连网络：INCR 恒回 1（永不触发限流），只数限流打了几次 Valkey
type countingRedis struct {
	mu    sync.Mutex
	incrs int
}

func (c *countingRedis) DialHook(next redis.DialHook) redis.DialHook {
	return func(context.Context, string, string) (net.Conn, error) {
		return nil, net.ErrClosed
	}
}

func (c *countingRedis) ProcessHook(redis.ProcessHook) redis.ProcessHook {
	return func(_ context.Context, cmd redis.Cmder) error {
		switch v := cmd.(type) {
		case *redis.IntCmd:
			c.mu.Lock()
			c.incrs++
			c.mu.Unlock()
			v.SetVal(1)
		case *redis.BoolCmd:
			v.SetVal(true)
		}
		return nil
	}
}

func (c *countingRedis) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}

func (c *countingRedis) take() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := c.incrs
	c.incrs = 0
	return n
}

// 管理控制台前端（入口与 /assets/*）挂在基础限流之外；其余路由照旧经过 adm_ip
func TestAdminStaticAppBypassesRateLimitButRoutesDoNot(t *testing.T) {
	counter := &countingRedis{}
	rdb := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"})
	rdb.AddHook(counter)
	t.Cleanup(func() { _ = rdb.Close() })
	router := NewRouter(Deps{
		Cfg:   &config.Config{},
		Log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		Redis: rdb,
	})

	serve := func(method, path string) {
		router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(method, path, nil))
	}

	for _, req := range [][2]string{
		{http.MethodGet, "/"},
		{http.MethodHead, "/"},
		{http.MethodGet, "/assets/index-abc.js"},
		{http.MethodHead, "/assets/missing.css"},
	} {
		serve(req[0], req[1])
		if n := counter.take(); n != 0 {
			t.Fatalf("%s %s hit the rate limiter %d times; static files must bypass it", req[0], req[1], n)
		}
	}

	// 基础限流只有 adm_ip 一个维度，每个请求恰好计一次
	for _, path := range []string{"/healthz", "/nope"} {
		serve(http.MethodGet, path)
		if n := counter.take(); n != 1 {
			t.Fatalf("GET %s counted %d times, want 1 (adm_ip)", path, n)
		}
	}
	for _, req := range [][2]string{
		{http.MethodGet, "/v1/definitely-missing"},
		{http.MethodPost, "/assets/index-abc.js"},
	} {
		serve(req[0], req[1])
		if n := counter.take(); n < 1 {
			t.Fatalf("%s %s counted %d times, want at least 1", req[0], req[1], n)
		}
	}
}
