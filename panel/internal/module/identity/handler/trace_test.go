package handler

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/module/identity/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/google/uuid"
)

var (
	traceUserID = uuid.MustParse("77777777-7777-4777-8777-777777777777")
	traceAt     = time.Date(2026, 10, 2, 8, 30, 0, 0, time.UTC)
)

// traceFake 实现四个溯源方法并记下入参。
type traceFake struct {
	Service
	window string
	sort   string
	token  string
	// revealErr 非 nil 时 TraceReveal 直接返回它。
	revealErr error
}

func (f *traceFake) TraceIPs(_ context.Context, window string) (service.TraceIPReport, error) {
	f.window = window
	return service.TraceIPReport{
		Window: window, Since: traceAt,
		Overview: service.TraceOverview{
			DistinctIPs: 3, DistinctIPsApprox: true, Fetches: 10, FetchFailures: 1,
			Logins: 5, LoginFailures: 2, AnalyzedIPs: 3, DatacenterIPs: 1,
			TopRegions: []service.TraceRegion{{Region: "测试地区", IPs: 2, Hits: 7}},
		},
		IPs: []service.TraceIPRow{{
			Token: "tok-1", IPMasked: "198.51.100.*", Geo: "测试地区", NetworkKind: "datacenter",
			Hits: 9, Fetches: 6, Events: 3, Accounts: 1, AccountsCapped: false, UAKinds: 2,
			Last: traceAt,
			Users: []service.TraceUserRef{{
				ID: traceUserID, Email: "a@example.test", Status: "active",
			}},
		}},
	}, nil
}

func (f *traceFake) TraceSubscribers(_ context.Context, window, sort string) (service.TraceSubscriberReport, error) {
	f.window, f.sort = window, sort
	return service.TraceSubscriberReport{
		Window: window, Since: traceAt, Sort: sort,
		Rows: []service.TraceSubscriberRow{{
			UserID: traceUserID, Email: "a@example.test", Status: "active",
			Fetches: 4, Failures: 1, IPs: 2, UAKinds: 1,
			Families: nil, Regions: 1, Last: traceAt,
		}},
	}, nil
}

func (f *traceFake) TraceAccounts(_ context.Context, window, sort string) (service.TraceAccountReport, error) {
	f.window, f.sort = window, sort
	return service.TraceAccountReport{
		Window: window, Since: traceAt, Sort: sort,
		Rows: []service.TraceAccountRow{{
			UserID: traceUserID, Email: "a@example.test", Status: "active",
			LinkedIPs: 2, Logins: 3, LoginIPs: 2, FetchIPs: 1, SharedIPs: 1,
			UAKinds: 2, DatacenterHits: 1, Last: traceAt,
		}},
	}, nil
}

func (f *traceFake) TraceReveal(_ context.Context, token string) (service.TraceReveal, error) {
	f.token = token
	if f.revealErr != nil {
		return service.TraceReveal{}, f.revealErr
	}
	return service.TraceReveal{IP: "198.51.100.7", Geo: "测试地区", NetworkKind: "datacenter"}, nil
}

