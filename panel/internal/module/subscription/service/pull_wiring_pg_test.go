package service

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"

	settingssvc "github.com/aegispanel/aegis/internal/module/settings/service"
	"github.com/aegispanel/aegis/internal/module/subscription/render"
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

func TestLinksUseLinkBaseURLs(t *testing.T) {
	de, ctx, user, sub := rotateSetup(t)
	de.useExtras(settingssvc.SubscriptionExtras{LinkBaseURLs: []string{"https://sub-a.example.test", "https://sub-b.example.test"}})
	links, err := de.svc.MyLinks(userCtx(ctx, user))
	if err != nil || len(links) != 1 {
		t.Fatalf("链接列表: %v %d", err, len(links))
	}
	u := links[0].URL
	if !strings.HasPrefix(u, "https://sub-a.example.test/"+testPrefix+"/") && !strings.HasPrefix(u, "https://sub-b.example.test/"+testPrefix+"/") {
		t.Fatalf("链接没用订阅域名：%s", u)
	}
	url, err := de.svc.MyRotate(userCtx(ctx, user), sub.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(url, "https://sub-a.example.test/") && !strings.HasPrefix(url, "https://sub-b.example.test/") {
		t.Fatalf("换链接返回的地址没用订阅域名：%s", url)
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
