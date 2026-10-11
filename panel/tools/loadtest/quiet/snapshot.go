package quiet

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// 采集脚本（scripts/quiet-collect.sh）在窗口首尾各留一份 CPU 快照、窗口外首尾各留一份内存快照；
// 这个包只读这些文件做差，不碰 /proc，所以能在任何机器上离线重算。

// 每秒时钟数：/proc/stat 与 /proc/<pid>/stat 的 tick 单位。Linux 的 USER_HZ 在所有主流架构上固定为 100，
// 也就是 1 tick/秒 = 单核的 1 个百分点，和 top 的进程列同口径。
const userHZ = 100.0

// cpuStat 是 /proc/stat 第一行（整机）的前八个计数。
var cpuFields = []string{"user", "nice", "system", "idle", "iowait", "irq", "softirq", "steal"}

type procTicks struct {
	comm  string
	ppid  int
	start uint64 // starttime（开机以来的 tick）：同一 pid 前后两份的 start 相同才是同一个进程
	own   uint64 // utime + stime
	child uint64 // cutime + cstime：已回收的子进程
}

type cpuSnapshot struct {
	at      float64 // 取快照的 unix 秒（带小数）
	stat    map[string]uint64
	cgroups map[string]map[string]uint64
	pids    map[int]procTicks
}

func loadCPUSnapshot(dir, tag string) (*cpuSnapshot, error) {
	base := filepath.Join(dir, "cpu-"+tag)
	s := &cpuSnapshot{stat: map[string]uint64{}, cgroups: map[string]map[string]uint64{}, pids: map[int]procTicks{}}

	raw, err := os.ReadFile(filepath.Join(base, "at"))
	if err != nil {
		return nil, fmt.Errorf("cpu snapshot %q: %w", tag, err)
	}
	if s.at, err = strconv.ParseFloat(strings.TrimSpace(string(raw)), 64); err != nil {
		return nil, fmt.Errorf("cpu snapshot %q: bad timestamp: %w", tag, err)
	}

	raw, err = os.ReadFile(filepath.Join(base, "procstat"))
	if err != nil {
		return nil, fmt.Errorf("cpu snapshot %q: %w", tag, err)
	}
	f := strings.Fields(string(raw))
	if len(f) < 1+len(cpuFields) || f[0] != "cpu" {
		return nil, fmt.Errorf("cpu snapshot %q: procstat is not the aggregate cpu line", tag)
	}
	for i, name := range cpuFields {
		if s.stat[name], err = strconv.ParseUint(f[1+i], 10, 64); err != nil {
			return nil, fmt.Errorf("cpu snapshot %q: procstat %s: %w", tag, name, err)
		}
	}

	if err := eachLine(filepath.Join(base, "cgroups"), func(line string) {
		parts := strings.Fields(line)
		if len(parts) < 2 {
			return
		}
		kv := map[string]uint64{}
		for _, p := range parts[1:] {
			k, v, ok := strings.Cut(p, "=")
			if !ok {
				continue
			}
			if n, err := strconv.ParseUint(v, 10, 64); err == nil {
				kv[k] = n
			}
		}
		s.cgroups[parts[0]] = kv
	}); err != nil {
		return nil, fmt.Errorf("cpu snapshot %q: %w", tag, err)
	}

	if err := eachLine(filepath.Join(base, "pidstat"), func(line string) {
		if pid, t, ok := parsePIDStat(line); ok {
			s.pids[pid] = t
		}
	}); err != nil {
		return nil, fmt.Errorf("cpu snapshot %q: %w", tag, err)
	}
	return s, nil
}

// parsePIDStat 解 /proc/<pid>/stat 一行：comm 可能含空格与括号，从最后一个 ')' 之后数字段。
// ')' 之后第 1 个字段是 state，第 2 个是 ppid，utime / stime / cutime / cstime 是第 12 至 15 个，starttime 是第 20 个。
func parsePIDStat(line string) (int, procTicks, bool) {
	open, closeIdx := strings.IndexByte(line, '('), strings.LastIndexByte(line, ')')
	if open < 0 || closeIdx < open {
		return 0, procTicks{}, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(line[:open]))
	if err != nil {
		return 0, procTicks{}, false
	}
	rest := strings.Fields(line[closeIdx+1:])
	if len(rest) < 20 {
		return 0, procTicks{}, false
	}
	n := func(i int) uint64 { v, _ := strconv.ParseUint(rest[i], 10, 64); return v }
	ppid, _ := strconv.Atoi(rest[1])
	return pid, procTicks{comm: line[open+1 : closeIdx], ppid: ppid, start: n(19), own: n(11) + n(12), child: n(13) + n(14)}, true
}

