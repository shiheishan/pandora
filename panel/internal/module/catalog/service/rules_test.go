package service

// rules_test.go 覆盖套餐新建校验里与类型、销售、用量、线路相关的纯规则，以及 convert.go 的
// trafficQuotas / sortPricesByTier；金额与数字上界在 rules_bounds_test.go。

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/pkg/errs"
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
func rulesIntPtr(n int) *int  { return &n }
func int64Ptr(n int64) *int64 { return &n }

// subCreate 是一份合法的订阅套餐新建输入（月付一档）。
func subCreate() PlanCreate {
	return PlanCreate{
		Kind:  KindSubscription,
		Name:  "套餐",
		Tiers: []TierInput{{BillingInterval: IntervalMonth, IntervalCount: 1, UnitAmount: decimal.New(15, 0)}},
	}
}

// oneTimeCreate 是一份合法的一次性套餐新建输入。
func oneTimeCreate() PlanCreate {
	return PlanCreate{
		Kind:    KindOneTime,
		Name:    "套餐",
		OneTime: &OneTimeInput{UnitAmount: decimal.New(9, 0)},
	}
}

func TestValidatePlanCreate(t *testing.T) {
	t.Run("订阅缺价格档", func(t *testing.T) {
		in := subCreate()
		in.Tiers = nil
		if f := expectValidation(t, validatePlanCreate(&in)); f["prices"] == "" {
			t.Fatalf("fields=%v", f)
		}
	})
	t.Run("订阅带一次性价格", func(t *testing.T) {
		in := subCreate()
		in.OneTime = &OneTimeInput{UnitAmount: decimal.New(1, 0)}
		if f := expectValidation(t, validatePlanCreate(&in)); f["unit_amount"] == "" {
			t.Fatalf("fields=%v", f)
		}
	})
	t.Run("一次性缺价格", func(t *testing.T) {
		in := oneTimeCreate()
		in.OneTime = nil
		if f := expectValidation(t, validatePlanCreate(&in)); f["unit_amount"] == "" {
			t.Fatalf("fields=%v", f)
		}
	})
	t.Run("一次性带周期价", func(t *testing.T) {
		in := oneTimeCreate()
		in.Tiers = []TierInput{{BillingInterval: IntervalMonth, IntervalCount: 1, UnitAmount: decimal.New(1, 0)}}
		if f := expectValidation(t, validatePlanCreate(&in)); f["prices"] == "" {
			t.Fatalf("fields=%v", f)
		}
	})
	t.Run("未知类型", func(t *testing.T) {
		in := subCreate()
		in.Kind = "wizard"
		if f := expectValidation(t, validatePlanCreate(&in)); f["kind"] == "" {
			t.Fatalf("fields=%v", f)
		}
	})
	t.Run("一次性开续费", func(t *testing.T) {
		in := oneTimeCreate()
		in.Sales.AllowRenewal = true
		if f := expectValidation(t, validatePlanCreate(&in)); f["allow_renewal"] == "" {
			t.Fatalf("fields=%v", f)
		}
	})
	nameCases := []struct {
		name string
		set  func(*PlanCreate)
	}{
		{"名称空白", func(in *PlanCreate) { in.Name = "   " }},
		{"名称超 120 字", func(in *PlanCreate) { in.Name = strings.Repeat("名", 121) }},
		{"名称含 NUL", func(in *PlanCreate) { in.Name = "名\x00字" }},
	}
	for _, tc := range nameCases {
		t.Run(tc.name, func(t *testing.T) {
			in := subCreate()
			tc.set(&in)
			if f := expectValidation(t, validatePlanCreate(&in)); f["name"] == "" {
				t.Fatalf("fields=%v", f)
			}
		})
	}
	t.Run("合法订阅原样通过", func(t *testing.T) {
		in := subCreate()
		if err := validatePlanCreate(&in); err != nil {
			t.Fatal(err)
		}
		if in.Name != "套餐" {
			t.Fatalf("名称不应被改：%q", in.Name)
		}
	})
	t.Run("说明全空白规整成 nil", func(t *testing.T) {
		in := subCreate()
		in.Description = strPtr("  \t ")
		if err := validatePlanCreate(&in); err != nil {
			t.Fatal(err)
		}
		if in.Description != nil {
			t.Fatalf("Description=%v", *in.Description)
		}
	})
	t.Run("说明去首尾空白", func(t *testing.T) {
		in := subCreate()
		in.Description = strPtr("  说明  ")
		if err := validatePlanCreate(&in); err != nil {
			t.Fatal(err)
		}
		if in.Description == nil || *in.Description != "说明" {
			t.Fatalf("Description=%v", in.Description)
		}
	})
}

