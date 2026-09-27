package panel

import (
	"encoding/json"
	"math"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// 字段名必须与面板 nodefabric.Metrics 完全一致。对不上不会报错：面板把
// 不认识的键丢掉、缺的键记零，曲线上就是一条贴着 0 的线，看着像真的很闲。
func TestHeartbeatMetricsJSONMatchesPanelContract(t *testing.T) {
	raw, err := json.Marshal(HeartbeatMetrics{})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	keys := make([]string, 0, len(got))
	for k := range got {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	want := []string{"cpu_bp", "disk_total_gb", "disk_used_gb", "load15_cbp", "load1_cbp", "load5_cbp",
		"mem_total_mb", "mem_used_mb", "net_rx_bytes", "net_tx_bytes", "tcp_conns", "uptime_sec"}
	if strings.Join(keys, ",") != strings.Join(want, ",") {
		t.Fatalf("metrics keys = %v, want %v", keys, want)
	}

	in := HeartbeatInput{Metrics: &HeartbeatMetrics{}, MetricsPartial: true}
	raw, _ = json.Marshal(in)
	if !strings.Contains(string(raw), `"metrics":{`) || !strings.Contains(string(raw), `"metrics_partial":true`) {
		t.Fatalf("heartbeat input does not carry metrics: %s", raw)
	}
	raw, _ = json.Marshal(HeartbeatInput{})
	if strings.Contains(string(raw), `"metrics"`) {
		t.Fatalf("empty heartbeat must omit metrics so the panel writes no point: %s", raw)
	}
}

// 换算口径与面板一致，越界值钳到面板列能收的范围：cpu_bp 有 0–10000 的
// CHECK，其余是 int4，一个越界值会让整条心跳事务回滚。
func TestHeartbeatMetricsUnitsAndClamps(t *testing.T) {
	m := heartbeatMetricsFrom(RuntimeStatus{
		CPU:  37.456,
		Mem:  ResourcePair{Used: 1536 << 20, Total: 4 << 30},
		Disk: ResourcePair{Used: 10<<30 + 1, Total: 40 << 30},
	}, hostCounters{load: [3]float64{0.5, 1.25, 2}, uptimeSec: 3600.7, rxBytes: 100, txBytes: 200, tcpConns: 7})
	want := HeartbeatMetrics{CPUBasisPoints: 3746, MemUsedMB: 1536, MemTotalMB: 4096, DiskUsedGB: 10, DiskTotalGB: 40,
		Load1CBP: 50, Load5CBP: 125, Load15CBP: 200, NetRxBytes: 100, NetTxBytes: 200, TCPConns: 7, UptimeSec: 3601}
	if m != want {
		t.Fatalf("metrics = %+v, want %+v", m, want)
	}

	m = heartbeatMetricsFrom(RuntimeStatus{
		CPU:  150,
		Mem:  ResourcePair{Used: math.MaxUint64, Total: math.MaxUint64},
		Disk: ResourcePair{Used: math.MaxUint64, Total: math.MaxUint64},
	}, hostCounters{load: [3]float64{math.NaN(), -1, 1e12}, uptimeSec: math.Inf(1), rxBytes: math.MaxUint64, tcpConns: math.MaxUint64})
	if m.CPUBasisPoints != 10000 {
		t.Errorf("cpu_bp = %d, want clamped to 10000", m.CPUBasisPoints)
	}
	for name, v := range map[string]int{"mem_used_mb": m.MemUsedMB, "mem_total_mb": m.MemTotalMB, "disk_used_gb": m.DiskUsedGB,
		"disk_total_gb": m.DiskTotalGB, "load15_cbp": m.Load15CBP, "tcp_conns": m.TCPConns} {
		if v != math.MaxInt32 {
			t.Errorf("%s = %d, want clamped to int4 max", name, v)
		}
	}
	if m.Load1CBP != 0 || m.Load5CBP != 0 {
		t.Errorf("NaN / negative load = %d / %d, want 0", m.Load1CBP, m.Load5CBP)
	}
	if m.NetRxBytes != math.MaxInt64 || m.UptimeSec <= 0 {
		t.Errorf("rx = %d uptime = %d, want clamped positive values", m.NetRxBytes, m.UptimeSec)
	}
	if m := heartbeatMetricsFrom(RuntimeStatus{CPU: math.NaN()}, hostCounters{}); m.CPUBasisPoints != 0 {
		t.Errorf("NaN CPU = %d, want 0", m.CPUBasisPoints)
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

// 真机采样也必须落在面板能收的范围内。
func TestCollectHeartbeatMetricsOnHost(t *testing.T) {
	m, partial := CollectHeartbeatMetrics()
	if m.CPUBasisPoints < 0 || m.CPUBasisPoints > 10000 {
		t.Errorf("cpu_bp = %d, outside 0–10000", m.CPUBasisPoints)
	}
	if m.MemUsedMB > m.MemTotalMB || m.DiskUsedGB > m.DiskTotalGB {
		t.Errorf("used exceeds total: %+v", m)
	}
	if runtime.GOOS == "linux" {
		if partial || m.MemTotalMB == 0 || m.UptimeSec == 0 {
			t.Errorf("Linux host reported partial metrics: partial=%v %+v", partial, m)
		}
	} else if !partial {
		t.Error("non-Linux host has no /proc and must report metrics_partial")
	}
}
