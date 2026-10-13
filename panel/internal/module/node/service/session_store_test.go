package service

import (
	"math"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/google/uuid"
)

func TestCleanText(t *testing.T) {
	ptr := func(s string) *string { return &s }
	cases := []struct {
		name string
		in   string
		max  int
		want *string
	}{
		{"空串", "", 10, nil},
		{"全空白", "  \t\n  ", 10, nil},
		{"去首尾空白", "  hello  ", 10, ptr("hello")},
		{"去 NUL", "a\x00b", 10, ptr("ab")},
		{"非法 UTF-8", "\xff\xfe", 10, nil},
		{"中文按字截断", "一二三四五", 3, ptr("一二三")},
		{"ASCII 截断", "abcdef", 3, ptr("abc")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := cleanText(tc.in, tc.max)
			if (got == nil) != (tc.want == nil) {
				t.Fatalf("nil 不一致: got %v want %v", got, tc.want)
			}
			if got != nil && tc.want != nil && *got != *tc.want {
				t.Fatalf("got %q want %q", *got, *tc.want)
			}
		})
	}
}

func TestTruncateBytes(t *testing.T) {
	ptr := func(s string) *string { return &s }
	cases := []struct {
		name string
		in   string
		n    int
		want *string
	}{
		{"空串", "", 8, nil},
		{"未超长", "abc", 8, ptr("abc")},
		{"中文边界", "一二三四", 8, ptr("一二")},
		{"纯 ASCII 截断", "hello world", 5, ptr("hello")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := truncateBytes(tc.in, tc.n)
			if (got == nil) != (tc.want == nil) {
				t.Fatalf("nil 不一致: got %v want %v", got, tc.want)
			}
			if got != nil && tc.want != nil && *got != *tc.want {
				t.Fatalf("got %q want %q", *got, *tc.want)
			}
			if got != nil && !utf8.ValidString(*got) {
				t.Fatalf("非法 UTF-8: %q", *got)
			}
			if got != nil && len(*got) > tc.n {
				t.Fatalf("超过 %d 字节: len=%d %q", tc.n, len(*got), *got)
			}
		})
	}
}

func TestPositiveInt32(t *testing.T) {
	if positiveInt32(0) != nil {
		t.Fatal("0 应为 nil")
	}
	v := positiveInt32(42)
	if v == nil || *v != 42 {
		t.Fatalf("got %v want 42", v)
	}
	capped := positiveInt32(uint64(math.MaxInt32) + 1)
	if capped == nil || *capped != math.MaxInt32 {
		t.Fatalf("封顶: got %v", capped)
	}
}

func TestAgentInfo(t *testing.T) {
	validSHA := strings.Repeat("A", 64)
	wantLower := strings.ToLower(validSHA)
	info := agentInfo(Hello{
		BinarySHA256: validSHA,
		Host: HostInfo{
			MemoryBytes: 3 << 30,
			DiskBytes:   512 << 20, // 小于 1GiB → DiskGB nil
			CPUCores:    0,
			Hostname:    strings.Repeat("h", 300),
		},
	})
	if info.BinarySHA256 == nil || *info.BinarySHA256 != wantLower {
		t.Fatalf("SHA: got %v want %s", info.BinarySHA256, wantLower)
	}
	if info.MemoryMB == nil || *info.MemoryMB != 3072 {
		t.Fatalf("MemoryMB: got %v", info.MemoryMB)
	}
	if info.DiskGB != nil {
		t.Fatalf("DiskGB 应为 nil: %v", info.DiskGB)
	}
	if info.CPUCores != nil {
		t.Fatal("CPUCores 0 应为 nil")
	}
	if info.Hostname == nil || len(*info.Hostname) != 255 {
		t.Fatalf("Hostname 截断: len=%d", len(ptrStr(info.Hostname)))
	}
	bad := agentInfo(Hello{BinarySHA256: "not-hex"})
	if bad.BinarySHA256 != nil {
		t.Fatal("非法 hex 应为 nil")
	}
	short := agentInfo(Hello{BinarySHA256: "abc"})
	if short.BinarySHA256 != nil {
		t.Fatal("长度不对应为 nil")
	}
}

func ptrStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func TestRuntimeRows(t *testing.T) {
	id1 := uuid.MustParse("00000000-0000-4000-8000-000000000001")
	id2 := uuid.MustParse("00000000-0000-4000-8000-000000000002")
	reasonID := uuid.MustParse("00000000-0000-4000-8000-000000000099")

	rows := runtimeRows([]NodeRuntime{
		{NodeID: id1, Status: ""},
		{NodeID: id1, Status: RuntimeDegraded, Reason: "port_busy", ReasonNodeID: &reasonID},
		{NodeID: id2, Status: RuntimeRunning, Reason: "ignored", ReasonNodeID: &reasonID},
		{NodeID: id1, Status: RuntimeDown, Reason: "start_failed"},
	})
	if len(rows) != 2 {
		t.Fatalf("行数: got %d want 2", len(rows))
	}
	// 同一节点后一条为准
	if rows[0].NodeID != id1 || rows[0].Status != RuntimeDown {
		t.Fatalf("id1: %+v", rows[0])
	}
	if rows[0].Reason == nil || *rows[0].Reason != "start_failed" || rows[0].ReasonNodeID != nil {
		t.Fatalf("id1 应取后一条: reason=%v reasonNode=%v", rows[0].Reason, rows[0].ReasonNodeID)
	}
	if rows[1].Status != RuntimeRunning {
		t.Fatalf("id2 status: %s", rows[1].Status)
	}
	if rows[1].Reason != nil || rows[1].ReasonNodeID != nil {
		t.Fatalf("running 应无原因: reason=%v reasonNode=%v", rows[1].Reason, rows[1].ReasonNodeID)
	}
}

func TestCleanTextStripsControls(t *testing.T) {
	ptr := func(s string) *string { return &s }
	cases := []struct {
		name string
		in   string
		max  int
		want *string
	}{
		{"NUL", "a\x00b", 10, ptr("ab")},
		{"换行回车", "a\nb\rc", 10, ptr("abc")},
		{"TAB", "a\tb", 10, ptr("ab")},
		{"ESC", "a\x1b[31mb", 10, ptr("a[31mb")},
		{"DEL", "a\x7fb", 10, ptr("ab")},
		{"C1", "a\u0085b", 10, ptr("ab")},
		{"非法 UTF-8 夹在中间", "a\xffb", 10, ptr("ab")},
		{"只有空白与控制符", " \x00\n\x1b\t ", 10, nil},
		{"按字符截断", strings.Repeat("节", 10), 3, ptr("节节节")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := cleanText(tc.in, tc.max)
			if (got == nil) != (tc.want == nil) {
				t.Fatalf("nil 不一致: got %v want %v", got, tc.want)
			}
			if got == nil {
				return
			}
			if *got != *tc.want {
				t.Fatalf("got %q want %q", *got, *tc.want)
			}
			if !utf8.ValidString(*got) {
				t.Fatalf("结果不是合法 UTF-8: %q", *got)
			}
		})
	}
}

// 接入与 Hello 写的是同一组列，必须得到同样的规整结果。
func TestEnrollAndHelloNormalizeAlike(t *testing.T) {
	h := HostInfo{
		Hostname: " host\x1b-1\n", OS: "linux\x00", Arch: "amd64", CPUCores: 8,
		MemoryBytes: 16 << 30, DiskBytes: 200 << 30,
	}
	agent := "pdnd/1.2\r\n"
	e := enrollHostInfo(h, agent)
	a := agentInfo(Hello{AgentVersion: agent, Host: h})
	eqStr := func(field string, x, y *string) {
		t.Helper()
		if (x == nil) != (y == nil) || (x != nil && *x != *y) {
			t.Fatalf("%s 不一致: %v vs %v", field, x, y)
		}
	}
	eqInt := func(field string, x, y *int32) {
		t.Helper()
		if (x == nil) != (y == nil) || (x != nil && *x != *y) {
			t.Fatalf("%s 不一致: %v vs %v", field, x, y)
		}
	}
	eqStr("hostname", e.Hostname, a.Hostname)
	eqStr("os", e.OSName, a.OSName)
	eqStr("arch", e.Architecture, a.Architecture)
	eqStr("agent", e.AgentVersion, a.AgentVersion)
	eqInt("cpu", e.CPUCores, a.CPUCores)
	eqInt("mem", e.MemoryMB, a.MemoryMB)
	eqInt("disk", e.DiskGB, a.DiskGB)
	if *e.Hostname != "host-1" || *e.AgentVersion != "pdnd/1.2" {
		t.Fatalf("规整结果: %q %q", *e.Hostname, *e.AgentVersion)
	}
}
