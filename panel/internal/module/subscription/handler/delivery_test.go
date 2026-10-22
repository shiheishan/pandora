package handler

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"

	"github.com/aegispanel/aegis/internal/module/subscription/service"
	"github.com/aegispanel/aegis/internal/pkg/authn"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/httpx"
	"github.com/aegispanel/aegis/internal/pkg/ratelimit"
	"github.com/aegispanel/aegis/internal/pkg/route"
)

// deliverySvc 是订阅分发接口的替身：记下入参、返回预设值；其余方法走嵌入的 nil 接口，被调到会 panic。
type deliverySvc struct {
	Service
	pullRes     service.PullResult
	pullErr     error
	pullReq     service.PullRequest
	links       []service.Link
	rotateURL   string
	rotateID    uuid.UUID
	nodes       []service.NodePreview
	usage       service.DailyUsage
	usageDays   int
	adminReason string
	adminOut    service.AdminRotateResult
}

func (f *deliverySvc) Pull(_ context.Context, req service.PullRequest) (service.PullResult, error) {
	f.pullReq = req
	return f.pullRes, f.pullErr
}

func (f *deliverySvc) MyLinks(context.Context) ([]service.Link, error) { return f.links, nil }

func (f *deliverySvc) MyRotate(_ context.Context, id uuid.UUID) (string, error) {
	f.rotateID = id
	return f.rotateURL, nil
}

func (f *deliverySvc) MyNodes(context.Context, uuid.UUID) ([]service.NodePreview, error) {
	return f.nodes, nil
}

func (f *deliverySvc) MyUsage(_ context.Context, _ uuid.UUID, days int) (service.DailyUsage, error) {
	f.usageDays = days
	return f.usage, nil
}

func (f *deliverySvc) AdminRotate(_ context.Context, _ uuid.UUID, reason string) (service.AdminRotateResult, error) {
	f.adminReason = reason
	return f.adminOut, nil
}

// rootEngine 建只挂订阅拉取根路由的引擎。
func rootEngine(svc Service) *gin.Engine {
	gin.SetMode(gin.TestMode)
	e := gin.New()
	e.Use(httpx.RequestID(zap.NewNop()), httpx.Errors())
	New(svc, nil, 0).RegisterRoot(e)
	return e
}

// keysOf 返回对象的键（排序后）。
func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// sameKeys 断言对象恰好有这些键。
func sameKeys(t *testing.T, m map[string]any, want ...string) {
	t.Helper()
	sort.Strings(want)
	if got := keysOf(m); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("键 %v，想要 %v", got, want)
	}
}

func TestSubscribeOKHeaders(t *testing.T) {
	f := &deliverySvc{pullRes: service.PullResult{
		Body:                []byte("proxies: []\n"),
		ContentType:         "text/yaml; charset=utf-8",
		Userinfo:            service.Userinfo{Upload: 0, Download: 10, Total: 100, Expire: 1700000000},
		UpdateIntervalHours: 12,
		ContentDisposition:  "attachment; filename=x",
	}}
	w := do(rootEngine(f), http.MethodGet, "/a1b2c3d4e5f6/tok.yaml?flag=clash", "")
	if w.Code != http.StatusOK || w.Body.String() != "proxies: []\n" {
		t.Fatalf("%d body=%q", w.Code, w.Body.String())
	}
	h := w.Header()
	checks := map[string]string{
		"Content-Type":            "text/yaml; charset=utf-8",
		"Subscription-Userinfo":   "upload=0; download=10; total=100; expire=1700000000",
		"Profile-Update-Interval": "12",
		"Cache-Control":           "no-store, no-cache, must-revalidate",
		"Content-Disposition":     "attachment; filename=x",
		"X-Robots-Tag":            "noindex, nofollow",
	}
	for k, v := range checks {
		if h.Get(k) != v {
			t.Fatalf("%s = %q，想要 %q", k, h.Get(k), v)
		}
	}
	if h.Get("Profile-Web-Page-Url") != "" {
		t.Fatalf("生效中不该带续费页: %q", h.Get("Profile-Web-Page-Url"))
	}
	if f.pullReq != (service.PullRequest{Prefix: "a1b2c3d4e5f6", Token: "tok.yaml", Target: "clash", Host: "example.com"}) {
		t.Fatalf("pullReq %+v", f.pullReq)
	}
}

