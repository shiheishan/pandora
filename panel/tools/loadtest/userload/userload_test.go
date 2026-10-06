// [INPUT]: 依赖 fakegw_test.go 的 fakePanel / testManifest，依赖 domain/subscription 的 DetectFormat 核对 UA 表
// [OUTPUT]: users 子命令的单测：UA 分支、固定来源 IP、登录一次复用令牌、开环速率、端点名规范化、429 分来源、-strict 判定、限流预估
// [POS]: tools/loadtest/userload 的 users 测试，不连库，全部打 httptest 假网关；burst 的测试在 burst_test.go

package userload

import (
	"context"
	"io"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/domain/subscription"
	"github.com/aegispanel/aegis/tools/loadtest/ltkit"
)

// 每条 UA 都必须落在它声明的 DetectFormat 分支上，三个分支都要有人打。
func TestSubscriptionUAsHitEveryFormatBranch(t *testing.T) {
	seen := map[string]bool{}
	for _, p := range subscriptionUAs {
		if got := string(subscription.DetectFormat(p.ua, "")); got != p.format {
			t.Errorf("%s: DetectFormat=%s, table says %s", p.ua, got, p.format)
		}
		seen[p.format] = true
	}
	for _, f := range []subscription.Format{subscription.FormatClash, subscription.FormatSingbox, subscription.FormatURI} {
		if !seen[string(f)] {
			t.Errorf("no UA reaches the %s branch", f)
		}
	}
	// 客户端分配对同一个用户恒定
	if uaForUser(42) != uaForUser(42) {
		t.Fatal("uaForUser is not stable")
	}
}

func baseConfig(f *fakePanel, out string) usersConfig {
	return usersConfig{
		publicURL: f.pub.URL, adminURL: f.adm.URL,
		admin:    credentials{fakeAdmin, fakeAdminPW},
		adminIPs: []string{fakeAdminIP},
		subRate:  30, portalRate: 30, adminRate: 10, loginRate: 2, warmupLoginRate: 200,
		portalUsers: 10, duration: 1500 * time.Millisecond, timeout: 5 * time.Second,
		window: time.Second, maxInflight: 256, out: out, ipHeaders: []string{"X-Real-IP"},
		limits: panelLimits{120, 300, 10, 60, 240},
	}
}

func endpointCount(rep ltkit.Report, prefix string) uint64 {
	var n uint64
	for _, e := range rep.Endpoints {
		if strings.HasPrefix(e.Endpoint, prefix) {
			n += e.Count
		}
	}
	return n
}

