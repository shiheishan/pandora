package render

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func TestSupportedRenderersProduceAllAdvertisedFormats(t *testing.T) {
	const credential = fixtureUUID
	tests := []struct {
		name        string
		nodeType    string
		config      map[string]any
		clashType   string
		singboxType string
		uriPrefix   string
	}{
		{"shadowsocks", "shadowsocks", map[string]any{"method": "aes-256-gcm"}, "ss", "shadowsocks", "ss://"},
		{"vless", "vless", map[string]any{"network": "tcp", "security": "none"}, "vless", "vless", "vless://"},
		{"vmess", "vmess", map[string]any{"network": "tcp", "security": "none"}, "vmess", "vmess", "vmess://"},
		{"trojan", "trojan", map[string]any{
			"network": "tcp", "security": "none", "tls": true, "cert_path": "/c.pem", "key_path": "/k.pem",
			"server_name": "edge.example.com",
		}, "trojan", "trojan", "trojan://"},
		{"hysteria2", "hysteria2", map[string]any{
			"cert_path": "/c.pem", "key_path": "/k.pem", "server_name": "edge.example.com",
		}, "hysteria2", "hysteria2", "hysteria2://"},
		{"tuic", "tuic", map[string]any{
			"cert_path": "/c.pem", "key_path": "/k.pem", "congestion_control": "bbr",
			"server_name": "edge.example.com",
		}, "tuic", "tuic", "tuic://"},
		{"anytls", "anytls", map[string]any{
			"tls": true, "cert_path": "/c.pem", "key_path": "/k.pem", "server_name": "edge.example.com",
		}, "anytls", "anytls", "anytls://"},
		{"socks", "socks", map[string]any{"network": "tcp"}, "socks5", "socks", "socks5://"},
		{"http", "http", map[string]any{}, "http", "http", "http://"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			node := Node{Name: "edge-" + tt.name, Type: tt.nodeType, Host: "203.0.113.9", Port: 443, Config: tt.config}

			clash, contentType, count := Render(FormatClash, []Node{node}, credential, "", Options{})
			if count != 1 || contentType != "text/yaml; charset=utf-8" ||
				!strings.Contains(string(clash), `type: "`+tt.clashType+`"`) {
				t.Fatalf("clash count=%d contentType=%q body=%s", count, contentType, clash)
			}

			singbox, contentType, count := Render(FormatSingbox, []Node{node}, credential, "", Options{})
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

			uriBody, contentType, count := Render(FormatURI, []Node{node}, credential, "", Options{})
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

func TestProtocolWithoutPortableURIIsSkipped(t *testing.T) {
	node := Node{Name: "mieru", Type: "mieru", Host: "203.0.113.10", Port: 443, Config: map[string]any{"transport": "TCP"}}
	body, _, count := Render(FormatURI, []Node{node}, fixtureUUID, "", Options{})
	if count != 0 {
		t.Fatalf("protocol without URI form falsely rendered, count=%d", count)
	}
	plain, err := base64.StdEncoding.DecodeString(string(body))
	if err != nil || len(plain) != 0 {
		t.Fatalf("unexpected URI body=%q err=%v", plain, err)
	}
}

func TestSingboxRenderUsesCurrentRouteSchemaAndHandlesEmptyNodes(t *testing.T) {
	body, _, count := Render(FormatSingbox, nil, fixtureUUID, "", Options{})
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
	// 私网直连规则仍在（模板的其余规则见 template_test.go）
	private := false
	for _, rule := range config.Route.Rules {
		private = private || (rule["action"] == "route" && rule["ip_is_private"] == true && rule["outbound"] == "direct")
	}
	if !private {
		t.Fatalf("modern route action missing: %s", body)
	}
	if strings.Contains(encoded, "节点选择") {
		t.Fatalf("empty subscription references the missing selector: %s", body)
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
	body, _, count := Render(FormatSingbox, nodes, fixtureUUID, "", Options{})
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
			continue
		}
		if tag == "block" {
			t.Fatalf("reserved tag leaked: %q", tag)
		}
	}
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
		"sing-box 1.10.0":     FormatSingbox,
		"SFI/1.9.0 (iOS)":     FormatSingbox,
		"SFA/1.9.0 (Android)": FormatSingbox,
		"SFM/1.9.0":           FormatSingbox,
		"HiddifyNext/2.0.5":   FormatSingbox,
		"Karing/1.0.0":        FormatSingbox,

		// Clash Meta（mihomo）系
		"clash-verge/v1.7.7":              FormatClash,
		"mihomo/1.19.10":                  FormatClash,
		"FlClash/0.8.60":                  FormatClash,
		"clash-nyanpasu/1.5.1":            FormatClash,
		"ClashMetaForAndroid/2.11.5.Meta": FormatClash,
		"ClashX Meta/1.4.0":               FormatClash,
		"Clash.Meta":                      FormatClash,
		"mihomo.party/v1.5.0":             FormatClash,
		"OpenClash":                       FormatClash,

		// Clash Premium 系：不认 vless / hysteria2 / tuic / anytls / mieru
		"ClashforWindows/0.20.39": FormatClashPremium,
		"ClashX/1.118.0":          FormatClashPremium,
		"ClashX Pro/1.118.0":      FormatClashPremium,
		"ClashForAndroid/2.5.12":  FormatClashPremium,
		"clash":                   FormatClashPremium,

		// 有专有格式的
		"Stash/2.5.0 Clash/1.9.0":   FormatStash,
		"Shadowrocket/2.2.28":       FormatShadowrocket,
		"Loon/3.1.3":                FormatLoon,
		"Surfboard/2.24.0":          FormatSurfboard,
		"shadowsocks-android/5.3.3": FormatSIP008,

		// 只吃 URI / base64 的
		"ShadowsocksX-NG/1.10.2": FormatURI,
		"v2rayN/6.23":            FormatURI,
		"v2rayNG/1.8.19":         FormatURI,
		"Quantumult%20X/1.0.30":  FormatURI,
		"Surge/5.8.0":            FormatURI,
		"NekoBox/1.3.4":          FormatURI,
		"Streisand/1.6.4":        FormatURI,

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
	if got := DetectFormat("clash-verge/v1.7.7", "base64"); got != FormatURI {
		t.Errorf("显式 base64 没盖过 UA：%s", got)
	}
	if got := DetectFormat("v2rayN/6.23", "clash"); got != FormatClash {
		t.Errorf("显式 clash 没盖过 UA：%s", got)
	}
	if got := DetectFormat("ClashX/1.118.0", "clash"); got != FormatClashPremium {
		t.Errorf("Premium 客户端带 flag=clash 应给 Premium：%s", got)
	}
	if got := DetectFormat("ClashX/1.118.0", "meta"); got != FormatClash {
		t.Errorf("显式 meta 应给 Meta：%s", got)
	}
	if got := DetectFormat("curl/8.14.1", "premium"); got != FormatClashPremium {
		t.Errorf("显式 premium 没生效：%s", got)
	}
	if got := DetectFormat("curl/8.14.1", "sing-box"); got != FormatSingbox {
		t.Errorf("显式 sing-box 没生效：%s", got)
	}
	if got := DetectFormat("clash-verge/v1.7.7", "什么格式"); got != FormatClash {
		t.Errorf("无效的显式值应当回落到 UA 判断，得到 %s", got)
	}
	if got := DetectFormat("curl/8.14.1", "  Clash  "); got != FormatClash {
		t.Errorf("显式值的大小写/空格处理有问题：%s", got)
	}
}

// 覆盖 DetectFormat 显式参数与 UA 各分支。
func TestDetectFormatEveryBranch(t *testing.T) {
	cases := []struct {
		ua, explicit string
		want         Format
	}{
		// 显式 clash：Premium UA → Premium，Meta UA → Meta
		{"ClashX/1.118.0", "clash", FormatClashPremium},
		{"clash-verge/v1.7.7", "clash", FormatClash},
		{"meta", "meta", FormatClash},
		{"", "clashmeta", FormatClash},
		{"", "clash-meta", FormatClash},
		{"", "mihomo", FormatClash},
		{"", "verge", FormatClash},
		{"", "stash", FormatStash},
		{"Stash/2.5.0", "clash", FormatStash},
		{"", "clash-premium", FormatClashPremium},
		{"", "premium", FormatClashPremium},
		{"", "singbox", FormatSingbox},
		{"", "sing-box", FormatSingbox},
		{"", "singbox-json", FormatSingbox},
		{"", "hiddify", FormatSingbox},
		{"", "karing", FormatSingbox},
		{"", "uri", FormatURI},
		{"", "base64", FormatURI},
		{"", "v2ray", FormatURI},
		{"", "v2rayn", FormatURI},
		{"", "v2rayng", FormatURI},
		{"", "shadowrocket", FormatShadowrocket},
		{"", "loon", FormatLoon},
		{"", "surfboard", FormatSurfboard},
		{"", "sip008", FormatSIP008},
		{"", "shadowsocks", FormatSIP008},
		{"", "general", FormatURI},
		// UA sing-box 子串
		{"sing-box/2.0.0", "", FormatSingbox},
		{"SFI/1.0", "", FormatSingbox},
		{"SFA/1.0", "", FormatSingbox},
		{"SFM/1.0", "", FormatSingbox},
		{"SFT/1.0", "", FormatSingbox},
		{"Hiddify/1.0", "", FormatSingbox},
		{"Karing/1.0", "", FormatSingbox},
		// UA 专有格式子串
		{"Shadowrocket/1.0", "", FormatShadowrocket},
		{"Loon/1.0", "", FormatLoon},
		{"Surfboard/1.0", "", FormatSurfboard},
		{"Stash/1.0", "", FormatStash},
		{"shadowsocks/1.0", "", FormatSIP008},
		// UA URI 子串（各至少一例）
		{"ShadowsocksX-NG/1.0", "", FormatURI},
		{"ShadowsocksR/1.0", "", FormatURI},
		{"Quantumult/1.0", "", FormatURI},
		{"Surge/1.0", "", FormatURI},
		{"v2rayN/1.0", "", FormatURI},
		{"v2rayNG/1.0", "", FormatURI},
		{"v2rayU/1.0", "", FormatURI},
		{"qv2ray/1.0", "", FormatURI},
		{"nekobox/1.0", "", FormatURI},
		{"nekoray/1.0", "", FormatURI},
		{"matsuri/1.0", "", FormatURI},
		{"sagernet/1.0", "", FormatURI},
		{"streisand/1.0", "", FormatURI},
		{"potatso/1.0", "", FormatURI},
		{"oneclick/1.0", "", FormatURI},
		// UA clash 子串 → clashFlavor
		{"clash-meta/1.0", "", FormatClash},
		{"mihomo/1.0", "", FormatClash},
		{"meta/1.0", "", FormatClash},
		{"flclash/1.0", "", FormatClash},
		{"nyanpasu/1.0", "", FormatClash},
		{"openclash/1.0", "", FormatClash},
		{"Koala Clash/1.0", "", FormatClash},
		{"ClashMi/1.0", "", FormatClash},
		{"only-clash-here/1.0", "", FormatClashPremium},
		// 专有格式与 URI 排在 clash 之前
		{"Clash-Shadowrocket/1.0", "", FormatShadowrocket},
		{"Clash-Quantumult/1.0", "", FormatURI},
		// sing-box 排在 clash 之前
		{"Hiddify clash/1.0", "", FormatSingbox},
	}
	for _, c := range cases {
		if got := DetectFormat(c.ua, c.explicit); got != c.want {
			t.Errorf("DetectFormat(%q, %q) = %s, want %s", c.ua, c.explicit, got, c.want)
		}
	}
}

func TestClashFlavorMarkers(t *testing.T) {
	metaMarkers := []string{"meta", "mihomo", "verge", "flclash", "nyanpasu", "stash", "openclash", "koala", "clashmi"}
	for _, m := range metaMarkers {
		if got := clashFlavor(m); got != FormatClash {
			t.Errorf("clashFlavor(%q) = %s, want Meta", m, got)
		}
	}
	if got := clashFlavor("nyanpasu"); got != FormatClash {
		t.Errorf("clashFlavor(nyanpasu) = %s", got)
	}
	if got := clashFlavor("plain-clash-client"); got != FormatClashPremium {
		t.Errorf("clashFlavor(only clash) = %s, want Premium", got)
	}
	if got := clashFlavor("no-markers-here"); got != FormatClash {
		t.Errorf("clashFlavor(no clash) = %s, want Meta default", got)
	}
}

func TestUAFamily(t *testing.T) {
	cases := map[string]string{
		"sing-box/1.0":     "sing-box",
		"SFI/1.0":          "sing-box",
		"SFA/1.0":          "sing-box",
		"SFM/1.0":          "other",
		"Shadowrocket/1.0": "shadowrocket",
		"clash/1.0":        "clash",
		"mihomo/1.0":       "clash",
		"stash/1.0":        "clash",
		"Quantumult/1.0":   "quantumult",
		"Surge/1.0":        "surge",
		"v2ray/1.0":        "v2ray",
		"":                 "none",
		"SomeClient/1.0":   "other",
	}
	for ua, want := range cases {
		if got := UAFamily(ua); got != want {
			t.Errorf("UAFamily(%q) = %q, want %q", ua, got, want)
		}
	}
}

func TestSingboxDialectFor(t *testing.T) {
	cases := []struct {
		ua   string
		want singboxDialect
	}{
		{"SFA/1.14.2 (614; sing-box 1.14.2; language zh_CN)", singboxDialectHTTPClients},
		{"SFI/1.15.0 (1; sing-box 1.15.0-beta.1; language en)", singboxDialectHTTPClients},
		{"sing-box/2.0.0", singboxDialectHTTPClients},
		{"sing-box v1.14.0", singboxDialectHTTPClients},
		{"SFM/1.13.14 (1; sing-box 1.13.14; language zh_CN)", singboxDialectCompat},
		{"SFA/1.9.0 (1; sing-box 1.9.0)", singboxDialectCompat},
		{"Hiddify/2.5 (sing-box)", singboxDialectCompat},
		{"", singboxDialectCompat},
		{"clash-verge/v2.0", singboxDialectCompat},
		{"sing-box 1.x sing-box 1.14.0", singboxDialectHTTPClients},
		{"sing-box 12345.1", singboxDialectCompat},
	}
	for _, c := range cases {
		if got := singboxDialectFor(c.ua); got != c.want {
			t.Errorf("singboxDialectFor(%q) = %v, want %v", c.ua, got, c.want)
		}
	}
}

func TestRenderSingboxSkipsMKCPNodes(t *testing.T) {
	credential := "019f9f00-4444-7555-8666-777777777777"
	nodes := []Node{
		{Name: "kcp-node", Type: "vless", Host: "203.0.113.31", Port: 2096,
			Config: map[string]any{"network": "mkcp", "security": "none"}},
		{Name: "tcp-node", Type: "vless", Host: "203.0.113.32", Port: 443,
			Config: map[string]any{"network": "tcp", "security": "none"}},
	}
	body, _, count := Render(FormatSingbox, nodes, credential, "", Options{})
	if count != 1 {
		t.Fatalf("sing-box 渲染出 %d 个节点，期望跳过 mKCP 只剩 1 个", count)
	}
	if text := string(body); strings.Contains(text, "kcp-node") {
		t.Error("mKCP 节点出现在了 sing-box 配置里")
	} else if !strings.Contains(text, "tcp-node") {
		t.Error("跳过 mKCP 时把正常节点也丢了")
	}
}

func TestRenderSkipsMKCPNodesWithMask(t *testing.T) {
	credential := "019f9f00-5555-7666-8777-888888888888"
	masked := Node{
		Name: "masked-kcp", Type: "vless", Host: "203.0.113.41", Port: 2096,
		Config: map[string]any{
			"security": "none", "network": "mkcp", "mask": "mkcp-aes128gcm", "mask_password": "secret",
		},
	}
	plain := Node{
		Name: "plain-tcp", Type: "vless", Host: "203.0.113.42", Port: 443,
		Config: map[string]any{"network": "tcp", "security": "none"},
	}

	for _, format := range []Format{FormatURI, FormatClash, FormatSingbox} {
		body, _, count := Render(format, []Node{masked, plain}, credential, "", Options{})
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

func TestRenderKeepsUnmaskedMKCPInURI(t *testing.T) {
	credential := "019f9f00-6666-7777-8888-999999999999"
	for _, mask := range []any{nil, "", "none", "mkcp-original"} {
		cfg := map[string]any{"network": "mkcp", "security": "none"}
		if mask != nil {
			cfg["mask"] = mask
		}
		node := Node{Name: "bare-kcp", Type: "vless", Host: "203.0.113.43", Port: 2096, Config: cfg}
		_, _, count := Render(FormatURI, []Node{node}, credential, "", Options{})
		if count != 1 {
			t.Errorf("mask=%v：裸 mKCP 节点被误跳过", mask)
		}
	}
}
