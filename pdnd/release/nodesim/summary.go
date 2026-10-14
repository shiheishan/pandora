package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"
)

// VersionGroup 是当前处在同一名单版本的节点：多少个、这个版本最早和最晚什么时候生效。
type VersionGroup struct {
	Version    string
	Nodes      int
	AppliedMin time.Time
	AppliedMax time.Time
}

// VersionReach 是指定名单版本送达了多少节点（当前版本是它，或最近几次变化里出现过它）。
type VersionReach struct {
	Version    string
	Reached    int
	Total      int
	AppliedMin time.Time
	AppliedMax time.Time
	Missing    []string // "服务器名/节点id"，升序，最多 20 个
}

// Summary 是状态文件的汇总。
type Summary struct {
	Servers, Enrolled, Connected, Tombstoned, Errors, Nodes int

	FirstConnectedMin, FirstConnectedMax time.Time
	ConnectedMin, ConnectedMax           time.Time
	DisconnectedMax                      time.Time
	Fulls, Deltas                        int
	Versions                             []VersionGroup
	Target                               *VersionReach
}

func summarize(snap Snapshot, version string) Summary {
	var s Summary
	s.Servers = len(snap.Servers)
	s.Errors = snap.Errors

	serverNames := sortedKeys(snap.Servers)
	groupByVer := make(map[string]*VersionGroup)

	var missing []string
	if version != "" {
		s.Target = &VersionReach{Version: version}
	}

	for _, srvName := range serverNames {
		srv := snap.Servers[srvName]
		if srv == nil {
			continue
		}
		if srv.Enrolled {
			s.Enrolled++
		}
		if srv.Connected {
			s.Connected++
		}
		if srv.Tombstoned {
			s.Tombstoned++
		}
		updateTimeMinMax(&s.FirstConnectedMin, &s.FirstConnectedMax, srv.FirstConnectedAt)
		if srv.Connected {
			updateTimeMinMax(&s.ConnectedMin, &s.ConnectedMax, srv.ConnectedAt)
		}
		updateTimeMax(&s.DisconnectedMax, srv.DisconnectedAt)

		nodeIDs := sortedKeys(srv.Nodes)
		for _, nodeID := range nodeIDs {
			node := srv.Nodes[nodeID]
			if node == nil {
				continue
			}
			s.Nodes++
			s.Fulls += node.FullsReceived
			s.Deltas += node.DeltasReceived

			if node.RosterVersion != "" {
				g := groupByVer[node.RosterVersion]
				if g == nil {
					g = &VersionGroup{Version: node.RosterVersion}
					groupByVer[node.RosterVersion] = g
				}
				g.Nodes++
				updateTimeMinMax(&g.AppliedMin, &g.AppliedMax, node.RosterAppliedAt)
			}

			if version != "" {
				applied, reached := targetAppliedAt(node, version)
				if reached {
					s.Target.Reached++
					updateTimeMinMax(&s.Target.AppliedMin, &s.Target.AppliedMax, applied)
				} else {
					missing = append(missing, srvName+"/"+nodeID)
				}
			}
		}
	}

	s.Versions = versionGroupsFromMap(groupByVer)

	if version != "" {
		s.Target.Total = s.Nodes
		sort.Strings(missing)
		if len(missing) > 20 {
			missing = missing[:20]
		}
		s.Target.Missing = missing
	}

	return s
}

func versionGroupsFromMap(m map[string]*VersionGroup) []VersionGroup {
	out := make([]VersionGroup, 0, len(m))
	for _, g := range m {
		out = append(out, *g)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Nodes != out[j].Nodes {
			return out[i].Nodes > out[j].Nodes
		}
		return out[i].Version < out[j].Version
	})
	return out
}

// targetAppliedAt 判断节点是否算送达 version，并返回用于 min/max 的生效时间（零值表示不计入）。
func targetAppliedAt(node *NodeStatus, version string) (applied time.Time, reached bool) {
	for _, h := range node.RosterHistory {
		if h.Version == version {
			reached = true
			if !h.AppliedAt.IsZero() {
				applied = h.AppliedAt
			}
			return applied, reached
		}
	}
	if node.RosterVersion == version {
		reached = true
		if !node.RosterAppliedAt.IsZero() {
			applied = node.RosterAppliedAt
		}
	}
	return applied, reached
}

