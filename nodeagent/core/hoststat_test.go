package core

import (
	"encoding/json"
	"testing"
)

// 面板对状态上报的数值做硬校验，任何一项越界就整包判非法丢弃
// （见 panel 的 ReportRuntimeStatus）。丢弃的后果不是报错，而是这个
// 节点的心跳不更新，进而被订阅下发跳过 —— 静默失联。所以采集器交出来
// 的数字必须先自己守住这些边界。
func TestReadStaysWithinPanelAcceptedRanges(t *testing.T) {
	got := NewHostStat().Read()

	if got.CPU < 0 || got.CPU > 100 {
		t.Errorf("CPU=%v，面板只接受 0..100", got.CPU)
	}
	for _, c := range []struct {
		name string
		p    ResourcePair
	}{{"mem", got.Mem}, {"swap", got.Swap}, {"disk", got.Disk}} {
		if c.p.Used > c.p.Total {
			t.Errorf("%s used=%d 大于 total=%d，面板会判非法", c.name, c.p.Used, c.p.Total)
		}
	}
	// 内存和磁盘总量为 0 说明 /proc 或 statfs 没读到，上报出去也没意义。
	if got.Mem.Total == 0 {
		t.Error("内存总量为 0，/proc/meminfo 没读出来")
	}
	if got.Disk.Total == 0 {
		t.Error("磁盘总量为 0，statfs 没读出来")
	}
}

// 上报的 JSON 字段名必须和面板的 uniProxyStatus 对齐，错一个字母
// 面板就解析成零值：CPU 永远 0、内存永远 0，而且不会报任何错。
func TestStatusJSONFieldNamesMatchPanelContract(t *testing.T) {
	raw, err := json.Marshal(SystemStatus{
		CPU:  12.5,
		Mem:  ResourcePair{Used: 1, Total: 2},
		Swap: ResourcePair{Used: 3, Total: 4},
		Disk: ResourcePair{Used: 5, Total: 6},
	})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"cpu", "mem", "swap", "disk"} {
		if _, ok := m[k]; !ok {
			t.Errorf("状态上报缺少字段 %q", k)
		}
	}
	pair, ok := m["mem"].(map[string]any)
	if !ok {
		t.Fatal("mem 不是对象")
	}
	for _, k := range []string{"used", "total"} {
		if _, ok := pair[k]; !ok {
			t.Errorf("资源对缺少字段 %q", k)
		}
	}
}

// 第二次读数必须走差值路径。冷启动那次会现采一个 200ms 短样本，
// 之后就该用「距上次调用」的差值 —— 如果 prevAll 没被记住，
// 每次上报都会重新 sleep 200ms，节点多了就是白白的启动延迟。
func TestSecondReadUsesRememberedBaseline(t *testing.T) {
	h := NewHostStat()
	h.Read()
	if h.prevAll == 0 {
		t.Fatal("首次采样之后应该记住基准，否则每次上报都要重新短采样")
	}
	before := h.prevAll
	h.Read()
	if h.prevAll < before {
		t.Error("累计时间片不该倒退")
	}
}

func TestPctClampsInsteadOfReturningOutOfRange(t *testing.T) {
	cases := []struct {
		idle, all uint64
		want      float64
	}{
		{0, 0, 0},     // 没有时间流逝
		{100, 100, 0}, // 全空闲
		{0, 100, 100}, // 全忙
		{200, 100, 0}, // 数据异常：空闲比总量还大，夹成 0 而不是负数
	}
	for _, c := range cases {
		if got := pct(c.idle, c.all); got != c.want {
			t.Errorf("pct(%d,%d)=%v, want %v", c.idle, c.all, got, c.want)
		}
	}
}
