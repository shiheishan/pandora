package protocol

import (
	"encoding/json"
	"net"
	"testing"
)

const testCertPath = "/etc/pandora-native/certs/a/fullchain.pem"
const testKeyPath = "/etc/pandora-native/certs/a/privkey.pem"

func TestUsesCertificateAndCertMode(t *testing.T) {
	h2Kernel := toKernelConfig("hysteria2", map[string]any{"cert_mode": "acme"})
	if !UsesCertificate("hysteria2", h2Kernel) {
		t.Fatal("hysteria2 should use certificate")
	}
	if CertMode("hysteria2", h2Kernel) != CertModeACME {
		t.Fatalf("cert mode %q want acme", CertMode("hysteria2", h2Kernel))
	}
	h2Plain := toKernelConfig("hysteria2", map[string]any{})
	if CertMode("hysteria2", h2Plain) != CertModeFile {
		t.Fatalf("default cert mode %q want file", CertMode("hysteria2", h2Plain))
	}
	trojanTLS := toKernelConfig("trojan", map[string]any{"tls": 1})
	if !UsesCertificate("trojan", trojanTLS) || !trojanTLS["tls"].(bool) {
		t.Fatalf("trojan tls=1 kernel %#v", trojanTLS)
	}
	trojanREALITY := toKernelConfig("trojan", map[string]any{"tls": 2})
	if UsesCertificate("trojan", trojanREALITY) {
		t.Fatal("trojan reality should not use file/acme cert")
	}
	socksOff := toKernelConfig("socks", map[string]any{"tls": false})
	if UsesCertificate("socks", socksOff) {
		t.Fatal("socks tls false")
	}
	if UsesCertificate("vless", map[string]any{"tls": true}) {
		t.Fatal("vless should not use certificate in this layer")
	}
}

func TestNormalizeCertIdentifier(t *testing.T) {
	if got := NormalizeCertIdentifier(" Node1.Example.COM. "); got != "node1.example.com" {
		t.Fatalf("domain got %q", got)
	}
	if got := NormalizeCertIdentifier("[2001:DB8::1]"); got != "2001:db8::1" {
		t.Fatalf("ipv6 got %q", got)
	}
	// Go 的 net.ParseIP 会把 192.0.2.010 解析成 192.0.2.10（前导零按十进制段处理）。
	if got := NormalizeCertIdentifier("192.0.2.010"); got != net.ParseIP("192.0.2.010").String() {
		t.Fatalf("dotted decimal got %q", got)
	}
}

func TestCertIdentifierProblem(t *testing.T) {
	ok := []string{"node1.example.com", "192.0.2.10", "2001:db8::10"}
	for _, id := range ok {
		if p := CertIdentifierProblem(id); p != "" {
			t.Fatalf("%q: %q", id, p)
		}
	}
	bad := []string{"", "10.0.0.1", "127.0.0.1", "100.64.1.1", "fe80::1", "*.example.com", "localhost", "node", "node.local", "1.2.3"}
	for _, id := range bad {
		if p := CertIdentifierProblem(id); p == "" {
			t.Fatalf("%q should fail", id)
		}
	}
}

func TestCertIdentifier(t *testing.T) {
	kernel := map[string]any{"server_name": " Node.EXAMPLE.com "}
	if CertIdentifier(kernel, "10.0.0.1") != "node.example.com" {
		t.Fatalf("SNI wins")
	}
	if CertIdentifier(map[string]any{}, "192.0.2.5") != "192.0.2.5" {
		t.Fatal("host fallback")
	}
}

