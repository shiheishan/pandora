package service

import (
	"math"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestEnrollHostInfo(t *testing.T) {
	h := enrollHostInfo(EnrollHostInfo{
		Hostname: "node-1", OS: "linux", Arch: "amd64", CPUCores: 4,
		MemoryBytes: 8 << 30, DiskBytes: 100 << 30,
	}, "v1.2.3")
	if *h.Hostname != "node-1" || *h.OSName != "linux" || *h.Architecture != "amd64" || *h.AgentVersion != "v1.2.3" {
		t.Fatalf("字符串列: %+v", h)
	}
	if *h.CPUCores != 4 || *h.MemoryMB != 8192 || *h.DiskGB != 100 {
		t.Fatalf("数值列: %d %d %d", *h.CPUCores, *h.MemoryMB, *h.DiskGB)
	}

	z := enrollHostInfo(EnrollHostInfo{}, "")
	if z.Hostname != nil || z.OSName != nil || z.Architecture != nil || z.AgentVersion != nil ||
		z.CPUCores != nil || z.MemoryMB != nil || z.DiskGB != nil {
		t.Fatalf("零值应全为未知: %+v", z)
	}

	c := enrollHostInfo(EnrollHostInfo{Hostname: "a\x00b\nc", OS: "\x01\x02", CPUCores: math.MaxUint32}, "")
	if *c.Hostname != "abc" {
		t.Fatalf("控制字符没去掉: %q", *c.Hostname)
	}
	if c.OSName != nil {
		t.Fatalf("全是控制字符应为未知: %q", *c.OSName)
	}
	if *c.CPUCores != math.MaxInt32 {
		t.Fatalf("CPU 截断: %d", *c.CPUCores)
	}

	long := enrollHostInfo(EnrollHostInfo{Hostname: strings.Repeat("节", 300)}, "")
	if n := len([]rune(*long.Hostname)); n != 255 {
		t.Fatalf("hostname 应截到 255 个字符: %d", n)
	}

	esc := enrollHostInfo(EnrollHostInfo{Hostname: "a\x1b[31mb\tc\u0085d"}, "")
	if *esc.Hostname != "a[31mbcd" {
		t.Fatalf("ANSI/控制符: got %q", *esc.Hostname)
	}
	ver := enrollHostInfo(EnrollHostInfo{}, "v1\n2")
	if ver.AgentVersion == nil || *ver.AgentVersion != "v12" {
		t.Fatalf("AgentVersion 去换行: got %v", ver.AgentVersion)
	}
}

func TestEnrollRequestIDPattern(t *testing.T) {
	if !enrollRequestIDPattern.MatchString(uuid.NewString()) {
		t.Fatal("uuid 应通过")
	}
	for _, bad := range []string{"", strings.Repeat("a", 65), "a b", "a/b"} {
		if enrollRequestIDPattern.MatchString(bad) {
			t.Fatalf("%q 不应通过", bad)
		}
	}
}
