package protocol

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// --- 1. protocol.go API ---

func TestHelperCanonicalType(t *testing.T) {
	cases := map[string]string{
		"":                 "",
		"  ":               "",
		"VLESS":            "vless",
		"  ShadowSocks \t": "shadowsocks",
	}
	for in, want := range cases {
		if got := CanonicalType(in); got != want {
			t.Errorf("CanonicalType(%q)=%q want %q", in, got, want)
		}
	}
}

func TestHelperStableTypesCopyAndCount(t *testing.T) {
	a := StableTypes()
	if len(a) != 13 {
		t.Fatalf("len=%d want 13", len(a))
	}
	if a[0] != stableTypes[0] {
		t.Fatalf("order changed: %q", a[0])
	}
	a[0] = "mutated"
	b := StableTypes()
	if b[0] == "mutated" {
		t.Fatal("StableTypes 应返回副本，修改返回值不应影响下次调用")
	}
	want := make(map[string]struct{})
	for _, s := range Schemas() {
		want[s.NodeType] = struct{}{}
	}
	for _, ty := range b {
		if _, ok := want[ty]; !ok {
			t.Errorf("StableTypes 含 %q 但 Schemas 没有", ty)
		}
	}
	if len(want) != len(b) {
		t.Fatalf("StableTypes 与 Schemas 集合大小不同: %d vs %d", len(b), len(want))
	}
}

func TestHelperSchemasSortedAndMeta(t *testing.T) {
	schemas := Schemas()
	if len(schemas) != 13 {
		t.Fatalf("len=%d want 13", len(schemas))
	}
	for i := 1; i < len(schemas); i++ {
		if schemas[i-1].NodeType >= schemas[i].NodeType {
			t.Fatalf("未按 NodeType 升序: %s >= %s", schemas[i-1].NodeType, schemas[i].NodeType)
		}
	}
	for _, s := range schemas {
		if s.Version != 1 || s.Status != "stable" {
			t.Errorf("%s version=%d status=%s", s.NodeType, s.Version, s.Status)
		}
		allowed := make(map[string]struct{}, len(s.AllowedProperties))
		for _, p := range s.AllowedProperties {
			allowed[p] = struct{}{}
		}
		for _, req := range s.Required {
			if _, ok := allowed[req]; !ok {
				t.Errorf("%s Required %q 不在 AllowedProperties", s.NodeType, req)
			}
		}
	}
}

func TestHelperListenL4Constants(t *testing.T) {
	if ListenTCP != "tcp" || ListenUDP != "udp" || ListenTCPUDP != "tcp+udp" {
		t.Fatalf("ListenTCP=%q ListenUDP=%q ListenTCPUDP=%q", ListenTCP, ListenUDP, ListenTCPUDP)
	}
}

func TestHelperPortClaimLabel(t *testing.T) {
	if got := PortClaimLabel(443, ListenTCP); got != "443/TCP" {
		t.Fatalf("got %q", got)
	}
}

// --- 3. Warnings ---

func TestHelperWarnings(t *testing.T) {
	for _, raw := range []json.RawMessage{
		json.RawMessage(`[]`),
		json.RawMessage(`not json`),
		json.RawMessage(``),
	} {
		if w := Warnings("vmess", raw); w != nil {
			t.Fatalf("raw=%q want nil warnings, got %v", raw, w)
		}
	}
	if w := Warnings("vmess", json.RawMessage(`{"tls":0,"network":"tcp"}`)); len(w) != 1 || w[0] != vmessRawTCPWarning {
		t.Fatalf("vmess xboard tcp: %v", w)
	}
	if w := Warnings("vmess", json.RawMessage(`{}`)); len(w) != 1 {
		t.Fatalf("vmess empty network default tcp: %v", w)
	}
	if w := Warnings("vmess", json.RawMessage(`{"network":"ws"}`)); len(w) != 0 {
		t.Fatalf("vmess ws: %v", w)
	}
	if w := Warnings("vless", json.RawMessage(`{"network":"tcp","tls":0}`)); len(w) != 0 {
		t.Fatalf("vless tcp: %v", w)
	}
}

// --- 4. Redact ---

