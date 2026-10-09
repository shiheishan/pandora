package middleware

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
	"github.com/aegispanel/aegis/internal/platform/realtime"
	"github.com/aegispanel/aegis/internal/platform/sourcetest"
)

const switchTestTenant = "11111111-1111-4111-8111-111111111111"

type switchFakeQuerier struct {
	mu      sync.Mutex
	enabled map[string]bool // 缺键 = 缺行
	err     error
	calls   int
}

func (q *switchFakeQuerier) QueryRowScoped(_ context.Context, s db.Scope, sql string, args []any, dest ...any) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.calls++
	if s.TenantID != switchTestTenant || !strings.Contains(sql, "FROM feature_switches WHERE tenant_id = $1 AND code = $2") || len(args) != 2 || args[0] != switchTestTenant {
		return errors.New("unexpected switch query")
	}
	if q.err != nil {
		return q.err
	}
	v, ok := q.enabled[args[1].(string)]
	if !ok {
		return pgx.ErrNoRows
	}
	*(dest[0].(*bool)) = v
	return nil
}

func (q *switchFakeQuerier) set(code string, v bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.enabled[code] = v
}

// useSwitchClock 把全局缓存换成可拨的时钟，测试结束恢复。
func useSwitchClock(t *testing.T) *time.Time {
	t.Helper()
	now := time.Unix(1_700_000_000, 0)
	old := switches
	switches = newSwitchCache(func() time.Time { return now })
	t.Cleanup(func() { switches = old })
	return &now
}

func TestFeatureSwitchCachesWithinTTL(t *testing.T) {
	now := useSwitchClock(t)
	q := &switchFakeQuerier{enabled: map[string]bool{"billing.checkout": false}}
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		on, err := switchEnabled(ctx, q, switchTestTenant, "billing.checkout")
		if err != nil || on {
			t.Fatalf("read %d: on=%v err=%v", i, on, err)
		}
	}
	if q.calls != 1 {
		t.Fatalf("database reads within TTL = %d, want 1", q.calls)
	}

	// 库里改了，TTL 内仍是旧值；过了 TTL 回库
	q.set("billing.checkout", true)
	*now = now.Add(switchCacheTTL - time.Millisecond)
	if on, _ := switchEnabled(ctx, q, switchTestTenant, "billing.checkout"); on {
		t.Fatal("cache expired before its TTL")
	}
	*now = now.Add(time.Millisecond)
	if on, _ := switchEnabled(ctx, q, switchTestTenant, "billing.checkout"); !on || q.calls != 2 {
		t.Fatalf("after TTL on=%v calls=%d, want fresh read", on, q.calls)
	}
}

// 缺行视为开启（急停开关），这个结论同样缓存；读库出错不缓存，下次重试。
func TestFeatureSwitchMissingRowFailsOpenAndErrorsAreNotCached(t *testing.T) {
	useSwitchClock(t)
	q := &switchFakeQuerier{enabled: map[string]bool{}}
	ctx := context.Background()
	if on, err := switchEnabled(ctx, q, switchTestTenant, "marketing.giftcard.redeem"); !on || err != nil {
		t.Fatalf("missing row on=%v err=%v", on, err)
	}
	if on, _ := switchEnabled(ctx, q, switchTestTenant, "marketing.giftcard.redeem"); !on || q.calls != 1 {
		t.Fatalf("missing-row result not cached: calls=%d", q.calls)
	}

	q.err = errors.New("connection reset")
	if _, err := switchEnabled(ctx, q, switchTestTenant, "admin.writes"); err == nil {
		t.Fatal("database error swallowed")
	}
	q.err = nil
	q.set("admin.writes", true)
	if on, err := switchEnabled(ctx, q, switchTestTenant, "admin.writes"); !on || err != nil {
		t.Fatalf("error was cached: on=%v err=%v", on, err)
	}
}

func TestSwitchCacheIsBounded(t *testing.T) {
	c := newSwitchCache(time.Now)
	for i := 0; i < switchCacheMax*3; i++ {
		key := switchKey{switchTestTenant, "code-" + string(rune('a'+i%26)) + strings.Repeat("x", i)}
		_, _ = c.Get(context.Background(), key, "", switchAlways, nil, func(context.Context) (bool, error) { return true, nil })
		if c.Len() > switchCacheMax {
			t.Fatalf("cache grew to %d entries", c.Len())
		}
	}
}

// 同一个开关的并发未命中只读一次库；Clear 时正在读的那一趟不写回（不会在失效之后留下旧值）。
func TestFeatureSwitchMissesCoalesceAndClearVoidsInFlightRead(t *testing.T) {
	useSwitchClock(t)
	q := &blockingSwitchQuerier{switchFakeQuerier: switchFakeQuerier{enabled: map[string]bool{"billing.checkout": true}},
		started: make(chan struct{}, 16), release: make(chan struct{})}
	ctx := context.Background()
	const workers = 8
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// 这一趟读到的是失效之前的值（开启），等着的人照常拿到它
			if on, err := switchEnabled(ctx, q, switchTestTenant, "billing.checkout"); !on || err != nil {
				t.Errorf("concurrent read: on=%v err=%v", on, err)
			}
		}()
	}
	<-q.started
	time.Sleep(20 * time.Millisecond) // 其余请求排到同一趟上
	// 读库进行中，后台关掉了开关并让本进程失效
	q.set("billing.checkout", false)
	InvalidateFeatureSwitches()
	close(q.release)
	wg.Wait()
	if q.calls != 1 {
		t.Fatalf("concurrent misses read the database %d times, want 1", q.calls)
	}
	q.release = make(chan struct{})
	close(q.release)
	if on, _ := switchEnabled(ctx, q, switchTestTenant, "billing.checkout"); on {
		t.Fatal("a read started before the invalidation was cached after it")
	}
}

