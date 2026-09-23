package nodefabric

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"golang.org/x/crypto/curve25519"
)

// REALITY 配错不会报错，只会安静地退化成一个容易识别的节点 ——
// 探测者拿 dest 的证书一比就露馅。所以这里的重点是「该拦的有没有拦住」，
// 而不是「能配出来就行」。

func realityConfig(over map[string]any) json.RawMessage {
	priv, pub, err := GenerateRealityKeypair()
	if err != nil {
		panic(err)
	}
	cfg := map[string]any{
		"network":      "tcp",
		"security":     "reality",
		"dest":         "www.microsoft.com:443",
		"server_names": []string{"www.microsoft.com"},
		"private_key":  priv,
		"public_key":   pub,
		"short_ids":    []string{"0123abcd"},
	}
	for k, v := range over {
		if v == nil {
			delete(cfg, k)
		} else {
			cfg[k] = v
		}
	}
	raw, _ := json.Marshal(cfg)
	return raw
}

func TestReality_合法配置能过(t *testing.T) {
	ver, fields := ValidateProtocolConfig("vless", "xray-core", 443, realityConfig(nil))
	if len(fields) != 0 {
		t.Fatalf("合法配置被拒: %v", fields)
	}
	if ver != 1 {
		t.Errorf("schema 版本应为 1，得到 %d", ver)
	}
}

func TestReality_拦住配错的情况(t *testing.T) {
	cases := []struct {
		name  string
		over  map[string]any
		field string
	}{
		{"没有 dest", map[string]any{"dest": nil}, "protocol_config.dest"},
		{"dest 不带端口", map[string]any{"dest": "www.microsoft.com"}, "protocol_config.dest"},
		{"dest 用 IP", map[string]any{"dest": "1.1.1.1:443"}, "protocol_config.dest"},
		{"dest 端口越界", map[string]any{"dest": "a.example:70000"}, "protocol_config.dest"},
		{"没有 server_names", map[string]any{"server_names": nil}, "protocol_config.server_names"},
		{"server_names 为空数组", map[string]any{"server_names": []string{}}, "protocol_config.server_names"},
		{"server_names 带协议头", map[string]any{"server_names": []string{"https://a.example"}}, "protocol_config.server_names"},
		{"没有私钥", map[string]any{"private_key": nil}, "protocol_config.private_key"},
		{"私钥不是 base64", map[string]any{"private_key": "这不是密钥"}, "protocol_config.private_key"},
		{"私钥长度不对", map[string]any{"private_key": base64.RawURLEncoding.EncodeToString([]byte("short"))}, "protocol_config.private_key"},
		{"没有公钥", map[string]any{"public_key": nil}, "protocol_config.public_key"},
		{"short_id 不是十六进制", map[string]any{"short_ids": []string{"zzzz"}}, "protocol_config.short_ids"},
		{"short_id 过长", map[string]any{"short_ids": []string{"0123456789abcdef00"}}, "protocol_config.short_ids"},
		{"同时开 tls", map[string]any{"tls": true}, "protocol_config.tls"},
		{"未知的 security", map[string]any{"security": "xtls"}, "protocol_config.security"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, fields := ValidateProtocolConfig("vless", "xray-core", 443, realityConfig(tc.over))
			if _, ok := fields[tc.field]; !ok {
				t.Errorf("没有拦住，期望字段 %s 报错，实际: %v", tc.field, fields)
			}
		})
	}
}

// vmess 走 REALITY 在客户端生态里基本没人支持，允许配置只会让人配出
// 一个连不上的节点，还以为是节点坏了。
func TestReality_vmess不允许(t *testing.T) {
	_, fields := ValidateProtocolConfig("vmess", "xray-core", 443, realityConfig(nil))
	if _, ok := fields["protocol_config.security"]; !ok {
		t.Errorf("vmess 开 reality 应当被拒: %v", fields)
	}
}

// 不开 reality 时，原来那条「TLS 尚未开放」的约束要保持原样。
func TestReality_不影响原有的TLS约束(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{"network": "tcp", "tls": true})
	_, fields := ValidateProtocolConfig("vless", "auto", 443, raw)
	if _, ok := fields["protocol_config.tls"]; !ok {
		t.Errorf("tls=true 仍应被拒: %v", fields)
	}
	raw2, _ := json.Marshal(map[string]any{"network": "tcp", "tls": false})
	if _, fields := ValidateProtocolConfig("vless", "auto", 443, raw2); len(fields) != 0 {
		t.Errorf("tls=false 应当照旧可用: %v", fields)
	}
}

func TestGenerateRealityKeypair(t *testing.T) {
	priv, pub, err := GenerateRealityKeypair()
	if err != nil {
		t.Fatal(err)
	}
	pb, err := base64.RawURLEncoding.DecodeString(priv)
	if err != nil || len(pb) != 32 {
		t.Fatalf("私钥不是 32 字节 base64url: %v / %d", err, len(pb))
	}
	kb, err := base64.RawURLEncoding.DecodeString(pub)
	if err != nil || len(kb) != 32 {
		t.Fatalf("公钥不是 32 字节 base64url: %v / %d", err, len(kb))
	}

	// 公钥必须真的是私钥算出来的。这一条错了，节点起得来、
	// 订阅发得出，用户就是连不上，而且没有任何一处会报错。
	want, err := curve25519.X25519(pb, curve25519.Basepoint)
	if err != nil {
		t.Fatal(err)
	}
	if base64.RawURLEncoding.EncodeToString(want) != pub {
		t.Error("公钥与私钥对不上")
	}

	// clamping 必须做过，否则某些实现算出的共享密钥不一致
	if pb[0]&7 != 0 || pb[31]&128 != 0 || pb[31]&64 == 0 {
		t.Errorf("私钥没有做 RFC 7748 clamping: %08b %08b", pb[0], pb[31])
	}

	// 两次生成不能一样
	priv2, _, _ := GenerateRealityKeypair()
	if priv == priv2 {
		t.Error("两次生成的私钥相同")
	}
}

// 私钥不能出现在管理端的读接口里。
func TestReality_私钥不回显(t *testing.T) {
	raw := realityConfig(nil)
	out := RedactProtocolConfig(raw)
	if strings.Contains(string(out), "private_key") {
		t.Errorf("读接口回显了私钥: %s", out)
	}
	// 公钥要留着 —— 后台得能看到它，才能核对订阅里发的是不是同一个
	if !strings.Contains(string(out), "public_key") {
		t.Errorf("公钥被误删了: %s", out)
	}
}