func TestValidateSales(t *testing.T) {
	fields := func(in SalesInput) map[string]string {
		f := map[string]string{}
		validateSales(KindSubscription, &in, f)
		return f
	}
	t.Run("空 visibility 规整成 public", func(t *testing.T) {
		in := SalesInput{}
		if f := fields(in); len(f) != 0 {
			t.Fatalf("fields=%v", f)
		}
		if in.Visibility != VisibilityPublic {
			t.Fatalf("Visibility=%q", in.Visibility)
		}
	})
	t.Run("未知 visibility", func(t *testing.T) {
		if f := fields(SalesInput{Visibility: "secret"}); f["visibility"] == "" {
			t.Fatalf("fields=%v", f)
		}
	})
	t.Run("四种可见性都合法", func(t *testing.T) {
		for _, v := range []string{VisibilityPublic, VisibilityAuthenticated, VisibilityGroup, VisibilityHidden} {
			in := SalesInput{Visibility: v, VisibleGroupIDs: []uuid.UUID{}}
			if v == VisibilityGroup {
				in.VisibleGroupIDs = []uuid.UUID{uuid.New()}
			}
			if f := fields(in); len(f) != 0 {
				t.Fatalf("visibility=%s fields=%v", v, f)
			}
		}
	})
	t.Run("group 没带组", func(t *testing.T) {
		in := SalesInput{Visibility: VisibilityGroup}
		if f := fields(in); f["visible_group_ids"] == "" {
			t.Fatalf("fields=%v", f)
		}
	})
	t.Run("非 group 带组", func(t *testing.T) {
		in := SalesInput{Visibility: VisibilityPublic, VisibleGroupIDs: []uuid.UUID{uuid.New()}}
		if f := fields(in); f["visible_group_ids"] == "" {
			t.Fatalf("fields=%v", f)
		}
	})
	t.Run("组里有 Nil", func(t *testing.T) {
		in := SalesInput{Visibility: VisibilityGroup, VisibleGroupIDs: []uuid.UUID{uuid.Nil}}
		if f := fields(in); f["visible_group_ids"] == "" {
			t.Fatalf("fields=%v", f)
		}
	})
	t.Run("组重复", func(t *testing.T) {
		id := uuid.New()
		in := SalesInput{Visibility: VisibilityGroup, VisibleGroupIDs: []uuid.UUID{id, id}}
		if f := fields(in); f["visible_group_ids"] == "" {
			t.Fatalf("fields=%v", f)
		}
	})
	t.Run("VisibleGroupIDs 为 nil 规整成空切片", func(t *testing.T) {
		in := SalesInput{Visibility: VisibilityPublic}
		if f := fields(in); len(f) != 0 {
			t.Fatalf("fields=%v", f)
		}
		if in.VisibleGroupIDs == nil || len(in.VisibleGroupIDs) != 0 {
			t.Fatalf("VisibleGroupIDs=%v", in.VisibleGroupIDs)
		}
	})
	t.Run("一次性开续费", func(t *testing.T) {
		f := map[string]string{}
		in := SalesInput{Visibility: VisibilityPublic, AllowRenewal: true}
		validateSales(KindOneTime, &in, f)
		if f["allow_renewal"] == "" {
			t.Fatalf("fields=%v", f)
		}
	})
	t.Run("订阅开续费合法", func(t *testing.T) {
		f := map[string]string{}
		in := SalesInput{Visibility: VisibilityPublic, AllowRenewal: true}
		validateSales(KindSubscription, &in, f)
		if len(f) != 0 {
			t.Fatalf("fields=%v", f)
		}
	})
}

