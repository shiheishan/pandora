package service

import (
	"net/netip"
	"sort"
	"strings"
	"time"

	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	"github.com/aegispanel/aegis/internal/pkg/geoip"
	"github.com/google/uuid"
)

// locateFunc 解析一个明文 IP 的归属地（Service 里传 s.geo.Lookup）。
type locateFunc func(ip string) geoip.Location

// normTraceIP 规整明文 IP 作合并键：trim；netip.ParseAddr 成功用 Unmap().String()，否则原串。
func normTraceIP(ip string) string {
	ip = strings.TrimSpace(ip)
	if ip == "" {
		return ""
	}
	if a, err := netip.ParseAddr(ip); err == nil {
		return a.Unmap().String()
	}
	return ip
}

// traceRegion 把归属地归成地区：strings.TrimSpace(Country + " " + Region)；为空时用 Display；仍为空用「未知」。
func traceRegion(loc geoip.Location) string {
	r := strings.TrimSpace(loc.Country + " " + loc.Region)
	if r == "" {
		r = strings.TrimSpace(loc.Display)
	}
	if r == "" {
		return "未知"
	}
	return r
}

// traceIPAgg 是一个 IP 两路合并后的统计。
type traceIPAgg struct {
	ip                           string
	events, fetches              int
	auditAccounts, fetchAccounts int
	auditUA, fetchUA             int
	ids                          map[uuid.UUID]struct{}
	last                         time.Time
	inAudit, inFetch             bool
}

// mergeTraceIPs 按明文合并两路的 IP 行；IP 为空串的行跳过（解不开，无法合并）。同一路同一 IP 出现多次时（头部 + 补齐）只取第一次。
func mergeTraceIPs(audit []auditsvc.TraceIP, fetch []TraceFetchIP) map[string]*traceIPAgg {
	merged := make(map[string]*traceIPAgg)
	seenAudit := make(map[string]struct{})
	seenFetch := make(map[string]struct{})
	for _, row := range audit {
		key := normTraceIP(row.IP)
		if key == "" {
			continue
		}
		if _, dup := seenAudit[key]; dup {
			continue
		}
		seenAudit[key] = struct{}{}
		g := merged[key]
		if g == nil {
			g = &traceIPAgg{ip: key, ids: make(map[uuid.UUID]struct{})}
			merged[key] = g
		}
		g.events = row.Events
		g.auditAccounts = row.Accounts
		g.auditUA = row.UAKinds
		for _, id := range row.AccountIDs {
			g.ids[id] = struct{}{}
		}
		if row.Last.After(g.last) {
			g.last = row.Last
		}
		g.inAudit = true
	}
	for _, row := range fetch {
		key := normTraceIP(row.IP)
		if key == "" {
			continue
		}
		if _, dup := seenFetch[key]; dup {
			continue
		}
		seenFetch[key] = struct{}{}
		g := merged[key]
		if g == nil {
			g = &traceIPAgg{ip: key, ids: make(map[uuid.UUID]struct{})}
			merged[key] = g
		}
		g.fetches = row.Fetches
		g.fetchAccounts = row.Accounts
		g.fetchUA = row.UAKinds
		for _, id := range row.AccountIDs {
			g.ids[id] = struct{}{}
		}
		if row.Last.After(g.last) {
			g.last = row.Last
		}
		g.inFetch = true
	}
	return merged
}

// traceOverview 由两路总数与合并结果算概览（Window、Since 不在这里填）。
func traceOverview(a auditsvc.TraceIPSet, f TraceFetchIPSet, merged map[string]*traceIPAgg, locate locateFunc) TraceOverview {
	ov := TraceOverview{
		Fetches:       f.Fetches,
		FetchFailures: f.Failures,
		Logins:        a.Logins,
		LoginFailures: a.LoginFailures,
		AnalyzedIPs:   len(merged),
		TopRegions:    []TraceRegion{},
	}
	overlap := 0
	for _, g := range merged {
		if g.inAudit && g.inFetch {
			overlap++
		}
	}
	if a.DistinctIPs <= TraceScanIPs && f.DistinctIPs <= TraceScanIPs {
		ov.DistinctIPs = len(merged)
		ov.DistinctIPsApprox = false
	} else {
		ov.DistinctIPs = a.DistinctIPs + f.DistinctIPs - overlap
		ov.DistinctIPsApprox = true
	}
	regionHits := make(map[string]*TraceRegion)
	for _, g := range merged {
		loc := locate(g.ip)
		if loc.Kind == geoip.KindDatacenter {
			ov.DatacenterIPs++
		}
		reg := traceRegion(loc)
		r, ok := regionHits[reg]
		if !ok {
			r = &TraceRegion{Region: reg}
			regionHits[reg] = r
		}
		r.IPs++
		r.Hits += g.events + g.fetches
	}
	regions := make([]TraceRegion, 0, len(regionHits))
	for _, r := range regionHits {
		regions = append(regions, *r)
	}
	sort.Slice(regions, func(i, j int) bool {
		if regions[i].Hits != regions[j].Hits {
			return regions[i].Hits > regions[j].Hits
		}
		if regions[i].IPs != regions[j].IPs {
			return regions[i].IPs > regions[j].IPs
		}
		return regions[i].Region < regions[j].Region
	})
	if len(regions) > 5 {
		regions = regions[:5]
	}
	ov.TopRegions = regions
	return ov
}

