package main

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime/debug"
	"testing"
	"time"

	"github.com/aegispanel/nodeagent/core"
	"github.com/aegispanel/nodeagent/outbound"
)

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// cgroup v2：服务自己的层级没设限额时，往上找 slice 的限额；"max" 视为无限额。
func TestReadCgroupLimitV2WalksUp(t *testing.T) {
	dir := t.TempDir()
	self := filepath.Join(dir, "self-cgroup")
	writeFile(t, self, "0::/system.slice/pandora-native.service\n")
	root := filepath.Join(dir, "cg")
	writeFile(t, filepath.Join(root, "system.slice/pandora-native.service/memory.max"), "max\n")
	writeFile(t, filepath.Join(root, "system.slice/memory.max"), "1073741824\n")
	if got := readCgroupLimit(self, root); got != 1<<30 {
		t.Fatalf("limit=%d", got)
	}
}

func TestReadCgroupLimitV1(t *testing.T) {
	dir := t.TempDir()
	self := filepath.Join(dir, "self-cgroup")
	writeFile(t, self, "12:cpu,cpuacct:/x\n7:memory:/system.slice/pdnd.service\n")
	root := filepath.Join(dir, "cg")
	writeFile(t, filepath.Join(root, "memory/system.slice/pdnd.service/memory.limit_in_bytes"), "536870912\n")
	if got := readCgroupLimit(self, root); got != 512<<20 {
		t.Fatalf("limit=%d", got)
	}
	// v1 的「无限」哨兵值不算限额。
	writeFile(t, filepath.Join(root, "memory/system.slice/pdnd.service/memory.limit_in_bytes"), "9223372036854771712\n")
	if got := readCgroupLimit(self, root); got != 0 {
		t.Fatalf("unlimited sentinel parsed as %d", got)
	}
}

func TestReadMemTotal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "meminfo")
	writeFile(t, path, "MemTotal:        8000000 kB\nMemFree: 1 kB\n")
	if got := readMemTotal(path); got != 8000000*1024 {
		t.Fatalf("memtotal=%d", got)
	}
}

// 没设 GOMEMLIMIT：按探测到的内存的 70% 设置；设了环境变量：不覆盖它。
func TestApplyMemoryLimit(t *testing.T) {
	prev := debug.SetMemoryLimit(-1)
	defer debug.SetMemoryLimit(prev)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	detect := func() (int64, string) { return 1000 << 20, "cgroup" }
	if got := applyMemoryLimit(log, 70, "", detect); got != (1000<<20)/100*70 {
		t.Fatalf("limit=%d", got)
	}
	if debug.SetMemoryLimit(-1) != (1000<<20)/100*70 {
		t.Fatal("运行时内存上限没有生效")
	}
	debug.SetMemoryLimit(prev)
	if got := applyMemoryLimit(log, 70, "6GiB", detect); got != prev {
		t.Fatalf("环境变量 GOMEMLIMIT 优先，不应被覆盖：got %d", got)
	}
	if got := applyMemoryLimit(log, 0, "", detect); got != 0 || debug.SetMemoryLimit(-1) != prev {
		t.Fatal("percent=0 应关闭自动设置")
	}
}

func TestRuntimeTuningValidateAndApply(t *testing.T) {
	zero := 0
	if err := (runtimeTuning{HalfCloseSeconds: &zero}).validate(); err == nil {
		t.Fatal("half_close_seconds=0 应被拒")
	}
	bad := 101
	if err := (runtimeTuning{MemoryLimitPercent: &bad}).validate(); err == nil {
		t.Fatal("memory_limit_percent=101 应被拒")
	}
	idle, half, off := 120, 2, 0
	defer core.SetRelayTimeouts(core.DefaultRelayIdleTimeout, core.DefaultRelayHalfCloseTimeout)
	prev := debug.SetMemoryLimit(-1)
	defer debug.SetMemoryLimit(prev)
	runtimeTuning{ConnectionIdleSeconds: &idle, HalfCloseSeconds: &half, MemoryLimitPercent: &off}.apply(slog.New(slog.NewTextHandler(io.Discard, nil)))
	gotIdle, gotHalf := core.RelayTimeouts()
	if gotIdle != 120*time.Second || gotHalf != 2*time.Second {
		t.Fatalf("idle=%s half=%s", gotIdle, gotHalf)
	}
	if !outbound.BlockPrivateDestinations() {
		t.Fatal("默认应拒绝私网目标")
	}
	runtimeTuning{AllowPrivateDestinations: true, MemoryLimitPercent: &off}.apply(slog.New(slog.NewTextHandler(io.Discard, nil)))
	if outbound.BlockPrivateDestinations() {
		t.Fatal("allow_private_destinations=true 应放开")
	}
	outbound.SetBlockPrivateDestinations(true)
}
