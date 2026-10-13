package handler

import (
	"encoding/json"
	"math"
	"net/http"
	"regexp"
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/module/report/service"
	"github.com/aegispanel/aegis/internal/pkg/money"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

func wantKeys(t *testing.T, where string, m map[string]any, keys ...string) {
	t.Helper()
	want := make(map[string]bool, len(keys))
	for _, k := range keys {
		want[k] = true
	}
	for k := range m {
		if !want[k] {
			t.Fatalf("%s: 多余键 %q", where, k)
		}
	}
	for k := range want {
		if _, ok := m[k]; !ok {
			t.Fatalf("%s: 缺少键 %q", where, k)
		}
	}
}

func assertInt(t *testing.T, where string, v any) {
	t.Helper()
	f, ok := v.(float64)
	if !ok || math.Mod(f, 1) != 0 {
		t.Fatalf("%s: 期望整数 float64，得 %T %v", where, v, v)
	}
}

func assertDecString(t *testing.T, where string, v any) {
	t.Helper()
	s, ok := v.(string)
	if !ok {
		t.Fatalf("%s: 期望十进制字符串，得 %T", where, v)
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			t.Fatalf("%s: 非数字字节串 %q", where, s)
		}
	}
}

var trafficTimeRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{6}Z$`)

func TestShapeOverview(t *testing.T) {
	f := &fakeSvc{overview: fullOverview()}
	w := do(newAdminEngine(f, nil, nil), http.MethodGet, "/v1/overview", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
	var root map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &root); err != nil {
		t.Fatal(err)
	}
	wantKeys(t, "overview", root, "users", "subscriptions", "revenue", "orders", "ledger_drift_accounts", "nodes")
	wantKeys(t, "users", root["users"].(map[string]any), "total", "active", "today", "last_7_days")
	rev := root["revenue"].([]any)
	if len(rev) == 0 {
		t.Fatal("revenue 空")
	}
	row := rev[0].(map[string]any)
	wantKeys(t, "revenue[0]", row,
		"currency", "today", "last_7_days", "last_30_days", "total",
		"actual_today", "actual_7_days", "actual_30_days", "actual_total",
		"adjustment_today", "adjustment_7_days", "adjustment_30_days", "adjustment_total",
		"yesterday", "actual_yesterday",
	)
	for _, k := range []string{"today", "actual_today", "yesterday"} {
		assertInt(t, "revenue."+k, row[k])
	}
}

func TestShapeRevenueTimeseries(t *testing.T) {
	f := &fakeSvc{revenueSeries: service.RevenueSeries{
		Currency: money.CNY, Days: 7,
		Points: []service.RevenuePoint{{
			Date: "2026-03-01", ActualCredit: decimal.New(1, 0), ActualDebit: decimal.Zero,
			Adjustment: decimal.New(1, -2), DisplayedNet: decimal.New(10001, -2),
		}},
		PreviousTotal: decimal.New(2, 0),
	}}
	w := do(newAdminEngine(f, nil, nil), http.MethodGet, "/v1/revenue/timeseries?currency=CNY&days=7", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
	var root map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &root)
	wantKeys(t, "revenue", root, "currency", "days", "points", "previous_total")
	pt := root["points"].([]any)[0].(map[string]any)
	wantKeys(t, "point", pt, "date", "actual_credit", "actual_debit", "adjustment", "displayed_net")
	if !regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`).MatchString(pt["date"].(string)) {
		t.Fatalf("date %v", pt["date"])
	}
}

