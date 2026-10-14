package handler

// 服务器与节点接口的 handler 测试：不连库，只测在碰数据库之前就返回的路径
// （路由、路径参数、请求体、字段校验、重认证）与 DTO 形状（对照前端 screens/nodes/schemas.ts）。

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"go.uber.org/zap"

	"github.com/aegispanel/aegis/internal/module/node/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/httpx"
	"github.com/aegispanel/aegis/internal/pkg/route"
)

// newInventoryEngine 用一个没有数据库的 service 挂好后台路由；denyReauth 时重认证中间件一律拒绝。
func newInventoryEngine(denyReauth bool) *gin.Engine {
	gin.SetMode(gin.TestMode)
	e := gin.New()
	e.Use(httpx.RequestID(zap.NewNop()), httpx.Errors())
	pass := func(c *gin.Context) { c.Next() }
	reauth := pass
	if denyReauth {
		reauth = func(c *gin.Context) { httpx.Fail(c, errs.New(errs.ReauthRequired)) }
	}
	kit := route.Kit{
		RequireAuth:  pass,
		RecentReauth: reauth,
		Idempotency:  func(string) gin.HandlerFunc { return pass },
		Switch:       func(string) gin.HandlerFunc { return pass },
	}
	New(service.New(nil, nil, nil, nil)).RegisterAdmin(e.Group("/v1"), kit)
	return e
}

func doInventory(e *gin.Engine, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)
	return w
}

// inventoryExpectErr 核对错误响应的状态码、错误码与字段。
func inventoryExpectErr(t *testing.T, w *httptest.ResponseRecorder, code errs.Code, field string) {
	t.Helper()
	if w.Code != errs.New(code).Status() {
		t.Fatalf("状态码 %d，期望 %d：%s", w.Code, errs.New(code).Status(), w.Body)
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
		t.Fatalf("fields 缺 %s：%v", field, env.Error.Fields)
	}
}

// inventoryKeys 把 v 编成 JSON 再取对象的键（升序）与解出的对象。
func inventoryKeys(t *testing.T, v any) ([]string, map[string]any) {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys, m
}

func inventoryExpectKeys(t *testing.T, got []string, want []string) {
	t.Helper()
	w := append([]string(nil), want...)
	sort.Strings(w)
	if strings.Join(got, ",") != strings.Join(w, ",") {
		t.Fatalf("JSON 键\n%v\n期望\n%v", got, w)
	}
}

// 以下键表照抄前端 schemas.ts，前端改了这里同改。
var (
	inventoryNodeRowKeys = []string{
		"id", "node_no", "row_version", "name", "status", "serving_status", "server_id", "server_name",
		"pool_id", "pool_name", "agent_version", "hostname", "public_ipv4", "cpu_cores", "memory_mb", "disk_gb",
		"health_score", "applied_config_version", "desired_config_version", "last_heartbeat_at", "stale",
		"delivered_to_users", "delivery_note", "identity_serial", "created_at", "node_type", "server_host",
		"server_port", "display_name", "country_code", "sort_order", "online_users", "online_ips",
		"traffic_bytes_24h", "cpu_percent", "mem_percent", "metrics_at", "granted_plans",
		"desired_effective_generation", "applied_effective_generation", "effective_state", "last_apply_failure",
		"runtime_status", "runtime_reason", "runtime_state_at", "runtime_reason_node", "delivery_degraded",
	}
	inventoryNodeDetailExtra = []string{
		"traffic_rate", "protocol_config", "protocol_schema_version", "config_validated_at",
	}
	inventoryAdminNodeKeys = []string{
		"id", "row_version", "name", "server_id", "pool_id", "status", "serving_status", "node_type",
		"server_host", "server_port", "traffic_rate", "display_name", "country_code", "protocol_config",
		"protocol_schema_version", "config_validated_at", "sort_order", "created_at", "updated_at",
	}
	inventoryServerKeys = []string{
		"id", "name", "status", "status_reason", "row_version", "region", "hostname", "public_ipv4", "public_ipv6",
		"private_ipv4", "architecture", "os_name", "agent_version", "last_heartbeat_at", "heartbeat_online",
		"cpu_cores", "memory_mb", "disk_gb", "capacity_nodes", "notes", "control_node_id", "node_count",
		"active_node_count", "serving_node_count", "never_seen_node_count", "created_at", "updated_at",
		"cpu_bp", "mem_used_mb", "mem_total_mb", "disk_used_gb", "disk_total_gb", "metrics_at",
	}
	inventoryServerNodeKeys = []string{
		"id", "name", "status", "serving_status", "node_type", "display_name", "server_host", "server_port",
		"traffic_rate", "config_validated_at", "last_heartbeat_at", "created_at",
	}
)

