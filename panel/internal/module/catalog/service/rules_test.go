package service

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/money"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

func expectValidation(t *testing.T, err error) map[string]string {
	t.Helper()
	var ve *errs.Error
	if !errors.As(err, &ve) || ve.Code != errs.ValidationFailed {
		t.Fatalf("got %v, want validation_failed", err)
	}
	return ve.Fields
}

func strPtr(s string) *string { return &s }
func intPtr(n int) *int       { return &n }
func int64Ptr(n int64) *int64 { return &n }

func TestNormalizeHighlights(t *testing.T) {
	t.Run("nil 输入返回非 nil 空切片", func(t *testing.T) {
		out, fields := normalizeHighlights(nil)
		if out == nil {
			t.Fatal("out 不应为 nil")
		}
		if len(out) != 0 || len(fields) != 0 {
			t.Fatalf("out=%v fields=%v", out, fields)
		}
	})
	t.Run("去首尾空白", func(t *testing.T) {
		out, fields := normalizeHighlights([]string{"  a  "})
		if len(fields) != 0 || out[0] != "a" {
			t.Fatalf("out=%v fields=%v", out, fields)
		}
	})
	cases := []struct {
		name   string
		raw    []string
		key    string
		wantOK bool
	}{
		{"空串", []string{""}, "highlights.0", false},
		{"超 40 字", []string{strings.Repeat("中", 41)}, "highlights.0", false},
		{"含 NUL", []string{"ok", "bad\x00"}, "highlights.1", false},
		{"重复", []string{"a", "a"}, "highlights.1", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, fields := normalizeHighlights(tc.raw)
			if tc.wantOK {
				if len(fields) != 0 {
					t.Fatalf("fields=%v", fields)
				}
			} else if fields[tc.key] == "" {
				t.Fatalf("want error on %s, fields=%v", tc.key, fields)
			}
		})
	}
	t.Run("6 条报 highlights", func(t *testing.T) {
		raw := make([]string, 6)
		for i := range raw {
			raw[i] = "x"
		}
		_, fields := normalizeHighlights(raw)
		if fields["highlights"] == "" {
			t.Fatalf("fields=%v", fields)
		}
	})
}

func TestPreparePlanInput(t *testing.T) {
	valid := func() PlanInput {
		return PlanInput{Code: "ab", Name: "套餐", Visibility: VisibilityPublic}
	}
	cases := []struct {
		name string
		mut  func(*PlanInput)
		key  string
	}{
		{"code 非法", func(in *PlanInput) { in.Code = "A" }, "code"},
		{"name 空", func(in *PlanInput) { in.Name = "   " }, "name"},
		{"name 121 字", func(in *PlanInput) { in.Name = strings.Repeat("n", 121) }, "name"},
		{"description 2001 字", func(in *PlanInput) {
			s := strings.Repeat("d", 2001)
			in.Description = &s
		}, "description"},
		{"visibility 非法", func(in *PlanInput) { in.Visibility = "secret" }, "visibility"},
		{"group 无组", func(in *PlanInput) {
			in.Visibility = VisibilityGroup
			in.VisibleGroupIDs = nil
		}, "visible_group_ids"},
		{"非 group 有组", func(in *PlanInput) {
			in.VisibleGroupIDs = []uuid.UUID{uuid.New()}
		}, "visible_group_ids"},
		{"组 id 重复", func(in *PlanInput) {
			id := uuid.New()
			in.Visibility = VisibilityGroup
			in.VisibleGroupIDs = []uuid.UUID{id, id}
		}, "visible_group_ids"},
		{"visible_until 不晚于 from", func(in *PlanInput) {
			from := time.Date(2026, 6, 2, 0, 0, 0, 0, time.UTC)
			until := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
			in.VisibleFrom = &from
			in.VisibleUntil = &until
		}, "visible_until"},
		{"purchase_limit 0", func(in *PlanInput) { in.PurchaseLimitPerUser = intPtr(0) }, "purchase_limit_per_user"},
		{"stock_total -1", func(in *PlanInput) { in.StockTotal = intPtr(-1) }, "stock_total"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := valid()
			if tc.mut != nil {
				tc.mut(&in)
			}
			f := expectValidation(t, preparePlanInput(&in))
			if f[tc.key] == "" {
				t.Fatalf("fields=%v", f)
			}
		})
	}
	t.Run("空 visibility 变 public", func(t *testing.T) {
		in := valid()
		in.Visibility = ""
		if err := preparePlanInput(&in); err != nil {
			t.Fatal(err)
		}
		if in.Visibility != VisibilityPublic {
			t.Fatalf("got %q", in.Visibility)
		}
	})
	t.Run("description 全空白置 nil", func(t *testing.T) {
		in := valid()
		in.Description = strPtr("   \t")
		if err := preparePlanInput(&in); err != nil {
			t.Fatal(err)
		}
		if in.Description != nil {
			t.Fatalf("desc=%v", in.Description)
		}
	})
	t.Run("卖点与资料问题同一 422", func(t *testing.T) {
		in := valid()
		in.Highlights = []string{""}
		in.Code = "!!"
		f := expectValidation(t, preparePlanInput(&in))
		if f["code"] == "" || f["highlights.0"] == "" {
			t.Fatalf("fields=%v", f)
		}
	})
}