func eachLine(path string, fn func(string)) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		if line := strings.TrimRight(sc.Text(), "\r"); strings.TrimSpace(line) != "" {
			fn(line)
		}
	}
	return sc.Err()
}

// ---------------------------------------------------------------------------
// 内存快照
// ---------------------------------------------------------------------------

type pssRow struct {
	comm      string
	n         int
	pssKB     int64
	rssKB     int64
	pssMiB    float64
	hasValues bool
}

type memSnapshot struct {
	taken         string // 文件第一行的时间戳
	freeUsedMiB   int64  // free -m 的 Mem 行 used 列；没有 free 输出时为 -1
	totalKB       int64
	availKB       int64
	swapTotalKB   int64
	swapFreeKB    int64
	swapIn        int64
	swapOut       int64
	pss           []pssRow
	haveMeminfo   bool
	haveSwapPages bool
}

// usedMiB 是整机已用：MemTotal − MemAvailable，等于 free 的 used（不含 cache）。
func (m *memSnapshot) usedMiB() float64 { return float64(m.totalKB-m.availKB) / 1024 }

func (m *memSnapshot) swapUsedMiB() float64 { return float64(m.swapTotalKB-m.swapFreeKB) / 1024 }

var pssLine = regexp.MustCompile(`^(\S+)\s+n=(\d+)\s+pss_kb=(\d+)\s+rss_kb=(\d+)`)

func loadMemSnapshot(path string) (*memSnapshot, error) {
	m := &memSnapshot{freeUsedMiB: -1}
	first := true
	section := ""
	err := eachLine(path, func(line string) {
		if first {
			m.taken, first = strings.TrimSpace(line), false
			return
		}
		if strings.HasPrefix(line, "== ") {
			section = strings.TrimSpace(strings.TrimPrefix(line, "== "))
			return
		}
		f := strings.Fields(line)
		switch {
		case section != "":
			if section == "PSS(kB) by comm" {
				if g := pssLine.FindStringSubmatch(line); g != nil {
					n, _ := strconv.Atoi(g[2])
					pss, _ := strconv.ParseInt(g[3], 10, 64)
					rss, _ := strconv.ParseInt(g[4], 10, 64)
					m.pss = append(m.pss, pssRow{comm: g[1], n: n, pssKB: pss, rssKB: rss, pssMiB: float64(pss) / 1024, hasValues: true})
				}
			}
		case len(f) >= 3 && f[0] == "Mem:":
			// free -m：total used free shared buff/cache available
			if v, err := strconv.ParseInt(f[2], 10, 64); err == nil {
				m.freeUsedMiB = v
			}
		case len(f) >= 2 && strings.HasSuffix(f[0], ":"):
			v, err := strconv.ParseInt(f[1], 10, 64)
			if err != nil {
				return
			}
			switch f[0] {
			case "MemTotal:":
				m.totalKB, m.haveMeminfo = v, true
			case "MemAvailable:":
				m.availKB = v
			case "SwapTotal:":
				m.swapTotalKB = v
			case "SwapFree:":
				m.swapFreeKB = v
			}
		case len(f) == 2 && (f[0] == "pswpin" || f[0] == "pswpout"):
			v, err := strconv.ParseInt(f[1], 10, 64)
			if err != nil {
				return
			}
			m.haveSwapPages = true
			if f[0] == "pswpin" {
				m.swapIn = v
			} else {
				m.swapOut = v
			}
		}
	})
	if err != nil {
		return nil, err
	}
	if !m.haveMeminfo || m.availKB == 0 {
		return nil, fmt.Errorf("%s has no MemTotal / MemAvailable lines", path)
	}
	return m, nil
}
