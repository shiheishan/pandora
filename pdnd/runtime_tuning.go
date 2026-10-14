package main

import (
	"bufio"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/aegispanel/nodeagent/core"
	nativekernel "github.com/aegispanel/nodeagent/kernel"
	"github.com/aegispanel/nodeagent/outbound"
)

// runtimeTuning 是 config.json 里可选的 "runtime" 段：连接回收、停机排空与内存上限。
// 全部可省略，省略即用默认值；面板安装器不写这一段。
type runtimeTuning struct {
	// ConnectionIdleSeconds：两个方向都没有数据超过这么久即回收连接，默认 1800（30 分钟）；0 不回收。
	ConnectionIdleSeconds *int `json:"connection_idle_seconds"`
	// HalfCloseSeconds：一侧结束后，另一侧空闲超过这么久即收尾，默认 1。
	HalfCloseSeconds *int `json:"half_close_seconds"`
	// ShutdownDrainSeconds：停机时等在途连接自然结束的上限，默认 5；0 直接强制关闭。
	ShutdownDrainSeconds *int `json:"shutdown_drain_seconds"`
	// MemoryLimitPercent：没设环境变量 GOMEMLIMIT 时，按 cgroup 或物理内存的这个
	// 百分比设置 Go 的软内存上限，默认 70；0 关闭自动设置。
	MemoryLimitPercent *int `json:"memory_limit_percent"`
	// AllowPrivateDestinations：放开用户访问回环、内网、链路本地等私网目标。
	// 默认拒绝（相当于 Xray 的 geoip:private block），见 outbound/private_guard.go。
	AllowPrivateDestinations bool `json:"allow_private_destinations"`
}

const defaultMemoryLimitPercent = 70

func (t runtimeTuning) validate() error {
	for name, v := range map[string]*int{
		"runtime.connection_idle_seconds": t.ConnectionIdleSeconds,
		"runtime.half_close_seconds":      t.HalfCloseSeconds,
		"runtime.shutdown_drain_seconds":  t.ShutdownDrainSeconds,
	} {
		if v != nil && (*v < 0 || *v > 86400) {
			return fmt.Errorf("%s 取值 %d 超出 0–86400", name, *v)
		}
	}
	if t.HalfCloseSeconds != nil && *t.HalfCloseSeconds == 0 {
		return fmt.Errorf("runtime.half_close_seconds 不能为 0（那等于一侧关闭后永不收尾）")
	}
	if p := t.MemoryLimitPercent; p != nil && (*p < 0 || *p > 100) {
		return fmt.Errorf("runtime.memory_limit_percent 取值 %d 超出 0–100", *p)
	}
	return nil
}

func secondsOr(v *int, def time.Duration) time.Duration {
	if v == nil {
		return def
	}
	return time.Duration(*v) * time.Second
}

// apply 把调优项落到进程：转发的空闲回收、停机排空、Go 内存上限。生效值打进日志。
func (t runtimeTuning) apply(log *slog.Logger) {
	idle := secondsOr(t.ConnectionIdleSeconds, core.DefaultRelayIdleTimeout)
	halfClose := secondsOr(t.HalfCloseSeconds, core.DefaultRelayHalfCloseTimeout)
	drain := secondsOr(t.ShutdownDrainSeconds, nativekernel.DefaultShutdownDrain)
	core.SetRelayTimeouts(idle, halfClose)
	nativekernel.SetShutdownDrain(drain)
	log.Info("连接回收与停机参数", "空闲回收", idle, "单向收尾", halfClose, "停机排空", drain)
	outbound.SetBlockPrivateDestinations(!t.AllowPrivateDestinations)
	if t.AllowPrivateDestinations {
		log.Warn("已放开私网目标：用户可经本节点访问回环、内网与链路本地地址")
	} else {
		log.Info("私网目标默认拒绝（回环、内网、链路本地、保留段）")
	}

	percent := defaultMemoryLimitPercent
	if t.MemoryLimitPercent != nil {
		percent = *t.MemoryLimitPercent
	}
	applyMemoryLimit(log, percent, os.Getenv("GOMEMLIMIT"), detectMemoryBytes)
}

