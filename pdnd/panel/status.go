package panel

import (
	"context"
	"os"
	"strconv"
	"strings"
	"time"
)

// 运行状态上报。
//
// 面板的节点管理页要显示节点是否存活、Agent 版本、CPU/内存/磁盘占用，
// 这些字段只能由节点端自己报——面板没法从外面探到。不报的后果是那几列
// 一直空着，服务挂了后台也不变色，只能等用户报障或者上服务器看。
//
// 走 UniProxy 的 /status，和 config / user / push / alive 是同一套认证。

// ResourcePair 是「已用 / 总量」，单位字节。
type ResourcePair struct {
	Used  uint64 `json:"used"`
	Total uint64 `json:"total"`
}

// RuntimeStatus 是一次状态上报的内容。
//
// 字段名和面板的 uniProxyStatus 对齐。刻意只报粗粒度的机器指标，
// 不含任何用户或地址信息——这条链路上的数据会长期留存在 node_metrics
// 里，往里塞用户级信息等于给自己造一个不该存在的数据副本。
type RuntimeStatus struct {
	CPU  float64      `json:"cpu"` // 百分比，0–100
	Mem  ResourcePair `json:"mem"`
	Swap ResourcePair `json:"swap"`
	Disk ResourcePair `json:"disk"`
}

// Status 上报一次运行状态。
func (c *Client) Status(ctx context.Context, s RuntimeStatus) error {
	return c.post(ctx, "status", s)
}

// CollectRuntimeStatus 采集本机的资源占用。
//
// 只读 /proc 和 statfs，不依赖任何外部命令——节点端跑在各种精简镜像里，
// 指望 top / df 存在是不安全的。取不到的值留零：面板那边对零值的处理是
// 「显示为 0」，比整条上报失败强，至少心跳还在。
func CollectRuntimeStatus() RuntimeStatus {
	var s RuntimeStatus
	s.CPU = cpuPercent()
	s.Mem.Total, s.Mem.Used = memoryBytes()
	s.Swap.Total, s.Swap.Used = swapBytes()
	s.Disk.Total, s.Disk.Used = diskBytes("/")
	return s
}

// meminfo 读 /proc/meminfo 里的若干项，单位统一成字节。
func meminfo(keys ...string) map[string]uint64 {
	out := make(map[string]uint64, len(keys))
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return out
	}
	want := make(map[string]bool, len(keys))
	for _, k := range keys {
		want[k] = true
	}
	for _, line := range strings.Split(string(data), "\n") {
		name, rest, ok := strings.Cut(line, ":")
		if !ok || !want[name] {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) == 0 {
			continue
		}
		n, err := strconv.ParseUint(fields[0], 10, 64)
		if err != nil {
			continue
		}
		// /proc/meminfo 的单位是 kB
		out[name] = n * 1024
	}
	return out
}

func memoryBytes() (total, used uint64) {
	m := meminfo("MemTotal", "MemAvailable")
	total = m["MemTotal"]
	// 用 MemAvailable 而不是 MemFree：后者把页缓存算成「已用」，
	// 于是任何跑了一阵子的机器看上去内存都快满了。
	if avail := m["MemAvailable"]; avail <= total {
		used = total - avail
	}
	return total, used
}

func swapBytes() (total, used uint64) {
	m := meminfo("SwapTotal", "SwapFree")
	total = m["SwapTotal"]
	if free := m["SwapFree"]; free <= total {
		used = total - free
	}
	return total, used
}

// cpuPercent 是两次采样之间的 CPU 占用百分比。
//
// /proc/stat 给的是开机以来的累计时间，单次读取算不出「当前占用」，
// 必须隔一小段再读一次求差。100ms 是权衡：再短了在负载低的机器上会因为
// 时钟粒度得到 0 或 100 这种跳变，再长了每轮上报都要多等这么久。
func cpuPercent() float64 {
	idle1, total1, ok1 := cpuTimes()
	if !ok1 {
		return 0
	}
	time.Sleep(100 * time.Millisecond)
	idle2, total2, ok2 := cpuTimes()
	if !ok2 || total2 <= total1 {
		return 0
	}
	busy := (total2 - total1) - (idle2 - idle1)
	pct := float64(busy) * 100 / float64(total2-total1)
	if pct < 0 {
		return 0
	}
	if pct > 100 {
		return 100
	}
	return pct
}

// cpuTimes 读 /proc/stat 第一行的累计时间片。
func cpuTimes() (idle, total uint64, ok bool) {
	data, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 0, 0, false
	}
	line, _, _ := strings.Cut(string(data), "\n")
	fields := strings.Fields(line)
	if len(fields) < 5 || fields[0] != "cpu" {
		return 0, 0, false
	}
	for i, f := range fields[1:] {
		n, err := strconv.ParseUint(f, 10, 64)
		if err != nil {
			continue
		}
		total += n
		// 第 4 项是 idle，第 5 项是 iowait——等 IO 的时间不算在忙里，
		// 否则一台在拷贝大文件的机器会显示成 CPU 跑满。
		if i == 3 || i == 4 {
			idle += n
		}
	}
	return idle, total, total > 0
}
