package render

import (
	"strings"
	"testing"
)

func TestRenderLoonShadowsocksLine(t *testing.T) {
	n := nodeByName(t, "ss-aes128")
	body, ct, count := RenderRequest(Request{
		Format:    FormatLoon,
		Nodes:     []Node{n},
		ProxyUUID: fixtureUUID,
	})
	want := "ss-aes128 = Shadowsocks,node.example.com,8388,aes-128-gcm,\"" + fixtureUUID + "\",udp=true"
	if strings.TrimSpace(string(body)) != want {
		t.Fatalf("loon ss line\n  want: %s\n  got:  %s", want, strings.TrimSpace(string(body)))
	}
	if ct != "text/plain; charset=utf-8" {
		t.Fatalf("content-type: %s", ct)
	}
	if count != 1 {
		t.Fatalf("count: %d", count)
	}
}

func TestRenderLoonAllSkippedCountZero(t *testing.T) {
	n := nodeByName(t, "vless-grpc")
	_, _, count := RenderRequest(Request{
		Format:    FormatLoon,
		Nodes:     []Node{n},
		ProxyUUID: fixtureUUID,
	})
	if count != 0 {
		t.Fatalf("count: %d", count)
	}
}

func TestRenderSurfboardDefaultTemplate(t *testing.T) {
	n := nodeByName(t, "ss-aes128")
	body, ct, count := RenderRequest(Request{
		Format:       FormatSurfboard,
		Nodes:        []Node{n},
		ProxyUUID:    fixtureUUID,
		SubscribeURL: "https://sub.example.com/p/t",
	})
	s := string(body)
	if ct != "text/plain; charset=utf-8" {
		t.Fatalf("content-type: %s", ct)
	}
	if count != 1 {
		t.Fatalf("count: %d", count)
	}
	for _, part := range []string{
		"#!MANAGED-CONFIG https://sub.example.com/p/t interval=43200 strict=false",
		"[Proxy]",
		"ss-aes128 = ss, node.example.com, 8388",
		"自动选择 = url-test, ",
		"节点选择 = select, 自动选择, ",
	} {
		if !strings.Contains(s, part) {
			t.Fatalf("missing %q in:\n%s", part, s)
		}
	}
}

func TestRenderSurfboardNoSubscribeURL(t *testing.T) {
	n := nodeByName(t, "ss-aes128")
	body, _, _ := RenderRequest(Request{
		Format:    FormatSurfboard,
		Nodes:     []Node{n},
		ProxyUUID: fixtureUUID,
	})
	if strings.Contains(string(body), "#!MANAGED-CONFIG") {
		t.Fatalf("should not contain MANAGED-CONFIG: %s", string(body))
	}
}

func TestRenderSurfboardNoNodesSelectDirect(t *testing.T) {
	n := nodeByName(t, "vless-grpc")
	body, _, _ := RenderRequest(Request{
		Format:    FormatSurfboard,
		Nodes:     []Node{n},
		ProxyUUID: fixtureUUID,
	})
	if !strings.Contains(string(body), "节点选择 = select, DIRECT") {
		t.Fatalf("missing select DIRECT: %s", string(body))
	}
}

func TestRenderSurfboardInfoLinesInSelectOnly(t *testing.T) {
	n := nodeByName(t, "ss-aes128")
	body, _, _ := RenderRequest(Request{
		Format:    FormatSurfboard,
		Nodes:     []Node{n},
		ProxyUUID: fixtureUUID,
		InfoLines: []string{"剩余流量：1.00 GB", "套餐到期：2026-12-31"},
	})
	s := string(body)
	if !strings.Contains(s, "节点选择 = select, 自动选择, 剩余流量：1.00 GB, 套餐到期：2026-12-31, ss-aes128") {
		t.Fatalf("节点选择 line: %s", s)
	}
	autoIdx := strings.Index(s, "自动选择 = url-test")
	if autoIdx < 0 {
		t.Fatal("no auto line")
	}
	autoLine := s[autoIdx:]
	if nl := strings.Index(autoLine, "\n"); nl >= 0 {
		autoLine = autoLine[:nl]
	}
	if strings.Contains(autoLine, "剩余流量") || strings.Contains(autoLine, "套餐到期") {
		t.Fatalf("info names in auto line: %s", autoLine)
	}
}

func TestRenderSurfboardInfoOmittedWhenAllRealSkipped(t *testing.T) {
	n := nodeByName(t, "vless-grpc")
	body, _, _ := RenderRequest(Request{
		Format:    FormatSurfboard,
		Nodes:     []Node{n},
		ProxyUUID: fixtureUUID,
		InfoLines: []string{"剩余流量：1.00 GB"},
	})
	if strings.Contains(string(body), "剩余流量：1.00 GB") {
		t.Fatalf("should omit info node: %s", string(body))
	}
}

func TestLineSafeNameInLoonSurfboard(t *testing.T) {
	n := Node{
		Name: "a=b,c", Type: "shadowsocks", Host: "node.example.com", Port: 8388,
		Config: map[string]any{"method": "aes-128-gcm"},
	}
	body, _, _ := RenderRequest(Request{
		Format:    FormatLoon,
		Nodes:     []Node{n},
		ProxyUUID: fixtureUUID,
	})
	if !strings.HasPrefix(string(body), "a b c = Shadowsocks,") {
		t.Fatalf("loon name: %s", string(body))
	}
	body2, _, _ := RenderRequest(Request{
		Format:    FormatSurfboard,
		Nodes:     []Node{n},
		ProxyUUID: fixtureUUID,
	})
	if !strings.Contains(string(body2), "a b c = ss,") {
		t.Fatalf("surfboard name: %s", string(body2))
	}
}

func TestSsNodeUDPInOutput(t *testing.T) {
	n := nodeByName(t, "ss-aes128")
	loon, _, _ := RenderRequest(Request{Format: FormatLoon, Nodes: []Node{n}, ProxyUUID: fixtureUUID})
	if !strings.Contains(string(loon), "udp=true") {
		t.Fatalf("loon: %s", string(loon))
	}
	sb, _, _ := RenderRequest(Request{Format: FormatSurfboard, Nodes: []Node{n}, ProxyUUID: fixtureUUID})
	if !strings.Contains(string(sb), "udp-relay=true") {
		t.Fatalf("surfboard: %s", string(sb))
	}
}
