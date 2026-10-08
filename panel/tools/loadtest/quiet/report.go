// Package quiet 是 quiet-report 子命令：读静默测量的采集产物（scripts/quiet-collect.sh 写的目录），
// 算出面板 + 数据库的 CPU（单核 = 100）与整机已用内存，并按标准判过或不过。
//
// 口径与 2026-10-07 的 5k-r4 静默轮次（ops-local/vultr-test2/5k-r4/quiet/）一致：
//   - CPU 取窗口首尾两次快照做差；cgroup 的 usage_usec 含已退出的子进程，是最准的一种，判定用它；
//     /proc/stat 整机与按进程名汇总作旁证，用来发现 cgroup 之外的开销。
//   - 面板 + 数据库 = 三个网关 + postgres + valkey；nginx、docker、containerd 单列，不进判定。
//   - 内存用窗口外首尾各一次的 MemTotal − MemAvailable（free 的 used，不含 cache）。
package quiet

import (
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
)

// 静默标准（用户 2026-10-07 / 10-08 定）：面板 + 数据库 ≤ 单核 30%，整机已用内存 ≤ 1 GiB。
const (
	DefaultCPULimit    = 30.0
	DefaultMemLimitMiB = 1024.0
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
	child float64
}

// cpuReport 是窗口内的 CPU 拆分，全部以单核 = 100 为单位。
type cpuReport struct {
	window float64
	stat   map[string]float64
	busy   float64
	units  []unitCPU
	procs  []procRow
}

