// [INPUT]: 依赖 fakegw_test.go 的 fakePanel / testManifest，依赖 userload_test.go 的 baseConfig / endpointCount，依赖 ltkit.Manifest 的 SubscribePathPrefix 与 ManifestUser.SubscriptionID
// [OUTPUT]: 单测：manifest 新增字段（subscribe_path_prefix、subscription_id）直接生效、不再经门户探测；按 seed 的地址规划（198.18.0.0/15 的 512 个 /24 轮流分配）四档缺省速率都不撞限流
// [POS]: tools/loadtest/userload 中 userload.go 的清单读取与 preflight.go 对齐 seed 实际输出的测试
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package userload

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// seed 的分配：用户轮流落到 198.18.0.0/15 的 512 个 /24。按建议速率（每人每 10 分钟拉一次订阅、
// 门户 10/s）与缺省重新登录速率，任何一维都不该告警。
func TestPreflightWithSeedAddressPlan(t *testing.T) {
	for _, n := range []int{200, 5000, 10000, 15000} {
		m := testManifest(n)
		for i := range m.Users {
			k := i % 512
			m.Users[i].RealIP = fmt.Sprintf("198.%d.%d.%d", 18+k/256, k%256, i/512%254+1)
		}
		users, err := actorsOf(m)
		if err != nil {
			t.Fatal(err)
		}
		cfg := usersConfig{subRate: float64(n) / 600, portalRate: 10, adminRate: 0.5, loginRate: 0.05,
			warmupLoginRate: 10, duration: time.Hour, adminIPs: []string{defaultAdminIP},
			limits: panelLimits{120, 300, 10, 60, 240}}
		lines, warns := preflight(cfg, users, spreadPool(users, min(200, n)))
		if len(warns) > 0 {
			t.Errorf("%d users: %v", n, warns)
		}
		if n == 15000 {
			t.Log("\n" + strings.Join(lines, "\n"))
		}
	}
}

// manifest 带 subscribe_path_prefix 与 subscription_id 时直接用，不再经门户探测。
func TestUsersReadsPrefixAndSubscriptionIDsFromManifest(t *testing.T) {
	m := testManifest(12)
	f := newFakePanel(t, m)
	m.SubscribePathPrefix = fakePrefix
	for i := range m.Users {
		m.Users[i].SubscriptionID = "manifest-sub"
	}

	cfg := baseConfig(f, t.TempDir())
	cfg.adminRate, cfg.loginRate, cfg.portalUsers = 0, 0, 4
	cfg.duration = time.Second
	rep, err := runUsers(context.Background(), cfg, m, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	// 预热段只该有登录：前缀与订阅 id 都来自 manifest（运行段的页面读取照常会打这两个接口）
	warm, err := os.ReadFile(filepath.Join(cfg.out, "users-warmup.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, probe := range []string{"/v1/me/subscription-links", "/v1/me/subscriptions"} {
		if strings.Contains(string(warm), probe) {
			t.Errorf("warm-up called %s although the manifest has the value", probe)
		}
	}
	if endpointCount(rep, "public:GET /{prefix}/{token}") == 0 || rep.Totals.Flags["decoy_404"] > 0 {
		t.Fatalf("subscription pulls with the manifest prefix failed: %v", rep.Totals.Flags)
	}
	for _, r := range f.requests("public", "/v1/me/subscriptions/") {
		if !strings.Contains(r.path, "/manifest-sub/") {
			t.Fatalf("subscription id not from the manifest: %s", r.path)
		}
	}
}