func TestHelperRedact(t *testing.T) {
	if s := string(Redact(nil)); s != "{}" {
		t.Fatalf("nil: %s", s)
	}
	if s := string(Redact(json.RawMessage(``))); s != "{}" {
		t.Fatalf("empty: %s", s)
	}
	if s := string(Redact(json.RawMessage(`{bad`))); s != "{}" {
		t.Fatalf("bad json: %s", s)
	}
	out := Redact(json.RawMessage(`[{"password":"x"}]`))
	if strings.Contains(string(out), "password") || strings.Contains(string(out), "x") {
		t.Fatalf("array top: %s", out)
	}
	out = Redact(json.RawMessage(`{"keep":"ok","Private_Key":"k","nested":{"token":"t"}}`))
	enc := string(out)
	if !strings.Contains(enc, "ok") || strings.Contains(enc, "Private_Key") || strings.Contains(enc, "token") {
		t.Fatalf("mixed case / nested: %s", enc)
	}
}

// --- 5. PreserveSecrets（与 secrets_test 互补的边界）---

func TestHelperPreserveSecretsIncomingBadOrTypeMismatch(t *testing.T) {
	stored := json.RawMessage(`{"password":"p"}`)
	incoming := json.RawMessage(`{bad`)
	got, err := PreserveSecrets(stored, incoming)
	if err != nil || !bytes.Equal(got, incoming) {
		t.Fatalf("incoming bad: got=%q err=%v", got, err)
	}
	got, err = PreserveSecrets(json.RawMessage(`{"password":"p"}`), json.RawMessage(`[]`))
	if err != nil || !bytes.Equal(got, json.RawMessage(`[]`)) {
		t.Fatalf("stored object incoming array: %q err=%v", got, err)
	}
	// 库里没有敏感键：无可补，原样返回请求字节
	unchanged := json.RawMessage(`{"a":1}`)
	got, err = PreserveSecrets(json.RawMessage(`{"a":2,"b":"x"}`), unchanged)
	if err != nil || !bytes.Equal(got, unchanged) {
		t.Fatalf("nothing to restore should return same bytes: %q", got)
	}
}

// --- 6. GenerateRealityKeypair / decode / check ---

func TestHelperGenerateRealityKeypairDecodeCheck(t *testing.T) {
	priv, pub, err := GenerateRealityKeypair()
	if err != nil {
		t.Fatal(err)
	}
	priv2, _, _ := GenerateRealityKeypair()
	if priv == priv2 {
		t.Error("两次私钥相同")
	}
	pb, err := decodeX25519Key(priv)
	if err != nil || len(pb) != 32 {
		t.Fatalf("decode priv: %v len=%d", err, len(pb))
	}
	if pb[0]&7 != 0 || pb[31]&128 != 0 || pb[31]&64 == 0 {
		t.Errorf("clamping: %08b %08b", pb[0], pb[31])
	}
	if !realityKeysPaired(priv, pub) {
		t.Error("公私钥不配对")
	}
	wrongPub := base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	if realityKeysPaired(priv, wrongPub) {
		t.Error("错误公钥不应配对")
	}
	// 标准 base64 带填充
	std := base64.StdEncoding.EncodeToString(pb)
	if b, err := decodeX25519Key(std); err != nil || len(b) != 32 {
		t.Fatalf("std b64: %v", err)
	}
	if _, err := decodeX25519Key("!!!"); err == nil {
		t.Fatal("非法字符应失败")
	}
	short := base64.RawURLEncoding.EncodeToString([]byte{1, 2, 3})
	if _, err := decodeX25519Key(short); err == nil {
		t.Fatal("长度非 32 应失败")
	}
	if err := checkX25519Key(""); err == nil || !strings.Contains(err.Error(), "必填") {
		t.Fatalf("empty: %v", err)
	}
	if err := checkX25519Key("!!!"); err == nil {
		t.Fatal("非法 base64")
	}
	if err := checkX25519Key(short); err == nil || !strings.Contains(err.Error(), "32") {
		t.Fatalf("short key: %v", err)
	}
}

// --- 7. kernel_config 辅助 ---