func TestUsersRunAgainstFakeGateway(t *testing.T) {
	m := testManifest(30)
	f := newFakePanel(t, m)
	out := t.TempDir()
	rep, err := runUsers(context.Background(), baseConfig(f, out), m, io.Discard)
	if err != nil {
		t.Fatal(err)
	}

	// 固定来源 IP：假网关逐个请求核对过；缺省只带 X-Real-IP
	if r := f.requests("public", "/v1/me"); len(r) == 0 || r[0].cf != "" {
		t.Fatalf("unexpected CF-Connecting-IP on %+v", r)
	}
	f.mu.Lock()
	violations, logins := append([]string(nil), f.violations...), maps.Clone(f.logins)
	f.mu.Unlock()
	if len(violations) > 0 {
		t.Fatalf("X-Real-IP mismatches: %v", violations[:min(5, len(violations))])
	}

	// 登录一次、复用令牌：预热每人一次，运行期只有 -login-rate 的重新登录
	totalLogins := 0
	for i, u := range m.Users {
		n := logins[u.Email]
		totalLogins += n
		inPool := i%3 == 0 // 30 人里等间距挑 10 个
		if inPool && n < 1 {
			t.Errorf("pool user %s never logged in", u.Email)
		}
		if !inPool && n > 0 {
			t.Errorf("non-pool user %s logged in %d times", u.Email, n)
		}
	}
	portal := len(f.requests("public", "/v1/me")) + len(f.requests("public", "/v1/orders")) + len(f.requests("public", "/v1/plans"))
	if totalLogins > 10+6 || portal < 30 {
		t.Fatalf("logins %d, portal reads %d: tokens are not being reused", totalLogins, portal)
	}

	// 开环速率：1.5 秒 × 30/s ≈ 45
	for _, c := range []struct {
		prefix string
		want   float64
	}{{"public:GET /{prefix}/{token}", 45}, {"admin:GET ", 15}} {
		got := float64(endpointCount(rep, c.prefix))
		if got < c.want*0.75 || got > c.want*1.25 {
			t.Errorf("%s: %v requests, want about %v", c.prefix, got, c.want)
		}
	}

	// 端点名规范化：只有模板，没有令牌、邮箱、id、前缀
	nameRE := regexp.MustCompile(`^(public|admin):(GET|POST) /`)
	for _, e := range rep.Endpoints {
		if !nameRE.MatchString(e.Endpoint) {
			t.Errorf("odd endpoint name %q", e.Endpoint)
		}
		for _, leak := range []string{fakePrefix, "subtoken-", "@loadtest.invalid", "00000000-0000-4000", "sub-0000"} {
			if strings.Contains(e.Endpoint, leak) {
				t.Errorf("endpoint %q leaks %q", e.Endpoint, leak)
			}
		}
		if e.Flags["format_mismatch"] > 0 || e.Flags["decoy_404"] > 0 {
			t.Errorf("%s: flags %v", e.Endpoint, e.Flags)
		}
	}
	for _, want := range []string{"[clash]", "[singbox]", "[uri]"} {
		if endpointCount(rep, "public:GET /{prefix}/{token} "+want) == 0 {
			t.Errorf("no subscription pull for %s", want)
		}
	}
	for _, name := range []string{"users.json", "users.txt", "users-warmup.json"} {
		if _, err := os.Stat(filepath.Join(out, name)); err != nil {
			t.Error(err)
		}
	}
}

