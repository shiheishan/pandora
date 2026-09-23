package subscription

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func TestSupportedRenderersProduceAllAdvertisedFormats(t *testing.T) {
	const credential = "019f9f00-1111-7222-8333-444444444444"
	tests := []struct {
		name        string
		nodeType    string
		config      map[string]any
		clashType   string
		singboxType string
		uriPrefix   string
	}{
		{"shadowsocks", "shadowsocks", map[string]any{"method": "aes-256-gcm"}, "ss", "shadowsocks", "ss://"},
		{"vless", "vless", map[string]any{"network": "tcp", "tls": false}, "vless", "vless", "vless://"},
		{"vmess", "vmess", map[string]any{"network": "tcp", "tls": false}, "vmess", "vmess", "vmess://"},
		{"trojan", "trojan", map[string]any{"network": "tcp", "server_name": "edge.example.com"}, "trojan", "trojan", "trojan://"},
		{"hysteria2", "hysteria2", map[string]any{"server_name": "edge.example.com"}, "hysteria2", "hysteria2", "hysteria2://"},
		{"tuic", "tuic", map[string]any{"server_name": "edge.example.com", "congestion_control": "bbr"}, "tuic", "tuic", "tuic://"},
		{"anytls", "anytls", map[string]any{"server_name": "edge.example.com"}, "anytls", "anytls", "anytls://"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			node := Node{Name: "edge-" + tt.name, Type: tt.nodeType, Host: "203.0.113.9", Port: 443, Config: tt.config}

			clash, contentType, count := Render(FormatClash, []Node{node}, credential)
			if count != 1 || contentType != "text/yaml; charset=utf-8" ||
				!strings.Contains(string(clash), `type: "`+tt.clashType+`"`) {
				t.Fatalf("clash count=%d contentType=%q body=%s", count, contentType, clash)
			}

			singbox, contentType, count := Render(FormatSingbox, []Node{node}, credential)
			if count != 1 || contentType != "application/json; charset=utf-8" {
				t.Fatalf("sing-box count=%d contentType=%q", count, contentType)
			}
			var decoded struct {
				Outbounds []map[string]any `json:"outbounds"`
			}
			if err := json.Unmarshal(singbox, &decoded); err != nil {
				t.Fatalf("invalid sing-box JSON: %v", err)
			}
			found := false
			for _, outbound := range decoded.Outbounds {
				if outbound["type"] == tt.singboxType && outbound["tag"] == node.Name {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("sing-box outbound %q not found: %s", tt.singboxType, singbox)
			}

			uriBody, contentType, count := Render(FormatURI, []Node{node}, credential)
			if count != 1 || contentType != "text/plain; charset=utf-8" {
				t.Fatalf("URI count=%d contentType=%q", count, contentType)
			}
			plain, err := base64.StdEncoding.DecodeString(string(uriBody))
			if err != nil || !strings.HasPrefix(string(plain), tt.uriPrefix) {
				t.Fatalf("URI decode=%q err=%v, want prefix %q", plain, err, tt.uriPrefix)
			}
		})
	}
}

func TestLegacyProtocolWithoutPortableRepresentationIsSkipped(t *testing.T) {
	node := Node{Name: "legacy-naive", Type: "naive", Host: "203.0.113.10", Port: 443, Config: map[string]any{}}
	body, _, count := Render(FormatURI, []Node{node}, "019f9f00-1111-7222-8333-444444444444")
	if count != 0 {
		t.Fatalf("legacy protocol falsely rendered, count=%d", count)
	}
	plain, err := base64.StdEncoding.DecodeString(string(body))
	if err != nil || len(plain) != 0 {
		t.Fatalf("unexpected legacy URI body=%q err=%v", plain, err)
	}
}

func TestSingboxRenderUsesCurrentRouteSchemaAndHandlesEmptyNodes(t *testing.T) {
	body, _, count := Render(FormatSingbox, nil, "019f9f00-1111-7222-8333-444444444444")
	if count != 0 {
		t.Fatalf("empty render count=%d", count)
	}
	var config struct {
		Outbounds []map[string]any `json:"outbounds"`
		Route     struct {
			Rules []map[string]any `json:"rules"`
			Final string           `json:"final"`
		} `json:"route"`
	}
	if err := json.Unmarshal(body, &config); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if config.Route.Final != "direct" || len(config.Outbounds) != 1 || config.Outbounds[0]["type"] != "direct" {
		t.Fatalf("unsafe empty subscription config: %s", body)
	}
	encoded := string(body)
	if strings.Contains(encoded, `"geoip"`) || strings.Contains(encoded, `"type": "block"`) {
		t.Fatalf("removed sing-box fields returned: %s", body)
	}
	if len(config.Route.Rules) != 1 || config.Route.Rules[0]["action"] != "route" || config.Route.Rules[0]["ip_is_private"] != true {
		t.Fatalf("modern route action missing: %s", body)
	}
}