type blockingSwitchQuerier struct {
	switchFakeQuerier
	started chan struct{}
	release chan struct{}
}

// QueryRowScoped 先读出值、再卡住：模拟「读到的是失效之前的旧值，回来得晚」。
func (q *blockingSwitchQuerier) QueryRowScoped(ctx context.Context, s db.Scope, sql string, args []any, dest ...any) error {
	err := q.switchFakeQuerier.QueryRowScoped(ctx, s, sql, args, dest...)
	q.started <- struct{}{}
	<-q.release
	return err
}

// 切开关的请求经过 AdminWritesGate 后，本进程缓存立即失效：关掉 admin.writes 的下一个写请求就被拒。
func TestAdminWritesGateInvalidatesCacheOnSwitchWrite(t *testing.T) {
	useSwitchClock(t)
	q := &switchFakeQuerier{enabled: map[string]bool{"admin.writes": true}}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ok := func(w http.ResponseWriter, _ *http.Request) { httpx.OK(w, map[string]any{"ok": true}) }
	r := chi.NewRouter()
	r.Route("/v1", func(r chi.Router) {
		r.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				next.ServeHTTP(w, req.WithContext(httpx.WithTenantID(req.Context(), switchTestTenant)))
			})
		})
		r.Use(AdminWritesGate(q, log))
		r.Post("/users/{id}/status", ok)
		r.Post("/switches/{code}", func(w http.ResponseWriter, req *http.Request) {
			q.set(chi.URLParam(req, "code"), false) // 处理器写库
			ok(w, req)
		})
	})
	do := func(path string) int {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`)))
		return w.Code
	}
	if code := do("/v1/users/x/status"); code != http.StatusOK {
		t.Fatalf("write before switch: %d", code)
	}
	if code := do("/v1/switches/admin.writes"); code != http.StatusOK {
		t.Fatalf("switch write: %d", code)
	}
	if code := do("/v1/users/x/status"); code != http.StatusServiceUnavailable {
		t.Fatalf("write after admin.writes off: %d, want 503 without waiting for the TTL", code)
	}
}

// 其它网关靠实时广播失效：收到 switches.changed 清空，别的主题不动。
func TestWatchFeatureSwitchChangesInvalidatesOnBroadcast(t *testing.T) {
	useSwitchClock(t)
	q := &switchFakeQuerier{enabled: map[string]bool{"billing.checkout": true}}
	hub := realtime.NewHub(nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(hub.Close)
	ctx, cancel := context.WithCancel(context.Background())
	wait := WatchFeatureSwitchChanges(ctx, hub, switchTestTenant)

	bg := context.Background()
	if on, _ := switchEnabled(bg, q, switchTestTenant, "billing.checkout"); !on {
		t.Fatal("initial read")
	}
	q.set("billing.checkout", false)
	hub.Publish(bg, realtime.ChannelAdmin(switchTestTenant), "orders.changed", nil)
	hub.Publish(bg, realtime.ChannelAdmin("22222222-2222-4222-8222-222222222222"), switchesChangedTopic, nil)
	time.Sleep(20 * time.Millisecond)
	if on, _ := switchEnabled(bg, q, switchTestTenant, "billing.checkout"); !on {
		t.Fatal("unrelated topic or another tenant's channel invalidated the cache")
	}

	hub.Publish(bg, realtime.ChannelAdmin(switchTestTenant), switchesChangedTopic,
		map[string]any{"code": "billing.checkout", "enabled": false})
	deadline := time.Now().Add(2 * time.Second)
	for {
		if on, _ := switchEnabled(bg, q, switchTestTenant, "billing.checkout"); !on {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("switches.changed did not invalidate the cache")
		}
		time.Sleep(5 * time.Millisecond)
	}

	cancel()
	done := make(chan struct{})
	go func() { wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("watcher did not stop on cancellation")
	}
}

// 广播主题由 api/admin 的 setSwitch 发出，两边字面量必须一致，否则失效静默退回 TTL。
func TestSwitchesChangedTopicMatchesAdminPublisher(t *testing.T) {
	body := sourcetest.Load(t, "../api/admin").Decl("handlers.setSwitch")
	if !strings.Contains(body, `realtime.ChannelAdmin(`) || !strings.Contains(body, `"`+switchesChangedTopic+`"`) {
		t.Fatalf("admin setSwitch no longer publishes %q on the admin channel", switchesChangedTopic)
	}
}

func BenchmarkFeatureSwitchCacheHit(b *testing.B) {
	q := &switchFakeQuerier{enabled: map[string]bool{"billing.checkout": true}}
	ctx := context.Background()
	_, _ = switchEnabled(ctx, q, switchTestTenant, "billing.checkout")
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, _ = switchEnabled(ctx, q, switchTestTenant, "billing.checkout")
		}
	})
}