func TestValidateVersionInput(t *testing.T) {
	base := func() VersionInput {
		return VersionInput{QuotaResetStrategy: ResetBillingCycle}
	}
	t.Run("fixed_day 无日", func(t *testing.T) {
		in := base()
		in.QuotaResetStrategy = ResetFixedDay
		f := expectValidation(t, validateVersionInput(&in))
		if f["quota_reset_day"] == "" {
			t.Fatal(f)
		}
	})
	t.Run("fixed_day 29 被拒", func(t *testing.T) {
		in := base()
		in.QuotaResetStrategy = ResetFixedDay
		in.QuotaResetDay = intPtr(29)
		f := expectValidation(t, validateVersionInput(&in))
		if f["quota_reset_day"] == "" {
			t.Fatal(f)
		}
	})
	t.Run("fixed_day 15 通过", func(t *testing.T) {
		in := base()
		in.QuotaResetStrategy = ResetFixedDay
		in.QuotaResetDay = intPtr(15)
		if err := validateVersionInput(&in); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("billing_cycle 有日被拒", func(t *testing.T) {
		in := base()
		in.QuotaResetDay = intPtr(5)
		f := expectValidation(t, validateVersionInput(&in))
		if f["quota_reset_day"] == "" {
			t.Fatal(f)
		}
	})
	t.Run("overage 空串写回 suspend", func(t *testing.T) {
		in := base()
		in.OveragePolicy = ""
		if err := validateVersionInput(&in); err != nil {
			t.Fatal(err)
		}
		if in.OveragePolicy != OveragePolicySuspend {
			t.Fatalf("got %q", in.OveragePolicy)
		}
	})
	t.Run("overage throttle 被拒", func(t *testing.T) {
		in := base()
		in.OveragePolicy = "throttle"
		f := expectValidation(t, validateVersionInput(&in))
		if f["overage_policy"] == "" {
			t.Fatal(f)
		}
	})
	t.Run("throttle_kbps 0", func(t *testing.T) {
		in := base()
		in.ThrottleKbps = intPtr(0)
		f := expectValidation(t, validateVersionInput(&in))
		if f["throttle_kbps"] == "" {
			t.Fatal(f)
		}
	})
	t.Run("notes 2001 字", func(t *testing.T) {
		in := base()
		in.Notes = strPtr(strings.Repeat("n", 2001))
		f := expectValidation(t, validateVersionInput(&in))
		if f["notes"] == "" {
			t.Fatal(f)
		}
	})
	t.Run("权益 trim 与空 value 写回 true", func(t *testing.T) {
		in := base()
		in.Entitlements = []Entitlement{{Code: "  x  ", Value: nil}}
		if err := validateVersionInput(&in); err != nil {
			t.Fatal(err)
		}
		if in.Entitlements[0].Code != "x" || string(in.Entitlements[0].Value) != "true" {
			t.Fatalf("ent=%+v", in.Entitlements[0])
		}
	})
	t.Run("非法 JSON", func(t *testing.T) {
		in := base()
		in.Entitlements = []Entitlement{{Code: "a", Value: json.RawMessage(`{`)}}
		f := expectValidation(t, validateVersionInput(&in))
		if f["entitlements.0"] == "" {
			t.Fatal(f)
		}
	})
	t.Run("重复权益码", func(t *testing.T) {
		in := base()
		in.Entitlements = []Entitlement{
			{Code: "a", Value: json.RawMessage(`true`)},
			{Code: "a", Value: json.RawMessage(`true`)},
		}
		expectValidation(t, validateVersionInput(&in))
	})
	t.Run("配额 metric 周期重复", func(t *testing.T) {
		in := base()
		lim := int64(1)
		in.Quotas = []Quota{
			{Metric: "m", Unit: "u", Period: PeriodCycle, Limit: &lim},
			{Metric: "m", Unit: "u", Period: PeriodCycle, Limit: &lim},
		}
		f := expectValidation(t, validateVersionInput(&in))
		if f["quotas.1"] == "" {
			t.Fatal(f)
		}
	})
	t.Run("period 非法", func(t *testing.T) {
		in := base()
		lim := int64(1)
		in.Quotas = []Quota{{Metric: "m", Unit: "u", Period: "bad", Limit: &lim}}
		f := expectValidation(t, validateVersionInput(&in))
		if f["quotas.0.period"] == "" {
			t.Fatal(f)
		}
	})
	t.Run("limit -1", func(t *testing.T) {
		in := base()
		lim := int64(-1)
		in.Quotas = []Quota{{Metric: "m", Unit: "u", Period: PeriodCycle, Limit: &lim}}
		f := expectValidation(t, validateVersionInput(&in))
		if f["quotas.0.limit"] == "" {
			t.Fatal(f)
		}
	})
}

func TestValidatePriceInput(t *testing.T) {
	valid := PriceInput{
		Currency: "CNY", UnitAmount: decimal.New(1, 0),
		BillingInterval: "month", IntervalCount: 1,
	}
	t.Run("EUR", func(t *testing.T) {
		in := valid
		in.Currency = "EUR"
		f := expectValidation(t, validatePriceInput(&in))
		if f["currency"] == "" {
			t.Fatal(f)
		}
	})
	t.Run("小写 cny 规整为 CNY", func(t *testing.T) {
		in := valid
		in.Currency = "cny"
		if err := validatePriceInput(&in); err != nil {
			t.Fatal(err)
		}
		if in.Currency != string(money.CNY) {
			t.Fatalf("got %q", in.Currency)
		}
	})
	t.Run("负数", func(t *testing.T) {
		in := valid
		in.UnitAmount = decimal.New(-1, 0)
		f := expectValidation(t, validatePriceInput(&in))
		if f["unit_amount"] == "" {
			t.Fatal(f)
		}
	})
	t.Run("三位小数", func(t *testing.T) {
		in := valid
		in.UnitAmount = decimal.RequireFromString("1.001")
		f := expectValidation(t, validatePriceInput(&in))
		if f["unit_amount"] != "最多两位小数" {
			t.Fatalf("fields=%v", f)
		}
	})
	t.Run("周期 fortnight", func(t *testing.T) {
		in := valid
		in.BillingInterval = "fortnight"
		f := expectValidation(t, validatePriceInput(&in))
		if f["billing_interval"] == "" {
			t.Fatal(f)
		}
	})
	t.Run("interval_count 0 与超 32767", func(t *testing.T) {
		in := valid
		in.IntervalCount = 0
		f := expectValidation(t, validatePriceInput(&in))
		if f["interval_count"] == "" {
			t.Fatal(f)
		}
		in.IntervalCount = 40000
		f = expectValidation(t, validatePriceInput(&in))
		if f["interval_count"] == "" {
			t.Fatal(f)
		}
	})
	t.Run("trial_days -1 与超 32767", func(t *testing.T) {
		in := valid
		in.TrialDays = -1
		f := expectValidation(t, validatePriceInput(&in))
		if f["trial_days"] == "" {
			t.Fatal(f)
		}
		in.TrialDays = 40000
		f = expectValidation(t, validatePriceInput(&in))
		if f["trial_days"] == "" {
			t.Fatal(f)
		}
	})
	t.Run("valid_until 不晚于 from", func(t *testing.T) {
		in := valid
		from := time.Date(2026, 6, 2, 0, 0, 0, 0, time.UTC)
		until := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
		in.ValidFrom = &from
		in.ValidUntil = &until
		f := expectValidation(t, validatePriceInput(&in))
		if f["valid_until"] == "" {
			t.Fatal(f)
		}
	})
}

func TestPreparePackInput(t *testing.T) {
	valid := func() PackInput {
		return PackInput{
			Name: "包", TrafficBytes: 1, Currency: "CNY",
			UnitAmount: decimal.New(1, 0),
		}
	}
	t.Run("名称去空白", func(t *testing.T) {
		in := valid()
		in.Name = "  名  "
		if err := preparePackInput(&in); err != nil {
			t.Fatal(err)
		}
		if in.Name != "名" {
			t.Fatalf("%q", in.Name)
		}
	})
	t.Run("61 字被拒", func(t *testing.T) {
		in := valid()
		in.Name = strings.Repeat("字", 61)
		f := expectValidation(t, preparePackInput(&in))
		if f["name"] == "" {
			t.Fatal(f)
		}
	})
	t.Run("traffic_bytes", func(t *testing.T) {
		in := valid()
		in.TrafficBytes = 0
		f := expectValidation(t, preparePackInput(&in))
		if f["traffic_bytes"] == "" {
			t.Fatal(f)
		}
		in = valid()
		in.TrafficBytes = 1 << 50
		if err := preparePackInput(&in); err != nil {
			t.Fatal(err)
		}
		in = valid()
		in.TrafficBytes = (1 << 50) + 1
		f = expectValidation(t, preparePackInput(&in))
		if f["traffic_bytes"] == "" {
			t.Fatal(f)
		}
	})
	t.Run("金额", func(t *testing.T) {
		in := valid()
		in.UnitAmount = decimal.Zero
		f := expectValidation(t, preparePackInput(&in))
		if f["unit_amount"] == "" {
			t.Fatal(f)
		}
		in = valid()
		in.UnitAmount = decimal.New(1_000_000, 0)
		if err := preparePackInput(&in); err != nil {
			t.Fatal(err)
		}
		in = valid()
		in.UnitAmount = decimal.RequireFromString("1000000.01")
		f = expectValidation(t, preparePackInput(&in))
		if f["unit_amount"] == "" {
			t.Fatal(f)
		}
	})
	t.Run("sort_order 超界", func(t *testing.T) {
		in := valid()
		in.SortOrder = 1_000_001
		f := expectValidation(t, preparePackInput(&in))
		if f["sort_order"] == "" {
			t.Fatal(f)
		}
		in.SortOrder = -1_000_001
		f = expectValidation(t, preparePackInput(&in))
		if f["sort_order"] == "" {
			t.Fatal(f)
		}
	})
}

func TestPriceInWindow(t *testing.T) {
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	future := now.Add(time.Hour)
	past := now.Add(-time.Hour)
	exact := now
	t.Run("未开始", func(t *testing.T) {
		p := Price{ValidFrom: &future}
		if priceInWindow(p, now) {
			t.Fatal("应 false")
		}
	})
	t.Run("已过期", func(t *testing.T) {
		p := Price{ValidUntil: &past}
		if priceInWindow(p, now) {
			t.Fatal("应 false")
		}
	})
	t.Run("ValidFrom 等于 now 有效", func(t *testing.T) {
		p := Price{ValidFrom: &exact}
		if !priceInWindow(p, now) {
			t.Fatal("应 true")
		}
	})
	t.Run("ValidUntil 等于 now 过期", func(t *testing.T) {
		p := Price{ValidUntil: &exact}
		if priceInWindow(p, now) {
			t.Fatal("应 false")
		}
	})
}

func TestPublishPriceCoverage(t *testing.T) {
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	g1 := uuid.MustParse("018e0000-0000-7000-8000-000000000001")
	g2 := uuid.MustParse("018e0000-0000-7000-8000-000000000002")
	pub := func() Price {
		return Price{Status: PriceActive, Currency: money.CNY, UnitAmount: decimal.New(1, 0)}
	}
	t.Run("public 有公开价", func(t *testing.T) {
		if !publishPriceCoverage(VisibilityPublic, nil, []Price{pub()}, now) {
			t.Fatal("应 true")
		}
	})
	t.Run("public 只有组价", func(t *testing.T) {
		p := pub()
		p.UserGroupID = &g1
		if publishPriceCoverage(VisibilityPublic, nil, []Price{p}, now) {
			t.Fatal("应 false")
		}
	})
	t.Run("group 两组都有组价", func(t *testing.T) {
		p1, p2 := pub(), pub()
		p1.UserGroupID = &g1
		p2.UserGroupID = &g2
		if !publishPriceCoverage(VisibilityGroup, []uuid.UUID{g1, g2}, []Price{p1, p2}, now) {
			t.Fatal("应 true")
		}
	})
	t.Run("group 只覆盖一组", func(t *testing.T) {
		p1 := pub()
		p1.UserGroupID = &g1
		if publishPriceCoverage(VisibilityGroup, []uuid.UUID{g1, g2}, []Price{p1}, now) {
			t.Fatal("应 false")
		}
	})
	t.Run("group 有公开价", func(t *testing.T) {
		if !publishPriceCoverage(VisibilityGroup, []uuid.UUID{g1}, []Price{pub()}, now) {
			t.Fatal("应 true")
		}
	})
	t.Run("归档过期非 CNYUSD 不算", func(t *testing.T) {
		arch := pub()
		arch.Status = PriceArchived
		exp := pub()
		past := now.Add(-time.Hour)
		exp.ValidUntil = &past
		eur2 := Price{Status: PriceActive, Currency: money.Currency("EUR"), UnitAmount: decimal.New(1, 0)}
		if publishPriceCoverage(VisibilityPublic, nil, []Price{arch, exp, eur2}, now) {
			t.Fatal("应 false")
		}
	})
	t.Run("group 无组且只有组价", func(t *testing.T) {
		p := pub()
		p.UserGroupID = &g1
		if publishPriceCoverage(VisibilityGroup, nil, []Price{p}, now) {
			t.Fatal("应 false")
		}
	})
}

func TestCanTransitions(t *testing.T) {
	planCases := []struct {
		from, to string
		want     bool
	}{
		{PlanDraft, PlanActive, true},
		{PlanDraft, PlanArchived, true},
		{PlanActive, PlanActive, true},
		{PlanActive, PlanArchived, true},
		{PlanArchived, PlanActive, false},
		{PlanDraft, PlanDraft, false},
	}
	for _, tc := range planCases {
		if CanTransitionPlan(tc.from, tc.to) != tc.want {
			t.Fatalf("plan %s->%s want %v", tc.from, tc.to, tc.want)
		}
	}
	priceCases := []struct {
		from, to string
		want     bool
	}{
		{PriceActive, PriceArchived, true},
		{PriceArchived, PriceActive, false},
		{PriceActive, PriceActive, false},
	}
	for _, tc := range priceCases {
		if CanTransitionPrice(tc.from, tc.to) != tc.want {
			t.Fatalf("price %s->%s", tc.from, tc.to)
		}
	}
}

func TestEncodeDecodeHighlights(t *testing.T) {
	s, err := encodeHighlights(nil)
	if err != nil {
		t.Fatal(err)
	}
	if s != "[]" {
		t.Fatalf("got %q", s)
	}
	out, err := decodeHighlights("")
	if err != nil {
		t.Fatal(err)
	}
	if out == nil || len(out) != 0 {
		t.Fatalf("%v", out)
	}
}
