package main

// 探针采集。全部来自 /proc 与 statfs，不引入任何第三方依赖 ——
// Agent 要能在最精简的系统镜像上跑起来，多一个依赖就多一处装不上的可能。

import (
	"bufio"
	"os"
	"strconv"
	"strings"
	"time"
)

type metrics struct {
	CPUBasisPoints int   `json:"cpu_bp"` // 万分比，0–10000
	MemUsedMB      int   `json:"mem_used_mb"`
	MemTotalMB     int   `json:"mem_total_mb"`
	DiskUsedGB     int   `json:"disk_used_gb"`
	DiskTotalGB    int   `json:"disk_total_gb"`
	Load1CBP       int   `json:"load1_cbp"` // 负载 ×100
	Load5CBP       int   `json:"load5_cbp"`
	Load15CBP      int   `json:"load15_cbp"`
	NetRxBytes     int64 `json:"net_rx_bytes"`
	NetTxBytes     int64 `json:"net_tx_bytes"`
	TCPConns       int   `json:"tcp_conns"`
	UptimeSec      int64 `json:"uptime_sec"`
}

func collectMetrics() metrics {
	m := metrics{
		MemTotalMB: memoryMB(),
		UptimeSec:  uptimeSeconds(),
		TCPConns:   tcpConnCount(),
	}
	m.MemUsedMB = m.MemTotalMB - memAvailableMB()
	if m.MemUsedMB < 0 {
		m.MemUsedMB = 0
	}
	m.DiskTotalGB, m.DiskUsedGB = diskUsage("/")
	m.Load1CBP, m.Load5CBP, m.Load15CBP = loadAvg()
	m.NetRxBytes, m.NetTxBytes = netCounters()
	m.CPUBasisPoints = cpuPercent()
	return m
}

// CPU 使用率必须由两次采样差分得出：单次读 /proc/stat 只能算出开机以来的
// 平均值，对判断「现在忙不忙」毫无用处。
//
// 采样在一次调用内自包含地做完，而不是跨调用保存上次结果 ——
// Agent 支持 --once 模式（每轮一个新进程），跨调用的状态在那里永远是空的，
// 结果就是 CPU 恒为 0%，而这种「看起来有数据但全是假的」比没有数据更危险。
func cpuPercent() int {
	i1, t1, ok := readCPUTimes()
	if !ok {
		return -1
	}
	// 200ms 足以让差值有统计意义，又不至于让每轮心跳明显变慢
	time.Sleep(200 * time.Millisecond)
	i2, t2, ok := readCPUTimes()
	if !ok || t2 <= t1 {
		return -1
	}

	dTotal := t2 - t1
	dIdle := i2 - i1
	if dIdle > dTotal {
		return -1
	}
	return int(float64(dTotal-dIdle) / float64(dTotal) * 10000)
}

// readCPUTimes 返回 /proc/stat 首行的 idle 与 total 累计时间。
func readCPUTimes() (idle, total uint64, ok bool) {
	f, err := os.Open("/proc/stat")
	if err != nil {
		return 0, 0, false
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	if !sc.Scan() {
		return 0, 0, false
	}
	fields := strings.Fields(sc.Text())
	if len(fields) < 5 || fields[0] != "cpu" {
		return 0, 0, false
	}

	for i, v := range fields[1:] {
		n, err := strconv.ParseUint(v, 10, 64)
		if err != nil {
			continue
		}
		total += n
		// 第 4 列 idle、第 5 列 iowait —— iowait 期间 CPU 同样没在干活
		if i == 3 || i == 4 {
			idle += n
		}
	}
	return idle, total, total > 0
}

func memAvailableMB() int {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		// MemAvailable 比 MemFree 准确得多：后者不含可回收的缓存，
		// 用它算出的「已用内存」在有大量页缓存的机器上会虚高到 90%+
		if strings.HasPrefix(sc.Text(), "MemAvailable:") {
			fs := strings.Fields(sc.Text())
			if len(fs) >= 2 {
				kb, _ := strconv.Atoi(fs[1])
				return kb / 1024
			}
		}
	}
	return 0
}

func loadAvg() (l1, l5, l15 int) {
	b, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0, 0, 0
	}
	fs := strings.Fields(string(b))
	if len(fs) < 3 {
		return 0, 0, 0
	}
	pf := func(s string) int {
		v, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return 0
		}
		return int(v * 100)
	}
	return pf(fs[0]), pf(fs[1]), pf(fs[2])
}

// netCounters 汇总所有物理网卡的收发字节。
// 跳过 lo 与虚拟接口：本机回环和容器网桥的流量不代表节点的对外带宽，
// 算进去会让一台跑着 Docker 的机器看起来流量惊人。
func netCounters() (rx, tx int64) {
	f, err := os.Open("/proc/net/dev")
	if err != nil {
		return 0, 0
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		idx := strings.Index(line, ":")
		if idx < 0 {
			continue
		}
		iface := strings.TrimSpace(line[:idx])
		if iface == "lo" || strings.HasPrefix(iface, "docker") ||
			strings.HasPrefix(iface, "br-") || strings.HasPrefix(iface, "veth") ||
			strings.HasPrefix(iface, "virbr") {
			continue
		}
		fs := strings.Fields(line[idx+1:])
		if len(fs) < 9 {
			continue
		}
		r, _ := strconv.ParseInt(fs[0], 10, 64)
		t, _ := strconv.ParseInt(fs[8], 10, 64)
		rx += r
		tx += t
	}
	return rx, tx
}

func tcpConnCount() int {
	b, err := os.ReadFile("/proc/net/sockstat")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "TCP:") {
			fs := strings.Fields(line)
			for i, v := range fs {
				if v == "inuse" && i+1 < len(fs) {
					n, _ := strconv.Atoi(fs[i+1])
					return n
				}
			}
		}
	}
	return 0
}

func uptimeSeconds() int64 {
	b, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0
	}
	fs := strings.Fields(string(b))
	if len(fs) < 1 {
		return 0
	}
	v, _ := strconv.ParseFloat(fs[0], 64)
	return int64(v)
}
