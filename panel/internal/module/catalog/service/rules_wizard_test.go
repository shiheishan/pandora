package service

// 向导相关的纯规则单测：新建校验、版本语义、额度变化判断、继承旧版本、价格清单。

import (
	"encoding/json"
	"testing"

	"github.com/shopspring/decimal"
)

func TestValidateWizardCreate(t *testing.T) {
	plan := PlanInput{Code: "ab", Name: "n", Visibility: VisibilityPublic}
	t.Run("publish 无价格", func(t *testing.T) {
		in := WizardCreate{Plan: plan, Publish: true}
		f := expectValidation(t, validateWizardCreate(&in))
		if f["prices"] == "" {
			t.Fatal(f)
		}
	})
	t.Run("两档同周期币种", func(t *testing.T) {
		in := WizardCreate{
			Plan: plan,
			Prices: []WizardPrice{
				{Currency: "CNY", UnitAmount: decimal.New(1, 0), BillingInterval: "month", IntervalCount: 1},
				{Currency: "CNY", UnitAmount: decimal.New(2, 0), BillingInterval: "month", IntervalCount: 1},
			},
		}
		f := expectValidation(t, validateWizardCreate(&in))
		if f["prices.1.billing_interval"] == "" {
			t.Fatalf("fields=%v", f)
		}
	})
	t.Run("第 1 档金额 0", func(t *testing.T) {
		in := WizardCreate{
			Plan: plan,
			Prices: []WizardPrice{
				{Currency: "CNY", UnitAmount: decimal.Zero, BillingInterval: "month", IntervalCount: 1},
			},
		}
		f := expectValidation(t, validateWizardCreate(&in))
		if f["prices.0.unit_amount"] == "" {
			t.Fatal(f)
		}
	})
	t.Run("traffic_gb -1", func(t *testing.T) {
		in := WizardCreate{Plan: plan, TrafficGB: int64Ptr(-1)}
		f := expectValidation(t, validateWizardCreate(&in))
		if f["traffic_gb"] == "" {
			t.Fatal(f)
		}
	})
}

func TestWizardVersionInput(t *testing.T) {
	nowPlan := PlanInput{Code: "x", Name: "y"}
	t.Run("TrafficGB 10", func(t *testing.T) {
		vin := wizardVersionInput(WizardCreate{Plan: nowPlan, TrafficGB: int64Ptr(10)})
		if len(vin.Quotas) != 1 || vin.Quotas[0].Metric != MetricTraffic {
			t.Fatalf("quotas=%v", vin.Quotas)
		}
		want := 10 * BytesPerGB
		if vin.Quotas[0].Limit == nil || *vin.Quotas[0].Limit != want || vin.Quotas[0].Period != PeriodCycle {
			t.Fatalf("q=%+v", vin.Quotas[0])
		}
	})
	t.Run("MaxDevices 0", func(t *testing.T) {
		vin := wizardVersionInput(WizardCreate{Plan: nowPlan, MaxDevices: intPtr(0)})
		if vin.MaxDevices != nil {
			t.Fatalf("max=%v", vin.MaxDevices)
		}
		for _, q := range vin.Quotas {
			if q.Metric == MetricDevices {
				t.Fatal("不应有设备配额")
			}
		}
	})
	t.Run("MaxDevices 3", func(t *testing.T) {
		vin := wizardVersionInput(WizardCreate{Plan: nowPlan, MaxDevices: intPtr(3)})
		if vin.MaxDevices == nil || *vin.MaxDevices != 3 {
			t.Fatalf("max=%v", vin.MaxDevices)
		}
		found := false
		for _, q := range vin.Quotas {
			if q.Metric == MetricDevices && q.Limit != nil && *q.Limit == 3 {
				found = true
			}
		}
		if !found {
			t.Fatalf("quotas=%v", vin.Quotas)
		}
	})
	t.Run("重置策略空串", func(t *testing.T) {
		vin := wizardVersionInput(WizardCreate{Plan: nowPlan})
		if vin.QuotaResetStrategy != ResetBillingCycle {
			t.Fatalf("%q", vin.QuotaResetStrategy)
		}
	})
	t.Run("DeviceReleaseHours 24", func(t *testing.T) {
		vin := wizardVersionInput(WizardCreate{Plan: nowPlan})
		if vin.DeviceReleaseHours != 24 {
			t.Fatalf("%d", vin.DeviceReleaseHours)
		}
	})
}

