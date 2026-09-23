package core

// 采集宿主机的 CPU / 内存 / 磁盘占用，供状态上报使用。
//
// 全部走 /proc 和 statfs，不引第三方库。节点端跑在别人的机器上，
// 每多一个依赖就多一份供应链风险，而这里要的三个数字用标准库就够。

import (
	"bufio"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// ResourcePair 是「用了多少 / 一共多少」，单位字节。
type ResourcePair struct {
	Used  uint64 `json:"used"`
	Total uint64 `json:"total"`
}

// SystemStatus 是一次状态快照。字段名与面板的 UniProxy 状态接口对齐。
type SystemStatus struct {
	// CPU 是百分比，0 到 100。
	CPU  float64      `json:"cpu"`
	Mem  ResourcePair `json:"mem"`
	Swap ResourcePair `json:"swap"`
	Disk ResourcePair `json:"disk"`
}

// HostStat 采集器。
//
// CPU 占用只能由两次采样的差值算出来 —— /proc/stat 给的是开机以来的
// 累计时间片。所以采集器要记住上一次的读数，不能做成无状态函数。
type HostStat struct {
	mu       sync.Mutex
	prevIdle uint64
	prevAll  uint64
	// diskPath 是统计磁盘用量的挂载点，默认根分区。
	diskPath string
}

func NewHostStat() *HostStat { return &HostStat{diskPath: "/"} }

// Read 采一次。
//
// 第一次调用时没有上一次读数可比，就地采一个 200 毫秒的短样本；
// 之后每次都用「距上次调用」的差值，这样上报周期越长，CPU 数字
// 越接近这段时间的真实平均值，而不是采样瞬间的抖动。
func (h *HostStat) Read() SystemStatus {
	var out SystemStatus
	out.CPU = h.cpuPercent()
	out.Mem, out.Swap = readMemInfo()
	out.Disk = readDisk(h.diskPath)
	return out
}

func (h *HostStat) cpuPercent() float64 {
	h.mu.Lock()
	defer h.mu.Unlock()

	idle, all, ok := readCPUTimes()
	if !ok {
		return 0
	}
	if h.prevAll == 0 {
		// 冷启动：没有基准就现采一个短样本，总好过第一次上报永远是 0。
		time.Sleep(200 * time.Millisecond)
		idle2, all2, ok2 := readCPUTimes()
		if !ok2 || all2 <= all {
			h.prevIdle, h.prevAll = idle, all
			return 0
		}
		h.prevIdle, h.prevAll = idle2, all2
		return pct(idle2-idle, all2-all)
	}

	dAll := all - h.prevAll
	dIdle := idle - h.prevIdle
	h.prevIdle, h.prevAll = idle, all
	if dAll == 0 {
		return 0
	}
	return pct(dIdle, dAll)
}

func pct(idleDelta, allDelta uint64) float64 {
	if allDelta == 0 || idleDelta > allDelta {
		return 0
	}
	v := (1 - float64(idleDelta)/float64(allDelta)) * 100
	// 面板对越界值直接判非法整包丢弃，所以夹在合法区间里再交出去。
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}

// readCPUTimes 读 /proc/stat 的首行汇总，返回 (空闲, 总计) 时间片。
func readCPUTimes() (idle, all uint64, ok bool) {
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
	for i, raw := range fields[1:] {
		v, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			continue
		}
		all += v
		// 第 4、5 列是 idle 和 iowait，两者都不算忙。
		if i == 3 || i == 4 {
			idle += v
		}
	}
	return idle, all, all > 0
}

// readMemInfo 读 /proc/meminfo，返回内存与 swap 的用量。
//
// 内存「已用」按 MemTotal - MemAvailable 算，不按 MemTotal - MemFree。
// 后者会把页缓存算成已用，于是一台正常的机器常年显示 90% 以上内存占用
// —— 那个数字没有任何指导意义，看见了也不知道该不该扩容。
func readMemInfo() (mem, swap ResourcePair) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return
	}
	defer f.Close()

	vals := map[string]uint64{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		parts := strings.SplitN(sc.Text(), ":", 2)
		if len(parts) != 2 {
			continue
		}
		fields := strings.Fields(parts[1])
		if len(fields) == 0 {
			continue
		}
		v, err := strconv.ParseUint(fields[0], 10, 64)
		if err != nil {
			continue
		}
		// /proc/meminfo 的单位是 kB。
		vals[parts[0]] = v * 1024
	}

	mem.Total = vals["MemTotal"]
	if avail, ok := vals["MemAvailable"]; ok && mem.Total >= avail {
		mem.Used = mem.Total - avail
	} else if free, ok := vals["MemFree"]; ok && mem.Total >= free {
		mem.Used = mem.Total - free
	}

	swap.Total = vals["SwapTotal"]
	if free, ok := vals["SwapFree"]; ok && swap.Total >= free {
		swap.Used = swap.Total - free
	}
	return
}

// readDisk 用 statfs 取挂载点的容量。
//
// 已用按 Blocks - Bfree（含 root 预留），可用空间对普通进程其实是 Bavail，
// 但「已用」要的是真实占用，两者差的那部分预留块并没有空着给谁用。
func readDisk(path string) ResourcePair {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return ResourcePair{}
	}
	bs := uint64(st.Bsize)
	total := st.Blocks * bs
	used := (st.Blocks - st.Bfree) * bs
	if used > total {
		used = total
	}
	return ResourcePair{Used: used, Total: total}
}
