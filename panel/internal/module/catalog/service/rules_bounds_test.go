package service

// 数值上界的纯规则单测：int 列超过 int4、向导流量超过 1 PiB 时回 422，不让库报 22003 变成 500。

import (
	"math"
	"testing"
)

func TestPlanIntUpperBounds(t *testing.T) {
	base := func() PlanInput {
		return PlanInput{Code: "ab", Name: "n", Visibility: VisibilityPublic}
	}
	big := math.MaxInt32 + 1
	t.Run("stock_total 超 int4", func(t *testing.T) {
		in := base()
		in.StockTotal = intPtr(big)
		if f := expectValidation(t, preparePlanInput(&in)); f["stock_total"] == "" {
			t.Fatal(f)
		}
	})
	t.Run("purchase_limit_per_user 超 int4", func(t *testing.T) {
		in := base()
		in.PurchaseLimitPerUser = intPtr(big)
		if f := expectValidation(t, preparePlanInput(&in)); f["purchase_limit_per_user"] == "" {
			t.Fatal(f)
		}
	})
	t.Run("sort_order 超 int4", func(t *testing.T) {
		in := base()
		in.SortOrder = big
		if f := expectValidation(t, preparePlanInput(&in)); f["sort_order"] == "" {
			t.Fatal(f)
		}
	})
	t.Run("int4 上界本身可以", func(t *testing.T) {
		in := base()
		in.StockTotal = intPtr(math.MaxInt32)
		in.SortOrder = math.MaxInt32
		if err := preparePlanInput(&in); err != nil {
			t.Fatal(err)
		}
	})
}

func TestVersionIntUpperBounds(t *testing.T) {
	big := math.MaxInt32 + 1
	cases := []struct {
		field string
		set   func(*VersionInput)
	}{
		{"max_devices", func(in *VersionInput) { in.MaxDevices = intPtr(big) }},
		{"max_concurrent", func(in *VersionInput) { in.MaxConcurrent = intPtr(big) }},
		{"device_release_hours", func(in *VersionInput) { in.DeviceReleaseHours = big }},
		{"throttle_kbps", func(in *VersionInput) { in.ThrottleKbps = intPtr(big) }},
	}
	for _, c := range cases {
		t.Run(c.field, func(t *testing.T) {
			in := VersionInput{QuotaResetStrategy: ResetBillingCycle}
			c.set(&in)
			if f := expectValidation(t, validateVersionInput(&in)); f[c.field] == "" {
				t.Fatal(f)
			}
		})
	}
}

func TestWizardTrafficGBUpperBound(t *testing.T) {
	plan := PlanInput{Code: "ab", Name: "n", Visibility: VisibilityPublic}
	t.Run("新建：超过 1 PiB", func(t *testing.T) {
		in := WizardCreate{Plan: plan, TrafficGB: int64Ptr(maxTrafficGB + 1)}
		if f := expectValidation(t, validateWizardCreate(&in)); f["traffic_gb"] == "" {
			t.Fatal(f)
		}
	})
	t.Run("新建：2^34 GB（乘出来会回绕成 0）", func(t *testing.T) {
		in := WizardCreate{Plan: plan, TrafficGB: int64Ptr(1 << 34)}
		if f := expectValidation(t, validateWizardCreate(&in)); f["traffic_gb"] == "" {
			t.Fatal(f)
		}
	})
	t.Run("新建：上界本身可以", func(t *testing.T) {
		in := WizardCreate{Plan: plan, TrafficGB: int64Ptr(maxTrafficGB)}
		if err := validateWizardCreate(&in); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("修改：超过 1 PiB", func(t *testing.T) {
		in := WizardUpdate{TrafficGB: int64Ptr(maxTrafficGB + 1)}
		if f := expectValidation(t, validateWizardQuotaEdits(in)); f["traffic_gb"] == "" {
			t.Fatal(f)
		}
	})
}