func TestTraceIPsShape(t *testing.T) {
	f := &traceFake{}
	e, _ := opsEngine(f)
	w := do(e, http.MethodGet, "/v1/trace/ips?window=7d", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d %s", w.Code, w.Body)
	}
	if f.window != "7d" {
		t.Fatalf("window %q", f.window)
	}
	got := decodeMap(t, w.Body.Bytes())
	assertKeys(t, got, "ips", "overview", "since", "window")
	ov, _ := got["overview"].(map[string]any)
	assertKeys(t, ov,
		"analyzed_ips", "datacenter_ips", "distinct_ips", "distinct_ips_approx", "fetch_failures",
		"fetches", "login_failures", "logins", "top_regions",
	)
	regions, _ := ov["top_regions"].([]any)
	if regions == nil || len(regions) != 1 {
		t.Fatalf("top_regions %v", regions)
	}
	assertKeys(t, regions[0].(map[string]any), "hits", "ips", "region")

	ips, _ := got["ips"].([]any)
	if len(ips) != 1 {
		t.Fatalf("ips len %d", len(ips))
	}
	ip0, _ := ips[0].(map[string]any)
	assertKeys(t, ip0,
		"accounts", "accounts_capped", "events", "fetches", "geo", "hits", "ip_masked",
		"last_seen", "network_kind", "token", "ua_kinds", "users",
	)
	if ip0["ip_masked"] != "198.51.100.*" || ip0["last_seen"] != "2026-10-02T08:30:00Z" {
		t.Fatalf("ip0 %v", ip0)
	}
	users, _ := ip0["users"].([]any)
	if len(users) != 1 {
		t.Fatalf("users len %d", len(users))
	}
	u0, _ := users[0].(map[string]any)
	assertKeys(t, u0, "email", "id", "status")
	if u0["id"] != traceUserID.String() {
		t.Fatalf("user id %v", u0["id"])
	}
}

func TestTraceSubscribersShape(t *testing.T) {
	f := &traceFake{}
	e, _ := opsEngine(f)
	w := do(e, http.MethodGet, "/v1/trace/subscribers?window=24h&sort=ips", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d %s", w.Code, w.Body)
	}
	if f.window != "24h" || f.sort != "ips" {
		t.Fatalf("入参 window=%q sort=%q", f.window, f.sort)
	}
	got := decodeMap(t, w.Body.Bytes())
	assertKeys(t, got, "since", "sort", "subscribers", "window")
	subs, _ := got["subscribers"].([]any)
	if len(subs) != 1 {
		t.Fatalf("subscribers len %d", len(subs))
	}
	s0, _ := subs[0].(map[string]any)
	assertKeys(t, s0,
		"email", "failures", "families", "fetches", "ips", "last_fetch", "regions",
		"status", "ua_kinds", "user_id",
	)
	families, ok := s0["families"].([]any)
	if !ok || families == nil || len(families) != 0 {
		t.Fatalf("families 应为 [] got %v", s0["families"])
	}
}

func TestTraceAccountsShape(t *testing.T) {
	f := &traceFake{}
	e, _ := opsEngine(f)
	w := do(e, http.MethodGet, "/v1/trace/accounts?window=30d&sort=logins", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d %s", w.Code, w.Body)
	}
	if f.window != "30d" || f.sort != "logins" {
		t.Fatalf("入参 window=%q sort=%q", f.window, f.sort)
	}
	got := decodeMap(t, w.Body.Bytes())
	assertKeys(t, got, "accounts", "since", "sort", "window")
	accounts, _ := got["accounts"].([]any)
	if len(accounts) != 1 {
		t.Fatalf("accounts len %d", len(accounts))
	}
	a0, _ := accounts[0].(map[string]any)
	assertKeys(t, a0,
		"datacenter_hits", "email", "fetch_ips", "last_seen", "linked_ips", "login_ips",
		"logins", "shared_ips", "status", "ua_kinds", "user_id",
	)
}

func TestTraceRevealPassesToken(t *testing.T) {
	f := &traceFake{}
	e, _ := opsEngine(f)
	w := do(e, http.MethodGet, "/v1/trace/reveal?token=abc", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d %s", w.Code, w.Body)
	}
	if f.token != "abc" {
		t.Fatalf("token %q", f.token)
	}
	got := decodeMap(t, w.Body.Bytes())
	assertKeys(t, got, "geo", "ip", "network_kind")
	if got["ip"] != "198.51.100.7" {
		t.Fatalf("ip %v", got["ip"])
	}

	f.revealErr = errs.Invalid(map[string]string{"token": "已失效，请刷新列表后再试"})
	w = do(e, http.MethodGet, "/v1/trace/reveal?token=abc", "")
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("失效令牌 status %d want 422", w.Code)
	}
	if errFields(t, w.Body.Bytes())["token"] == "" {
		t.Fatal("fields 缺 token")
	}
}