func TestHelperIsNumericAsIntAsStringSlice(t *testing.T) {
	for _, v := range []any{float64(1), float32(1), int(1), int64(1), int32(1)} {
		if !isNumeric(v) {
			t.Errorf("isNumeric(%T) false", v)
		}
	}
	for _, v := range []any{"1", true, nil} {
		if isNumeric(v) {
			t.Errorf("isNumeric(%T) true", v)
		}
	}
	if asInt(float64(3)) != 3 || asInt(int(4)) != 4 || asInt(int64(5)) != 5 {
		t.Fatal("asInt numbers")
	}
	if asInt(true) != 1 || asInt(false) != 0 {
		t.Fatal("asInt bool")
	}
	if asInt("x") != 0 {
		t.Fatal("asInt string")
	}
	if len(asStringSlice("")) != 0 || !reflect.DeepEqual(asStringSlice("a"), []string{"a"}) {
		t.Fatal("asStringSlice string")
	}
	if !reflect.DeepEqual(asStringSlice([]string{"a", "b"}), []string{"a", "b"}) {
		t.Fatal("asStringSlice []string")
	}
	if len(asStringSlice([]any{"a", 1})) != 1 || len(asStringSlice(3)) != 0 {
		t.Fatal("asStringSlice mixed/number")
	}
}

func TestHelperFirstStringLowerASCII(t *testing.T) {
	if firstString("", "  ", "ok") != "ok" {
		t.Fatal("firstString")
	}
	if lowerASCII("TCP") != "tcp" {
		t.Fatal("lowerASCII")
	}
}

func TestHelperUnflattenFieldPath(t *testing.T) {
	cases := map[string]string{
		"network_settings_headers_Host": "network_settings.headers.Host",
		"up_mbps":                       "up_mbps",
		"reality_settings_short_id":     "reality_settings.short_id",
	}
	for flat, want := range cases {
		if got := unflattenFieldPath(flat); got != want {
			t.Errorf("%q -> %q want %q", flat, got, want)
		}
	}
}

func TestHelperRenameErrorFieldsToXboard(t *testing.T) {
	empty := map[string]string{}
	if got := renameErrorFieldsToXboard("unknown", empty); len(got) != 0 {
		t.Fatal("unknown protocol")
	}
	fields := map[string]string{
		"protocol_config.short_ids":      "err",
		"protocol_config.fingerprint":    "err2",
		"protocol_config.host":           "err3",
		"protocol_config.unknown_kernel": "err4",
	}
	out := renameErrorFieldsToXboard("vless", fields)
	if out["protocol_config.reality_settings.short_id"] != "err" {
		t.Fatalf("short_ids: %#v", out)
	}
	if out["protocol_config.utls"] != "err2" {
		t.Fatalf("fingerprint: %#v", out)
	}
	// host 逆映射取字典序较小的 xboard 名
	hostKey := "protocol_config.network_settings.headers.Host"
	if out[hostKey] != "err3" && out["protocol_config.network_settings.host"] != "err3" {
		t.Fatalf("host remap: %#v", out)
	}
	if out["protocol_config.unknown_kernel"] != "err4" {
		t.Fatalf("unknown key: %#v", out)
	}
	bare := map[string]string{"short_ids": "m"}
	out2 := renameErrorFieldsToXboard("trojan", bare)
	if out2["reality_settings.short_id"] != "m" {
		t.Fatalf("no prefix: %#v", out2)
	}
}

// --- 8. toKernelConfig 形状 ---

