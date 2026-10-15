package protocol

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestShadowsocksMethodsIncludeSS2022 钉住 schema 的加密方式名单：shadowsocks
// 是经典 AEAD 加 2022 两种 AES；ShadowTLS 的内层只收经典三种。
func TestShadowsocksMethodsIncludeSS2022(t *testing.T) {
	var ssMethods []string
	var ssHints map[string]string
	var stlsMethods []string
	for _, schema := range Schemas() {
		switch schema.NodeType {
		case "shadowsocks":
			ssMethods = schema.Methods
			ssHints = schema.Hints
		case "shadowtls":
			stlsMethods = schema.Enums["method"]
		}
	}
	want := []string{"aes-128-gcm", "aes-256-gcm", "chacha20-ietf-poly1305", "2022-blake3-aes-128-gcm", "2022-blake3-aes-256-gcm"}
	if len(ssMethods) != len(want) {
		t.Fatalf("shadowsocks Methods=%v, want %v", ssMethods, want)
	}
	for i := range want {
		if ssMethods[i] != want[i] {
			t.Fatalf("shadowsocks Methods=%v, want %v（按顺序）", ssMethods, want)
		}
	}
	wantClassic := []string{"aes-128-gcm", "aes-256-gcm", "chacha20-ietf-poly1305"}
	if len(stlsMethods) != len(wantClassic) {
		t.Fatalf("shadowtls method 枚举=%v, want %v", stlsMethods, wantClassic)
	}
	for i := range wantClassic {
		if stlsMethods[i] != wantClassic[i] {
			t.Fatalf("shadowtls method 枚举=%v, want %v", stlsMethods, wantClassic)
		}
	}
	if hint := ssHints["cipher"]; !strings.Contains(hint, "Shadowsocks 2022") {
		t.Errorf("shadowsocks Hints[cipher]=%q, 应含 Shadowsocks 2022 的说明", hint)
	}
}

// TestValidateAcceptsSS2022 表单形状的 2022 配置必须通过（cipher 翻成 method 再校验）。
func TestValidateAcceptsSS2022(t *testing.T) {
	for _, cipher := range []string{"2022-blake3-aes-128-gcm", "2022-blake3-aes-256-gcm"} {
		version, fields := Validate("shadowsocks", 8388, json.RawMessage(`{"cipher":"`+cipher+`"}`))
		if version != 1 || len(fields) != 0 {
			t.Fatalf("%s：version=%d fields=%v, want version 1 且无字段错误", cipher, version, fields)
		}
	}
}

// TestValidateRejectsSS2022Chacha chacha20 版不开放：表单写入必须被拒。
func TestValidateRejectsSS2022Chacha(t *testing.T) {
	_, fields := Validate("shadowsocks", 8388, json.RawMessage(`{"cipher":"2022-blake3-chacha20-poly1305"}`))
	if _, ok := fields["protocol_config.method"]; !ok {
		t.Fatalf("fields=%v, want protocol_config.method 错误", fields)
	}
}

// TestShadowTLSRejectsSS2022InnerMethod ShadowTLS 的内层只收经典三种，2022 必须被拒。
func TestShadowTLSRejectsSS2022InnerMethod(t *testing.T) {
	base := func(method string) json.RawMessage {
		return json.RawMessage(`{"password":"stls-pass-1","handshake_server":"www.example.com","server_port":443,"method":"` + method + `","version":3}`)
	}
	_, fields := validateKernelShape("shadowtls", 443, base("2022-blake3-aes-128-gcm"))
	if _, ok := fields["protocol_config.method"]; !ok {
		t.Fatalf("fields=%v, want protocol_config.method 错误", fields)
	}
	_, fields = validateKernelShape("shadowtls", 443, base("aes-128-gcm"))
	if len(fields) != 0 {
		t.Fatalf("经典方法被拒：fields=%v", fields)
	}
}

// TestSS2022KeyLen 密钥长度按 SIP022：aes-128 是 16 字节、aes-256 是 32 字节。
func TestSS2022KeyLen(t *testing.T) {
	if n, ok := SS2022KeyLen("2022-blake3-aes-128-gcm"); !ok || n != 16 {
		t.Errorf("aes-128: n=%d ok=%v, want 16 true", n, ok)
	}
	if n, ok := SS2022KeyLen("2022-blake3-aes-256-gcm"); !ok || n != 32 {
		t.Errorf("aes-256: n=%d ok=%v, want 32 true", n, ok)
	}
	if _, ok := SS2022KeyLen("2022-blake3-chacha20-poly1305"); ok {
		t.Error("chacha20 2022 未开放，不该返回 true")
	}
	if _, ok := SS2022KeyLen("aes-128-gcm"); ok {
		t.Error("经典 aes-128-gcm 不是 2022 方法，不该返回 true")
	}
}
