package service

import (
	"testing"

	"github.com/google/uuid"

	catalogsvc "github.com/aegispanel/aegis/internal/module/catalog/service"
	"github.com/aegispanel/aegis/internal/module/subscription/model"
)

func TestUsageOf(t *testing.T) {
	traffic := i64(100 * gib)
	v := catalogsvc.Version{
		MaxDevices:   intp(5),
		ThrottleKbps: intp(100),
		Quotas: []catalogsvc.Quota{
			{Metric: MetricTraffic, Period: PeriodCycle, Limit: traffic},
		},
	}
	u := usageOf(v, catalogsvc.KindSubscription, nil)
	if u.TrafficBytes == nil || *u.TrafficBytes != 100*gib {
		t.Fatalf("流量: %+v", u.TrafficBytes)
	}
	if u.DeviceLimit == nil || *u.DeviceLimit != 5 {
		t.Fatalf("设备: %+v", u.DeviceLimit)
	}
	if u.ThrottleKbps == nil || *u.ThrottleKbps != 100 {
		t.Fatalf("限速: %+v", u.ThrottleKbps)
	}

	override := intp(2)
	u2 := usageOf(v, catalogsvc.KindSubscription, override)
	if u2.DeviceLimit == nil || *u2.DeviceLimit != 2 {
		t.Fatalf("覆盖设备: %+v", u2.DeviceLimit)
	}

	// 一次性：版本里存 cycle，withKind 后按 total 取
	vOne := catalogsvc.Version{
		Quotas: []catalogsvc.Quota{
			{Metric: MetricTraffic, Period: PeriodCycle, Limit: i64(50 * gib)},
		},
	}
	u3 := usageOf(vOne, catalogsvc.KindOneTime, nil)
	if u3.TrafficBytes == nil || *u3.TrafficBytes != 50*gib {
		t.Fatalf("一次性流量: %+v", u3.TrafficBytes)
	}
}

func TestNextUsageOf(t *testing.T) {
	curVer := uuid.New()
	nextVer := uuid.New()
	planID := uuid.New()
	traffic100 := i64(100 * gib)
	traffic200 := i64(200 * gib)

	curV := catalogsvc.Version{
		ID:     curVer,
		PlanID: planID,
		Quotas: []catalogsvc.Quota{{Metric: MetricTraffic, Period: PeriodCycle, Limit: traffic100}},
	}
	nextV := catalogsvc.Version{
		ID:     nextVer,
		PlanID: planID,
		Quotas: []catalogsvc.Quota{{Metric: MetricTraffic, Period: PeriodCycle, Limit: traffic200}},
	}
	plan := catalogsvc.Plan{
		ID:               planID,
		Kind:             catalogsvc.KindSubscription,
		CurrentVersionID: &nextVer,
	}
	m := model.Subscription{PlanVersionID: curVer, PlanID: planID}
	cur := usageOf(curV, plan.Kind, nil)
	versions := map[uuid.UUID]catalogsvc.Version{curVer: curV, nextVer: nextV}

	cases := []struct {
		name        string
		plan        catalogsvc.Plan
		m           model.Subscription
		st          State
		cur         Usage
		vers        map[uuid.UUID]catalogsvc.Version
		wantNil     bool
		wantTraffic *int64
	}{
		{"同版本", plan, m, StateLive, cur, map[uuid.UUID]catalogsvc.Version{curVer: curV, nextVer: nextV}, true, nil},
		{"流量变 200GiB", plan, m, StateLive, cur, versions, false, traffic200},
		{"三项相同版本不同", plan, m, StateLive, usageOf(nextV, plan.Kind, nil), versions, true, nil},
		{"一次性", catalogsvc.Plan{ID: planID, Kind: catalogsvc.KindOneTime, CurrentVersionID: &nextVer}, m, StateLive, cur, versions, true, nil},
		{"已停用", plan, m, StateEnded, cur, versions, true, nil},
		{"当前版本不在 map", plan, m, StateLive, cur, map[uuid.UUID]catalogsvc.Version{curVer: curV}, true, nil},
	}
	// 同版本：CurrentVersionID 指向锁定版本
	planSame := plan
	planSame.CurrentVersionID = &curVer
	cases[0].plan = planSame

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := nextUsageOf(tc.m, tc.plan, tc.st, tc.cur, tc.vers)
			if tc.wantNil {
				if got != nil {
					t.Fatalf("应为 nil，得 %+v", got)
				}
				return
			}
			if got == nil || got.TrafficBytes == nil || tc.wantTraffic == nil || *got.TrafficBytes != *tc.wantTraffic {
				t.Fatalf("得 %+v", got)
			}
		})
	}

	// 只限速不同
	throttleCur := catalogsvc.Version{
		ID: curVer, PlanID: planID,
		ThrottleKbps: intp(100),
		Quotas:       []catalogsvc.Quota{{Metric: MetricTraffic, Period: PeriodCycle, Limit: traffic100}},
	}
	throttleNext := catalogsvc.Version{
		ID: nextVer, PlanID: planID,
		ThrottleKbps: intp(200),
		Quotas:       []catalogsvc.Quota{{Metric: MetricTraffic, Period: PeriodCycle, Limit: traffic100}},
	}
	planT := catalogsvc.Plan{ID: planID, Kind: catalogsvc.KindSubscription, CurrentVersionID: &nextVer}
	mT := model.Subscription{PlanVersionID: curVer, PlanID: planID}
	curT := usageOf(throttleCur, planT.Kind, nil)
	versT := map[uuid.UUID]catalogsvc.Version{curVer: throttleCur, nextVer: throttleNext}
	if got := nextUsageOf(mT, planT, StateLive, curT, versT); got == nil || got.ThrottleKbps == nil || *got.ThrottleKbps != 200 {
		t.Fatalf("限速不同: %+v", got)
	}

	// 设备覆盖：新版本改了设备数，两边都等于覆盖
	devOverride := intp(3)
	devCur := catalogsvc.Version{ID: curVer, PlanID: planID, MaxDevices: intp(3), Quotas: []catalogsvc.Quota{{Metric: MetricTraffic, Period: PeriodCycle, Limit: traffic100}}}
	devNext := catalogsvc.Version{ID: nextVer, PlanID: planID, MaxDevices: intp(10), Quotas: []catalogsvc.Quota{{Metric: MetricTraffic, Period: PeriodCycle, Limit: traffic100}}}
	mDev := model.Subscription{PlanVersionID: curVer, PlanID: planID, DeviceLimit: devOverride}
	curDev := usageOf(devCur, plan.Kind, devOverride)
	versDev := map[uuid.UUID]catalogsvc.Version{curVer: devCur, nextVer: devNext}
	if got := nextUsageOf(mDev, plan, StateLive, curDev, versDev); got != nil {
		t.Fatalf("覆盖优先应 nil，得 %+v", got)
	}
}
