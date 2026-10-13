package service

// IngestOnline、NodeLiveStats、RecordMetrics、NodeMetrics、LatestServerMetrics 的 PostgreSQL 集成测试。

import (
	"net/netip"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/pkg/errs"
)

func TestIngestOnlineFiltersAndLiveStatsPG(t *testing.T) {
	e, ctx := newTrafficEnv(t)
	f := newTrafficFixture(t, ctx, e)
	ghostUID := int64(1 << 40) // 名单内但查不到订阅
	f.members[trafficKey{f.n1, ghostUID}] = true
	ip := netip.MustParseAddr

	entries := []OnlineEntry{
		// 1.2.3.4 与它的 v4 映射写法同一设备；2001:db8::1 与 ::2 同一 /64
		{NodeID: f.n1, UID: f.uidA, IPs: []netip.Addr{ip("1.2.3.4"), ip("::ffff:1.2.3.4"), ip("2001:db8::1"), ip("2001:db8::2"), {}}},
		{NodeID: f.n2, UID: f.uidB, IPs: []netip.Addr{ip("5.6.7.8")}},    // 名单外
		{NodeID: f.nx, UID: f.uidA, IPs: []netip.Addr{ip("5.6.7.9")}},    // 别的服务器
		{NodeID: f.n1, UID: 0, IPs: []netip.Addr{ip("5.6.7.10")}},        // uid 非正
		{NodeID: f.n1, UID: ghostUID, IPs: []netip.Addr{ip("5.6.7.11")}}, // 无订阅
	}
	if err := e.svc.IngestOnline(ctx, f.server, entries, f.members); err != nil {
		t.Fatalf("IngestOnline: %v", err)
	}
	if n := e.count(t, ctx, `SELECT count(*) FROM node_alive_ips WHERE node_id IN (?, ?, ?)`, f.n1, f.n2, f.nx); n != 2 {
		t.Fatalf("应只记 (N1,A) 的 2 个设备键，得 %d 行", n)
	}
	if n := e.count(t, ctx, `SELECT count(*) FROM node_alive_ips WHERE node_id = ? AND subscription_id = ?`, f.n1, f.subA); n != 2 {
		t.Fatalf("(N1,A) 应 2 行，得 %d", n)
	}

	stats, err := e.svc.NodeLiveStats(ctx, []uuid.UUID{f.n1, f.n2, f.nx})
	if err != nil {
		t.Fatal(err)
	}
	if got := stats[f.n1]; got.OnlineUsers != 1 || got.OnlineIPs != 2 || got.TrafficBytes24h != 0 {
		t.Fatalf("N1 读数 %+v", got)
	}
	if _, ok := stats[f.n2]; ok {
		t.Fatal("N2 没有数据，不该在结果里")
	}

	// 窗口外：时钟拨到 OnlineWindow 之后，不再算在线
	base := e.svc.now()
	e.svc.now = func() time.Time { return base.Add(OnlineWindow + time.Second) }
	stats, err = e.svc.NodeLiveStats(ctx, []uuid.UUID{f.n1})
	if err != nil {
		t.Fatal(err)
	}
	if got := stats[f.n1]; got.OnlineUsers != 0 || got.OnlineIPs != 0 {
		t.Fatalf("窗口外不该在线：%+v", got)
	}
}

