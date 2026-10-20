package service

import (
	"encoding/base64"
	"testing"
	"time"

	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	"github.com/aegispanel/aegis/internal/pkg/crypto"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/geoip"
	"github.com/google/uuid"
)

func TestTraceWindow(t *testing.T) {
	fixed := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	svc := &Service{now: func() time.Time { return fixed }}

	win, since, err := svc.traceWindow("")
	if err != nil || win != TraceWindow24h || !since.Equal(fixed.Add(-24*time.Hour)) {
		t.Fatalf("空 window: win=%s since=%v err=%v", win, since, err)
	}
	_, since, err = svc.traceWindow(TraceWindow7d)
	if err != nil || !since.Equal(fixed.Add(-7*24*time.Hour)) {
		t.Fatalf("7d: %v %v", since, err)
	}
	_, since, err = svc.traceWindow(TraceWindow30d)
	if err != nil || !since.Equal(fixed.Add(-30*24*time.Hour)) {
		t.Fatalf("30d: %v %v", since, err)
	}
	_, _, err = svc.traceWindow("1h")
	if errs.As(err).Code != errs.ValidationFailed {
		t.Fatalf("1h 应 validation_failed，得到 %v", err)
	}
}

func TestMaskIP(t *testing.T) {
	cases := map[string]string{
		"198.51.100.7":     "198.51.*.*",
		"2001:db8::1":      "2001:db8:*",
		"::ffff:192.0.2.9": "192.0.*.*",
		"":                 "",
		"not-an-ip":        "*",
	}
	for in, want := range cases {
		if got := maskIP(in); got != want {
			t.Errorf("maskIP(%q) = %q，要 %q", in, got, want)
		}
	}
}

func TestTraceTokenRoundTrip(t *testing.T) {
	master := base64.StdEncoding.EncodeToString(make([]byte, 32))
	keys, err := crypto.NewKeys(master)
	if err != nil {
		t.Fatal(err)
	}
	sealer, err := keys.Sealer()
	if err != nil {
		t.Fatal(err)
	}
	admin := uuid.MustParse("00000000-0000-4000-8000-000000000001")
	other := uuid.MustParse("00000000-0000-4000-8000-000000000002")
	now := time.Date(2026, 10, 20, 8, 0, 0, 0, time.UTC)
	ip := "198.51.100.7"

	tok := traceToken(sealer, admin, ip, now)
	if tok == "" {
		t.Fatal("令牌为空")
	}
	got, ok := openTraceToken(sealer, admin, tok, now)
	if !ok || got != ip {
		t.Fatalf("解不开或 IP 不对：%v %q", ok, got)
	}
	if _, ok := openTraceToken(sealer, other, tok, now); ok {
		t.Fatal("换管理员应解不开")
	}
	if _, ok := openTraceToken(sealer, admin, tok, now.Add(TraceRevealTTL+time.Second)); ok {
		t.Fatal("过期应解不开")
	}
	if _, ok := openTraceToken(sealer, admin, "not-a-token", now); ok {
		t.Fatal("乱串应解不开")
	}
}