func TestValidateTierChanges(t *testing.T) {
	amount := decimal.New(15, 0)
	t.Run("空列表", func(t *testing.T) {
		f := map[string]string{}
		validateTierChanges(nil, f)
		if f["prices"] == "" {
			t.Fatalf("fields=%v", f)
		}
	})
	t.Run("金额为 nil 合法（不卖这一档）", func(t *testing.T) {
		f := map[string]string{}
		validateTierChanges([]TierChange{{BillingInterval: IntervalMonth, IntervalCount: 1}}, f)
		if len(f) != 0 {
			t.Fatalf("fields=%v", f)
		}
	})
	t.Run("带金额且合法", func(t *testing.T) {
		f := map[string]string{}
		validateTierChanges([]TierChange{{BillingInterval: IntervalYear, IntervalCount: 1, UnitAmount: &amount}}, f)
		if len(f) != 0 {
			t.Fatalf("fields=%v", f)
		}
	})
	t.Run("周期不在四档", func(t *testing.T) {
		f := map[string]string{}
		validateTierChanges([]TierChange{{BillingInterval: IntervalMonth, IntervalCount: 2}}, f)
		if f["prices.0"] == "" {
			t.Fatalf("fields=%v", f)
		}
	})
	t.Run("同档重复", func(t *testing.T) {
		f := map[string]string{}
		validateTierChanges([]TierChange{
			{BillingInterval: IntervalMonth, IntervalCount: 1},
			{BillingInterval: IntervalMonth, IntervalCount: 1, UnitAmount: &amount},
		}, f)
		if f["prices.1"] == "" {
			t.Fatalf("fields=%v", f)
		}
	})
}

func TestValidatePoolIDs(t *testing.T) {
	ids := func(n int) []uuid.UUID {
		out := make([]uuid.UUID, n)
		for i := range out {
			out[i] = uuid.New()
		}
		return out
	}
	t.Run("500 个合法", func(t *testing.T) {
		f := map[string]string{}
		validatePoolIDs(ids(maxPlanPools), f)
		if len(f) != 0 {
			t.Fatalf("fields=%v", f)
		}
	})
	t.Run("501 个", func(t *testing.T) {
		f := map[string]string{}
		validatePoolIDs(ids(maxPlanPools+1), f)
		if f["pool_ids"] == "" {
			t.Fatalf("fields=%v", f)
		}
	})
	t.Run("含 uuid.Nil", func(t *testing.T) {
		f := map[string]string{}
		validatePoolIDs([]uuid.UUID{uuid.New(), uuid.Nil}, f)
		if f["pool_ids"] == "" {
			t.Fatalf("fields=%v", f)
		}
	})
	t.Run("重复", func(t *testing.T) {
		id := uuid.New()
		f := map[string]string{}
		validatePoolIDs([]uuid.UUID{id, id}, f)
		if f["pool_ids"] == "" {
			t.Fatalf("fields=%v", f)
		}
	})
	t.Run("空列表合法", func(t *testing.T) {
		f := map[string]string{}
		validatePoolIDs(nil, f)
		if len(f) != 0 {
			t.Fatalf("fields=%v", f)
		}
	})
}

