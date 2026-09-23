package nodefabric

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestValidateProtocolConfigShadowsocksV1(t *testing.T) {
	version, fields := ValidateProtocolConfig(
		"shadowsocks", "auto", 443, json.RawMessage(`{"method":"aes-256-gcm"}`))
	if version != 1 || len(fields) != 0 {
		t.Fatalf("version=%d fields=%#v, want v1 valid", version, fields)
	}
}

func TestValidateProtocolConfigAcceptsNativeProxyInbounds(t *testing.T) {
	tests := []struct {
		nodeType, raw string
	}{
		{"socks", `{"network":"udp","tls":true,"cert_path":"cert","key_path":"key"}`},
		{"http", `{"network":"tcp","tls":false,"security":"none"}`},
		{"naive", `{"network":"tcp","tls":true,"cert_path":"cert","key_path":"key","security":"none"}`},
		{"mieru", `{"transport":"udp"}`},
		{"shadowtls", `{"network":"tcp","version":3,"password":"outer-secret","server":"www.example.com:443","method":"aes-256-gcm","strict":true}`},
	}
	for _, test := range tests {
		if version, fields := ValidateProtocolConfig(test.nodeType, "pandora-native", 443, json.RawMessage(test.raw)); version != 1 || len(fields) != 0 {
			t.Fatalf("%s version=%d fields=%#v", test.nodeType, version, fields)
		}
	}
}

func TestValidateProtocolConfigRejectsInvalidNativeProxyInbounds(t *testing.T) {
	tests := []struct {
		name, nodeType, raw, field string
	}{
		{"HTTP UDP", "http", `{"network":"udp"}`, "protocol_config.network"},
		{"SOCKS bad security", "socks", `{"security":"reality"}`, "protocol_config.security"},
		{"TLS missing certificate", "socks", `{"tls":true}`, "protocol_config.tls"},
		{"Naive without TLS", "naive", `{"network":"tcp","cert_path":"cert","key_path":"key"}`, "protocol_config.tls"},
		{"Mieru bad transport", "mieru", `{"transport":"quic"}`, "protocol_config.transport"},
		{"ShadowTLS bad version", "shadowtls", `{"password":"secret","server":"example.com","version":2}`, "protocol_config.version"},
		{"ShadowTLS missing server", "shadowtls", `{"password":"secret"}`, "protocol_config.server"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, fields := ValidateProtocolConfig(test.nodeType, "pandora-native", 443, json.RawMessage(test.raw))
			if _, ok := fields[test.field]; !ok {
				t.Fatalf("fields=%#v, want %s", fields, test.field)
			}
		})
	}
}

func TestValidateProtocolConfigAcceptsPandoraNativeKernel(t *testing.T) {
	version, fields := ValidateProtocolConfig(
		"vless", "pandora-native", 443, json.RawMessage(`{"network":"tcp","tls":false}`))
	if version != StableProtocolSchemaVersion || len(fields) != 0 {
		t.Fatalf("version=%d fields=%#v, want NativeCore v1 valid", version, fields)
	}
}

func TestProtocolSchemasAdvertiseNativeXHTTPTransports(t *testing.T) {
	byType := map[string]ProtocolSchema{}
	for _, schema := range ProtocolSchemas() {
		byType[schema.NodeType] = schema
	}
	for _, nodeType := range []string{"vless", "vmess"} {
		schema := byType[nodeType]
		for _, network := range []string{"tcp", "ws", "httpupgrade", "grpc", "xhttp", "xhttp-h3"} {
			if !containsString(schema.Enums["network"], network) {
				t.Errorf("%s network %q is not advertised", nodeType, network)
			}
		}
		if schema.PropertyTypes["headers"] != "json" || schema.PropertyTypes["sc_max_each_post_bytes"] != "json" {
			t.Errorf("%s XHTTP structured fields missing types: %#v", nodeType, schema.PropertyTypes)
		}
	}
}

