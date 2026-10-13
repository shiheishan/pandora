package protocol

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

const (
	edgeCert = "/etc/pandora-native/certs/example.com/fullchain.pem"
	edgeKey  = "/etc/pandora-native/certs/example.com/privkey.pem"
)

func edgeHas(fields map[string]string, key string) bool {
	_, ok := fields[key]
	return ok
}

func edgeMustField(t *testing.T, fields map[string]string, key string) {
	t.Helper()
	if !edgeHas(fields, key) {
		t.Fatalf("fields=%#v, want key %q", fields, key)
	}
}

func edgeMustNoField(t *testing.T, fields map[string]string, key string) {
	t.Helper()
	if edgeHas(fields, key) {
		t.Fatalf("fields=%#v, unexpected key %q", fields, key)
	}
}

func edgeTuicXboard(extra string) json.RawMessage {
	base := `{"network":"udp","cert_path":"` + edgeCert + `","key_path":"` + edgeKey + `"`
	if extra != "" {
		base += "," + extra
	}
	return json.RawMessage(base + "}")
}

// --- 1. Validate 入口与通用约束 ---

func TestEdgeValidateEmptyNodeType(t *testing.T) {
	cases := []struct {
		nodeType string
		raw      json.RawMessage
	}{
		{"", nil},
		{"", json.RawMessage(`{}`)},
		{"  ", json.RawMessage(`{"cipher":"aes-128-gcm"}`)},
	}
	for _, c := range cases {
		ver, fields := Validate(c.nodeType, "auto", 443, c.raw)
		if ver != 0 || len(fields) != 0 {
			t.Fatalf("nodeType=%q ver=%d fields=%#v, want 0 and empty", c.nodeType, ver, fields)
		}
	}
}

func TestEdgeValidateRawShapeAndSize(t *testing.T) {
	_, f := Validate("shadowsocks", "auto", 443, nil)
	edgeMustField(t, f, "protocol_config")

	_, f = Validate("shadowsocks", "auto", 443, json.RawMessage(``))
	edgeMustField(t, f, "protocol_config")

	big := json.RawMessage(`{"cipher":"` + strings.Repeat("a", 16*1024) + `"}`)
	if len(big) <= 16*1024 {
		t.Fatal("oversize fixture too small")
	}
	_, f = Validate("shadowsocks", "auto", 443, big)
	edgeMustField(t, f, "protocol_config")

	_, f = Validate("shadowsocks", "auto", 443, json.RawMessage(`{"cipher":"a","cipher":"b"}`))
	edgeMustField(t, f, "protocol_config")

	for _, raw := range []json.RawMessage{
		json.RawMessage(`[]`),
		json.RawMessage(`"x"`),
		json.RawMessage(`null`),
	} {
		_, f = Validate("shadowsocks", "auto", 443, raw)
		edgeMustField(t, f, "protocol_config")
	}
}

func TestEdgeValidateUnknownTypeKernelPort(t *testing.T) {
	ver, f := Validate("v2ray", "auto", 443, json.RawMessage(`{}`))
	if ver != 0 {
		t.Fatalf("ver=%d, want 0", ver)
	}
	edgeMustField(t, f, "node_type")

	_, f = Validate("vless", "sing-box", 443, json.RawMessage(`{"network":"ws","tls":0}`))
	edgeMustField(t, f, "kernel")

	_, f = Validate("vless", "auto", 0, json.RawMessage(`{"network":"ws","tls":0}`))
	edgeMustField(t, f, "server_port")
	_, f = Validate("vless", "auto", 70000, json.RawMessage(`{"network":"ws","tls":0}`))
	edgeMustField(t, f, "server_port")
}

// --- 2. 客户端 TLS 提示 ---

