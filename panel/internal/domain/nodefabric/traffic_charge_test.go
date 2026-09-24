package nodefabric

import (
	"os"
	"strings"
	"testing"
)

func ptr64(v int64) *int64 { return &v }

// D-E-1：每个周期先扣套餐额度，扣完再扣流量包；两者都不够的部分仍记在套餐上。
func TestSplitTrafficChargePlanFirstThenPacks(t *testing.T) {
	for _, tc := range []struct {
		name               string
		billed             int64
		planRoom           *int64
		packs              int64
		wantPlan, wantPack int64
	}{
		{"fits in plan", 80, ptr64(100), 500, 80, 0},
		{"exactly fills plan", 100, ptr64(100), 500, 100, 0},
		{"overflow goes to packs", 120, ptr64(100), 500, 100, 20},
		{"plan already exhausted", 50, ptr64(0), 500, 0, 50},
		{"plan overdrawn counts as zero room", 50, ptr64(-30), 500, 0, 50},
		{"packs run out, rest stays on plan", 200, ptr64(100), 30, 170, 30},
		{"no packs keeps old overage behaviour", 150, ptr64(100), 0, 150, 0},
		{"unlimited plan never touches packs", 10_000, nil, 500, 10_000, 0},
		{"nothing billed", 0, ptr64(100), 500, 0, 0},
	} {
		plan, pack := splitTrafficCharge(tc.billed, tc.planRoom, tc.packs)
		if plan != tc.wantPlan || pack != tc.wantPack {
			t.Errorf("%s: split=(%d,%d) want (%d,%d)", tc.name, plan, pack, tc.wantPlan, tc.wantPack)
		}
		if tc.billed > 0 && plan+pack != tc.billed {
			t.Errorf("%s: split loses traffic %d+%d != %d", tc.name, plan, pack, tc.billed)
		}
	}
}

func TestSortedReportEntriesOrdersAndMergesUIDs(t *testing.T) {
	got := sortedReportEntries(map[string][2]int64{
		"30": {1, 2}, "7": {10, 0}, "007": {0, 5}, "bad": {99, 99}, "12": {0, 4},
	})
	want := []reportEntry{{7, 15}, {12, 4}, {30, 3}}
	if len(got) != len(want) {
		t.Fatalf("entries=%v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("entries=%v want %v", got, want)
		}
	}
}

// 流量包有剩余的订阅，套餐额度用完也要继续下发；扣量先锁配额行再锁流量包。
func TestUniProxyServesAndChargesTrafficPacks(t *testing.T) {
	body, err := os.ReadFile("uniproxy.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(body)
	list := src[strings.Index(src, "流量耗尽的订阅不下发到节点"):]
	list = list[:strings.Index(list, "poolFilter")]
	for _, needle := range []string{"qb.remaining <= 0", "OR EXISTS", "traffic_pack_grants g",
		"g.user_id = s.user_id", "g.consumed_bytes < g.granted_bytes"} {
		if !strings.Contains(list, needle) {
			t.Errorf("node user list eligibility missing %q", needle)
		}
	}
	charge := src[strings.Index(src, "func chargeTraffic("):strings.Index(src, "// POST /api/v1/server/UniProxy/alive")]
	quotaLock := strings.Index(charge, "ORDER BY id FOR UPDATE")
	packLock := strings.Index(charge, "ORDER BY created_at, id FOR UPDATE")
	if quotaLock < 0 || packLock < 0 || quotaLock > packLock {
		t.Fatal("chargeTraffic must lock quota rows before traffic pack grants, packs oldest first")
	}
	if !strings.Contains(src, "for _, entry := range sortedReportEntries(report)") {
		t.Fatal("ReportTraffic must charge users in a deterministic order")
	}
}