func TestInventoryRoutesRegistered(t *testing.T) {
	e := newInventoryEngine(false)
	have := map[string]bool{}
	for _, r := range e.Routes() {
		have[r.Method+" "+r.Path] = true
	}
	for _, want := range []string{
		"GET /v1/nodes", "POST /v1/nodes", "PUT /v1/nodes/order", "POST /v1/nodes/config/publish",
		"POST /v1/nodes/reality-keypair", "PATCH /v1/nodes/:id", "DELETE /v1/nodes/:id",
		"POST /v1/nodes/:id/copy", "POST /v1/nodes/:id/move", "POST /v1/nodes/:id/activate",
		"POST /v1/nodes/:id/retire", "GET /v1/node-protocol-schemas",
		"GET /v1/servers", "POST /v1/servers", "GET /v1/servers/:id", "PATCH /v1/servers/:id",
		"POST /v1/servers/:id/status", "DELETE /v1/servers/:id", "GET /v1/servers/:id/nodes",
	} {
		if !have[want] {
			t.Fatalf("缺路由 %s（已注册 %v）", want, have)
		}
	}
}

// status:batch 用 gin 的转义冒号注册：请求打到批量接口（422 items），不会被当成 /nodes/:id。
func TestNodeStatusBatchLiteralColon(t *testing.T) {
	e := newInventoryEngine(false)
	w := doInventory(e, http.MethodPost, "/v1/nodes/status:batch", `{"items":[],"serving_status":"active"}`)
	inventoryExpectErr(t, w, errs.ValidationFailed, "items")

	w = doInventory(e, http.MethodPost, "/v1/nodes/status:batch",
		`{"items":[{"id":"not-a-uuid","row_version":1}],"serving_status":"active"}`)
	inventoryExpectErr(t, w, errs.ValidationFailed, "items")

	ok := `{"items":[{"id":"` + uuid.NewString() + `","row_version":1}],"serving_status":"bogus"}`
	w = doInventory(e, http.MethodPost, "/v1/nodes/status:batch", ok)
	inventoryExpectErr(t, w, errs.ValidationFailed, "serving_status")
}

func TestInventoryPathIDNotFound(t *testing.T) {
	e := newInventoryEngine(false)
	for _, c := range []struct{ method, path, body string }{
		{http.MethodPatch, "/v1/nodes/nope", `{"row_version":1}`},
		{http.MethodPost, "/v1/nodes/nope/copy", `{"row_version":1}`},
		{http.MethodPost, "/v1/nodes/nope/activate", `{"row_version":1}`},
		{http.MethodGet, "/v1/servers/nope", ""},
		{http.MethodGet, "/v1/servers/nope/nodes", ""},
		{http.MethodPost, "/v1/servers/nope/status", `{"status":"ready","row_version":1}`},
	} {
		w := doInventory(e, c.method, c.path, c.body)
		if w.Code != http.StatusNotFound {
			t.Fatalf("%s %s：状态码 %d，期望 404：%s", c.method, c.path, w.Code, w.Body)
		}
	}
}

func TestInventoryUnknownFieldRejected(t *testing.T) {
	e := newInventoryEngine(false)
	id := uuid.NewString()
	for _, c := range []struct{ method, path, body string }{
		{http.MethodPost, "/v1/nodes", `{"name":"a","bogus":1}`},
		{http.MethodPatch, "/v1/nodes/" + id, `{"row_version":1,"bogus":1}`},
		{http.MethodPost, "/v1/servers", `{"name":"a","bogus":1}`},
		{http.MethodPatch, "/v1/servers/" + id, `{"row_version":1,"bogus":1}`},
	} {
		w := doInventory(e, c.method, c.path, c.body)
		inventoryExpectErr(t, w, errs.BadRequest, "")
	}
}