func TestShapeAdjustmentsList(t *testing.T) {
	revID := uuid.New()
	email := "admin@example.test"
	f := &fakeSvc{adjustments: []service.Adjustment{
		{
			ID: uuid.New(), Currency: money.CNY, Amount: decimal.New(100, -2), Reason: "原因一原因一",
			EffectiveOn: "2026-03-01", ReversalOf: &revID, CreatedBy: uuid.New(), CreatedByEmail: &email,
			CreatedAt: time.Now().UTC(), Reversed: false,
		},
		{
			ID: uuid.New(), Currency: money.USD, Amount: decimal.New(200, -2), Reason: "原因二原因二",
			EffectiveOn: "2026-03-02", CreatedBy: uuid.New(), CreatedByEmail: nil,
			CreatedAt: time.Now().UTC(), Reversed: true,
		},
	}}
	w := do(newAdminEngine(f, nil, nil), http.MethodGet, "/v1/revenue/adjustments", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
	var root map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &root)
	wantKeys(t, "adjustments", root, "adjustments")
	items := root["adjustments"].([]any)
	if len(items) != 2 {
		t.Fatalf("len %d", len(items))
	}
	withRev := items[0].(map[string]any)
	wantKeys(t, "adj with reversal", withRev,
		"id", "currency", "amount", "reason", "effective_on", "reversal_of",
		"created_by", "created_by_email", "created_at", "reversed",
	)
	withoutRev := items[1].(map[string]any)
	wantKeys(t, "adj no reversal", withoutRev,
		"id", "currency", "amount", "reason", "effective_on",
		"created_by", "created_by_email", "created_at", "reversed",
	)
	if _, ok := withoutRev["reversal_of"]; ok {
		t.Fatal("无 reversal_of 时不应有键")
	}
	if withoutRev["created_by_email"] != nil {
		t.Fatalf("email 应为 null")
	}
}

