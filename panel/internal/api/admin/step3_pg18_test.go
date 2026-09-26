// [INPUT]: 依赖 announcement_pg18_test.go 的 openAnnouncementPG18，依赖 access_log.go 的 accessLogList、node_routing.go 的 nodeSetRouting、nodes.go 的 nodeList，依赖 platform/audit 写审计样本
// [OUTPUT]: 对外提供 TestAccessLogCategoryPG18、TestNodeRoutingGlobalOutboundPG18、TestNodeListPagingPG18
// [POS]: api/admin 的 PG18 测试：访问日志按分类真正过滤（缺陷 14）、单节点规则可引用全局出站（缺陷 18）、节点列表不再静默截断（缺陷 21）
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package admin

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aegispanel/aegis/internal/domain/nodefabric"
	"github.com/aegispanel/aegis/internal/platform/audit"
	platformdb "github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// step3Router 把被测处理器挂在真实路径上，并注入租户与一个有全部相关权限的管理员。
func step3Router(tenant, actor string, h *handlers) http.Handler {
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			c := httpx.WithTenantID(req.Context(), tenant)
			c = httpx.WithPrincipal(c, &httpx.Principal{Kind: "admin", Audience: "admin",
				UserID: actor, TenantID: tenant, ReauthedRecently: true})
			next.ServeHTTP(w, req.WithContext(c))
		})
	})
	r.Get("/v1/access-log", h.accessLogList)
	r.Get("/v1/nodes", h.nodeList)
	r.Put("/v1/nodes/{id}/routing", h.nodeSetRouting)
	return r
}

func step3Do(t *testing.T, ctx context.Context, r http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req.WithContext(ctx))
	return w
}

func step3Seed(t *testing.T, ctx context.Context, admin *pgxpool.Pool, sqls ...string) {
	t.Helper()
	for _, sql := range sqls {
		if _, err := admin.Exec(ctx, sql); err != nil {
			t.Fatalf("seed: %v\nSQL: %s", err, sql)
		}
	}
}

func step3Handlers(app *platformdb.Pool) *handlers {
	return &handlers{d: Deps{Pool: app, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Node: nodefabric.NewService(app, nil)}}
}

