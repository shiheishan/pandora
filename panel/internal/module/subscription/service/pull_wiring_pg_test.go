package service

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	settingssvc "github.com/aegispanel/aegis/internal/module/settings/service"
	"github.com/aegispanel/aegis/internal/module/subscription/render"
	"github.com/aegispanel/aegis/internal/pkg/geoip"
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
	custom := "mixed-port: 7891\n{{proxies}}\nproxy-groups:\n{{proxy_groups}}\nrules:\n  - MATCH,节点选择\n"
	de.exec(t, ctx, `INSERT INTO subscription_templates (format, content, updated_at) VALUES (?, ?, now())`, string(render.FormatClash), custom)
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
		m, ok := de.svc.scores.Read(ctx, []string{"https://sub-a.example.test"}, domainRegion{}, cur)
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
	queued := false
	for b := range de.svc.scores.pending {
		if b.BaseURL == "https://vip.example.test" {
			queued = true
		}
	}
	de.svc.scores.mu.Unlock()
	if queued || rdb.Exists(ctx, domainScoreKeyFor("https://vip.example.test")).Val() != 0 {
		t.Fatal("令牌主人不在专线组，vip 域名不应加分")
	}

	if _, err := de.svc.Pull(ctx, PullRequest{Prefix: testPrefix, Token: token, Host: "other.example.test"}); err != nil {
		t.Fatal(err)
	}
	if rdb.Exists(ctx, domainScoreKeyFor("https://other.example.test")).Val() != 0 {
		t.Fatal("未登记域名不应写分数")
	}
}

func TestPullNotesSuccessByRegion(t *testing.T) {
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

	geo := &fakeGeo{locs: map[string]geoip.Location{
		"198.51.100.7": {Country: "中国", Region: "福建", ISP: "电信", Kind: geoip.KindResidential},
	}}
	de.svc.geo = geo

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

	// 同一凭据去重只记一次：全国 1.05，福建电信再加省、省|运营商两级偏移各 0.05
	fj := domainRegion{Province: "福建", ISP: ispTelecom}
	waitScore(t, de.svc.scores, "https://sub-a.example.test", cur, 1.05)
	waitScoreIn(t, de.svc.scores, "https://sub-a.example.test", fj, cur, 1.15)

	key := domainScoreKeyFor("https://sub-a.example.test")
	if !rdb.HExists(ctx, key, "福建|电信").Val() {
		t.Fatal("应有省|运营商 field")
	}
	if !rdb.HExists(ctx, key, "福建").Val() {
		t.Fatal("应有省 field")
	}
	if !rdb.HExists(ctx, key, domainScoreFieldAll).Val() {
		t.Fatal("应有全国 field")
	}
	if geo.calls.Load() != 1 {
		t.Fatalf("同一凭据×域名每小时只查一次归属地，calls=%d", geo.calls.Load())
	}
}

