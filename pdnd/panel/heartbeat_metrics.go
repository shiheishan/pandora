package panel

import (
	"math"
	"os"
	"runtime"
	"strconv"
	"strings"

	nodev1 "github.com/aegispanel/nodeapi/pandora/node/v1"
)

//------------------------------------------------------------------------------
// 心跳的机器指标与 Hello 的机器信息（proto 的整数口径）
//------------------------------------------------------------------------------

// hostCounters 是 RuntimeStatus 之外、只有心跳要的几项内核计数。
type hostCounters struct {
	load          [3]float64
	uptimeSec     float64
	rxBytes       uint64
	txBytes       uint64
	tcpConns      uint64
	missingSource bool
}

// CollectHostMetrics 采一次本机资源，换算成心跳的 HostMetrics。
//
// 有数据源读不到（非 Linux 开发机，或被裁掉 /proc 的容器）时对应字段为零、partial 为真；
// 取不到也照报而不是整组丢掉：心跳本身比指标重要，少一项只是曲线上那一格为零。
// CPU 要两次采样求差（约 100ms），在主循环里调用即可，节拍是 30 秒。
func CollectHostMetrics() *nodev1.HostMetrics {
	s := CollectRuntimeStatus()
	h := readHostCounters()
	m := hostMetricsFrom(s, h)
	m.Partial = h.missingSource || s.Mem.Total == 0
	return m
}

// hostMetricsFrom 把采样换算成 proto 口径并钳到字段范围：CPU 万分比（0–10000），
// 负载 ×100，内存与磁盘按字节，网卡是开机以来的累计字节。
func hostMetricsFrom(s RuntimeStatus, h hostCounters) *nodev1.HostMetrics {
	return &nodev1.HostMetrics{
		CpuBp:          uint32(clampRound(s.CPU*100, 0, 10000)),
		MemUsedBytes:   s.Mem.Used,
		MemTotalBytes:  s.Mem.Total,
		DiskUsedBytes:  s.Disk.Used,
		DiskTotalBytes: s.Disk.Total,
		Load1Cp:        uint32(clampRound(h.load[0]*100, 0, math.MaxUint32)),
		Load5Cp:        uint32(clampRound(h.load[1]*100, 0, math.MaxUint32)),
		Load15Cp:       uint32(clampRound(h.load[2]*100, 0, math.MaxUint32)),
		NetRxBytes:     h.rxBytes,
		NetTxBytes:     h.txBytes,
		TcpConns:       uint32(min(h.tcpConns, math.MaxUint32)),
		UptimeSec:      uint64(clampRound(h.uptimeSec, 0, 1<<53)),
	}
}

// CollectHostInfo 采一次机器信息（接入与每次 Hello 都带）。不采 CPU 占用，不睡眠；
// 读不到的项留零，面板按「未知」存。
func CollectHostInfo() *nodev1.HostInfo {
	hostname, _ := os.Hostname()
	memTotal, _ := memoryBytes()
	diskTotal, _ := diskBytes("/")
	return &nodev1.HostInfo{
		Hostname:    hostname,
		Os:          runtime.GOOS,
		Arch:        runtime.GOARCH,
		CpuCores:    uint32(runtime.NumCPU()),
		MemoryBytes: memTotal,
		DiskBytes:   diskTotal,
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
