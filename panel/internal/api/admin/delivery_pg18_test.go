// [INPUT]: 依赖 platform/pg18test 的一次性库护栏（run-pg18-gates.sh 的 delivery 域），依赖 pools.go 的 setPlanPools、handlers.go 的 nodeList，依赖 nodefabric 的 ListNodeUsers / NotifyUsersChanged 与 platform/realtime 的本机 Hub
// [OUTPUT]: 对外提供 TestDeliveryAdminPG18、openDeliveryPG18
// [POS]: api/admin 的交付集合 PG18 门禁：改变交付集合的后台写接口提交后通知节点、失败不通知；节点列表的 delivered_to_users 与节点实际拉到的用户同口径（R104）
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package admin

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aegispanel/aegis/internal/domain/nodefabric"
	platformdb "github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
	"github.com/aegispanel/aegis/internal/platform/pg18test"
	"github.com/aegispanel/aegis/internal/platform/realtime"
)

// openDeliveryPG18 与 domain/subscription 的同名函数参数一字不差：同一个域、同一个库。
func openDeliveryPG18(t *testing.T) (context.Context, *pgxpool.Pool, *platformdb.Pool) {
	return pg18test.Open(t, pg18test.Fixture{
		Domain:         "DELIVERY",
		DatabasePrefix: "pandora_delivery_",
		MarkerTable:    "pandora_delivery_test_marker",
		CommentTag:     "pandora-delivery-pg18",
	})
}

// deliveryHarness 是 delivery 域后台测试的公共装配：真实 aegis_app 连接、挂了本机
// realtime Hub 的 nodefabric 服务，以及订阅在租户节点频道上的事件接收端。
type deliveryHarness struct {
	t      *testing.T
	ctx    context.Context
	tenant string
	actor  string
	router chi.Router
	nodes  *nodefabric.Service
	events <-chan realtime.Event
	// reauthed 是请求主体的 ReauthedRecently，默认 true；测字段级 reauth 时临时关掉
	reauthed bool
}

func newDeliveryHarness(t *testing.T, ctx context.Context, app *platformdb.Pool, tenant, actor string,
	perms []string) *deliveryHarness {
	hub := realtime.NewHub(nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(hub.Close)
	events, unsubscribe := hub.Subscribe([]string{realtime.ChannelNodeAll(tenant)})
	t.Cleanup(unsubscribe)
	nodes := nodefabric.NewService(app, nil)
	nodes.AttachRealtime(hub)

	h := &handlers{d: Deps{Pool: app, Node: nodes, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}}
	d := &deliveryHarness{t: t, ctx: ctx, tenant: tenant, actor: actor, nodes: nodes, events: events, reauthed: true}
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			c := httpx.WithTenantID(req.Context(), tenant)
			c = httpx.WithPrincipal(c, &httpx.Principal{Kind: "admin", Audience: "admin",
				UserID: actor, TenantID: tenant, Permissions: perms, ReauthedRecently: d.reauthed})
			next.ServeHTTP(w, req.WithContext(c))
		})
	})
	// 直接挂处理器：权限、路由级 reauth 与幂等由路由契约测试守，这里证明处理器本身
	r.Post("/v1/plans/{id}/pools", h.setPlanPools)
	r.Get("/v1/nodes", h.nodeList)
	r.Get("/v1/node-pools", h.listNodePools)
	r.Post("/v1/node-pools", h.createNodePool)
	r.Post("/v1/node-pools/{id}", h.updateNodePool)
	r.Get("/v1/user-groups", h.listUserGroups)
	r.Delete("/v1/user-groups/{id}", h.deleteUserGroup)
	r.Post("/v1/users/{id}/group", h.assignUserGroup)
	d.router = r
	return d
}

func (d *deliveryHarness) do(method, path, body string) *httptest.ResponseRecorder {
	d.t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	d.router.ServeHTTP(w, req.WithContext(d.ctx))
	return w
}