// linkScoresSetup 准备一份订阅、缺省组里两个域名（基础权重 wa、wb）与已注入的域名分数 Redis。
func linkScoresSetup(t *testing.T, wa, wb int) (*deliveryEnv, context.Context, uuid.UUID, *miniredis.Miniredis, *redis.Client) {
	t.Helper()
	de, ctx, user, _ := rotateSetup(t)
	_, err := de.svc.AdminSaveDomainConfig(adminCtx(ctx, uuid.New()), []DomainGroupInput{{
		Name: "缺省-" + uuid.NewString()[:8], IsDefault: true,
		Domains: []Domain{
			{BaseURL: "https://sub-a.example.test", Weight: wa},
			{BaseURL: "https://sub-b.example.test", Weight: wb},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	de.svc.extras.snap.Store(nil)
	mr, rdb := testkit.Redis(t)
	de.svc.UseDomainScores(rdb)
	return de, ctx, user, mr, rdb
}

// countLinkHosts 调 n 次 MyLinks，数链接落在 sub-a、sub-b 的次数。
func countLinkHosts(t *testing.T, de *deliveryEnv, ctx context.Context, n int) (a, b int) {
	t.Helper()
	for i := 0; i < n; i++ {
		links, err := de.svc.MyLinks(ctx)
		if err != nil || len(links) != 1 {
			t.Fatalf("链接列表: %v %d", err, len(links))
		}
		switch u := links[0].URL; {
		case strings.HasPrefix(u, "https://sub-a.example.test/"):
			a++
		case strings.HasPrefix(u, "https://sub-b.example.test/"):
			b++
		default:
			t.Fatalf("链接不在组里: %s", u)
		}
	}
	return a, b
}

// MyLinks（linkBase）按请求 IP 的地区读细分级：全国分 A 0.1、B 3；福建电信的偏移把 A 顶到 3、把 B 压到 0.1。
// 同一用户从福建电信 IP 来时多半拿到 A，从判不出地区的 IP 来时多半拿到 B（各 20 次，单次约 97%，阈值 14 次，误报概率可忽略）。
func TestLinkBaseReadsVisitorRegionPG(t *testing.T) {
	de, ctx, user, _, rdb := linkScoresSetup(t, 1, 1)
	fjIP, otherIP := "198.51.100.7", "203.0.113.9"
	geo := &fakeGeo{locs: map[string]geoip.Location{
		fjIP: {Country: "中国", Region: "福建", ISP: "电信", Kind: geoip.KindResidential},
	}}
	de.svc.geo = geo
	now := de.svc.clock()
	if err := de.svc.scores.Apply(ctx, map[string]map[string]float64{
		"https://sub-a.example.test": {domainScoreFieldAll: -5, "福建": 2.9, "福建|电信": 2.9},
		"https://sub-b.example.test": {domainScoreFieldAll: 5, "福建": -2.9, "福建|电信": -2.9},
	}, now); err != nil {
		t.Fatal(err)
	}
	if rdb.Exists(ctx, domainScoreKeyFor("https://sub-a.example.test")).Val() != 1 {
		t.Fatal("分数应已写入")
	}

	a, _ := countLinkHosts(t, de, userCtx(pullCtx(ctx, fjIP, "Mozilla/5.0"), user), 20)
	if a < 14 {
		t.Fatalf("福建电信访客 20 次里只有 %d 次拿到 A，应按细分级读分", a)
	}
	if geo.calls.Load() == 0 {
		t.Fatal("生成链接应按请求 IP 查归属地")
	}
	_, b := countLinkHosts(t, de, userCtx(pullCtx(ctx, otherIP, "Mozilla/5.0"), user), 20)
	if b < 14 {
		t.Fatalf("判不出地区的访客 20 次里只有 %d 次拿到 B，应只读全国分", b)
	}
}

// 分数 Redis 读失败时 linkBase 按基础权重选、不报错：A 权重 1 分数 3，B 权重 30 分数 0.1，有分数时两边约各半；
// 读失败后按基础权重 B 约 97%（20 次，阈值 14 次）。
func TestLinkBaseRedisDownUsesBaseWeightsPG(t *testing.T) {
	de, ctx, user, mr, _ := linkScoresSetup(t, 1, 30)
	if err := de.svc.scores.Apply(ctx, map[string]map[string]float64{
		"https://sub-a.example.test": {domainScoreFieldAll: 5},
		"https://sub-b.example.test": {domainScoreFieldAll: -5},
	}, de.svc.clock()); err != nil {
		t.Fatal(err)
	}
	mr.SetError("ERR 测试注入的故障")
	_, b := countLinkHosts(t, de, userCtx(pullCtx(ctx, "198.51.100.7", "Mozilla/5.0"), user), 20)
	if b < 14 {
		t.Fatalf("Redis 读失败时 20 次里只有 %d 次拿到 B，应按基础权重", b)
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