func TestHelperKernelConfigShapeFixups(t *testing.T) {
	// trojan tls=1 无 security
	got := KernelConfig("trojan", map[string]any{"tls": float64(1)})
	if got["security"] != "none" || got["tls"] != true {
		t.Fatalf("trojan tls1: %#v", got)
	}
	got = KernelConfig("trojan", map[string]any{"tls": float64(1), "security": "reality"})
	if got["security"] != "reality" || got["tls"] != true {
		t.Fatalf("trojan tls1 keep security: %#v", got)
	}
	got = KernelConfig("trojan", map[string]any{"tls": float64(2)})
	if got["security"] != "reality" || got["tls"] != false {
		t.Fatalf("trojan tls2: %#v", got)
	}
	got = KernelConfig("vless", map[string]any{"tls": float64(0)})
	if _, has := got["tls"]; has {
		t.Fatalf("vless tls0 should delete tls: %#v", got)
	}
	got = KernelConfig("vless", map[string]any{"tls": float64(2)})
	if _, has := got["tls"]; has {
		t.Fatalf("vless tls2: %#v", got)
	}
	got = KernelConfig("vless", map[string]any{"tls": float64(1)})
	if got["tls"] != float64(1) {
		t.Fatalf("vless tls1 passthrough: %#v", got)
	}
	got = KernelConfig("vless", map[string]any{"tls": true})
	if got["tls"] != true {
		t.Fatalf("vless bool tls: %#v", got)
	}
	got = KernelConfig("vless", map[string]any{
		"reality_settings": map[string]any{"short_id": "ab"},
	})
	ids, _ := got["short_ids"].([]string)
	if len(ids) != 1 || ids[0] != "ab" {
		t.Fatalf("short_id string: %#v", got["short_ids"])
	}
	got = KernelConfig("vless", map[string]any{
		"reality_settings": map[string]any{"short_id": []any{"cd"}},
	})
	if _, ok := got["short_ids"].([]any); !ok {
		t.Fatalf("short_ids array: %#v", got["short_ids"])
	}
	// shadowtls
	got = KernelConfig("shadowtls", map[string]any{"server": "www.example.com", "server_port": float64(8443)})
	if got["server"] != "www.example.com:8443" {
		t.Fatalf("shadowtls fold: %v", got["server"])
	}
	got = KernelConfig("shadowtls", map[string]any{"server": "www.example.com:443"})
	if got["server"] != "www.example.com:443" {
		t.Fatalf("already port: %v", got["server"])
	}
	got = KernelConfig("shadowtls", map[string]any{"server": "2001:db8::1", "server_port": float64(443)})
	if got["server"] != "2001:db8::1" {
		t.Fatalf("ipv6 literal unchanged: %v", got["server"])
	}
	got = KernelConfig("shadowtls", map[string]any{"handshake_server": "www.example.com", "server_port": float64(443)})
	if got["server"] != "www.example.com:443" {
		t.Fatalf("handshake_server fold: %v", got["server"])
	}
	got = KernelConfig("shadowtls", map[string]any{"server": "www.example.com", "server_port": float64(0)})
	if got["server"] != "www.example.com" {
		t.Fatalf("port 0 no fold: %v", got["server"])
	}
	got = KernelConfig("shadowtls", map[string]any{"server": "www.example.com", "server_port": "x"})
	if got["server"] != "www.example.com" {
		t.Fatalf("non-numeric port: %v", got["server"])
	}
	got = KernelConfig("mieru", map[string]any{"transport": "UDP"})
	if got["transport"] != "udp" {
		t.Fatalf("mieru transport: %v", got["transport"])
	}
	got = KernelConfig("naive", map[string]any{"masquerade": map[string]any{"url": "http://example.com"}})
	if _, ok := got["masquerade"].(map[string]any); !ok {
		t.Fatalf("masquerade not flattened: %#v", got)
	}
	got = KernelConfig("socks", map[string]any{"network": "tcp"})
	if got["network"] != "tcp" {
		t.Fatalf("unknown rename only flatten: %#v", got)
	}
	// host 双源：结果应是两者之一（map 遍历顺序不定）
	got = KernelConfig("vless", map[string]any{
		"network_settings": map[string]any{
			"host":    "h1.example.com",
			"headers": map[string]any{"Host": "h2.example.com"},
		},
	})
	h, _ := got["host"].(string)
	if h != "h1.example.com" && h != "h2.example.com" {
		t.Fatalf("host either: %v", got["host"])
	}
}

// --- 9. validate_policy 辅助 ---

