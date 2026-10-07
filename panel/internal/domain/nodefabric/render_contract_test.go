package nodefabric

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// 这一组钉住「订阅渲染能连上」依赖的面板侧行为：Host 下发、客户端 TLS 提示、
// AnyTLS 证书、REALITY 密钥成对、ShadowTLS 握手端口。

func buildConfig(t *testing.T, nodeType, host, protocol string) map[string]any {
	t.Helper()
	body, _, err := (&Service{}).BuildNodeConfig(&ServingNode{
		NodeType: nodeType, ServerHost: host, ServerPort: 10443, Kernel: "pandora-native",
		Protocol: json.RawMessage(protocol),
	})
	if err != nil {
		t.Fatalf("BuildNodeConfig: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	return got
}

// 后台在 network_settings.headers.Host 配了 Host（走 CDN）才下发它，否则保持
// server_host；server_name 始终是 server_host。以前 Host 永远到不了节点。
func TestBuildNodeConfigDeliversConfiguredHost(t *testing.T) {
	got := buildConfig(t, "vless", "node.example.com",
		`{"tls":0,"network":"ws","network_settings":{"path":"/ws","headers":{"Host":"cdn.example.com"}}}`)
	if got["host"] != "cdn.example.com" || got["server_name"] != "node.example.com" || got["path"] != "/ws" {
		t.Fatalf("configured Host not delivered: %#v", got)
	}
	got = buildConfig(t, "vless", "node.example.com", `{"tls":0,"network":"ws","network_settings":{"path":"/ws"}}`)
	if got["host"] != "node.example.com" {
		t.Fatalf("without a configured Host the node must keep server_host, got %#v", got["host"])
	}
	got = buildConfig(t, "vless", "node.example.com", `{"tls":0,"network":"ws","network_settings":{"headers":{"Host":"   "}}}`)
	if got["host"] != "node.example.com" {
		t.Fatalf("blank Host must not replace server_host, got %#v", got["host"])
	}
}

func TestKernelConfigIsTheSameTranslationAsDelivery(t *testing.T) {
	in := map[string]any{"tls": float64(2), "network": "grpc", "utls": "firefox",
		"network_settings": map[string]any{"serviceName": "svc"},
		"reality_settings": map[string]any{"server_name": "www.example.com", "public_key": "pk", "short_id": "ab"}}
	want := toKernelConfig("vless", in)
	if got := KernelConfig(" VLESS ", in); !reflect.DeepEqual(got, want) {
		t.Fatalf("KernelConfig differs from the delivery translation:\n got %#v\nwant %#v", got, want)
	}
	if want["security"] != "reality" || want["fingerprint"] != "firefox" || want["grpc_service_name"] != "svc" {
		t.Fatalf("unexpected kernel shape %#v", want)
	}
	if _, nested := in["security"]; nested {
		t.Fatal("KernelConfig mutated its input")
	}
}

func TestClientTLSHintsAreValidatedAndAccepted(t *testing.T) {
	cert := `"cert_path":"/etc/pandora-native/certs/example.com/c.pem","key_path":"/etc/pandora-native/certs/example.com/k.pem"`
	ok := map[string]string{
		"trojan":    `{"tls":1,"network":"tcp",` + cert + `,"tls_settings":{"server_name":"sni.example.com","allow_insecure":true}}`,
		"hysteria2": `{` + cert + `,"tls_settings":{"server_name":"203.0.113.5","allow_insecure":false}}`,
		"tuic":      `{` + cert + `,"tls_settings":{"server_name":"sni.example.com"}}`,
		"anytls":    `{"tls":true,` + cert + `,"tls_settings":{"allow_insecure":true}}`,
		"naive":     `{"tls":true,` + cert + `,"tls_settings":{"server_name":"sni.example.com"}}`,
	}
	for nodeType, raw := range ok {
		if version, fields := ValidateAdminProtocolConfig(nodeType, "pandora-native", 443, json.RawMessage(raw)); version != 1 || len(fields) != 0 {
			t.Errorf("%s: valid TLS hints rejected: %v", nodeType, fields)
		}
	}
	bad := []struct{ nodeType, raw, field string }{
		{"tuic", `{` + cert + `,"tls_settings":{"server_name":"bad name"}}`, "protocol_config.tls_settings.server_name"},
		{"hysteria2", `{` + cert + `,"tls_settings":{"allow_insecure":"yes"}}`, "protocol_config.tls_settings.allow_insecure"},
		{"trojan", `{"tls":1,` + cert + `,"tls_settings":{"server_name":42}}`, "protocol_config.tls_settings.server_name"},
		// vless 的普通 TLS 没开放，tls_settings 仍按未知字段拒绝
		{"vless", `{"tls":0,"tls_settings":{"server_name":"sni.example.com"}}`, "protocol_config"},
	}
	for _, c := range bad {
		_, fields := ValidateAdminProtocolConfig(c.nodeType, "pandora-native", 443, json.RawMessage(c.raw))
		found := false
		for key := range fields {
			if strings.HasPrefix(key, c.field) {
				found = true
			}
		}
		if !found {
			t.Errorf("%s %s: want error on %s, got %v", c.nodeType, c.raw, c.field, fields)
		}
	}
}

func TestAnyTLSRequiresCertificate(t *testing.T) {
	for _, raw := range []string{`{}`, `{"tls":true}`, `{"cert_path":"/etc/pandora-native/certs/example.com/c.pem"}`} {
		_, fields := ValidateAdminProtocolConfig("anytls", "pandora-native", 443, json.RawMessage(raw))
		if fields["protocol_config.cert_path"] == "" && fields["protocol_config.key_path"] == "" {
			t.Errorf("anytls %s without certificate accepted: %v", raw, fields)
		}
	}
	for _, schema := range ProtocolSchemas() {
		if schema.NodeType == "anytls" && (!containsString(schema.Required, "cert_path") || !containsString(schema.Required, "key_path")) {
			t.Fatalf("anytls schema must mark the certificate as required: %v", schema.Required)
		}
	}
}

func TestRealityKeysMustBeAPair(t *testing.T) {
	const priv = "u03CTWUF4qEGT24p7bBnBU7OM0VUUrHThNjlklFEfcg"
	const pub = "EVmCoU4Swh5-Pv9WHB68iac25hNF2VwABHEnIZOTfyE"
	const other = "L-V9o0fNYkMVKNqsX7spBzD_9oSvxM_C7ZCZX1jLO3Q"
	cfg := func(publicKey string) json.RawMessage {
		return json.RawMessage(`{"tls":2,"network":"tcp","reality_settings":{"dest":"www.example.com:443","server_name":"www.example.com","private_key":"` + priv + `","public_key":"` + publicKey + `","short_id":"0a1b"}}`)
	}
	if _, fields := ValidateAdminProtocolConfig("vless", "pandora-native", 443, cfg(pub)); len(fields) != 0 {
		t.Fatalf("matching key pair rejected: %v", fields)
	}
	_, fields := ValidateAdminProtocolConfig("vless", "pandora-native", 443, cfg(other))
	msg := ""
	for key, value := range fields {
		if strings.Contains(key, "public_key") {
			msg = value
		}
	}
	if !strings.Contains(msg, "不是一对") {
		t.Fatalf("mismatched key pair accepted or wrong message: %v", fields)
	}
	// 生成按钮出来的一对一定通过
	gPriv, gPub, err := GenerateRealityKeypair()
	if err != nil || !realityKeysPaired(gPriv, gPub) {
		t.Fatalf("generated pair not recognised: %v", err)
	}
}

// ShadowTLS 的握手端口要折进 server：下发时 server_port 被入站监听端口占住。
func TestShadowTLSHandshakePortSurvivesDelivery(t *testing.T) {
	got := buildConfig(t, "shadowtls", "node.example.com",
		`{"password":"p","handshake_server":"www.example.com","server_port":443,"version":3}`)
	if got["server"] != "www.example.com:443" || got["server_port"] != float64(10443) {
		t.Fatalf("handshake target lost its port: server=%#v server_port=%#v", got["server"], got["server_port"])
	}
	// 已带端口、没填 server_port 的：原样不动（存量下发字节不变）
	got = buildConfig(t, "shadowtls", "node.example.com", `{"password":"p","server":"www.example.com:8443"}`)
	if got["server"] != "www.example.com:8443" {
		t.Fatalf("explicit handshake port rewritten: %#v", got["server"])
	}
	got = buildConfig(t, "shadowtls", "node.example.com", `{"password":"p","handshake_server":"www.example.com"}`)
	if _, has := got["server"]; has {
		t.Fatalf("legacy config without server_port must keep its delivered bytes: %#v", got)
	}
}
