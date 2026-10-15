package handler

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/module/node/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
)

func TestXBRoutesRegistered(t *testing.T) {
	e := newInventoryEngine(false)
	have := map[string]bool{}
	for _, r := range e.Routes() {
		have[r.Method+" "+r.Path] = true
	}
	for _, want := range []string{
		"GET /v1/nodes/:id/relays",
		"POST /v1/nodes/:id/relays",
		"PATCH /v1/node-relays/:id",
		"DELETE /v1/node-relays/:id",
		"POST /v1/nodes/:id/traffic/reset",
		`POST /v1/nodes/edit\:batch`, // gin 的路由表里冒号转义成 \:（同 status\:batch）
		"GET /v1/nodes/:id/ech",
		"POST /v1/nodes/:id/ech",
		"DELETE /v1/nodes/:id/ech",
	} {
		if !have[want] {
			t.Fatalf("缺路由 %s（已注册 %v）", want, have)
		}
	}
}

func TestNodeEditBatchLiteralColon(t *testing.T) {
	e := newInventoryEngine(false)
	w := doInventory(e, http.MethodPost, "/v1/nodes/edit:batch", `{"items":[],"set":{"traffic_rate":1}}`)
	inventoryExpectErr(t, w, errs.ValidationFailed, "items")

	w = doInventory(e, http.MethodPost, "/v1/nodes/edit:batch",
		`{"items":[{"id":"not-a-uuid","row_version":1}],"set":{"traffic_rate":1}}`)
	inventoryExpectErr(t, w, errs.ValidationFailed, "items")

	w = doInventory(e, http.MethodPost, "/v1/nodes/edit:batch",
		`{"items":[{"id":"`+uuid.NewString()+`","row_version":1}],"set":{}}`)
	inventoryExpectErr(t, w, errs.ValidationFailed, "set")
}

func TestXBReauthRoutes(t *testing.T) {
	e := newInventoryEngine(true)
	id := uuid.NewString()
	for _, c := range []struct{ method, path, body string }{
		{http.MethodPost, "/v1/nodes/" + id + "/traffic/reset", `{"reason":"r"}`},
		{http.MethodPost, "/v1/nodes/edit:batch", `{"items":[{"id":"` + id + `","row_version":1}],"set":{"traffic_rate":1}}`},
		{http.MethodPost, "/v1/nodes/" + id + "/ech", `{"public_name":""}`},
		{http.MethodDelete, "/v1/nodes/" + id + "/ech", ""},
	} {
		w := doInventory(e, c.method, c.path, c.body)
		inventoryExpectErr(t, w, errs.ReauthRequired, "")
	}
}

func TestXBUnknownFieldRejected(t *testing.T) {
	e := newInventoryEngine(false)
	id := uuid.NewString()
	w := doInventory(e, http.MethodPost, "/v1/nodes/"+id+"/relays", `{"name":"r","server_host":"203.0.113.1","server_port":443,"bogus":1}`)
	inventoryExpectErr(t, w, errs.BadRequest, "")
}

func TestNodeOptInt64TriState(t *testing.T) {
	var req nodePatchRequest
	if err := json.Unmarshal([]byte(`{"row_version":1}`), &req); err != nil {
		t.Fatal(err)
	}
	if req.TrafficLimitBytes.Set {
		t.Fatal("没给的字段不应 Set")
	}
	if err := json.Unmarshal([]byte(`{"traffic_limit_bytes":null}`), &req); err != nil {
		t.Fatal(err)
	}
	if !req.TrafficLimitBytes.Set || req.TrafficLimitBytes.Value != nil {
		t.Fatalf("null 应为 Set 且 Value=nil：%+v", req.TrafficLimitBytes)
	}
	if err := json.Unmarshal([]byte(`{"traffic_limit_bytes":1024}`), &req); err != nil {
		t.Fatal(err)
	}
	if !req.TrafficLimitBytes.Set || req.TrafficLimitBytes.Value == nil || *req.TrafficLimitBytes.Value != 1024 {
		t.Fatalf("数字应为 Set 且有值：%+v", req.TrafficLimitBytes)
	}
}

func TestNodeOptIntTriState(t *testing.T) {
	var req nodePatchRequest
	if err := json.Unmarshal([]byte(`{"row_version":1}`), &req); err != nil {
		t.Fatal(err)
	}
	if req.TrafficResetDay.Set {
		t.Fatal("没给的字段不应 Set")
	}
	if err := json.Unmarshal([]byte(`{"traffic_reset_day":null}`), &req); err != nil {
		t.Fatal(err)
	}
	if !req.TrafficResetDay.Set || req.TrafficResetDay.Value != nil {
		t.Fatalf("null 应为 Set 且 Value=nil：%+v", req.TrafficResetDay)
	}
	if err := json.Unmarshal([]byte(`{"traffic_reset_day":15}`), &req); err != nil {
		t.Fatal(err)
	}
	if !req.TrafficResetDay.Set || req.TrafficResetDay.Value == nil || *req.TrafficResetDay.Value != 15 {
		t.Fatalf("数字应为 Set 且有值：%+v", req.TrafficResetDay)
	}
}

func TestRelayResponseShape(t *testing.T) {
	keys, m := inventoryKeys(t, relayFrom(service.NodeRelay{}))
	want := []string{
		"id", "parent_node_id", "name", "server_host", "server_port", "sni", "host_header",
		"enabled", "sort_order", "row_version", "created_at", "updated_at",
	}
	inventoryExpectKeys(t, keys, want)
	if m["sni"] != nil {
		t.Fatalf("sni 应为 null：%v", m["sni"])
	}
}

func TestECHResponseShape(t *testing.T) {
	keys, m := inventoryKeys(t, echFrom(service.NodeECH{}))
	want := []string{
		"supported", "unsupported_reason", "enabled", "public_name", "config_list", "config_pem",
		"created_at", "retired_keys",
	}
	inventoryExpectKeys(t, keys, want)
	if m["config_list"] != nil {
		t.Fatalf("config_list 应为 null：%v", m["config_list"])
	}
	if m["created_at"] != nil {
		t.Fatalf("created_at 应为 null：%v", m["created_at"])
	}
}

func TestNodeRowXBFieldsShape(t *testing.T) {
	keys, m := inventoryKeys(t, nodeRowFrom(service.NodeRow{}))
	for _, k := range []string{"tags", "traffic", "traffic_rate_schedule", "relay_count", "ech_enabled"} {
		found := false
		for _, key := range keys {
			if key == k {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("缺键 %s", k)
		}
	}
	tags, ok := m["tags"].([]any)
	if !ok || len(tags) != 0 {
		t.Fatalf("tags 应为空数组：%v", m["tags"])
	}
	sched, ok := m["traffic_rate_schedule"].([]any)
	if !ok || len(sched) != 0 {
		t.Fatalf("traffic_rate_schedule 应为空数组：%v", m["traffic_rate_schedule"])
	}
	traffic, ok := m["traffic"].(map[string]any)
	if !ok {
		t.Fatalf("traffic 应为对象：%v", m["traffic"])
	}
	if traffic["limit_bytes"] != nil {
		t.Fatalf("traffic.limit_bytes 应为 null：%v", traffic["limit_bytes"])
	}
}
