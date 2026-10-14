package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/aegispanel/aegis/internal/module/node/repo"
	"github.com/google/uuid"
)

func TestBuildNodeConfigDocDeterministic(t *testing.T) {
	id := uuid.MustParse("018f0000-0000-7000-8000-000000000099")
	proto := `{"dest":"www.apple.com:443","network":"tcp","security":"reality"}`
	layers := []routingLayer{{source: RoutingSource{Scope: routingScopeGlobal}}}
	d1, err := buildNodeConfigDoc(id, "vless", 443, proto, layers)
	if err != nil {
		t.Fatal(err)
	}
	d2, err := buildNodeConfigDoc(id, "vless", 443, proto, layers)
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
	d1, err := buildNodeConfigDoc(id, "vless", 443, a, layers)
	if err != nil {
		t.Fatal(err)
	}
	d2, err := buildNodeConfigDoc(id, "vless", 443, b, layers)
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
	doc, err := buildNodeConfigDoc(id, "vless", 443, `{}`, layers)
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
	doc, err := buildNodeConfigDoc(id, "vless", 443, `{}`, layers)
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
	// 协议配置里夹带的保留键（outbounds）不能进文档
	proto := `{"server":"example.com","server_port":8443,"password":"x","version":3,"outbounds":[{"tag":"x","type":"block"}]}`
	doc, err := buildNodeConfigDoc(id, "shadowtls", inbound, proto, nil)
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
	for _, k := range []string{"outbounds", "kernel"} {
		if _, ok := m[k]; ok {
			t.Fatalf("unexpected %s", k)
		}
	}
}

func TestBuildNodeConfigDocNoAdminFields(t *testing.T) {
	id := uuid.New()
	doc, err := buildNodeConfigDoc(id, "vless", 443, `{"network":"tcp"}`, nil)
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
	_, err := buildNodeConfigDoc(uuid.New(), "vless", 443, `[]`, nil)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestBuildNodeConfigDocProtocolInDigest(t *testing.T) {
	id := uuid.New()
	// 同一份配置换协议，摘要必须不同；协议配置里夹带的 protocol 不能顶替
	a, err := buildNodeConfigDoc(id, "vless", 443, `{"protocol":"trojan"}`, nil)
	if err != nil {
		t.Fatal(err)
	}
	b, err := buildNodeConfigDoc(id, "trojan", 443, `{"protocol":"trojan"}`, nil)
	if err != nil {
		t.Fatal(err)
	}
	if a.SHA256 == b.SHA256 {
		t.Fatal("换协议后摘要没变")
	}
	var m map[string]any
	if err := json.Unmarshal(a.JSON, &m); err != nil {
		t.Fatal(err)
	}
	if m["protocol"] != "vless" {
		t.Fatalf("protocol %v", m["protocol"])
	}
}

func TestAssembleServerConfigsSkipsBadNode(t *testing.T) {
	sA := uuid.New()
	sB := uuid.New()
	sC := uuid.New()
	n1 := uuid.New()
	n2 := uuid.New()
	n3 := uuid.New()

	goodProto := "{}"
	badProto := "[1]"
	nodeType := "vless"
	port := 443
	kernel := "auto"

	rows := []repo.ConfigNodeRow{
		{ServerID: sA, NodeID: &n1, NodeType: &nodeType, ServerPort: &port, Kernel: &kernel, ProtocolConfig: &goodProto},
		{ServerID: sA, NodeID: &n2, NodeType: &nodeType, ServerPort: &port, Kernel: &kernel, ProtocolConfig: &badProto},
		{ServerID: sB, NodeID: &n3, NodeType: &nodeType, ServerPort: &port, Kernel: &kernel, ProtocolConfig: &goodProto},
		{ServerID: sC, NodeID: nil},
	}

	out, skips := assembleServerConfigs(rows, map[uuid.UUID][]routingLayer{})

	if len(out[sA]) != 1 || out[sA][0].NodeID != n1 {
		t.Fatalf("sA docs: %+v, want only n1", out[sA])
	}
	if len(out[sB]) != 1 || out[sB][0].NodeID != n3 {
		t.Fatalf("sB docs: %+v, want only n3", out[sB])
	}
	if out[sC] == nil {
		t.Fatal("sC key missing")
	}
	if len(out[sC]) != 0 {
		t.Fatalf("sC should be empty slice, got %v", out[sC])
	}

	if len(skips) != 1 {
		t.Fatalf("跳过集合应只有 n2，got %+v", skips)
	}
	sk, ok := skips[n2]
	if !ok || sk.ServerID != sA || sk.Reason == "" {
		t.Fatalf("n2 的跳过记录不符: %+v ok=%v", sk, ok)
	}
}
