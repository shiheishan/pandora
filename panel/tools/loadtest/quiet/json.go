package quiet

import (
	"encoding/json"
	"io"
	"math"
	"sort"
)

// -json 的输出：perf-gate skill 的 verdict.py 读它做改前改后对比，判定口径只在这个包里一份。
// 字段不改名；单位：CPU 单核 = 100，内存 MiB。docker / containerd 两列随 Docker 布局删掉了（verdict.py 不读）。
type jsonReport struct {
	Tier    string    `json:"tier"`
	WindowS float64   `json:"window_s"`
	Pass    bool      `json:"pass"`
	CPU     jsonCPU   `json:"cpu"`
	Mem     *jsonMem  `json:"mem"`
	Procs   []jsonRow `json:"procs"`
}

type jsonCPU struct {
	PanelDB    float64            `json:"panel_db"`
	Limit      float64            `json:"limit"`
	Pass       bool               `json:"pass"`
	Nginx      float64            `json:"nginx"`
	NginxLimit float64            `json:"nginx_limit"`
	NginxPass  bool               `json:"nginx_pass"`
	Roles      map[string]float64 `json:"roles"`
	Busy       float64            `json:"busy"`
	Steal      float64            `json:"steal"`
	// Throttled 是窗口内各 cgroup 单元的 Δnr_throttled（只列非 0 的）
	Throttled map[string]uint64 `json:"throttled"`
	Problems  []string          `json:"problems"`
}

type jsonMem struct {
	UsedBeforeMiB    float64            `json:"used_before_mib"`
	UsedAfterMiB     float64            `json:"used_after_mib"`
	UsedMiB          float64            `json:"used_mib"`
	LimitMiB         float64            `json:"limit_mib"`
	UsedPass         bool               `json:"used_pass"`
	SwapKnown        bool               `json:"swap_known"`
	SwapIn           int64              `json:"swap_in"`
	SwapOut          int64              `json:"swap_out"`
	SwapPass         bool               `json:"swap_pass"`
	SwapUsedAfterMiB float64            `json:"swap_used_after_mib"`
	MemTotalKB       int64              `json:"mem_total_kb"`
	Pass             bool               `json:"pass"`
	PSSAfterMiB      map[string]float64 `json:"pss_after_mib"`
}

type jsonRow struct {
	Comm   string  `json:"comm"`
	Own    float64 `json:"own"`
	Reaped float64 `json:"reaped"`
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }

func writeJSON(w io.Writer, r *cpuReport, v verdict, m *memVerdict) error {
	out := jsonReport{
		Tier:    v.tier,
		WindowS: round2(r.window),
		CPU: jsonCPU{
			PanelDB: round2(v.panelDB), Limit: v.limit, Pass: v.cpuPass,
			Nginx: round2(v.nginx), NginxLimit: v.nginxLimit, NginxPass: v.nginxPass,
			Roles: map[string]float64{},
			Busy:  round2(r.busy), Steal: round2(r.stat["steal"]),
			Throttled: map[string]uint64{}, Problems: append([]string{}, v.problems...),
		},
		Procs: []jsonRow{},
	}
	for role, c := range v.roles {
		out.CPU.Roles[role] = round2(c)
	}
	for _, u := range r.units {
		if u.throttled > 0 {
			out.CPU.Throttled[u.name] = u.throttled
		}
	}
	for _, p := range r.procs {
		if p.own+p.child >= 0.01 {
			out.Procs = append(out.Procs, jsonRow{Comm: p.comm, Own: round2(p.own), Reaped: round2(p.child)})
		}
	}
	sort.SliceStable(out.Procs, func(i, j int) bool { return out.Procs[i].Own > out.Procs[j].Own })
	if m != nil {
		jm := &jsonMem{
			UsedBeforeMiB: round2(m.before.usedMiB()), UsedAfterMiB: round2(m.after.usedMiB()),
			UsedMiB: round2(m.usedMiB), LimitMiB: m.limitMiB, UsedPass: m.usedPass,
			SwapKnown: m.haveSwapPages, SwapIn: m.swapIn, SwapOut: m.swapOut, SwapPass: m.swapPass,
			SwapUsedAfterMiB: round2(m.after.swapUsedMiB()), MemTotalKB: m.after.totalKB, Pass: m.pass,
			PSSAfterMiB: map[string]float64{},
		}
		for _, p := range m.after.pss {
			jm.PSSAfterMiB[p.comm] = round2(p.pssMiB)
		}
		out.Mem = jm
	}
	out.Pass = v.cpuPass && v.nginxPass && m != nil && m.pass
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}