// 前端会发的请求体（logic.ts、infra.ts、NodeOps.tsx）都能绑定，走到 service 的校验。
func TestInventoryFrontendBodiesBind(t *testing.T) {
	e := newInventoryEngine(false)
	id := uuid.NewString()
	// 建节点：名字为空，绑定通过后在 service 校验处回 422 name
	w := doInventory(e, http.MethodPost, "/v1/nodes", `{"name":"","server_id":"`+id+`","node_type":"shadowsocks",
		"server_host":"203.0.113.10","server_port":8388,"protocol_config":{},"traffic_rate":1,
		"pool_id":"`+id+`","display_name":"x","country_code":"HK"}`)
	inventoryExpectErr(t, w, errs.ValidationFailed, "name")
	// 改节点：版本号为 0，在 service 预检处回 422 row_version
	w = doInventory(e, http.MethodPatch, "/v1/nodes/"+id, `{"row_version":0,"name":"a","display_name":"",
		"pool_id":null,"node_type":"vless","server_host":"h.example","server_port":443,
		"traffic_rate":1.5,"country_code":null,"protocol_config":{}}`)
	inventoryExpectErr(t, w, errs.ValidationFailed, "row_version")
	// 建服务器：容量为负
	w = doInventory(e, http.MethodPost, "/v1/servers", `{"name":"s","capacity_nodes":-1,"region":"r","hostname":"h",
		"public_ipv4":"203.0.113.1","public_ipv6":"2001:db8::1","private_ipv4":"10.0.0.1","architecture":"amd64",
		"os_name":"linux","notes":"n"}`)
	inventoryExpectErr(t, w, errs.ValidationFailed, "capacity_nodes")
	// 改服务器：版本号为 0
	w = doInventory(e, http.MethodPatch, "/v1/servers/"+id, `{"row_version":0,"name":"s","capacity_nodes":3,"notes":""}`)
	inventoryExpectErr(t, w, errs.ValidationFailed, "row_version")
	// 改服务器状态：版本号为 0、状态不认识
	w = doInventory(e, http.MethodPost, "/v1/servers/"+id+"/status", `{"status":"bogus","row_version":0,"reason":"r"}`)
	inventoryExpectErr(t, w, errs.ValidationFailed, "status")
	// 删服务器、删节点、退役、上线：版本号为 0
	for _, c := range []struct{ method, path, body string }{
		{http.MethodDelete, "/v1/servers/" + id, `{"row_version":0}`},
		{http.MethodDelete, "/v1/nodes/" + id, `{"row_version":0}`},
		{http.MethodPost, "/v1/nodes/" + id + "/retire", `{"row_version":0}`},
		{http.MethodPost, "/v1/nodes/" + id + "/activate", `{"row_version":0}`},
		{http.MethodPost, "/v1/nodes/" + id + "/copy", `{"row_version":0,"name":"c","copy_routing":true,"server_port":9000}`},
		{http.MethodPost, "/v1/nodes/" + id + "/move", `{"server_id":"` + id + `","row_version":0}`},
	} {
		w := doInventory(e, c.method, c.path, c.body)
		inventoryExpectErr(t, w, errs.ValidationFailed, "row_version")
	}
	// 排序：空列表
	w = doInventory(e, http.MethodPut, "/v1/nodes/order", `{"items":[]}`)
	inventoryExpectErr(t, w, errs.ValidationFailed, "items")
}

func TestNodeRequestFieldValidation(t *testing.T) {
	e := newInventoryEngine(false)
	id := uuid.NewString()
	cases := []struct {
		name, method, path, body, field string
	}{
		{"建节点缺服务器", http.MethodPost, "/v1/nodes", `{"name":"a"}`, "server_id"},
		{"建节点服务器 id 坏", http.MethodPost, "/v1/nodes", `{"name":"a","server_id":"x"}`, "server_id"},
		{"建节点池 id 坏", http.MethodPost, "/v1/nodes", `{"name":"a","server_id":"` + id + `","pool_id":"x"}`, "pool_id"},
		{"改节点池 id 坏", http.MethodPatch, "/v1/nodes/" + id, `{"row_version":1,"pool_id":"x"}`, "pool_id"},
		{"改节点协议给 null", http.MethodPatch, "/v1/nodes/" + id, `{"row_version":1,"protocol_config":null}`, "protocol_config"},
		{"改节点协议给数组", http.MethodPatch, "/v1/nodes/" + id, `{"row_version":1,"protocol_config":[]}`, "protocol_config"},
		{"复制目标服务器 id 坏", http.MethodPost, "/v1/nodes/" + id + "/copy", `{"row_version":1,"target_server_id":"x"}`, "target_server_id"},
		{"复制端口越界", http.MethodPost, "/v1/nodes/" + id + "/copy", `{"row_version":1,"server_port":70000}`, "server_port"},
		{"迁移缺服务器", http.MethodPost, "/v1/nodes/" + id + "/move", `{"row_version":1}`, "server_id"},
		{"排序 id 坏", http.MethodPut, "/v1/nodes/order", `{"items":[{"id":"x","row_version":1,"sort_order":10}]}`, "items"},
		{"排序版本缺", http.MethodPut, "/v1/nodes/order", `{"items":[{"id":"` + id + `","sort_order":10}]}`, "items"},
		{"列表 id 坏", http.MethodGet, "/v1/nodes?id=x", "", "id"},
		{"列表 state 坏", http.MethodGet, "/v1/nodes?state=bogus", "", "state"},
		{"列表 limit 坏", http.MethodGet, "/v1/nodes?limit=0", "", "limit"},
		{"服务器列表状态坏", http.MethodGet, "/v1/servers?status=unhealthy", "", "status"},
		{"建服务器名字空", http.MethodPost, "/v1/servers", `{"name":" "}`, "name"},
		{"建服务器 IP 族不对", http.MethodPost, "/v1/servers", `{"name":"s","public_ipv4":"2001:db8::1"}`, "public_ipv4"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := doInventory(e, c.method, c.path, c.body)
			inventoryExpectErr(t, w, errs.ValidationFailed, c.field)
		})
	}
}

