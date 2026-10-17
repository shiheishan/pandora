package service

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	settingssvc "github.com/aegispanel/aegis/internal/module/settings/service"
	"github.com/aegispanel/aegis/internal/module/subscription/render"
	"github.com/aegispanel/aegis/internal/pkg/testkit"
)

// wiringExtras 实现 ExtrasSettings：固定返回一份附加设置。
type wiringExtras struct {
	v settingssvc.SubscriptionExtras
}

func (f *wiringExtras) SubscriptionExtras(context.Context) (settingssvc.SubscriptionExtras, error) {
	return f.v, nil
}

// useExtras 在已装配的分发依赖上换一份附加设置（快照跟着清掉）。
func (de *deliveryEnv) useExtras(v settingssvc.SubscriptionExtras) {
	d := *de.svc.delivery
	d.Extras = &wiringExtras{v: v}
	de.svc.UseDelivery(d)
	de.svc.extras.snap.Store(nil)
}

// 两条线路：vless 与 hysteria2，名字带不同关键字，验 types / filter。
func wiringNodes() []render.Node {
	return []render.Node{
		{Name: "香港 01", Type: "vless", Host: "hk.example.test", Port: 443, Config: map[string]any{}, TrafficRate: 1},
		{Name: "东京 02", Type: "hysteria2", Host: "jp.example.test", Port: 8443, Config: map[string]any{}, TrafficRate: 1},
	}
}

func TestPullTypesAndFilter(t *testing.T) {
	de, ctx, _, _, token := setupPull(t)
	de.nodes.nodes = wiringNodes()
	uri := pullCtx(ctx, "198.51.100.7", "v2rayN/6.0")

	res, err := de.svc.Pull(uri, PullRequest{Prefix: testPrefix, Token: token, Types: "hy2"})
	if err != nil {
		t.Fatal(err)
	}
	body := wiringURIBody(t, res.Body)
	if !strings.Contains(body, "hysteria2://") || strings.Contains(body, "vless://") {
		t.Fatalf("types=hy2 只该留 hysteria2：%s", body)
	}

	res, err = de.svc.Pull(uri, PullRequest{Prefix: testPrefix, Token: token, Filter: "香港"})
	if err != nil {
		t.Fatal(err)
	}
	body = wiringURIBody(t, res.Body)
	if !strings.Contains(body, "vless://") || strings.Contains(body, "hysteria2://") {
		t.Fatalf("filter=香港 只该留香港：%s", body)
	}
}

func TestPullInfoNodesAndPrefix(t *testing.T) {
	de, ctx, _, _, token := setupPull(t)
	de.useExtras(settingssvc.SubscriptionExtras{InfoNodes: true, ProtocolPrefix: true})
	res, err := de.svc.Pull(ctx, PullRequest{Prefix: testPrefix, Token: token})
	if err != nil {
		t.Fatal(err)
	}
	body := string(res.Body)
	if !strings.Contains(body, "剩余流量") || !strings.Contains(body, "套餐到期") {
		t.Fatalf("开了信息节点却没有：%s", body)
	}
	// 前缀与名字之间不加空格，照 Xboard 的 getPrefixedServerName（$prefix . $name）
	if !strings.Contains(body, `"[vless]香港 01"`) {
		t.Fatalf("开了协议前缀却没有：%s", body)
	}
}

func TestPullUsesCustomTemplate(t *testing.T) {
	de, ctx, _, _, token := setupPull(t)
	custom := strings.Replace(render.DefaultTemplate(render.FormatClash, render.Options{}), "mixed-port: 7890", "mixed-port: 7891", 1)
	if err := de.svc.repo.UpsertTemplate(ctx, string(render.FormatClash), custom, de.svc.clock()); err != nil {
		t.Fatal(err)
	}
	de.svc.extras.snap.Store(nil)
	res, err := de.svc.Pull(ctx, PullRequest{Prefix: testPrefix, Token: token})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(res.Body), "mixed-port: 7891") {
		t.Fatalf("没用上自定义模板：%s", res.Body)
	}
}