func TestShapeCreateAdjustmentResponse(t *testing.T) {
	f := &fakeSvc{createOut: sampleAdjustment()}
	w := do(newAdminEngine(f, nil, nil), http.MethodPost, "/v1/revenue/adjustments",
		`{"currency":"CNY","amount":1234,"reason":"补录线下收款","effective_on":"2026-03-01"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
	var root map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &root)
	wantKeys(t, "create", root,
		"id", "currency", "amount", "reason", "effective_on",
		"created_by", "created_by_email", "created_at", "reversed",
	)
}

func TestShapeReverseAdjustmentResponse(t *testing.T) {
	orig := uuid.New()
	out := sampleAdjustment()
	out.ReversalOf = &orig
	out.Amount = decimal.New(-1234, -2)
	f := &fakeSvc{reverseOut: out}
	w := do(newAdminEngine(f, nil, nil), http.MethodPost, "/v1/revenue/adjustments/"+orig.String()+"/reverse",
		`{"reason":"撤销理由够长测试"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
	var root map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &root)
	wantKeys(t, "reverse", root,
		"id", "currency", "amount", "reason", "effective_on", "reversal_of",
		"created_by", "created_by_email", "created_at", "reversed",
	)
	assertInt(t, "reverse amount", root["amount"])
	if root["amount"].(float64) != -1234 {
		t.Fatalf("amount %v", root["amount"])
	}
}

func TestShapeTrafficNodes(t *testing.T) {
	snap := time.Date(2026, 3, 10, 12, 0, 0, 123456, time.UTC)
	dn := "显示名"
	f := &fakeSvc{nodeTraffic: service.NodeTraffic{
		Window:  service.TrafficWindow{Range: "24h", SnapshotAt: snap, From: snap.Add(-24 * time.Hour), To: snap},
		Totals:  service.TrafficTotals{Reported: 1000, Attributed: 800, Unattributed: 200},
		Quality: service.TrafficQuality{InvalidReports: 1, InvalidEntries: 2},
		Items: []service.NodeTrafficItem{{
			NodeID: uuid.New(), Name: "n1", DisplayName: nil,
			Upload: 10, Download: 20, Total: 30, Entries: 3, Reports: 2, LastAt: snap,
		}, {
			NodeID: uuid.New(), Name: "n2", DisplayName: &dn,
			Upload: 1, Download: 1, Total: 2, Entries: 1, Reports: 1, LastAt: snap,
		}},
		Returned: 32, Other: 968,
	}}
	w := do(newAdminEngine(f, nil, nil), http.MethodGet, "/v1/dashboard/traffic/nodes?range=24h&limit=5", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
	var root map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &root)
	wantKeys(t, "node traffic", root, "range", "snapshot_at", "from", "to", "basis", "totals", "quality", "items", "ranking")
	if root["basis"] != "strict_raw_report_entries" {
		t.Fatalf("basis %v", root["basis"])
	}
	for _, k := range []string{"snapshot_at", "from", "to"} {
		if !trafficTimeRe.MatchString(root[k].(string)) {
			t.Fatalf("%s %q", k, root[k])
		}
	}
	item0 := root["items"].([]any)[0].(map[string]any)
	if item0["display_name"] != nil {
		t.Fatalf("display_name null")
	}
	item1 := root["items"].([]any)[1].(map[string]any)
	if item1["display_name"] == nil {
		t.Fatal("display_name 应有值")
	}
	assertDecString(t, "total_bytes", item0["total_bytes"])
}

func TestShapeTrafficUsers(t *testing.T) {
	snap := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	f := &fakeSvc{userTraffic: service.UserTraffic{
		Window: service.TrafficWindow{Range: "7d", SnapshotAt: snap, From: snap, To: snap},
		Totals: service.TrafficTotals{Reported: 1, Attributed: 1, Unattributed: 0},
		Items: []service.UserTrafficItem{{
			UserID: uuid.New(), EmailMasked: "a***@example.test", SubscriptionCount: 2,
			Upload: 5, Download: 5, Total: 10, Entries: 1, LastAt: snap,
		}},
		Returned: 10, Other: 0,
	}}
	w := do(newAdminEngine(f, nil, nil), http.MethodGet, "/v1/dashboard/traffic/users", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
	var root map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &root)
	wantKeys(t, "ranking", root["ranking"].(map[string]any), "returned_bytes", "other_user_bytes")
}

func TestShapeBacklog(t *testing.T) {
	ready := time.Date(2026, 3, 10, 11, 0, 0, 0, time.UTC)
	f := &fakeSvc{backlog: service.NotificationBacklog{
		AsOf: time.Now().UTC(), State: "clear", Ready: 1, Failed: 2, Suppressed: 3,
		OldestReadyAt: &ready, MaxReadyLag: 10, Reason: "within_threshold",
	}}
	w := do(newAdminEngine(f, nil, nil), http.MethodGet, "/v1/dashboard/backlog/notifications", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
	var root map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &root)
	wantKeys(t, "backlog", root,
		"as_of", "backlog_state", "processor_state", "scanner_interval_seconds", "counts",
		"oldest_ready_at", "max_ready_lag_seconds", "last_sent_at", "assessment",
	)
	if root["processor_state"] != "unobservable" {
		t.Fatal("processor_state")
	}
	wantKeys(t, "counts", root["counts"].(map[string]any),
		"ready", "ready_retry", "scheduled", "scheduled_retry", "sending_unobservable",
		"failed_total", "suppressed_total", "bounced_total",
	)

	f2 := &fakeSvc{backlog: service.NotificationBacklog{
		AsOf: time.Now().UTC(), State: "clear", Reason: "no_due_backlog",
	}}
	w2 := do(newAdminEngine(f2, nil, nil), http.MethodGet, "/v1/dashboard/backlog/notifications", "")
	var root2 map[string]any
	_ = json.Unmarshal(w2.Body.Bytes(), &root2)
	if root2["oldest_ready_at"] != nil {
		t.Fatalf("oldest_ready_at 应为 null")
	}
}

func TestShapeTasks(t *testing.T) {
	wait := int64(120)
	f := &fakeSvc{tasks: service.Tasks{
		AsOf:        time.Now().UTC(),
		TicketsOpen: service.TicketsOpenTask{Count: 1, HighPriority: 2, OldestWaitSeconds: &wait},
		WithdrawalsPending: service.WithdrawalsPendingTask{
			Count: 1, Amounts: []service.CurrencyAmount{{Currency: money.CNY, Amount: decimal.New(999, -2)}},
		},
		NodesOffline: service.NodesOfflineTask{
			Count: 1, Sample: []service.NodeRef{{ID: uuid.New(), Name: "n"}}, LongestOfflineSeconds: &wait,
		},
		OrdersPendingStale:   service.OrdersPendingStaleTask{Count: 3, ThresholdSeconds: 1800},
		NotificationsBacklog: service.NotificationsBacklogTask{Queued: 4, FailedTotal: 5, BacklogState: "clear"},
		LedgerDrift:          6,
	}}
	w := do(newAdminEngine(f, nil, nil), http.MethodGet, "/v1/dashboard/tasks", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
	var root map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &root)
	wantKeys(t, "tasks", root, "as_of", "items")
	items := root["items"].([]any)
	if len(items) != 6 {
		t.Fatalf("items len %d", len(items))
	}
	kinds := []string{"tickets_open", "withdrawals_pending", "nodes_offline", "orders_pending_stale", "notifications_backlog", "ledger_drift"}
	for i, kind := range kinds {
		m := items[i].(map[string]any)
		if m["kind"] != kind {
			t.Fatalf("[%d] kind %v", i, m["kind"])
		}
	}
	itemKeys := [][]string{
		{"kind", "count", "high_priority", "oldest_wait_seconds"},
		{"kind", "count", "amounts"},
		{"kind", "count", "sample", "longest_offline_seconds"},
		{"kind", "count", "threshold_seconds"},
		{"kind", "queued", "failed_total", "backlog_state"},
		{"kind", "count"},
	}
	for i, keys := range itemKeys {
		wantKeys(t, kinds[i], items[i].(map[string]any), keys...)
	}
	wd := items[1].(map[string]any)
	amt := wd["amounts"].([]any)[0].(map[string]any)
	wantKeys(t, "withdraw amount", amt, "currency", "amount")
	assertInt(t, "withdraw amount", amt["amount"])
}

func TestShapeStatsTimeseries(t *testing.T) {
	f := &fakeSvc{activityPts: []service.ActivityPoint{{
		Day: "03-10", Registered: 1, Logins: 2, Orders: 3, UniqueIPs: 4, ActiveUsers: 5,
	}}}
	w := do(newAdminEngine(f, nil, nil), http.MethodGet, "/v1/stats/timeseries?days=14", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
	var root map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &root)
	pt := root["points"].([]any)[0].(map[string]any)
	if !regexp.MustCompile(`^\d{2}-\d{2}$`).MatchString(pt["day"].(string)) {
		t.Fatalf("day %v", pt["day"])
	}
}

func fullOverview() service.Overview {
	d := decimal.New(1, 0)
	row := service.RevenueRow{
		Currency: money.CNY,
		Today:    d, Yesterday: d, Last7Days: d, Last30Days: d, Total: d,
		ActualToday: d, ActualYesterday: d, Actual7Days: d, Actual30Days: d, ActualTotal: d,
		AdjustmentToday: d, Adjustment7Days: d, Adjustment30Days: d, AdjustmentTotal: d,
	}
	return service.Overview{
		Users:               service.UserCounts{Total: 1, Active: 2, Today: 3, Last7Days: 4},
		Subscriptions:       service.SubscriptionCounts{Active: 1, Trialing: 0, Expiring7Days: 2, Expired: 3, New7Days: 4},
		Revenue:             []service.RevenueRow{row},
		Orders:              service.OrderCounts{PaidToday: 1, Pending: 2, FailedToday: 3},
		LedgerDriftAccounts: 5,
		Nodes:               service.NodeCounts{Total: 10, Online: 9},
	}
}