func TestValidateProtocolConfigAcceptsNativeXHTTPAndVMessGRPC(t *testing.T) {
	vless := json.RawMessage(`{"network":"xhttp","mode":"stream-one","path":"/native/","headers":{"x-pandora":"ok"},"sc_max_each_post_bytes":{"from":1024,"to":2048},"sc_min_posts_interval_ms":{"from":10,"to":30},"sc_stream_up_server_secs":{"from":20,"to":80},"uplink_chunk_size":{"from":1024,"to":4096}}`)
	if version, fields := ValidateProtocolConfig("vless", "pandora-native", 443, vless); version != 1 || len(fields) != 0 {
		t.Fatalf("vless xhttp version=%d fields=%#v", version, fields)
	}
	vmess := json.RawMessage(`{"network":"grpc","grpc_service_name":"PandoraService","security":"auto"}`)
	if version, fields := ValidateProtocolConfig("vmess", "pandora-native", 443, vmess); version != 1 || len(fields) != 0 {
		t.Fatalf("vmess grpc version=%d fields=%#v", version, fields)
	}
}

func TestValidateProtocolConfigAcceptsNativeHysteria2(t *testing.T) {
	raw := json.RawMessage(`{"network":"udp","cert_path":"/etc/pandora/cert.pem","key_path":"/etc/pandora/key.pem","obfs":{"type":"salamander","password":"secret"},"up_mbps":100,"down_mbps":"200","udp_timeout":"5m"}`)
	if version, fields := ValidateProtocolConfig("hysteria2", "pandora-native", 443, raw); version != 1 || len(fields) != 0 {
		t.Fatalf("hysteria2 version=%d fields=%#v", version, fields)
	}
}

func TestValidateProtocolConfigAcceptsNativeJuicity(t *testing.T) {
	raw := json.RawMessage(`{"network":"udp","cert_path":"/etc/pandora/cert.pem","key_path":"/etc/pandora/key.pem","congestion_control":"bbr"}`)
	if version, fields := ValidateProtocolConfig("juicity", "pandora-native", 443, raw); version != 1 || len(fields) != 0 {
		t.Fatalf("juicity version=%d fields=%#v", version, fields)
	}
}

func TestValidateProtocolConfigRejectsInvalidNativeJuicity(t *testing.T) {
	tests := []struct {
		name, raw, field string
	}{
		{"missing certificate", `{"network":"udp","key_path":"key"}`, "protocol_config.cert_path"},
		{"wrong network", `{"network":"tcp","cert_path":"cert","key_path":"key"}`, "protocol_config.network"},
		{"wrong congestion", `{"network":"udp","cert_path":"cert","key_path":"key","congestion_control":"reno"}`, "protocol_config.congestion_control"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, fields := ValidateProtocolConfig("juicity", "pandora-native", 443, json.RawMessage(test.raw))
			if _, ok := fields[test.field]; !ok {
				t.Fatalf("fields=%#v, want %s", fields, test.field)
			}
		})
	}
}

func TestValidateProtocolConfigAcceptsNativeTUIC(t *testing.T) {
	raw := json.RawMessage(`{"network":"udp","cert_path":"/etc/pandora/cert.pem","key_path":"/etc/pandora/key.pem","congestion_control":"bbr","auth_timeout":"3s","heartbeat":10,"udp_timeout":"5m","zero_rtt":false}`)
	if version, fields := ValidateProtocolConfig("tuic", "pandora-native", 443, raw); version != 1 || len(fields) != 0 {
		t.Fatalf("tuic version=%d fields=%#v", version, fields)
	}
}

func TestValidateProtocolConfigAcceptsNativeAnyTLS(t *testing.T) {
	raw := json.RawMessage(`{"network":"tcp","tls":true,"cert_path":"/etc/pandora/cert.pem","key_path":"/etc/pandora/key.pem","padding_scheme":["64-128","256-512"]}`)
	if version, fields := ValidateProtocolConfig("anytls", "pandora-native", 443, raw); version != 1 || len(fields) != 0 {
		t.Fatalf("anytls version=%d fields=%#v", version, fields)
	}
}

func TestValidateProtocolConfigAcceptsNativeTrojanTLSAndReality(t *testing.T) {
	tests := []string{
		`{"network":"grpc","tls":true,"grpc_path":"/pandora.Trojan","grpc_service_name":"Proxy","cert_path":"/etc/pandora/cert.pem","key_path":"/etc/pandora/key.pem"}`,
		`{"network":"tcp","tls":false,"security":"reality","dest":"www.example.com:443","server_names":["www.example.com"],"private_key":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA","public_key":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA","short_ids":["0123456789abcdef"]}`,
	}
	for _, raw := range tests {
		if version, fields := ValidateProtocolConfig("trojan", "pandora-native", 443, json.RawMessage(raw)); version != 1 || len(fields) != 0 {
			t.Fatalf("trojan version=%d fields=%#v raw=%s", version, fields, raw)
		}
	}
}

