package app

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	appearancesvc "github.com/aegispanel/aegis/internal/module/appearance/service"
	catalogsvc "github.com/aegispanel/aegis/internal/module/catalog/service"
	settingssvc "github.com/aegispanel/aegis/internal/module/settings/service"
	"github.com/aegispanel/aegis/internal/pkg/webapp"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

const gib = 1024 * 1024 * 1024

type fakeHomeSettings struct {
	h   settingssvc.Home
	err error
	n   int
}

func (f *fakeHomeSettings) Home(context.Context) (settingssvc.Home, error) {
	f.n++
	return f.h, f.err
}

type fakeHomeCatalog struct {
	plans []catalogsvc.OfferPlan
	err   error
	n     int
}

func (f *fakeHomeCatalog) PortalCatalog(context.Context, *uuid.UUID) ([]catalogsvc.OfferPlan, error) {
	f.n++
	return f.plans, f.err
}

type fakePageMeta struct {
	m appearancesvc.PageMeta
}

func (f fakePageMeta) PageMeta(context.Context) appearancesvc.PageMeta { return f.m }

func TestFormatYuan(t *testing.T) {
	cases := []struct {
		cents int64
		want  string
	}{
		{0, "¥0.00"},
		{1200, "¥12.00"},
		{123456, "¥1,234.56"},
		{100000000, "¥1,000,000.00"},
		{-50, "-¥0.50"},
	}
	for _, c := range cases {
		if got := formatYuan(c.cents); got != c.want {
			t.Fatalf("formatYuan(%d) = %q, want %q", c.cents, got, c.want)
		}
	}
}

func TestGB(t *testing.T) {
	cases := []struct {
		bytes int64
		want  string
	}{
		{100 * gib, "100G"},
		{int64(1.5 * float64(gib)), "1.5G"},
		{2 * gib, "2G"},
		{1536 * gib, "1.5T"},
		{500 * 1024 * 1024, "500M"},
	}
	for _, c := range cases {
		if got := gb(c.bytes); got != c.want {
			t.Fatalf("gb(%d) = %q, want %q", c.bytes, got, c.want)
		}
	}
}

func TestHomePlans(t *testing.T) {
	g100 := int64(100 * gib)
	g500 := int64(500 * gib)
	d3 := 3
	kbps := 100000
	days := 30
	desc := "  虚构的入门套餐  "

	sub := catalogsvc.OfferPlan{
		Plan: catalogsvc.Plan{
			Kind:             catalogsvc.KindSubscription,
			Name:             "虚构入门",
			Description:      &desc,
			AllowNewPurchase: true,
			Recommended:      true,
		},
		Version: catalogsvc.Version{
			Quotas:     []catalogsvc.Quota{{Metric: catalogsvc.MetricTraffic, Limit: &g100}},
			MaxDevices: &d3,
		},
		Prices: []catalogsvc.Price{
			{BillingInterval: catalogsvc.IntervalMonth, IntervalCount: 1, UnitAmount: decimal.RequireFromString("12.00")},
			{BillingInterval: catalogsvc.IntervalMonth, IntervalCount: 3, UnitAmount: decimal.RequireFromString("33.00")},
			{BillingInterval: catalogsvc.IntervalYear, IntervalCount: 1, UnitAmount: decimal.RequireFromString("108.00")},
		},
	}

	once := catalogsvc.OfferPlan{
		Plan: catalogsvc.Plan{
			Kind:             catalogsvc.KindOneTime,
			Name:             "虚构一次性",
			AllowNewPurchase: true,
		},
		Version: catalogsvc.Version{
			Quotas:       []catalogsvc.Quota{{Metric: catalogsvc.MetricTraffic, Limit: &g500}},
			ThrottleKbps: &kbps,
		},
		Prices: []catalogsvc.Price{
			{BillingInterval: catalogsvc.IntervalOneTime, IntervalCount: 1, UnitAmount: decimal.RequireFromString("50.00"), ValidityDays: &days},
		},
	}

	hidden := catalogsvc.OfferPlan{
		Plan: catalogsvc.Plan{Kind: catalogsvc.KindSubscription, AllowNewPurchase: false},
		Prices: []catalogsvc.Price{
			{BillingInterval: catalogsvc.IntervalMonth, IntervalCount: 1, UnitAmount: decimal.RequireFromString("1.00")},
		},
	}

	// 输入时一次性在前，输出应订阅在前。
	got := homePlans([]catalogsvc.OfferPlan{once, sub, hidden})
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	wantSub := webapp.HomePlan{
		Name: "虚构入门", Kind: "按时间", Badge: "推荐", Desc: "虚构的入门套餐",
		Usage: "每期 100G · 3 台设备", Price: "¥12.00 / 月", PriceNote: "季付 ¥33.00 · 年付 ¥108.00",
	}
	if got[0] != wantSub {
		t.Fatalf("sub plan:\n got %+v\nwant %+v", got[0], wantSub)
	}
	wantOnce := webapp.HomePlan{
		Name: "虚构一次性", Kind: "一次性",
		Usage: "共 500G · 不限台数 · 限速 100 Mbps", Price: "¥50.00", PriceNote: "有效 30 天",
	}
	if got[1] != wantOnce {
		t.Fatalf("once plan:\n got %+v\nwant %+v", got[1], wantOnce)
	}

	// 只有季付档。
	seasonOnly := catalogsvc.OfferPlan{
		Plan:    catalogsvc.Plan{Kind: catalogsvc.KindSubscription, Name: "仅季付", AllowNewPurchase: true},
		Version: catalogsvc.Version{Quotas: []catalogsvc.Quota{{Metric: catalogsvc.MetricTraffic, Limit: &g100}}},
		Prices: []catalogsvc.Price{
			{BillingInterval: catalogsvc.IntervalMonth, IntervalCount: 3, UnitAmount: decimal.RequireFromString("33.00")},
		},
	}
	g2 := homePlans([]catalogsvc.OfferPlan{seasonOnly})
	if g2[0].Price != "¥33.00 / 季" || g2[0].PriceNote != "" {
		t.Fatalf("season only: price=%q note=%q", g2[0].Price, g2[0].PriceNote)
	}
}