func TestEdgeValidateClientTLSHints(t *testing.T) {
	badSNI := []json.RawMessage{
		edgeTuicXboard(`"tls_settings":{"server_name":123}`),
		edgeTuicXboard(`"tls_settings":{"server_name":" a.example.com"}`),
		edgeTuicXboard(`"tls_settings":{"server_name":"a_b.example.com"}`),
		edgeTuicXboard(`"tls_settings":{"server_name":"` + strings.Repeat("a", 254) + `.com"}`),
	}
	for _, raw := range badSNI {
		_, f := Validate("tuic", "auto", 443, raw)
		edgeMustField(t, f, "protocol_config.tls_settings.server_name")
	}

	_, f := Validate("tuic", "auto", 443, edgeTuicXboard(`"tls_settings":{"server_name":""}`))
	edgeMustNoField(t, f, "protocol_config.tls_settings.server_name")

	_, f = Validate("tuic", "auto", 443, edgeTuicXboard(`"tls_settings":{"allow_insecure":"yes"}`))
	edgeMustField(t, f, "protocol_config.tls_settings.allow_insecure")
}

// --- 3. AnyTLS ---

func TestEdgeValidateAnyTLSCertsAndPadding(t *testing.T) {
	_, f := Validate("anytls", "auto", 443, json.RawMessage(`{"network":"tcp"}`))
	edgeMustField(t, f, "protocol_config.cert_path")
	edgeMustField(t, f, "protocol_config.key_path")

	badPad := []string{
		`"padding_scheme":""`,
		`"padding_scheme":[]`,
		`"padding_scheme":[1]`,
		`"padding_scheme":[""]`,
		`"padding_scheme":{}`,
		`"padding_scheme":5`,
	}
	for _, p := range badPad {
		raw := json.RawMessage(`{"network":"tcp","cert_path":"` + edgeCert + `","key_path":"` + edgeKey + `",` + p + `}`)
		_, f = Validate("anytls", "auto", 443, raw)
		edgeMustField(t, f, "protocol_config.padding_scheme")
	}

	ok := json.RawMessage(`{"network":"tcp","cert_path":"` + edgeCert + `","key_path":"` + edgeKey + `","padding_scheme":"64-128"}`)
	ver, f := Validate("anytls", "auto", 443, ok)
	if ver != schemaVersion || edgeHas(f, "protocol_config.padding_scheme") {
		t.Fatalf("ver=%d fields=%#v, want valid padding string", ver, f)
	}
	ok2 := json.RawMessage(`{"network":"tcp","cert_path":"` + edgeCert + `","key_path":"` + edgeKey + `","padding_scheme":["a","b"]}`)
	ver, f = Validate("anytls", "auto", 443, ok2)
	if ver != schemaVersion || len(f) != 0 {
		t.Fatalf("ver=%d fields=%#v, want valid padding array", ver, f)
	}
}

// --- 4. ShadowTLS ---

func TestEdgeValidateShadowTLS(t *testing.T) {
	base := `{"password":"p","server":"www.example.com:443","version":3}`
	ver, f := validateKernelShape("shadowtls", "auto", 443, json.RawMessage(base))
	if ver != schemaVersion || len(f) != 0 {
		t.Fatalf("valid base: ver=%d fields=%#v", ver, f)
	}

	withHS := `{"password":"p","handshake_server":"www.example.com","server_port":443,"version":3}`
	ver, f = validateKernelShape("shadowtls", "auto", 443, json.RawMessage(withHS))
	if ver != schemaVersion || len(f) != 0 {
		t.Fatalf("handshake_server shape: ver=%d fields=%#v", ver, f)
	}

	cases := []struct {
		raw   string
		field string
	}{
		{`{"password":"p","server":"www.example.com:443","version":3,"network":"udp"}`, "protocol_config.network"},
		{`{"password":"p","server":"www.example.com:443","version":2}`, "protocol_config.version"},
		{`{"password":"p","server":"www.example.com:443","version":"x"}`, "protocol_config.version"},
		{`{"server":"www.example.com:443","version":3}`, "protocol_config.password"},
		{`{"password":"p","version":3}`, "protocol_config.server"},
		{`{"password":"p","server":"line\nhost:443","version":3}`, "protocol_config.server"},
		{`{"password":"p","server":"a.example.com:0","version":3}`, "protocol_config.server"},
		{`{"password":"p","server":"a.example.com:x","version":3}`, "protocol_config.server"},
		{`{"password":"p","server":":443","version":3}`, "protocol_config.server"},
		{`{"password":"p","server":"bad_host.example:443","version":3}`, "protocol_config.server"},
		{`{"password":"p","server":"www.example.com:443","version":3,"server_port":0}`, "protocol_config.server_port"},
		{`{"password":"p","server":"www.example.com:443","version":3,"server_port":70000}`, "protocol_config.server_port"},
		{`{"password":"p","server":"www.example.com:443","version":3,"server_port":"x"}`, "protocol_config.server_port"},
		{`{"password":"p","server":"www.example.com:443","version":3,"method":"rc4"}`, "protocol_config.method"},
	}
	for _, c := range cases {
		_, f := validateKernelShape("shadowtls", "auto", 443, json.RawMessage(c.raw))
		edgeMustField(t, f, c.field)
	}
}

