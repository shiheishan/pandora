package service

import (
	"crypto/sha256"
	"math"
	"net/netip"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/module/node/repo"
)

func TestDeviceKey(t *testing.T) {
	for in, want := range map[string]string{
		"1.2.3.4":              "1.2.3.4",
		"::ffff:1.2.3.4":       "1.2.3.4",
		"2001:db8:1:2:3:4:5:6": "2001:db8:1:2::/64",
		"fe80::1%eth0":         "fe80::/64",
	} {
		if got := deviceKey(netip.MustParseAddr(in)); got != want {
			t.Errorf("deviceKey(%s) = %s，期望 %s", in, got, want)
		}
	}
}

func TestAliveRowsDedupAndOrder(t *testing.T) {
	n1, n2 := tid(1), tid(2)
	sub := tid(50)
	rows := aliveRows([]OnlineEntry{
		{NodeID: n2, UID: 7, IPs: []netip.Addr{netip.MustParseAddr("1.2.3.4")}},
		{NodeID: n1, UID: 7, IPs: []netip.Addr{
			netip.MustParseAddr("1.2.3.4"), netip.MustParseAddr("1.2.3.4"),
			netip.MustParseAddr("2001:db8:1:2::1"), netip.MustParseAddr("2001:db8:1:2::2"), {},
		}},
		{NodeID: n1, UID: 8, IPs: []netip.Addr{netip.MustParseAddr("5.6.7.8")}}, // 找不到订阅
	}, map[int64]uuid.UUID{7: sub}, func(b []byte) []byte { h := sha256.Sum256(b); return h[:] })
	if len(rows) != 3 || rows[0].NodeID != n1 || rows[1].NodeID != n1 || rows[2].NodeID != n2 {
		t.Fatalf("行 %v", rows)
	}
	if rows[0].IPHash >= rows[1].IPHash || len(rows[0].IPHash) != 64 {
		t.Fatalf("同节点同订阅应按哈希升序：%v", rows)
	}
}

func TestBuildNodeMetrics(t *testing.T) {
	empty := buildNodeMetrics(nil)
	if empty.Points == nil || len(empty.Points) != 0 || empty.Latest != nil {
		t.Fatalf("空输入 %+v", empty)
	}
	t0 := time.Date(2026, 10, 12, 8, 0, 0, 0, time.UTC)
	row := func(at time.Time, rx int64) repo.MetricRow {
		return repo.MetricRow{RecordedAt: at, CPUBp: 1234, MemUsedBytes: 1 << 30, MemTotalBytes: 4 << 30,
			DiskUsedBytes: 10 << 30, DiskTotalBytes: 40 << 30, Load1Cp: 50, Load5Cp: 40, Load15Cp: 30,
			NetRxBytes: rx, NetTxBytes: rx / 2, TCPConns: 7, UptimeSec: 3600}
	}
	m := buildNodeMetrics([]repo.MetricRow{
		row(t0, 0), row(t0.Add(60*time.Second), 6000), row(t0.Add(120*time.Second), 3000), row(t0.Add(520*time.Second), 9000),
	})
	if len(m.Points) != 4 {
		t.Fatalf("点数 %d", len(m.Points))
	}
	if m.Points[0].RxSpeed != 0 || m.Points[1].RxSpeed != 100 || m.Points[1].TxSpeed != 50 || m.Points[2].RxSpeed != 0 || m.Points[3].RxSpeed != 0 {
		t.Fatalf("速率 %+v", m.Points)
	}
	if p := m.Points[0]; p.CPUPercent != 12.34 || p.MemPercent != 25 || p.Load1 != 0.5 || p.TCPConns != 7 {
		t.Fatalf("换算 %+v", p)
	}
	l := m.Latest
	if l == nil || l.MemUsedMB != 1024 || l.MemTotalMB != 4096 || l.DiskUsedGB != 10 || l.DiskTotalGB != 40 ||
		l.Load15 != 0.3 || l.RxTotal != 9000 || l.TxTotal != 4500 || l.UptimeSec != 3600 {
		t.Fatalf("Latest %+v", l)
	}
}

func TestClamp(t *testing.T) {
	if clampU64(math.MaxUint64) != math.MaxInt64 || clampU32(math.MaxUint32) != math.MaxInt32 || clampU64(5) != 5 {
		t.Fatal("clamp")
	}
}
