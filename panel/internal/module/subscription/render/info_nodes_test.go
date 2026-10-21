package render

import (
	"encoding/json"
	"strings"
	"testing"
)

// 信息节点：Clash InfoLines: []string{"剩余流量：1.00 GB"} 时，该名字出现在 节点选择 组、不在 自动选择 组，
// 返回的节点数不含它；节点全被跳过（只给一个 naive 节点）时输出里没有信息节点。sing-box 同理（selector 含、urltest 不含）。
func TestInfoNodes(t *testing.T) {
	infoLines := []string{"剩余流量：1.00 GB"}

	// Clash 有节点
	body, _, count := RenderRequest(Request{
		Format:    FormatClash,
		Nodes:     sampleNodes(),
		ProxyUUID: fixtureUUID,
		InfoLines: infoLines,
	})
	if count != len(sampleNodes()) {
		t.Fatalf("expected count %d, got %d", len(sampleNodes()), count)
	}
	text := string(body)
	if !strings.Contains(text, `[自动选择, "剩余流量：1.00 GB"`) {
		t.Fatalf("info line missing from 节点选择 group:\n%s", text)
	}
	if strings.Contains(text, `自动选择, type: url-test, url: 'http://www.gstatic.com/generate_204', interval: 300, proxies: ["剩余流量：1.00 GB"`) {
		t.Fatalf("info line must not appear in 自动选择 group:\n%s", text)
	}

	// Clash 节点全被跳过（只给一个 naive 节点，Clash Meta/Premium 都不支持 naive）
	skippedNodes := []Node{{
		Name:   "naive-node",
		Type:   "naive",
		Host:   fixtureHost,
		Port:   443,
		Config: map[string]any{"insecure": false},
	}}
	bodySkipped, _, countSkipped := RenderRequest(Request{
		Format:    FormatClash,
		Nodes:     skippedNodes,
		ProxyUUID: fixtureUUID,
		InfoLines: infoLines,
	})
	if countSkipped != 0 {
		t.Fatalf("expected count 0, got %d", countSkipped)
	}
	if strings.Contains(string(bodySkipped), "剩余流量") {
		t.Fatalf("info node should not appear when all real nodes are skipped:\n%s", bodySkipped)
	}

	// sing-box 有节点
	bodySB, _, countSB := RenderRequest(Request{
		Format:    FormatSingbox,
		Nodes:     sampleNodes(),
		ProxyUUID: fixtureUUID,
		InfoLines: infoLines,
	})
	if countSB != len(sampleNodes()) {
		t.Fatalf("sing-box count = %d, want %d", countSB, len(sampleNodes()))
	}
	var docSB singboxTemplateDoc
	if err := json.Unmarshal(bodySB, &docSB); err != nil {
		t.Fatalf("singbox unmarshal failed: %v", err)
	}
	for _, o := range docSB.Outbounds {
		if o["tag"] == "节点选择" {
			outs := o["outbounds"].([]any)
			found := false
			for _, item := range outs {
				if item == "剩余流量：1.00 GB" {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("singbox 节点选择 missing info node: %v", outs)
			}
		}
		if o["tag"] == "自动选择" {
			outs := o["outbounds"].([]any)
			for _, item := range outs {
				if item == "剩余流量：1.00 GB" {
					t.Fatalf("singbox 自动选择 must not contain info node: %v", outs)
				}
			}
		}
	}

	// sing-box 节点全被跳过（只给一个 mieru 节点，sing-box 不支持 mieru）
	skippedMieru := []Node{{
		Name:   "mieru-node",
		Type:   "mieru",
		Host:   fixtureHost,
		Port:   3000,
		Config: map[string]any{},
	}}
	bodySBSkipped, _, countSBSkipped := RenderRequest(Request{
		Format:    FormatSingbox,
		Nodes:     skippedMieru,
		ProxyUUID: fixtureUUID,
		InfoLines: infoLines,
	})
	if countSBSkipped != 0 {
		t.Fatalf("expected count 0, got %d", countSBSkipped)
	}
	if strings.Contains(string(bodySBSkipped), "剩余流量") {
		t.Fatalf("singbox info node should not appear when all real nodes are skipped:\n%s", bodySBSkipped)
	}
}

func sampleNodes() []Node {
	return []Node{
		{
			Name: "示例 · 香港 01",
			Type: "shadowsocks",
			Host: "hk.example.com",
			Port: 8388,
			Config: map[string]any{
				"method": "aes-128-gcm",
			},
		},
		{
			Name: "示例 · 日本 01",
			Type: "vmess",
			Host: "jp.example.com",
			Port: 443,
			Config: map[string]any{
				"network":     "ws",
				"tls":         true,
				"server_name": "jp.example.com",
				"ws_path":     "/ws",
			},
		},
		{
			Name: "示例 · 美国 01",
			Type: "trojan",
			Host: "us.example.com",
			Port: 443,
			Config: map[string]any{
				"server_name": "us.example.com",
			},
		},
		{
			Name: "示例 · 新加坡 01",
			Type: "hysteria2",
			Host: "sg.example.com",
			Port: 443,
			Config: map[string]any{
				"server_name": "sg.example.com",
			},
		},
	}
}