func TestRenderDeduplicatesDisplayNames(t *testing.T) {
	nodes := []Node{
		{Name: "香港", Type: "shadowsocks", Host: "203.0.113.1", Port: 8388, Config: map[string]any{"method": "aes-128-gcm"}},
		{Name: "香港", Type: "shadowsocks", Host: "203.0.113.2", Port: 8388, Config: map[string]any{"method": "aes-128-gcm"}},
		{Name: "香港 · 1", Type: "shadowsocks", Host: "203.0.113.3", Port: 8388, Config: map[string]any{"method": "aes-128-gcm"}},
		{Name: "direct", Type: "shadowsocks", Host: "203.0.113.4", Port: 8388, Config: map[string]any{"method": "aes-128-gcm"}},
		{Name: "自动选择", Type: "shadowsocks", Host: "203.0.113.5", Port: 8388, Config: map[string]any{"method": "aes-128-gcm"}},
	}
	body, _, count := Render(FormatSingbox, nodes, "019f9f00-1111-7222-8333-444444444444")
	if count != len(nodes) {
		t.Fatalf("count=%d", count)
	}
	var cfg struct {
		Outbounds []map[string]any `json:"outbounds"`
	}
	if err := json.Unmarshal(body, &cfg); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, outbound := range cfg.Outbounds {
		tag, _ := outbound["tag"].(string)
		if seen[tag] {
			t.Fatalf("duplicate tag %q", tag)
		}
		seen[tag] = true
		if tag == "direct" || tag == "节点选择" || tag == "自动选择" {
			continue // expected sing-box built-ins
		}
		if tag == "block" {
			t.Fatalf("reserved tag leaked: %q", tag)
		}
	}
	// Direct, selector and URL-test are expected sing-box built-ins.
	if len(seen) != len(nodes)+3 {
		t.Fatalf("deduplicated tags missing: %#v", cfg.Outbounds)
	}
}

// 一个链接到处能用：常见客户端的 UA 都要映射到它真能吃下去的格式。
//
// 判断错的代价不对称——给 Clash 用户一份 base64，它还能当订阅导入；
// 给只吃 base64 的客户端一份 YAML，直接报错。所以拿不准的必须回落 URI。
func TestDetectFormatCoversCommonClients(t *testing.T) {
	cases := map[string]Format{
		// sing-box 内核系
		"sing-box 1.10.0":     FormatSingbox,
		"SFI/1.9.0 (iOS)":     FormatSingbox,
		"SFA/1.9.0 (Android)": FormatSingbox,
		"SFM/1.9.0":           FormatSingbox,
		"HiddifyNext/2.0.5":   FormatSingbox,
		"Karing/1.0.0":        FormatSingbox,
		// Clash 内核系
		"clash-verge/v1.7.7":      FormatClash,
		"ClashforWindows/0.20.39": FormatClash,
		"ClashX/1.118.0":          FormatClash,
		"mihomo/1.19.10":          FormatClash,
		"Stash/2.5.0":             FormatClash,
		"FlClash/0.8.60":          FormatClash,
		"clash-nyanpasu/1.5.1":    FormatClash,
		// 只吃 URI / base64 的
		"v2rayN/6.23":           FormatURI,
		"v2rayNG/1.8.19":        FormatURI,
		"Shadowrocket/2.2.28":   FormatURI,
		"Quantumult%20X/1.0.30": FormatURI,
		"Surge/5.8.0":           FormatURI,
		"Loon/3.1.3":            FormatURI,
		"NekoBox/1.3.4":         FormatURI,
		"Streisand/1.6.4":       FormatURI,
		// 完全不认识的一律回落到兼容面最宽的
		"":                              FormatURI,
		"curl/8.14.1":                   FormatURI,
		"Mozilla/5.0 (Windows NT 10.0)": FormatURI,
		"某个还没出现的客户端/1.0":                FormatURI,
	}
	for ua, want := range cases {
		if got := DetectFormat(ua, ""); got != want {
			t.Errorf("UA %q → %s，期望 %s", ua, got, want)
		}
	}
}

