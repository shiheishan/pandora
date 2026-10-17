package render

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// 一个节点都没有（没有可下发节点，或全被跳过）时 Clash 配置仍要合法：
// url-test 组的空 proxies 会让 mihomo 拒掉整份配置。
func TestClashWithoutNodesStaysLoadable(t *testing.T) {
	onlySkipped := []Node{{Name: "juicity", Type: "juicity", Host: fixtureHost, Port: 1, Config: map[string]any{"cert_path": "/c", "key_path": "/k"}}}
	for _, nodes := range [][]Node{nil, onlySkipped} {
		for _, f := range []Format{FormatClash, FormatClashPremium} {
			body, _, count := Render(f, nodes, fixtureUUID, "", Options{})
			text := string(body)
			if count != 0 || !strings.Contains(text, "proxies: []") ||
				!strings.Contains(text, "{name: 节点选择, type: select, proxies: [DIRECT]}") ||
				strings.Contains(text, "url-test") || !strings.Contains(text, "MATCH,节点选择") {
				t.Fatalf("%s with %d nodes: empty config is not loadable:\n%s", f, len(nodes), text)
			}
		}
	}
}

// Premium 内核只认 ss / vmess / trojan / socks5 / http 与 ws / grpc：
// 整份订阅里不能出现任何它不认识的类型或字段。
func TestClashPremiumOnlyGetsProxiesItUnderstands(t *testing.T) {
	body, _, count := Render(FormatClashPremium, loadGroup(t, groupForm), fixtureUUID, "", Options{})
	text := string(body)
	if count == 0 {
		t.Fatal("premium got no nodes at all")
	}
	for _, banned := range []string{
		`type: "vless"`, `type: "hysteria2"`, `type: "tuic"`, `type: "anytls"`, `type: "mieru"`,
		"reality-opts", "client-fingerprint", "v2ray-http-upgrade", "xhttp", "shadow-tls", "2022-blake3",
	} {
		if strings.Contains(text, banned) {
			t.Errorf("premium config contains %s:\n%s", banned, text)
		}
	}
	meta, _, metaCount := Render(FormatClash, loadGroup(t, groupForm), fixtureUUID, "", Options{})
	if metaCount <= count || !strings.Contains(string(meta), `type: "vless"`) {
		t.Fatalf("meta should get more nodes than premium (%d vs %d)", metaCount, count)
	}
}

// sing-box 的 ShadowTLS 必须是一对出站：内层 Shadowsocks 经 detour 指向外层
// shadowtls。只有内层时 sing-box 启动报 dependency not found，整份配置起不来。
func TestSingboxShadowTLSEmitsBothOutbounds(t *testing.T) {
	// nodes.json 的 extra 组里有一个名字正好撞上外层 tag 的节点（「shadowtls · shadowtls」），tag 仍须唯一
	nodes := loadNodes(t)
	body, _, _ := Render(FormatSingbox, nodes, fixtureUUID, "", Options{})
	var cfg struct {
		Outbounds []map[string]any `json:"outbounds"`
	}
	if err := json.Unmarshal(body, &cfg); err != nil {
		t.Fatal(err)
	}
	byTag := map[string]map[string]any{}
	for _, o := range cfg.Outbounds {
		tag, _ := o["tag"].(string)
		if _, dup := byTag[tag]; dup {
			t.Fatalf("duplicate outbound tag %q", tag)
		}
		byTag[tag] = o
	}
	inner := byTag["shadowtls"]
	if inner == nil || inner["type"] != "shadowsocks" {
		t.Fatalf("inner shadowsocks outbound missing: %v", inner)
	}
	detour, _ := inner["detour"].(string)
	outer := byTag[detour]
	if outer == nil || outer["type"] != "shadowtls" {
		t.Fatalf("detour %q does not point at a shadowtls outbound", detour)
	}
	tls, _ := outer["tls"].(map[string]any)
	utls, _ := tls["utls"].(map[string]any)
	if outer["version"] != float64(3) || outer["password"] != "stls-pass-1" ||
		outer["server"] != fixtureHost || outer["server_port"] != float64(4443) ||
		tls["enabled"] != true || tls["server_name"] != "www.example.com" || utls["enabled"] != true {
		t.Fatalf("shadowtls outbound incomplete: %v", outer)
	}
	// 外层只是内层的传输，不应出现在选择组里
	for _, o := range cfg.Outbounds {
		if o["type"] == "selector" || o["type"] == "urltest" {
			for _, member := range o["outbounds"].([]any) {
				if member == detour {
					t.Fatalf("%s group lists the shadowtls transport outbound", o["tag"])
				}
			}
		}
	}
}