// rankTraceIPs 按 hits（events + fetches）降序、last 降序、ip 升序排，取前 TraceRankLimit 个。
func rankTraceIPs(merged map[string]*traceIPAgg) []*traceIPAgg {
	all := make([]*traceIPAgg, 0, len(merged))
	for _, g := range merged {
		all = append(all, g)
	}
	sort.Slice(all, func(i, j int) bool {
		hi, hj := all[i].events+all[i].fetches, all[j].events+all[j].fetches
		if hi != hj {
			return hi > hj
		}
		if !all[i].last.Equal(all[j].last) {
			return all[i].last.After(all[j].last)
		}
		return all[i].ip < all[j].ip
	})
	if len(all) > TraceRankLimit {
		all = all[:TraceRankLimit]
	}
	return all
}

// traceAccountsOf 是一行的账号数与是否截断：union = len(ids)；任一路账号数 > TraceIPAccounts 时 capped，
// 数取 max(union, auditAccounts, fetchAccounts)；否则就是 union。
func traceAccountsOf(g *traceIPAgg) (int, bool) {
	union := len(g.ids)
	capped := g.auditAccounts > TraceIPAccounts || g.fetchAccounts > TraceIPAccounts
	if capped {
		return max(union, g.auditAccounts, g.fetchAccounts), true
	}
	return union, false
}

// combineTraceAccounts 把两路的用户统计合成账号综合排行的行（Email、Status 不在这里填，未排序）。
func combineTraceAccounts(audit map[uuid.UUID]auditsvc.TraceUser, fetch map[uuid.UUID]TraceFetchUser, locate locateFunc) []TraceAccountRow {
	ids := make(map[uuid.UUID]struct{})
	for id := range audit {
		ids[id] = struct{}{}
	}
	for id := range fetch {
		ids[id] = struct{}{}
	}
	rows := make([]TraceAccountRow, 0, len(ids))
	for id := range ids {
		a := audit[id]
		f := fetch[id]
		rows = append(rows, combineOneTraceAccount(id, a, f, locate))
	}
	return rows
}

func combineOneTraceAccount(id uuid.UUID, a auditsvc.TraceUser, f TraceFetchUser, locate locateFunc) TraceAccountRow {
	aIPs := auditIPCounts(a.IPs)
	linked := a.DistinctIPs + f.DistinctIPs - overlapCount(plainIPSet(aIPs, false), plainIPSet(f.IPs, false))
	shared := a.SharedIPs + f.SharedIPs - overlapCount(plainIPSet(aIPs, true), plainIPSet(f.IPs, true))
	last := a.Last
	if f.Last.After(last) {
		last = f.Last
	}
	return TraceAccountRow{
		UserID:         id,
		LinkedIPs:      max(linked, 0),
		Logins:         a.Logins,
		LoginIPs:       a.LoginIPs,
		FetchIPs:       f.DistinctIPs,
		SharedIPs:      max(shared, 0),
		UAKinds:        a.UAKinds + f.UAKinds,
		DatacenterHits: datacenterHits(aIPs, locate) + datacenterHits(f.IPs, locate),
		Last:           last,
	}
}

// auditIPCounts 把审计这一路的 IP 行换成与拉取这一路同形，后面只写一套集合运算。
func auditIPCounts(ips []auditsvc.TraceUserIP) []TraceIPCount {
	out := make([]TraceIPCount, len(ips))
	for i, row := range ips {
		out[i] = TraceIPCount{IP: row.IP, Count: row.Count, Shared: row.Shared}
	}
	return out
}

// plainIPSet 是一组 IP 规整后的明文集合（跳过空串）；onlyShared 为 true 时只收 Shared 的。
func plainIPSet(ips []TraceIPCount, onlyShared bool) map[string]struct{} {
	out := make(map[string]struct{}, len(ips))
	for _, row := range ips {
		if onlyShared && !row.Shared {
			continue
		}
		if ip := normTraceIP(row.IP); ip != "" {
			out[ip] = struct{}{}
		}
	}
	return out
}

// overlapCount 是两个集合的交集大小。
func overlapCount(a, b map[string]struct{}) int {
	n := 0
	for k := range a {
		if _, ok := b[k]; ok {
			n++
		}
	}
	return n
}

// datacenterHits 是一组 IP 里机房 IP 的次数之和。
func datacenterHits(ips []TraceIPCount, locate locateFunc) int {
	n := 0
	for _, row := range ips {
		if ip := normTraceIP(row.IP); ip != "" && locate(ip).Kind == geoip.KindDatacenter {
			n += row.Count
		}
	}
	return n
}

// sortTraceAccounts 按 sort（TraceAccSort* 之一）降序，再 Last 降序，再 UserID.String() 升序；原地排序。
func sortTraceAccounts(rows []TraceAccountRow, sortKey string) {
	metric := func(r TraceAccountRow) int {
		switch sortKey {
		case TraceAccSortLogins:
			return r.Logins
		case TraceAccSortLoginIPs:
			return r.LoginIPs
		case TraceAccSortFetchIPs:
			return r.FetchIPs
		case TraceAccSortSharedIPs:
			return r.SharedIPs
		case TraceAccSortUAKinds:
			return r.UAKinds
		case TraceAccSortDCHits:
			return r.DatacenterHits
		default:
			return r.LinkedIPs
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		ki, kj := metric(rows[i]), metric(rows[j])
		if ki != kj {
			return ki > kj
		}
		if !rows[i].Last.Equal(rows[j].Last) {
			return rows[i].Last.After(rows[j].Last)
		}
		return rows[i].UserID.String() < rows[j].UserID.String()
	})
}

// traceRegionCount 是一组 IP（跳过空串）的不同地区数。
func traceRegionCount(ips []TraceIPCount, locate locateFunc) int {
	seen := make(map[string]struct{})
	for _, row := range ips {
		ip := normTraceIP(row.IP)
		if ip == "" {
			continue
		}
		reg := traceRegion(locate(ip))
		seen[reg] = struct{}{}
	}
	return len(seen)
}