func TestLinksUseDomainGroups(t *testing.T) {
	de, ctx, user, sub := rotateSetup(t)
	admin := adminCtx(ctx, uuid.New())
	G1 := seedUserGroup(t, de.env, ctx, "vip")
	G2 := seedUserGroup(t, de.env, ctx, "other")
	_, err := de.svc.AdminSaveDomainConfig(admin, []DomainGroupInput{
		{Name: "默认", IsDefault: true, Domains: []Domain{
			{BaseURL: "https://sub-a.example.test", Weight: 1},
			{BaseURL: "https://sub-b.example.test", Weight: 1},
		}},
		{Name: "专线", Domains: []Domain{{BaseURL: "https://vip.example.test", Weight: 1}}, UserGroupIDs: []uuid.UUID{G1}},
	})
	if err != nil {
		t.Fatal(err)
	}
	de.svc.extras.snap.Store(nil)

	links, err := de.svc.MyLinks(userCtx(ctx, user))
	if err != nil || len(links) != 1 {
		t.Fatalf("链接列表: %v %d", err, len(links))
	}
	u := links[0].URL
	if !strings.HasPrefix(u, "https://sub-a.example.test/"+testPrefix+"/") && !strings.HasPrefix(u, "https://sub-b.example.test/"+testPrefix+"/") {
		t.Fatalf("缺省组链接: %s", u)
	}

	de.users.setGroup(user, &G1)
	de.svc.extras.snap.Store(nil)
	links, err = de.svc.MyLinks(userCtx(ctx, user))
	if err != nil || !strings.HasPrefix(links[0].URL, "https://vip.example.test/") {
		t.Fatalf("G1 链接: %v", links)
	}
	url, err := de.svc.MyRotate(userCtx(ctx, user), sub.ID)
	if err != nil || !strings.HasPrefix(url, "https://vip.example.test/") {
		t.Fatalf("G1 换链: %q err %v", url, err)
	}

	de.users.setGroup(user, &G2)
	de.svc.extras.snap.Store(nil)
	links, err = de.svc.MyLinks(userCtx(ctx, user))
	if err != nil {
		t.Fatal(err)
	}
	u = links[0].URL
	if !strings.HasPrefix(u, "https://sub-a.example.test/") && !strings.HasPrefix(u, "https://sub-b.example.test/") {
		t.Fatalf("G2 走缺省: %s", u)
	}

	_, err = de.svc.AdminSaveDomainConfig(admin, []DomainGroupInput{})
	if err != nil {
		t.Fatal(err)
	}
	de.svc.extras.snap.Store(nil)
	links, err = de.svc.MyLinks(userCtx(ctx, user))
	if err != nil || !strings.HasPrefix(links[0].URL, testBaseURL+"/") {
		t.Fatalf("清空后走面板: %v", links)
	}
}

func TestPullSurfboardManagedURLUsesTopWeight(t *testing.T) {
	de, ctx, user, _, token := setupPull(t)
	admin := adminCtx(ctx, uuid.New())
	G := seedUserGroup(t, de.env, ctx, "surf")
	_, err := de.svc.AdminSaveDomainConfig(admin, []DomainGroupInput{
		{Name: "缺省", IsDefault: true, Domains: []Domain{
			{BaseURL: "https://sub-a.example.test", Weight: 1},
			{BaseURL: "https://sub-b.example.test", Weight: 9},
		}},
		{Name: "专线", Domains: []Domain{{BaseURL: "https://vip.example.test", Weight: 1}}, UserGroupIDs: []uuid.UUID{G}},
	})
	if err != nil {
		t.Fatal(err)
	}
	de.svc.extras.snap.Store(nil)

	res, err := de.svc.Pull(ctx, PullRequest{Prefix: testPrefix, Token: token, Target: string(render.FormatSurfboard)})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(res.Body), "#!MANAGED-CONFIG https://sub-b.example.test/") {
		t.Fatalf("缺省组最高权重: %s", res.Body)
	}

	de.owners.setGroup(user, &G)
	de.svc.extras.snap.Store(nil)
	res, err = de.svc.Pull(ctx, PullRequest{Prefix: testPrefix, Token: token, Target: string(render.FormatSurfboard)})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(res.Body), "#!MANAGED-CONFIG https://vip.example.test/") {
		t.Fatalf("用户组专线: %s", res.Body)
	}
}

