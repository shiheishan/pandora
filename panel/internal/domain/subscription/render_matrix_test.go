package subscription

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

// 渲染矩阵：每个协议变体 × 每种格式，要么渲染出客户端连得上所需的全部参数，
// 要么显式跳过并给出原因。夹具是后台表单形状（render_fixtures_test.go）。
//
// 这张表就是订阅渲染的事实清单：加协议、改字段名、改跳过规则，都要在这里
// 改对应的格。has 里的子串按各格式的写法核对：Clash 是 inline YAML 的一行，
// sing-box 是该出站的紧凑 JSON，URI 是解码后的分享链接（vmess 再解一层 JSON）。

type cell struct {
	skip string   // 非空：必须跳过，且跳过原因包含这个子串
	has  []string // 渲染出的内容必须包含的子串
	not  []string // 渲染出的内容不许包含的子串
}

func skip(reason string) cell           { return cell{skip: reason} }
func has(parts ...string) cell          { return cell{has: parts} }
func (c cell) without(p ...string) cell { c.not = append(c.not, p...); return c }

type matrixRow struct {
	clash, premium, singbox, uri cell
}

const uu = fixtureUUID + ":" + fixtureUUID

func renderMatrix() map[string]matrixRow {
	ssB64 := func(method string) string {
		return "ss://" + base64.RawURLEncoding.EncodeToString([]byte(method+":"+fixtureUUID)) + "@node.example.com:"
	}
	realityClash := []string{`reality-opts: {public-key: "` + fixtureRealityPub + `", short-id: "0a1b2c3d"}`}
	realitySingbox := `"reality":{"enabled":true,"public_key":"` + fixtureRealityPub + `","short_id":"0a1b2c3d"}`
	realityURI := []string{"security=reality", "pbk=" + fixtureRealityPub, "sid=0a1b2c3d", "sni=www.example.com"}
	return map[string]matrixRow{
		// 节点端 Shadowsocks 只听 TCP：Clash 写 udp:false，sing-box 声明只走 tcp
		"ss-aes128": {
			clash: has(`type: "ss"`, `cipher: "aes-128-gcm"`, `udp: false`), premium: has(`cipher: "aes-128-gcm"`, `udp: false`),
			singbox: has(`"method":"aes-128-gcm"`, `"network":"tcp"`), uri: has(ssB64("aes-128-gcm")),
		},
		"ss-chacha": {
			clash: has(`cipher: "chacha20-ietf-poly1305"`, `udp: false`), premium: has(`cipher: "chacha20-ietf-poly1305"`),
			singbox: has(`"method":"chacha20-ietf-poly1305"`, `"network":"tcp"`), uri: has(ssB64("chacha20-ietf-poly1305")),
		},
		"hy2-obfs": {
			clash:   has(`obfs: "salamander"`, `obfs-password: "obfs-pass-1"`, `sni: "sni.example.com"`, `skip-cert-verify: true`),
			premium: skip("Premium"),
			singbox: has(`"obfs":{"password":"obfs-pass-1","type":"salamander"}`, `"server_name":"sni.example.com"`, `"insecure":true`),
			uri:     has("hysteria2://", "obfs=salamander", "obfs-password=obfs-pass-1", "sni=sni.example.com", "insecure=1"),
		},
		"hy2-plain": {
			clash: has(`type: "hysteria2"`).without("obfs", "skip-cert-verify"), premium: skip("Premium"),
			singbox: has(`"type":"hysteria2"`).without("obfs", "insecure"), uri: has("hysteria2://").without("obfs", "insecure"),
		},
		"tuic": {
			clash: has(`congestion-controller: "cubic"`, `sni: "sni.example.com"`, `alpn: ["h3"]`), premium: skip("Premium"),
			singbox: has(`"congestion_control":"cubic"`, `"alpn":["h3"]`, `"server_name":"sni.example.com"`),
			uri:     has("tuic://"+uu+"@", "congestion_control=cubic", "sni=sni.example.com", "alpn=h3"),
		},
		// 没配 utls 时三种格式都下发 chrome
		"anytls": {
			clash: has(`type: "anytls"`, `sni: "sni.example.com"`, `skip-cert-verify: true`, `client-fingerprint: "chrome"`), premium: skip("Premium"),
			singbox: has(`"type":"anytls"`, `"insecure":true`, `"server_name":"sni.example.com"`, `"utls":{"enabled":true,"fingerprint":"chrome"}`),
			uri:     has("anytls://", "sni=sni.example.com", "insecure=1", "fp=chrome"),
		},
		// 回落只给节点端，不进订阅
		"anytls-utls-fallback": {
			clash: has(`client-fingerprint: "safari"`).without("decoy"), premium: skip("Premium"),
			singbox: has(`"utls":{"enabled":true,"fingerprint":"safari"}`).without("decoy"),
			uri:     has("anytls://", "fp=safari").without("decoy"),
		},
		"naive": {
			clash: skip("naive"), premium: skip("Premium"),
			singbox: has(`"type":"naive"`, `"username":"`+fixtureUUID+`"`, `"server_name":"sni.example.com"`),
			uri:     has("naive+https://" + uu + "@node.example.com:9443?sni=sni.example.com#naive"),
		},
		"naive-fallback": {
			clash: skip("naive"), premium: skip("Premium"),
			singbox: has(`"type":"naive"`, `"server_name":"sni.example.com"`).without("decoy"),
			uri:     has("naive+https://").without("decoy"),
		},
		"naive-insecure": {
			clash: skip("naive"), premium: skip("Premium"), singbox: skip("跳过证书校验"), uri: skip("跳过证书校验"),
		},
		"juicity": {
			clash: skip("juicity"), premium: skip("Premium"), singbox: skip("juicity"),
			uri: has("juicity://" + uu + "@node.example.com:8445?congestion_control=bbr#juicity"),
		},
		"socks": {
			clash: has(`type: "socks5"`, `username: "`+fixtureUUID+`"`, `udp: false`), premium: has(`type: "socks5"`),
			singbox: has(`"type":"socks"`, `"version":"5"`, `"network":"tcp"`),
			uri:     has("socks5://" + uu + "@node.example.com:1080#socks"),
		},
		"socks-udp": {
			clash: has(`udp: true`), premium: has(`type: "socks5"`),
			singbox: has(`"type":"socks"`).without(`"network"`), uri: has("socks5://"),
		},
		"socks-tls": {
			clash: has(`type: "socks5"`, `tls: true`), premium: has(`tls: true`),
			singbox: skip("TLS"), uri: skip("TLS"),
		},
		"http": {
			clash: has(`type: "http"`).without("tls", "udp"), premium: has(`type: "http"`),
			singbox: has(`"type":"http"`).without(`"tls"`), uri: has("http://" + uu + "@node.example.com:8080#http"),
		},
		"http-tls": {
			clash: has(`type: "http"`, `tls: true`), premium: has(`tls: true`),
			singbox: has(`"tls":{"enabled":true}`), uri: has("https://" + uu + "@node.example.com:8081#http-tls"),
		},
		"mieru-tcp": {
			clash: has(`type: "mieru"`, `transport: "TCP"`), premium: skip("Premium"), singbox: skip("mieru"), uri: skip("mieru"),
		},
		"mieru-udp": {
			clash: has(`transport: "UDP"`), premium: skip("Premium"), singbox: skip("mieru"), uri: skip("mieru"),
		},
		"shadowtls": {
			clash: has(`type: "ss"`, `plugin: "shadow-tls"`, `client-fingerprint: "chrome"`, `cipher: "aes-128-gcm"`, `udp: false`,
				`plugin-opts: {host: "www.example.com", password: "stls-pass-1", version: 3}`),
			premium: skip("Premium"),
			singbox: has(`"type":"shadowsocks"`, `"method":"aes-128-gcm"`, `"detour":"shadowtls · shadowtls"`, `"network":"tcp"`),
			uri:     skip("shadowtls"),
		},
		"trojan-tls-tcp": {
			clash:   has(`type: "trojan"`, `sni: "sni.example.com"`, `skip-cert-verify: true`, `client-fingerprint: "firefox"`, `network: "tcp"`),
			premium: has(`type: "trojan"`, `sni: "sni.example.com"`).without("client-fingerprint"),
			singbox: has(`"server_name":"sni.example.com"`, `"insecure":true`, `"utls":{"enabled":true,"fingerprint":"firefox"}`).without("transport"),
			uri:     has("trojan://", "security=tls", "sni=sni.example.com", "fp=firefox", "allowInsecure=1", "type=tcp"),
		},
		// 没配 utls 的普通 TLS 也下发 chrome（Premium 内核不认 client-fingerprint）
		"trojan-tls-ws": {
			clash:   has(`network: "ws"`, `ws-opts: {headers: {Host: "cdn.example.com"}, path: "/tw"}`, `client-fingerprint: "chrome"`),
			premium: has(`network: "ws"`, `Host: "cdn.example.com"`).without("client-fingerprint"),
			singbox: has(`"transport":{"headers":{"Host":"cdn.example.com"},"path":"/tw","type":"ws"}`, `"utls":{"enabled":true,"fingerprint":"chrome"}`),
			uri:     has("type=ws", "path=%2Ftw", "host=cdn.example.com", "security=tls", "fp=chrome"),
		},
		"trojan-tls-grpc": {
			clash: has(`network: "grpc"`, `grpc-opts: {grpc-service-name: "tgrpc"}`), premium: has(`grpc-service-name: "tgrpc"`),
			singbox: has(`"transport":{"service_name":"tgrpc","type":"grpc"}`),
			uri:     has("type=grpc", "serviceName=tgrpc", "mode=gun"),
		},
		"trojan-tls-grpc-sni": {
			// 设不了 :authority 的客户端会把 SNI 当 :authority，节点校验的是 server_host
			clash: skip(":authority"), premium: skip(":authority"), singbox: skip(":authority"),
			uri: has("serviceName=tg2", "authority=node.example.com", "sni=sni.example.com"),
		},
		"trojan-tls-httpupgrade": {
			clash:   has(`network: "ws"`, `ws-opts: {headers: {Host: "node.example.com"}, path: "/tu", v2ray-http-upgrade: true}`),
			premium: skip("httpupgrade"),
			singbox: has(`"transport":{"host":"node.example.com","path":"/tu","type":"httpupgrade"}`),
			uri:     has("type=httpupgrade", "path=%2Ftu", "host=node.example.com"),
		},
		"trojan-tls-fallback": {
			clash:   has(`type: "trojan"`, `client-fingerprint: "chrome"`, `network: "tcp"`).without("decoy"),
			premium: has(`type: "trojan"`).without("client-fingerprint", "decoy"),
			singbox: has(`"utls":{"enabled":true,"fingerprint":"chrome"}`).without("decoy"),
			uri:     has("trojan://", "security=tls", "fp=chrome").without("decoy"),
		},
		"trojan-reality-fallback": {
			clash:   has(append(realityClash, `client-fingerprint: "chrome"`)...).without("tls: true", "decoy"),
			premium: skip("REALITY"),
			singbox: has(realitySingbox, `"utls":{"enabled":true,"fingerprint":"chrome"}`).without("decoy"),
			uri:     has(append(realityURI, "trojan://", "fp=chrome")...).without("decoy"),
		},
		"trojan-reality": {
			clash:   has(append(realityClash, `sni: "www.example.com"`, `client-fingerprint: "firefox"`)...).without("tls: true"),
			premium: skip("REALITY"),
			singbox: has(realitySingbox, `"utls":{"enabled":true,"fingerprint":"firefox"}`, `"server_name":"www.example.com"`),
			uri:     has(append(realityURI, "trojan://", "fp=firefox")...),
		},
		"vless-reality-vision": {
			clash:   has(append(realityClash, `servername: "www.example.com"`, `flow: "xtls-rprx-vision"`, `client-fingerprint: "chrome"`, `tls: true`)...),
			premium: skip("Premium"),
			singbox: has(realitySingbox, `"flow":"xtls-rprx-vision"`, `"fingerprint":"chrome"`),
			uri:     has(append(realityURI, "flow=xtls-rprx-vision", "fp=chrome", "type=tcp")...).without("security=tls"),
		},
		// 多个 server name / short id：挑哪个由用户决定，见 render_reality_spread_test.go
		"vless-reality-multi": {
			clash:   has(`reality-opts: {public-key: "`+fixtureRealityPub+`", short-id: "`, `servername: "`, `flow: "xtls-rprx-vision"`, `client-fingerprint: "chrome"`),
			premium: skip("Premium"),
			singbox: has(`"reality":{"enabled":true,"public_key":"`+fixtureRealityPub+`","short_id":"`, `"server_name":"`, `"flow":"xtls-rprx-vision"`),
			uri:     has("security=reality", "pbk="+fixtureRealityPub, "sid=", "sni=", "flow=xtls-rprx-vision"),
		},
		"vless-reality-grpc": {
			clash: has(append(realityClash, `grpc-service-name: "vgrpc"`, `client-fingerprint: "safari"`)...), premium: skip("Premium"),
			singbox: has(realitySingbox, `"service_name":"vgrpc"`, `"fingerprint":"safari"`),
			uri:     has(append(realityURI, "serviceName=vgrpc", "fp=safari")...),
		},
		"vless-reality-xhttp": {
			clash:   has(append(realityClash, `network: "xhttp"`, `xhttp-opts: {host: "node.example.com", mode: "auto", path: "/xh"}`)...),
			premium: skip("Premium"),
			singbox: skip("XHTTP"),
			uri:     has(append(realityURI, "type=xhttp", "path=%2Fxh", "mode=auto", "host=node.example.com")...),
		},
		"vless-xhttp-header": {
			clash: skip("session_placement"), premium: skip("Premium"), singbox: skip("session_placement"), uri: skip("session_placement"),
		},
		"vless-reality-xhttp-h3": {
			clash: skip("HTTP/3"), premium: skip("Premium"), singbox: skip("HTTP/3"), uri: skip("HTTP/3"),
		},
		"vless-ws": {
			clash: has(`network: "ws"`, `ws-opts: {headers: {Host: "cdn.example.com"}, path: "/vw"}`).without("tls"), premium: skip("Premium"),
			singbox: has(`"transport":{"headers":{"Host":"cdn.example.com"},"path":"/vw","type":"ws"}`).without(`"tls"`),
			uri:     has("type=ws", "path=%2Fvw", "host=cdn.example.com", "security=none"),
		},
		"vless-ws-nohost": {
			clash: has(`ws-opts: {headers: {Host: "node.example.com"}, path: "/vw"}`), premium: skip("Premium"),
			singbox: has(`"headers":{"Host":"node.example.com"}`), uri: has("host=node.example.com"),
		},
		"vless-httpupgrade": {
			clash: has(`v2ray-http-upgrade: true`, `Host: "cdn.example.com"`, `path: "/vu"`), premium: skip("Premium"),
			singbox: has(`"transport":{"host":"cdn.example.com","path":"/vu","type":"httpupgrade"}`),
			uri:     has("type=httpupgrade", "path=%2Fvu", "host=cdn.example.com"),
		},
		"vless-grpc": {
			clash: has(`grpc-opts: {grpc-service-name: "vg"}`), premium: skip("Premium"),
			singbox: has(`"transport":{"service_name":"vg","type":"grpc"}`), uri: has("serviceName=vg", "authority=node.example.com"),
		},
		"vless-grpc-cdnhost": {
			clash: skip(":authority"), premium: skip("Premium"), singbox: skip(":authority"),
			uri: has("serviceName=vg", "authority=cdn.example.com"),
		},
		"vless-mkcp": {
			clash: skip("mKCP"), premium: skip("Premium"), singbox: skip("mKCP"), uri: has("type=kcp"),
		},
		"vless-mkcp-mask": {
			clash: skip("mKCP"), premium: skip("Premium"), singbox: skip("mKCP"), uri: skip("掩码"),
		},
		"vmess-tcp": {
			clash: has(`type: "vmess"`, `cipher: "auto"`, `network: "tcp"`), premium: has(`type: "vmess"`),
			singbox: has(`"type":"vmess"`, `"security":"auto"`).without("transport"),
			uri:     has(`"net":"tcp"`, `"id":"`+fixtureUUID+`"`),
		},
		// 以下是存量形状（legacyFixtures）：新写入被拒，渲染照旧
		"vless-tcp-plain": {
			clash: has(`type: "vless"`, `network: "tcp"`).without("tls"), premium: skip("Premium"),
			singbox: has(`"type":"vless"`).without("transport", `"tls"`), uri: has("vless://", "security=none", "type=tcp"),
		},
		"trojan-legacy-cert": {
			clash: has(`type: "trojan"`, `client-fingerprint: "chrome"`), premium: has(`type: "trojan"`),
			singbox: has(`"type":"trojan"`, `"utls":{"enabled":true,"fingerprint":"chrome"}`), uri: has("trojan://", "security=tls", "fp=chrome"),
		},
		"vmess-ws": {
			clash: has(`ws-opts: {headers: {Host: "cdn.example.com"}, path: "/mw"}`), premium: has(`network: "ws"`),
			singbox: has(`"transport":{"headers":{"Host":"cdn.example.com"},"path":"/mw","type":"ws"}`),
			uri:     has(`"net":"ws"`, `"host":"cdn.example.com"`, `"path":"/mw"`),
		},
		"vmess-grpc": {
			clash: has(`grpc-service-name: "mg"`), premium: has(`grpc-service-name: "mg"`),
			singbox: has(`"transport":{"service_name":"mg","type":"grpc"}`),
			uri:     has(`"net":"grpc"`, `"path":"mg"`, `"type":"gun"`),
		},
		"vmess-httpupgrade": {
			clash: has(`v2ray-http-upgrade: true`, `Host: "node.example.com"`), premium: skip("httpupgrade"),
			singbox: has(`"transport":{"host":"node.example.com","path":"/mu","type":"httpupgrade"}`),
			uri:     has(`"net":"httpupgrade"`, `"host":"node.example.com"`, `"path":"/mu"`),
		},
		"vmess-xhttp": {
			clash: skip("只支持 vless"), premium: skip("xhttp"), singbox: skip("XHTTP"),
			uri: has(`"net":"xhttp"`, `"path":"/mx"`, `"type":"auto"`),
		},
	}
}