func TestNodePublishValidation(t *testing.T) {
	e := newInventoryEngine(false)
	id := uuid.NewString()
	cases := []struct{ body, field string }{
		{`{"scope":"global","scope_ref":"` + id + `","payload":{}}`, "scope"},
		{`{"scope":"node","scope_ref":"","payload":{}}`, "scope_ref"},
		{`{"scope":"node","scope_ref":"x","payload":{}}`, "scope_ref"},
		{`{"scope":"node","scope_ref":"` + id + `"}`, "payload"},
		{`{"scope":"node","scope_ref":"` + id + `","payload":null}`, "payload"},
		{`{"scope":"node","scope_ref":"` + id + `","payload":[]}`, "payload"},
		{`{"scope":"node","scope_ref":"` + id + `","payload":{"a":1}}`, "payload"},
	}
	for _, c := range cases {
		w := doInventory(e, http.MethodPost, "/v1/nodes/config/publish", c.body)
		inventoryExpectErr(t, w, errs.ValidationFailed, c.field)
	}
}

func TestInventoryReauthRoutes(t *testing.T) {
	e := newInventoryEngine(true)
	id := uuid.NewString()
	for _, c := range []struct{ method, path, body string }{
		{http.MethodPost, "/v1/nodes/config/publish", `{"scope":"node","scope_ref":"` + id + `","payload":{}}`},
		{http.MethodDelete, "/v1/nodes/" + id, `{"row_version":1}`},
		{http.MethodPost, "/v1/nodes/" + id + "/retire", `{"row_version":1}`},
		{http.MethodDelete, "/v1/servers/" + id, `{"row_version":1}`},
	} {
		w := doInventory(e, c.method, c.path, c.body)
		inventoryExpectErr(t, w, errs.ReauthRequired, "")
	}
	// 不要求重认证的写：重认证拒绝也照常走到校验
	w := doInventory(e, http.MethodPost, "/v1/nodes/"+id+"/activate", `{"row_version":0}`)
	inventoryExpectErr(t, w, errs.ValidationFailed, "row_version")
}

