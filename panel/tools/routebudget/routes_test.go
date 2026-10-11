package routebudget

import (
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/aegispanel/aegis/internal/api/admin"
	"github.com/aegispanel/aegis/internal/api/node"
	"github.com/aegispanel/aegis/internal/api/public"
	"github.com/aegispanel/aegis/internal/platform/config"
)

// walkRouters 遍历三个网关的真实路由器（只建路由表，不连库）。
func walkRouters(t *testing.T) []Route {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	routers := map[string]http.Handler{
		"public": public.NewRouter(public.Deps{Cfg: &config.Config{}, Log: log}),
		"admin":  admin.NewRouter(admin.Deps{Cfg: &config.Config{}, Log: log}),
		"node":   node.NewRouter(node.Deps{Log: log}),
	}
	var out []Route
	for _, gw := range Gateways {
		routes, ok := routers[gw].(chi.Routes)
		if !ok {
			t.Fatalf("%s router does not implement chi.Routes", gw)
		}
		err := chi.Walk(routes, func(method, pattern string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
			out = append(out, Route{Gateway: gw, Method: method, Pattern: pattern})
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s routes: %v", gw, err)
		}
	}
	slices.SortFunc(out, compare)
	return slices.CompactFunc(out, func(a, b Route) bool { return compare(a, b) == 0 })
}

// 登记表与三个网关的路由器一一对应：新增、删除、改路径的路由都要同步改 routes.txt。
// 新增的行先写预算「- -」，有 PG18 用例量过再填数。
func TestRouteRegistryMatchesRouters(t *testing.T) {
	table, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	live := walkRouters(t)
	if len(live) < 250 {
		t.Fatalf("walked only %d routes; the router construction is broken", len(live))
	}
	id := func(r Route) string { return r.Gateway + " " + r.Key() }
	inTable := map[string]bool{}
	for _, r := range table {
		inTable[id(r)] = true
	}
	inRouters := map[string]bool{}
	var missing []string
	for _, r := range live {
		inRouters[id(r)] = true
		if !inTable[id(r)] {
			missing = append(missing, Format(r))
		}
	}
	if len(missing) > 0 {
		t.Errorf("routes registered in the routers but missing from tools/routebudget/routes.txt (add these lines, keep the order):\n%s",
			strings.Join(missing, "\n"))
	}
	for _, r := range table {
		if !inRouters[id(r)] {
			t.Errorf("routes.txt:%d %s is no longer registered in any router; delete the line", r.Line, id(r))
		}
	}
}

func TestRegistryParserRejectsMalformedRows(t *testing.T) {
	for name, src := range map[string]string{
		"columns":   "public GET /x 1\n",
		"gateway":   "portal GET /x - -\n",
		"half":      "public GET /x 1 -\n",
		"negative":  "public GET /x -1 0\n",
		"duplicate": "public GET /x - -\npublic GET /x - -\n",
		"order":     "public GET /y - -\npublic GET /x - -\n",
		"gw order":  "node GET /x - -\npublic GET /x - -\n",
		"method":    "public get /x - -\n",
	} {
		if _, err := parse([]byte(src)); err == nil {
			t.Errorf("%s: malformed table accepted", name)
		}
	}
	got, err := parse([]byte("# c\npublic GET /a 2 1\npublic POST /a - -\nnode GET /a 0 0\n"))
	if err != nil || len(got) != 3 || !got[0].Budgeted || got[0].DB != 2 || got[0].KV != 1 || got[1].Budgeted {
		t.Fatalf("parse = %+v, %v", got, err)
	}
}