func TestSubscribeExpiredHeaders(t *testing.T) {
	f := &deliverySvc{pullRes: service.PullResult{
		Body:                []byte("x"),
		ContentType:         "text/plain; charset=utf-8",
		Userinfo:            service.Userinfo{Download: 1, Total: 2},
		UpdateIntervalHours: 1,
		Expired:             true,
		WebPageURL:          "https://panel.example.test/#/checkout?renew=x",
	}}
	w := do(rootEngine(f), http.MethodGet, "/a1b2c3d4e5f6/tok", "")
	if w.Code != http.StatusOK {
		t.Fatalf("%d", w.Code)
	}
	h := w.Header()
	if h.Get("Profile-Web-Page-Url") != "https://panel.example.test/#/checkout?renew=x" || h.Get("Profile-Update-Interval") != "1" {
		t.Fatalf("头 %v", h)
	}
	if strings.Contains(h.Get("Subscription-Userinfo"), "expire=") {
		t.Fatalf("Expire 为 0 不该输出: %q", h.Get("Subscription-Userinfo"))
	}
}

func TestSubscribeDecoy(t *testing.T) {
	for name, err := range map[string]error{"拒绝": service.ErrPullDenied, "内部错误": errors.New("boom")} {
		t.Run(name, func(t *testing.T) {
			w := do(rootEngine(&deliverySvc{pullErr: err}), http.MethodGet, "/a1b2c3d4e5f6/tok", "")
			if w.Code != http.StatusNotFound || w.Body.String() != decoyPage {
				t.Fatalf("%d body=%q", w.Code, w.Body.String())
			}
			h := w.Header()
			if h.Get("Content-Type") != "text/html; charset=utf-8" {
				t.Fatalf("Content-Type %q", h.Get("Content-Type"))
			}
			if h.Get("X-Robots-Tag") != "noindex, nofollow" {
				t.Fatalf("X-Robots-Tag = %q", h.Get("X-Robots-Tag"))
			}
			for _, k := range []string{"Subscription-Userinfo", "Cache-Control", "Content-Disposition"} {
				if h.Get(k) != "" {
					t.Fatalf("伪装页不该带 %s", k)
				}
			}
		})
	}
}

func TestSubscribeRateLimited(t *testing.T) {
	f := &deliverySvc{pullErr: &errs.Error{Code: errs.RateLimited, Message: "x", RetryAfterSeconds: 120}}
	w := do(rootEngine(f), http.MethodGet, "/a1b2c3d4e5f6/tok", "")
	if w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") != "120" || w.Body.String() != "too many requests\n" {
		t.Fatalf("%d %q body=%q", w.Code, w.Header().Get("Retry-After"), w.Body.String())
	}
}

func TestSubscribeUnavailable(t *testing.T) {
	w := do(rootEngine(&deliverySvc{pullErr: service.ErrDeliveryUnavailable}), http.MethodGet, "/a1b2c3d4e5f6/tok", "")
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("%d body=%s", w.Code, w.Body.String())
	}
	if code := decMap(t, w)["error"].(map[string]any)["code"]; code != "service_unavailable" {
		t.Fatalf("code %v", code)
	}
}

func TestSubscriptionLinksShape(t *testing.T) {
	at := time.Date(2026, 10, 1, 8, 0, 0, 0, time.FixedZone("x", 8*3600))
	f := &deliverySvc{links: []service.Link{
		{SubscriptionID: uuid.New(), URL: "https://panel.example.test/p/t1", FetchCount: 3, LastFetchedAt: &at, DistinctSources24h: 2},
		{SubscriptionID: uuid.New(), URL: "https://panel.example.test/p/t2", Expired: true},
	}}
	e := newEngine(f, false)
	w := do(e, http.MethodGet, "/v1/me/subscription-links", "")
	if w.Code != http.StatusOK {
		t.Fatalf("%d", w.Code)
	}
	links := decMap(t, w)["links"].([]any)
	if len(links) != 2 {
		t.Fatalf("links %v", links)
	}
	first := links[0].(map[string]any)
	sameKeys(t, first, "subscription_id", "url", "fetch_count", "last_fetched_at", "distinct_sources_24h", "expired")
	if first["last_fetched_at"] != "2026-10-01T00:00:00Z" {
		t.Fatalf("first %v", first)
	}
	if second := links[1].(map[string]any); second["last_fetched_at"] != nil || second["expired"] != true {
		t.Fatalf("second %v", second)
	}
	f.links = nil
	if w := do(e, http.MethodGet, "/v1/me/subscription-links", ""); !strings.Contains(w.Body.String(), `"links":[]`) {
		t.Fatalf("空列表: %s", w.Body.String())
	}
}