// 不测 cipher 与 method 打架的 Shadowsocks 节点：这类节点
// 由 node 模块在翻译时排除，渲染拿到的已是内核形状，不再处理冲突。

// 存量的明文 AnyTLS（后台现在要求证书）在任何格式里都跳过。
func TestAnyTLSWithoutCertificateIsSkipped(t *testing.T) {
	node := Node{Name: "plain-anytls", Type: "anytls", Host: fixtureHost, Port: 1, Config: map[string]any{}}
	for _, f := range []Format{FormatClash, FormatSingbox, FormatURI} {
		if _, _, count := Render(f, []Node{node}, fixtureUUID, "", Options{}); count != 0 {
			t.Errorf("%s rendered a plaintext AnyTLS node that no client can reach", f)
		}
	}
}

func TestContentDispositionIsASCIISafe(t *testing.T) {
	cases := map[string]string{
		"Pandora":               `attachment; filename="Pandora"; filename*=UTF-8''Pandora`,
		"潘多拉 Cloud":             `attachment; filename="Cloud"; filename*=UTF-8''%E6%BD%98%E5%A4%9A%E6%8B%89%20Cloud`,
		"全中文":                   `attachment; filename="subscription"; filename*=UTF-8''%E5%85%A8%E4%B8%AD%E6%96%87`,
		"a\"b;\r\nSet-Cookie:x": `attachment; filename="abSet-Cookiex"; filename*=UTF-8''a%22b%3B%0D%0ASet-Cookie%3Ax`,
		"":                      `attachment; filename="Pandora"; filename*=UTF-8''Pandora`,
	}
	for name, want := range cases {
		got := ContentDisposition(name)
		if got != want {
			t.Errorf("ContentDisposition(%q)\n got  %s\n want %s", name, got, want)
		}
		for _, r := range got {
			if r < 0x20 || r > 0x7e {
				t.Errorf("ContentDisposition(%q) contains non-printable-ASCII %q", name, r)
			}
		}
	}
}

// 配置来自 JSON 时数字是 float64，代码里构造时可能是 int，也可能直接写 bool：
// 数字类布尔（tls、allow_insecure）三种写法渲染出的字节必须一样；
// REALITY 的 server_names / short_ids 写成 []any 与 []string 也一样。
func TestNumericConfigTypesRenderTheSame(t *testing.T) {
	build := func(one, zero any) []Node {
		return []Node{
			{Name: "trojan", Type: "trojan", Host: fixtureHost, Port: 1, Config: map[string]any{
				"network": "tcp", "security": "none", "tls": one, "cert_path": "/c", "key_path": "/k",
				"server_name": "edge.example.com", "allow_insecure": one,
			}},
			{Name: "socks", Type: "socks", Host: fixtureHost, Port: 2, Config: map[string]any{"network": "tcp", "tls": one}},
			{Name: "http", Type: "http", Host: fixtureHost, Port: 3, Config: map[string]any{"tls": one}},
			{Name: "vless", Type: "vless", Host: fixtureHost, Port: 4, Config: map[string]any{"network": "tcp", "security": "none", "tls": zero}},
		}
	}
	reality := func(names, sids any) []Node {
		return []Node{{Name: "reality", Type: "vless", Host: fixtureHost, Port: 5, Config: map[string]any{
			"network": "tcp", "security": "reality", "public_key": fixtureRealityPub,
			"server_names": names, "short_ids": sids, "flow": "xtls-rprx-vision",
		}}}
	}
	for _, f := range []Format{FormatClash, FormatClashPremium, FormatSingbox, FormatURI} {
		fromJSON, _, n := Render(f, build(float64(1), float64(0)), fixtureUUID, "", Options{})
		fromInt, _, _ := Render(f, build(1, 0), fixtureUUID, "", Options{})
		fromBool, _, _ := Render(f, build(true, false), fixtureUUID, "", Options{})
		if n == 0 {
			t.Fatalf("%s rendered no nodes", f)
		}
		if !bytes.Equal(fromJSON, fromInt) || !bytes.Equal(fromJSON, fromBool) {
			t.Errorf("%s: float64 / int / bool configs render differently\nfloat64: %s\nint: %s\nbool: %s", f, fromJSON, fromInt, fromBool)
		}
		anyList, _, _ := Render(f, reality([]any{"a.example.com", "b.example.com"}, []any{"01ab", "02cd"}), fixtureUUID, "", Options{})
		strList, _, _ := Render(f, reality([]string{"a.example.com", "b.example.com"}, []string{"01ab", "02cd"}), fixtureUUID, "", Options{})
		if !bytes.Equal(anyList, strList) {
			t.Errorf("%s: []any and []string server_names render differently\n%s\n%s", f, anyList, strList)
		}
	}
}
