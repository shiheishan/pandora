package service

import (
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/module/node/model"
	"github.com/aegispanel/aegis/internal/pkg/errs"
)

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