func TestValidateUsage(t *testing.T) {
	fields := func(mut func(*UsageInput)) map[string]string {
		in := UsageInput{}
		mut(&in)
		f := map[string]string{}
		validateUsage(in, f)
		return f
	}
	t.Run("全空不限合法", func(t *testing.T) {
		if f := fields(func(*UsageInput) {}); len(f) != 0 {
			t.Fatalf("fields=%v", f)
		}
	})
	trafficCases := []struct {
		name string
		set  func(*UsageInput)
		want bool // true = 非法
	}{
		{"流量 0", func(in *UsageInput) { in.TrafficBytes = int64Ptr(0) }, true},
		{"流量负数", func(in *UsageInput) { in.TrafficBytes = int64Ptr(-1) }, true},
		{"流量超 1 PiB", func(in *UsageInput) { in.TrafficBytes = int64Ptr(maxTrafficBytes + 1) }, true},
		{"流量 1 PiB 合法", func(in *UsageInput) { in.TrafficBytes = int64Ptr(maxTrafficBytes) }, false},
		{"设备 0", func(in *UsageInput) { in.MaxDevices = rulesIntPtr(0) }, true},
		{"设备负数", func(in *UsageInput) { in.MaxDevices = rulesIntPtr(-1) }, true},
		{"设备超 int4", func(in *UsageInput) { in.MaxDevices = rulesIntPtr(maxInt4 + 1) }, true},
		{"设备 int4 合法", func(in *UsageInput) { in.MaxDevices = rulesIntPtr(maxInt4) }, false},
		{"限速 0", func(in *UsageInput) { in.ThrottleKbps = rulesIntPtr(0) }, true},
		{"限速负数", func(in *UsageInput) { in.ThrottleKbps = rulesIntPtr(-1) }, true},
		{"限速超 int4", func(in *UsageInput) { in.ThrottleKbps = rulesIntPtr(maxInt4 + 1) }, true},
		{"限速 int4 合法", func(in *UsageInput) { in.ThrottleKbps = rulesIntPtr(maxInt4) }, false},
	}
	for _, tc := range trafficCases {
		t.Run(tc.name, func(t *testing.T) {
			f := fields(tc.set)
			illegal := len(f) != 0
			if illegal != tc.want {
				t.Fatalf("fields=%v", f)
			}
		})
	}
}

func TestTrafficQuotas(t *testing.T) {
	bytes := int64(1 << 30)
	t.Run("nil 字节没有配额行", func(t *testing.T) {
		out := trafficQuotas(KindSubscription, nil)
		if out == nil || len(out) != 0 {
			t.Fatalf("out=%v", out)
		}
	})
	t.Run("订阅按周期", func(t *testing.T) {
		out := trafficQuotas(KindSubscription, &bytes)
		if len(out) != 1 {
			t.Fatalf("out=%v", out)
		}
		q := out[0]
		if q.Metric != MetricTraffic || q.Unit != UnitBytes || q.Period != PeriodCycle {
			t.Fatalf("quota=%+v", q)
		}
		if q.Limit == nil || *q.Limit != bytes {
			t.Fatalf("limit=%v", q.Limit)
		}
	})
	t.Run("一次性是总量", func(t *testing.T) {
		out := trafficQuotas(KindOneTime, &bytes)
		if len(out) != 1 || out[0].Period != PeriodTotal {
			t.Fatalf("out=%v", out)
		}
	})
}

func TestSortPricesByTier(t *testing.T) {
	id := func(n int) uuid.UUID {
		return uuid.MustParse(fmt.Sprintf("018e0000-0000-7000-8000-%012d", n))
	}
	prices := []Price{
		{ID: id(4), UnitAmount: decimal.New(4, 0), BillingInterval: IntervalYear, IntervalCount: 1},
		{ID: id(2), UnitAmount: decimal.New(3, 0), BillingInterval: IntervalMonth, IntervalCount: 3},
		{ID: id(3), UnitAmount: decimal.New(2, 0), BillingInterval: IntervalMonth, IntervalCount: 6},
		{ID: id(1), UnitAmount: decimal.New(1, 0), BillingInterval: IntervalMonth, IntervalCount: 1},
	}
	sortPricesByTier(prices)
	want := []string{"1", "3", "2", "4"}
	for i, w := range want {
		if got := prices[i].UnitAmount.String(); got != w {
			t.Fatalf("第 %d 位金额=%s，期望 %s", i, got, w)
		}
	}
	t.Run("一次性价排最后", func(t *testing.T) {
		ot := Price{UnitAmount: decimal.New(9, 0), BillingInterval: IntervalOneTime, IntervalCount: 1}
		ps := []Price{ot, {UnitAmount: decimal.New(1, 0), BillingInterval: IntervalMonth, IntervalCount: 1}}
		sortPricesByTier(ps)
		if ps[0].BillingInterval != IntervalMonth || ps[1].BillingInterval != IntervalOneTime {
			t.Fatalf("顺序=%v", ps)
		}
	})
}
