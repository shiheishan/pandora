// Package quiet 是 quiet-report 子命令：读静默测量的采集产物（scripts/quiet-collect.sh 写的目录），
// 算出面板 + 数据库的 CPU（单核 = 100）与整机已用内存，并按标准判过或不过。
//
// 口径与 2026-10-07 的 5k-r4 静默轮次（ops-local/vultr-test2/5k-r4/quiet/）一致：
//   - CPU 取窗口首尾两次快照做差；cgroup 的 usage_usec 含已退出的子进程，是最准的一种，判定用它；
//     /proc/stat 整机与按进程名汇总作旁证，用来发现 cgroup 之外的开销。
//   - 面板 + 数据库 = 三个网关 + postgres + valkey；nginx 单列、有自己的线。
//   - 只认直装布局（deploy/install.sh）的 systemd 单元名；Docker 布局的旧结果不在这里读。
//   - 内存用窗口外首尾各一次的 MemTotal − MemAvailable（free 的 used，不含 cache），窗口内换入、换出都要为 0。
package quiet

import (
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
)

// 静默标准（用户 2026-10-09 定，取代 10-07 的「≤ 30、≤ 1 GiB」）：两档，单核 = 100，2c4g 也必须达标。
//   - A 档完全静默（run-quiet.sh ONLINE_RATIO=0）：面板 + 数据库 ≤ 10，nginx ≤ 3；
//   - B 档 30% 在线（缺省 0.3）：面板 + 数据库 ≤ 25（push 计费 N2 第 7 项查清后收到 ≤ 20），nginx ≤ 6；
//   - 两档相同：整机已用 ≤ 950 MiB（不改内核参数），窗口内换入、换出都为 0。
//
// 改这里要同步 prod-retest skill 的静默一节与 loadtest README 第 11 节。
type tierLimits struct{ cpu, nginx float64 }

var tiers = map[string]tierLimits{"A": {cpu: 10, nginx: 3}, "B": {cpu: 25, nginx: 6}}

const (
	DefaultTier        = "B"
	DefaultMemLimitMiB = 950.0
)

// gatewayUnits 是三个网关的 systemd 单元名。
var gatewayUnits = []string{"aegis-public.service", "aegis-admin.service", "aegis-node.service"}

type unitCPU struct {
	name      string
	total     float64
	user      float64
	system    float64
	throttled uint64
}

type procRow struct {
	comm  string
	own   float64
	child float64 // 窗口内被回收的子进程 CPU（已扣掉它们开窗前的部分），可以直接和 own 相加
}

// cpuReport 是窗口内的 CPU 拆分，全部以单核 = 100 为单位。
type cpuReport struct {
	window float64
	stat   map[string]float64
	busy   float64
	units  []unitCPU
	procs  []procRow
}

