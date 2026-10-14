package render

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// REALITY 配了多个 server name / short id 时，每个用户按稳定哈希分到其中一个：
// 同一用户每次拉到的不变、三种格式一致；不同用户分散到每一个值上。

func multiRealityNode(t *testing.T) Node {
	t.Helper()
	return nodeByName(t, "vless-reality-multi")
}

func userUUID(i int) string {
	return fmt.Sprintf("00000000-0000-4000-8000-%012d", i)
}

func TestRealityPicksSpreadAcrossUsers(t *testing.T) {
	n := multiRealityNode(t)
	snis, sids := map[string]int{}, map[string]int{}
	for i := 0; i < 600; i++ {
		o := parseStream(n, userUUID(i))
		snis[o.SNI]++
		sids[o.ShortID]++
	}
	if len(snis) != 2 || snis["www.example.com"] == 0 || snis["static.example.com"] == 0 {
		t.Fatalf("server names not spread: %v", snis)
	}
	if len(sids) != 3 || sids["0a1b2c3d"] == 0 || sids["4e5f"] == 0 || sids["6a7b8c9d0e1f2a3b"] == 0 {
		t.Fatalf("short ids not spread: %v", sids)
	}
	// 粗看均匀：每个值至少拿到平均份额的一半
	for value, count := range snis {
		if count < 600/2/2 {
			t.Errorf("server name %s only got %d of 600 users", value, count)
		}
	}
	for value, count := range sids {
		if count < 600/3/2 {
			t.Errorf("short id %s only got %d of 600 users", value, count)
		}
	}
}

func TestRealityPickIsStablePerUserAndConsistentAcrossFormats(t *testing.T) {
	n := multiRealityNode(t)
	for i := 0; i < 20; i++ {
		uuid := userUUID(i)
		first := parseStream(n, uuid)
		if again := parseStream(n, uuid); again.SNI != first.SNI || again.ShortID != first.ShortID {
			t.Fatalf("user %s: pick changed between calls", uuid)
		}
		// 改名不换握手参数：哈希只看用户与节点地址
		renamed := n
		renamed.Name = "renamed"
		if r := parseStream(renamed, uuid); r.SNI != first.SNI || r.ShortID != first.ShortID {
			t.Fatalf("user %s: renaming the node changed the pick", uuid)
		}

		clash, _ := nodeToClash(n, uuid, false)
		ro := clash["reality-opts"].(map[string]any)
		if clash["servername"] != first.SNI || ro["short-id"] != first.ShortID {
			t.Errorf("user %s: clash got %v/%v, want %s/%s", uuid, clash["servername"], ro["short-id"], first.SNI, first.ShortID)
		}
		sb, _ := nodeToSingbox(n, uuid)
		raw, _ := json.Marshal(sb)
		if !strings.Contains(string(raw), `"server_name":"`+first.SNI+`"`) || !strings.Contains(string(raw), `"short_id":"`+first.ShortID+`"`) {
			t.Errorf("user %s: sing-box %s does not carry %s/%s", uuid, raw, first.SNI, first.ShortID)
		}
		uri, _ := nodeToURI(n, uuid)
		if !strings.Contains(uri, "sni="+first.SNI) || !strings.Contains(uri, "sid="+first.ShortID) {
			t.Errorf("user %s: uri %s does not carry %s/%s", uuid, uri, first.SNI, first.ShortID)
		}
	}
}

func TestRealitySingleValueIsUnchanged(t *testing.T) {
	cfg := map[string]any{"server_names": []any{"www.example.com"}, "short_ids": []any{"", "0a1b"}}
	n := Node{Host: fixtureHost, Port: 443, Config: cfg}
	for i := 0; i < 10; i++ {
		// 空 short id（存量数据里可能有）不参与挑选
		if got := pickForUser(cfgStrings(cfg, "short_ids"), userUUID(i), n, "sid"); got != "0a1b" {
			t.Fatalf("pick = %q, want the only non-empty short id", got)
		}
		if got := pickForUser(cfgStrings(cfg, "server_names"), userUUID(i), n, "sni"); got != "www.example.com" {
			t.Fatalf("pick = %q, want the only server name", got)
		}
	}
	if got := pickForUser(nil, userUUID(1), n, "sni"); got != "" {
		t.Fatalf("empty list picked %q", got)
	}
}

// REALITY 节点必须在三种格式里都带全参数。
//
// 这一组手写内核形状（security、public_key、server_names…），与 node 模块翻译后交进来的
// 一致；表单形状经翻译后的 REALITY 见 matrix_test.go。
//
// 这组测试是补出来的：第一版只改了 URI 渲染器，Clash 和 sing-box 漏了，
// 结果是 Clash 用户导入后拿到一条没有 reality-opts 的 vless，
// 客户端安静地按明文去连，超时，而用户看到的只是「节点不可用」。
// 少一个字段不会有任何一端报错，所以只能靠测试盯着。

func realityNode() Node {
	return Node{
		Name: "hk-01", Type: "vless", Host: "203.0.113.20", Port: 443,
		Config: map[string]any{
			"network":      "tcp",
			"security":     "reality",
			"dest":         "www.example.com:443",
			"server_names": []any{"www.example.com"},
			"public_key":   "4AivfVM4IIPkDufX8shD0J5tViX9CYc_3yKZ9Vn_HyM",
			"short_ids":    []any{"0a1b2c3d"},
			"fingerprint":  "chrome",
		},
	}
}

