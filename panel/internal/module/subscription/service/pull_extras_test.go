package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	settingssvc "github.com/aegispanel/aegis/internal/module/settings/service"
	"github.com/aegispanel/aegis/internal/module/subscription/render"
	"github.com/aegispanel/aegis/internal/module/subscription/repo"
)

type fakeExtrasSettings struct {
	calls int
	val   settingssvc.SubscriptionExtras
	err   error
}

func (f *fakeExtrasSettings) SubscriptionExtras(context.Context) (settingssvc.SubscriptionExtras, error) {
	f.calls++
	if f.err != nil {
		return settingssvc.SubscriptionExtras{}, f.err
	}
	return f.val, nil
}

type fakeTemplateReader struct {
	calls int
	m     map[string]string
	err   error
}

func (f *fakeTemplateReader) TemplateContents(context.Context) (map[string]string, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return f.m, nil
}

func TestPullExtrasGet(t *testing.T) {
	ctx := context.Background()
	var pe pullExtras
	t0 := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	settings := &fakeExtrasSettings{val: settingssvc.SubscriptionExtras{
		InfoNodes: true, ProtocolPrefix: true, LinkBaseURLs: []string{"https://sub.example.com"},
	}}
	tr := &fakeTemplateReader{m: map[string]string{"clash": "mixed-port: 7891\n{{proxies}}\n{{proxy_groups}}\n"}}

	snap, err := pe.get(ctx, settings, tr, t0)
	if err != nil {
		t.Fatal(err)
	}
	if settings.calls != 1 || tr.calls != 1 {
		t.Fatalf("首次读取 settings=%d templates=%d", settings.calls, tr.calls)
	}
	if snap.template(render.FormatClash) == "" {
		t.Fatal("应有 clash 模板")
	}
	if snap.template(render.FormatLoon) != "" {
		t.Fatal("没有 loon 模板应为空")
	}

	_, err = pe.get(ctx, settings, tr, t0.Add(10*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if settings.calls != 1 || tr.calls != 1 {
		t.Fatalf("+10s 不应重读 settings=%d templates=%d", settings.calls, tr.calls)
	}

	_, err = pe.get(ctx, settings, tr, t0.Add(31*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if settings.calls != 2 || tr.calls != 2 {
		t.Fatalf("+31s 应重读 settings=%d templates=%d", settings.calls, tr.calls)
	}

	_, err = pe.get(ctx, settings, tr, t0.Add(-1*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if settings.calls != 3 {
		t.Fatalf("时钟回拨应重读 settings=%d", settings.calls)
	}

	var pe2 pullExtras
	snap2, err := pe2.get(ctx, nil, tr, t0)
	if err != nil {
		t.Fatal(err)
	}
	if snap2.settings.InfoNodes || snap2.settings.ProtocolPrefix || len(snap2.settings.LinkBaseURLs) > 0 {
		t.Fatalf("src nil 时 settings 应为零值 %+v", snap2.settings)
	}

	failSettings := &fakeExtrasSettings{err: errors.New("读设置失败")}
	var pe3 pullExtras
	_, err = pe3.get(ctx, failSettings, tr, t0)
	if err == nil {
		t.Fatal("应返回错误")
	}
	failSettings.err = nil
	_, err = pe3.get(ctx, failSettings, tr, t0.Add(40*time.Second))
	if err != nil {
		t.Fatal("失败后下次仍应可读")
	}
	if failSettings.calls != 2 {
		t.Fatalf("失败后下次应重读 settings=%d", failSettings.calls)
	}
}

func TestPullExtrasGetConcurrent(t *testing.T) {
	ctx := context.Background()
	var pe pullExtras
	t0 := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	settings := &fakeExtrasSettings{val: settingssvc.SubscriptionExtras{}}
	tr := &fakeTemplateReader{m: map[string]string{}}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = pe.get(ctx, settings, tr, t0)
		}()
	}
	wg.Wait()
	if settings.calls != 1 {
		t.Fatalf("并发 get 应只读设置 1 次，实际 %d", settings.calls)
	}
}

func TestLiveRequest(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	periodEnd := now.Add(30 * 24 * time.Hour)
	nextReset := now.Add(7 * 24 * time.Hour)
	limit := int64(10 << 30)
	consumed := int64(1 << 30)
	row := repo.PullRow{
		ProxyUUID:       uuid.MustParse("00000000-0000-4000-8000-000000000001"),
		PeriodEnd:       periodEnd,
		TrafficLimit:    &limit,
		TrafficConsumed: &consumed,
		PackRemaining:   1 << 20,
	}
	snapOn := &extrasSnapshot{
		settings:  settingssvc.SubscriptionExtras{InfoNodes: true, ProtocolPrefix: true},
		templates: map[render.Format]string{render.FormatClash: "custom"},
	}
	req := liveRequest(liveInput{
		Format:    render.FormatClash,
		Nodes:     []render.Node{{Name: "n1", Type: "shadowsocks", Host: "h", Port: 1, Config: map[string]any{"method": "aes-128-gcm"}}},
		Row:       row,
		Extras:    snapOn,
		Types:     "vless|ss",
		Filter:    "香港",
		NextReset: &nextReset,
		Loc:       time.UTC,
		Now:       now,
	})
	if len(req.InfoLines) == 0 || !stringsHasPrefix(req.InfoLines[0], "剩余流量") {
		t.Fatalf("信息节点应有剩余流量行: %v", req.InfoLines)
	}
	if req.Template != "custom" {
		t.Fatalf("Clash 应取快照模板 %q", req.Template)
	}
	if !req.ProtocolPrefix {
		t.Fatal("应开协议前缀")
	}
	wantTypes := []string{"vless", "shadowsocks"}
	if len(req.Types) != 2 || req.Types[0] != wantTypes[0] || req.Types[1] != wantTypes[1] {
		t.Fatalf("Types %v", req.Types)
	}
	if len(req.Keywords) != 1 || req.Keywords[0] != "香港" {
		t.Fatalf("Keywords %v", req.Keywords)
	}
	if req.Status.Total != limit+row.PackRemaining {
		t.Fatalf("Total %d", req.Status.Total)
	}

	snapOff := &extrasSnapshot{settings: settingssvc.SubscriptionExtras{}}
	reqOff := liveRequest(liveInput{Format: render.FormatClash, Row: row, Extras: snapOff, Now: now})
	if len(reqOff.InfoLines) != 0 {
		t.Fatalf("关了信息节点应为空 %v", reqOff.InfoLines)
	}

	reqURI := liveRequest(liveInput{Format: render.FormatURI, Row: row, Extras: snapOn, Now: now})
	if reqURI.Template != "" {
		t.Fatal("URI 不取模板")
	}
}

func stringsHasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

func TestPickLinkBase(t *testing.T) {
	if pickLinkBase(nil, "https://panel.example.com") != "https://panel.example.com" {
		t.Fatal("空列表")
	}
	if pickLinkBase([]string{"https://a.example.com"}, "fb") != "https://a.example.com" {
		t.Fatal("单个元素")
	}
	bases := []string{"https://a.example.com", "https://b.example.com", "https://c.example.com"}
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		u := pickLinkBase(bases, "fb")
		seen[u] = true
		found := false
		for _, b := range bases {
			if b == u {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("结果不在列表里 %q", u)
		}
	}
	for _, b := range bases {
		if !seen[b] {
			t.Fatalf("200 次内应出现过 %s", b)
		}
	}
}

func TestSubscribeURLFor(t *testing.T) {
	got := subscribeURLFor([]string{"https://a.example.com"}, "https://panel.example.com", "a1b2c3d4e5f6", "tok")
	want := "https://a.example.com/a1b2c3d4e5f6/tok"
	if got != want {
		t.Fatalf("有 bases: %q", got)
	}
	got2 := subscribeURLFor(nil, "https://panel.example.com", "a1b2c3d4e5f6", "tok")
	if got2 != "https://panel.example.com/a1b2c3d4e5f6/tok" {
		t.Fatalf("无 bases: %q", got2)
	}
}