// computeCPU 对 A、B 两份快照做差。
func computeCPU(a, b *cpuSnapshot) (*cpuReport, error) {
	dt := b.at - a.at
	if dt <= 0 {
		return nil, fmt.Errorf("snapshot B (%.1f) is not after A (%.1f)", b.at, a.at)
	}
	r := &cpuReport{window: dt, stat: map[string]float64{}}
	for _, k := range cpuFields {
		// 计数器回绕或重启后会出现 B < A：按 0 处理比出一个巨大的负数更容易看出问题
		r.stat[k] = float64(sub(b.stat[k], a.stat[k])) / dt
	}
	for _, k := range []string{"user", "nice", "system", "irq", "softirq", "steal"} {
		r.busy += r.stat[k]
	}

	for name, bv := range b.cgroups {
		av, ok := a.cgroups[name]
		if !ok {
			continue
		}
		if _, has := bv["usage_usec"]; !has {
			continue
		}
		pct := func(key string) float64 { return float64(sub(bv[key], av[key])) / dt / 1e4 }
		r.units = append(r.units, unitCPU{
			name: name, total: pct("usage_usec"), user: pct("user_usec"), system: pct("system_usec"),
			throttled: sub(bv["nr_throttled"], av["nr_throttled"]),
		})
	}
	sort.Slice(r.units, func(i, j int) bool {
		if r.units[i].total != r.units[j].total {
			return r.units[i].total > r.units[j].total
		}
		return r.units[i].name < r.units[j].name
	})

	// 窗口内退出的进程：开窗前已累计的 utime+stime（连同它自己回收过的子进程）会在被回收时整笔并入
	// 父进程的 cutime。父进程的 cutime 增量要扣掉这一段，否则窗口外的 CPU 会算进「已回收子进程」，
	// 和活进程那一列相加就重复计了（w10quiet v3：postgres 的 5.42 里 3.81 是 4 条后端开窗前的累计）。
	preReaped := map[int]uint64{}
	for pid, ap := range a.pids {
		if bp, ok := b.pids[pid]; ok && sameProcess(ap, bp) {
			continue
		}
		preReaped[ap.ppid] += ap.own + ap.child
	}
	own, child := map[string]uint64{}, map[string]uint64{}
	for pid, bp := range b.pids {
		if ap, ok := a.pids[pid]; ok && sameProcess(ap, bp) {
			own[bp.comm] += sub(bp.own, ap.own)
			child[bp.comm] += sub(sub(bp.child, ap.child), preReaped[pid])
		} else {
			// 窗口内新出现的进程（含 pid 被复用）整段计入，它回收的子进程也都生在窗口内
			own[bp.comm] += bp.own
			child[bp.comm] += bp.child
		}
	}
	for comm, t := range own {
		r.procs = append(r.procs, procRow{comm: comm, own: float64(t) / dt, child: float64(child[comm]) / dt})
	}
	sort.Slice(r.procs, func(i, j int) bool {
		if r.procs[i].own != r.procs[j].own {
			return r.procs[i].own > r.procs[j].own
		}
		return r.procs[i].comm < r.procs[j].comm
	})
	return r, nil
}

// sameProcess 判断前后两份是不是同一个进程：starttime 相同（pid 没被复用）。kworker 之类的
// comm 会随手上的工作队列变化，所以不拿 comm 比；读不到 starttime 的旧格式才退回比 comm。
func sameProcess(a, b procTicks) bool {
	if a.start != 0 || b.start != 0 {
		return a.start == b.start
	}
	return a.comm == b.comm
}

func sub(b, a uint64) uint64 {
	if b < a {
		return 0
	}
	return b - a
}

// roleOf 把 cgroup 单元归到判定用的角色：三个网关各自一角；postgresql@<版本>-main.service 是 postgres，
// valkey-server.service（Debian 12 上是 redis-server.service）是 valkey。
func roleOf(unit string) string {
	switch {
	case unit == "aegis-public.service" || unit == "aegis-admin.service" || unit == "aegis-node.service":
		return unit
	case strings.HasPrefix(unit, "postgresql"):
		return "postgres"
	case strings.HasPrefix(unit, "valkey") || strings.HasPrefix(unit, "redis"):
		return "valkey"
	}
	return ""
}

type verdict struct {
	tier       string
	roles      map[string]float64 // 角色 → CPU
	members    map[string][]string
	panelDB    float64
	nginx      float64
	cpuPass    bool
	limit      float64
	nginxLimit float64
	nginxPass  bool
	problems   []string
}

