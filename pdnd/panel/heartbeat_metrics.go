// [INPUT]: 依赖 status.go 的 CollectRuntimeStatus（CPU / 内存 / 磁盘），依赖 /proc 的 loadavg、uptime、net/dev、net/sockstat(6)
// [OUTPUT]: 对外提供 HeartbeatMetrics、CollectHeartbeatMetrics、HeartbeatInput.AttachHostMetrics、HostCapacity、CollectHostCapacity
// [POS]: pdnd/panel 的签名通道资源指标：把本机采样换算成面板 nodefabric.Metrics 的整数口径，挂进 signed.go 的 HeartbeatInput，容量部分也供 enrollment.go 的 begin 请求；与 status.go（兼容通道 /status 的字节口径）共用同一份采样

package panel

import (
	"math"
	"os"
	"runtime"
	"strconv"
	"strings"
)

//------------------------------------------------------------------------------
// 签名心跳的资源指标
//------------------------------------------------------------------------------

// HeartbeatMetrics 是签名心跳里的 metrics 对象，字段名与面板
// nodefabric.Metrics 一一对应，面板收到后原样写进 node_metrics——后台的
// 资源曲线和节点列表的 CPU / 内存列都读那张表。
//
// 口径全是放大后的整数：CPU 为万分比（100% = 10000），负载放大 100 倍，
// 内存按 MiB、磁盘按 GiB 取整，网络是开机以来的累计字节（速率由面板
// 相邻两点差分，节点重启归零只会让一段差值为负、被面板丢弃）。
type HeartbeatMetrics struct {
	CPUBasisPoints int   `json:"cpu_bp"`
	MemUsedMB      int   `json:"mem_used_mb"`
	MemTotalMB     int   `json:"mem_total_mb"`
	DiskUsedGB     int   `json:"disk_used_gb"`
	DiskTotalGB    int   `json:"disk_total_gb"`
	Load1CBP       int   `json:"load1_cbp"`
	Load5CBP       int   `json:"load5_cbp"`
	Load15CBP      int   `json:"load15_cbp"`
	NetRxBytes     int64 `json:"net_rx_bytes"`
	NetTxBytes     int64 `json:"net_tx_bytes"`
	TCPConns       int   `json:"tcp_conns"`
	UptimeSec      int64 `json:"uptime_sec"`
}

// hostCounters 是 RuntimeStatus 之外、只有签名心跳要的几项内核计数。
type hostCounters struct {
	load          [3]float64
	uptimeSec     float64
	rxBytes       uint64
	txBytes       uint64
	tcpConns      uint64
	missingSource bool
}

// AttachHostMetrics 采集一次本机资源，填进心跳的 metrics 与机器容量字段。
//
// 容量字段（cpu_cores / memory_mb / disk_gb）面板按「非零才覆盖」写进
// nodes 与 servers，报零等于不改，所以采不到时留零是安全的。
func (in *HeartbeatInput) AttachHostMetrics() {
	m, partial := CollectHeartbeatMetrics()
	in.Metrics = m
	in.MetricsPartial = partial
	c := capacityFrom(m)
	in.CPUCores, in.MemoryMB, in.DiskGB = c.CPUCores, c.MemoryMB, c.DiskGB
}

// HostCapacity 是机器容量，签名心跳与 enrollment begin 报给面板的
// cpu_cores / memory_mb / disk_gb。两处共用同一份采集与换算，面板在接入
// 时看到的容量就和之后心跳刷新的口径一致。
type HostCapacity struct {
	CPUCores int
	MemoryMB int
	DiskGB   int
}

// CollectHostCapacity 采一次本机容量；读不到的项为零，面板按「未知」存。
func CollectHostCapacity() HostCapacity {
	m, _ := CollectHeartbeatMetrics()
	return capacityFrom(m)
}

func capacityFrom(m *HeartbeatMetrics) HostCapacity {
	return HostCapacity{CPUCores: runtime.NumCPU(), MemoryMB: m.MemTotalMB, DiskGB: m.DiskTotalGB}
}

// CollectHeartbeatMetrics 采一次本机资源。partial 为真表示有数据源读不到
// （非 Linux 开发机，或被裁掉 /proc 的容器），对应字段留零。
//
// 取不到也照报而不是整组丢掉：心跳本身比指标重要，少一项只是曲线上那一
// 格为零。
func CollectHeartbeatMetrics() (*HeartbeatMetrics, bool) {
	s := CollectRuntimeStatus()
	h := readHostCounters()
	m := heartbeatMetricsFrom(s, h)
	return &m, h.missingSource || s.Mem.Total == 0
}