func TestValidateCertIdentity(t *testing.T) {
	raw := json.RawMessage(`{"cert_mode":"acme"}`)
	if f := ValidateCertIdentity("hysteria2", "10.0.0.5", raw); f["server_host"] == "" {
		t.Fatalf("want server_host error, got %#v", f)
	}
	rawSNI := json.RawMessage(`{"cert_mode":"acme","tls_settings":{"server_name":"bad_name"}}`)
	if f := ValidateCertIdentity("hysteria2", "192.0.2.1", rawSNI); f["protocol_config.tls_settings.server_name"] == "" {
		t.Fatalf("want SNI error, got %#v", f)
	}
	rawFile := json.RawMessage(`{"cert_mode":"file","cert_path":"` + testCertPath + `","key_path":"` + testKeyPath + `"}`)
	if f := ValidateCertIdentity("hysteria2", "10.0.0.5", rawFile); len(f) != 0 {
		t.Fatalf("file mode: %#v", f)
	}
	if f := ValidateCertIdentity("hysteria2", "", raw); len(f) != 0 {
		t.Fatalf("draft empty host: %#v", f)
	}
}

func TestValidateCertModeMatrix(t *testing.T) {
	port := 443
	assertField := func(t *testing.T, nodeType string, raw string, field string, wantPresent bool) {
		_, fields := Validate(nodeType, port, json.RawMessage(raw))
		ok := fields[field] != ""
		if wantPresent != ok {
			t.Fatalf("%s %s field %q present=%v fields=%#v", nodeType, raw, field, ok, fields)
		}
	}
	assertOK := func(t *testing.T, nodeType string, raw string) {
		v, fields := Validate(nodeType, port, json.RawMessage(raw))
		if v != 1 || len(fields) != 0 {
			t.Fatalf("%s %s version=%d fields=%#v", nodeType, raw, v, fields)
		}
	}

	t.Run("hysteria2 acme", func(t *testing.T) { assertOK(t, "hysteria2", `{"cert_mode":"acme"}`) })
	t.Run("hysteria2 file missing cert", func(t *testing.T) {
		assertField(t, "hysteria2", `{}`, "protocol_config.cert_path", true)
	})
	t.Run("hysteria2 acme with paths", func(t *testing.T) {
		assertField(t, "hysteria2", `{"cert_mode":"acme","cert_path":"`+testCertPath+`","key_path":"`+testKeyPath+`"}`, "protocol_config.cert_path", true)
	})
	t.Run("hysteria2 bogus mode", func(t *testing.T) {
		assertField(t, "hysteria2", `{"cert_mode":"bogus"}`, "protocol_config.cert_mode", true)
	})
	t.Run("hysteria2 allow_insecure acme", func(t *testing.T) {
		assertField(t, "hysteria2", `{"cert_mode":"acme","tls_settings":{"allow_insecure":true}}`, "protocol_config.tls_settings.allow_insecure", true)
	})
	t.Run("hysteria2 allow_insecure false", func(t *testing.T) {
		assertOK(t, "hysteria2", `{"cert_mode":"acme","tls_settings":{"allow_insecure":false}}`)
	})
	t.Run("anytls acme", func(t *testing.T) {
		assertOK(t, "anytls", `{"cert_mode":"acme","tls":true}`)
	})
	t.Run("naive acme", func(t *testing.T) {
		assertOK(t, "naive", `{"cert_mode":"acme","tls":true}`)
	})
	t.Run("trojan acme", func(t *testing.T) {
		assertOK(t, "trojan", `{"tls":1,"cert_mode":"acme"}`)
	})
	t.Run("socks acme", func(t *testing.T) {
		assertOK(t, "socks", `{"tls":true,"cert_mode":"acme"}`)
	})
	t.Run("tuic acme", func(t *testing.T) {
		assertOK(t, "tuic", `{"cert_mode":"acme","network":"udp"}`)
	})
	t.Run("juicity acme", func(t *testing.T) {
		assertOK(t, "juicity", `{"cert_mode":"acme"}`)
	})
	t.Run("vless cert_mode rejected", func(t *testing.T) {
		_, fields := Validate("vless", port, json.RawMessage(`{"cert_mode":"acme","network":"ws","tls":0}`))
		if fields["protocol_config"] == "" && fields["protocol_config.cert_mode"] == "" {
			t.Fatalf("vless should reject cert_mode, fields=%#v", fields)
		}
	})
}
