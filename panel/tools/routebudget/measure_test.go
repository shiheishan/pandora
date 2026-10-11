package routebudget

import (
	"strings"
	"testing"
)

// 预算的棘轮：相等通过；多了超预算；少了要求改小；没填预算的给出可粘贴的行；
// 填了预算却没人量、量了却没登记都报。
func TestCompareBudgets(t *testing.T) {
	routes := map[string]Route{
		"GET /same":  {Gateway: "public", Method: "GET", Pattern: "/same", Budgeted: true, DB: 2, KV: 1, Line: 10},
		"GET /over":  {Gateway: "public", Method: "GET", Pattern: "/over", Budgeted: true, DB: 2, KV: 1, Line: 11},
		"GET /kv":    {Gateway: "public", Method: "GET", Pattern: "/kv", Budgeted: true, DB: 2, KV: 1, Line: 12},
		"GET /down":  {Gateway: "public", Method: "GET", Pattern: "/down", Budgeted: true, DB: 3, KV: 1, Line: 13},
		"GET /new":   {Gateway: "public", Method: "GET", Pattern: "/new", Line: 14},
		"GET /lost":  {Gateway: "public", Method: "GET", Pattern: "/lost", Budgeted: true, DB: 1, KV: 1, Line: 15},
		"GET /quiet": {Gateway: "public", Method: "GET", Pattern: "/quiet", Line: 16},
	}
	measured := map[string]Entry{
		// 探测、准备、归还清理不进预算
		"GET /same":  {DBRT: 5, Ping: 1, Prepare: 1, Reset: 1, KV: 1},
		"GET /over":  {DBRT: 3, KV: 1},
		"GET /kv":    {DBRT: 1, KV: 2},
		"GET /down":  {DBRT: 2, KV: 1},
		"GET /new":   {DBRT: 4, KV: 0},
		"GET /stray": {DBRT: 1},
	}
	got := strings.Join(compareBudgets("public", routes, measured), "\n")
	for _, want := range []string{
		"routes.txt:11 over budget",
		"routes.txt:12 over budget",
		"routes.txt:13 improved",
		Format(Route{Gateway: "public", Method: "GET", Pattern: "/down", Budgeted: true, DB: 2, KV: 1}),
		"routes.txt:14 has no budget yet",
		Format(Route{Gateway: "public", Method: "GET", Pattern: "/new", Budgeted: true, DB: 4, KV: 0}),
		"routes.txt:15 public GET /lost has a budget but no PG18 case measures it",
		"public GET /stray is measured but not registered",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	for _, unwanted := range []string{"routes.txt:10", "routes.txt:16"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("unexpected %q in:\n%s", unwanted, got)
		}
	}
}