func TestPortalHomeDisabled(t *testing.T) {
	settings := &fakeHomeSettings{h: settingssvc.Home{Enabled: false}}
	catalog := &fakeHomeCatalog{}
	p := newPortalHome(settings, catalog, fakePageMeta{})
	_, ok := p.Home(context.Background())
	if ok {
		t.Fatal("expected false when disabled")
	}
	if catalog.n != 0 {
		t.Fatalf("PortalCatalog calls = %d, want 0", catalog.n)
	}
}

func TestPortalHomeCachesAndInvalidates(t *testing.T) {
	now := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	settings := &fakeHomeSettings{h: settingssvc.Home{Enabled: true}}
	catalog := &fakeHomeCatalog{plans: []catalogsvc.OfferPlan{}}
	p := newPortalHome(settings, catalog, fakePageMeta{})
	p.now = func() time.Time { return now }
	ctx := context.Background()

	_, ok := p.Home(ctx)
	if !ok || settings.n != 1 || catalog.n != 1 {
		t.Fatalf("first read: ok=%v settings=%d catalog=%d", ok, settings.n, catalog.n)
	}
	_, ok = p.Home(ctx)
	if !ok || settings.n != 1 || catalog.n != 1 {
		t.Fatalf("cached: settings=%d catalog=%d", settings.n, catalog.n)
	}

	p.HomeSettingsChanged()
	_, ok = p.Home(ctx)
	if !ok || settings.n != 2 || catalog.n != 2 {
		t.Fatalf("after invalidate: settings=%d catalog=%d", settings.n, catalog.n)
	}

	now = now.Add(31 * time.Second)
	_, ok = p.Home(ctx)
	if !ok || settings.n != 3 || catalog.n != 3 {
		t.Fatalf("after ttl: settings=%d catalog=%d", settings.n, catalog.n)
	}
}