// --- 5. Hysteria2 ---

func TestEdgeValidateHysteria2(t *testing.T) {
	cert := `,"cert_path":"` + edgeCert + `","key_path":"` + edgeKey + `"`
	cases := []struct {
		raw   string
		field string
	}{
		{`{"network":"tcp"` + cert + `}`, "protocol_config.network"},
		{`{"network":"udp","key_path":"` + edgeKey + `"}`, "protocol_config.cert_path"},
		{`{"network":"udp","cert_path":"` + edgeCert + `"}`, "protocol_config.key_path"},
		{`{"network":"udp"` + cert + `,"up_mbps":-1}`, "protocol_config.up_mbps"},
		{`{"network":"udp"` + cert + `,"down_mbps":1.5}`, "protocol_config.down_mbps"},
		{`{"network":"udp"` + cert + `,"up_mbps":"x"}`, "protocol_config.up_mbps"},
		{`{"network":"udp"` + cert + `,"udp_timeout":-1}`, "protocol_config.udp_timeout"},
		{`{"network":"udp"` + cert + `,"udp_timeout":1.5}`, "protocol_config.udp_timeout"},
		{`{"network":"udp"` + cert + `,"udp_timeout":""}`, "protocol_config.udp_timeout"},
		{`{"network":"udp"` + cert + `,"udp_timeout":"abc"}`, "protocol_config.udp_timeout"},
		{`{"network":"udp"` + cert + `,"udp_timeout":true}`, "protocol_config.udp_timeout"},
		{`{"network":"udp"` + cert + `,"udp_timeout":[]}`, "protocol_config.udp_timeout"},
		{`{"network":"udp"` + cert + `,"obfs":{"type":"plain"}}`, "protocol_config.obfs"},
		{`{"network":"udp"` + cert + `,"obfs":{"type":"salamander"}}`, "protocol_config.obfs"},
	}
	for _, c := range cases {
		_, f := validateKernelShape("hysteria2", "auto", 443, json.RawMessage(c.raw))
		edgeMustField(t, f, c.field)
	}

	for _, extra := range []string{
		`"udp_timeout":30`, `"udp_timeout":"30"`, `"udp_timeout":"30s"`, `"udp_timeout":null`,
	} {
		raw := json.RawMessage(`{"network":"udp"` + cert + `,` + extra + `}`)
		ver, f := validateKernelShape("hysteria2", "auto", 443, raw)
		if edgeHas(f, "protocol_config.udp_timeout") {
			t.Fatalf("udp_timeout should pass for %s: fields=%#v", extra, f)
		}
		_ = ver
	}

	// xboard 带宽键经 Validate 映射为 up_mbps / down_mbps
	_, f := Validate("hysteria2", "auto", 443, json.RawMessage(
		`{"network":"udp","cert_path":"`+edgeCert+`","key_path":"`+edgeKey+`","bandwidth":{"up":-1,"down":0}}`,
	))
	edgeMustField(t, f, "protocol_config.bandwidth.up")
}

// --- 6. Juicity / TUIC ---

