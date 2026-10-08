package quiet

import (
	"bytes"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func near(t *testing.T, what string, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 0.005 {
		t.Fatalf("%s = %.3f, want %.3f", what, got, want)
	}
}

const pidA = `100 (aegis-node) S 1 0 0 0 -1 0 0 0 0 0 1000 500 0 0 20 0 1 0 1 0 0 18446744073709551615 0 0 0 0 0 0 0 0 0 0 0 0 17 0 0 0 0 0 0
200 (a b) c) S 1 0 0 0 -1 0 0 0 0 0 100 100 10 10 20 0 1 0 1 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 17 0 0 0 0 0 0
`
const pidB = `100 (aegis-node) S 1 0 0 0 -1 0 0 0 0 0 1600 800 0 0 20 0 1 0 1 0 0 18446744073709551615 0 0 0 0 0 0 0 0 0 0 0 0 17 0 0 0 0 0 0
200 (a b) c) S 1 0 0 0 -1 0 0 0 0 0 150 150 30 30 20 0 1 0 1 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 17 0 0 0 0 0 0
300 (new) S 1 0 0 0 -1 0 0 0 0 0 50 50 0 0 20 0 1 0 1 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 17 0 0 0 0 0 0
`

// fixture 写一份窗口 100 秒的合成采集目录。postgres / valkey 用哪种名字由 layout 决定。
func fixture(t *testing.T, layout string) string {
	dir := t.TempDir()
	cg := func(rows map[string][3]uint64) string {
		var b strings.Builder
		for name, v := range rows {
			b.WriteString(name + " usage_usec=" + itoa(v[0]) + " user_usec=" + itoa(v[0]*6/10) + " system_usec=" + itoa(v[0]*4/10) + " nr_throttled=" + itoa(v[1]) + " throttled_usec=0\n")
		}
		return b.String()
	}
	pg, vk := "aegis-postgres", "aegis-valkey"
	if layout == "docker" {
		write(t, filepath.Join(dir, "containers.txt"), "abc aegis-postgres\ndef aegis-valkey\n")
		pg, vk = "docker-abc.scope", "docker-def.scope"
	} else {
		pg, vk = "postgresql@18-main.service", "valkey-server.service"
	}
	a := map[string][3]uint64{"aegis-public.service": {1_000_000, 0}, "aegis-admin.service": {1_000_000, 0}, "aegis-node.service": {10_000_000, 5},
		pg: {20_000_000, 0}, vk: {2_000_000, 0}, "nginx.service": {3_000_000, 0}, "docker.service": {1_000_000, 0}}
	b := map[string][3]uint64{"aegis-public.service": {1_100_000, 0}, "aegis-admin.service": {1_100_000, 0}, "aegis-node.service": {17_000_000, 30},
		pg: {30_000_000, 0}, vk: {3_000_000, 0}, "nginx.service": {5_000_000, 0}, "docker.service": {1_500_000, 0}}
	write(t, filepath.Join(dir, "cpu-A/at"), "1000.0\n")
	write(t, filepath.Join(dir, "cpu-B/at"), "1100.0\n")
	write(t, filepath.Join(dir, "cpu-A/procstat"), "cpu  1000 0 500 90000 100 0 50 10 0 0\n")
	write(t, filepath.Join(dir, "cpu-B/procstat"), "cpu  2200 0 1000 98000 140 0 150 30 0 0\n")
	write(t, filepath.Join(dir, "cpu-A/cgroups"), cg(a))
	write(t, filepath.Join(dir, "cpu-B/cgroups"), cg(b))
	write(t, filepath.Join(dir, "cpu-A/pidstat"), pidA)
	write(t, filepath.Join(dir, "cpu-B/pidstat"), pidB)
	mem := func(avail int, swapIn int) string {
		return "2026-10-08T01:00:00Z\n               total        used        free      shared  buff/cache   available\nMem:            3915         900         100         100        2900        " + itoa(uint64(avail/1024)) + "\n" +
			"MemTotal:        4009980 kB\nMemAvailable:    " + itoa(uint64(avail)) + " kB\nSwapTotal:       8089596 kB\nSwapFree:        8082636 kB\npswpin " + itoa(uint64(swapIn)) + "\npswpout 0\n" +
			"== PSS(kB) by comm\naegis-node                 n=1   pss_kb=43356    rss_kb=43364\npostgres                   n=24  pss_kb=184299   rss_kb=790832\n== docker stats\nx 1MiB / 2MiB 1%\n"
	}
	write(t, filepath.Join(dir, "mem-before.txt"), mem(3_000_000, 0))
	write(t, filepath.Join(dir, "mem-after.txt"), mem(2_900_000, 0))
	write(t, filepath.Join(dir, "vmstat.txt"), `procs -----------memory---------- ---swap-- -----io---- -system-- -------cpu------- -----timestamp-----
 r  b   swpd   free   buff  cache   si   so    bi    bo   in   cs us sy id wa st gu                 UTC
 1  0   6960 182712  52948 3292524    0    0   109   469 1262   12  7  5 88  0  0  0 2026-10-07 12:21:15
 0  0   6960 181940  52952 3292632    0    0     0   107 2068 3000  6  4 90  0  1  0 2026-10-07 12:21:20
 0  0   6960 181940  52952 3292632    0    0     0   107 2068 5000  4  4 88  0  3  0 2026-10-07 12:21:25
`)
	return dir
}

func itoa(v uint64) string { return strconv.FormatUint(v, 10) }

func TestReportMatchesHandComputedNumbersForBothLayouts(t *testing.T) {
	for _, layout := range []string{"docker", "native"} {
		t.Run(layout, func(t *testing.T) {
			dir := fixture(t, layout)
			a, err := loadCPUSnapshot(dir, "A")
			if err != nil {
				t.Fatal(err)
			}
			b, err := loadCPUSnapshot(dir, "B")
			if err != nil {
				t.Fatal(err)
			}
			r, err := computeCPU(a, b, loadContainers(dir))
			if err != nil {
				t.Fatal(err)
			}
			near(t, "window", r.window, 100)
			// /proc/stat：user +1200 tick / 100 s = 12，system 5，softirq 1，steal 0.2
			near(t, "user", r.stat["user"], 12)
			near(t, "system", r.stat["system"], 5)
			near(t, "busy", r.busy, 12+5+1+0.2)
			v := judgeCPU(r, DefaultCPULimit)
			// usage_usec 增量 / 100 s / 1e4：public 0.1、admin 0.1、node 7、postgres 10、valkey 1
			near(t, "node", v.roles["aegis-node.service"], 7)
			near(t, "postgres", v.roles["postgres"], 10)
			near(t, "valkey", v.roles["valkey"], 1)
			near(t, "panel+db", v.panelDB, 0.1+0.1+7+10+1)
			near(t, "nginx", v.nginx, 2)
			if !v.cpuPass || len(v.problems) != 0 {
				t.Fatalf("verdict %+v", v)
			}
			if got := judgeCPU(r, 15).cpuPass; got {
				t.Fatal("18.2 must fail a limit of 15")
			}
			// 进程：node 的 utime+stime 增量 (1600+800-1500)=900/100=9；"a b) c" 含括号与空格，own 增量 100/100=1，子进程 40/100；新进程整段计入 100/100=1
			byName := map[string]procRow{}
			for _, p := range r.procs {
				byName[p.comm] = p
			}
			near(t, "aegis-node ticks", byName["aegis-node"].own, 9)
			near(t, "paren comm own", byName["a b) c"].own, 1)
			near(t, "paren comm child", byName["a b) c"].child, 0.4)
			near(t, "new process", byName["new"].own, 1)
		})
	}
}

func TestRunPrintsVerdictsAndStrictFailsOnTheLimits(t *testing.T) {
	dir := fixture(t, "docker")
	// 夹具内存：MemTotal 4009980 kB − 可用 2900000 kB ≈ 1084 MiB，高于 1024
	var out bytes.Buffer
	if err := run([]string{"-dir", dir}, &out); err != nil {
		t.Fatalf("without -strict the report is informational: %v", err)
	}
	text := out.String()
	for _, want := range []string{"**合计 18.20**（标准 ≤ 30）→ 过", "- postgres: 10.00", "整机已用 1084 MiB", "→ 不过", "上下文切换 4000/s", "窗口 100 秒"} {
		if !strings.Contains(text, want) {
			t.Fatalf("report lacks %q:\n%s", want, text)
		}
	}
	if err := run([]string{"-dir", dir, "-strict"}, &bytes.Buffer{}); err == nil {
		t.Fatal("-strict must fail when memory is over the limit")
	}
	if err := run([]string{"-dir", dir, "-strict", "-mem-limit-mib", "2048"}, &bytes.Buffer{}); err != nil {
		t.Fatalf("with a 2 GiB ceiling the same data passes: %v", err)
	}
	if err := run([]string{"-dir", dir, "-strict", "-mem-limit-mib", "2048", "-cpu-limit", "10"}, &bytes.Buffer{}); err == nil {
		t.Fatal("-strict must fail when CPU is over the limit")
	}
}

func TestMissingGatewayIsFlaggedNotSilentlyZero(t *testing.T) {
	dir := fixture(t, "native")
	raw, _ := os.ReadFile(filepath.Join(dir, "cpu-B/cgroups"))
	var keep []string
	for _, l := range strings.Split(string(raw), "\n") {
		if !strings.HasPrefix(l, "aegis-node.service") {
			keep = append(keep, l)
		}
	}
	write(t, filepath.Join(dir, "cpu-B/cgroups"), strings.Join(keep, "\n"))
	a, _ := loadCPUSnapshot(dir, "A")
	b, _ := loadCPUSnapshot(dir, "B")
	r, _ := computeCPU(a, b, nil)
	v := judgeCPU(r, DefaultCPULimit)
	if v.cpuPass || len(v.problems) == 0 || !strings.Contains(v.problems[0], "aegis-node.service") {
		t.Fatalf("a missing gateway cgroup must fail the verdict loudly: %+v", v)
	}
}

func TestSwapActivityIsCalledOut(t *testing.T) {
	dir := fixture(t, "docker")
	raw, _ := os.ReadFile(filepath.Join(dir, "mem-after.txt"))
	write(t, filepath.Join(dir, "mem-after.txt"), strings.Replace(string(raw), "pswpin 0", "pswpin 77", 1))
	var out bytes.Buffer
	_ = run([]string{"-dir", dir}, &out)
	if !strings.Contains(out.String(), "发生了换页（换入 77") {
		t.Fatalf("swap-in during the window must be flagged:\n%s", out.String())
	}
}

func TestBackwardsSnapshotsAreRefused(t *testing.T) {
	dir := fixture(t, "docker")
	write(t, filepath.Join(dir, "cpu-B/at"), "900.0\n")
	var out bytes.Buffer
	if err := run([]string{"-dir", dir}, &out); err == nil {
		t.Fatal("snapshot B before A must be an error")
	}
}
