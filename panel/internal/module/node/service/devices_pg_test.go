package service

// 设备数限制：在线总览、计数、strict 名单与 RecheckStrictDevices（PG）。

import (
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"

	settingssvc "github.com/aegispanel/aegis/internal/module/settings/service"
)

func findOnlineDevice(devs []OnlineDevice, subID uuid.UUID) (OnlineDevice, bool) {
	for _, d := range devs {
		if d.SubscriptionID == subID {
			return d, true
		}
	}
	return OnlineDevice{}, false
}

func TestOnlineDevicesOverviewPG(t *testing.T) {
	e, ctx := newDevicesEnv(t)
	e.clearAlive(t, ctx)
	f := seedDevices(t, ctx, e)

	e.alive(t, ctx, f.n1, f.A.ID, "d1", 30*time.Second)
	e.alive(t, ctx, f.n1, f.A.ID, "d2", 30*time.Second)
	e.alive(t, ctx, f.n2, f.A.ID, "d1", 30*time.Second)
	e.alive(t, ctx, f.n2, f.A.ID, "d3", 30*time.Second)
	e.alive(t, ctx, f.n1, f.B.ID, "b1", 10*time.Minute)
	e.alive(t, ctx, f.n1, f.C.ID, "d9", 30*time.Second)

	ov, err := e.svc.OnlineDevices(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if ov.Mode != settingssvc.DeviceModeLoose || ov.Grace != 1 || ov.WindowMinutes != 5 {
		t.Fatalf("策略 %+v", ov)
	}
	if len(ov.Devices) != 2 {
		t.Fatalf("应只有 A、C，得 %d 行", len(ov.Devices))
	}
	if ov.Devices[0].SubscriptionID != f.A.ID {
		t.Fatalf("A 应排第一，得 %v", ov.Devices[0].SubscriptionID)
	}
	da, ok := findOnlineDevice(ov.Devices, f.A.ID)
	if !ok {
		t.Fatal("缺 A")
	}
	if da.Online != 3 || da.Nodes != 2 || da.Limit != 1 || da.Overridden || !da.Exceeded || da.Email != f.userEmail || da.Plan != "测试套餐" || da.LastSeenAt.IsZero() {
		t.Fatalf("A 行 %+v", da)
	}
	dc, ok := findOnlineDevice(ov.Devices, f.C.ID)
	if !ok {
		t.Fatal("缺 C")
	}
	if dc.Limit != 0 || !dc.Overridden || dc.Exceeded || dc.Online != 1 {
		t.Fatalf("C 行 %+v", dc)
	}

	e.setPolicy(t, ctx, "loose", 2, 5)
	ov, err = e.svc.OnlineDevices(ctx)
	if err != nil {
		t.Fatal(err)
	}
	da, _ = findOnlineDevice(ov.Devices, f.A.ID)
	if da.Exceeded {
		t.Fatalf("宽容 2 时 A 不应超限: %+v", da)
	}
}

// 在线设备总览只统计生效中（active）的订阅：刚到期的订阅即使窗口内还有在线记录也不出现。
func TestOnlineDevicesSkipsExpiredPG(t *testing.T) {
	e, ctx := newDevicesEnv(t)
	e.clearAlive(t, ctx)
	f := seedDevices(t, ctx, e)
	expired := e.dSub(t, ctx, f.user, f.v, subOpts{Expired: true})
	e.alive(t, ctx, f.n1, f.A.ID, "a1", 30*time.Second)
	e.alive(t, ctx, f.n1, expired.ID, "x1", 30*time.Second)
	e.alive(t, ctx, f.n2, expired.ID, "x2", 30*time.Second)

	ov, err := e.svc.OnlineDevices(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := findOnlineDevice(ov.Devices, f.A.ID); !ok {
		t.Fatal("active 订阅 A 应在总览里")
	}
	if _, ok := findOnlineDevice(ov.Devices, expired.ID); ok {
		t.Fatal("已到期的订阅不应出现在总览里")
	}
	if len(ov.Devices) != 1 {
		t.Fatalf("应只有 A 一行，得 %d", len(ov.Devices))
	}
}

func TestOnlineDevicesWindowFollowsSettingPG(t *testing.T) {
	e, ctx := newDevicesEnv(t)
	e.clearAlive(t, ctx)
	f := seedDevices(t, ctx, e)
	e.alive(t, ctx, f.n1, f.A.ID, "d1", 7*time.Minute)

	ov, err := e.svc.OnlineDevices(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := findOnlineDevice(ov.Devices, f.A.ID); ok {
		t.Fatal("5 分钟窗口不应含 A")
	}
	counts, err := e.svc.OnlineDeviceCounts(ctx, []uuid.UUID{f.A.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := counts[f.A.ID]; ok || len(counts) != 0 {
		t.Fatalf("计数不应含 A: %v", counts)
	}
	stats, err := e.svc.NodeLiveStats(ctx, []uuid.UUID{f.n1})
	if err != nil {
		t.Fatal(err)
	}
	if st := stats[f.n1]; st.OnlineUsers != 0 {
		t.Fatalf("节点在线用户 %d", st.OnlineUsers)
	}

	e.setPolicy(t, ctx, "loose", 1, 10)
	ov, err = e.svc.OnlineDevices(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if ov.WindowMinutes != 10 {
		t.Fatalf("窗口 %d", ov.WindowMinutes)
	}
	if _, ok := findOnlineDevice(ov.Devices, f.A.ID); !ok {
		t.Fatal("10 分钟窗口应含 A")
	}
	counts, err = e.svc.OnlineDeviceCounts(ctx, []uuid.UUID{f.A.ID})
	if err != nil || counts[f.A.ID] != 1 {
		t.Fatalf("计数 %v err=%v", counts, err)
	}
	stats, err = e.svc.NodeLiveStats(ctx, []uuid.UUID{f.n1})
	if err != nil {
		t.Fatal(err)
	}
	if stats[f.n1].OnlineUsers != 1 {
		t.Fatalf("节点在线用户 %d", stats[f.n1].OnlineUsers)
	}
}

func TestOnlineDevicesCapPG(t *testing.T) {
	e, ctx := newDevicesEnv(t)
	e.clearAlive(t, ctx)
	f := seedDevices(t, ctx, e)
	for i := 0; i < 201; i++ {
		s := e.dSub(t, ctx, f.user, f.v, subOpts{})
		e.alive(t, ctx, f.n1, s.ID, fmt.Sprintf("cap-%d", i), 20*time.Second)
	}

	ov, err := e.svc.OnlineDevices(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(ov.Devices) != 200 {
		t.Fatalf("上限 200，得 %d", len(ov.Devices))
	}
}

func TestOnlineDeviceCountsPG(t *testing.T) {
	e, ctx := newDevicesEnv(t)
	e.clearAlive(t, ctx)
	f := seedDevices(t, ctx, e)
	e.alive(t, ctx, f.n1, f.A.ID, "d1", 30*time.Second)
	e.alive(t, ctx, f.n2, f.A.ID, "d1", 30*time.Second)
	e.alive(t, ctx, f.n1, f.A.ID, "d2", 30*time.Second)

	counts, err := e.svc.OnlineDeviceCounts(ctx, []uuid.UUID{f.A.ID, f.B.ID})
	if err != nil {
		t.Fatal(err)
	}
	if counts[f.A.ID] != 2 {
		t.Fatalf("A 计数 %d", counts[f.A.ID])
	}
	if _, ok := counts[f.B.ID]; ok {
		t.Fatal("B 不应在结果里")
	}
	empty, err := e.svc.OnlineDeviceCounts(ctx, nil)
	if err != nil || len(empty) != 0 {
		t.Fatalf("空入参 %v %v", empty, err)
	}
}

func TestStrictRosterExcludesPG(t *testing.T) {
	e, ctx := newDevicesEnv(t)
	e.clearAlive(t, ctx)
	f := seedDevices(t, ctx, e)
	e.alive(t, ctx, f.n1, f.A.ID, "d1", 30*time.Second)
	e.alive(t, ctx, f.n1, f.A.ID, "d2", 30*time.Second)
	e.alive(t, ctx, f.n2, f.A.ID, "d3", 30*time.Second)
	e.alive(t, ctx, f.n1, f.B.ID, "b1", 30*time.Second)
	for i := 0; i < 5; i++ {
		e.alive(t, ctx, f.n1, f.C.ID, fmt.Sprintf("c%d", i), 30*time.Second)
	}

	assertRoster := func(wantA, wantB bool) {
		rosters, err := e.svc.Rosters(ctx, []uuid.UUID{f.srv})
		if err != nil {
			t.Fatal(err)
		}
		for _, node := range []uuid.UUID{f.n1, f.n2} {
			r, ok := rosterOf(rosters, f.srv, node)
			if !ok {
				t.Fatalf("缺节点 %s", node)
			}
			_, hasA := hasUID(r, f.A.UID)
			_, hasB := hasUID(r, f.B.UID)
			_, hasC := hasUID(r, f.C.UID)
			if hasA != wantA || hasB != wantB || !hasC {
				t.Fatalf("节点 %s: A=%v B=%v C=%v，期望 A=%v B=%v C=true", node, hasA, hasB, hasC, wantA, wantB)
			}
			if wantB {
				u, _ := hasUID(r, f.B.UID)
				if u.DeviceLimit != 1 {
					t.Fatalf("B 设备上限 %+v", u)
				}
			}
		}
	}

	recheck := func() {
		t.Helper()
		if err := e.svc.RecheckStrictDevices(ctx); err != nil {
			t.Fatal(err)
		}
	}
	assertRoster(true, true)
	e.setPolicy(t, ctx, "strict", 1, 5)
	// 名单只读周期任务写下的集合：任务还没跑时谁都不摘
	assertRoster(true, true)
	recheck()
	assertRoster(false, true)
	e.setPolicy(t, ctx, "strict", 2, 5)
	recheck()
	assertRoster(true, true)
}

// 审查 H1：被摘的订阅在线数回落后，任务必须看出集合变了、推进纪元，名单把人放回来。
func TestStrictRosterRestoresAfterDropPG(t *testing.T) {
	e, ctx := newDevicesEnv(t)
	e.clearAlive(t, ctx)
	f := seedDevices(t, ctx, e)
	e.setPolicy(t, ctx, "strict", 1, 5)
	e.alive(t, ctx, f.n1, f.A.ID, "d1", 30*time.Second)
	e.alive(t, ctx, f.n1, f.A.ID, "d2", 30*time.Second)
	e.alive(t, ctx, f.n2, f.A.ID, "d3", 30*time.Second)
	if err := e.svc.RecheckStrictDevices(ctx); err != nil {
		t.Fatal(err)
	}
	before := e.epoch(t)
	inRoster := func() bool {
		rosters, err := e.svc.Rosters(ctx, []uuid.UUID{f.srv})
		if err != nil {
			t.Fatal(err)
		}
		r, ok := rosterOf(rosters, f.srv, f.n1)
		if !ok {
			t.Fatal("缺节点 n1")
		}
		_, has := hasUID(r, f.A.UID)
		return has
	}
	if inRoster() {
		t.Fatal("A 超限应被摘")
	}
	// 最早那台设备滑出窗口：A 回到 上限+宽容 以内
	e.clearAlive(t, ctx)
	e.alive(t, ctx, f.n1, f.A.ID, "d1", 30*time.Second)
	e.alive(t, ctx, f.n2, f.A.ID, "d3", 30*time.Second)
	if err := e.svc.RecheckStrictDevices(ctx); err != nil {
		t.Fatal(err)
	}
	if e.epoch(t) != before+1 {
		t.Fatalf("集合变空应推进纪元：前 %d 后 %d", before, e.epoch(t))
	}
	if !inRoster() {
		t.Fatal("A 回落后应放回名单")
	}
}

func TestRecheckStrictDevicesPG(t *testing.T) {
	e, ctx := newDevicesEnv(t)
	e.clearAlive(t, ctx)
	f := seedDevices(t, ctx, e)
	e.setPolicy(t, ctx, "strict", 0, 5)
	e.alive(t, ctx, f.n1, f.A.ID, "d1", 30*time.Second)
	e.alive(t, ctx, f.n1, f.A.ID, "d2", 30*time.Second)

	if err := e.svc.RecheckStrictDevices(ctx); err != nil {
		t.Fatal(err)
	}
	if e.epoch(t) != 1 {
		t.Fatalf("第一次纪元 %d", e.epoch(t))
	}
	if !e.mr.Exists(StrictDigestKey) || !e.mr.Exists(StrictSetKey) {
		t.Fatal("应有 strict 集合与摘要键")
	}
	if err := e.svc.RecheckStrictDevices(ctx); err != nil {
		t.Fatal(err)
	}
	if e.epoch(t) != 1 {
		t.Fatalf("第二次纪元应仍为 1，得 %d", e.epoch(t))
	}

	e.alive(t, ctx, f.n1, f.B.ID, "b1", 30*time.Second)
	e.alive(t, ctx, f.n1, f.B.ID, "b2", 30*time.Second)
	if err := e.svc.RecheckStrictDevices(ctx); err != nil {
		t.Fatal(err)
	}
	if e.epoch(t) != 2 {
		t.Fatalf("第三次纪元 %d", e.epoch(t))
	}

	e.setPolicy(t, ctx, "loose", 0, 5)
	if err := e.svc.RecheckStrictDevices(ctx); err != nil {
		t.Fatal(err)
	}
	if e.epoch(t) != 2 {
		t.Fatalf("loose 后纪元应仍为 2，得 %d", e.epoch(t))
	}
	if e.mr.Exists(StrictDigestKey) || e.mr.Exists(StrictSetKey) {
		t.Fatal("loose 后集合与摘要键应删除")
	}
}