func TestEdgeValidateJuicityTUIC(t *testing.T) {
	cert := `,"cert_path":"` + edgeCert + `","key_path":"` + edgeKey + `"`
	_, f := validateKernelShape("juicity", "auto", 443, json.RawMessage(`{"network":"tcp"`+cert+`}`))
	edgeMustField(t, f, "protocol_config.network")
	_, f = validateKernelShape("juicity", "auto", 443, json.RawMessage(`{"network":"udp"`+cert+`,"congestion_control":"reno"}`))
	edgeMustField(t, f, "protocol_config.congestion_control")

	_, f = validateKernelShape("tuic", "auto", 443, json.RawMessage(`{"network":"tcp"`+cert+`}`))
	edgeMustField(t, f, "protocol_config.network")
	_, f = validateKernelShape("tuic", "auto", 443, json.RawMessage(`{"network":"udp"`+cert+`,"congestion_control":"reno"}`))
	edgeMustField(t, f, "protocol_config.congestion_control")
	_, f = validateKernelShape("tuic", "auto", 443, json.RawMessage(`{"network":"udp"`+cert+`,"auth_timeout":-1}`))
	edgeMustField(t, f, "protocol_config.auth_timeout")
	_, f = validateKernelShape("tuic", "auto", 443, json.RawMessage(`{"network":"udp"`+cert+`,"heartbeat":"nope"}`))
	edgeMustField(t, f, "protocol_config.heartbeat")
}

// --- 7. socks / http / naive ---

func TestEdgeValidateNativeProxies(t *testing.T) {
	_, f := validateKernelShape("http", "auto", 443, json.RawMessage(`{"network":"udp","security":"none"}`))
	edgeMustField(t, f, "protocol_config.network")
	_, f = validateKernelShape("socks", "auto", 443, json.RawMessage(`{"network":"quic","security":"none"}`))
	edgeMustField(t, f, "protocol_config.network")
	_, f = validateKernelShape("naive", "auto", 443, json.RawMessage(`{"network":"udp","tls":true,"cert_path":"`+edgeCert+`","key_path":"`+edgeKey+`","security":"none"}`))
	edgeMustField(t, f, "protocol_config.network")

	_, f = validateKernelShape("socks", "auto", 443, json.RawMessage(`{"network":"tcp","security":"tls"}`))
	edgeMustField(t, f, "protocol_config.security")

	_, f = validateKernelShape("naive", "auto", 443, json.RawMessage(`{"network":"tcp","tls":false,"cert_path":"`+edgeCert+`","key_path":"`+edgeKey+`","security":"none"}`))
	edgeMustField(t, f, "protocol_config.tls")

	_, f = validateKernelShape("socks", "auto", 443, json.RawMessage(`{"network":"tcp","tls":true,"cert_path":"`+edgeCert+`","security":"none"}`))
	edgeMustField(t, f, "protocol_config.cert_path")

	_, f = validateKernelShape("socks", "auto", 443, json.RawMessage(`{"network":"tcp","tls":true,"security":"none"}`))
	edgeMustField(t, f, "protocol_config.tls")

	_, f = validateKernelShape("socks", "auto", 443, json.RawMessage(`{"network":"tcp","security":"none","fallback":"www.example.com:80"}`))
	edgeMustField(t, f, "protocol_config.fallback")

	_, f = validateKernelShape("naive", "auto", 443, json.RawMessage(`{"network":"tcp","tls":true,"cert_path":"`+edgeCert+`","key_path":"`+edgeKey+`","security":"none","fallback":"bad"}`))
	edgeMustField(t, f, "protocol_config.fallback")

	ver, f := validateKernelShape("naive", "auto", 443, json.RawMessage(`{"network":"tcp","tls":true,"cert_path":"`+edgeCert+`","key_path":"`+edgeKey+`","security":"none","fallback":"www.example.com:80"}`))
	if ver != schemaVersion || edgeHas(f, "protocol_config.fallback") {
		t.Fatalf("naive valid fallback: ver=%d fields=%#v", ver, f)
	}
}

// --- 8. Mieru ---