// 面板很慢时开环调度仍按点发：发出的请求数由速率决定，不由响应快慢决定。
func TestOpenLoopKeepsRateWhenServerIsSlow(t *testing.T) {
	var ticks atomic.Int64
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	openLoop(ctx, 50, func() { ticks.Add(1) })
	if n := ticks.Load(); n < 45 || n > 52 {
		t.Fatalf("openLoop fired %d ticks in 1s at 50/s", n)
	}

	m := testManifest(20)
	f := newFakePanel(t, m)
	f.set(func() { f.slow = 400 * time.Millisecond })
	cfg := baseConfig(f, t.TempDir())
	cfg.adminRate, cfg.loginRate, cfg.subRate, cfg.portalRate = 0, 0, 40, 0
	cfg.duration = time.Second
	rep, err := runUsers(context.Background(), cfg, m, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	// 闭环的话 1 秒里每条连接只能跑 2 个来回；开环应当发满约 40 个
	if n := endpointCount(rep, "public:GET /{prefix}/{token}"); n < 34 {
		t.Fatalf("only %d pulls at 40/s against a 400ms server: the loop is waiting for responses", n)
	}
	if rep.Totals.P50MS < 350 {
		t.Fatalf("p50 %.0fms does not show the server delay", rep.Totals.P50MS)
	}
}

func TestStrictFailsOn5xx(t *testing.T) {
	m := testManifest(10)
	f := newFakePanel(t, m)
	f.set(func() { f.fail500Portal = true })
	cfg := baseConfig(f, t.TempDir())
	cfg.duration = 500 * time.Millisecond
	if _, err := runUsers(context.Background(), cfg, m, io.Discard); err != nil {
		t.Fatalf("without -strict 5xx must not fail the run: %v", err)
	}
	cfg.strict = true
	_, err := runUsers(context.Background(), cfg, m, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "5xx") {
		t.Fatalf("strict run returned %v, want a 5xx failure", err)
	}

	f.set(func() { f.fail500Portal = false })
	if _, err := runUsers(context.Background(), cfg, m, io.Discard); err != nil {
		t.Fatalf("clean strict run failed: %v", err)
	}
}

// 中间件限流（JSON rate_limited）与订阅每凭据每小时上限（纯文本）分开打标。
func TestRateLimitFlagsBySource(t *testing.T) {
	m := testManifest(10)
	f := newFakePanel(t, m)
	f.set(func() { f.rlPortal, f.rlSub = true, true })
	cfg := baseConfig(f, t.TempDir())
	cfg.adminRate, cfg.loginRate = 0, 0
	cfg.duration = 500 * time.Millisecond
	rep, err := runUsers(context.Background(), cfg, m, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Totals.Flags["rl_mw"] == 0 || rep.Totals.Flags["rl_sub_hourly"] == 0 || rep.Totals.Codes["429"] == 0 {
		t.Fatalf("flags %v codes %v", rep.Totals.Flags, rep.Totals.Codes)
	}
	for _, e := range rep.Endpoints {
		if strings.HasPrefix(e.Endpoint, "public:GET /{prefix}") && e.Flags["rl_mw"] > 0 {
			t.Fatalf("%s: subscription 429 tagged as middleware", e.Endpoint)
		}
	}
}

func TestPreflightWarnsWhenUsersShareASlash24(t *testing.T) {
	m := testManifest(300)
	for i := range m.Users {
		m.Users[i].RealIP = "10.9.9." + strconv.Itoa(i%250+1)
	}
	users, err := actorsOf(m)
	if err != nil {
		t.Fatal(err)
	}
	cfg := usersConfig{subRate: 50, portalRate: 50, loginRate: 1, warmupLoginRate: 10, duration: time.Hour,
		limits: panelLimits{120, 300, 10, 60, 240}}
	_, warns := preflight(cfg, users, spreadPool(users, 200))
	joined := strings.Join(warns, "\n")
	for _, want := range []string{"pub_net", "auth_net", "sub "} {
		if !strings.Contains(joined, want) {
			t.Errorf("no %s warning in:\n%s", want, joined)
		}
	}

	spread := testManifest(300)
	users, _ = actorsOf(spread)
	cfg = usersConfig{subRate: 2, portalRate: 2, loginRate: 0.05, warmupLoginRate: 10, duration: time.Minute,
		limits: panelLimits{120, 300, 10, 60, 240}}
	if _, warns := preflight(cfg, users, spreadPool(users, 200)); len(warns) > 0 {
		t.Fatalf("CI-sized run should not warn: %v", warns)
	}
}

func TestParseUsersFlagsTakesAdminPasswordFromEnv(t *testing.T) {
	t.Setenv(envAdminEmail, fakeAdmin)
	t.Setenv(envAdminPassword, fakeAdminPW)
	cfg, err := parseUsersFlags([]string{"-manifest", "m.json", "-public-url", "http://x", "-admin-url", "http://y/secret"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.admin.email != fakeAdmin || cfg.admin.password != fakeAdminPW || cfg.adminIPs[0] != defaultAdminIP {
		t.Fatalf("got %+v", cfg.admin)
	}
	if _, err := parseUsersFlags([]string{"-public-url", "http://x"}); err == nil {
		t.Fatal("missing -manifest accepted")
	}
}

func TestPrefixFromLinks(t *testing.T) {
	body := []byte(`{"links":[{"url":"https://panel.example.invalid/base/abcdef012345/tok123"}]}`)
	if got := prefixFromLinks(body); got != "abcdef012345" {
		t.Fatalf("prefix %q", got)
	}
	if got := netKey("10.1.2.3"); got != "10.1.2.0/24" {
		t.Fatalf("netKey %q", got)
	}
}