func TestValidateProtocolConfigRejectsInvalidNativeTrojan(t *testing.T) {
	tests := []struct {
		name, raw, field string
	}{
		{"missing TLS", `{"network":"tcp"}`, "protocol_config.tls"},
		{"TLS missing certificate", `{"network":"tcp","tls":true}`, "protocol_config.cert_path"},
		{"REALITY on websocket", `{"network":"ws","tls":false,"security":"reality","dest":"www.example.com:443","server_names":["www.example.com"],"private_key":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA","public_key":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA","short_ids":["0123456789abcdef"]}`, "protocol_config.security"},
		{"bad network", `{"network":"xhttp","tls":true,"cert_path":"cert","key_path":"key"}`, "protocol_config.network"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, fields := ValidateProtocolConfig("trojan", "pandora-native", 443, json.RawMessage(test.raw))
			if _, ok := fields[test.field]; !ok {
				t.Fatalf("fields=%#v, want %s", fields, test.field)
			}
		})
	}
}

func TestValidateProtocolConfigRejectsInvalidNativeAnyTLS(t *testing.T) {
	tests := []struct {
		name, raw, field string
	}{
		{"TLS without certificate", `{"network":"tcp","tls":true}`, "protocol_config.tls"},
		{"wrong network", `{"network":"udp"}`, "protocol_config.network"},
		{"bad padding", `{"padding_scheme":["ok",3]}`, "protocol_config.padding_scheme"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, fields := ValidateProtocolConfig("anytls", "pandora-native", 443, json.RawMessage(test.raw))
			if _, ok := fields[test.field]; !ok {
				t.Fatalf("fields=%#v, want %s", fields, test.field)
			}
		})
	}
}

func TestValidateProtocolConfigRejectsInvalidNativeTUIC(t *testing.T) {
	tests := []struct {
		name, raw, field string
	}{
		{"missing certificate", `{"network":"udp","key_path":"key"}`, "protocol_config.cert_path"},
		{"wrong congestion", `{"network":"udp","cert_path":"cert","key_path":"key","congestion_control":"reno"}`, "protocol_config.congestion_control"},
		{"wrong boolean", `{"network":"udp","cert_path":"cert","key_path":"key","zero_rtt":"false"}`, "protocol_config"},
		{"bad heartbeat", `{"network":"udp","cert_path":"cert","key_path":"key","heartbeat":-1}`, "protocol_config.heartbeat"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, fields := ValidateProtocolConfig("tuic", "pandora-native", 443, json.RawMessage(test.raw))
			if _, ok := fields[test.field]; !ok {
				t.Fatalf("fields=%#v, want %s", fields, test.field)
			}
		})
	}
}

func TestValidateProtocolConfigRejectsInvalidNativeHysteria2(t *testing.T) {
	tests := []struct {
		name, raw, field string
	}{
		{"missing certificate", `{"network":"udp","key_path":"key"}`, "protocol_config.cert_path"},
		{"wrong network", `{"network":"tcp","cert_path":"cert","key_path":"key"}`, "protocol_config.network"},
		{"bad obfs", `{"network":"udp","cert_path":"cert","key_path":"key","obfs":{"type":"http"}}`, "protocol_config.obfs"},
		{"bad timeout", `{"network":"udp","cert_path":"cert","key_path":"key","udp_timeout":-1}`, "protocol_config.udp_timeout"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, fields := ValidateProtocolConfig("hysteria2", "pandora-native", 443, json.RawMessage(test.raw))
			if _, ok := fields[test.field]; !ok {
				t.Fatalf("fields=%#v, want %s", fields, test.field)
			}
		})
	}
}

func TestValidateProtocolConfigRequiresNativeXHTTPH3CertificateOrReality(t *testing.T) {
	_, fields := ValidateProtocolConfig("vless", "pandora-native", 443, json.RawMessage(`{"network":"xhttp-h3"}`))
	if _, ok := fields["protocol_config.cert_path"]; !ok {
		t.Fatalf("xhttp-h3 without certificate was accepted: %#v", fields)
	}
}