func TestEdgeValidateMieruTransport(t *testing.T) {
	_, f := validateKernelShape("mieru", "auto", 443, json.RawMessage(`{"transport":"QUIC"}`))
	edgeMustField(t, f, "protocol_config.transport")
	for _, raw := range []string{`{"transport":"TCP"}`, `{"transport":"UDP"}`, `{}`} {
		ver, f := validateKernelShape("mieru", "auto", 443, json.RawMessage(raw))
		if edgeHas(f, "protocol_config.transport") {
			t.Fatalf("transport should pass for %s: fields=%#v", raw, f)
		}
		_ = ver
	}
}

// --- 9. Shadowsocks ---

func TestEdgeValidateShadowsocksCipher(t *testing.T) {
	_, f := Validate("shadowsocks", "auto", 443, json.RawMessage(`{}`))
	edgeMustField(t, f, "protocol_config.cipher")

	_, f = Validate("shadowsocks", "auto", 443, json.RawMessage(`{"cipher":"rc4-md5"}`))
	edgeMustField(t, f, "protocol_config.cipher")

	_, f = Validate("shadowsocks", "auto", 443, json.RawMessage(`{"cipher":"aes-128-gcm","unknown":1}`))
	edgeMustField(t, f, "protocol_config")
}

// --- 10. Trojan ---

func TestEdgeValidateTrojan(t *testing.T) {
	priv, pub, err := GenerateRealityKeypair()
	if err != nil {
		t.Fatal(err)
	}
	reality := `{"network":"grpc","tls":false,"security":"reality","dest":"www.example.com:443","server_names":["www.example.com"],"private_key":"` + priv + `","public_key":"` + pub + `","short_ids":["ab"]}`
	_, f := validateKernelShape("trojan", "auto", 443, json.RawMessage(reality))
	edgeMustField(t, f, "protocol_config.security")

	_, f = validateKernelShape("trojan", "auto", 443, json.RawMessage(`{"network":"xhttp","tls":true,"cert_path":"`+edgeCert+`","key_path":"`+edgeKey+`"}`))
	edgeMustField(t, f, "protocol_config.network")

	_, f = Validate("trojan", "auto", 443, json.RawMessage(`{"network":"ws","tls":true,"cert_path":"`+edgeCert+`","key_path":"`+edgeKey+`","network_settings":{"path":"no-slash"}}`))
	edgeMustField(t, f, "protocol_config.network_settings.path")

	_, f = validateKernelShape("trojan", "auto", 443, json.RawMessage(`{"network":"grpc","tls":true,"grpc_path":"nope","cert_path":"`+edgeCert+`","key_path":"`+edgeKey+`"}`))
	edgeMustField(t, f, "protocol_config.grpc_path")

	long := "/" + strings.Repeat("a", 512)
	_, f = validateKernelShape("trojan", "auto", 443, json.RawMessage(`{"network":"grpc","tls":true,"grpc_path":"`+long+`","cert_path":"`+edgeCert+`","key_path":"`+edgeKey+`"}`))
	edgeMustField(t, f, "protocol_config.grpc_path")

	_, f = validateKernelShape("trojan", "auto", 443, json.RawMessage(`{"network":"grpc","tls":true,"grpc_service_name":"a\nb","cert_path":"`+edgeCert+`","key_path":"`+edgeKey+`"}`))
	edgeMustField(t, f, "protocol_config.grpc_service_name")

	_, f = validateKernelShape("trojan", "auto", 443, json.RawMessage(`{"network":"tcp","security":"none"}`))
	edgeMustField(t, f, "protocol_config.tls")

	_, f = Validate("trojan", "auto", 443, json.RawMessage(`{"network":"tcp","tls":0,"cert_path":"`+edgeCert+`","key_path":"`+edgeKey+`"}`))
	edgeMustField(t, f, "protocol_config.tls")

	_, f = validateKernelShape("trojan", "auto", 443, json.RawMessage(`{"network":"tcp","tls":true,"security":"none"}`))
	edgeMustField(t, f, "protocol_config.cert_path")

	_, f = validateKernelShape("vless", "auto", 443, json.RawMessage(`{"network":"ws","tls":false,"security":"xtls"}`))
	edgeMustField(t, f, "protocol_config.security")
}

