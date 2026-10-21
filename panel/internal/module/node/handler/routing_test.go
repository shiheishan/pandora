package handler

// 路由与路由组接口的 handler 测试：不连库，只测碰库之前就返回的路径（路径参数、请求体、id 列表、
// 重认证与幂等的挂法、service 的纯校验）与 DTO 形状（对照前端 zod schema）。

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/aegispanel/aegis/internal/module/node/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/httpx"
	"github.com/aegispanel/aegis/internal/pkg/route"
)

// routingKitStub 记录重认证被调的次数与注册时传给幂等中间件的作用域名。
type routingKitStub struct {
	reauthCalls int
	denyReauth  bool
	scopes      []string
}

func newRoutingEngine(stub *routingKitStub) *gin.Engine {
	gin.SetMode(gin.TestMode)
	e := gin.New()
	e.Use(httpx.RequestID(zap.NewNop()), httpx.Errors())
	pass := func(c *gin.Context) { c.Next() }
	kit := route.Kit{
		RequireAuth: pass,
		RecentReauth: func(c *gin.Context) {
			stub.reauthCalls++
			if stub.denyReauth {
				httpx.Fail(c, errs.New(errs.ReauthRequired))
				return
			}
			c.Next()
		},
		Idempotency: func(scope string) gin.HandlerFunc {
			stub.scopes = append(stub.scopes, scope)
			return pass
		},
		Switch: func(string) gin.HandlerFunc { return pass },
	}
	New(service.New(nil, nil, nil, nil)).registerRouting(e.Group("/v1"), kit)
	return e
}

func doRouting(e *gin.Engine, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)
	return w
}

