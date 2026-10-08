package public

import (
	"context"
	"encoding/json"
	"fmt"
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

	"github.com/go-chi/chi/v5"
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

// TestSubscriptionRotateRateLimit 钉住门户重置订阅链接的限频（用户 2026-10-07 定口径，购买模型
// 统一后按份）：每份两次之间至少隔 10 分钟、每份当天第 6 次被拒；重置 A 之后马上重置 B 不受
// 影响；按账号每天最多 20 次。被拒的请求不进处理器（旧链接不受影响），提示写明还要等几分钟；
// 别的用户不受牵连。
func TestSubscriptionRotateRateLimit(t *testing.T) {
	rdb, clock := newLuaLimiterRedis(t)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	rotated := 0
	limited := middleware.RateLimit(rdb, log, subscriptionRotateLimits()...)(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { rotated++; w.WriteHeader(http.StatusOK) }))
	// 限频挂在 r.With 上，路由已匹配：按份的维度要从路由参数里取订阅 ID
	router := chi.NewRouter()
	router.Method(http.MethodPost, "/v1/me/subscriptions/{id}/rotate", limited)
	sub := func(n int) string { return fmt.Sprintf("7a1b0000-0000-4000-8000-%012d", n) }
	subA, subB := sub(1), sub(2)
	rotate := func(user, subID string) (int, string) {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/v1/me/subscriptions/"+subID+"/rotate", nil)
		req = req.WithContext(httpx.WithPrincipal(req.Context(), &httpx.Principal{Kind: "user", UserID: user}))
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		var body struct {
			Error struct{ Message string } `json:"error"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &body)
		return w.Code, body.Error.Message
	}
	waitMinutes := func(msg string) int {
		t.Helper()
		if !strings.HasPrefix(msg, "操作太频繁，请 ") || !strings.HasSuffix(msg, " 分钟后再试") {
			t.Fatalf("rate limit message=%q", msg)
		}
		m, _ := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(msg, "操作太频繁，请 "), " 分钟后再试"))
		return m
	}

	// 重置 A 之后马上重置 B 都成功；10 分钟内再重置 A 被拒，提示还要等 9 分钟（向上取整）
	if code, _ := rotate("u1", subA); code != http.StatusOK {
		t.Fatalf("first rotation of A status=%d", code)
	}
	if code, msg := rotate("u1", subB); code != http.StatusOK || rotated != 2 {
		t.Fatalf("rotating B right after A: status=%d msg=%q", code, msg)
	}
	clock.advance(time.Minute + 30*time.Second)
	code, msg := rotate("u1", subA)
	if code != http.StatusTooManyRequests || msg != "操作太频繁，请 9 分钟后再试" || rotated != 2 {
		t.Fatalf("second rotation of A within 10 minutes: status=%d msg=%q rotated=%d", code, msg, rotated)
	}
	// 同一份订阅 ID 换个写法（大写）也是同一个计数
	if code, _ := rotate("u1", strings.ToUpper(subA)); code != http.StatusTooManyRequests {
		t.Fatalf("upper-case id escaped the per-subscription gap: status=%d", code)
	}
	// 别的用户不受牵连
	if code, _ := rotate("u2", subA); code != http.StatusOK || rotated != 3 {
		t.Fatalf("another user's rotation status=%d", code)
	}
	// 满 10 分钟后 A 可以再换；被间隔拦下的那几次不占当天次数：A 再换 4 次（共 5 次）都放行
	for i := 2; i <= 5; i++ {
		clock.advance(10*time.Minute + time.Second)
		if code, msg := rotate("u1", subA); code != http.StatusOK {
			t.Fatalf("rotation %d of A after the gap: status=%d msg=%q", i, code, msg)
		}
	}
	// A 当天第 6 次被拒，提示按 A 当天第一次重置起 24 小时算
	clock.advance(10*time.Minute + time.Second)
	code, msg = rotate("u1", subA)
	if code != http.StatusTooManyRequests || rotated != 7 {
		t.Fatalf("sixth rotation of A in a day: status=%d msg=%q rotated=%d", code, msg, rotated)
	}
	elapsed := 1*time.Minute + 30*time.Second + 5*(10*time.Minute+time.Second)
	if minutes := waitMinutes(msg); minutes != int((24*time.Hour-elapsed+time.Minute-1)/time.Minute) {
		t.Fatalf("sixth rotation of A waits %d minutes, want the rest of A's 24 hours", minutes)
	}

	// 按账号每天 20 次：u1 已成功 6 次（A 5 次、B 1 次），再换 14 份别的都放行，第 21 次被拒
	for n := 3; n < 17; n++ {
		if code, msg := rotate("u1", sub(n)); code != http.StatusOK {
			t.Fatalf("rotation of subscription %d: status=%d msg=%q", n, code, msg)
		}
	}
	code, msg = rotate("u1", sub(17))
	if code != http.StatusTooManyRequests || rotated != 21 {
		t.Fatalf("21st rotation of the account in a day: status=%d msg=%q rotated=%d", code, msg, rotated)
	}
	if minutes := waitMinutes(msg); minutes != int((24*time.Hour-elapsed+time.Minute-1)/time.Minute) {
		t.Fatalf("21st rotation waits %d minutes, want the rest of the account's 24 hours", minutes)
	}
	// 24 小时后重新计数
	clock.advance(24 * time.Hour)
	if code, _ := rotate("u1", subA); code != http.StatusOK {
		t.Fatalf("rotation on the next day status=%d", code)
	}
}

// 路由契约：重置接口挂着 subscriptionRotateLimits，只注册一次；三条规则都是冷却式、带等待时间
// 提示：每份的间隔在前、每份每天 5 次其次（都按「用户:订阅」），按账号每天 20 次最后。
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
	gap := strings.Index(limits, `middleware.ByAccountParam("sub_rotate_gap", "id", 10*time.Minute, 1).AsCooldown().WithRetryHint()`)
	day := strings.Index(limits, `middleware.ByAccountParam("sub_rotate_day", "id", 24*time.Hour, 5).AsCooldown().WithRetryHint()`)
	acct := strings.Index(limits, `middleware.ByAccount("sub_rotate_acct_day", 24*time.Hour, 20).AsCooldown().WithRetryHint()`)
	if gap < 0 || day < gap || acct < day {
		t.Fatalf("rotate limits must be the per-subscription 10-minute gap, 5 per subscription per day, then 20 per account per day:\n%s", limits)
	}
}

// 改名接口挂着按账号每分钟 10 次的限频，只注册一次。
func TestSubscriptionRenameRouteCarriesItsRateLimit(t *testing.T) {
	router := sourcetest.Load(t, ".").Decl("NewRouter")
	const route = `Patch("/me/subscriptions/{id}", h.renameSubscription)`
	if strings.Count(router, route) != 1 {
		t.Fatal("rename route must have exactly one registration")
	}
	routeAt := strings.Index(router, route)
	segment := router[strings.LastIndex(router[:routeAt], "r.With("):routeAt]
	if !strings.Contains(segment, `middleware.ByAccount("sub_rename", time.Minute, 10)`) {
		t.Fatalf("rename route must carry the sub_rename limit, got %q", segment)
	}
}
