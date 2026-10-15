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
	rows  repo.DomainConfigRows
	err   error
}

func (f *fakeTemplateReader) TemplateContents(context.Context) (map[string]string, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return f.m, nil
}

func (f *fakeTemplateReader) DomainConfig(context.Context) (repo.DomainConfigRows, error) {
	return f.rows, nil
}

func TestPullExtrasGet(t *testing.T) {
	ctx := context.Background()
	var pe pullExtras
	t0 := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	settings := &fakeExtrasSettings{val: settingssvc.SubscriptionExtras{
		InfoNodes: true, ProtocolPrefix: true,
	}}
	defGroupID := uuid.MustParse("00000000-0000-4000-8000-000000000010")
	tr := &fakeTemplateReader{
		m: map[string]string{"clash": "mixed-port: 7891\n{{proxies}}\n{{proxy_groups}}\n"},
		rows: repo.DomainConfigRows{
			Groups: []repo.DomainGroupRow{
				{ID: defGroupID, Name: "默认", IsDefault: true, Position: 0},
			},
			Domains: []repo.DomainRow{
				{GroupID: defGroupID, BaseURL: "https://sub.example.test", Weight: 2, Position: 0},
			},
			Assigns: []repo.DomainAssignRow{},
		},
	}

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
	if snap.domains.empty() {
		t.Fatal("snap.domains 不应为空")
	}
	if g := snap.domains.groupFor(nil); g == nil || g.ID != defGroupID {
		t.Fatalf("groupFor(nil) 返回异常: %+v", g)
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
	if snap2.settings.InfoNodes || snap2.settings.ProtocolPrefix {
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

	// 已有快照时重读失败：沿用旧快照、不报错，extrasRetry 内不再重读，之后再试
	failSettings.val = settingssvc.SubscriptionExtras{InfoNodes: true}
	failSettings.err = errors.New("读设置失败")
	t1 := t0.Add(80 * time.Second)
	stale, err := pe3.get(ctx, failSettings, tr, t1)
	if err != nil || stale == nil || stale.settings.InfoNodes {
		t.Fatalf("重读失败应沿用旧快照: snap=%+v err=%v", stale, err)
	}
	if _, err := pe3.get(ctx, failSettings, tr, t1.Add(extrasRetry-time.Second)); err != nil || failSettings.calls != 3 {
		t.Fatalf("extrasRetry 内不应重读 calls=%d err=%v", failSettings.calls, err)
	}
	failSettings.err = nil
	fresh, err := pe3.get(ctx, failSettings, tr, t1.Add(extrasRetry))
	if err != nil || failSettings.calls != 4 || !fresh.settings.InfoNodes {
		t.Fatalf("extrasRetry 后应重读并用上新值 calls=%d err=%v", failSettings.calls, err)
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
		ProxyUUID:        uuid.MustParse("00000000-0000-4000-8000-000000000001"),
		PeriodEnd:        periodEnd,
		TrafficLimit:     &limit,
		TrafficConsumed:  &consumed,
		PackRemaining:    1 << 20,
		TrafficPeriodEnd: &nextReset,
	}
	snapOn := &extrasSnapshot{
		settings:  settingssvc.SubscriptionExtras{InfoNodes: true, ProtocolPrefix: true},
		templates: map[render.Format]string{render.FormatClash: "custom"},
	}
	req := liveRequest(liveInput{
		Format: render.FormatClash,
		Nodes:  []render.Node{{Name: "n1", Type: "shadowsocks", Host: "h", Port: 1, Config: map[string]any{"method": "aes-128-gcm"}}},
		Row:    row,
		Extras: snapOn,
		Types:  "vless|ss",
		Filter: "香港",
		Loc:    time.UTC,
		Now:    now,
	})
	if len(req.InfoLines) == 0 || !stringsHasPrefix(req.InfoLines[0], "剩余流量") {
		t.Fatalf("信息节点应有剩余流量行: %v", req.InfoLines)
	}
	if len(req.InfoLines) != 3 || req.InfoLines[1] != "距离下次重置剩余：7 天" {
		t.Fatalf("距下次重置应取 Row.TrafficPeriodEnd: %v", req.InfoLines)
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

func TestSubscribeURLFor(t *testing.T) {
	fallback := "https://panel.example.test"
	prefix := "a1b2c3d4e5f6"
	token := "tok"

	// 1. ex == nil → 用 fallback
	gotNil := subscribeURLFor(nil, nil, fallback, prefix, token)
	wantNil := linkURL(fallback, prefix, token)
	if gotNil != wantNil {
		t.Fatalf("ex nil: got %q want %q", gotNil, wantNil)
	}

	// 2. 有一个缺省组（域名 a 权重 1、b 权重 5、c 权重 5）→ 取 b（最高权重、同权重取前面的）
	defGroup := DomainGroup{
		ID:        uuid.New(),
		Name:      "默认",
		IsDefault: true,
		Domains: []Domain{
			{BaseURL: "https://a.example.test", Weight: 1},
			{BaseURL: "https://b.example.test", Weight: 5},
			{BaseURL: "https://c.example.test", Weight: 5},
		},
	}
	// 3. 用户组 G 分到另一组（只有域名 d）→ 传 &G 取 d；传别的用户组取缺省组的 b
	userGroupG := uuid.New()
	otherUserGroup := uuid.New()
	vipGroup := DomainGroup{
		ID:           uuid.New(),
		Name:         "专线",
		IsDefault:    false,
		Domains:      []Domain{{BaseURL: "https://d.example.test", Weight: 10}},
		UserGroupIDs: []uuid.UUID{userGroupG},
	}

	exWithDefault := &extrasSnapshot{domains: newDomainSet([]DomainGroup{defGroup, vipGroup})}
	if got := subscribeURLFor(exWithDefault, nil, fallback, prefix, token); got != linkURL("https://b.example.test", prefix, token) {
		t.Fatalf("缺省组无用户组: got %q want %q", got, linkURL("https://b.example.test", prefix, token))
	}
	if got := subscribeURLFor(exWithDefault, &userGroupG, fallback, prefix, token); got != linkURL("https://d.example.test", prefix, token) {
		t.Fatalf("用户组 G: got %q want %q", got, linkURL("https://d.example.test", prefix, token))
	}
	if got := subscribeURLFor(exWithDefault, &otherUserGroup, fallback, prefix, token); got != linkURL("https://b.example.test", prefix, token) {
		t.Fatalf("未分配的用户组: got %q want %q", got, linkURL("https://b.example.test", prefix, token))
	}

	// 4. 只有非缺省组、用户没分到 → fallback
	exNoDefault := &extrasSnapshot{domains: newDomainSet([]DomainGroup{vipGroup})}
	if got := subscribeURLFor(exNoDefault, nil, fallback, prefix, token); got != wantNil {
		t.Fatalf("只有非缺省组用户未分组: got %q want %q", got, wantNil)
	}
	if got := subscribeURLFor(exNoDefault, &otherUserGroup, fallback, prefix, token); got != wantNil {
		t.Fatalf("只有非缺省组用户分到未登记组: got %q want %q", got, wantNil)
	}
}
