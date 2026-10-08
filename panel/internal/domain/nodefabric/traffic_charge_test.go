package nodefabric

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/platform/sourcetest"
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

func TestParseTrafficReportOrdersMergesAndValidates(t *testing.T) {
	got, err := parseTrafficReport([]byte(`{"30":[1,2],"7":[10,0],"007":[0,5],"bad":[99,99],"12":[0,4],
		"40":[-5000000000,6000000000],"41":[100],"42":[100,200,300],"43":[9223372036854775807,1],
		"44":[30000000001,0],"45":[1.5,0],"46":null,"47":{"up":1},"48":[0,99999999999999999999]}`))
	if err != nil {
		t.Fatal(err)
	}
	want := []reportEntry{{7, 15}, {12, 4}, {30, 3}}
	if len(got.entries) != len(want) {
		t.Fatalf("entries=%v want %v", got.entries, want)
	}
	for i := range want {
		if got.entries[i] != want[i] {
			t.Fatalf("entries=%v want %v", got.entries, want)
		}
	}
	// 非整数 uid、负数、错长度、超上限、小数、null、对象、超出 int64：逐项计 invalid，不拒整份
	if got.invalid != 10 || got.keys != 14 || got.upload != 11 || got.download != 11 {
		t.Fatalf("invalid=%d keys=%d up=%d down=%d", got.invalid, got.keys, got.upload, got.download)
	}
	if empty, err := parseTrafficReport([]byte(`null`)); err != nil || empty.keys != 0 {
		t.Fatalf("null report = %+v, %v; want an empty report", empty, err)
	}
	for _, bad := range []string{`[]`, `"x"`, `{`} {
		if _, err := parseTrafficReport([]byte(bad)); err == nil {
			t.Fatalf("non-object report %s accepted", bad)
		}
	}
}

// 流量包有剩余的订阅，套餐额度用完也要继续下发；扣量先锁配额行再锁流量包。
// 购买模型统一后流量包按份挂：下发判断与扣量都按订阅，不再按用户。
func TestUniProxyServesAndChargesTrafficPacks(t *testing.T) {
	pkg := sourcetest.Load(t, ".")
	list := pkg.Decl("Service.ListNodeUsers")
	list = list[strings.Index(list, "流量耗尽的订阅不下发到节点"):]
	list = list[:strings.Index(list, "poolFilter")]
	for _, needle := range []string{"qb.remaining <= 0", "OR EXISTS", "traffic_pack_grants g",
		"g.subscription_id = s.id", "g.consumed_bytes < g.granted_bytes"} {
		if !strings.Contains(list, needle) {
			t.Errorf("node user list eligibility missing %q", needle)
		}
	}
	if strings.Contains(list, "g.user_id = s.user_id") {
		t.Error("node user list must not count packs attached to the owner's other subscriptions")
	}
	// 批量记账（applyTrafficCharges）：一次锁全部配额行（按 id），再锁流量包（按订阅、先到先扣）
	charge := pkg.Decl("applyTrafficCharges")
	quotaLock := strings.Index(charge, "ORDER BY id FOR UPDATE")
	packLock := strings.Index(charge, "ORDER BY subscription_id, created_at, id FOR UPDATE")
	if quotaLock < 0 || packLock < 0 || quotaLock > packLock ||
		!strings.Contains(charge, "subscription_id = ANY($2::uuid[])") || strings.Contains(charge, "user_id = ANY(") {
		t.Fatal("applyTrafficCharges must lock quota rows before the subscriptions' traffic pack grants, packs oldest first")
	}
	if !strings.Contains(pkg.Decl("chargeTraffic"), "applyTrafficCharges(") ||
		!strings.Contains(pkg.Decl("chargeReportEntries"), "applyTrafficCharges(") {
		t.Fatal("single and batch charges must share one charging core")
	}
	report := pkg.Decl("Service.reportTraffic")
	if !strings.Contains(report, "for _, entry := range report.entries") ||
		!strings.Contains(pkg.Decl("parseTrafficReport"), "slices.SortFunc(out.entries") {
		t.Fatal("ReportTraffic must charge users in a deterministic order")
	}
	// 节点只能扣自己放行名单里的用户（审计 N2）
	if !strings.Contains(report, "s.ListNodeUsers(ctx, tenantID, n)") || !strings.Contains(report, "allowed[entry.uid]") ||
		!strings.Contains(report, "if n.epochKnown {") {
		t.Fatal("ReportTraffic must only charge users the node currently serves")
	}
	// 死锁 / 序列化失败整笔重来（审计 N4）
	if !strings.Contains(report, "db.IsSerializationFailure(err)") || pushRetryAttempts < 2 {
		t.Fatal("ReportTraffic must retry deadlocked charges")
	}
	// 滚动空窗照扣（审计 N1）：不再要求 period_end > now()
	if strings.Contains(charge, "period_end > now()))") || !strings.Contains(charge, "period_start <= now()") {
		t.Fatal("applyTrafficCharges must charge the latest started period even after its period_end")
	}
}

// ReportTraffic 只核对经认证的节点视图的放行名单：生产里唯一的调用方必须是先过 authNode
// 的 uniPush，否则「包外拼的视图不核对」就成了绕过名单的口子。
func TestReportTrafficOnlyReachedThroughAuthentication(t *testing.T) {
	root := filepath.Join("..", "..")
	var callers []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		// 带与不带上报编号的两个入口都只许 uniPush 调（ReportTrafficWithID 是带 X-Report-Id 的那个）
		if strings.Contains(string(body), ".ReportTraffic(") || strings.Contains(string(body), ".ReportTrafficWithID(") {
			callers = append(callers, filepath.ToSlash(path))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(callers) != 1 || !strings.HasSuffix(callers[0], "api/node/handlers.go") {
		t.Fatalf("ReportTraffic non-test callers = %v, want only api/node/handlers.go", callers)
	}
	handler := sourcetest.Load(t, filepath.Join(root, "api", "node")).Decl("handlers.uniPush")
	if auth, report := strings.Index(handler, "h.authNode(w, r)"), strings.Index(handler, ".ReportTrafficWithID("); auth < 0 || report < auth {
		t.Fatal("uniPush must authenticate the node before reporting traffic")
	}
}
