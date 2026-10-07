package public

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/redis/go-redis/v9"

	"github.com/aegispanel/aegis/internal/platform/config"
)

// strictCountingRedis 在 countingRedis 之外也接住严格限流用的 EVAL（回 1，永不触发），
// 两种计数合在一起数
type strictCountingRedis struct{ countingRedis }

func (c *strictCountingRedis) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	base := c.countingRedis.ProcessHook(next)
	return func(ctx context.Context, cmd redis.Cmder) error {
		if v, ok := cmd.(*redis.Cmd); ok && (cmd.Name() == "eval" || cmd.Name() == "evalsha") {
			c.mu.Lock()
			c.incrs++
			c.mu.Unlock()
			v.SetVal(int64(1))
			return nil
		}
		return base(ctx, cmd)
	}
}

// 找回密码两步都在免鉴权区，按 IP、网段、（第 1 步）租户与邮箱哈希严格限流：
// 每个请求在基础限流（pub_ip + pub_net）之外，第 1 步再计 4 次、第 2 步再计 3 次。
func TestPasswordResetRoutesAreStrictlyRateLimited(t *testing.T) {
	counter := &strictCountingRedis{}
	rdb := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"})
	rdb.AddHook(counter)
	t.Cleanup(func() { _ = rdb.Close() })
	router := NewRouter(Deps{
		Cfg:   &config.Config{RateLimitPerIPPerMinute: 120, RateLimitAuthPerMinute: 10},
		Log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		Redis: rdb,
	})
	assertRouteContract(t, router, http.MethodPost, "/v1/auth/password-reset/start")
	assertRouteContract(t, router, http.MethodPost, "/v1/auth/password-reset/complete")

	for path, want := range map[string]int{
		"/v1/auth/password-reset/start":    2 + 4,
		"/v1/auth/password-reset/complete": 2 + 3,
	} {
		req := httptest.NewRequest(http.MethodPost, path,
			strings.NewReader(`{"email":"someone@example.test","code":"123456","new_password":"abcd12345"}`))
		req.Header.Set("Content-Type", "application/json")
		// 缺 Identity 时处理器会 panic、被 Recovery 兜成 500；限流在处理器之前，计数照旧
		router.ServeHTTP(httptest.NewRecorder(), req)
		if n := counter.take(); n != want {
			t.Fatalf("POST %s counted %d limits, want %d", path, n, want)
		}
	}
}
