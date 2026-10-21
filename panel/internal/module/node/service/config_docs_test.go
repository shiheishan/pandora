package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/aegispanel/aegis/internal/module/node/protocol"
	"github.com/aegispanel/aegis/internal/module/node/repo"
	"github.com/google/uuid"
)

func TestBuildNodeConfigDocDeterministic(t *testing.T) {
	id := uuid.MustParse("018f0000-0000-7000-8000-000000000099")
	proto := `{"dest":"www.apple.com:443","network":"tcp","security":"reality"}`
	layers := []routingLayer{{source: RoutingSource{Scope: routingScopeGlobal}}}
	d1, err := buildNodeConfigDoc(id, "vless", 443, "node.example.com", proto, layers, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	d2, err := buildNodeConfigDoc(id, "vless", 443, "node.example.com", proto, layers, nil, nil)
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
	d1, err := buildNodeConfigDoc(id, "vless", 443, "node.example.com", a, layers, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	d2, err := buildNodeConfigDoc(id, "vless", 443, "node.example.com", b, layers, nil, nil)
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
	doc, err := buildNodeConfigDoc(id, "vless", 443, "node.example.com", `{}`, layers, nil, nil)
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
	doc, err := buildNodeConfigDoc(id, "vless", 443, "node.example.com", `{}`, layers, nil, nil)
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
	doc, err := buildNodeConfigDoc(id, "shadowtls", inbound, "node.example.com", proto, nil, nil, nil)
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
	doc, err := buildNodeConfigDoc(id, "vless", 443, "node.example.com", `{"network":"tcp"}`, nil, nil, nil)
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
	_, err := buildNodeConfigDoc(uuid.New(), "vless", 443, "node.example.com", `[]`, nil, nil, nil)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestBuildNodeConfigDocProtocolInDigest(t *testing.T) {
	id := uuid.New()
	// 同一份配置换协议，摘要必须不同；协议配置里夹带的 protocol 不能顶替
	a, err := buildNodeConfigDoc(id, "vless", 443, "node.example.com", `{"protocol":"trojan"}`, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	b, err := buildNodeConfigDoc(id, "trojan", 443, "node.example.com", `{"protocol":"trojan"}`, nil, nil, nil)
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

func TestBuildNodeConfigDocECH(t *testing.T) {
	id := uuid.New()
	trojanTLS := `{"tls":1,"cert_path":"/etc/x/fullchain.pem","key_path":"/etc/x/privkey.pem","server_name":"node.example.com"}`
	vlessReality := `{"tls":2,"dest":"www.apple.com:443","server_name":"www.apple.com","private_key":"x","short_ids":["ab"],"public_key":"y"}`
	ss := `{"cipher":"aes-128-gcm"}`
	withReserved := `{"tls":1,"cert_path":"/etc/x/fullchain.pem","key_path":"/etc/x/privkey.pem","server_name":"node.example.com","ech_keys":"x"}`

	key, err := protocol.GenerateECHKey("ech.example.com")
	if err != nil {
		t.Fatal(err)
	}
	ech := []echDocKey{{Config: key.Config, PrivateKey: key.PrivateKey, SendAsRetry: true}}

	doc, err := buildNodeConfigDoc(id, "trojan", 443, "node.example.com", trojanTLS, nil, ech, nil)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(doc.JSON, &m); err != nil {
		t.Fatal(err)
	}
	keys, ok := m["ech_keys"].([]any)
	if !ok || len(keys) != 1 {
		t.Fatalf("ech_keys %v", m["ech_keys"])
	}
	item, ok := keys[0].(map[string]any)
	if !ok {
		t.Fatalf("ech item %T", keys[0])
	}
	for _, k := range []string{"config", "private_key", "send_as_retry"} {
		if _, ok := item[k]; !ok {
			t.Fatalf("missing %s in ech_keys[0]", k)
		}
	}
	if item["send_as_retry"] != true {
		t.Fatalf("send_as_retry %v", item["send_as_retry"])
	}

	for _, tc := range []struct {
		name string
		typ  string
		cfg  string
	}{
		{"vless reality", "vless", vlessReality},
		{"shadowsocks", "shadowsocks", ss},
	} {
		doc, err := buildNodeConfigDoc(id, tc.typ, 443, "node.example.com", tc.cfg, nil, ech, nil)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal(doc.JSON, &m); err != nil {
			t.Fatal(err)
		}
		if _, ok := m["ech_keys"]; ok {
			t.Fatalf("%s should not have ech_keys", tc.name)
		}
	}

	doc, err = buildNodeConfigDoc(id, "trojan", 443, "node.example.com", withReserved, nil, ech, nil)
	if err != nil {
		t.Fatal(err)
	}
	m = nil // 不清空的话 Unmarshal 会把上一份文档的键合并进来
	if err := json.Unmarshal(doc.JSON, &m); err != nil {
		t.Fatal(err)
	}
	if _, ok := m["ech_keys"].([]any); !ok {
		t.Fatalf("ech_keys 应是服务层写的数组，实际 %v", m["ech_keys"])
	}
	// 协议配置里夹带的 ech_keys 字符串键应被删掉，文档里只有数组形态的 ech_keys
	if len(m) > 0 {
		for k, v := range m {
			if k == "ech_keys" && v == "x" {
				t.Fatal("protocol ech_keys leaked")
			}
		}
	}
}

func TestBuildNodeConfigDocACMEIdentifier(t *testing.T) {
	id := uuid.New()

	// 1. hysteria2 {"cert_mode":"acme"}，host "Node1.Example.com"：文档 JSON 里 cert_mode=="acme"、cert_identifier=="node1.example.com"，没有 cert_path
	{
		doc, err := buildNodeConfigDoc(id, "hysteria2", 443, "Node1.Example.com", `{"cert_mode":"acme"}`, nil, nil, nil)
		if err != nil {
			t.Fatalf("case 1 err: %v", err)
		}
		var m map[string]any
		if err := json.Unmarshal(doc.JSON, &m); err != nil {
			t.Fatal(err)
		}
		if m["cert_mode"] != "acme" {
			t.Fatalf("case 1 cert_mode %v", m["cert_mode"])
		}
		if m["cert_identifier"] != "node1.example.com" {
			t.Fatalf("case 1 cert_identifier %v", m["cert_identifier"])
		}
		if _, ok := m["cert_path"]; ok {
			t.Fatalf("case 1 should not have cert_path: %v", m["cert_path"])
		}
	}

	// 2. 同上，但带 {"cert_mode":"acme","tls_settings":{"server_name":"sni.example.com"}}：cert_identifier=="sni.example.com"
	{
		doc, err := buildNodeConfigDoc(id, "hysteria2", 443, "Node1.Example.com", `{"cert_mode":"acme","tls_settings":{"server_name":"sni.example.com"}}`, nil, nil, nil)
		if err != nil {
			t.Fatalf("case 2 err: %v", err)
		}
		var m map[string]any
		if err := json.Unmarshal(doc.JSON, &m); err != nil {
			t.Fatal(err)
		}
		if m["cert_mode"] != "acme" {
			t.Fatalf("case 2 cert_mode %v", m["cert_mode"])
		}
		if m["cert_identifier"] != "sni.example.com" {
			t.Fatalf("case 2 cert_identifier %v", m["cert_identifier"])
		}
	}

	// 3. trojan {"tls":1,"cert_mode":"acme"}，host "192.0.2.10"：cert_identifier=="192.0.2.10"
	{
		doc, err := buildNodeConfigDoc(id, "trojan", 443, "192.0.2.10", `{"tls":1,"cert_mode":"acme"}`, nil, nil, nil)
		if err != nil {
			t.Fatalf("case 3 err: %v", err)
		}
		var m map[string]any
		if err := json.Unmarshal(doc.JSON, &m); err != nil {
			t.Fatal(err)
		}
		if m["cert_mode"] != "acme" {
			t.Fatalf("case 3 cert_mode %v", m["cert_mode"])
		}
		if m["cert_identifier"] != "192.0.2.10" {
			t.Fatalf("case 3 cert_identifier %v", m["cert_identifier"])
		}
	}

	// 4. hysteria2 acme，host ""：返回错误
	{
		_, err := buildNodeConfigDoc(id, "hysteria2", 443, "", `{"cert_mode":"acme"}`, nil, nil, nil)
		if err == nil {
			t.Fatal("case 4 should fail")
		}
	}

	// 5. hysteria2 acme，host "10.0.0.1"：返回错误
	{
		_, err := buildNodeConfigDoc(id, "hysteria2", 443, "10.0.0.1", `{"cert_mode":"acme"}`, nil, nil, nil)
		if err == nil {
			t.Fatal("case 5 should fail")
		}
	}

	// 6. hysteria2 {"cert_mode":"file","cert_path":"/etc/pandora-native/certs/a/fullchain.pem","key_path":"/etc/pandora-native/certs/a/privkey.pem"}：没有 cert_identifier
	{
		proto := `{"cert_mode":"file","cert_path":"/etc/pandora-native/certs/a/fullchain.pem","key_path":"/etc/pandora-native/certs/a/privkey.pem"}`
		doc, err := buildNodeConfigDoc(id, "hysteria2", 443, "node.example.com", proto, nil, nil, nil)
		if err != nil {
			t.Fatalf("case 6 err: %v", err)
		}
		var m map[string]any
		if err := json.Unmarshal(doc.JSON, &m); err != nil {
			t.Fatal(err)
		}
		if _, ok := m["cert_identifier"]; ok {
			t.Fatalf("case 6 should not have cert_identifier: %v", m["cert_identifier"])
		}
	}

	// 7. 协议配置夹带 {"cert_identifier":"evil.example.com","cert_mode":"file",…路径}：文档里没有 cert_identifier
	{
		proto := `{"cert_identifier":"evil.example.com","cert_mode":"file","cert_path":"/etc/pandora-native/certs/a/fullchain.pem","key_path":"/etc/pandora-native/certs/a/privkey.pem"}`
		doc, err := buildNodeConfigDoc(id, "hysteria2", 443, "node.example.com", proto, nil, nil, nil)
		if err != nil {
			t.Fatalf("case 7 err: %v", err)
		}
		var m map[string]any
		if err := json.Unmarshal(doc.JSON, &m); err != nil {
			t.Fatal(err)
		}
		if _, ok := m["cert_identifier"]; ok {
			t.Fatalf("case 7 should not have cert_identifier: %v", m["cert_identifier"])
		}
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

	rows := []repo.ConfigNodeRow{
		{ServerID: sA, NodeID: &n1, NodeType: &nodeType, ServerPort: &port, ProtocolConfig: &goodProto},
		{ServerID: sA, NodeID: &n2, NodeType: &nodeType, ServerPort: &port, ProtocolConfig: &badProto},
		{ServerID: sB, NodeID: &n3, NodeType: &nodeType, ServerPort: &port, ProtocolConfig: &goodProto},
		{ServerID: sC, NodeID: nil},
	}

	out, skips := assembleServerConfigs(rows, map[uuid.UUID][]routingLayer{}, map[uuid.UUID][]echDocKey{}, map[uuid.UUID]string{}, nil)

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
