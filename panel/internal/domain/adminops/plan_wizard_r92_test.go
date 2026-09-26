package adminops

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// R92 ①：*int 分不出缺省与 null，向导改不回「不限设备」。
func TestUpdatePlanCompleteInputTriState(t *testing.T) {
	for body, want := range map[string]struct{ set, null bool }{
		`{}`:                   {false, false},
		`{"max_devices":null}`: {true, true},
		`{"max_devices":5}`:    {true, false},
	} {
		var in UpdatePlanCompleteInput
		if err := json.Unmarshal([]byte(body), &in); err != nil {
			t.Fatal(err)
		}
		if in.MaxDevices.Set != want.set || (in.MaxDevices.Value == nil) != (want.null || !want.set) {
			t.Fatalf("%s decoded as %+v", body, in.MaxDevices)
		}
	}
	var in UpdatePlanCompleteInput
	if err := json.Unmarshal([]byte(`{"throttle_kbps":"fast"}`), &in); err == nil {
		t.Fatal("non-integer throttle accepted")
	}
	cur := 3
	if got := (OptionalInt{}).resolve(&cur); got != &cur {
		t.Fatal("omitted must keep current")
	}
	if got := (OptionalInt{Set: true}).resolve(&cur); got != nil {
		t.Fatal("null must clear")
	}
}

// R92 ②：新版本以当前版本为底稿，只换向导改了的额度。
func TestInheritVersionSemantics(t *testing.T) {
	day, conc, devices, throttle := int16(5), 2, 3, 2000
	note := "keep me"
	partialGB := int64(100*bytesPerGB + 7)
	cur := &VersionRow{
		QuotaResetStrategy: "fixed_day", QuotaResetDay: &day, GracePeriodHours: 48,
		GraceKeepsService: false, RenewalExtendsPeriod: false, RenewalResetsQuota: false, RenewalKeepsAddons: false,
		MaxDevices: &devices, MaxConcurrent: &conc, DeviceReleaseHours: 12,
		OveragePolicy: "throttle", ThrottleKbps: &throttle, Notes: &note,
		Entitlements: []EntitlementInput{{Code: "support.priority", Value: json.RawMessage(`true`)}},
		Quotas: []QuotaInput{
			{Metric: "traffic.bytes", Limit: &partialGB, Unit: "bytes", Period: "total"},
			{Metric: "devices.active", Limit: ptrInt64(3), Unit: "count", Period: "cycle"},
			{Metric: "requests.count", Limit: ptrInt64(1000), Unit: "count", Period: "day"},
		},
	}

	got := inheritVersionSemantics(cur, UpdatePlanCompleteInput{})
	if got.QuotaResetStrategy != "fixed_day" || got.QuotaResetDay == nil || *got.QuotaResetDay != 5 ||
		got.GracePeriodHours != 48 || got.GraceKeepsService || got.RenewalExtendsPeriod || got.RenewalResetsQuota ||
		got.RenewalKeepsAddons || got.MaxConcurrent != &conc || got.DeviceReleaseHours != 12 || got.Notes != &note ||
		len(got.Entitlements) != 1 || got.OveragePolicy != "suspend" || got.ThrottleKbps != &throttle || got.MaxDevices != &devices {
		t.Fatalf("not inherited: %+v", got)
	}
	// 流量没改：整行照抄，不经 GB 取整，周期也保留。
	if len(got.Quotas) != 3 || got.Quotas[0].Limit != &partialGB || got.Quotas[0].Period != "total" {
		t.Fatalf("quotas: %+v", got.Quotas)
	}
	if err := validateVersionSemantics(got); err != nil {
		t.Fatalf("inherited semantics rejected: %v", err)
	}

	gb := int64(0)
	got = inheritVersionSemantics(cur, UpdatePlanCompleteInput{TrafficGB: &gb, MaxDevices: OptionalInt{Set: true}})
	if got.MaxDevices != nil || len(got.Quotas) != 1 || got.Quotas[0].Metric != "requests.count" {
		t.Fatalf("unlimited traffic and devices: %+v", got.Quotas)
	}
	gb = 200
	got = inheritVersionSemantics(cur, UpdatePlanCompleteInput{TrafficGB: &gb})
	for _, q := range got.Quotas {
		if q.Metric == "traffic.bytes" && (*q.Limit != 200*bytesPerGB || q.Period != "total") {
			t.Fatalf("rewritten traffic row: %+v", q)
		}
	}

	// 没有当前版本：沿用向导原来的默认值。
	got = inheritVersionSemantics(nil, UpdatePlanCompleteInput{})
	if got.QuotaResetStrategy != "billing_cycle" || !got.GraceKeepsService || got.OveragePolicy != "suspend" || len(got.Quotas) != 0 {
		t.Fatalf("defaults: %+v", got)
	}
}

func TestQuotaDiffersTreatsMissingTrafficAsUnlimited(t *testing.T) {
	id := "v1"
	three := 3
	plan := &CatalogPlanDetail{CurrentVersionID: &id, Versions: []VersionRow{{ID: id, MaxDevices: &three}}}
	zero := int64(0)
	if quotaDiffers(plan, UpdatePlanCompleteInput{TrafficGB: &zero}) {
		t.Fatal("re-submitting unlimited traffic must not roll a version")
	}
	if quotaDiffers(plan, UpdatePlanCompleteInput{}) {
		t.Fatal("nothing submitted")
	}
	if !quotaDiffers(plan, UpdatePlanCompleteInput{MaxDevices: OptionalInt{Set: true}}) {
		t.Fatal("null devices on a limited plan is a change")
	}
	if quotaDiffers(plan, UpdatePlanCompleteInput{MaxDevices: OptionalInt{Set: true, Value: &three}}) {
		t.Fatal("same devices is not a change")
	}
}

// R99：限速与策略解耦，新写入的策略只收 suspend。
func TestValidateVersionSemanticsThrottleDecoupled(t *testing.T) {
	kbps, zero := 1000, 0
	base := VersionSemanticsInput{QuotaResetStrategy: "billing_cycle"}
	for _, policy := range []string{"", "suspend"} {
		in := base
		in.OveragePolicy, in.ThrottleKbps = policy, &kbps
		if err := validateVersionSemantics(in); err != nil {
			t.Fatalf("policy %q with throttle rejected: %v", policy, err)
		}
	}
	for _, tc := range []struct {
		policy string
		kbps   *int
		field  string
	}{{"throttle", &kbps, "overage_policy"}, {"metered_billing", nil, "overage_policy"}, {"suspend", &zero, "throttle_kbps"}} {
		in := base
		in.OveragePolicy, in.ThrottleKbps = tc.policy, tc.kbps
		var he *httpx.Error
		if err := validateVersionSemantics(in); !errors.As(err, &he) || he.Fields[tc.field] == "" {
			t.Fatalf("%+v: err=%v", tc, err)
		}
	}
}

func TestWizardVersionSemanticsZeroDevicesIsUnlimited(t *testing.T) {
	zero, kbps := 0, 500
	got := wizardVersionSemantics(CreatePlanCompleteInput{MaxDevices: &zero, ThrottleKbps: &kbps})
	if got.MaxDevices != nil || got.ThrottleKbps != &kbps || got.OveragePolicy != "suspend" {
		t.Fatalf("wizard semantics: %+v", got)
	}
	if err := validateVersionSemantics(got); err != nil {
		t.Fatalf("wizard semantics rejected: %v", err)
	}
}