func TestIngestOnlineRefreshGranularityPG(t *testing.T) {
	e, ctx := newTrafficEnv(t)
	f := newTrafficFixture(t, ctx, e)
	base := e.svc.now()
	entries := []OnlineEntry{{NodeID: f.n1, UID: f.uidA, IPs: []netip.Addr{netip.MustParseAddr("1.2.3.4")}}}
	seen := func() time.Time {
		t.Helper()
		var at time.Time
		if err := e.row(ctx, `SELECT last_seen_at FROM node_alive_ips WHERE node_id = ? AND subscription_id = ?`, f.n1, f.subA).Scan(&at); err != nil {
			t.Fatal(err)
		}
		return at
	}
	ingestAt := func(at time.Time) {
		t.Helper()
		e.svc.now = func() time.Time { return at }
		if err := e.svc.IngestOnline(ctx, f.server, entries, f.members); err != nil {
			t.Fatal(err)
		}
	}

	ingestAt(base)
	if got := seen(); !got.Equal(base) {
		t.Fatalf("首次应记 %v，得 %v", base, got)
	}
	ingestAt(base.Add(time.Minute))
	if got := seen(); !got.Equal(base) {
		t.Fatalf("不到 %v 不刷新，得 %v", aliveRefresh, got)
	}
	later := base.Add(aliveRefresh + time.Second)
	ingestAt(later)
	if got := seen(); !got.Equal(later) {
		t.Fatalf("超过 %v 应刷新到 %v，得 %v", aliveRefresh, later, got)
	}
}

func TestNodeLiveStatsTraffic24hPG(t *testing.T) {
	e, ctx := newTrafficEnv(t)
	f := newTrafficFixture(t, ctx, e)
	// 时钟在 e.hour + 30 分，窗口起点是 e.hour - 23h30m
	for _, c := range []struct {
		hour     time.Time
		up, down int64
	}{
		{e.hour, 100, 200},
		{e.hour.Add(-23 * time.Hour), 10, 20},
		{e.hour.Add(-24 * time.Hour), 1000, 1000}, // 窗口外
	} {
		e.mustExec(t, ctx, `INSERT INTO node_traffic_hourly (node_id, hour_start, upload_bytes, download_bytes, billed_bytes)
			VALUES (?, ?, ?, ?, 0)`, f.n2, c.hour, c.up, c.down)
	}
	stats, err := e.svc.NodeLiveStats(ctx, []uuid.UUID{f.n2})
	if err != nil {
		t.Fatal(err)
	}
	if got := stats[f.n2]; got.TrafficBytes24h != 330 || got.OnlineUsers != 0 || got.OnlineIPs != 0 {
		t.Fatalf("N2 读数 %+v（应只有 24 小时流量 330）", got)
	}
}

