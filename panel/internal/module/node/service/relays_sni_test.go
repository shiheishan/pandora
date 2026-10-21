package service

import (
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/module/node/model"
	"github.com/aegispanel/aegis/internal/module/node/repo"
	"github.com/aegispanel/aegis/internal/module/subscription/render"
	"github.com/aegispanel/aegis/internal/pkg/errs"
)

func ptr[T any](v T) *T {
	return &v
}

func relaySNITestNode(nodeType, host, cfg string) model.Node {
	return model.Node{NodeType: &nodeType, ServerHost: &host, ProtocolConfig: cfg}
}

func TestRelayCheckSNIReality(t *testing.T) {
	parent := relaySNITestNode("vless", "203.0.113.10",
		`{"tls":2,"network":"tcp","reality_settings":{"public_key":"pk","short_id":["0a1b"],"server_name":["a.example.test","b.example.test"],"dest":"a.example.test:443"}}`)

	ok := "b.example.test"
	if err := relayCheckSNI(parent, &ok); err != nil {
		t.Fatalf("server_names 里的 SNI 应通过: %v", err)
	}
	if err := relayCheckSNI(parent, nil); err != nil {
		t.Fatalf("不覆盖 SNI 应通过: %v", err)
	}
	bad := "other.example.test"
	err := relayCheckSNI(parent, &bad)
	if err == nil {
		t.Fatal("不在 server_names 里的 SNI 应被拒")
	}
	if ee := errs.As(err); ee.Code != errs.ValidationFailed || ee.Fields["sni"] == "" {
		t.Fatalf("不在 server_names 里的 SNI 应 422 sni，got %v", err)
	}
	if hint := relaySNIHint(parent); !strings.Contains(hint, "REALITY") || !strings.Contains(hint, "a.example.test") {
		t.Fatalf("REALITY 提示 = %q", hint)
	}
}

func TestRelaySNIHintIPParent(t *testing.T) {
	cases := []struct {
		name     string
		node     model.Node
		wantHint bool
	}{
		{"IP 地址 + TLS + 没配 server_name", relaySNITestNode("trojan", "203.0.113.11",
			`{"network":"tcp","tls":1,"cert_path":"/etc/x/fullchain.pem","key_path":"/etc/x/privkey.pem"}`), true},
		{"IP 地址 + TLS + 配了 server_name", relaySNITestNode("trojan", "203.0.113.11",
			`{"network":"tcp","tls":1,"cert_path":"/etc/x/fullchain.pem","key_path":"/etc/x/privkey.pem","server_name":"node.example.test"}`), false},
		{"域名地址 + TLS + 没配 server_name", relaySNITestNode("trojan", "node.example.test",
			`{"network":"tcp","tls":1,"cert_path":"/etc/x/fullchain.pem","key_path":"/etc/x/privkey.pem"}`), false},
		{"IP 地址 + 不开 TLS", relaySNITestNode("shadowsocks", "203.0.113.11",
			`{"cipher":"aes-128-gcm"}`), false},
	}
	for _, c := range cases {
		hint := relaySNIHint(c.node)
		if (hint != "") != c.wantHint {
			t.Errorf("%s: hint = %q, want hint=%v", c.name, hint, c.wantHint)
		}
		// 非 REALITY 父节点不限制 SNI 覆盖
		sni := "relay.example.test"
		if err := relayCheckSNI(c.node, &sni); err != nil {
			t.Errorf("%s: 非 REALITY 父节点的 SNI 覆盖应通过: %v", c.name, err)
		}
	}
}

func TestRelayCheckSNIACME(t *testing.T) {
	// acme 父节点（hysteria2 {"cert_mode":"acme"}，ServerHost "192.0.2.10"）
	parent := relaySNITestNode("hysteria2", "192.0.2.10", `{"cert_mode":"acme"}`)

	if err := relayCheckSNI(parent, ptr("192.0.2.10")); err != nil {
		t.Fatalf("acme 标识符匹配的 SNI 应通过: %v", err)
	}

	bad := "other.example.com"
	err := relayCheckSNI(parent, &bad)
	if err == nil {
		t.Fatal("不匹配 acme 标识符的 SNI 应被拒")
	}
	if ee := errs.As(err); ee == nil || ee.Code != errs.ValidationFailed || ee.Fields["sni"] == "" {
		t.Fatalf("不匹配 acme 标识符的 SNI 应 422 sni，got %v", err)
	}

	if err := relayCheckSNI(parent, nil); err != nil {
		t.Fatalf("nil SNI 应通过: %v", err)
	}

	if hint := relaySNIHint(parent); hint != "" {
		t.Fatalf("acme 父节点的 relaySNIHint 应为空，got %q", hint)
	}

	// acme 父节点 ServerHost "node1.example.com"、配置带 SNI "sni.example.com"
	parentWithSNI := relaySNITestNode("hysteria2", "node1.example.com",
		`{"cert_mode":"acme","tls_settings":{"server_name":"sni.example.com"}}`)

	if err := relayCheckSNI(parentWithSNI, ptr("SNI.example.com")); err != nil {
		t.Fatalf("规整后相等的 SNI 应通过: %v", err)
	}

	badHost := "node1.example.com"
	err = relayCheckSNI(parentWithSNI, &badHost)
	if err == nil {
		t.Fatal("填 node1.example.com 而非 sni.example.com 应被拒")
	}
	if ee := errs.As(err); ee == nil || ee.Code != errs.ValidationFailed || ee.Fields["sni"] == "" {
		t.Fatalf("应 422 sni，got %v", err)
	}
}

func TestRelayRenderNodeACME(t *testing.T) {
	rel := repo.DeliveryRelay{
		Name: "中转 01",
		Host: "198.51.100.1",
		Port: 8443,
	}

	// 父节点 render.Node{Type:"hysteria2", Host:"192.0.2.10", Config: map{"cert_mode":"acme"}}，中转没 sni → server_name=="192.0.2.10"
	parentACME := render.Node{
		Type:   "hysteria2",
		Host:   "192.0.2.10",
		Config: map[string]any{"cert_mode": "acme"},
	}
	outACME := relayRenderNode(parentACME, rel)
	if outACME.Config["server_name"] != "192.0.2.10" {
		t.Fatalf("acme 父节点中转 server_name = %v, want 192.0.2.10", outACME.Config["server_name"])
	}

	// 同样的 file 父节点（Config 带 cert_path）→ 没有 server_name，维持原行为
	parentFile := render.Node{
		Type:   "hysteria2",
		Host:   "192.0.2.10",
		Config: map[string]any{"cert_mode": "file", "cert_path": "/etc/pandora-native/certs/a/fullchain.pem"},
	}
	outFile := relayRenderNode(parentFile, rel)
	if sn, ok := outFile.Config["server_name"]; ok && sn != nil && sn != "" {
		t.Fatalf("file 父节点中转不应有 server_name，实际 %v", sn)
	}
}