func TestAccessLogCategoryPG18(t *testing.T) {
	ctx, admin, app := openAnnouncementPG18(t)
	const (
		tenant = "7c000000-0000-4000-8000-000000000001"
		actor  = "7c000000-0000-4000-8000-000000000011"
	)
	step3Seed(t, ctx, admin,
		`INSERT INTO tenants(id,slug,display_name,default_currency) VALUES('`+tenant+`','access-log-pg18','Access Log','CNY')`,
		`INSERT INTO users(id,tenant_id,email,display_name,status) VALUES('`+actor+`','`+tenant+`','ops@access-log.invalid','Ops','active')`)
	actions := map[string]string{
		"user.login": "login", "node.update": "admin", "payment_provider.toggle": "admin",
		"payment.captured": "payment", "order.created": "order", "session.revoked_by_user": "other",
	}
	if err := app.InTx(ctx, platformdb.Scope{TenantID: tenant, ActorID: actor}, func(tx pgx.Tx) error {
		for action := range actions {
			id := actor
			if err := audit.Write(ctx, tx, tenant, audit.Entry{ActorKind: "admin", ActorID: &id,
				Action: action, APIDomain: "admin", Outcome: "success"}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("write audit samples: %v", err)
	}
	r := step3Router(tenant, actor, step3Handlers(app))

	for _, cat := range []string{"admin", "payment", "order", "login", "other"} {
		w := step3Do(t, ctx, r, http.MethodGet, "/v1/access-log?category="+cat, "")
		if w.Code != http.StatusOK {
			t.Fatalf("category=%s status=%d body=%s", cat, w.Code, w.Body.String())
		}
		var body struct {
			Items []accessLogItem `json:"items"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		want := 0
		for _, c := range actions {
			if c == cat {
				want++
			}
		}
		if len(body.Items) != want {
			t.Fatalf("category=%s returned %d items, want %d: %+v", cat, len(body.Items), want, body.Items)
		}
		for _, it := range body.Items {
			if it.Category != cat {
				t.Fatalf("category=%s returned %s item %s", cat, it.Category, it.Action)
			}
		}
	}
	if w := step3Do(t, ctx, r, http.MethodGet, "/v1/access-log?category=bogus", ""); w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("unknown category: status=%d, want 422", w.Code)
	}
}

// step3Nodes 建一个池、一台服务器和 n 个节点，返回节点 id（按插入顺序）。
func step3Nodes(t *testing.T, ctx context.Context, admin *pgxpool.Pool, tenant, prefix string, n int) []string {
	t.Helper()
	pool, server := prefix+"0000000000a1", prefix+"0000000000a2"
	step3Seed(t, ctx, admin,
		`INSERT INTO node_pools(id,tenant_id,code,name,status) VALUES('`+pool+`','`+tenant+`','step3','Step3','active')`,
		`INSERT INTO servers(id,tenant_id,name,status) VALUES('`+server+`','`+tenant+`','step3-server','ready')`)
	var ids []string
	for i := 0; i < n; i++ {
		id := prefix + "0000000000b" + string(rune('1'+i))
		step3Seed(t, ctx, admin, `INSERT INTO nodes(id,tenant_id,name,pool_id,status,node_type,server_host,server_port,server_id,serving_status)
			VALUES('`+id+`','`+tenant+`','step3-node-`+string(rune('1'+i))+`','`+pool+`','active','vless','n.invalid',443,'`+server+`','active')`)
		ids = append(ids, id)
	}
	return ids
}

func TestNodeRoutingGlobalOutboundPG18(t *testing.T) {
	ctx, admin, app := openAnnouncementPG18(t)
	const (
		tenant = "7d000000-0000-4000-8000-000000000001"
		actor  = "7d000000-0000-4000-8000-000000000011"
	)
	step3Seed(t, ctx, admin,
		`INSERT INTO tenants(id,slug,display_name,default_currency) VALUES('`+tenant+`','routing-pg18','Routing','CNY')`,
		`INSERT INTO users(id,tenant_id,email,display_name,status) VALUES('`+actor+`','`+tenant+`','ops@routing.invalid','Ops','active')`)
	node := step3Nodes(t, ctx, admin, tenant, "7d000000-0000-4000-8000-", 1)[0]
	step3Seed(t, ctx, admin, `INSERT INTO node_outbounds(tenant_id,node_id,tag,type,settings)
		VALUES('`+tenant+`',NULL,'US-LAX-01','trojan','{}')`)
	r := step3Router(tenant, actor, step3Handlers(app))
	path := "/v1/nodes/" + node + "/routing"
	routeCount := func() int {
		var n int
		if err := admin.QueryRow(ctx, `SELECT count(*) FROM node_routes WHERE node_id=$1`, node).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	// 反向：指向既不是私有、也不是全局的出站，仍然 422，什么都不写
	w := step3Do(t, ctx, r, http.MethodPut, path,
		`{"row_version":1,"outbounds":[],"routes":[{"matcher":{"domain_suffix":"example.com"},"outbound_tag":"nowhere","enabled":true}]}`)
	if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "nowhere") || routeCount() != 0 {
		t.Fatalf("unknown outbound: status=%d body=%s routes=%d", w.Code, w.Body.String(), routeCount())
	}
	// 引用全局出站（大小写不敏感）：原先一律 422
	w = step3Do(t, ctx, r, http.MethodPut, path,
		`{"row_version":1,"outbounds":[],"routes":[{"matcher":{"domain_suffix":"example.com"},"outbound_tag":"us-lax-01","enabled":true},{"matcher":{},"outbound_tag":"direct","enabled":true}]}`)
	if w.Code != http.StatusOK || routeCount() != 2 {
		t.Fatalf("global outbound reference: status=%d body=%s routes=%d", w.Code, w.Body.String(), routeCount())
	}
}

func TestNodeListPagingPG18(t *testing.T) {
	ctx, admin, app := openAnnouncementPG18(t)
	const (
		tenant = "7e000000-0000-4000-8000-000000000001"
		actor  = "7e000000-0000-4000-8000-000000000011"
	)
	step3Seed(t, ctx, admin,
		`INSERT INTO tenants(id,slug,display_name,default_currency) VALUES('`+tenant+`','node-list-pg18','Node List','CNY')`)
	step3Nodes(t, ctx, admin, tenant, "7e000000-0000-4000-8000-", 3)
	r := step3Router(tenant, actor, step3Handlers(app))

	page := func(query string) (int, int64) {
		w := step3Do(t, ctx, r, http.MethodGet, "/v1/nodes"+query, "")
		if w.Code != http.StatusOK {
			t.Fatalf("node list %s: status=%d body=%s", query, w.Code, w.Body.String())
		}
		var body struct {
			Nodes []json.RawMessage `json:"nodes"`
			Total int64             `json:"total"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		return len(body.Nodes), body.Total
	}
	// total 是筛选后的真实总数，与本页条数无关（原先恒等于本页条数）
	for query, want := range map[string][2]int64{
		"":                   {3, 3},
		"?limit=2":           {2, 3},
		"?limit=2&offset=2":  {1, 3},
		"?limit=2&offset=10": {0, 3},
	} {
		if n, total := page(query); int64(n) != want[0] || total != want[1] {
			t.Fatalf("node list %q: nodes=%d total=%d, want %d/%d", query, n, total, want[0], want[1])
		}
	}
}