func routingExpectErr(t *testing.T, w *httptest.ResponseRecorder, status int, code errs.Code, field string) {
	t.Helper()
	if w.Code != status {
		t.Fatalf("状态码 %d，期望 %d：%s", w.Code, status, w.Body)
	}
	var env struct {
		Error struct {
			Code   string            `json:"code"`
			Fields map[string]string `json:"fields"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("错误信封解析失败: %v %s", err, w.Body)
	}
	if env.Error.Code != string(code) {
		t.Fatalf("错误码 %q，期望 %q：%s", env.Error.Code, code, w.Body)
	}
	if field != "" && env.Error.Fields[field] == "" {
		t.Fatalf("缺少字段错误 %q：%s", field, w.Body)
	}
}

// routingKeys 把 v 编成 JSON 再解成对象，返回排好序的键与对象本身。
func routingKeys(t *testing.T, v any) ([]string, map[string]any) {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys, m
}

func routingWantKeys(t *testing.T, got []string, want ...string) {
	t.Helper()
	sort.Strings(want)
	if len(got) != len(want) {
		t.Fatalf("键 %v，期望 %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("键 %v，期望 %v", got, want)
		}
	}
}

func TestRoutingPathIDNotUUID(t *testing.T) {
	e := newRoutingEngine(&routingKitStub{})
	routingExpectErr(t, doRouting(e, http.MethodGet, "/v1/nodes/xyz/routing", ""), http.StatusNotFound, errs.NotFound, "")
	routingExpectErr(t, doRouting(e, http.MethodGet, "/v1/nodes/xyz/routing/effective", ""), http.StatusNotFound, errs.NotFound, "")
	routingExpectErr(t, doRouting(e, http.MethodPut, "/v1/route-groups/xyz/routing", `{"row_version":1}`), http.StatusNotFound, errs.NotFound, "")
	routingExpectErr(t, doRouting(e, http.MethodGet, "/v1/route-groups/xyz/routing", ""), http.StatusNotFound, errs.NotFound, "")
}

func TestRoutingBadBody(t *testing.T) {
	e := newRoutingEngine(&routingKitStub{})
	id := uuid.NewString()
	routingExpectErr(t, doRouting(e, http.MethodPut, "/v1/nodes/"+id+"/routing", `{`), http.StatusBadRequest, errs.BadRequest, "")
	routingExpectErr(t, doRouting(e, http.MethodPut, "/v1/nodes/"+id+"/routing", `{"row_version":1,"extra":1}`), http.StatusBadRequest, errs.BadRequest, "")
	routingExpectErr(t, doRouting(e, http.MethodPost, "/v1/route-groups", `{"name":"a","x":1}`), http.StatusBadRequest, errs.BadRequest, "")
}

func TestRoutingBadIDLists(t *testing.T) {
	e := newRoutingEngine(&routingKitStub{})
	id := uuid.NewString()
	routingExpectErr(t, doRouting(e, http.MethodPut, "/v1/nodes/"+id+"/route-groups", `{"row_version":1,"group_ids":["bad"]}`),
		http.StatusUnprocessableEntity, errs.ValidationFailed, "group_ids")
}

func TestRoutingReauthRequired(t *testing.T) {
	stub := &routingKitStub{denyReauth: true}
	e := newRoutingEngine(stub)
	id := uuid.NewString()
	for _, c := range []struct{ method, path string }{
		{http.MethodPut, "/v1/nodes/routing"},
		{http.MethodPatch, "/v1/route-groups/" + id},
		{http.MethodDelete, "/v1/route-groups/" + id},
		{http.MethodPut, "/v1/route-groups/" + id + "/routing"},
	} {
		routingExpectErr(t, doRouting(e, c.method, c.path, `{"row_version":1}`), http.StatusForbidden, errs.ReauthRequired, "")
	}
	if stub.reauthCalls != 4 {
		t.Fatalf("重认证调用 %d 次，期望 4", stub.reauthCalls)
	}

	// 单节点路由、节点所在组、新建空组不要求重认证：走到 service 的纯校验
	stub.reauthCalls = 0
	routingExpectErr(t, doRouting(e, http.MethodPut, "/v1/nodes/"+id+"/routing", `{"row_version":0}`),
		http.StatusUnprocessableEntity, errs.ValidationFailed, "row_version")
	routingExpectErr(t, doRouting(e, http.MethodPut, "/v1/nodes/"+id+"/route-groups", `{"row_version":0,"group_ids":[]}`),
		http.StatusUnprocessableEntity, errs.ValidationFailed, "row_version")
	routingExpectErr(t, doRouting(e, http.MethodPost, "/v1/route-groups", `{"name":"  "}`),
		http.StatusUnprocessableEntity, errs.ValidationFailed, "name")
	if stub.reauthCalls != 0 {
		t.Fatalf("不该调重认证，调了 %d 次", stub.reauthCalls)
	}
}

func TestRoutingIdempotencyScopes(t *testing.T) {
	stub := &routingKitStub{}
	newRoutingEngine(stub)
	want := []string{
		"node_routing_global_publish", "route_group_create", "route_group_update",
		"route_group_delete", "route_group_routing_publish",
	}
	if len(stub.scopes) != len(want) {
		t.Fatalf("幂等作用域 %v，期望 %v", stub.scopes, want)
	}
	for i := range want {
		if stub.scopes[i] != want[i] {
			t.Fatalf("幂等作用域 %v，期望 %v", stub.scopes, want)
		}
	}
}

func TestRoutingServiceValidation(t *testing.T) {
	e := newRoutingEngine(&routingKitStub{})
	body := `{"expected_revision":"x","outbounds":[{"tag":"HK","type":"socks","settings":{}},{"tag":"hk","type":"socks","settings":{}}],"routes":[]}`
	routingExpectErr(t, doRouting(e, http.MethodPut, "/v1/nodes/routing", body), http.StatusUnprocessableEntity, errs.ValidationFailed, "outbounds")
}

func TestRoutingDTOShapes(t *testing.T) {
	gid := uuid.New()
	now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)

	keys, m := routingKeys(t, routeGroupFromService(service.RouteGroup{ID: gid, Name: "g", CreatedAt: now, UpdatedAt: now}))
	routingWantKeys(t, keys, "id", "name", "description", "sort_order", "row_version", "outbound_count", "rule_count",
		"members", "created_at", "updated_at")
	if members, ok := m["members"].([]any); !ok || len(members) != 0 {
		t.Fatalf("members 应为 []：%v", m["members"])
	}

	keys, m = routingKeys(t, nodeRoutingResponse{
		Groups: groupRefsFromService(nil), Outbounds: outboundsFromService(nil), Routes: routesFromService(nil),
	})
	routingWantKeys(t, keys, "row_version", "groups", "outbounds", "routes")
	for _, k := range []string{"groups", "outbounds", "routes"} {
		if l, ok := m[k].([]any); !ok || len(l) != 0 {
			t.Fatalf("%s 应为 []：%v", k, m[k])
		}
	}

	keys, _ = routingKeys(t, sourceFromService(service.RoutingSource{Scope: "global"}))
	routingWantKeys(t, keys, "scope")
	keys, _ = routingKeys(t, sourceFromService(service.RoutingSource{Scope: "group", GroupID: &gid, GroupName: "g"}))
	routingWantKeys(t, keys, "scope", "group_id", "group_name")

	eff := effectiveFromService(service.EffectiveRouting{
		Outbounds: []service.EffectiveOutbound{{RoutingOutbound: service.RoutingOutbound{Tag: "a", Type: "socks"}, Source: service.RoutingSource{Scope: "node"}}},
		Routes:    []service.EffectiveRoute{{Matcher: json.RawMessage(`{"port":[25]}`), Outbound: "block", Source: service.RoutingSource{Scope: "global"}}},
	})
	keys, m = routingKeys(t, eff)
	routingWantKeys(t, keys, "groups", "outbounds", "routes")
	if l, ok := m["groups"].([]any); !ok || len(l) != 0 {
		t.Fatalf("groups 应为 []：%v", m["groups"])
	}
	keys, _ = routingKeys(t, eff.Routes[0])
	routingWantKeys(t, keys, "matcher", "outbound", "source")
	keys, om := routingKeys(t, eff.Outbounds[0])
	routingWantKeys(t, keys, "tag", "type", "settings", "source")
	if s, ok := om["settings"].(map[string]any); !ok || len(s) != 0 {
		t.Fatalf("空 settings 应为 {}：%v", om["settings"])
	}

	keys, _ = routingKeys(t, routesFromService([]service.RoutingRule{{Priority: 10, OutboundTag: "direct"}})[0])
	routingWantKeys(t, keys, "priority", "matcher", "outbound_tag", "enabled", "note")
	keys, _ = routingKeys(t, globalRoutingResponse{})
	routingWantKeys(t, keys, "revision", "outbounds", "routes", "online_nodes")
	keys, _ = routingKeys(t, globalRoutingSavedResponse{OK: true})
	routingWantKeys(t, keys, "ok", "revision", "affected_nodes")
	keys, _ = routingKeys(t, groupRoutingSavedResponse{OK: true})
	routingWantKeys(t, keys, "ok", "row_version", "affected_nodes")
	keys, _ = routingKeys(t, routeGroupDeletedResponse{Deleted: true})
	routingWantKeys(t, keys, "deleted", "affected_nodes")
	keys, _ = routingKeys(t, routeGroupUpdatedResponse{})
	routingWantKeys(t, keys, "group", "affected_nodes")
	keys, _ = routingKeys(t, nodeRoutingSavedResponse{OK: true})
	routingWantKeys(t, keys, "ok", "row_version")
}