func TestNodeRealityKeypairAndSchemas(t *testing.T) {
	e := newInventoryEngine(false)
	w := doInventory(e, http.MethodPost, "/v1/nodes/reality-keypair", "")
	if w.Code != http.StatusOK {
		t.Fatalf("状态码 %d：%s", w.Code, w.Body)
	}
	var kp nodeRealityKeypairResponse
	if err := json.Unmarshal(w.Body.Bytes(), &kp); err != nil {
		t.Fatal(err)
	}
	if kp.PrivateKey == "" || kp.PublicKey == "" || kp.PrivateKey == kp.PublicKey || len(kp.ShortID) != 8 || kp.Hint == "" {
		t.Fatalf("密钥对不对：%+v", kp)
	}

	w = doInventory(e, http.MethodGet, "/v1/node-protocol-schemas", "")
	if w.Code != http.StatusOK {
		t.Fatalf("状态码 %d：%s", w.Code, w.Body)
	}
	var body struct {
		Schemas []map[string]any `json:"schemas"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Schemas) == 0 {
		t.Fatal("schemas 为空")
	}
	for _, s := range body.Schemas {
		if _, ok := s["required"].([]any); !ok {
			t.Fatalf("required 不是数组：%v", s)
		}
		if _, ok := s["allowed_properties"].([]any); !ok {
			t.Fatalf("allowed_properties 不是数组：%v", s)
		}
	}
}

func TestNodeRowResponseShape(t *testing.T) {
	keys, m := inventoryKeys(t, nodeRowFrom(service.NodeRow{ServingStatus: "active"}))
	inventoryExpectKeys(t, keys, inventoryNodeRowKeys)
	if gp, ok := m["granted_plans"].([]any); !ok || len(gp) != 0 {
		t.Fatalf("granted_plans 应为空数组：%v", m["granted_plans"])
	}
	for _, k := range []string{"health_score", "identity_serial", "cpu_percent", "metrics_at",
		"desired_effective_generation", "applied_effective_generation", "last_apply_failure"} {
		if m[k] != nil {
			t.Fatalf("%s 应为 null：%v", k, m[k])
		}
	}
	if m["status"] != "active" || m["online_users"] != float64(0) {
		t.Fatalf("status / online_users 不对：%v", m)
	}

	at := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	_, m = inventoryKeys(t, nodeRowFrom(service.NodeRow{LastApplyFailure: &service.ApplyFailure{Phase: "failed", Detail: "d", At: at}}))
	f, ok := m["last_apply_failure"].(map[string]any)
	if !ok || f["phase"] != "failed" || f["detail"] != "d" || f["generation"] != nil {
		t.Fatalf("last_apply_failure 不对：%v", m["last_apply_failure"])
	}
}

func TestNodeDetailResponseShape(t *testing.T) {
	row := service.NodeRow{Detail: &service.NodeDetail{
		TrafficRate: decimal.RequireFromString("1.50"), ProtocolConfig: json.RawMessage(`{"cipher":"aes-256-gcm"}`),
	}}
	keys, m := inventoryKeys(t, nodeDetailFrom(row))
	inventoryExpectKeys(t, keys, append(append([]string{}, inventoryNodeRowKeys...), inventoryNodeDetailExtra...))
	if m["traffic_rate"] != 1.5 {
		t.Fatalf("traffic_rate = %v", m["traffic_rate"])
	}
	row.Detail.Warnings = []string{"w"}
	keys, _ = inventoryKeys(t, nodeDetailFrom(row))
	inventoryExpectKeys(t, keys, append(append(append([]string{}, inventoryNodeRowKeys...), inventoryNodeDetailExtra...), "warnings"))
}

func TestAdminNodeResponseShape(t *testing.T) {
	keys, m := inventoryKeys(t, adminNodeFrom(service.AdminNode{ServingStatus: "draft", TrafficRate: decimal.NewFromInt(1)}))
	inventoryExpectKeys(t, keys, inventoryAdminNodeKeys)
	if _, ok := m["protocol_config"].(map[string]any); !ok {
		t.Fatalf("protocol_config 应为对象：%v", m["protocol_config"])
	}
	keys, _ = inventoryKeys(t, adminNodeFrom(service.AdminNode{Warnings: []string{"w"}}))
	inventoryExpectKeys(t, keys, append(append([]string{}, inventoryAdminNodeKeys...), "warnings"))
}

func TestServerResponseShape(t *testing.T) {
	keys, m := inventoryKeys(t, serverFrom(service.Server{Status: "draft"}))
	inventoryExpectKeys(t, keys, inventoryServerKeys)
	for _, k := range []string{"control_node_id", "cpu_bp", "mem_used_mb", "mem_total_mb", "disk_used_gb", "disk_total_gb", "metrics_at"} {
		if m[k] != nil {
			t.Fatalf("%s 应为 null：%v", k, m[k])
		}
	}
	_, m = inventoryKeys(t, serversFrom(nil))
	if s, ok := m["servers"].([]any); !ok || len(s) != 0 || m["total"] != float64(0) {
		t.Fatalf("空列表应为 []：%v", m)
	}

	keys, _ = inventoryKeys(t, serverNodeFrom(service.ServerNode{ServingStatus: "retired"}))
	inventoryExpectKeys(t, keys, inventoryServerNodeKeys)
	_, m = inventoryKeys(t, serverNodesFrom(nil))
	if s, ok := m["nodes"].([]any); !ok || len(s) != 0 {
		t.Fatalf("空列表应为 []：%v", m)
	}
}

func TestNodeOptStringTriState(t *testing.T) {
	var req nodePatchRequest
	if err := json.Unmarshal([]byte(`{"row_version":1}`), &req); err != nil {
		t.Fatal(err)
	}
	if req.PoolID.Set || req.CountryCode.Set {
		t.Fatal("没给的字段不应 Set")
	}
	if err := json.Unmarshal([]byte(`{"pool_id":null,"country_code":"HK"}`), &req); err != nil {
		t.Fatal(err)
	}
	if !req.PoolID.Set || req.PoolID.Value != nil {
		t.Fatalf("null 应为 Set 且 Value=nil：%+v", req.PoolID)
	}
	if !req.CountryCode.Set || req.CountryCode.Value == nil || *req.CountryCode.Value != "HK" {
		t.Fatalf("字符串应为 Set 且有值：%+v", req.CountryCode)
	}
}
