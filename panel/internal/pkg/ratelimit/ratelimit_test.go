package ratelimit

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/httpx"
	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
)

func testRedis(t *testing.T) (*miniredis.Miniredis, *redis.Client) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	t.Cleanup(mr.Close)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr(), MaxRetries: -1})
	t.Cleanup(func() { _ = rdb.Close() })
	return mr, rdb
}

func TestPerMinuteThirdRequestRejected(t *testing.T) {
	_, rdb := testRedis(t)
	lim := New(rdb)
	ctx := context.Background()
	rule := PerMinute("test_min", 2)

	for i := 0; i < 2; i++ {
		if err := lim.Allow(ctx, rule, "k1"); err != nil {
			t.Fatalf("第 %d 次应放行: %v", i+1, err)
		}
	}
	err := lim.Allow(ctx, rule, "k1")
	if err == nil {
		t.Fatal("第 3 次应被拒")
	}
	e := errs.As(err)
	if e.Code != errs.RateLimited {
		t.Fatalf("code=%s", e.Code)
	}
	if e.RetryAfterSeconds < 1 {
		t.Fatalf("RetryAfterSeconds=%d", e.RetryAfterSeconds)
	}
}

func TestDifferentKeysIndependent(t *testing.T) {
	_, rdb := testRedis(t)
	lim := New(rdb)
	ctx := context.Background()
	rule := PerMinute("test_keys", 1)

	if err := lim.Allow(ctx, rule, "a"); err != nil {
		t.Fatal(err)
	}
	if err := lim.Allow(ctx, rule, "b"); err != nil {
		t.Fatal(err)
	}
	if err := lim.Allow(ctx, rule, "a"); err == nil {
		t.Fatal("key a 第二次应被拒")
	}
}

func TestRedisDownNonStrictAllows(t *testing.T) {
	mr, rdb := testRedis(t)
	lim := New(rdb)
	ctx := context.Background()
	rule := PerMinute("test_loose", 1)

	mr.Close()
	if err := lim.Allow(ctx, rule, "x"); err != nil {
		t.Fatalf("非 Strict 应放行: %v", err)
	}
}

func TestRedisDownStrictUnavailable(t *testing.T) {
	mr, rdb := testRedis(t)
	lim := New(rdb)
	ctx := context.Background()
	rule := PerMinute("test_strict", 1).StrictMode()

	mr.Close()
	err := lim.Allow(ctx, rule, "x")
	if err == nil {
		t.Fatal("Strict 应拒绝")
	}
	e := errs.As(err)
	if e.Code != errs.Unavailable {
		t.Fatalf("code=%s", e.Code)
	}
}

func TestMiddlewareSkipsWhenKeyFalse(t *testing.T) {
	_, rdb := testRedis(t)
	lim := New(rdb)
	gin.SetMode(gin.TestMode)

	rule := PerMinute("mw_skip", 1)
	h := lim.Middleware(
		Dimension{
			Rule: rule,
			Key:  func(c *gin.Context) (string, bool) { return "", false },
		},
		Dimension{
			Rule: rule,
			Key:  func(c *gin.Context) (string, bool) { return "always", true },
		},
	)

	r := gin.New()
	r.Use(httpx.Errors())
	r.GET("/x", h, func(c *gin.Context) { c.Status(http.StatusOK) })

	for i := 0; i < 2; i++ {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/x", nil)
		r.ServeHTTP(w, req)
		if i == 0 && w.Code != http.StatusOK {
			t.Fatalf("第一次 status=%d", w.Code)
		}
		if i == 1 && w.Code == http.StatusOK {
			t.Fatal("第二次应被限流")
		}
	}
}

func TestCooldownAndStrictMode(t *testing.T) {
	r := Cooldown("cd", 10)
	if r.Limit.Rate != 1 || r.Limit.Burst != 1 {
		t.Fatal("Cooldown 限流参数")
	}
	r2 := PerMinute("x", 3).StrictMode()
	if !r2.Strict {
		t.Fatal("StrictMode")
	}
}

func TestAllowRedisErrorWrapped(t *testing.T) {
	mr, rdb := testRedis(t)
	lim := New(rdb)
	ctx := context.Background()
	rule := PerWindow("w", 5, 60).StrictMode()
	mr.Close()
	err := lim.Allow(ctx, rule, "k")
	if err == nil || errs.As(err).Code != errs.Unavailable {
		t.Fatalf("%v", err)
	}
}
