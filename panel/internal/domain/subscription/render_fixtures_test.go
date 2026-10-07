package subscription

import (
	"encoding/json"
	"testing"

	"github.com/aegispanel/aegis/internal/domain/nodefabric"
)

// 渲染测试的夹具一律用后台表单写进库的形状（xboard：tls 三态、
// reality_settings.*、network_settings.*、cipher、utls、tls_settings.*）。
//
// 以前的夹具是内核扁平形状（security、public_key、method…），而库里从来不存
// 这个形状：渲染器读错字段名、REALITY 被渲染成普通 TLS，测试照样是绿的。
// TestFormFixturesAreAcceptedByAdminValidation 保证每个夹具都是后台真能保存的
// 配置；旧扁平形状只留在 reality_render_test.go 一组回归里。
//
// 全部是虚构值：域名用 example.com，REALITY 密钥对是测试专用的一对。

const (
	fixtureUUID       = "3f1c2b4a-5d6e-4f70-8a9b-0c1d2e3f4a5b"
	fixtureHost       = "node.example.com"
	fixtureRealityPri = "u03CTWUF4qEGT24p7bBnBU7OM0VUUrHThNjlklFEfcg"
	fixtureRealityPub = "EVmCoU4Swh5-Pv9WHB68iac25hNF2VwABHEnIZOTfyE"
)

type formFixture struct {
	id     string
	typ    string
	port   int
	config string
}

func realitySettings() string {
	return `"reality_settings":{"dest":"www.example.com:443","server_name":"www.example.com",` +
		`"private_key":"` + fixtureRealityPri + `","public_key":"` + fixtureRealityPub + `","short_id":"0a1b2c3d"}`
}

const certPaths = `"cert_path":"/etc/pdnd/c.pem","key_path":"/etc/pdnd/k.pem"`