// --- 11. VLESS / VMess（节选：未在 schema_test 覆盖的边） ---

func TestEdgeValidateVLESSVMessStream(t *testing.T) {
	_, f := validateKernelShape("vless", "auto", 443, json.RawMessage(`{"network":"quic","tls":false}`))
	edgeMustField(t, f, "protocol_config.network")

	_, f = validateKernelShape("vless", "auto", 443, json.RawMessage(`{"network":"ws","tls":false,"host":"a\nb"}`))
	edgeMustField(t, f, "protocol_config.host")

	_, f = validateKernelShape("vless", "auto", 443, json.RawMessage(`{"network":"ws","tls":false,"path":"no"}`))
	edgeMustField(t, f, "protocol_config.path")

	_, f = validateKernelShape("vless", "auto", 443, json.RawMessage(`{"network":"grpc","tls":false,"grpc_path":"x"}`))
	edgeMustField(t, f, "protocol_config.grpc_path")

	_, f = validateKernelShape("vless", "auto", 443, json.RawMessage(`{"network":"grpc","tls":false,"grpc_service_name":"x\ny"}`))
	edgeMustField(t, f, "protocol_config.grpc_service_name")

	xhttp := `{"network":"xhttp","tls":false,"path":"/ok","headers":{" ":"v"},"mode":"bogus","sc_max_each_post_bytes":1,"sc_max_buffered_posts":true,"server_max_header_bytes":1,"uplink_http_method":"PUT","session_placement":"bad","seq_placement":"bad","uplink_data_placement":"bad"}`
	_, f = validateKernelShape("vless", "auto", 443, json.RawMessage(xhttp))
	for _, key := range []string{
		"protocol_config.headers", "protocol_config.mode", "protocol_config.sc_max_each_post_bytes",
		"protocol_config.sc_max_buffered_posts", "protocol_config.server_max_header_bytes",
		"protocol_config.uplink_http_method", "protocol_config.session_placement",
		"protocol_config.seq_placement", "protocol_config.uplink_data_placement",
	} {
		edgeMustField(t, f, key)
	}

	_, f = validateKernelShape("vmess", "auto", 443, json.RawMessage(`{"network":"xhttp-h3","security":"auto"}`))
	edgeMustField(t, f, "protocol_config.cert_path")

	_, f = validateKernelShape("vmess", "auto", 443, json.RawMessage(`{"network":"tcp","tls":true,"security":"auto"}`))
	if f["protocol_config.tls"] != plainTLSNotYetMessage {
		t.Fatalf("vmess tls=true: fields=%#v", f)
	}

	_, f = validateKernelShape("vmess", "auto", 443, json.RawMessage(`{"network":"tcp","tls":false,"security":"reality"}`))
	edgeMustField(t, f, "protocol_config.security")

	_, f = validateKernelShape("vmess", "auto", 443, json.RawMessage(`{"network":"tcp","tls":false,"security":"bogus"}`))
	edgeMustField(t, f, "protocol_config.security")

	_, f = validateKernelShape("vmess", "auto", 443, json.RawMessage(`{"network":"tcp","tls":false,"security":"auto","flow":"x"}`))
	edgeMustField(t, f, "protocol_config.flow")

	_, f = validateKernelShape("vmess", "auto", 443, json.RawMessage(`{"network":"tcp","tls":false,"security":"auto","fallback":"www.example.com:80"}`))
	edgeMustField(t, f, "protocol_config.fallback")

	_, f = Validate("vless", "auto", 443, json.RawMessage(`{"network":"ws","tls":1}`))
	if f["protocol_config.tls"] != plainTLSNotYetMessage {
		t.Fatalf("vless tls=1: fields=%#v", f)
	}

	_, f = Validate("vless", "auto", 443, json.RawMessage(`{"network":"ws","tls":3}`))
	edgeMustField(t, f, "protocol_config.tls")

	_, f = validateKernelShape("vless", "auto", 443, json.RawMessage(`{"network":"ws","tls":false,"security":"nope"}`))
	edgeMustField(t, f, "protocol_config.security")

	badRange := `{"network":"xhttp","tls":false,"path":"/p","sc_max_each_post_bytes":{"from":10,"to":5}}`
	_, f = validateKernelShape("vless", "auto", 443, json.RawMessage(badRange))
	edgeMustField(t, f, "protocol_config.sc_max_each_post_bytes")
}