func TestValidateProtocolConfigRejectsMalformedNativeXHTTPRange(t *testing.T) {
	_, fields := ValidateProtocolConfig("vless", "pandora-native", 443, json.RawMessage(`{"network":"xhttp","sc_max_each_post_bytes":1024}`))
	if _, ok := fields["protocol_config.sc_max_each_post_bytes"]; !ok {
		t.Fatalf("malformed XHTTP range was accepted: %#v", fields)
	}
}

func TestValidateProtocolConfigRejectsUnknownAndUnsafeValues(t *testing.T) {
	tests := []struct {
		name     string
		typeName string
		port     int
		raw      string
		field    string
	}{
		{"unknown property", "shadowsocks", 443, `{"method":"aes-256-gcm","command":"sh"}`, "protocol_config"},
		{"bad method", "shadowsocks", 443, `{"method":"rc4-md5"}`, "protocol_config.method"},
		{"bad port", "shadowsocks", 0, `{"method":"aes-256-gcm"}`, "server_port"},
		{"unsupported protocol", "wireguard", 443, `{}`, "node_type"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, fields := ValidateProtocolConfig(tt.typeName, "auto", tt.port, json.RawMessage(tt.raw))
			if _, ok := fields[tt.field]; !ok {
				t.Fatalf("fields=%#v, want %q", fields, tt.field)
			}
		})
	}
}

func TestValidateProtocolConfigKeepsLegacyProtocolsAtVersionZero(t *testing.T) {
	version, fields := ValidateProtocolConfig(
		"v2ray", "sing-box", 443, json.RawMessage(`{"legacy_option":true}`))
	if version != 0 || len(fields) != 0 {
		t.Fatalf("version=%d fields=%#v, want compatible v0", version, fields)
	}
}

func TestCanonicalNodeType(t *testing.T) {
	if got := CanonicalNodeType("  ShadowSocks \t"); got != "shadowsocks" {
		t.Fatalf("canonical=%q, want shadowsocks", got)
	}
	version, fields := ValidateProtocolConfig(
		"  ShadowSocks \t", "auto", 443, json.RawMessage(`{"method":"aes-256-gcm"}`))
	if version != 1 || len(fields) != 0 {
		t.Fatalf("version=%d fields=%#v, want canonical v1 valid", version, fields)
	}
}

func TestValidateProtocolConfigRejectsDuplicateKeys(t *testing.T) {
	tests := []string{
		`{"method":"aes-256-gcm","method":"rc4-md5"}`,
		`{"method":"aes-256-gcm","nested":{"enabled":true,"enabled":false}}`,
	}
	for _, raw := range tests {
		_, fields := ValidateProtocolConfig("shadowsocks", "auto", 443, json.RawMessage(raw))
		if _, ok := fields["protocol_config"]; !ok {
			t.Fatalf("raw=%s fields=%#v, want duplicate-key rejection", raw, fields)
		}
	}
}

func TestValidateProtocolConfigReportsMalformedJSONSeparately(t *testing.T) {
	_, fields := ValidateProtocolConfig(
		"v2ray", "sing-box", 443, json.RawMessage(`{"network":`))
	if got := fields["protocol_config"]; got != "协议配置必须是合法的 JSON 对象" {
		t.Fatalf("message=%q, want malformed JSON message", got)
	}
}