func judgeCPU(r *cpuReport, tier string, limit, nginxLimit float64) verdict {
	v := verdict{tier: tier, roles: map[string]float64{}, members: map[string][]string{}, limit: limit, nginxLimit: nginxLimit}
	for _, u := range r.units {
		if role := roleOf(u.name); role != "" {
			v.roles[role] += u.total
			v.members[role] = append(v.members[role], u.name)
		}
		if u.name == "nginx.service" {
			v.nginx = u.total
		}
	}
	for _, g := range gatewayUnits {
		if _, ok := v.roles[g]; !ok {
			v.problems = append(v.problems, fmt.Sprintf("no cgroup entry for %s: the collector did not see the gateway unit", g))
		}
	}
	for _, role := range []string{"postgres", "valkey"} {
		if _, ok := v.roles[role]; !ok {
			v.problems = append(v.problems, fmt.Sprintf("no cgroup entry for %s: no postgresql* / valkey* / redis* unit under system.slice", role))
		}
	}
	for _, g := range gatewayUnits {
		v.panelDB += v.roles[g]
	}
	v.panelDB += v.roles["postgres"] + v.roles["valkey"]
	v.cpuPass = v.panelDB <= limit && len(v.problems) == 0
	v.nginxPass = v.nginx <= nginxLimit
	return v
}

// ---------------------------------------------------------------------------
// 输出
// ---------------------------------------------------------------------------

func writeCPU(w io.Writer, r *cpuReport, v verdict) {
	fmt.Fprintf(w, "窗口 %.0f 秒\n\n", r.window)
	fmt.Fprint(w, "## /proc/stat（单核 = 100，两核整机 = 200）\n\n| 类别 | 单核百分点 |\n|---|---|\n")
	for _, k := range []string{"user", "nice", "system", "irq", "softirq", "steal", "iowait"} {
		fmt.Fprintf(w, "| %s | %.2f |\n", k, r.stat[k])
	}
	fmt.Fprintf(w, "| **忙（不含 idle/iowait）** | **%.2f** |\n\n", r.busy)

	fmt.Fprint(w, "## cgroup（system.slice 下各单元，含已退出的子进程）\n\n| 单元 | CPU（单核 = 100） | user | system | Δnr_throttled |\n|---|---|---|---|---|\n")
	var total float64
	for _, u := range r.units {
		total += u.total
		if u.total >= 0.01 {
			fmt.Fprintf(w, "| %s | %.2f | %.2f | %.2f | %d |\n", u.name, u.total, u.user, u.system, u.throttled)
		}
	}
	fmt.Fprintf(w, "| system.slice 合计 | %.2f | | | |\n\n", total)

	fmt.Fprint(w, "## 判定：面板 + 数据库（三网关 + postgres + valkey，cgroup 口径）\n\n")
	for _, g := range gatewayUnits {
		fmt.Fprintf(w, "- %s: %.2f\n", g, v.roles[g])
	}
	for _, role := range []string{"postgres", "valkey"} {
		fmt.Fprintf(w, "- %s: %.2f（%s）\n", role, v.roles[role], strings.Join(v.members[role], "、"))
	}
	fmt.Fprintf(w, "- **合计 %.2f**（%s 档标准 ≤ %g）→ %s\n", v.panelDB, v.tier, v.limit, passWord(v.cpuPass))
	fmt.Fprintf(w, "- **nginx 单列 %.2f**（%s 档标准 ≤ %g）→ %s\n", v.nginx, v.tier, v.nginxLimit, passWord(v.nginxPass))
	fmt.Fprintf(w, "- 含 nginx：%.2f\n", v.panelDB+v.nginx)
	for _, p := range v.problems {
		fmt.Fprintf(w, "- **注意**：%s\n", p)
	}
	fmt.Fprintln(w)

	fmt.Fprint(w, "## 按进程名（单核 = 100）\n\n")
	fmt.Fprint(w, "活进程是窗口内 utime+stime 的增量；已回收子进程是父进程 cutime+cstime 的增量扣掉窗口内退出的子进程开窗前的累计，只剩窗口内的部分，两列可以相加。\n\n")
	fmt.Fprint(w, "| 进程名 | 活进程 | 已回收子进程（窗口内） | 合计 |\n|---|---|---|---|\n")
	var ownSum, childSum float64
	for i, p := range r.procs {
		ownSum += p.own
		childSum += p.child
		if i < 15 {
			fmt.Fprintf(w, "| %s | %.2f | %.2f | %.2f |\n", p.comm, p.own, p.child, p.own+p.child)
		}
	}
	fmt.Fprintf(w, "| 全部进程 | %.2f | %.2f | %.2f |\n", ownSum, childSum, ownSum+childSum)
}

