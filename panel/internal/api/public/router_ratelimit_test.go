package public

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

// countingRedis 截住全部命令、不连网络：限流脚本恒回「放行」，数限流计了几个维度（incrs）、
// 打了几次 Valkey（trips）
type countingRedis struct {
	mu    sync.Mutex
	incrs int
	trips int
}

func (c *countingRedis) DialHook(next redis.DialHook) redis.DialHook {
	return func(context.Context, string, string) (net.Conn, error) {
		return nil, net.ErrClosed
	}
}

func (c *countingRedis) ProcessHook(redis.ProcessHook) redis.ProcessHook {
	return func(_ context.Context, cmd redis.Cmder) error {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.trips++
		switch v := cmd.(type) {
		case *redis.Cmd:
			// EVALSHA / EVAL：参数是 命令、脚本、键数、键…、参数…
			if keys, ok := v.Args()[2].(int); ok {
				c.incrs += keys
			}
			v.SetVal([]any{int64(0), int64(0)})
		case *redis.IntCmd:
			c.incrs++
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

// takeTrips 返回并清零 Valkey 往返次数。
func (c *countingRedis) takeTrips() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := c.trips
	c.trips = 0
	return n
}

// 静态前端（入口与 /assets/*）挂在基础限流之外：不碰 Valkey、不占每 IP 额度；
// 其余路由（探针、订阅、/v1、404）照旧逐个经过 pub_ip + pub_net 两个维度
func TestPublicStaticAppBypassesRateLimitButRoutesDoNot(t *testing.T) {
	counter := &countingRedis{}
	rdb := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"})
	rdb.AddHook(counter)
	t.Cleanup(func() { _ = rdb.Close() })
	router := NewRouter(Deps{
		Cfg:   &config.Config{RateLimitPerIPPerMinute: 120},
		Log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		Redis: rdb,
	})

	serve := func(method, path string) int {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
		return rec.Code
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

	// 基础限流是两个维度（IP 与网段），每个请求恰好各计一次。/v1/plans 在这里缺依赖会回 500
	// （Recovery 兜住），但限流在处理器之前，计数照旧
	for _, path := range []string{"/healthz", "/nope", "/v1/plans", "/0123456789ab/sometoken"} {
		serve(http.MethodGet, path)
		if n := counter.take(); n != 2 {
			t.Fatalf("GET %s counted %d times, want 2 (pub_ip + pub_net)", path, n)
		}
		// 两个维度在一次往返里判定（限流脚本），不再各打一次 INCR
		if n := counter.takeTrips(); n != 1 {
			t.Fatalf("GET %s took %d Valkey round trips, want 1", path, n)
		}
	}
	counter.takeTrips()
	// /v1 下的路由也要经过基础限流（404 会再被子路由的 NotFound 计一次，只会更严）
	serve(http.MethodGet, "/v1/definitely-missing")
	if n := counter.take(); n < 2 {
		t.Fatalf("GET /v1/definitely-missing counted %d times, want at least 2", n)
	}
	// 静态路径上不允许的方法同样受限
	serve(http.MethodPost, "/assets/index-abc.js")
	if n := counter.take(); n < 2 {
		t.Fatalf("POST /assets/index-abc.js counted %d times, want at least 2", n)
	}
}
