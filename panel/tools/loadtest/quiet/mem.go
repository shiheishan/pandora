package quiet

import (
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
)

// writeMem 打印内存对照并返回是否达标。整机已用取窗口外首尾两次里较大的一个（静默期内存只涨不降，
// 取大是对标准更严的读法）。
func writeMem(w io.Writer, before, after *memSnapshot, limitMiB float64) bool {
	worst := max(before.usedMiB(), after.usedMiB())
	pass := worst <= limitMiB
	fmt.Fprint(w, "\n## 内存（窗口外首尾各一次）\n\n| 项 | 前 | 后 |\n|---|---|---|\n")
	fmt.Fprintf(w, "| MemTotal − MemAvailable（MiB，= free 的 used，不含 cache） | %.0f | %.0f |\n", before.usedMiB(), after.usedMiB())
	if before.freeUsedMiB >= 0 && after.freeUsedMiB >= 0 {
		fmt.Fprintf(w, "| free -m 的 used（MiB） | %d | %d |\n", before.freeUsedMiB, after.freeUsedMiB)
	}
	fmt.Fprintf(w, "| swap 已用（MiB） | %.1f | %.1f |\n", before.swapUsedMiB(), after.swapUsedMiB())
	if before.haveSwapPages && after.haveSwapPages {
		fmt.Fprintf(w, "| 窗口内换入 / 换出页数 | %d / %d | |\n", after.swapIn-before.swapIn, after.swapOut-before.swapOut)
	}
	verdict := "过"
	if !pass {
		verdict = "不过"
	}
	fmt.Fprintf(w, "\n- **整机已用 %.0f MiB**（标准 ≤ %.0f）→ %s\n", worst, limitMiB, verdict)
	if before.swapIn != after.swapIn || before.swapOut != after.swapOut {
		fmt.Fprintf(w, "- **注意**：窗口内发生了换页（换入 %d、换出 %d 页），内存已吃紧，已用数被 swap 掩盖\n",
			after.swapIn-before.swapIn, after.swapOut-before.swapOut)
	}

	if len(before.pss) > 0 || len(after.pss) > 0 {
		merged := map[string][2]*pssRow{}
		for i := range before.pss {
			p := merged[before.pss[i].comm]
			p[0] = &before.pss[i]
			merged[before.pss[i].comm] = p
		}
		for i := range after.pss {
			p := merged[after.pss[i].comm]
			p[1] = &after.pss[i]
			merged[after.pss[i].comm] = p
		}
		names := make([]string, 0, len(merged))
		for k := range merged {
			names = append(names, k)
		}
		sort.Slice(names, func(i, j int) bool {
			return pssOf(merged[names[i]][1], merged[names[i]][0]) > pssOf(merged[names[j]][1], merged[names[j]][0])
		})
		fmt.Fprint(w, "\n### PSS（MiB，共享页按进程数均摊）\n\n| 进程 | 前 | 后 |\n|---|---|---|\n")
		var sumB, sumA float64
		for _, n := range names {
			p := merged[n]
			fmt.Fprintf(w, "| %s | %s | %s |\n", n, pssCell(p[0]), pssCell(p[1]))
			if p[0] != nil {
				sumB += p[0].pssMiB
			}
			if p[1] != nil {
				sumA += p[1].pssMiB
			}
		}
		fmt.Fprintf(w, "| 合计 | %.0f | %.0f |\n", sumB, sumA)
	}
	return pass
}

func pssOf(after, before *pssRow) float64 {
	if after != nil {
		return after.pssMiB
	}
	if before != nil {
		return before.pssMiB
	}
	return 0
}

func pssCell(p *pssRow) string {
	if p == nil {
		return "—"
	}
	return fmt.Sprintf("%.0f", p.pssMiB)
}

// ---------------------------------------------------------------------------
// vmstat
// ---------------------------------------------------------------------------

type vmstat struct {
	samples int
	us, sy  float64
	id, wa  float64
	st      float64
	stMax   float64
	cs      float64
}

// loadVmstat 读 `vmstat -n -t <间隔>` 的输出。第一行数据是开机以来的平均，不算；列位置按表头找，
// 新旧版 procps 的列数（有无 gu）不同。
func loadVmstat(path string) (*vmstat, error) {
	var header []string
	var rows [][]string
	err := eachLine(path, func(line string) {
		f := strings.Fields(line)
		switch {
		case len(f) > 0 && f[0] == "r" && header == nil:
			header = f
		case len(f) > 0 && f[0] != "procs" && header != nil:
			if _, err := strconv.Atoi(f[0]); err == nil {
				rows = append(rows, f)
			}
		}
	})
	if err != nil {
		return nil, err
	}
	col := map[string]int{}
	for i, h := range header {
		col[h] = i
	}
	for _, need := range []string{"cs", "us", "sy", "id", "wa", "st"} {
		if _, ok := col[need]; !ok {
			return nil, fmt.Errorf("vmstat header lacks column %q", need)
		}
	}
	if len(rows) < 2 {
		return nil, fmt.Errorf("vmstat has no samples after the since-boot line")
	}
	v := &vmstat{}
	for _, r := range rows[1:] {
		get := func(name string) float64 {
			if col[name] >= len(r) {
				return 0
			}
			x, _ := strconv.ParseFloat(r[col[name]], 64)
			return x
		}
		v.samples++
		v.us += get("us")
		v.sy += get("sy")
		v.id += get("id")
		v.wa += get("wa")
		st := get("st")
		v.st += st
		v.stMax = max(v.stMax, st)
		v.cs += get("cs")
	}
	n := float64(v.samples)
	v.us, v.sy, v.id, v.wa, v.st, v.cs = v.us/n, v.sy/n, v.id/n, v.wa/n, v.st/n, v.cs/n
	return v, nil
}

func writeVmstat(w io.Writer, v *vmstat) {
	fmt.Fprintf(w, "\n## vmstat（%d 个样本的平均，百分比按整机算）\n\n", v.samples)
	fmt.Fprintf(w, "- us %.1f、sy %.1f、id %.1f、wa %.1f、st %.1f（最大 %.0f）；上下文切换 %.0f/s\n", v.us, v.sy, v.id, v.wa, v.st, v.stMax, v.cs)
}