func formFixtures() []formFixture {
	return []formFixture{
		{"ss-aes128", "shadowsocks", 8388, `{"cipher":"aes-128-gcm"}`},
		{"ss-chacha", "shadowsocks", 8389, `{"cipher":"chacha20-ietf-poly1305"}`},
		{"hy2-obfs", "hysteria2", 8443, `{` + certPaths + `,"obfs":{"type":"salamander","password":"obfs-pass-1"},"bandwidth":{"up":100,"down":200},"tls_settings":{"server_name":"sni.example.com","allow_insecure":true}}`},
		{"hy2-plain", "hysteria2", 8444, `{` + certPaths + `}`},
		{"tuic", "tuic", 5443, `{` + certPaths + `,"congestion_control":"cubic","tls_settings":{"server_name":"sni.example.com"}}`},
		{"anytls", "anytls", 6443, `{"tls":true,` + certPaths + `,"tls_settings":{"server_name":"sni.example.com","allow_insecure":true}}`},
		{"naive", "naive", 9443, `{"tls":true,` + certPaths + `,"tls_settings":{"server_name":"sni.example.com"}}`},
		{"naive-insecure", "naive", 9444, `{"tls":true,` + certPaths + `,"tls_settings":{"allow_insecure":true}}`},
		{"juicity", "juicity", 8445, `{` + certPaths + `,"congestion_control":"bbr"}`},
		{"socks", "socks", 1080, `{"network":"tcp"}`},
		{"socks-udp", "socks", 1081, `{"network":"udp"}`},
		{"socks-tls", "socks", 1082, `{"tls":true,` + certPaths + `}`},
		{"http", "http", 8080, `{}`},
		{"http-tls", "http", 8081, `{"tls":true,` + certPaths + `}`},
		{"mieru-tcp", "mieru", 2999, `{"transport":"TCP"}`},
		{"mieru-udp", "mieru", 3000, `{"transport":"UDP"}`},
		{"shadowtls", "shadowtls", 4443, `{"password":"stls-pass-1","handshake_server":"www.example.com","server_port":443,"method":"aes-128-gcm","version":3}`},
		{"trojan-tls-tcp", "trojan", 7443, `{"tls":1,"network":"tcp",` + certPaths + `,"utls":"firefox","tls_settings":{"server_name":"sni.example.com","allow_insecure":true}}`},
		{"trojan-tls-ws", "trojan", 7444, `{"tls":1,"network":"ws",` + certPaths + `,"network_settings":{"path":"/tw","headers":{"Host":"cdn.example.com"}}}`},
		{"trojan-tls-grpc", "trojan", 7445, `{"tls":1,"network":"grpc",` + certPaths + `,"network_settings":{"serviceName":"tgrpc"}}`},
		{"trojan-tls-grpc-sni", "trojan", 7448, `{"tls":1,"network":"grpc",` + certPaths + `,"network_settings":{"serviceName":"tg2"},"tls_settings":{"server_name":"sni.example.com"}}`},
		{"trojan-tls-httpupgrade", "trojan", 7447, `{"tls":1,"network":"httpupgrade",` + certPaths + `,"network_settings":{"path":"/tu"}}`},
		{"trojan-reality", "trojan", 7446, `{"tls":2,"network":"tcp","utls":"firefox",` + realitySettings() + `}`},
		{"vless-reality-vision", "vless", 443, `{"tls":2,"network":"tcp","flow":"xtls-rprx-vision","utls":"chrome",` + realitySettings() + `}`},
		{"vless-reality-grpc", "vless", 444, `{"tls":2,"network":"grpc","utls":"safari","network_settings":{"serviceName":"vgrpc"},` + realitySettings() + `}`},
		{"vless-reality-xhttp", "vless", 445, `{"tls":2,"network":"xhttp","network_settings":{"path":"/xh","mode":"auto"},` + realitySettings() + `}`},
		{"vless-xhttp-header", "vless", 447, `{"tls":2,"network":"xhttp","session_placement":"header","network_settings":{"path":"/xh"},` + realitySettings() + `}`},
		{"vless-reality-xhttp-h3", "vless", 448, `{"tls":2,"network":"xhttp-h3","network_settings":{"path":"/h3"},` + realitySettings() + `}`},
		{"vless-ws", "vless", 10080, `{"tls":0,"network":"ws","network_settings":{"path":"/vw","headers":{"Host":"cdn.example.com"}}}`},
		{"vless-ws-nohost", "vless", 10084, `{"tls":0,"network":"ws","network_settings":{"path":"/vw"}}`},
		{"vless-httpupgrade", "vless", 10081, `{"tls":0,"network":"httpupgrade","network_settings":{"path":"/vu","headers":{"Host":"cdn.example.com"}}}`},
		{"vless-grpc", "vless", 10082, `{"tls":0,"network":"grpc","network_settings":{"serviceName":"vg"}}`},
		{"vless-grpc-cdnhost", "vless", 10085, `{"tls":0,"network":"grpc","network_settings":{"serviceName":"vg","headers":{"Host":"cdn.example.com"}}}`},
		{"vless-mkcp", "vless", 10083, `{"tls":0,"network":"mkcp"}`},
		{"vless-mkcp-mask", "vless", 10086, `{"tls":0,"network":"mkcp","mask":"mkcp-aes128gcm","mask_password":"mask-pass-1"}`},
		{"vmess-tcp", "vmess", 10090, `{"tls":0,"network":"tcp"}`},
		{"vmess-ws", "vmess", 10091, `{"tls":0,"network":"ws","network_settings":{"path":"/mw","headers":{"Host":"cdn.example.com"}}}`},
		{"vmess-grpc", "vmess", 10092, `{"tls":0,"network":"grpc","network_settings":{"serviceName":"mg"}}`},
		{"vmess-httpupgrade", "vmess", 10093, `{"tls":0,"network":"httpupgrade","network_settings":{"path":"/mu"}}`},
		{"vmess-xhttp", "vmess", 10094, `{"tls":0,"network":"xhttp","network_settings":{"path":"/mx"}}`},
	}
}

// formNode 把夹具变成 listEligibleNodesTx 给出的 Node（配置从 JSON 反序列化，
// 嵌套是 map[string]any 与 []any，和库里读出来的一样）。
func formNode(t *testing.T, f formFixture) Node {
	t.Helper()
	n := Node{Name: f.id, Type: f.typ, Host: fixtureHost, Port: f.port}
	if err := json.Unmarshal([]byte(f.config), &n.Config); err != nil {
		t.Fatalf("%s: fixture is not JSON: %v", f.id, err)
	}
	return n
}

func formNodes(t *testing.T) []Node {
	t.Helper()
	var out []Node
	for _, f := range formFixtures() {
		out = append(out, formNode(t, f))
	}
	return out
}

// 每个夹具都必须是后台真能保存的配置，否则测的是一个永远不会出现的形状。
func TestFormFixturesAreAcceptedByAdminValidation(t *testing.T) {
	for _, f := range formFixtures() {
		version, fields := nodefabric.ValidateAdminProtocolConfig(f.typ, "pandora-native", f.port, json.RawMessage(f.config))
		if version != nodefabric.StableProtocolSchemaVersion || len(fields) != 0 {
			t.Errorf("%s: admin validation rejected fixture: version=%d fields=%v", f.id, version, fields)
		}
	}
}
