// [INPUT]: 依赖本包的订阅渲染，节点配置用虚构 REALITY 公钥（与 nodefabric 夹具同一批假密钥对）
// [OUTPUT]: REALITY 节点在 URI、Clash、sing-box 三种订阅格式里的参数渲染契约测试（pbk / sid / sni 齐全、单字符串 server_names、不波及普通节点）
// [POS]: subscription 的 REALITY 输出守卫；只断言公开字段，私钥永不出现在订阅里
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package subscription

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

// REALITY 节点必须在三种格式里都带全参数。
//
// 这组测试是补出来的：第一版只改了 URI 渲染器，Clash 和 sing-box 漏了，
// 结果是 Clash 用户导入后拿到一条没有 reality-opts 的 vless，
// 客户端安静地按明文去连，超时，而用户看到的只是「节点不可用」。
// 少一个字段不会有任何一端报错，所以只能靠测试盯着。

func realityNode() Node {
	return Node{
		Name: "hk-01", Type: "vless", Host: "1.2.3.4", Port: 443,
		Config: map[string]any{
			"network":      "tcp",
			"security":     "reality",
			"dest":         "www.apple.com:443",
			"server_names": []any{"www.apple.com"},
			"public_key":   "g6JP7noZPAfVjuXKL2xmTnKG-2xRwlt65iUksUaGGio",
			"short_ids":    []any{"b5301d06"},
			"fingerprint":  "chrome",
		},
	}
}

func TestReality_URI里参数齐全(t *testing.T) {
	body, _, n := Render(FormatURI, []Node{realityNode()}, "u-1")
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
		"pbk=g6JP7noZPAfVjuXKL2xmTnKG-2xRwlt65iUksUaGGio",
		"sid=b5301d06",
		"fp=chrome",
		"sni=www.apple.com",
	} {
		if !strings.Contains(uri, want) {
			t.Errorf("URI 里缺 %q\n实际: %s", want, uri)
		}
	}
}

func TestReality_Clash里参数齐全(t *testing.T) {
	body, _, _ := Render(FormatClash, []Node{realityNode()}, "u-1")
	y := string(body)
	// 字段名按 mihomo 的 vless adapter：带连字符。
	// 写成下划线不会报错，只会被静默忽略。
	for _, want := range []string{
		"tls: true",
		`servername: "www.apple.com"`,
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

func TestReality_Singbox里参数齐全(t *testing.T) {
	body, _, _ := Render(FormatSingbox, []Node{realityNode()}, "u-1")
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
	if tls["server_name"] != "www.apple.com" {
		t.Errorf("server_name = %v", tls["server_name"])
	}
	r, ok := tls["reality"].(map[string]any)
	if !ok {
		t.Fatalf("没有 tls.reality 段\n%s", body)
	}
	if r["enabled"] != true || r["public_key"] == "" || r["short_id"] != "b5301d06" {
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
func TestReality_不影响普通节点(t *testing.T) {
	plain := Node{Name: "plain", Type: "vless", Host: "1.2.3.4", Port: 443,
		Config: map[string]any{"network": "tcp", "tls": false}}

	body, _, _ := Render(FormatClash, []Node{plain}, "u-1")
	if strings.Contains(string(body), "reality") {
		t.Errorf("普通节点被加了 reality 字段:\n%s", body)
	}
	body, _, _ = Render(FormatSingbox, []Node{plain}, "u-1")
	if strings.Contains(string(body), "reality") {
		t.Errorf("普通节点被加了 reality 字段:\n%s", body)
	}
	raw, _ := base64.StdEncoding.DecodeString(func() string {
		b, _, _ := Render(FormatURI, []Node{plain}, "u-1")
		return string(b)
	}())
	if strings.Contains(string(raw), "security=reality") {
		t.Errorf("普通节点的 URI 带了 security=reality: %s", raw)
	}
}

// server_names 可能被写成单个字符串而不是数组（手写配置很容易这样），
// 三种格式都不该因此少发 SNI。
func TestReality_单字符串的server_names(t *testing.T) {
	n := realityNode()
	n.Config["server_names"] = "www.apple.com"
	n.Config["short_ids"] = "b5301d06"

	body, _, _ := Render(FormatClash, []Node{n}, "u-1")
	if !strings.Contains(string(body), "www.apple.com") {
		t.Errorf("Clash 丢了 SNI:\n%s", body)
	}
	body, _, _ = Render(FormatSingbox, []Node{n}, "u-1")
	if !strings.Contains(string(body), "www.apple.com") {
		t.Errorf("sing-box 丢了 SNI:\n%s", body)
	}
}
