package adminops

import (
	"errors"
	"testing"
	"time"
)

// 看板缓存：TTL 内复用、过期重读、读错不缓存、nil 缓存直读；带 snapshot_at 的历史视图不进缓存
func TestDashboardCacheServesWithinTTLOnly(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	c := newDashboardCache(30*time.Second, func() time.Time { return now })
	reads := 0
	read := func() (*Overview, error) { reads++; return &Overview{LedgerDrift: int64(reads)}, nil }

	a, _ := cachedRead(c, "overview:t", read)
	b, _ := cachedRead(c, "overview:t", read)
	if reads != 1 || a != b {
		t.Fatalf("reads=%d, want one read shared within TTL", reads)
	}
	if _, _ = cachedRead(c, "overview:other", read); reads != 2 {
		t.Fatalf("another tenant must not share the entry (reads=%d)", reads)
	}
	now = now.Add(30 * time.Second)
	if v, _ := cachedRead(c, "overview:t", read); reads != 3 || v.LedgerDrift != 3 {
		t.Fatalf("expired entry must be re-read: reads=%d drift=%d", reads, v.LedgerDrift)
	}

	boom := errors.New("down")
	fail := func() (*Overview, error) { reads++; return nil, boom }
	if _, err := cachedRead(c, "overview:fail", fail); !errors.Is(err, boom) {
		t.Fatal(err)
	}
	if _, err := cachedRead(c, "overview:fail", fail); !errors.Is(err, boom) || reads != 5 {
		t.Fatalf("errors must not be cached: reads=%d", reads)
	}

	var none *dashboardCache
	before := reads
	cachedRead(none, "k", read)
	cachedRead(none, "k", read)
	if reads != before+2 {
		t.Fatal("nil cache must read through")
	}

	if dashboardTrafficCacheKey("traffic-nodes", "t", DashboardTrafficQuery{Range: "24h", Limit: 5, SnapshotAt: "2026-10-07T00:00:00Z"}) != "" ||
		dashboardTrafficCacheKey("traffic-nodes", "t", DashboardTrafficQuery{Range: "24h", Limit: 5}) !=
			"traffic-nodes:t:24h:5" {
		t.Fatal("only the live (no snapshot_at) traffic view is cached, keyed by tenant/range/limit")
	}
}