func TestRenderMatrixCoversEveryFormFixture(t *testing.T) {
	matrix := renderMatrix()
	fixtures := append(formFixtures(), legacyFixtures()...)
	if len(matrix) != len(fixtures) {
		t.Fatalf("matrix has %d rows, fixtures %d: every fixture needs a row", len(matrix), len(fixtures))
	}
	var raw []Node
	for _, f := range fixtures {
		raw = append(raw, formNode(t, f))
	}
	nodes := kernelShapedNodes(uniqueNodeNames(raw))
	for _, n := range nodes {
		row, ok := matrix[n.Name]
		if !ok {
			t.Errorf("%s: no matrix row", n.Name)
			continue
		}
		clash, clashSkip := nodeToClash(n, fixtureUUID, false)
		checkCell(t, n.Name, "clash", row.clash, clashText(clash), clashSkip)
		premium, premiumSkip := nodeToClash(n, fixtureUUID, true)
		checkCell(t, n.Name, "clash-premium", row.premium, clashText(premium), premiumSkip)
		sb, sbSkip := nodeToSingbox(n, fixtureUUID)
		checkCell(t, n.Name, "singbox", row.singbox, singboxText(t, sb, n), sbSkip)
		uri, uriSkip := nodeToURI(n, fixtureUUID)
		checkCell(t, n.Name, "uri", row.uri, uriText(t, uri), uriSkip)
	}
}