// applyMemoryLimit 设置 Go 的软内存上限（10 万连接实测：默认 GOGC 下活跃堆 4GB 时
// GC 目标是 8GB，超过物理内存，ss / hy2 的分配直接把堆顶进 swap）。环境变量
// GOMEMLIMIT 优先（运行时已在启动时读过它），否则按 cgroup 或物理内存的 percent%。
func applyMemoryLimit(log *slog.Logger, percent int, env string, detect func() (int64, string)) int64 {
	if strings.TrimSpace(env) != "" {
		current := debug.SetMemoryLimit(-1)
		log.Info("Go 内存上限", "来源", "环境变量 GOMEMLIMIT", "取值", env, "生效字节", current)
		return current
	}
	if percent <= 0 {
		log.Info("Go 内存上限", "来源", "配置关闭自动设置", "生效字节", debug.SetMemoryLimit(-1))
		return 0
	}
	total, source := detect()
	if total <= 0 {
		log.Warn("取不到 cgroup 与物理内存大小，未设置 Go 内存上限")
		return 0
	}
	limit := total / 100 * int64(percent)
	debug.SetMemoryLimit(limit)
	log.Info("Go 内存上限", "来源", source, "总内存字节", total, "百分比", percent, "生效字节", limit,
		"生效 MiB", limit>>20)
	return limit
}

// detectMemoryBytes 取进程可用内存：cgroup 限额与物理内存中的较小者。
func detectMemoryBytes() (int64, string) {
	physical := readMemTotal("/proc/meminfo")
	cgroup := readCgroupLimit("/proc/self/cgroup", "/sys/fs/cgroup")
	switch {
	case cgroup > 0 && (physical <= 0 || cgroup < physical):
		return cgroup, "cgroup"
	case physical > 0:
		return physical, "物理内存"
	}
	return 0, ""
}

// readMemTotal 解析 /proc/meminfo 的 MemTotal（kB）。
func readMemTotal(path string) int64 {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if rest, ok := strings.CutPrefix(sc.Text(), "MemTotal:"); ok {
			fields := strings.Fields(rest)
			if len(fields) > 0 {
				kb, err := strconv.ParseInt(fields[0], 10, 64)
				if err == nil {
					return kb * 1024
				}
			}
		}
	}
	return 0
}

// readCgroupLimit 读本进程所在 cgroup 的内存限额：v2 是 <挂载点>/<路径>/memory.max，
// v1 是 <挂载点>/memory/<路径>/memory.limit_in_bytes。沿路径往上找第一个有限额的
// 层级（systemd 常把限额设在上层 slice）。无限额返回 0。
func readCgroupLimit(selfCgroup, root string) int64 {
	raw, err := os.ReadFile(selfCgroup)
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		parts := strings.SplitN(line, ":", 3)
		if len(parts) != 3 {
			continue
		}
		var base, file string
		switch {
		case parts[0] == "0" && parts[1] == "":
			base, file = root, "memory.max"
		case strings.Contains(","+parts[1]+",", ",memory,"):
			base, file = filepath.Join(root, "memory"), "memory.limit_in_bytes"
		default:
			continue
		}
		dir := filepath.Join(base, filepath.Clean("/"+parts[2]))
		for {
			if v := readCgroupValue(filepath.Join(dir, file)); v > 0 {
				return v
			}
			if dir == base || len(dir) <= len(base) {
				break
			}
			dir = filepath.Dir(dir)
		}
	}
	return 0
}

// readCgroupValue 读一个限额文件；"max" 或 v1 的「无限」哨兵值（接近 int64 上限）返回 0。
func readCgroupValue(path string) int64 {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	text := strings.TrimSpace(string(raw))
	if text == "" || text == "max" {
		return 0
	}
	v, err := strconv.ParseInt(text, 10, 64)
	if err != nil || v <= 0 || v >= 1<<62 {
		return 0
	}
	return v
}
