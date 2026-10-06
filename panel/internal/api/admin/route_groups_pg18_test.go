package admin

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/aegispanel/aegis/internal/domain/nodefabric"
	platformcrypto "github.com/aegispanel/aegis/internal/platform/crypto"
	platformdb "github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
	"github.com/aegispanel/aegis/internal/platform/realtime"
)

func TestRouteGroupsPG18(t *testing.T) {
	ctx, admin, app := openDeliveryPG18(t)

	const (
		tenant  = "96000000-0000-4000-8000-000000000001"
		other   = "96000000-0000-4000-8000-000000000002"
		actor   = "96000000-0000-4000-8000-000000000011"
		nodeHK1 = "96000000-0000-4000-8000-000000000101"
		nodeHK2 = "96000000-0000-4000-8000-000000000102"
		nodeEtc = "96000000-0000-4000-8000-000000000103"
	)
	must := func(sql string, args ...any) {
		t.Helper()
		if _, err := admin.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("seed route group fixture: %v\nSQL: %s", err, sql)
		}
	}
	must(`INSERT INTO tenants(id,slug,display_name,default_currency) VALUES($1,'route-groups-pg18','Route Groups PG18','USD')`, tenant)
	must(`INSERT INTO tenants(id,slug,display_name,default_currency) VALUES($1,'route-groups-other','Route Groups Other','USD')`, other)
	must(`INSERT INTO users(id,tenant_id,email,display_name,status) VALUES($2,$1,'ops@route-groups.invalid','Ops','active')`, tenant, actor)
	for i, id := range []string{nodeHK1, nodeHK2, nodeEtc} {
		must(`INSERT INTO nodes(id,tenant_id,name,status,serving_status,node_type,server_host,server_port,kernel,protocol_config)
			VALUES($2,$1,$3,'active','active','vless','node.invalid',443,'auto','{}')`,
			tenant, id, "rg-node-"+strconv.Itoa(i+1))
	}

	signer, err := platformcrypto.NewSigner([]byte("route-groups-pg18-seed-32-bytes!"))
	if err != nil {
		t.Fatal(err)
	}
	hub := realtime.NewHub(nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(hub.Close)
	events, unsubscribe := hub.Subscribe([]string{realtime.ChannelNodeAll(tenant)})
	t.Cleanup(unsubscribe)
	nodes := nodefabric.NewService(app, signer)
	nodes.AttachRealtime(hub)
	h := &handlers{d: Deps{Pool: app, Node: nodes, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}}
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			c := httpx.WithTenantID(req.Context(), tenant)
			c = httpx.WithPrincipal(c, &httpx.Principal{Kind: "admin", Audience: "admin",
				UserID: actor, TenantID: tenant, ReauthedRecently: true})
			next.ServeHTTP(w, req.WithContext(c))
		})
	})
	// 直接挂处理器：权限、重认证与幂等由 route_groups_routes_test.go 守
	r.Get("/v1/route-groups", h.listRouteGroups)
	r.Post("/v1/route-groups", h.createRouteGroup)
	r.Patch("/v1/route-groups/{id}", h.updateRouteGroup)
	r.Delete("/v1/route-groups/{id}", h.deleteRouteGroup)
	r.Get("/v1/route-groups/{id}/routing", h.getRouteGroupRouting)
	r.Put("/v1/route-groups/{id}/routing", h.setRouteGroupRouting)
	r.Put("/v1/route-groups/{id}/members", h.setRouteGroupMembers)
	r.Put("/v1/nodes/{id}/route-groups", h.setNodeRouteGroups)
	r.Get("/v1/nodes/{id}/routing", h.nodeGetRouting)
	r.Put("/v1/nodes/{id}/routing", h.nodeSetRouting)
	r.Get("/v1/nodes/{id}/routing/effective", h.nodeEffectiveRouting)

	do := func(method, path, body string, code int, dst any) string {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req.WithContext(ctx))
		if w.Code != code {
			t.Fatalf("%s %s: status=%d body=%s, want %d", method, path, w.Code, w.Body.String(), code)
		}
		if dst != nil {
			if err := json.Unmarshal(w.Body.Bytes(), dst); err != nil {
				t.Fatal(err)
			}
		}
		return w.Body.String()
	}
	scalar := func(sql string, args ...any) int64 {
		t.Helper()
		var v int64
		if err := admin.QueryRow(ctx, sql, args...).Scan(&v); err != nil {
			t.Fatalf("%v\nSQL: %s", err, sql)
		}
		return v
	}
	generation := func(id string) int64 {
		return scalar(`SELECT config_source_generation FROM nodes WHERE id=$1`, id)
	}
	nodeVersion := func(id string) int64 { return scalar(`SELECT row_version FROM nodes WHERE id=$1`, id) }
	notified := func() map[string]int {
		out := map[string]int{}
		for {
			select {
			case ev := <-events:
				if ev.Topic == realtime.TopicNodeConfigChanged {
					id, _ := ev.Payload["node_id"].(string)
					out[id]++
				}
			default:
				return out
			}
		}
	}
	// effective 拿节点的有效发布物，返回 generation 与按顺序的规则出站 tag、出站 tag→type
	effective := func(id string) (uint64, []string, map[string]string) {
		t.Helper()
		cfg, err := nodes.FetchEffectiveConfig(ctx, tenant, id)
		if err != nil {
			t.Fatalf("FetchEffectiveConfig(%s): %v", id, err)
		}
		var payload struct {
			Outbounds []nodefabric.NodeOutbound `json:"outbounds"`
			Routes    []nodefabric.NodeRoute    `json:"routes"`
		}
		if err := json.Unmarshal(cfg.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		var routes []string
		for _, x := range payload.Routes {
			routes = append(routes, x.OutboundTag)
		}
		outs := map[string]string{}
		for _, o := range payload.Outbounds {
			outs[o.Tag] = o.Type
		}
		return cfg.Generation, routes, outs
	}

	// 全局：一条公共出站 pub 与一条 80 端口规则
	if _, err := nodes.SetGlobalRouting(ctx, nodefabric.SetGlobalRoutingInput{TenantID: tenant, ActorID: actor,
		ExpectedRevision: mustGlobalRevision(t, nodes, tenant),
		Outbounds:        []nodefabric.RoutingOutbound{{Tag: "pub", Type: "socks", Settings: json.RawMessage(`{}`)}},
		Routes:           []nodefabric.RoutingRule{{Matcher: json.RawMessage(`{"port":[80]}`), OutboundTag: "pub", Enabled: true}},
	}); err != nil {
		t.Fatalf("seed global routing: %v", err)
	}
	gen0, routes0, _ := effective(nodeHK1)
	if strings.Join(routes0, ",") != "pub" {
		t.Fatalf("before groups node routes = %v", routes0)
	}
	_ = notified()

	// --- 建组：名称大小写不敏感唯一 ---
	var hk, jp nodefabric.RouteGroup
	do(http.MethodPost, "/v1/route-groups", `{"name":"HK 解锁","description":"流媒体","sort_order":10}`, http.StatusCreated, &hk)
	do(http.MethodPost, "/v1/route-groups", `{"name":"JP 中转","sort_order":20}`, http.StatusCreated, &jp)
	do(http.MethodPost, "/v1/route-groups", `{"name":"hk 解锁"}`, http.StatusConflict, nil)
	if hk.RowVersion != 1 || len(hk.Members) != 0 {
		t.Fatalf("created group = %+v", hk)
	}

	// --- 组内路由：组规则不能指向节点私有出站；可以覆盖全局同名出站 ---
	hkPath := "/v1/route-groups/" + hk.ID
	do(http.MethodPut, hkPath+"/routing", `{"row_version":1,"outbounds":[],"routes":[{"matcher":{"port":[1]},"outbound_tag":"nowhere","enabled":true}]}`,
		http.StatusUnprocessableEntity, nil)
	var put struct {
		RowVersion    int64 `json:"row_version"`
		AffectedNodes int   `json:"affected_nodes"`
	}
	do(http.MethodPut, hkPath+"/routing", `{"row_version":1,
		"outbounds":[{"tag":"unlock","type":"trojan","settings":{}},{"tag":"pub","type":"http","settings":{}}],
		"routes":[{"matcher":{"domain_suffix":["netflix.com"]},"outbound_tag":"unlock","enabled":true}]}`, http.StatusOK, &put)
	if put.RowVersion != 2 || put.AffectedNodes != 0 {
		t.Fatalf("empty group publish = %+v, want row_version 2 and no affected nodes", put)
	}
	do(http.MethodPut, hkPath+"/routing", `{"row_version":1,"outbounds":[],"routes":[]}`, http.StatusConflict, nil)
	if gen := generation(nodeHK1); gen != int64(gen0) {
		t.Fatalf("non-member generation moved: %d -> %d", gen0, gen)
	}

	// --- 成员：进组的节点推进 generation 与行版本、收到通知，组外节点不动 ---
	etcGen, hk1Version := generation(nodeEtc), nodeVersion(nodeHK1)
	do(http.MethodPut, hkPath+"/members", `{"row_version":2,"node_ids":["`+nodeHK1+`","`+nodeHK2+`","`+nodeHK1+`"]}`, http.StatusOK, &put)
	if put.RowVersion != 3 || put.AffectedNodes != 2 {
		t.Fatalf("members = %+v", put)
	}
	if n := notified(); n[nodeHK1] != 1 || n[nodeHK2] != 1 || n[nodeEtc] != 0 {
		t.Fatalf("member notifications = %v", n)
	}
	if generation(nodeEtc) != etcGen || nodeVersion(nodeHK1) != hk1Version+1 {
		t.Fatal("members write must bump joined nodes only")
	}
	do(http.MethodPut, hkPath+"/members", `{"row_version":3,"node_ids":["96000000-0000-4000-8000-0000000009ff"]}`, http.StatusUnprocessableEntity, nil)

	// 有效发布物真的变了：新 generation，规则 组 → 全局，pub 被组覆盖成 http
	gen1, routes1, outs1 := effective(nodeHK1)
	if gen1 <= gen0 || strings.Join(routes1, ",") != "unlock,pub" || outs1["pub"] != "http" || outs1["unlock"] != "trojan" {
		t.Fatalf("member effective = gen %d routes %v outs %v (before gen %d)", gen1, routes1, outs1, gen0)
	}
	if _, routes, outs := effective(nodeEtc); strings.Join(routes, ",") != "pub" || outs["pub"] != "socks" || outs["unlock"] != "" {
		t.Fatalf("non-member effective leaked group routing: %v %v", routes, outs)
	}

	// --- 节点私有规则可以指向所在组的出站；组外节点不行 ---
	ruleTo := func(tag string) string {
		return `"outbounds":[],"routes":[{"matcher":{"port":[443]},"outbound_tag":"` + tag + `","enabled":true}]}`
	}
	// 引用按 tag 原样精确比较（与下发、pdnd 查表一致）：大小写或空白不同都拒，内置名沿用不分大小写
	for _, tag := range []string{"UNLOCK", "Unlock", "unlock ", "PUB"} {
		do(http.MethodPut, "/v1/nodes/"+nodeHK1+"/routing", `{"row_version":`+strconv.FormatInt(nodeVersion(nodeHK1), 10)+`,`+ruleTo(tag), http.StatusUnprocessableEntity, nil)
	}
	do(http.MethodPut, hkPath+"/routing", `{"row_version":3,"outbounds":[{"tag":"unlock","type":"trojan","settings":{}},{"tag":"pub","type":"http","settings":{}}],
		"routes":[{"matcher":{"port":[1]},"outbound_tag":"Unlock","enabled":true}]}`, http.StatusUnprocessableEntity, nil)
	do(http.MethodPut, "/v1/nodes/"+nodeEtc+"/routing", `{"row_version":`+strconv.FormatInt(nodeVersion(nodeEtc), 10)+`,`+ruleTo("Direct"), http.StatusOK, nil)
	// 内置出站引用在保存与下发两处都规范成小写：读回与有效发布物里都是 direct
	var etcRouting nodefabric.NodeRouting
	do(http.MethodGet, "/v1/nodes/"+nodeEtc+"/routing", "", http.StatusOK, &etcRouting)
	if len(etcRouting.Routes) != 1 || etcRouting.Routes[0].OutboundTag != "direct" {
		t.Fatalf("saved builtin ref = %+v, want direct", etcRouting.Routes)
	}
	if _, routes, _ := effective(nodeEtc); strings.Join(routes, ",") != "direct,pub" {
		t.Fatalf("effective builtin ref = %v, want direct,pub", routes)
	}
	// 库里已有的大小写旧行（不经保存入口）在下发时同样规范，不用写迁移
	must(`INSERT INTO node_routes(tenant_id,node_id,priority,matcher,outbound_tag,enabled) VALUES($1,$2,5,'{"port":[25]}',' BLOCK ',true)`, tenant, nodeEtc)
	must(`UPDATE nodes SET config_source_generation=config_source_generation+1 WHERE id=$1`, nodeEtc)
	if _, routes, _ := effective(nodeEtc); strings.Join(routes, ",") != "block,direct,pub" {
		t.Fatalf("legacy builtin row delivered as %v, want block,direct,pub", routes)
	}
	do(http.MethodPut, "/v1/nodes/"+nodeEtc+"/routing", `{"row_version":`+strconv.FormatInt(nodeVersion(nodeEtc), 10)+`,"outbounds":[],"routes":[]}`, http.StatusOK, nil)
	do(http.MethodPut, "/v1/nodes/"+nodeHK1+"/routing", `{"row_version":`+strconv.FormatInt(nodeVersion(nodeHK1), 10)+`,`+ruleTo("unlock"), http.StatusOK, nil)
	do(http.MethodPut, "/v1/nodes/"+nodeEtc+"/routing", `{"row_version":`+strconv.FormatInt(nodeVersion(nodeEtc), 10)+`,`+ruleTo("unlock"), http.StatusUnprocessableEntity, nil)
	if _, routes, _ := effective(nodeHK1); strings.Join(routes, ",") != "unlock,unlock,pub" {
		t.Fatalf("node → group → global order = %v", routes)
	}

	// 生效预览：来源逐条标注，与有效发布物同口径
	var preview nodefabric.EffectiveRouting
	do(http.MethodGet, "/v1/nodes/"+nodeHK1+"/routing/effective", "", http.StatusOK, &preview)
	var scopes []string
	for _, x := range preview.Routes {
		scopes = append(scopes, x.Source.Scope)
	}
	if len(preview.Groups) != 1 || preview.Groups[0].ID != hk.ID || strings.Join(scopes, ",") != "node,group,global" {
		t.Fatalf("preview = %+v", preview)
	}
	for _, o := range preview.Outbounds {
		if o.Tag == "pub" && (o.Source.Scope != "group" || o.Source.GroupName != "HK 解锁") {
			t.Fatalf("pub source = %+v, want the group override", o.Source)
		}
	}
	var nodeRouting nodefabric.NodeRouting
	do(http.MethodGet, "/v1/nodes/"+nodeHK1+"/routing", "", http.StatusOK, &nodeRouting)
	if len(nodeRouting.Groups) != 1 || nodeRouting.Groups[0].Name != "HK 解锁" {
		t.Fatalf("node routing groups = %+v", nodeRouting.Groups)
	}

	// --- 新造成的悬空引用被拒，事务整体回滚 ---
	refused := do(http.MethodPut, hkPath+"/routing", `{"row_version":3,"outbounds":[],"routes":[]}`, http.StatusConflict, nil)
	if !strings.Contains(refused, "rg-node-1") {
		t.Fatalf("dangling refusal must name the node: %s", refused)
	}
	refused = do(http.MethodPut, hkPath+"/members", `{"row_version":3,"node_ids":["`+nodeHK2+`"]}`, http.StatusConflict, nil)
	if !strings.Contains(refused, "rg-node-1") || scalar(`SELECT count(*) FROM route_group_members WHERE group_id=$1`, hk.ID) != 2 {
		t.Fatalf("leaving the group with a dangling rule must be refused and rolled back: %s", refused)
	}
	do(http.MethodDelete, hkPath, `{"row_version":3}`, http.StatusConflict, nil)

	// --- 节点侧改所在组：组与节点行版本互相推进，组序决定覆盖 ---
	do(http.MethodPut, "/v1/route-groups/"+jp.ID+"/routing", `{"row_version":1,
		"outbounds":[{"tag":"unlock","type":"http","settings":{}}],
		"routes":[{"matcher":{"port":[22]},"outbound_tag":"direct","enabled":true}]}`, http.StatusOK, nil)
	do(http.MethodPut, "/v1/nodes/"+nodeHK2+"/route-groups",
		`{"row_version":`+strconv.FormatInt(nodeVersion(nodeHK2), 10)+`,"group_ids":["`+jp.ID+`","`+hk.ID+`"]}`, http.StatusOK, nil)
	if v := scalar(`SELECT row_version FROM route_groups WHERE id=$1`, jp.ID); v != 3 {
		t.Fatalf("jp row_version = %d, want 3 (routing + member join)", v)
	}
	if _, routes, outs := effective(nodeHK2); strings.Join(routes, ",") != "unlock,direct,pub" || outs["unlock"] != "trojan" {
		t.Fatalf("two groups: routes %v outs %v, want HK (sort 10) before JP (sort 20)", routes, outs)
	}
	do(http.MethodPatch, "/v1/route-groups/"+jp.ID, `{"row_version":3,"sort_order":5}`, http.StatusOK, nil)
	if _, routes, outs := effective(nodeHK2); strings.Join(routes, ",") != "direct,unlock,pub" || outs["unlock"] != "http" {
		t.Fatalf("after reorder: routes %v outs %v, want JP first", routes, outs)
	}
	do(http.MethodPut, "/v1/nodes/"+nodeHK2+"/route-groups", `{"row_version":1,"group_ids":[]}`, http.StatusConflict, nil)

	// --- 三选一与组内 tag 唯一：库层兜底 ---
	pgCode := func(sql string, args ...any) string {
		t.Helper()
		_, err := admin.Exec(ctx, sql, args...)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) {
			t.Fatalf("expected a database refusal, got %v\nSQL: %s", err, sql)
		}
		return pgErr.Code
	}
	if c := pgCode(`INSERT INTO node_outbounds(tenant_id,node_id,group_id,tag,type) VALUES($1,$2,$3,'both','socks')`, tenant, nodeEtc, hk.ID); c != "23514" {
		t.Fatalf("node+group outbound = %s, want check_violation", c)
	}
	if c := pgCode(`INSERT INTO node_routes(tenant_id,node_id,group_id,outbound_tag) VALUES($1,$2,$3,'direct')`, tenant, nodeEtc, hk.ID); c != "23514" {
		t.Fatalf("node+group route = %s, want check_violation", c)
	}
	if c := pgCode(`INSERT INTO node_outbounds(tenant_id,group_id,tag,type) VALUES($1,$2,'unlock','socks')`, tenant, hk.ID); c != "23505" {
		t.Fatalf("duplicate group tag = %s, want unique_violation", c)
	}
	if c := pgCode(`INSERT INTO node_outbounds(tenant_id,group_id,tag,type) VALUES($1,$2,'x','socks')`, other, hk.ID); c != "23503" {
		t.Fatalf("cross-tenant group outbound = %s, want foreign_key_violation", c)
	}

	// --- RLS：另一租户既看不见也写不进 ---
	err = app.InTx(ctx, platformdb.Scope{TenantID: other}, func(tx pgx.Tx) error {
		var n int
		if err := tx.QueryRow(ctx, `SELECT (SELECT count(*) FROM route_groups) + (SELECT count(*) FROM route_group_members)`).Scan(&n); err != nil {
			return err
		}
		if n != 0 {
			t.Errorf("other tenant sees %d route group rows", n)
		}
		_, err := tx.Exec(ctx, `INSERT INTO route_groups(tenant_id,name) VALUES($1,'sneak')`, tenant)
		return err
	})
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
		t.Fatalf("cross-tenant insert = %v, want RLS refusal", err)
	}

	// --- 删组：先把私有规则改走，再删；组内出站规则与成员级联清掉，成员节点推进并收到通知 ---
	do(http.MethodPut, "/v1/nodes/"+nodeHK1+"/routing", `{"row_version":`+strconv.FormatInt(nodeVersion(nodeHK1), 10)+`,"outbounds":[],"routes":[]}`, http.StatusOK, nil)
	_ = notified()
	hk1Gen, hk2Gen, etcGen := generation(nodeHK1), generation(nodeHK2), generation(nodeEtc)
	var deleted struct {
		Deleted       bool `json:"deleted"`
		AffectedNodes int  `json:"affected_nodes"`
	}
	do(http.MethodDelete, hkPath, `{"row_version":3}`, http.StatusOK, &deleted)
	if !deleted.Deleted || deleted.AffectedNodes != 2 {
		t.Fatalf("delete = %+v", deleted)
	}
	if n := scalar(`SELECT (SELECT count(*) FROM node_outbounds WHERE group_id=$1)
		+ (SELECT count(*) FROM node_routes WHERE group_id=$1)
		+ (SELECT count(*) FROM route_group_members WHERE group_id=$1)`, hk.ID); n != 0 {
		t.Fatalf("group rows left after delete: %d", n)
	}
	if generation(nodeHK1) != hk1Gen+1 || generation(nodeHK2) != hk2Gen+1 || generation(nodeEtc) != etcGen {
		t.Fatal("delete must bump exactly the former members")
	}
	if n := notified(); n[nodeHK1] != 1 || n[nodeHK2] != 1 || n[nodeEtc] != 0 {
		t.Fatalf("delete notifications = %v", n)
	}
	if _, routes, outs := effective(nodeHK1); strings.Join(routes, ",") != "pub" || outs["pub"] != "socks" || outs["unlock"] != "" {
		t.Fatalf("after delete node keeps group routing: %v %v", routes, outs)
	}
	do(http.MethodGet, hkPath+"/routing", "", http.StatusNotFound, nil)

	for _, action := range []string{"route_group.create", "node.routing.group_publish", "route_group.members_update",
		"node.route_groups_update", "route_group.update", "route_group.delete"} {
		if scalar(`SELECT count(*) FROM audit_events WHERE tenant_id=$1 AND action=$2`, tenant, action) == 0 {
			t.Errorf("no audit row for %s", action)
		}
	}
}

func mustGlobalRevision(t *testing.T, nodes *nodefabric.Service, tenant string) string {
	t.Helper()
	g, err := nodes.GetGlobalRouting(t.Context(), tenant)
	if err != nil {
		t.Fatal(err)
	}
	return g.Revision
}