func TestRotateLink(t *testing.T) {
	f := &deliverySvc{rotateURL: "https://panel.example.test/p/new"}
	e := newEngine(f, false)
	if w := do(e, http.MethodPost, "/v1/me/subscriptions/not-a-uuid/rotate", ""); w.Code != http.StatusNotFound {
		t.Fatalf("坏 id: %d", w.Code)
	}
	id := uuid.New()
	w := do(e, http.MethodPost, "/v1/me/subscriptions/"+id.String()+"/rotate", "")
	if w.Code != http.StatusOK || f.rotateID != id {
		t.Fatalf("%d %v", w.Code, f.rotateID)
	}
	got := decMap(t, w)
	sameKeys(t, got, "url")
	if got["url"] != "https://panel.example.test/p/new" {
		t.Fatalf("%v", got)
	}
}

func TestSubscriptionNodes(t *testing.T) {
	f := &deliverySvc{nodes: []service.NodePreview{
		{Name: "香港 01", Protocol: "vless", TrafficRate: 1},
		{Name: "日本 01", Protocol: "trojan", TrafficRate: 1.5},
	}}
	e := newEngine(f, false)
	path := "/v1/me/subscriptions/" + uuid.NewString() + "/nodes"
	w := do(e, http.MethodGet, path, "")
	if w.Code != http.StatusOK || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("%d %q", w.Code, w.Header().Get("Cache-Control"))
	}
	got := decMap(t, w)
	if got["count"] != float64(2) {
		t.Fatalf("count %v", got["count"])
	}
	sameKeys(t, got["nodes"].([]any)[0].(map[string]any), "name", "protocol", "traffic_rate")
	f.nodes = nil
	if w := do(e, http.MethodGet, path, ""); !strings.Contains(w.Body.String(), `"nodes":[]`) {
		t.Fatalf("空列表: %s", w.Body.String())
	}
}

func TestSubscriptionUsage(t *testing.T) {
	f := &deliverySvc{usage: service.DailyUsage{
		Timezone:    "Asia/Shanghai",
		PeriodStart: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC),
		Days:        []service.UsageDay{{Date: "2026-10-12", Bytes: 5}},
		TodayBytes:  5, AvgDailyBytes: 5,
	}}
	e := newEngine(f, false)
	base := "/v1/me/subscriptions/" + uuid.NewString() + "/usage"
	for _, q := range []string{"?days=0", "?days=94", "?days=abc"} {
		w := do(e, http.MethodGet, base+q, "")
		if w.Code != http.StatusUnprocessableEntity {
			t.Fatalf("%s: %d", q, w.Code)
		}
		fields, _ := decMap(t, w)["error"].(map[string]any)["fields"].(map[string]any)
		if _, ok := fields["days"]; !ok {
			t.Fatalf("%s: 没有 fields.days: %s", q, w.Body.String())
		}
	}
	w := do(e, http.MethodGet, base, "")
	if w.Code != http.StatusOK || f.usageDays != 0 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("%d days=%d", w.Code, f.usageDays)
	}
	got := decMap(t, w)
	sameKeys(t, got, "timezone", "period_start", "period_end", "days", "today_bytes", "avg_daily_bytes")
	if got["period_end"] != nil || got["timezone"] != "Asia/Shanghai" {
		t.Fatalf("%v", got)
	}
	if w := do(e, http.MethodGet, base+"?days=7", ""); w.Code != http.StatusOK || f.usageDays != 7 {
		t.Fatalf("days=7: %d %d", w.Code, f.usageDays)
	}
	f.usage.Days = nil
	if w := do(e, http.MethodGet, base, ""); !strings.Contains(w.Body.String(), `"days":[]`) {
		t.Fatalf("空列表: %s", w.Body.String())
	}
}

func TestAdminRotate(t *testing.T) {
	f := &deliverySvc{adminOut: service.AdminRotateResult{UserEmail: "user@example.test"}}
	e := newEngine(f, true)
	path := "/v1/subscriptions/" + uuid.NewString() + "/rotate"
	if w := do(e, http.MethodPost, path, `{"reason":"太短"}`); w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("原因太短: %d", w.Code)
	}
	w := do(e, http.MethodPost, path, `{"reason":"  用户说链接泄露了  "}`)
	if w.Code != http.StatusOK || f.adminReason != "用户说链接泄露了" {
		t.Fatalf("%d reason=%q", w.Code, f.adminReason)
	}
	got := decMap(t, w)
	sameKeys(t, got, "old_revoked", "user_email")
	if got["old_revoked"] != true || got["user_email"] != "user@example.test" {
		t.Fatalf("%v", got)
	}
	if w := do(e, http.MethodPost, "/v1/subscriptions/not-a-uuid/rotate", `{"reason":"用户说链接泄露了"}`); w.Code != http.StatusNotFound {
		t.Fatalf("坏 id: %d", w.Code)
	}
}

