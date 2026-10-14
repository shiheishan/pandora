package panel

import (
	"math"
	"runtime"
	"testing"

	nodev1 "github.com/aegispanel/nodeapi/pandora/node/v1"
	"google.golang.org/protobuf/proto"
)

// 换算口径：CPU 万分比、负载 ×100、内存磁盘按字节；越界值钳到 proto 字段范围，
// 面板按同一口径入库（cpu_bp 0–10000）。
func TestHostMetricsUnitsAndClamps(t *testing.T) {
	m := hostMetricsFrom(RuntimeStatus{
		CPU:  37.456,
		Mem:  ResourcePair{Used: 1536 << 20, Total: 4 << 30},
		Disk: ResourcePair{Used: 10<<30 + 1, Total: 40 << 30},
	}, hostCounters{load: [3]float64{0.5, 1.25, 2}, uptimeSec: 3600.7, rxBytes: 100, txBytes: 200, tcpConns: 7})
	want := &nodev1.HostMetrics{CpuBp: 3746, MemUsedBytes: 1536 << 20, MemTotalBytes: 4 << 30, DiskUsedBytes: 10<<30 + 1,
		DiskTotalBytes: 40 << 30, Load1Cp: 50, Load5Cp: 125, Load15Cp: 200, NetRxBytes: 100, NetTxBytes: 200, TcpConns: 7, UptimeSec: 3601}
	if !proto.Equal(m, want) {
		t.Fatalf("metrics = %v, want %v", m, want)
	}

	m = hostMetricsFrom(RuntimeStatus{CPU: 150}, hostCounters{
		load: [3]float64{math.NaN(), -1, 1e12}, uptimeSec: math.Inf(1), rxBytes: math.MaxUint64, tcpConns: math.MaxUint64})
	if m.CpuBp != 10000 {
		t.Errorf("cpu_bp = %d, want clamped to 10000", m.CpuBp)
	}
	if m.Load1Cp != 0 || m.Load5Cp != 0 || m.Load15Cp != math.MaxUint32 {
		t.Errorf("load = %d / %d / %d, want 0 / 0 / max", m.Load1Cp, m.Load5Cp, m.Load15Cp)
	}
	if m.TcpConns != math.MaxUint32 || m.NetRxBytes != math.MaxUint64 || m.UptimeSec == 0 {
		t.Errorf("tcp = %d rx = %d uptime = %d, want clamped values", m.TcpConns, m.NetRxBytes, m.UptimeSec)
	}
	if m := hostMetricsFrom(RuntimeStatus{CPU: math.NaN()}, hostCounters{}); m.CpuBp != 0 {
		t.Errorf("NaN CPU = %d, want 0", m.CpuBp)
	}
}

func TestProcParsers(t *testing.T) {
	load, ok := parseLoadavg([]byte("0.52 1.03 2.10 3/412 12345\n"))
	if !ok || load != [3]float64{0.52, 1.03, 2.10} {
		t.Errorf("loadavg = %v %v", load, ok)
	}
	if _, ok := parseLoadavg([]byte("garbage")); ok {
		t.Error("malformed loadavg accepted")
	}
	if up, ok := parseUptime([]byte("35092.13 138000.55\n")); !ok || up != 35092.13 {
		t.Errorf("uptime = %v %v", up, ok)
	}

	netDev := `Inter-|   Receive                                                |  Transmit
 face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed
    lo: 9000000   100    0    0    0     0          0         0  9000000   100    0    0    0     0       0          0
  eth0: 1000      10     0    0    0     0          0         0  2000      20     0    0    0     0       0          0
  eth1:500        5      0    0    0     0          0         0  700       7      0    0    0     0       0          0
`
	rx, tx, ok := parseNetDev([]byte(netDev))
	if !ok || rx != 1500 || tx != 2700 {
		t.Errorf("net/dev = rx %d tx %d ok %v, want 1500 / 2700 with lo excluded", rx, tx, ok)
	}
	if _, _, ok := parseNetDev(nil); ok {
		t.Error("empty net/dev reported as readable")
	}

	sockstat := "sockets: used 300\nTCP: inuse 42 orphan 0 tw 5 alloc 50 mem 3\nUDP: inuse 4 mem 1\n"
	if n, ok := parseSockstatInuse([]byte(sockstat), "TCP:"); !ok || n != 42 {
		t.Errorf("sockstat TCP inuse = %d %v", n, ok)
	}
	if n, ok := parseSockstatInuse([]byte("TCP6: inuse 3\nUDP6: inuse 1\n"), "TCP6:"); !ok || n != 3 {
		t.Errorf("sockstat6 TCP6 inuse = %d %v", n, ok)
	}
}

// 真机采样也必须落在字段范围内；非 Linux 没有 /proc，必须标 partial。
func TestCollectHostMetricsOnHost(t *testing.T) {
	m := CollectHostMetrics()
	if m.CpuBp > 10000 {
		t.Errorf("cpu_bp = %d, outside 0–10000", m.CpuBp)
	}
	if m.MemUsedBytes > m.MemTotalBytes || m.DiskUsedBytes > m.DiskTotalBytes {
		t.Errorf("used exceeds total: %v", m)
	}
	if runtime.GOOS == "linux" {
		if m.Partial || m.MemTotalBytes == 0 || m.UptimeSec == 0 {
			t.Errorf("Linux host reported partial metrics: %v", m)
		}
	} else if !m.Partial {
		t.Error("non-Linux host has no /proc and must report partial")
	}
}

func TestCollectHostInfo(t *testing.T) {
	h := CollectHostInfo()
	if h.Os != runtime.GOOS || h.Arch != runtime.GOARCH || h.CpuCores == 0 {
		t.Fatalf("host info = %v", h)
	}
	if runtime.GOOS == "linux" && (h.MemoryBytes == 0 || h.DiskBytes == 0) {
		t.Errorf("Linux host info has zero totals: %v", h)
	}
}