func TestValidateProtocolConfigStableRenderSubset(t *testing.T) {
	tests := []struct {
		name, nodeType, kernel, raw string
	}{
		{"shadowsocks", "shadowsocks", "auto", `{"method":"aes-256-gcm"}`},
		{"vless tcp plaintext", "vless", "xray-core", `{"network":"tcp","tls":false}`},
		{"vmess tcp", "vmess", "xray-core", `{"network":"tcp","tls":false}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			version, fields := ValidateProtocolConfig(tt.nodeType, tt.kernel, 443, json.RawMessage(tt.raw))
			if version != 1 || len(fields) != 0 {
				t.Fatalf("version=%d fields=%#v", version, fields)
			}
		})
	}
}

func TestValidateProtocolConfigStableSubsetFailsClosed(t *testing.T) {
	tests := []struct{ nodeType, kernel, raw, field string }{
		{"shadowsocks", "auto", `{"method":"2022-blake3-aes-128-gcm"}`, "protocol_config.method"},
		{"vless", "xray-core", `{"network":"quic","tls":false}`, "protocol_config.network"},
		{"vmess", "xray-core", `{"network":"tcp","tls":"true"}`, "protocol_config"},
		{"vless", "xray-core", `{"network":"tcp","tls":true}`, "protocol_config.tls"},
		{"vless", "xray-core", `{"network":"tcp","server_name":"edge.example.com"}`, "protocol_config"},
	}
	for _, tt := range tests {
		_, fields := ValidateProtocolConfig(tt.nodeType, tt.kernel, 443, json.RawMessage(tt.raw))
		if _, ok := fields[tt.field]; !ok {
			t.Errorf("%s fields=%#v, want %s", tt.nodeType, fields, tt.field)
		}
	}
}

func TestProtocolSchemasAdvertiseOnlyClosedStableSubset(t *testing.T) {
	stable := map[string]ProtocolSchema{}
	for _, schema := range ProtocolSchemas() {
		if schema.Status == "stable" {
			stable[schema.NodeType] = schema
		}
	}
	for _, nodeType := range []string{"anytls", "http", "hysteria2", "juicity", "mieru", "naive", "shadowsocks", "shadowtls", "socks", "trojan", "tuic", "vless", "vmess"} {
		if stable[nodeType].Version != 1 {
			t.Errorf("%s not advertised as stable v1", nodeType)
		}
	}
}

func TestRedactProtocolConfigRemovesNestedCredentialKeys(t *testing.T) {
	raw := json.RawMessage(`{"method":"aes-256-gcm","password":"top-secret","nested":{"token":"abc","host":"edge.example.com"},"items":[{"private_key":"key","name":"safe"}]}`)
	redacted := RedactProtocolConfig(raw)
	var got map[string]any
	if err := json.Unmarshal(redacted, &got); err != nil {
		t.Fatal(err)
	}
	encoded := string(redacted)
	for _, secret := range []string{"top-secret", "abc", "key", "password", "token", "private_key"} {
		if strings.Contains(encoded, secret) {
			t.Fatalf("redacted config still contains %q: %s", secret, encoded)
		}
	}
	if got["method"] != "aes-256-gcm" || !strings.Contains(encoded, "edge.example.com") || !strings.Contains(encoded, "safe") {
		t.Fatalf("non-sensitive fields were lost: %s", encoded)
	}
}

// mKCP 的调优参数要能存进去。节点端实现了这些参数的解析，面板不放行
// 就等于白做——管理员只能用默认值。
func TestValidateProtocolConfigAcceptsMKCPTuning(t *testing.T) {
	raw := json.RawMessage(`{"network":"mkcp","security":"none","mtu":1350,"tti":50,
		"uplink_capacity":50,"downlink_capacity":100,"congestion":true,
		"read_buffer_size":4,"write_buffer_size":4}`)
	_, fields := ValidateProtocolConfig("vless", "pandora-native", 28444, raw)
	if len(fields) != 0 {
		t.Fatalf("合法的 mKCP 配置被拒：%v", fields)
	}
}

// 越界要在面板层就拦住。放过去的话，管理员要等到节点启动失败才知道
// 配错了，而他看到的只是「节点不健康」。
func TestValidateProtocolConfigRejectsOutOfRangeMKCP(t *testing.T) {
	for name, raw := range map[string]string{
		"mtu 太小":      `{"network":"mkcp","mtu":100}`,
		"mtu 太大":      `{"network":"mkcp","mtu":9000}`,
		"tti 太小":      `{"network":"mkcp","tti":1}`,
		"tti 太大":      `{"network":"mkcp","tti":9999}`,
		"uplink 为零":   `{"network":"mkcp","uplink_capacity":0}`,
		"downlink 过大": `{"network":"mkcp","downlink_capacity":99999}`,
		"缓冲区过大":       `{"network":"mkcp","read_buffer_size":9999}`,
	} {
		_, fields := ValidateProtocolConfig("vless", "pandora-native", 28444, json.RawMessage(raw))
		if len(fields) == 0 {
			t.Errorf("%s：越界值被放行了", name)
		}
	}
}

// 传输不是 mKCP 却带着这些参数，多半是从别的配置复制粘贴带过来的。
// 留着会让人以为它们生效了。
func TestValidateProtocolConfigRejectsMKCPParamsOnOtherTransports(t *testing.T) {
	_, fields := ValidateProtocolConfig("vless", "pandora-native", 443,
		json.RawMessage(`{"network":"ws","path":"/x","mtu":1350}`))
	if len(fields) == 0 {
		t.Error("ws 传输带 mtu 参数应当被拒")
	}
}

// 掩码配置要能存进去，vless / vmess / trojan 三个都得支持——
// 它们的 network 枚举里都有 mkcp。
func TestValidateProtocolConfigAcceptsMKCPMask(t *testing.T) {
	raw := json.RawMessage(`{"network":"mkcp","security":"none",
		"mask":"mkcp-aes128gcm","mask_password":"hunter2","mtu":1350}`)
	for _, nodeType := range []string{"vless", "vmess", "trojan"} {
		cfg := raw
		if nodeType == "trojan" {
			// trojan 的 tls=true 要求同时给证书路径，那是既有校验
			cfg = json.RawMessage(`{"network":"mkcp","tls":true,
				"cert_path":"/etc/ssl/a.crt","key_path":"/etc/ssl/a.key",
				"mask":"mkcp-aes128gcm","mask_password":"hunter2","mtu":1350}`)
		}
		_, fields := ValidateProtocolConfig(nodeType, "pandora-native", 28444, cfg)
		if len(fields) != 0 {
			t.Errorf("%s 的合法掩码配置被拒：%v", nodeType, fields)
		}
	}
}

// 掩码配错要在面板层拦住，不能等节点起不来。
func TestValidateProtocolConfigRejectsBadMKCPMask(t *testing.T) {
	for name, raw := range map[string]string{
		"加密掩码不给密码":     `{"network":"mkcp","mask":"mkcp-aes128gcm"}`,
		"不认识的掩码类型":     `{"network":"mkcp","mask":"header-srtp"}`,
		"没开加密却填密码":     `{"network":"mkcp","mask":"none","mask_password":"x"}`,
		"非 mkcp 传输带掩码": `{"network":"ws","path":"/x","mask":"mkcp-aes128gcm","mask_password":"p"}`,
		// 顶着 MTU 上限再套掩码，加密后的包会被 IP 分片
		"掩码开销撑爆 MTU": `{"network":"mkcp","mask":"mkcp-aes128gcm","mask_password":"p","mtu":1460}`,
	} {
		_, fields := ValidateProtocolConfig("vless", "pandora-native", 28444, json.RawMessage(raw))
		if len(fields) == 0 {
			t.Errorf("%s：应当被拒却通过了", name)
		}
	}
}

// 面板和节点端的 MTU 边界必须是同一个数。对不上就会出现
// 「面板存进去了、节点起不来」这种最难查的状态。
func TestMKCPMTUBoundsMatchNodeAgent(t *testing.T) {
	// 这两个值同时写在 pdnd/kernel/mkcp_transport.go 里，改一处就要改两处
	if mkcpMinMTU != 576 || mkcpMaxMTU != 1460 {
		t.Errorf("MTU 边界变成了 %d-%d，节点端那边也要同步改", mkcpMinMTU, mkcpMaxMTU)
	}
	// aes128gcm 的开销也是两边共享的常量
	if mkcpMaskOverhead["mkcp-aes128gcm"] != 28 {
		t.Errorf("aes128gcm 开销 = %d，节点端 udpmask 算的是 28（12 nonce + 16 tag）",
			mkcpMaskOverhead["mkcp-aes128gcm"])
	}
}

// TUIC 入站不该有 udp_over_stream。
//
// 它是客户端侧的 UDP 中继模式选择（QUIC datagram 还是流），服务端两种
// 都收。原先面板允许配、节点端只校验类型从不读取，管理员勾上、保存成功、
// 什么都没发生——配了没用比配不了更误导。
func TestTUICRejectsUDPOverStream(t *testing.T) {
	_, fields := ValidateProtocolConfig("tuic", "pandora-native", 443,
		json.RawMessage(`{"network":"udp","cert_path":"/a.crt","key_path":"/a.key","udp_over_stream":true}`))
	if len(fields) == 0 {
		t.Error("udp_over_stream 应当被拒")
	}
	// zero_rtt 是真实现了的，不能连它一起拒
	_, fields = ValidateProtocolConfig("tuic", "pandora-native", 443,
		json.RawMessage(`{"network":"udp","cert_path":"/a.crt","key_path":"/a.key","zero_rtt":true}`))
	if len(fields) != 0 {
		t.Errorf("zero_rtt 被误拒：%v", fields)
	}
}