func TestPortalHomeReadErrorDegrades(t *testing.T) {
	now := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	settings := &fakeHomeSettings{err: errors.New("db down")}
	catalog := &fakeHomeCatalog{}
	p := newPortalHome(settings, catalog, fakePageMeta{})
	p.now = func() time.Time { return now }
	ctx := context.Background()

	_, ok := p.Home(ctx)
	if ok || settings.n != 1 {
		t.Fatalf("first: ok=%v reads=%d", ok, settings.n)
	}
	_, ok = p.Home(ctx)
	if ok || settings.n != 1 {
		t.Fatalf("error cached: reads=%d", settings.n)
	}
	now = now.Add(6 * time.Second)
	_, ok = p.Home(ctx)
	if ok || settings.n != 2 {
		t.Fatalf("after error ttl: reads=%d", settings.n)
	}
}

func TestPortalHomeUsesPageMeta(t *testing.T) {
	settings := &fakeHomeSettings{h: settingssvc.Home{Enabled: true}}
	catalog := &fakeHomeCatalog{plans: []catalogsvc.OfferPlan{}}
	meta := fakePageMeta{m: appearancesvc.PageMeta{
		SiteName: "虚构站点",
		Tagline:  "虚构标语",
		Logo:     "data:image/png;base64,xx",
	}}
	p := newPortalHome(settings, catalog, meta)
	h, ok := p.Home(context.Background())
	if !ok {
		t.Fatal("expected home")
	}
	if h.SiteName != "虚构站点" || h.Tagline != "虚构标语" || h.Logo != meta.m.Logo {
		t.Fatalf("meta: %+v", h)
	}
}

func TestPortalHomeCatalogErrorDegrades(t *testing.T) {
	now := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	settings := &fakeHomeSettings{h: settingssvc.Home{Enabled: true, Intro: "介绍"}}
	catalog := &fakeHomeCatalog{err: errors.New("catalog down")}
	p := newPortalHome(settings, catalog, fakePageMeta{m: appearancesvc.PageMeta{SiteName: "夜海加速"}})
	p.now = func() time.Time { return now }
	ctx := context.Background()

	if _, ok := p.Home(ctx); ok || catalog.n != 1 {
		t.Fatalf("first: ok=%v catalog=%d", ok, catalog.n)
	}
	// 失败缓存 5 秒：这段时间不再读库，也不出片段
	now = now.Add(4 * time.Second)
	if _, ok := p.Home(ctx); ok || catalog.n != 1 || settings.n != 1 {
		t.Fatalf("error cached: ok=%v catalog=%d settings=%d", ok, catalog.n, settings.n)
	}
	now = now.Add(2 * time.Second)
	catalog.err = nil
	if _, ok := p.Home(ctx); !ok || catalog.n != 2 {
		t.Fatalf("after error ttl: ok=%v catalog=%d", ok, catalog.n)
	}
}

// blockingHomeSettings 第一次 Home 等 release 再返回旧值，之后返回新值。
type blockingHomeSettings struct {
	started chan struct{}
	release chan struct{}
	calls   atomic.Int32
}

func (b *blockingHomeSettings) Home(context.Context) (settingssvc.Home, error) {
	if b.calls.Add(1) == 1 {
		close(b.started)
		<-b.release
		return settingssvc.Home{Enabled: true, Intro: "旧介绍"}, nil
	}
	return settingssvc.Home{Enabled: true, Intro: "新介绍"}, nil
}

func TestPortalHomeChangeDuringLoadServesNewValue(t *testing.T) {
	settings := &blockingHomeSettings{started: make(chan struct{}), release: make(chan struct{})}
	p := newPortalHome(settings, &fakeHomeCatalog{}, fakePageMeta{m: appearancesvc.PageMeta{SiteName: "夜海加速"}})
	ctx := context.Background()

	done := make(chan webapp.Home)
	go func() {
		h, _ := p.Home(ctx)
		done <- h
	}()
	<-settings.started
	// 加载途中保存了首页设置：在途的旧结果可以回给当时的请求，但之后的请求要拿到新值
	p.HomeSettingsChanged()
	close(settings.release)
	if old := <-done; old.Intro != "旧介绍" {
		t.Fatalf("in-flight result = %q", old.Intro)
	}
	for range 2 {
		h, ok := p.Home(ctx)
		if !ok || h.Intro != "新介绍" {
			t.Fatalf("after change: ok=%v intro=%q", ok, h.Intro)
		}
	}
	if n := settings.calls.Load(); n != 2 {
		t.Fatalf("settings reads = %d, want 2 (new value cached)", n)
	}
}