func TestRecordAndReadNodeMetricsPG(t *testing.T) {
	e, ctx := newTrafficEnv(t)
	f := newTrafficFixture(t, ctx, e)
	now := e.svc.now()
	const mib, gib = 1 << 20, 1 << 30
	record := func(at time.Time, m HostMetrics) {
		t.Helper()
		if err := e.svc.RecordMetrics(ctx, f.server, m, at); err != nil {
			t.Fatalf("RecordMetrics %v: %v", at, err)
		}
	}
	record(now.Add(-2*time.Hour), HostMetrics{CPUBp: 100, MemTotalBytes: gib})
	record(now.Add(-2*time.Minute), HostMetrics{
		CPUBp: 12000, MemUsedBytes: 512 * mib, MemTotalBytes: gib, DiskUsedBytes: 10 * gib, DiskTotalBytes: 20 * gib,
		Load1Cp: 150, NetRxBytes: 1000, NetTxBytes: 2000, TCPConns: 7, UptimeSec: 99,
	})
	second := HostMetrics{
		CPUBp: 2500, MemUsedBytes: 256 * mib, MemTotalBytes: gib, DiskUsedBytes: 11 * gib, DiskTotalBytes: 20 * gib,
		Load1Cp: 50, Load5Cp: 40, Load15Cp: 30, NetRxBytes: 61000, NetTxBytes: 2000, TCPConns: 9, UptimeSec: 159,
	}
	record(now.Add(-time.Minute), second)
	dup := second
	dup.CPUBp = 9000
	record(now.Add(-time.Minute), dup) // 同一时刻重复：丢弃，不报错

	m, err := e.svc.NodeMetrics(ctx, f.n1, 0) // 0 按 60 分钟
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Points) != 2 {
		t.Fatalf("60 分钟内应 2 点，得 %d", len(m.Points))
	}
	p0, p1 := m.Points[0], m.Points[1]
	if p0.CPUPercent != 100 || p0.MemPercent != 50 || p0.Load1 != 1.5 || p0.RxSpeed != 0 {
		t.Fatalf("第一点 %+v（CPU 应封顶 100%%）", p0)
	}
	if p1.CPUPercent != 25 || p1.RxSpeed != 1000 || p1.TxSpeed != 0 || p1.TCPConns != 9 {
		t.Fatalf("第二点 %+v", p1)
	}
	l := m.Latest
	if l == nil || l.CPUPercent != 25 || l.MemUsedMB != 256 || l.MemTotalMB != 1024 || l.DiskUsedGB != 11 || l.DiskTotalGB != 20 ||
		l.Load5 != 0.4 || l.UptimeSec != 159 || l.RxTotal != 61000 {
		t.Fatalf("最近一点 %+v", l)
	}

	m, err = e.svc.NodeMetrics(ctx, f.n2, 180) // 同一服务器的另一个节点，窗口 3 小时
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Points) != 3 {
		t.Fatalf("180 分钟内应 3 点，得 %d", len(m.Points))
	}
	if m.Points[1].RxSpeed != 0 {
		t.Fatal("与上一点相隔超过 maxRateGap，不该算速率")
	}

	m, err = e.svc.NodeMetrics(ctx, f.nx, 60) // 别的服务器没有点
	if err != nil || len(m.Points) != 0 || m.Latest != nil {
		t.Fatalf("没有点应空：%+v, %v", m, err)
	}
	loose := seedTrafficNode(t, ctx, e, nil, "1.00")
	m, err = e.svc.NodeMetrics(ctx, loose, 60) // 没挂服务器
	if err != nil || len(m.Points) != 0 || m.Latest != nil {
		t.Fatalf("没挂服务器应空：%+v, %v", m, err)
	}
	if _, err := e.svc.NodeMetrics(ctx, uuid.New(), 60); poolErrCode(err) != errs.NotFound {
		t.Fatalf("节点不存在应 NotFound，得 %v", err)
	}
}

func TestLatestServerMetricsPG(t *testing.T) {
	e, ctx := newTrafficEnv(t)
	now := e.svc.now()
	withMem := seedTrafficServer(t, ctx, e)
	noMem := seedTrafficServer(t, ctx, e)
	empty := seedTrafficServer(t, ctx, e)
	for _, c := range []struct {
		server uuid.UUID
		at     time.Time
		m      HostMetrics
	}{
		{withMem, now.Add(-2 * time.Minute), HostMetrics{CPUBp: 9000, MemUsedBytes: 1, MemTotalBytes: 2}},
		{withMem, now.Add(-time.Minute), HostMetrics{CPUBp: 1234, MemUsedBytes: 1, MemTotalBytes: 3}},
		{noMem, now.Add(-time.Minute), HostMetrics{CPUBp: 50}},
	} {
		if err := e.svc.RecordMetrics(ctx, c.server, c.m, c.at); err != nil {
			t.Fatal(err)
		}
	}
	got, err := e.svc.LatestServerMetrics(ctx, []uuid.UUID{withMem, noMem, empty})
	if err != nil {
		t.Fatal(err)
	}
	if s, ok := got[withMem]; !ok || s.CPUPercent != 12.34 || s.MemPercent == nil || *s.MemPercent != 33.3 || !s.At.Equal(now.Add(-time.Minute)) {
		t.Fatalf("withMem %+v", s)
	}
	if s := got[noMem]; s.CPUPercent != 0.5 || s.MemPercent != nil {
		t.Fatalf("内存总量为 0 时 MemPercent 应 nil：%+v", s)
	}
	if _, ok := got[empty]; ok {
		t.Fatal("没有点的服务器不该在结果里")
	}
}