func sortedKeys[K ~string, V any](m map[K]V) []K {
	keys := make([]K, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	return keys
}

func updateTimeMinMax(lo, hi *time.Time, t time.Time) {
	if t.IsZero() {
		return
	}
	if lo.IsZero() || t.Before(*lo) {
		*lo = t
	}
	if hi.IsZero() || t.After(*hi) {
		*hi = t
	}
}

func updateTimeMax(hi *time.Time, t time.Time) {
	if t.IsZero() {
		return
	}
	if hi.IsZero() || t.After(*hi) {
		*hi = t
	}
}

// printSummary 先在内存里拼好整段输出，再一次写出，写失败返回错误。
func printSummary(w io.Writer, s Summary, since time.Time) error {
	var b strings.Builder
	fmt.Fprintf(&b, "servers=%d enrolled=%d connected=%d tombstoned=%d errors=%d nodes=%d\n",
		s.Servers, s.Enrolled, s.Connected, s.Tombstoned, s.Errors, s.Nodes)
	fmt.Fprintf(&b, "first_connected min=%s max=%s\n", formatSummaryTime(s.FirstConnectedMin, since), formatSummaryTime(s.FirstConnectedMax, since))
	fmt.Fprintf(&b, "connected min=%s max=%s\n", formatSummaryTime(s.ConnectedMin, since), formatSummaryTime(s.ConnectedMax, since))
	fmt.Fprintf(&b, "disconnected max=%s\n", formatSummaryTime(s.DisconnectedMax, since))
	fmt.Fprintf(&b, "rosters fulls=%d deltas=%d\n", s.Fulls, s.Deltas)
	for _, vg := range s.Versions {
		fmt.Fprintf(&b, "version %s nodes=%d applied_min=%s applied_max=%s\n",
			vg.Version, vg.Nodes, formatSummaryTime(vg.AppliedMin, since), formatSummaryTime(vg.AppliedMax, since))
	}
	if s.Target != nil {
		fmt.Fprintf(&b, "target %s reached=%d/%d applied_min=%s applied_max=%s\n",
			s.Target.Version, s.Target.Reached, s.Target.Total,
			formatSummaryTime(s.Target.AppliedMin, since), formatSummaryTime(s.Target.AppliedMax, since))
		if len(s.Target.Missing) > 0 {
			fmt.Fprintf(&b, "missing %s\n", strings.Join(s.Target.Missing, " "))
		}
	}
	_, err := io.WriteString(w, b.String())
	return err
}

func formatSummaryTime(t time.Time, since time.Time) string {
	if t.IsZero() {
		return "-"
	}
	s := t.UTC().Format(time.RFC3339Nano)
	if since.IsZero() {
		return s
	}
	return fmt.Sprintf("%s (%+.3fs)", s, t.Sub(since).Seconds())
}

func runSummary(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("nodesim summary", flag.ContinueOnError)
	fs.SetOutput(stderr)
	statusFile := fs.String("status-file", "", "状态 JSON 路径（必填）")
	version := fs.String("version", "", "要统计送达情况的名单版本")
	sinceStr := fs.String("since", "", "相对时间基准（RFC3339）")

	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *statusFile == "" {
		_, _ = fmt.Fprintln(stderr, "缺少必填参数 --status-file")
		return 2
	}

	var since time.Time
	if *sinceStr != "" {
		t, err := time.Parse(time.RFC3339Nano, *sinceStr)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "--since 格式错误: %v\n", err)
			return 2
		}
		since = t
	}

	raw, err := os.ReadFile(*statusFile)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "读取状态文件失败: %v\n", err)
		return 2
	}
	var snap Snapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		_, _ = fmt.Fprintf(stderr, "解析状态 JSON 失败: %v\n", err)
		return 2
	}

	sum := summarize(snap, *version)
	if err := printSummary(stdout, sum, since); err != nil {
		_, _ = fmt.Fprintf(stderr, "写出汇总失败: %v\n", err)
		return 2
	}

	if *version != "" && sum.Target != nil && sum.Target.Reached < sum.Target.Total {
		return 1
	}
	return 0
}