// usersChanged 取走已收到的全部事件，返回其中租户级 node.users.changed 的条数。
// 本机 Hub 同步投递，请求返回时事件已在通道里。
func (d *deliveryHarness) usersChanged() int {
	n := 0
	for {
		select {
		case ev := <-d.events:
			if _, scoped := ev.Payload["node_id"]; ev.Topic == realtime.TopicNodeUsersChanged && !scoped {
				n++
			}
		default:
			return n
		}
	}
}

// TestDeliveryAdminPG18 证明两件事：
//
//  1. 套餐版本换绑节点池（POST v1/plans/{id}/pools）提交后发一次租户级
//     node.users.changed（R104，过去不发）；被拒的请求什么都没改，不发。
//  2. 后台节点列表的 delivered_to_users 与节点实际拉到的用户同口径：无池节点
//     报「未划入节点池，不服务任何用户」，节点用户列表也确实为空。
func TestDeliveryAdminPG18(t *testing.T) {
	ctx, admin, app := openDeliveryPG18(t)

	const (
		tenant    = "93000000-0000-4000-8000-000000000101"
		actor     = "93000000-0000-4000-8000-000000000111"
		owner     = "93000000-0000-4000-8000-000000000112"
		product   = "93000000-0000-4000-8000-000000000121"
		plan      = "93000000-0000-4000-8000-000000000131"
		published = "93000000-0000-4000-8000-000000000141"
		draft     = "93000000-0000-4000-8000-000000000142"
		pool      = "93000000-0000-4000-8000-000000000151"
		server    = "93000000-0000-4000-8000-000000000161"
		pooled    = "93000000-0000-4000-8000-000000000171"
		noPool    = "93000000-0000-4000-8000-000000000172"
		sub       = "93000000-0000-4000-8000-000000000181"
	)
	must := func(sql string, args ...any) {
		t.Helper()
		if _, err := admin.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("seed delivery admin fixture: %v\nSQL: %s", err, sql)
		}
	}
	must(`INSERT INTO tenants(id,slug,display_name,default_currency) VALUES($1,'delivery-admin-pg18','Delivery Admin PG18','USD')`, tenant)
	must(`INSERT INTO users(id,tenant_id,email,display_name,status) VALUES($2,$1,'operator@delivery-admin.invalid','Operator','active')`, tenant, actor)
	must(`INSERT INTO users(id,tenant_id,email,display_name,status) VALUES($2,$1,'owner@delivery-admin.invalid','Owner','active')`, tenant, owner)
	must(`INSERT INTO products(id,tenant_id,code,name,status) VALUES($2,$1,'delivery-admin-product','Delivery Admin Product','active')`, tenant, product)
	must(`INSERT INTO node_pools(id,tenant_id,code,name,status) VALUES($2,$1,'delivery-admin-pool','Delivery Admin Pool','active')`, tenant, pool)
	must(`INSERT INTO plans(id,tenant_id,product_id,code,name,status) VALUES($3,$1,$2,'delivery-admin-plan','Delivery Admin Plan','draft')`, tenant, product, plan)
	must(`INSERT INTO plan_versions(id,tenant_id,plan_id,version,created_by) VALUES($3,$1,$2,1,$4)`, tenant, plan, published, actor)
	must(`INSERT INTO plan_node_pools(tenant_id,plan_version_id,pool_id) VALUES($1,$2,$3)`, tenant, published, pool)
	must(`UPDATE plan_versions SET frozen_at=now() WHERE tenant_id=$1 AND id=$2`, tenant, published)
	must(`UPDATE plans SET current_version_id=$2,status='active' WHERE tenant_id=$1 AND id=$3`, tenant, published, plan)
	must(`INSERT INTO plan_versions(id,tenant_id,plan_id,version,created_by) VALUES($3,$1,$2,2,$4)`, tenant, plan, draft, actor)
	must(`INSERT INTO servers(id,tenant_id,name,status) VALUES($2,$1,'delivery-admin-server','ready')`, tenant, server)
	for _, node := range []struct {
		id, name, host string
		pool           any
	}{
		{pooled, "delivery-admin-pooled", "pooled.delivery-admin.invalid", pool},
		{noPool, "delivery-admin-no-pool", "nopool.delivery-admin.invalid", nil},
	} {
		must(`INSERT INTO nodes(id,tenant_id,name,pool_id,status,node_type,server_host,server_port,
				server_id,serving_status,protocol_schema_version,config_validated_at,last_heartbeat_at)
			  VALUES($2,$1,$3,$4,'active','vless',$5,443,$6,'active',1,now(),now())`,
			tenant, node.id, node.name, node.pool, node.host, server)
	}
	must(`INSERT INTO subscriptions(id,tenant_id,user_id,plan_id,plan_version_id,status,snapshot_currency,snapshot_amount)
		  VALUES($1,$2,$3,$4,$5,'active','USD',100)`, sub, tenant, owner, plan, published)

	d := newDeliveryHarness(t, ctx, app, tenant, actor, []string{"catalog.publish", "node.read"})

	// --- 1. 换绑节点池：成功才通知 ---
	rowVersion := func(id string) int64 {
		t.Helper()
		var v int64
		if err := admin.QueryRow(ctx, `SELECT row_version FROM plan_versions WHERE id=$1`, id).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	bind := func(version string) *httptest.ResponseRecorder {
		body := `{"version_id":"` + version + `","expected_version_row_version":` +
			strconv.FormatInt(rowVersion(version), 10) + `,"pool_ids":["` + pool + `"]}`
		return d.do(http.MethodPost, "/v1/plans/"+plan+"/pools", body)
	}
	if w := bind(published); w.Code != http.StatusConflict {
		t.Fatalf("binding a published version = %d %s, want 409", w.Code, w.Body)
	}
	if n := d.usersChanged(); n != 0 {
		t.Fatalf("rejected pool binding published %d node.users.changed, want 0", n)
	}
	if w := bind(draft); w.Code != http.StatusOK {
		t.Fatalf("binding the draft version = %d %s, want 200", w.Code, w.Body)
	}
	if n := d.usersChanged(); n != 1 {
		t.Fatalf("committed pool binding published %d node.users.changed, want 1", n)
	}

	// --- 2. 节点列表的交付判定与节点实际拉到的用户同口径 ---
	w := d.do(http.MethodGet, "/v1/nodes", "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET v1/nodes = %d %s", w.Code, w.Body)
	}
	var list struct {
		Nodes []struct {
			ID           string `json:"id"`
			Delivered    bool   `json:"delivered_to_users"`
			DeliveryNote string `json:"delivery_note"`
		} `json:"nodes"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode node list: %v", err)
	}
	seen := map[string]bool{}
	for _, n := range list.Nodes {
		poolID := func() *string {
			if n.ID == pooled {
				p := pool
				return &p
			}
			return nil
		}()
		users, err := d.nodes.ListNodeUsers(ctx, tenant, &nodefabric.ServingNode{ID: n.ID, PoolID: poolID})
		if err != nil {
			t.Fatalf("ListNodeUsers(%s): %v", n.ID, err)
		}
		switch n.ID {
		case pooled:
			if !n.Delivered || n.DeliveryNote != "" || len(users) != 1 {
				t.Fatalf("pooled node delivered=%v note=%q users=%d, want true/empty/1", n.Delivered, n.DeliveryNote, len(users))
			}
		case noPool:
			if n.Delivered || n.DeliveryNote != "未划入节点池，不服务任何用户" || len(users) != 0 {
				t.Fatalf("no-pool node delivered=%v note=%q users=%d, want false/未划入节点池/0", n.Delivered, n.DeliveryNote, len(users))
			}
		}
		seen[n.ID] = true
	}
	if !seen[pooled] || !seen[noPool] {
		t.Fatalf("node list missing fixture nodes: %+v", list.Nodes)
	}
	t.Log("delivery_admin_pg18 plan_pools_notify=commit-only node_list_delivery=node_users")
}