// 门户换链接限频：同一份第二次被 10 分钟间隔拦下，文案写明分钟数；另一份不受影响。
func TestRotateLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	user := uuid.New()
	e := gin.New()
	e.Use(httpx.RequestID(zap.NewNop()), httpx.Errors())
	pass := func(c *gin.Context) { c.Next() }
	kit := route.Kit{
		RequireAuth:  func(c *gin.Context) { authn.Set(c, authn.Principal{UserID: user}); c.Next() },
		RecentReauth: pass,
		Idempotency:  func(string) gin.HandlerFunc { return pass },
		Switch:       func(string) gin.HandlerFunc { return pass },
	}
	f := &deliverySvc{rotateURL: "https://panel.example.test/p/new"}
	New(f, ratelimit.New(rdb), 60).RegisterPublic(e.Group("/v1"), kit)

	sub := "/v1/me/subscriptions/" + uuid.NewString() + "/rotate"
	if w := do(e, http.MethodPost, sub, ""); w.Code != http.StatusOK {
		t.Fatalf("第一次: %d body=%s", w.Code, w.Body.String())
	}
	w := do(e, http.MethodPost, sub, "")
	if w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") == "" {
		t.Fatalf("第二次: %d body=%s", w.Code, w.Body.String())
	}
	msg, _ := decMap(t, w)["error"].(map[string]any)["message"].(string)
	if msg != "操作太频繁，请 10 分钟后再试" {
		t.Fatalf("文案 %q", msg)
	}
	if w := do(e, http.MethodPost, "/v1/me/subscriptions/"+uuid.NewString()+"/rotate", ""); w.Code != http.StatusOK {
		t.Fatalf("另一份: %d", w.Code)
	}
	// 同一份换个 id 写法（大写、带花括号）仍是同一个限频键
	id := uuid.New()
	if w := do(e, http.MethodPost, "/v1/me/subscriptions/"+id.String()+"/rotate", ""); w.Code != http.StatusOK {
		t.Fatalf("第三份第一次: %d", w.Code)
	}
	for _, alias := range []string{strings.ToUpper(id.String()), "{" + id.String() + "}"} {
		if w := do(e, http.MethodPost, "/v1/me/subscriptions/"+alias+"/rotate", ""); w.Code != http.StatusTooManyRequests {
			t.Fatalf("写法 %s 应被同一个间隔拦下: %d", alias, w.Code)
		}
	}
	// 坏 id 直接 404，不进限流
	if w := do(e, http.MethodPost, "/v1/me/subscriptions/not-a-uuid/rotate", ""); w.Code != http.StatusNotFound {
		t.Fatalf("坏 id: %d", w.Code)
	}
}

// 认证之后的 Unavailable（读库、取线路）回伪装页；只有「分发没装配」回 503。
func TestSubscribeUnavailableOnlyWhenUnwired(t *testing.T) {
	f := &deliverySvc{pullErr: errs.New(errs.Unavailable)}
	w := do(rootEngine(f), http.MethodGet, "/a1b2c3d4e5f6/0123456789abcdef", "")
	if w.Code != http.StatusNotFound || w.Body.String() != decoyPage {
		t.Fatalf("其它 Unavailable 应回伪装页: %d %q", w.Code, w.Body.String())
	}
	f.pullErr = service.ErrDeliveryUnavailable
	if w := do(rootEngine(f), http.MethodGet, "/a1b2c3d4e5f6/0123456789abcdef", ""); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("没装配应回 503: %d", w.Code)
	}
}

// 拉取按来源 IP 限流：超过每分钟上限回 429。
func TestSubscribeIPLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	e := gin.New()
	e.Use(httpx.RequestID(zap.NewNop()), httpx.Errors())
	f := &deliverySvc{pullErr: service.ErrPullDenied}
	New(f, ratelimit.New(rdb), 2).RegisterRoot(e)
	for i := 0; i < 2; i++ {
		if w := do(e, http.MethodGet, "/a1b2c3d4e5f6/0123456789abcdef", ""); w.Code != http.StatusNotFound {
			t.Fatalf("第 %d 次: %d", i+1, w.Code)
		}
	}
	if w := do(e, http.MethodGet, "/a1b2c3d4e5f6/0123456789abcdef", ""); w.Code != http.StatusTooManyRequests {
		t.Fatalf("第 3 次应被限流: %d", w.Code)
	}
}