func TestPullViaSubDomainNotesSuccess(t *testing.T) {
	de, ctx, _, _, token := setupPull(t)
	admin := adminCtx(ctx, uuid.New())
	_, err := de.svc.AdminSaveDomainConfig(admin, []DomainGroupInput{{
		Name: "缺省", IsDefault: true,
		Domains: []Domain{{BaseURL: "https://sub-a.example.test", Weight: 1}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	de.svc.extras.snap.Store(nil)
	_, rdb := testkit.Redis(t)
	de.svc.UseDomainScores(rdb)

	origNow := de.svc.now
	base := time.Now().UTC()
	var cur time.Time
	cur = base
	de.svc.now = func() time.Time { return cur }
	t.Cleanup(func() { de.svc.now = origNow })

	if _, err := de.svc.Pull(ctx, PullRequest{Prefix: testPrefix, Token: token, Host: "sub-a.example.test"}); err != nil {
		t.Fatalf("第一次拉取: %v", err)
	}
	cur = base.Add(11 * time.Second)
	if _, err := de.svc.Pull(ctx, PullRequest{Prefix: testPrefix, Token: token, Host: "sub-a.example.test"}); err != nil {
		t.Fatalf("第二次拉取: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	var score float64
	for time.Now().Before(deadline) {
		m, ok := de.svc.scores.Read(ctx, []string{"https://sub-a.example.test"}, cur)
		if ok && almost(m["https://sub-a.example.test"], 1.05, 0.01) {
			score = m["https://sub-a.example.test"]
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if score == 0 {
		t.Fatalf("分数应 ≈ 1.05（同一凭据去重），实际 %v", score)
	}

	G := seedUserGroup(t, de.env, ctx, "vip-pull")
	_, err = de.svc.AdminSaveDomainConfig(admin, []DomainGroupInput{
		{Name: "缺省", IsDefault: true, Domains: []Domain{{BaseURL: "https://sub-a.example.test", Weight: 1}}},
		{Name: "专线", Domains: []Domain{{BaseURL: "https://vip.example.test", Weight: 1}}, UserGroupIDs: []uuid.UUID{G}},
	})
	if err != nil {
		t.Fatal(err)
	}
	de.svc.extras.snap.Store(nil)
	cur = base.Add(22 * time.Second)
	if _, err := de.svc.Pull(ctx, PullRequest{Prefix: testPrefix, Token: token, Host: "vip.example.test"}); err != nil {
		t.Fatalf("别组 Host 拉取: %v", err)
	}
	// 加分在拉取里同步攒进 pending，没攒进去就不会有后台写：直接看 pending 与 Redis 即可，不用等
	de.svc.scores.mu.Lock()
	_, queued := de.svc.scores.pending["https://vip.example.test"]
	de.svc.scores.mu.Unlock()
	if queued || rdb.HExists(ctx, domainScoreKey, "https://vip.example.test").Val() {
		t.Fatal("令牌主人不在专线组，vip 域名不应加分")
	}

	if _, err := de.svc.Pull(ctx, PullRequest{Prefix: testPrefix, Token: token, Host: "other.example.test"}); err != nil {
		t.Fatal(err)
	}
	if rdb.HExists(ctx, domainScoreKey, "https://other.example.test").Val() {
		t.Fatal("未登记域名不应写分数")
	}
}

// TestPullNewFormatsWriteFetchLog 钉住迁移里 fetch log 的 format CHECK 与 render.Format 常量一致。
func TestPullNewFormatsWriteFetchLog(t *testing.T) {
	de, ctx, _, sub, token := setupPull(t)
	de.nodes.nodes = []render.Node{
		{Name: "香港 01", Type: "vless", Host: "hk.example.test", Port: 443, Config: map[string]any{}, TrafficRate: 1},
		{Name: "香港 SS", Type: "shadowsocks", Host: "ss.example.test", Port: 8388, Config: map[string]any{"method": "aes-128-gcm"}, TrafficRate: 1},
	}

	formats := []render.Format{
		render.FormatStash,
		render.FormatShadowrocket,
		render.FormatLoon,
		render.FormatSurfboard,
		render.FormatSIP008,
	}
	for _, f := range formats {
		res, err := de.svc.Pull(ctx, PullRequest{Prefix: testPrefix, Token: token, Target: string(f)})
		if err != nil {
			t.Fatalf("拉取格式 %s 失败：%v", f, err)
		}
		if len(res.Body) == 0 {
			t.Fatalf("拉取格式 %s 正文为空", f)
		}
		if n := countRows(t, de.env, ctx, "SELECT count(*) FROM subscription_fetch_log WHERE subscription_id = ? AND result = 'ok' AND format = ?", sub.ID, string(f)); n != 1 {
			t.Fatalf("格式 %s 的成功拉取日志行数应为 1，实际为 %d", f, n)
		}
	}
}

// decodeURIBody 解开 URI 订阅的整体 base64。
func wiringURIBody(t *testing.T, b []byte) string {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(string(b))
	if err != nil {
		t.Fatalf("URI 订阅不是 base64: %v", err)
	}
	return string(raw)
}
