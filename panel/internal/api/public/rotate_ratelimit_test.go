package public

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/aegispanel/aegis/internal/middleware"
	"github.com/aegispanel/aegis/internal/platform/httpx"
	"github.com/aegispanel/aegis/internal/platform/sourcetest"
)

// luaLimiterRedis 截住全部命令、不连网络，在内存里按限流脚本（middleware 的 rateLimitScriptSource）
// 的语义执行 EVALSHA / EVAL：逐维度 INCR，首次建键设过期，第一个超限的维度返回
// {序号, 计数, 剩余毫秒}。时间由 now 驱动，测试里手动往前拨。
type luaLimiterRedis struct {
	mu      sync.Mutex
	now     time.Time
	count   map[string]int64
	expires map[string]time.Time
}

func newLuaLimiterRedis(t *testing.T) (*redis.Client, *luaLimiterRedis) {
	t.Helper()
	fake := &luaLimiterRedis{now: time.Date(2026, 10, 7, 9, 0, 0, 0, time.UTC),
		count: map[string]int64{}, expires: map[string]time.Time{}}
	rdb := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", MaxRetries: -1})
	rdb.AddHook(fake)
	t.Cleanup(func() { _ = rdb.Close() })
	return rdb, fake
}

func (f *luaLimiterRedis) advance(d time.Duration) {
	f.mu.Lock()
	f.now = f.now.Add(d)
	f.mu.Unlock()
}

func (f *luaLimiterRedis) DialHook(redis.DialHook) redis.DialHook {
	return func(context.Context, string, string) (net.Conn, error) { return nil, net.ErrClosed }
}

func (f *luaLimiterRedis) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}

func (f *luaLimiterRedis) ProcessHook(redis.ProcessHook) redis.ProcessHook {
	return func(_ context.Context, cmd redis.Cmder) error {
		c, ok := cmd.(*redis.Cmd)
		if !ok {
			return nil
		}
		args := c.Args() // evalsha|eval, 脚本, 键数, 键…, 参数…
		n, _ := args[2].(int)
		keys, argv := args[3:3+n], args[3+n:]
		f.mu.Lock()
		defer f.mu.Unlock()
		for i, k := range keys {
			key := k.(string)
			if exp, ok := f.expires[key]; ok && !f.now.Before(exp) {
				delete(f.count, key)
				delete(f.expires, key)
			}
			f.count[key]++
			if f.count[key] == 1 {
				ms, _ := strconv.ParseInt(toString(argv[2*i+1]), 10, 64)
				f.expires[key] = f.now.Add(time.Duration(ms) * time.Millisecond)
			}
			limit, _ := strconv.ParseInt(toString(argv[2*i]), 10, 64)
			if f.count[key] > limit {
				c.SetVal([]any{int64(i + 1), f.count[key], f.expires[key].Sub(f.now).Milliseconds()})
				return nil
			}
		}
		c.SetVal([]any{int64(0), int64(0)})
		return nil
	}
}

func toString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case int:
		return strconv.Itoa(x)
	case int64:
		return strconv.FormatInt(x, 10)
	}
	return ""
}

