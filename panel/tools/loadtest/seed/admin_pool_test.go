package seed

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// poolGateway 记下每个请求的来源头、令牌与在途并发数；登录给每个来源一枚专属令牌，
// 之后带别人的令牌会被拒，证明各会话互不串用。
type poolGateway struct {
	mu       sync.Mutex
	ipByTok  map[string]string
	loginIPs []string
	perIP    map[string]int
	revoked  map[string]int
	inflight atomic.Int32
	peak     atomic.Int32
	hold     time.Duration
}

func newPoolGateway(hold time.Duration) *poolGateway {
	return &poolGateway{ipByTok: map[string]string{}, perIP: map[string]int{}, revoked: map[string]int{}, hold: hold}
}

func (g *poolGateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	n := g.inflight.Add(1)
	defer g.inflight.Add(-1)
	for {
		p := g.peak.Load()
		if n <= p || g.peak.CompareAndSwap(p, n) {
			break
		}
	}
	time.Sleep(g.hold)
	reply := func(status int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}
	ip := r.Header.Get("X-Real-IP")
	g.mu.Lock()
	defer g.mu.Unlock()
	if r.URL.Path == "/v1/auth/login" {
		tok := "tok-" + ip
		g.ipByTok[tok] = ip
		g.loginIPs = append(g.loginIPs, ip)
		reply(http.StatusOK, map[string]any{"access_token": tok})
		return
	}
	if g.ipByTok[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")] != ip {
		reply(http.StatusUnauthorized, map[string]any{"error": map[string]any{"code": "unauthorized"}})
		return
	}
	g.perIP[ip]++
	if strings.HasSuffix(r.URL.Path, "/revoke-identity") {
		id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1/nodes/"), "/revoke-identity")
		g.revoked[id]++
		if strings.HasPrefix(id, "gone") {
			reply(http.StatusNotFound, map[string]any{"error": map[string]any{"code": "not_found"}})
			return
		}
	}
	reply(http.StatusOK, map[string]any{"ok": true})
}

func TestAdminPoolSessionsKeepTheirOwnIPAndToken(t *testing.T) {
	gw := newPoolGateway(0)
	srv := httptest.NewServer(gw)
	defer srv.Close()
	p := newAdminPool(srv.URL, "admin@example.test", "not-a-secret", 0, 4, []string{"X-Real-IP"})
	ctx := context.Background()
	if err := p.login(ctx); err != nil {
		t.Fatal(err)
	}
	sort.Strings(gw.loginIPs)
	if strings.Join(gw.loginIPs, ",") != "198.51.100.1,198.51.100.2,198.51.100.3,198.51.100.4" {
		t.Fatalf("login source IPs = %v", gw.loginIPs)
	}
	// 40 个任务，令牌与来源错配会换来 401，forEach 就会失败
	if err := p.forEach(ctx, 40, func(ctx context.Context, c *adminClient, i int) error {
		_, err := c.call(ctx, http.MethodGet, "/v1/ping", nil)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	total := 0
	for ip, n := range gw.perIP {
		if n == 0 {
			t.Fatalf("session %s did no work", ip)
		}
		total += n
	}
	if total != 40 || len(gw.perIP) != 4 {
		t.Fatalf("work spread over %d sessions totalling %d requests: %v", len(gw.perIP), total, gw.perIP)
	}
	if got := p.Calls(); got != 4+40 {
		t.Fatalf("Calls() = %d, want 44 (4 logins + 40 requests)", got)
	}
}

func TestAdminPoolSingleSessionSendsNoSourceHeader(t *testing.T) {
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Header.Get("X-Real-IP"))
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "t"})
	}))
	defer srv.Close()
	p := newAdminPool(srv.URL, "admin@example.test", "not-a-secret", 0, 1, []string{"X-Real-IP"})
	if len(p.clients) != 1 || p.primary().ip != "" {
		t.Fatalf("one worker must behave as before the pool existed: %+v", p.primary())
	}
	if err := p.login(context.Background()); err != nil || len(seen) != 1 || seen[0] != "" {
		t.Fatalf("login headers %q, err %v", seen, err)
	}
}

func TestAdminPoolRunsSessionsConcurrentlyAndPacesEachOne(t *testing.T) {
	gw := newPoolGateway(15 * time.Millisecond)
	srv := httptest.NewServer(gw)
	defer srv.Close()
	p := newAdminPool(srv.URL, "admin@example.test", "not-a-secret", 10*time.Millisecond, 4, []string{"X-Real-IP"})
	ctx := context.Background()
	if err := p.login(ctx); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	if err := p.forEach(ctx, 32, func(ctx context.Context, c *adminClient, i int) error {
		calls.Add(1)
		_, err := c.call(ctx, http.MethodGet, "/v1/ping", nil)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 32 {
		t.Fatalf("fn ran %d times, want 32", calls.Load())
	}
	if gw.peak.Load() < 2 {
		t.Fatalf("peak in-flight %d: sessions did not run in parallel", gw.peak.Load())
	}
}

func TestAdminPoolStopsAtTheFirstError(t *testing.T) {
	srv := httptest.NewServer(newPoolGateway(0))
	defer srv.Close()
	p := newAdminPool(srv.URL, "admin@example.test", "not-a-secret", 0, 3, []string{"X-Real-IP"})
	boom := errors.New("boom")
	var ran atomic.Int32
	err := p.forEach(context.Background(), 1000, func(ctx context.Context, c *adminClient, i int) error {
		ran.Add(1)
		if i == 5 {
			return boom
		}
		time.Sleep(time.Millisecond)
		return ctx.Err()
	})
	if !errors.Is(err, boom) {
		t.Fatalf("forEach error = %v, want the first failure", err)
	}
	if ran.Load() >= 1000 {
		t.Fatal("a failure must stop the remaining jobs")
	}
}

func TestRevokeIdentitiesToleratesAlreadyRevoked(t *testing.T) {
	gw := newPoolGateway(0)
	srv := httptest.NewServer(gw)
	defer srv.Close()
	p := newAdminPool(srv.URL, "admin@example.test", "not-a-secret", 0, 3, []string{"X-Real-IP"})
	ctx := context.Background()
	if err := p.login(ctx); err != nil {
		t.Fatal(err)
	}
	ids := []string{"n1", "n2", "gone1", "n3", "gone2"}
	got, err := revokeIdentities(ctx, p, ids)
	if err != nil {
		t.Fatal(err)
	}
	if got != 3 {
		t.Fatalf("revoked = %d, want 3 (the two 404s are already gone, not errors)", got)
	}
	for _, id := range ids {
		if gw.revoked[id] != 1 {
			t.Fatalf("node %s revoked %d times, want exactly once", id, gw.revoked[id])
		}
	}
	if n, err := revokeIdentities(ctx, p, nil); err != nil || n != 0 {
		t.Fatalf("no nodes: %d %v", n, err)
	}
}

func TestActiveIdentityNodesSQLIsScopedToLoadtestNodes(t *testing.T) {
	for _, want := range []string{"n.tenant_id = $1::uuid", "n.name LIKE $2", "n.serving_status <> 'retired'", "i.status = 'active'"} {
		if !strings.Contains(activeIdentityNodesSQL, want) {
			t.Fatalf("identity listing lacks %q", want)
		}
	}
	for _, bad := range []string{"DELETE", "UPDATE"} {
		if strings.Contains(activeIdentityNodesSQL, bad) {
			t.Fatalf("the listing must be read-only, found %s", bad)
		}
	}
}