// --- 12. mKCP ---

func TestEdgeValidateMKCP(t *testing.T) {
	_, f := validateKernelShape("vless", "auto", 443, json.RawMessage(`{"network":"ws","tls":false,"mtu":1400}`))
	edgeMustField(t, f, "protocol_config.mtu")

	_, f = validateKernelShape("vless", "auto", 443, json.RawMessage(`{"network":"mkcp","tls":false,"mtu":"x"}`))
	edgeMustField(t, f, "protocol_config.mtu")

	_, f = validateKernelShape("vless", "auto", 443, json.RawMessage(`{"network":"mkcp","tls":false,"mtu":1.5}`))
	edgeMustField(t, f, "protocol_config.mtu")

	_, f = validateKernelShape("vless", "auto", 443, json.RawMessage(`{"network":"ws","tls":false,"mask":"none"}`))
	edgeMustField(t, f, "protocol_config.mask")

	_, f = validateKernelShape("vless", "auto", 443, json.RawMessage(`{"network":"mkcp","tls":false,"mask":"weird"}`))
	edgeMustField(t, f, "protocol_config.mask")

	_, f = validateKernelShape("vless", "auto", 443, json.RawMessage(`{"network":"mkcp","tls":false,"mask":"mkcp-aes128gcm"}`))
	edgeMustField(t, f, "protocol_config.mask_password")

	_, f = validateKernelShape("vless", "auto", 443, json.RawMessage(`{"network":"mkcp","tls":false,"mask":"none","mask_password":"x"}`))
	edgeMustField(t, f, "protocol_config.mask_password")

	_, f = validateKernelShape("vless", "auto", 443, json.RawMessage(`{"network":"mkcp","tls":false,"mask":"mkcp-aes128gcm","mask_password":"p","mtu":1450}`))
	edgeMustField(t, f, "protocol_config.mtu")
}

// --- 13. 严格解码与 protocolJSONInt ---

func TestEdgeStrictDecodeAndJSONInt(t *testing.T) {
	var sink struct {
		Method string `json:"method"`
	}
	for _, raw := range []string{
		`{"a":{"b":1,"b":2}}`,
		`[{"k":1,"k":2}]`,
		`{} {}`,
		`{"method":"aes-128-gcm"`,
	} {
		err := decodeStrictProtocolObject([]byte(raw), &sink)
		if err == nil {
			t.Fatalf("decode should fail for %q", raw)
		}
	}

	if n, ok := protocolJSONInt(float64(3)); !ok || n != 3 {
		t.Fatalf("float64: n=%d ok=%v", n, ok)
	}
	if _, ok := protocolJSONInt(float64(1.5)); ok {
		t.Fatal("non-integer float should fail")
	}
	if n, ok := protocolJSONInt(json.Number("7")); !ok || n != 7 {
		t.Fatalf("json.Number: n=%d ok=%v", n, ok)
	}
	if n, ok := protocolJSONInt("42"); !ok || n != 42 {
		t.Fatalf("string: n=%d ok=%v", n, ok)
	}
	if _, ok := protocolJSONInt(true); ok {
		t.Fatal("bool should fail")
	}

	if err := rejectDuplicateJSONKeys([]byte(`{"cipher":"a","cipher":"b"}`)); err == nil {
		t.Fatal("duplicate top-level keys")
	}
	var extra bytes.Buffer
	extra.WriteString(`{"method":"aes-128-gcm"}`)
	extra.WriteString(` {"method":"aes-256-gcm"}`)
	if err := decodeStrictProtocolObject(extra.Bytes(), &sink); err == nil {
		t.Fatal("trailing JSON value")
	}
}