func TestQuotaDiffers(t *testing.T) {
	traffic10 := func() *Version {
		lim := int64(10 * BytesPerGB)
		return &Version{Quotas: []Quota{{Metric: MetricTraffic, Limit: &lim, Unit: "bytes", Period: PeriodCycle}}}
	}
	t.Run("三个都没给", func(t *testing.T) {
		if quotaDiffers(traffic10(), WizardUpdate{}) {
			t.Fatal("应 false")
		}
	})
	t.Run("cur nil 给了流量", func(t *testing.T) {
		if !quotaDiffers(nil, WizardUpdate{TrafficGB: int64Ptr(1)}) {
			t.Fatal("应 true")
		}
	})
	t.Run("原样提交当前流量 GB", func(t *testing.T) {
		if quotaDiffers(traffic10(), WizardUpdate{TrafficGB: int64Ptr(10)}) {
			t.Fatal("应 false")
		}
	})
	t.Run("MaxDevices nil 改 3", func(t *testing.T) {
		cur := &Version{MaxDevices: nil}
		if !quotaDiffers(cur, WizardUpdate{MaxDevices: OptionalInt{Set: true, Value: intPtr(3)}}) {
			t.Fatal("应 true")
		}
	})
	t.Run("显式 null 设备且当前也是 nil", func(t *testing.T) {
		cur := &Version{MaxDevices: nil}
		if quotaDiffers(cur, WizardUpdate{MaxDevices: OptionalInt{Set: true, Value: nil}}) {
			t.Fatal("应 false")
		}
	})
}

func TestInheritVersionInput(t *testing.T) {
	limT := int64(5 * BytesPerGB)
	devLim := int64(2)
	cur := &Version{
		QuotaResetStrategy:   ResetNaturalMonth,
		QuotaResetDay:        intPtr(10),
		RenewalExtendsPeriod: false,
		RenewalResetsQuota:   false,
		RenewalKeepsAddons:   false,
		MaxDevices:           intPtr(2),
		DeviceReleaseHours:   48,
		Notes:                strPtr("备注"),
		Entitlements:         []Entitlement{{Code: "e", Value: json.RawMessage(`true`)}},
		Quotas: []Quota{
			{Metric: MetricTraffic, Limit: &limT, Unit: "bytes", Period: PeriodMonth},
			{Metric: MetricDevices, Limit: &devLim, Unit: "count", Period: PeriodCycle},
		},
	}
	t.Run("继承续费备注权益与非流量配额", func(t *testing.T) {
		out := inheritVersionInput(cur, WizardUpdate{})
		if out.RenewalExtendsPeriod || out.RenewalResetsQuota || out.RenewalKeepsAddons {
			t.Fatalf("renewal: %+v", out)
		}
		if out.Notes == nil || *out.Notes != "备注" || len(out.Entitlements) != 1 {
			t.Fatalf("notes/ent: %+v", out)
		}
		if out.QuotaResetStrategy != ResetNaturalMonth || out.QuotaResetDay == nil || *out.QuotaResetDay != 10 {
			t.Fatalf("reset: %+v", out)
		}
	})
	t.Run("只改流量时设备配额保持", func(t *testing.T) {
		out := inheritVersionInput(cur, WizardUpdate{TrafficGB: int64Ptr(20)})
		if out.MaxDevices == nil || *out.MaxDevices != 2 {
			t.Fatalf("max=%v", out.MaxDevices)
		}
		var dev, traf *Quota
		for _, q := range out.Quotas {
			if q.Metric == MetricDevices {
				dev = &q
			}
			if q.Metric == MetricTraffic {
				traf = &q
			}
		}
		if dev == nil || traf == nil || traf.Period != PeriodMonth {
			t.Fatalf("quotas=%v", out.Quotas)
		}
		want := int64(20 * BytesPerGB)
		if traf.Limit == nil || *traf.Limit != want {
			t.Fatalf("traffic=%v", traf)
		}
	})
	t.Run("TrafficGB 0 去掉流量配额", func(t *testing.T) {
		out := inheritVersionInput(cur, WizardUpdate{TrafficGB: int64Ptr(0)})
		for _, q := range out.Quotas {
			if q.Metric == MetricTraffic {
				t.Fatal("不应有流量配额")
			}
		}
	})
	t.Run("cur nil 缺省", func(t *testing.T) {
		out := inheritVersionInput(nil, WizardUpdate{})
		if out.QuotaResetStrategy != ResetBillingCycle || !out.RenewalExtendsPeriod {
			t.Fatalf("%+v", out)
		}
		if out.Entitlements == nil || out.Quotas == nil {
			t.Fatal("应非 nil 切片")
		}
	})
}

func TestPrepareWizardPrices(t *testing.T) {
	raw := []WizardPrice{
		{Currency: "CNY", UnitAmount: decimal.New(1, 0), BillingInterval: "month", IntervalCount: 1},
		{Currency: "CNY", UnitAmount: decimal.New(2, 0), BillingInterval: "month", IntervalCount: 1},
	}
	_, err := prepareWizardPrices(raw)
	f := expectValidation(t, err)
	if f["prices.1.billing_interval"] == "" {
		t.Fatal(f)
	}
	single := []WizardPrice{
		{Currency: "usd", UnitAmount: decimal.New(9, 0), BillingInterval: "year", IntervalCount: 2},
	}
	m, err := prepareWizardPrices(single)
	if err != nil {
		t.Fatal(err)
	}
	key := wizardKey("year", 2, "USD")
	if _, ok := m[key]; !ok {
		t.Fatalf("keys=%v", m)
	}
}