// TestSubscriptionRotateRateLimit 钉住门户重置订阅链接的限频（用户 2026-10-07）：
// 两次之间至少隔 10 分钟，当天第 6 次被拒；被拒的请求不进处理器（旧链接不受影响），
// 提示写明还要等几分钟；按用户计数，别的用户不受牵连。
func TestSubscriptionRotateRateLimit(t *testing.T) {
	rdb, clock := newLuaLimiterRedis(t)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	rotated := 0
	h := middleware.RateLimit(rdb, log, subscriptionRotateLimits()...)(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { rotated++; w.WriteHeader(http.StatusOK) }))
	rotate := func(user string) (int, string) {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/v1/me/subscriptions/x/rotate", nil)
		req = req.WithContext(httpx.WithPrincipal(req.Context(), &httpx.Principal{Kind: "user", UserID: user}))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		var body struct {
			Error struct{ Message string } `json:"error"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &body)
		return w.Code, body.Error.Message
	}

	// 第 1 次放行；第 2 次在 10 分钟内被拒，提示还要等 9 分钟（向上取整）
	if code, _ := rotate("u1"); code != http.StatusOK {
		t.Fatalf("first rotation status=%d", code)
	}
	clock.advance(time.Minute + 30*time.Second)
	code, msg := rotate("u1")
	if code != http.StatusTooManyRequests || msg != "操作太频繁，请 9 分钟后再试" || rotated != 1 {
		t.Fatalf("second rotation within 10 minutes: status=%d msg=%q rotated=%d", code, msg, rotated)
	}
	// 别的用户不受牵连
	if code, _ := rotate("u2"); code != http.StatusOK || rotated != 2 {
		t.Fatalf("another user's rotation status=%d", code)
	}
	// 满 10 分钟后可以再换；被间隔拦下的那次不占当天次数：再换 3 次（共 5 次）都放行
	for i := 2; i <= 5; i++ {
		clock.advance(10*time.Minute + time.Second)
		if code, msg := rotate("u1"); code != http.StatusOK {
			t.Fatalf("rotation %d after the gap: status=%d msg=%q", i, code, msg)
		}
	}
	// 当天第 6 次被拒，提示按当天第一次重置起 24 小时算
	clock.advance(10*time.Minute + time.Second)
	code, msg = rotate("u1")
	if code != http.StatusTooManyRequests || rotated != 6 {
		t.Fatalf("sixth rotation of the day: status=%d msg=%q rotated=%d", code, msg, rotated)
	}
	if !strings.HasPrefix(msg, "操作太频繁，请 ") || !strings.HasSuffix(msg, " 分钟后再试") {
		t.Fatalf("sixth rotation message=%q", msg)
	}
	minutes, _ := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(msg, "操作太频繁，请 "), " 分钟后再试"))
	if elapsed := 1*time.Minute + 30*time.Second + 5*(10*time.Minute+time.Second); minutes != int((24*time.Hour-elapsed+time.Minute-1)/time.Minute) {
		t.Fatalf("sixth rotation waits %d minutes, want the rest of the 24 hours", minutes)
	}
	// 24 小时后重新计数
	clock.advance(24 * time.Hour)
	if code, _ := rotate("u1"); code != http.StatusOK {
		t.Fatalf("rotation on the next day status=%d", code)
	}
}

// 路由契约：重置接口挂着 subscriptionRotateLimits，只注册一次；两条规则都是按用户、冷却式、
// 带等待时间提示，间隔在前。
func TestSubscriptionRotateRouteCarriesItsRateLimit(t *testing.T) {
	pkg := sourcetest.Load(t, ".")
	router := pkg.Decl("NewRouter")
	const route = `Post("/me/subscriptions/{id}/rotate", h.rotateSubscriptionLink)`
	if strings.Count(router, route) != 1 {
		t.Fatal("rotate route must have exactly one registration")
	}
	routeAt := strings.Index(router, route)
	segment := router[strings.LastIndex(router[:routeAt], "r.With("):routeAt]
	if !strings.Contains(segment, "middleware.RateLimit(d.Redis, d.Log, subscriptionRotateLimits()...)") {
		t.Fatalf("rotate route must carry subscriptionRotateLimits, got %q", segment)
	}
	limits := pkg.Decl("subscriptionRotateLimits")
	gap := strings.Index(limits, `middleware.ByAccount("sub_rotate_gap", 10*time.Minute, 1).AsCooldown().WithRetryHint()`)
	day := strings.Index(limits, `middleware.ByAccount("sub_rotate_day", 24*time.Hour, 5).AsCooldown().WithRetryHint()`)
	if gap < 0 || day < gap {
		t.Fatalf("rotate limits must be the 10-minute gap then 5 per day:\n%s", limits)
	}
}