// 显式参数覆盖 UA。给浏览器调试和「UA 还没被认出来」这两种情况留的后门。
func TestDetectFormatExplicitOverridesUA(t *testing.T) {
	// 拿一个会被判成 Clash 的 UA，显式要 base64
	if got := DetectFormat("clash-verge/v1.7.7", "base64"); got != FormatURI {
		t.Errorf("显式 base64 没盖过 UA：%s", got)
	}
	if got := DetectFormat("v2rayN/6.23", "clash"); got != FormatClash {
		t.Errorf("显式 clash 没盖过 UA：%s", got)
	}
	if got := DetectFormat("curl/8.14.1", "sing-box"); got != FormatSingbox {
		t.Errorf("显式 sing-box 没生效：%s", got)
	}
	// 不认识的值当没传，回落到 UA
	if got := DetectFormat("clash-verge/v1.7.7", "什么格式"); got != FormatClash {
		t.Errorf("无效的显式值应当回落到 UA 判断，得到 %s", got)
	}
	// 大小写和空格不该影响
	if got := DetectFormat("curl/8.14.1", "  Clash  "); got != FormatClash {
		t.Errorf("显式值的大小写/空格处理有问题：%s", got)
	}
}

// sing-box 同样没有 mKCP。这条的症状比 Clash 那边更隐蔽：sing-box 不会
// 报错，它只是拿一个没有 transport 的 vless 出站去连 UDP 端口，当成裸
// TCP——用户看到节点在列表里、能选中、就是连不上。
func TestRenderSingboxSkipsMKCPNodes(t *testing.T) {
	credential := "019f9f00-4444-7555-8666-777777777777"
	nodes := []Node{
		{Name: "kcp-node", Type: "vless", Host: "203.0.113.31", Port: 2096,
			Config: map[string]any{"network": "mkcp"}},
		{Name: "tcp-node", Type: "vless", Host: "203.0.113.32", Port: 443,
			Config: map[string]any{"network": "tcp", "tls": true}},
	}
	body, _, count := Render(FormatSingbox, nodes, credential)
	if count != 1 {
		t.Fatalf("sing-box 渲染出 %d 个节点，期望跳过 mKCP 只剩 1 个", count)
	}
	if text := string(body); strings.Contains(text, "kcp-node") {
		t.Error("mKCP 节点出现在了 sing-box 配置里")
	} else if !strings.Contains(text, "tcp-node") {
		t.Error("跳过 mKCP 时把正常节点也丢了")
	}
}

// 配了掩码的 mKCP 节点不能出现在任何订阅格式里。
//
// 这条是踩过之后补的：mKCP 的掩码是 xray 新加的功能，而 vless:// 分享
// 链接的参数是早就定死的那一套，没有地方放掩码类型和密码。客户端从链接
// 里读不到，只会用裸 mKCP 去连一个开着加密的服务端。
//
// 后果不是「降级」而是「完全连不上」，而且不会有有用的报错：包发过去
// 服务端解不开直接丢弃，用户看到的就是节点一直转圈。给一条注定失败的
// 链接不如不给。
func TestRenderSkipsMKCPNodesWithMask(t *testing.T) {
	credential := "019f9f00-5555-7666-8777-888888888888"
	masked := Node{
		Name: "masked-kcp", Type: "vless", Host: "203.0.113.41", Port: 2096,
		Config: map[string]any{
			"network": "mkcp", "mask": "mkcp-aes128gcm", "mask_password": "secret",
		},
	}
	plain := Node{
		Name: "plain-tcp", Type: "vless", Host: "203.0.113.42", Port: 443,
		Config: map[string]any{"network": "tcp", "tls": true},
	}

	for _, format := range []Format{FormatURI, FormatClash, FormatSingbox} {
		body, _, count := Render(format, []Node{masked, plain}, credential)
		if count != 1 {
			t.Errorf("%s：渲染出 %d 个节点，带掩码的那个应当被跳过", format, count)
		}
		text := string(body)
		if decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(text)); err == nil {
			text = string(decoded)
		}
		if strings.Contains(text, "masked-kcp") || strings.Contains(text, "203.0.113.41") {
			t.Errorf("%s：带掩码的节点出现在了订阅里", format)
		}
		if !strings.Contains(text, "plain-tcp") {
			t.Errorf("%s：跳过带掩码节点时把正常节点也丢了", format)
		}
	}
}

// 不带掩码、或显式写了 none 的 mKCP 节点仍然要出现在 URI 订阅里——
// 裸 mKCP 是能用的，只是没有伪装。
func TestRenderKeepsUnmaskedMKCPInURI(t *testing.T) {
	credential := "019f9f00-6666-7777-8888-999999999999"
	for _, mask := range []any{nil, "", "none", "mkcp-original"} {
		cfg := map[string]any{"network": "mkcp"}
		if mask != nil {
			cfg["mask"] = mask
		}
		node := Node{Name: "bare-kcp", Type: "vless", Host: "203.0.113.43", Port: 2096, Config: cfg}
		_, _, count := Render(FormatURI, []Node{node}, credential)
		if count != 1 {
			t.Errorf("mask=%v：裸 mKCP 节点被误跳过", mask)
		}
	}
}