// heartbeatMetricsFrom 把采样换算成面板口径，并钳到面板能收的范围。
//
// 钳位不是装饰：node_metrics.cpu_bp 有 0–10000 的 CHECK，其余列是 int4。
// 一个越界值会让面板心跳事务整体回滚——节点在后台上就成了「没心跳」，
// 比少一个点严重得多。
func heartbeatMetricsFrom(s RuntimeStatus, h hostCounters) HeartbeatMetrics {
	return HeartbeatMetrics{
		CPUBasisPoints: int(clampRound(s.CPU*100, 0, 10000)),
		MemUsedMB:      int(bytesTo(s.Mem.Used, 1<<20)),
		MemTotalMB:     int(bytesTo(s.Mem.Total, 1<<20)),
		DiskUsedGB:     int(bytesTo(s.Disk.Used, 1<<30)),
		DiskTotalGB:    int(bytesTo(s.Disk.Total, 1<<30)),
		Load1CBP:       int(clampRound(h.load[0]*100, 0, math.MaxInt32)),
		Load5CBP:       int(clampRound(h.load[1]*100, 0, math.MaxInt32)),
		Load15CBP:      int(clampRound(h.load[2]*100, 0, math.MaxInt32)),
		NetRxBytes:     int64(min(h.rxBytes, math.MaxInt64)),
		NetTxBytes:     int64(min(h.txBytes, math.MaxInt64)),
		TCPConns:       int(min(h.tcpConns, math.MaxInt32)),
		UptimeSec:      int64(clampRound(h.uptimeSec, 0, 1<<53)),
	}
}

func clampRound(v, lo, hi float64) float64 {
	if math.IsNaN(v) || v < lo {
		return lo
	}
	r := math.Round(v)
	if r > hi {
		return hi
	}
	return r
}

// bytesTo 与面板兼容通道的 metricUnit 同一口径：向下取整，封顶 int4。
func bytesTo(v, unit uint64) uint64 { return min(v/unit, math.MaxInt32) }

//------------------------------------------------------------------------------
// /proc 读取：解析与读文件分开，解析部分可以喂夹具测
//------------------------------------------------------------------------------

func readHostCounters() hostCounters {
	var h hostCounters
	read := func(path string) []byte {
		b, err := os.ReadFile(path)
		if err != nil {
			h.missingSource = true
		}
		return b
	}
	var ok bool
	if h.load, ok = parseLoadavg(read("/proc/loadavg")); !ok {
		h.missingSource = true
	}
	if h.uptimeSec, ok = parseUptime(read("/proc/uptime")); !ok {
		h.missingSource = true
	}
	if h.rxBytes, h.txBytes, ok = parseNetDev(read("/proc/net/dev")); !ok {
		h.missingSource = true
	}
	// IPv6 统计单独一个文件；没开 IPv6 的机器上它不存在，不算缺数据源。
	v4, ok := parseSockstatInuse(read("/proc/net/sockstat"), "TCP:")
	if !ok {
		h.missingSource = true
	}
	var v6 uint64
	if b, err := os.ReadFile("/proc/net/sockstat6"); err == nil {
		v6, _ = parseSockstatInuse(b, "TCP6:")
	}
	h.tcpConns = v4 + v6
	return h
}

// parseLoadavg 取 /proc/loadavg 的前三列。
func parseLoadavg(b []byte) ([3]float64, bool) {
	var out [3]float64
	f := strings.Fields(string(b))
	if len(f) < 3 {
		return out, false
	}
	for i := range out {
		v, err := strconv.ParseFloat(f[i], 64)
		if err != nil {
			return [3]float64{}, false
		}
		out[i] = v
	}
	return out, true
}

// parseUptime 取 /proc/uptime 第一列（开机秒数）。
func parseUptime(b []byte) (float64, bool) {
	f := strings.Fields(string(b))
	if len(f) == 0 {
		return 0, false
	}
	v, err := strconv.ParseFloat(f[0], 64)
	return v, err == nil
}

// parseNetDev 汇总 /proc/net/dev 里除回环外所有网卡的收发字节。
//
// 排除 lo：本机进程之间的流量（例如出站走本地 SOCKS）会在 lo 上各记一遍，
// 算进去就把节点的真实吞吐虚高了。
func parseNetDev(b []byte) (rx, tx uint64, ok bool) {
	for _, line := range strings.Split(string(b), "\n") {
		name, rest, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		name = strings.TrimSpace(name)
		f := strings.Fields(rest)
		// 表头两行没有冒号，上面已跳过；数据行冒号后是收 8 列、发 8 列。
		if len(f) < 16 {
			continue
		}
		r, err1 := strconv.ParseUint(f[0], 10, 64)
		t, err2 := strconv.ParseUint(f[8], 10, 64)
		if err1 != nil || err2 != nil {
			continue
		}
		ok = true
		if name == "lo" {
			continue
		}
		rx += r
		tx += t
	}
	return rx, tx, ok
}

// parseSockstatInuse 取 sockstat 里 prefix 那一行的 inuse 值。
//
// 用 sockstat 而不是逐行数 /proc/net/tcp：代理节点上连接动辄上万，
// 每 30 秒读一遍几 MB 的连接表只为一个计数，不值得。
func parseSockstatInuse(b []byte, prefix string) (uint64, bool) {
	for _, line := range strings.Split(string(b), "\n") {
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		f := strings.Fields(strings.TrimPrefix(line, prefix))
		for i := 0; i+1 < len(f); i += 2 {
			if f[i] == "inuse" {
				v, err := strconv.ParseUint(f[i+1], 10, 64)
				return v, err == nil
			}
		}
	}
	return 0, false
}