func passWord(ok bool) string {
	if ok {
		return "过"
	}
	return "不过"
}

// Main 是 quiet-report 的入口。
func Main(args []string) error { return run(args, os.Stdout) }

func run(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("quiet-report", flag.ContinueOnError)
	dir := fs.String("dir", "", "collector output directory with cpu-A/, cpu-B/, mem-before.txt, mem-after.txt (required)")
	tagA := fs.String("a", "A", "label of the first CPU snapshot (directory cpu-<label>)")
	tagB := fs.String("b", "B", "label of the second CPU snapshot")
	tier := fs.String("tier", DefaultTier, "quiet tier: A = fully quiet (run-quiet.sh with ONLINE_RATIO=0), B = 30% online (the run-quiet.sh default); picks the CPU and nginx ceilings")
	cpuLimit := fs.Float64("cpu-limit", 0, "override the tier's panel + database CPU ceiling, single core = 100")
	nginxLimit := fs.Float64("nginx-limit", 0, "override the tier's nginx CPU ceiling, single core = 100")
	memLimit := fs.Float64("mem-limit-mib", DefaultMemLimitMiB, "whole-machine used memory ceiling in MiB (MemTotal - MemAvailable, no cache)")
	asJSON := fs.Bool("json", false, "print the verdict as JSON (read by the perf-gate skill's verdict.py) instead of markdown")
	strict := fs.Bool("strict", false, "exit non-zero when the CPU, nginx or memory standard is not met")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *dir == "" {
		fs.Usage()
		return fmt.Errorf("-dir is required")
	}
	lim, ok := tiers[*tier]
	if !ok {
		return fmt.Errorf("-tier must be A or B, got %q", *tier)
	}
	if *cpuLimit > 0 {
		lim.cpu = *cpuLimit
	}
	if *nginxLimit > 0 {
		lim.nginx = *nginxLimit
	}
	a, err := loadCPUSnapshot(*dir, *tagA)
	if err != nil {
		return err
	}
	b, err := loadCPUSnapshot(*dir, *tagB)
	if err != nil {
		return err
	}
	cpu, err := computeCPU(a, b)
	if err != nil {
		return err
	}
	v := judgeCPU(cpu, *tier, lim.cpu, lim.nginx)

	var mem *memVerdict
	memBefore, errB := loadMemSnapshot(*dir + "/mem-before.txt")
	memAfter, errA := loadMemSnapshot(*dir + "/mem-after.txt")
	if errB == nil && errA == nil {
		m := judgeMem(memBefore, memAfter, *memLimit)
		mem = &m
	}
	vm, vmErr := loadVmstat(*dir + "/vmstat.txt")
	if vmErr != nil {
		vm = nil
	}

	if *asJSON {
		if err := writeJSON(stdout, cpu, v, mem); err != nil {
			return err
		}
	} else {
		writeCPU(stdout, cpu, v)
		if mem == nil {
			fmt.Fprintf(stdout, "\n## 内存\n\n没有可用的内存快照（mem-before.txt / mem-after.txt）：%v %v\n", errB, errA)
		} else {
			writeMem(stdout, *mem)
		}
		if vm != nil {
			writeVmstat(stdout, vm)
		}
	}
	memPass := mem != nil && mem.pass
	if *strict && !(v.cpuPass && v.nginxPass && memPass) {
		return fmt.Errorf("quiet standard (tier %s) not met (cpu pass=%v, nginx pass=%v, memory pass=%v)", *tier, v.cpuPass, v.nginxPass, memPass)
	}
	return nil
}