// computeCPU 对 A、B 两份快照做差。containers 把 docker 容器的 cgroup 单元翻成容器名。
func computeCPU(a, b *cpuSnapshot, containers map[string]string) (*cpuReport, error) {
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
		label := name
		if c, ok := containers[name]; ok {
			label = c
		}
		pct := func(key string) float64 { return float64(sub(bv[key], av[key])) / dt / 1e4 }
		r.units = append(r.units, unitCPU{
			name: label, total: pct("usage_usec"), user: pct("user_usec"), system: pct("system_usec"),
			throttled: sub(bv["nr_throttled"], av["nr_throttled"]),
		})
	}
	sort.Slice(r.units, func(i, j int) bool {
		if r.units[i].total != r.units[j].total {
			return r.units[i].total > r.units[j].total
		}
		return r.units[i].name < r.units[j].name
	})

	own, child := map[string]uint64{}, map[string]uint64{}
	for pid, bp := range b.pids {
		if ap, ok := a.pids[pid]; ok && ap.comm == bp.comm {
			own[bp.comm] += sub(bp.own, ap.own)
			child[bp.comm] += sub(bp.child, ap.child)
		} else {
			// 窗口内新出现的进程整段计入
			own[bp.comm] += bp.own
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

func sub(b, a uint64) uint64 {
	if b < a {
		return 0
	}
	return b - a
}

// panelRoles 把 cgroup 单元归到判定用的角色。docker 布局里单元是容器名（aegis-postgres / aegis-valkey），
// 直装布局里是 postgresql@<版本>-main.service、valkey-server.service（或 redis-server.service）。
func roleOf(unit string) string {
	switch {
	case unit == "aegis-public.service" || unit == "aegis-admin.service" || unit == "aegis-node.service":
		return unit
	case unit == "aegis-postgres" || strings.HasPrefix(unit, "postgresql"):
		return "postgres"
	case unit == "aegis-valkey" || strings.HasPrefix(unit, "valkey") || strings.HasPrefix(unit, "redis"):
		return "valkey"
	}
	return ""
}

type verdict struct {
	roles    map[string]float64 // 角色 → CPU
	members  map[string][]string
	panelDB  float64
	nginx    float64
	docker   float64
	contd    float64
	cpuPass  bool
	limit    float64
	problems []string
}

func judgeCPU(r *cpuReport, limit float64) verdict {
	v := verdict{roles: map[string]float64{}, members: map[string][]string{}, limit: limit}
	for _, u := range r.units {
		if role := roleOf(u.name); role != "" {
			v.roles[role] += u.total
			v.members[role] = append(v.members[role], u.name)
		}
		switch u.name {
		case "nginx.service":
			v.nginx = u.total
		case "docker.service":
			v.docker = u.total
		case "containerd.service":
			v.contd = u.total
		}
	}
	for _, g := range gatewayUnits {
		if _, ok := v.roles[g]; !ok {
			v.problems = append(v.problems, fmt.Sprintf("no cgroup entry for %s: the collector did not see the gateway unit", g))
		}
	}
	for _, role := range []string{"postgres", "valkey"} {
		if _, ok := v.roles[role]; !ok {
			v.problems = append(v.problems, fmt.Sprintf("no cgroup entry for %s: containers.txt / unit names did not match", role))
		}
	}
	for _, g := range gatewayUnits {
		v.panelDB += v.roles[g]
	}
	v.panelDB += v.roles["postgres"] + v.roles["valkey"]
	v.cpuPass = v.panelDB <= limit && len(v.problems) == 0
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
	pass := "过"
	if !v.cpuPass {
		pass = "不过"
	}
	fmt.Fprintf(w, "- **合计 %.2f**（标准 ≤ %.0f）→ %s\n", v.panelDB, v.limit, pass)
	fmt.Fprintf(w, "- nginx 单列：%.2f；docker.service（含 docker-proxy）：%.2f；containerd：%.2f\n", v.nginx, v.docker, v.contd)
	fmt.Fprintf(w, "- 含 nginx：%.2f\n", v.panelDB+v.nginx)
	for _, p := range v.problems {
		fmt.Fprintf(w, "- **注意**：%s\n", p)
	}
	fmt.Fprintln(w)

	fmt.Fprint(w, "## 按进程名（活进程 utime+stime；单核 = 100）\n\n| 进程名 | CPU | 已回收子进程 |\n|---|---|---|\n")
	var ownSum, childSum float64
	for i, p := range r.procs {
		ownSum += p.own
		childSum += p.child
		if i < 15 {
			fmt.Fprintf(w, "| %s | %.2f | %.2f |\n", p.comm, p.own, p.child)
		}
	}
	fmt.Fprintf(w, "| 合计 | %.2f | %.2f |\n", ownSum, childSum)
}

// Main 是 quiet-report 的入口。
func Main(args []string) error { return run(args, os.Stdout) }

func run(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("quiet-report", flag.ContinueOnError)
	dir := fs.String("dir", "", "collector output directory with cpu-A/, cpu-B/, mem-before.txt, mem-after.txt (required)")
	tagA := fs.String("a", "A", "label of the first CPU snapshot (directory cpu-<label>)")
	tagB := fs.String("b", "B", "label of the second CPU snapshot")
	cpuLimit := fs.Float64("cpu-limit", DefaultCPULimit, "panel + database CPU ceiling, single core = 100")
	memLimit := fs.Float64("mem-limit-mib", DefaultMemLimitMiB, "whole-machine used memory ceiling in MiB (MemTotal - MemAvailable, no cache)")
	strict := fs.Bool("strict", false, "exit non-zero when the CPU or memory standard is not met")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *dir == "" {
		fs.Usage()
		return fmt.Errorf("-dir is required")
	}
	a, err := loadCPUSnapshot(*dir, *tagA)
	if err != nil {
		return err
	}
	b, err := loadCPUSnapshot(*dir, *tagB)
	if err != nil {
		return err
	}
	cpu, err := computeCPU(a, b, loadContainers(*dir))
	if err != nil {
		return err
	}
	v := judgeCPU(cpu, *cpuLimit)
	writeCPU(stdout, cpu, v)

	memPass := true
	memBefore, errB := loadMemSnapshot(*dir + "/mem-before.txt")
	memAfter, errA := loadMemSnapshot(*dir + "/mem-after.txt")
	switch {
	case errB != nil || errA != nil:
		fmt.Fprintf(stdout, "\n## 内存\n\n没有可用的内存快照（mem-before.txt / mem-after.txt）：%v %v\n", errB, errA)
		memPass = false
	default:
		memPass = writeMem(stdout, memBefore, memAfter, *memLimit)
	}
	if vm, err := loadVmstat(*dir + "/vmstat.txt"); err == nil {
		writeVmstat(stdout, vm)
	}
	if *strict && !(v.cpuPass && memPass) {
		return fmt.Errorf("quiet standard not met (cpu pass=%v, memory pass=%v)", v.cpuPass, memPass)
	}
	return nil
}