func TestRealityURICarriesAllParams(t *testing.T) {
	body, _, n := Render(FormatURI, []Node{realityNode()}, "u-1", "", Options{})
	if n != 1 {
		t.Fatalf("应当渲染出 1 条，得到 %d", n)
	}
	raw, err := base64.StdEncoding.DecodeString(string(body))
	if err != nil {
		t.Fatal(err)
	}
	uri := string(raw)
	for _, want := range []string{
		"security=reality",
		"pbk=4AivfVM4IIPkDufX8shD0J5tViX9CYc_3yKZ9Vn_HyM",
		"sid=0a1b2c3d",
		"fp=chrome",
		"sni=www.example.com",
	} {
		if !strings.Contains(uri, want) {
			t.Errorf("URI 里缺 %q\n实际: %s", want, uri)
		}
	}
}

func TestRealityClashCarriesAllParams(t *testing.T) {
	body, _, _ := Render(FormatClash, []Node{realityNode()}, "u-1", "", Options{})
	y := string(body)
	// 字段名按 mihomo 的 vless adapter：带连字符。
	// 写成下划线不会报错，只会被静默忽略。
	for _, want := range []string{
		"tls: true",
		`servername: "www.example.com"`,
		"reality-opts",
		"public-key",
		"short-id",
		"client-fingerprint",
	} {
		if !strings.Contains(y, want) {
			t.Errorf("Clash 输出里缺 %q\n实际:\n%s", want, y)
		}
	}
	if strings.Contains(y, "public_key") || strings.Contains(y, "short_id") {
		t.Errorf("Clash 用的是连字符不是下划线\n实际:\n%s", y)
	}
}

func TestRealitySingboxCarriesAllParams(t *testing.T) {
	body, _, _ := Render(FormatSingbox, []Node{realityNode()}, "u-1", "", Options{})
	var cfg struct {
		Outbounds []map[string]any `json:"outbounds"`
	}
	if err := json.Unmarshal(body, &cfg); err != nil {
		t.Fatalf("输出不是合法 JSON: %v\n%s", err, body)
	}
	var ob map[string]any
	for _, o := range cfg.Outbounds {
		if o["type"] == "vless" {
			ob = o
			break
		}
	}
	if ob == nil {
		t.Fatalf("没有 vless 出站\n%s", body)
	}
	tls, ok := ob["tls"].(map[string]any)
	if !ok {
		t.Fatalf("没有 tls 段\n%s", body)
	}
	if tls["enabled"] != true {
		t.Error("tls.enabled 不为 true")
	}
	if tls["server_name"] != "www.example.com" {
		t.Errorf("server_name = %v", tls["server_name"])
	}
	r, ok := tls["reality"].(map[string]any)
	if !ok {
		t.Fatalf("没有 tls.reality 段\n%s", body)
	}
	if r["enabled"] != true || r["public_key"] == "" || r["short_id"] != "0a1b2c3d" {
		t.Errorf("reality 段不完整: %v", r)
	}
	// utls 不开的话，客户端用的是 Go 自己的 TLS 指纹，
	// 和它声称伪装的浏览器对不上，等于白伪装
	u, ok := tls["utls"].(map[string]any)
	if !ok || u["enabled"] != true || u["fingerprint"] != "chrome" {
		t.Errorf("utls 段不完整: %v", tls["utls"])
	}
}

// 没开 REALITY 的普通节点不该被带上这些字段。
func TestRealityLeavesPlainNodesAlone(t *testing.T) {
	plain := Node{Name: "plain", Type: "vless", Host: "203.0.113.20", Port: 443,
		Config: map[string]any{"network": "tcp", "security": "none"}}

	body, _, _ := Render(FormatClash, []Node{plain}, "u-1", "", Options{})
	if strings.Contains(string(body), "reality") {
		t.Errorf("普通节点被加了 reality 字段:\n%s", body)
	}
	body, _, _ = Render(FormatSingbox, []Node{plain}, "u-1", "", Options{})
	if strings.Contains(string(body), "reality") {
		t.Errorf("普通节点被加了 reality 字段:\n%s", body)
	}
	raw, _ := base64.StdEncoding.DecodeString(func() string {
		b, _, _ := Render(FormatURI, []Node{plain}, "u-1", "", Options{})
		return string(b)
	}())
	if strings.Contains(string(raw), "security=reality") {
		t.Errorf("普通节点的 URI 带了 security=reality: %s", raw)
	}
}

// server_names 可能被写成单个字符串而不是数组（手写配置很容易这样），
// 三种格式都不该因此少发 SNI。
func TestRealitySingleStringServerNames(t *testing.T) {
	n := realityNode()
	n.Config["server_names"] = "www.example.com"
	n.Config["short_ids"] = "0a1b2c3d"

	body, _, _ := Render(FormatClash, []Node{n}, "u-1", "", Options{})
	if !strings.Contains(string(body), "www.example.com") {
		t.Errorf("Clash 丢了 SNI:\n%s", body)
	}
	body, _, _ = Render(FormatSingbox, []Node{n}, "u-1", "", Options{})
	if !strings.Contains(string(body), "www.example.com") {
		t.Errorf("sing-box 丢了 SNI:\n%s", body)
	}
}
