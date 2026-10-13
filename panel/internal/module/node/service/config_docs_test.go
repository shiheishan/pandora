package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
)

func TestBuildNodeConfigDocDeterministic(t *testing.T) {
	id := uuid.MustParse("018f0000-0000-7000-8000-000000000099")
	proto := `{"dest":"www.apple.com:443","network":"tcp","security":"reality"}`
	layers := []routingLayer{{source: RoutingSource{Scope: routingScopeGlobal}}}
	d1, err := buildNodeConfigDoc(id, "vless", 443, "auto", proto, layers)
	if err != nil {
		t.Fatal(err)
	}
	d2, err := buildNodeConfigDoc(id, "vless", 443, "auto", proto, layers)
	if err != nil {
		t.Fatal(err)
	}
	if d1.JSON == nil || d2.JSON == nil || string(d1.JSON) != string(d2.JSON) || d1.SHA256 != d2.SHA256 {
		t.Fatal("not deterministic")
	}
	h := sha256.Sum256(d1.JSON)
	if d1.SHA256 != hex.EncodeToString(h[:]) {
		t.Fatalf("sha mismatch %s vs %s", d1.SHA256, hex.EncodeToString(h[:]))
	}
}

func TestBuildNodeConfigDocKeyOrderInvariantDigest(t *testing.T) {
	id := uuid.MustParse("018f0000-0000-7000-8000-0000000000aa")
	layers := []routingLayer{{source: RoutingSource{Scope: routingScopeGlobal}}}
	a := `{"dest":"www.apple.com:443","network":"tcp","security":"reality","short_ids":["0a1b2c3d"]}`
	b := `{"security":"reality","network":"tcp","dest":"www.apple.com:443","short_ids":["0a1b2c3d"]}`
	d1, err := buildNodeConfigDoc(id, "vless", 443, "", a, layers)
	if err != nil {
		t.Fatal(err)
	}
	d2, err := buildNodeConfigDoc(id, "vless", 443, "", b, layers)
	if err != nil {
		t.Fatal(err)
	}
	if d1.SHA256 != d2.SHA256 {
		t.Fatalf("digest differ %s %s", d1.SHA256, d2.SHA256)
	}
}

func TestBuildNodeConfigDocNoRoutingKeys(t *testing.T) {
	id := uuid.New()
	layers := []routingLayer{{
		source: RoutingSource{Scope: routingScopeGlobal},
		routes: []RoutingRule{{Matcher: json.RawMessage(`{"domain":["x"]}`), OutboundTag: "direct", Enabled: false}},
	}}
	doc, err := buildNodeConfigDoc(id, "vless", 443, "auto", `{}`, layers)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(doc.JSON, &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"outbounds", "routes", "final"} {
		if _, ok := m[k]; ok {
			t.Fatalf("unexpected key %s", k)
		}
	}
}

func TestBuildNodeConfigDocWithRoutingKeys(t *testing.T) {
	id := uuid.New()
	layers := []routingLayer{{
		source: RoutingSource{Scope: routingScopeGlobal},
		routes: []RoutingRule{{Matcher: json.RawMessage(`{"domain":["x.com"]}`), OutboundTag: "direct", Enabled: true}},
	}}
	doc, err := buildNodeConfigDoc(id, "vless", 443, "auto", `{}`, layers)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(doc.JSON, &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"outbounds", "routes", "final"} {
		if _, ok := m[k]; !ok {
			t.Fatalf("missing %s", k)
		}
	}
	if m["final"] != "direct" {
		t.Fatalf("final %v", m["final"])
	}
}

func TestBuildNodeConfigDocShadowtlsServerPort(t *testing.T) {
	id := uuid.New()
	inbound := 2080
	// 协议配置里夹带的保留键（outbounds、kernel_type、kernel）不能进文档
	proto := `{"server":"example.com","server_port":8443,"password":"x","version":3,"outbounds":[{"tag":"x","type":"block"}],"kernel_type":"xray","kernel":"sing-box"}`
	doc, err := buildNodeConfigDoc(id, "shadowtls", inbound, "auto", proto, nil)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(doc.JSON, &m); err != nil {
		t.Fatal(err)
	}
	if m["server_port"] != float64(inbound) {
		t.Fatalf("server_port %v", m["server_port"])
	}
	if m["server"] != "example.com:8443" {
		t.Fatalf("server %v", m["server"])
	}
	for _, k := range []string{"outbounds", "kernel_type"} {
		if _, ok := m[k]; ok {
			t.Fatalf("unexpected %s", k)
		}
	}
	if m["kernel"] != "auto" {
		t.Fatalf("kernel %v", m["kernel"])
	}
}

func TestBuildNodeConfigDocNoAdminFields(t *testing.T) {
	id := uuid.New()
	doc, err := buildNodeConfigDoc(id, "vless", 443, "auto", `{"network":"tcp"}`, nil)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(doc.JSON, &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"name", "id", "traffic_rate"} {
		if _, ok := m[k]; ok {
			t.Fatalf("leaked %s", k)
		}
	}
}

func TestBuildNodeConfigDocProtocolNotObject(t *testing.T) {
	_, err := buildNodeConfigDoc(uuid.New(), "vless", 443, "auto", `[]`, nil)
	if err == nil {
		t.Fatal("expected error")
	}
}
