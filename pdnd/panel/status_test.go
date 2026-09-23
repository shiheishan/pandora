package panel

import (
	"encoding/json"
	"runtime"
	"testing"
)

// 采集出来的值必须自洽，否则面板会以「状态上报数值非法」整条拒掉——
// 那时节点看上去就是没心跳，和根本没上报没区别。
func TestCollectRuntimeStatusIsSelfConsistent(t *testing.T) {
	s := CollectRuntimeStatus()

	if s.CPU < 0 || s.CPU > 100 {
		t.Errorf("CPU = %v，面板只接受 0–100", s.CPU)
	}
	for name, p := range map[string]ResourcePair{
		"mem": s.Mem, "swap": s.Swap, "disk": s.Disk,
	} {
		if p.Used > p.Total {
			t.Errorf("%s 已用 %d 超过总量 %d，面板会判成非法", name, p.Used, p.Total)
		}
	}

	// Linux 上至少内存要读得出来——读不到说明 /proc/meminfo 的解析坏了
	if runtime.GOOS == "linux" && s.Mem.Total == 0 {
		t.Error("Linux 上没采到内存总量")
	}
}

// 序列化出来的字段名要和面板的 uniProxyStatus 对齐。
// 对不上不会报错，只会让面板收到一堆零值，然后把节点显示成 CPU 0%、
// 内存 0——比没数据更误导，因为看上去像是真的很闲。
func TestRuntimeStatusJSONMatchesPanelContract(t *testing.T) {
	raw, err := json.Marshal(RuntimeStatus{
		CPU:  12.5,
		Mem:  ResourcePair{Used: 100, Total: 200},
		Swap: ResourcePair{Used: 1, Total: 2},
		Disk: ResourcePair{Used: 3, Total: 4},
	})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"cpu", "mem", "swap", "disk"} {
		if _, ok := got[key]; !ok {
			t.Errorf("缺字段 %q", key)
		}
	}
	pair, ok := got["mem"].(map[string]any)
	if !ok {
		t.Fatal("mem 不是对象")
	}
	for _, key := range []string{"used", "total"} {
		if _, ok := pair[key]; !ok {
			t.Errorf("mem 缺字段 %q", key)
		}
	}
}

// 内存已用要按 MemAvailable 算，不能用 MemFree。
//
// MemFree 把页缓存算成「已用」，于是任何跑了一阵子的机器看上去内存都
// 快满了——运维会以为要加内存，实际那些缓存随时可以回收。
func TestMemoryUsesAvailableNotFree(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("只在 Linux 上有 /proc/meminfo")
	}
	total, used := memoryBytes()
	if total == 0 {
		t.Skip("读不到 /proc/meminfo")
	}
	m := meminfo("MemFree", "MemAvailable")
	// MemAvailable 通常明显大于 MemFree（差的就是可回收的缓存）。
	// 如果 used 是按 MemFree 算的，它会等于 total-MemFree。
	if free := m["MemFree"]; free > 0 && m["MemAvailable"] > free {
		if used == total-free {
			t.Error("内存已用是按 MemFree 算的，应当用 MemAvailable")
		}
	}
}