func TestMergeTraceIPsAndOverview(t *testing.T) {
	idA := uuid.MustParse("00000000-0000-4000-8000-000000000010")
	idB := uuid.MustParse("00000000-0000-4000-8000-000000000011")
	audit := []auditsvc.TraceIP{
		{IP: "198.51.100.1", Events: 3, Accounts: 1, AccountIDs: []uuid.UUID{idA}, Last: time.Unix(1, 0)},
		{IP: "198.51.100.2", Events: 1, Last: time.Unix(2, 0)},
	}
	fetch := []TraceFetchIP{
		{IP: "198.51.100.1", Fetches: 5, Accounts: 2, AccountIDs: []uuid.UUID{idA, idB}, Last: time.Unix(3, 0)},
		{IP: "192.0.2.3", Fetches: 2, Last: time.Unix(4, 0)},
	}
	locate := func(ip string) geoip.Location {
		if ip == "192.0.2.3" {
			return geoip.Location{Country: "文档", Region: "机房", Kind: geoip.KindDatacenter, Display: "机房"}
		}
		return geoip.Location{Country: "文档", Region: "A", Display: "文档 A"}
	}

	merged := mergeTraceIPs(audit, fetch)
	if len(merged) != 3 {
		t.Fatalf("merged 应有 3 个，得到 %d", len(merged))
	}
	g := merged["198.51.100.1"]
	if g == nil || g.events+g.fetches != 8 || len(g.ids) != 2 {
		t.Fatalf("198.51.100.1 统计不对：%v ids=%d", g, len(g.ids))
	}

	aSet := auditsvc.TraceIPSet{DistinctIPs: 2, Logins: 1, LoginFailures: 0}
	fSet := TraceFetchIPSet{DistinctIPs: 2, Fetches: 7, Failures: 0}
	ov := traceOverview(aSet, fSet, merged, locate)
	if ov.DistinctIPs != 3 || ov.DistinctIPsApprox {
		t.Fatalf("DistinctIPs=%d approx=%v", ov.DistinctIPs, ov.DistinctIPsApprox)
	}
	if ov.DatacenterIPs != 1 {
		t.Fatalf("DatacenterIPs=%d", ov.DatacenterIPs)
	}
	if len(ov.TopRegions) == 0 || ov.TopRegions[0].Hits < ov.TopRegions[len(ov.TopRegions)-1].Hits {
		t.Fatalf("TopRegions 应按 Hits 降序：%v", ov.TopRegions)
	}
	maxHits := 0
	for _, r := range ov.TopRegions {
		if r.Hits > maxHits {
			maxHits = r.Hits
		}
	}
	if ov.TopRegions[0].Hits != maxHits {
		t.Fatalf("TopRegions 第一个应是最大 Hits")
	}

	aSet.DistinctIPs = TraceScanIPs + 10
	ov2 := traceOverview(aSet, fSet, merged, locate)
	if !ov2.DistinctIPsApprox {
		t.Fatal("应估算")
	}
	wantDistinct := aSet.DistinctIPs + fSet.DistinctIPs - 1
	if ov2.DistinctIPs != wantDistinct {
		t.Fatalf("DistinctIPs=%d 要 %d", ov2.DistinctIPs, wantDistinct)
	}
}

func TestCombineTraceAccounts(t *testing.T) {
	uid := uuid.MustParse("00000000-0000-4000-8000-000000000020")
	fetchOnly := uuid.MustParse("00000000-0000-4000-8000-000000000021")
	locate := func(ip string) geoip.Location {
		if ip == "203.0.113.2" {
			return geoip.Location{Kind: geoip.KindDatacenter}
		}
		return geoip.Location{Country: "文档"}
	}
	audit := map[uuid.UUID]auditsvc.TraceUser{
		uid: {
			UserID:      uid,
			Logins:      5,
			LoginIPs:    2,
			DistinctIPs: 2,
			SharedIPs:   1,
			IPs: []auditsvc.TraceUserIP{
				{IP: "198.51.100.10", Count: 3, Shared: true},
				{IP: "198.51.100.11", Count: 1, Shared: false},
			},
		},
	}
	fetch := map[uuid.UUID]TraceFetchUser{
		uid: {
			UserID:      uid,
			DistinctIPs: 2,
			SharedIPs:   1,
			IPs: []TraceIPCount{
				{IP: "198.51.100.10", Count: 4, Shared: true},
				{IP: "203.0.113.2", Count: 2, Shared: false},
			},
		},
		fetchOnly: {
			UserID:      fetchOnly,
			DistinctIPs: 1,
			IPs:         []TraceIPCount{{IP: "192.0.2.1", Count: 1}},
		},
	}
	rows := combineTraceAccounts(audit, fetch, locate)
	var main TraceAccountRow
	for _, r := range rows {
		if r.UserID == uid {
			main = r
		}
		if r.UserID == fetchOnly && r.Logins != 0 {
			t.Fatalf("仅拉取一路的用户 Logins 应为 0，得到 %d", r.Logins)
		}
	}
	if main.LinkedIPs != 3 || main.SharedIPs != 1 || main.DatacenterHits != 2 {
		t.Fatalf("合并行不对：linked=%d shared=%d dc=%d", main.LinkedIPs, main.SharedIPs, main.DatacenterHits)
	}

	sortTraceAccounts(rows, TraceAccSortLogins)
	if rows[0].UserID != uid || rows[0].Logins < rows[len(rows)-1].Logins {
		t.Fatalf("按 logins 排序不对：%v", rows)
	}
}
