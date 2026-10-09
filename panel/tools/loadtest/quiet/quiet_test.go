package quiet

import (
	"bytes"
	"encoding/json"
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
			v := judgeCPU(r, "B", tiers["B"].cpu, tiers["B"].nginx)
			// usage_usec 增量 / 100 s / 1e4：public 0.1、admin 0.1、node 7、postgres 10、valkey 1
			near(t, "node", v.roles["aegis-node.service"], 7)
			near(t, "postgres", v.roles["postgres"], 10)
			near(t, "valkey", v.roles["valkey"], 1)
			near(t, "panel+db", v.panelDB, 0.1+0.1+7+10+1)
			near(t, "nginx", v.nginx, 2)
			if !v.cpuPass || !v.nginxPass || len(v.problems) != 0 {
				t.Fatalf("verdict %+v", v)
			}
			if got := judgeCPU(r, "A", tiers["A"].cpu, tiers["A"].nginx); got.cpuPass || !got.nginxPass {
				t.Fatalf("18.2 must fail tier A's 10 while nginx 2 passes its 3: %+v", got)
			}
			if got := judgeCPU(r, "B", 25, 1.5); got.nginxPass {
				t.Fatal("nginx 2 must fail a ceiling of 1.5")
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
	for _, want := range []string{"**合计 18.20**（B 档标准 ≤ 25）→ 过", "**nginx 单列 2.00**（B 档标准 ≤ 6）→ 过", "- postgres: 10.00",
		"整机已用 1084 MiB**（标准 ≤ 950）→ 不过", "换入、换出都为 0 → 过", "上下文切换 4000/s", "窗口 100 秒"} {
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
	if err := run([]string{"-dir", dir, "-strict", "-mem-limit-mib", "2048", "-tier", "A"}, &bytes.Buffer{}); err == nil {
		t.Fatal("-strict must fail tier A: 18.2 is over 10")
	}
	if err := run([]string{"-dir", dir, "-tier", "C"}, &bytes.Buffer{}); err == nil {
		t.Fatal("an unknown tier must be refused")
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
	v := judgeCPU(r, "B", tiers["B"].cpu, tiers["B"].nginx)
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
	if !strings.Contains(out.String(), "窗口内换入 77、换出 0 页（标准都为 0）→ 不过") {
		t.Fatalf("swap-in during the window must be flagged:\n%s", out.String())
	}
	if err := run([]string{"-dir", dir, "-strict", "-mem-limit-mib", "2048"}, &bytes.Buffer{}); err == nil {
		t.Fatal("-strict must fail on any page swapped in during the window, even under the used-memory ceiling")
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

// 窗口内退出的子进程：父进程的 cutime 增量里有它开窗前的累计，要扣掉，只留窗口内的部分（w10quiet v3 的 postgres
// 5.42 里 3.81 是开窗前的）；kworker 换了工作队列 comm 会变，按 starttime 认同一个进程，不当成新进程整段计入。
func TestReapedChildrenCountOnlyTheirInWindowTicks(t *testing.T) {
	dir := fixture(t, "native")
	line := func(pid, ppid int, comm string, own, child, start uint64) string {
		return itoa(uint64(pid)) + " (" + comm + ") S " + itoa(uint64(ppid)) + " 0 0 0 -1 0 0 0 0 0 " + itoa(own) + " 0 " + itoa(child) + " 0 20 0 1 0 " + itoa(start) + " 0 0\n"
	}
	write(t, filepath.Join(dir, "cpu-A/pidstat"), line(10, 1, "postgres", 100, 0, 5)+line(11, 10, "postgres", 300, 0, 6)+line(12, 2, "kworker/0:1-events", 10, 0, 7))
	write(t, filepath.Join(dir, "cpu-B/pidstat"), line(10, 1, "postgres", 150, 350, 5)+line(12, 2, "kworker/0:1-mm_percpu_wq", 20, 0, 7))
	a, _ := loadCPUSnapshot(dir, "A")
	b, _ := loadCPUSnapshot(dir, "B")
	r, err := computeCPU(a, b, nil)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]procRow{}
	for _, p := range r.procs {
		byName[p.comm] = p
	}
	// 父进程自己 +50 tick；子进程开窗前 300、窗口内又跑 50 后退出，cutime +350 里只有 50 属于窗口
	near(t, "postgres own", byName["postgres"].own, 0.5)
	near(t, "postgres reaped", byName["postgres"].child, 0.5)
	near(t, "kworker counted by starttime", byName["kworker/0:1-mm_percpu_wq"].own, 0.1)
	if _, ok := byName["kworker/0:1-events"]; ok {
		t.Fatal("the old kworker comm must not appear as a separate row")
	}
}

func TestJSONCarriesTheVerdictForPerfGate(t *testing.T) {
	dir := fixture(t, "docker")
	var out bytes.Buffer
	if err := run([]string{"-dir", dir, "-json", "-tier", "A"}, &out); err != nil {
		t.Fatal(err)
	}
	var got jsonReport
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out.String())
	}
	near(t, "panel_db", got.CPU.PanelDB, 18.2)
	near(t, "nginx", got.CPU.Nginx, 2)
	if got.Tier != "A" || got.CPU.Limit != 10 || got.CPU.Pass || !got.CPU.NginxPass || got.Pass {
		t.Fatalf("tier A verdict wrong: %+v", got)
	}
	if got.CPU.Throttled["aegis-node.service"] != 25 {
		t.Fatalf("throttled = %v, want aegis-node.service 25", got.CPU.Throttled)
	}
	if got.Mem == nil || math.Round(got.Mem.UsedMiB) != 1084 || got.Mem.UsedPass || !got.Mem.SwapPass || got.Mem.PSSAfterMiB["postgres"] < 179 {
		t.Fatalf("memory part wrong: %+v", got.Mem)
	}
}