func checkCell(t *testing.T, id, format string, want cell, got, skipReason string) {
	t.Helper()
	if want.skip != "" {
		if got != "" {
			t.Errorf("%s × %s: want skip (%s), got %s", id, format, want.skip, got)
		} else if !strings.Contains(skipReason, want.skip) {
			t.Errorf("%s × %s: skip reason %q does not mention %q", id, format, skipReason, want.skip)
		}
		return
	}
	if got == "" {
		t.Errorf("%s × %s: unexpectedly skipped: %s", id, format, skipReason)
		return
	}
	for _, part := range want.has {
		if !strings.Contains(got, part) {
			t.Errorf("%s × %s: missing %s\n  got: %s", id, format, part, got)
		}
	}
	for _, part := range want.not {
		if strings.Contains(got, part) {
			t.Errorf("%s × %s: must not contain %s\n  got: %s", id, format, part, got)
		}
	}
}

func clashText(p map[string]any) string {
	if p == nil {
		return ""
	}
	return inlineYAML(p)
}

func singboxText(t *testing.T, o map[string]any, n Node) string {
	t.Helper()
	if o == nil {
		return ""
	}
	if n.Type == "shadowtls" {
		// detour 由 renderSingbox 填，这里按它的取名规则补上，单测只看内层
		o["detour"] = n.Name + " · shadowtls"
	}
	b, err := json.Marshal(o)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func uriText(t *testing.T, uri string) string {
	t.Helper()
	if body, ok := strings.CutPrefix(uri, "vmess://"); ok {
		raw, err := base64.StdEncoding.DecodeString(body)
		if err != nil {
			t.Fatalf("vmess link is not base64: %v", err)
		}
		return uri + " " + string(raw)
	}
	return uri
}