func TestHelperNonPublicHostReason(t *testing.T) {
	cases := []struct {
		host string
		want string // 非空表示应有原因（只检查子串或精确）
	}{
		{"", "主机为空"},
		{"127.0.0.1", "回环"},
		{"0.0.0.0", "0.0.0.0"},
		{"::", "::"},
		{"10.0.0.1", "私网"},
		{"100.64.0.1", "私网"},
		{"169.254.1.1", "链路本地"},
		{"fe80::1", "链路本地"},
		{"224.0.0.1", "链路本地"}, // 224.0.0.0/24 是链路本地组播
		{"239.1.1.1", "组播"},
		{"ff02::1", "链路本地"},
		{"ff05::1", "组播"},
		{"::ffff:127.0.0.1", "回环"},
		{"localhost", "localhost"},
		{"a.local", "local"},
		{"x.home.arpa", "home.arpa"},
		{"svc.internal", "internal"},
		{"intranet", "单个标签"},
		{"a_b.example.com", "有效"},
		{"www.example.com.", ""},
		{"www.example.com", ""},
		{"203.0.113.5", ""},
	}
	for _, tc := range cases {
		r := nonPublicHostReason(tc.host)
		if tc.want == "" {
			if r != "" {
				t.Errorf("%q public got %q", tc.host, r)
			}
			continue
		}
		if r == "" {
			t.Errorf("%q want reason containing %q, got empty", tc.host, tc.want)
			continue
		}
		if !strings.Contains(r, tc.want) {
			t.Errorf("%q: reason %q, want it to mention %q", tc.host, r, tc.want)
		}
	}
}

func TestHelperIsLoopbackHost(t *testing.T) {
	if !isLoopbackHost("localhost") || !isLoopbackHost("127.0.0.1") || !isLoopbackHost("::1") {
		t.Fatal("loopback")
	}
	if isLoopbackHost("203.0.113.5") {
		t.Fatal("doc ip not loopback")
	}
}

func TestHelperCertificatePathProblem(t *testing.T) {
	base := nodeCertificateDir + "example.com/fullchain.pem"
	if certificatePathProblem(base) != "" {
		t.Fatalf("valid: %q", certificatePathProblem(base))
	}
	if certificatePathProblem(" "+base) == "" {
		t.Fatal("leading space")
	}
	if certificatePathProblem("relative.pem") == "" {
		t.Fatal("relative")
	}
	if certificatePathProblem(nodeCertificateDir+"../escape.pem") == "" {
		t.Fatal("dot dot")
	}
	if certificatePathProblem(nodeCertificateDir) == "" {
		t.Fatal("dir only")
	}
	if certificatePathProblem(base+"|") == "" {
		t.Fatal("pipe")
	}
}

func TestHelperNumericTLSValue(t *testing.T) {
	v, ok := numericTLSValue(json.RawMessage(`{"tls":1}`))
	if !ok || v != 1 {
		t.Fatalf("1: %d %v", v, ok)
	}
	_, ok = numericTLSValue(json.RawMessage(`{"tls":true}`))
	if ok {
		t.Fatal("bool tls")
	}
	_, ok = numericTLSValue(json.RawMessage(`{}`))
	if ok {
		t.Fatal("missing tls")
	}
	v, ok = numericTLSValue(json.RawMessage(`{"tls":1.5}`))
	if !ok || v != -1 {
		t.Fatalf("1.5: %d %v", v, ok)
	}
	_, ok = numericTLSValue(json.RawMessage(`{`))
	if ok {
		t.Fatal("bad json")
	}
}

func TestHelperNormalizedNetwork(t *testing.T) {
	if normalizedNetwork("") != "tcp" || normalizedNetwork(" GRPC ") != "grpc" {
		t.Fatal("normalizedNetwork")
	}
}

// --- 10. listenL4 补 ---

func TestHelperListenL4Hy2SSNil(t *testing.T) {
	if ListenL4("hy2", nil) != ListenUDP {
		t.Fatal("hy2")
	}
	if ListenL4("ss", json.RawMessage(`{"network":"udp"}`)) != ListenTCPUDP {
		t.Fatal("ss tcp+udp")
	}
	if ListenL4("shadowsocks", nil) != ListenTCPUDP {
		t.Fatal("shadowsocks nil")
	}
	if ListenL4("vless", nil) != ListenTCP {
		t.Fatal("nil raw")
	}
}

func TestHelperSchemasNodeTypesMatchStableTypesLiteral(t *testing.T) {
	fromSchemas := make([]string, 0, len(Schemas()))
	for _, s := range Schemas() {
		fromSchemas = append(fromSchemas, s.NodeType)
	}
	st := StableTypes()
	sort.Strings(st)
	sort.Strings(fromSchemas)
	if !reflect.DeepEqual(st, fromSchemas) {
		t.Fatalf("stableTypes vs Schemas: %v vs %v", st, fromSchemas)
	}
}
