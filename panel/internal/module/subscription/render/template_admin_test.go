package render

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// 1. 内置模板与不带模板逐字节相同：对 clash、clash-premium、stash 三种，
// RenderRequest(Request{Format: f, Nodes: loadNodes(t), ProxyUUID: fixtureUUID, Template: defaultTemplateFor(f, Options{})})
// 与 renderPlain(f, loadNodes(t), fixtureUUID, "", Options{}) 的 body 完全相同；Nodes 为 nil 时同样相同。
func TestDefaultTemplateByteForByteWithNoTemplate(t *testing.T) {
	formats := []Format{FormatClash, FormatClashPremium, FormatStash}
	for _, f := range formats {
		t.Run(string(f), func(t *testing.T) {
			for _, nodes := range [][]Node{loadNodes(t), nil} {
				withTmpl, ct1, count1 := RenderRequest(Request{
					Format:    f,
					Nodes:     nodes,
					ProxyUUID: fixtureUUID,
					Template:  defaultTemplateFor(f, Options{}),
				})
				withoutTmpl, ct2, count2 := renderPlain(f, nodes, fixtureUUID, "", Options{})
				if !bytes.Equal(withTmpl, withoutTmpl) {
					t.Fatalf("%s nodes=%d: with default template diff from without template:\nwith:\n%s\nwithout:\n%s",
						f, len(nodes), withTmpl, withoutTmpl)
				}
				if ct1 != ct2 {
					t.Errorf("content-type mismatch: %q vs %q", ct1, ct2)
				}
				if count1 != count2 {
					t.Errorf("count mismatch: %d vs %d", count1, count2)
				}
			}
		})
	}
}

// 2. sing-box 自定义模板：用 defaultTemplateFor(FormatSingbox, Options{}) 当 Template 渲染 loadGroup(t, groupForm)：
// JSON 能解析；outbounds 里有 节点选择、自动选择、direct 与节点 tag；没有节点时也能解析，节点选择是 ["direct"] 的 selector。
// 用 assertSingboxReferencesResolve 核对引用都能解析。
func TestSingboxCustomTemplate(t *testing.T) {
	tmpl := defaultTemplateFor(FormatSingbox, Options{})

	// 有节点时
	nodes := loadGroup(t, groupForm)
	body, _, count := RenderRequest(Request{
		Format:    FormatSingbox,
		Nodes:     nodes,
		ProxyUUID: fixtureUUID,
		Template:  tmpl,
	})
	if count == 0 {
		t.Fatal("expected > 0 nodes for sing-box custom template")
	}
	var doc singboxTemplateDoc
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("singbox custom template json unmarshal failed: %v\n%s", err, body)
	}
	assertSingboxReferencesResolve(t, doc, string(body))

	outboundTags := map[string]bool{}
	for _, o := range doc.Outbounds {
		if tag, ok := o["tag"].(string); ok {
			outboundTags[tag] = true
		}
	}
	for _, expected := range []string{"节点选择", "自动选择", "direct"} {
		if !outboundTags[expected] {
			t.Errorf("singbox custom template missing outbound %q", expected)
		}
	}
	foundNode := false
	for _, n := range nodes {
		if outboundTags[n.Name] {
			foundNode = true
			break
		}
	}
	if !foundNode {
		t.Error("singbox custom template outbounds did not contain any node tag")
	}

	// 没有节点时
	bodyEmpty, _, countEmpty := RenderRequest(Request{
		Format:    FormatSingbox,
		Nodes:     nil,
		ProxyUUID: fixtureUUID,
		Template:  tmpl,
	})
	if countEmpty != 0 {
		t.Fatalf("expected count 0, got %d", countEmpty)
	}
	var docEmpty singboxTemplateDoc
	if err := json.Unmarshal(bodyEmpty, &docEmpty); err != nil {
		t.Fatalf("singbox custom template empty nodes unmarshal failed: %v\n%s", err, bodyEmpty)
	}
	assertSingboxReferencesResolve(t, docEmpty, string(bodyEmpty))

	var selectorOutbounds []any
	for _, o := range docEmpty.Outbounds {
		if o["tag"] == "节点选择" {
			if outs, ok := o["outbounds"].([]any); ok {
				selectorOutbounds = outs
			}
		}
	}
	if len(selectorOutbounds) != 1 || selectorOutbounds[0] != "direct" {
		t.Fatalf("expected 节点选择 selector outbounds to be [\"direct\"], got %v", selectorOutbounds)
	}
}

// 3. {{proxy_names}}：Clash 自定义模板在 {{proxy_groups}} 下一行加
//   - {name: 媒体, type: select, proxies: [节点选择, {{proxy_names}}]}，输出含节点名；没有节点时是 [节点选择, DIRECT]。
func TestClashCustomTemplateProxyNames(t *testing.T) {
	customTmpl := strings.Replace(
		defaultClashTemplate,
		"{{proxy_groups}}",
		"{{proxy_groups}}\n  - {name: 媒体, type: select, proxies: [节点选择, {{proxy_names}}]}",
		1,
	)

	// 有节点
	body, _, count := RenderRequest(Request{
		Format:    FormatClash,
		Nodes:     sampleNodes(),
		ProxyUUID: fixtureUUID,
		Template:  customTmpl,
	})
	if count == 0 {
		t.Fatal("expected > 0 nodes")
	}
	text := string(body)
	if !strings.Contains(text, "name: 媒体") || !strings.Contains(text, `"示例 · 香港 01"`) {
		t.Fatalf("custom template output missing node name in 媒体 group:\n%s", text)
	}

	// 没有节点
	bodyEmpty, _, countEmpty := RenderRequest(Request{
		Format:    FormatClash,
		Nodes:     nil,
		ProxyUUID: fixtureUUID,
		Template:  customTmpl,
	})
	if countEmpty != 0 {
		t.Fatalf("expected count 0, got %d", countEmpty)
	}
	textEmpty := string(bodyEmpty)
	if !strings.Contains(textEmpty, "proxies: [节点选择, DIRECT]") {
		t.Fatalf("expected [节点选择, DIRECT] when no nodes, got:\n%s", textEmpty)
	}
}

// 4. 信息节点：Clash InfoLines: []string{"剩余流量：1.00 GB"} 时，该名字出现在 节点选择 组、不在 自动选择 组，
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

// defaultTemplateFor 返回内置模板原文，测试里当自定义模板用。
func defaultTemplateFor(f Format, opt Options) string {
	switch f {
	case FormatClash, FormatClashPremium, FormatStash:
		return defaultClashTemplate
	case FormatSurfboard:
		return defaultSurfboardTemplate
	case FormatLoon:
		return defaultLoonTemplate
	case FormatSingbox:
		return singboxDefaultTemplate(opt)
	default:
		return ""
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

// singboxDefaultTemplate 按内置框架拼出 sing-box 模板原文（兼容写法），测试里当自定义模板用。
// 生产拉取不走这里：内置渲染直接用 singboxFrame，自定义模板对所有版本原样下发。
func singboxDefaultTemplate(opt Options) string {
	frame := singboxFrame(nil, "节点选择", "自动选择", singboxDialectCompat, opt.Singbox)
	frame["outbounds"] = []string{"{{outbounds}}"}
	b, err := json.MarshalIndent(frame, "", "  ")
	if err != nil {
		return ""
	}
	return string(b) + "\n"
}
