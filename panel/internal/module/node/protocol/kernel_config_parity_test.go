package protocol

// KernelConfig 与旧实现逐项对照。
//
// panel/internal/module/subscription/render/testdata/nodes.json 是旧 KernelConfig 的输出
// （panel/tools/subgolden 生成）：输入是下面这组后台表单形状（与 tools/subgolden/fixtures.go
// 逐字一致），经 json.Unmarshal 后交给旧的翻译。这里用同一组输入调新包，结果必须逐项相同——
// 下发给节点的配置文档与订阅渲染都经它，翻译差一个字段就是节点或客户端静默连不上。
//
// 全部是虚构值：域名用 example.com，REALITY 密钥对是测试专用的一对。

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

type parityFixture struct {
	group  string
	name   string
	typ    string
	port   int
	config string // 表单形状的 JSON 文本
}

const (
	parityRealityPri = "u03CTWUF4qEGT24p7bBnBU7OM0VUUrHThNjlklFEfcg"
	parityRealityPub = "EVmCoU4Swh5-Pv9WHB68iac25hNF2VwABHEnIZOTfyE"
)

func parityRealitySettings() string {
	return `"reality_settings":{"dest":"www.example.com:443","server_name":"www.example.com",` +
		`"private_key":"` + parityRealityPri + `","public_key":"` + parityRealityPub + `","short_id":"0a1b2c3d"}`
}

// 证书放在约定目录下（docs/node-certificates.md），后台校验只收这个目录里的路径。
const parityCertPaths = `"cert_path":"/etc/pandora-native/certs/example.com/fullchain.pem","key_path":"/etc/pandora-native/certs/example.com/privkey.pem"`

// realityMultiSettings 配了两个 server name、三个 short id：订阅按用户分散。
func parityRealityMultiSettings() string {
	return `"reality_settings":{"dest":"www.example.com:443","server_name":["www.example.com","static.example.com"],` +
		`"private_key":"` + parityRealityPri + `","public_key":"` + parityRealityPub + `","short_id":["0a1b2c3d","4e5f","6a7b8c9d0e1f2a3b"]}`
}

func parityFixtures() []parityFixture {
	out := make([]parityFixture, 0, 128)

	// form：后台表单能存进库的形状
	for _, f := range []struct {
		id, typ string
		port    int
		config  string
	}{
		{"ss-aes128", "shadowsocks", 8388, `{"cipher":"aes-128-gcm"}`},
		{"ss-chacha", "shadowsocks", 8389, `{"cipher":"chacha20-ietf-poly1305"}`},
		{"hy2-obfs", "hysteria2", 8443, `{` + parityCertPaths + `,"obfs":{"type":"salamander","password":"obfs-pass-1"},"bandwidth":{"up":100,"down":200},"tls_settings":{"server_name":"sni.example.com","allow_insecure":true}}`},
		{"hy2-plain", "hysteria2", 8444, `{` + parityCertPaths + `}`},
		{"tuic", "tuic", 5443, `{` + parityCertPaths + `,"congestion_control":"cubic","tls_settings":{"server_name":"sni.example.com"}}`},
		{"anytls", "anytls", 6443, `{"tls":true,` + parityCertPaths + `,"tls_settings":{"server_name":"sni.example.com","allow_insecure":true}}`},
		{"anytls-utls-fallback", "anytls", 6445, `{"tls":true,` + parityCertPaths + `,"utls":"safari","fallback":"decoy.example.net:80","tls_settings":{"server_name":"sni.example.com","allow_insecure":true}}`},
		{"naive", "naive", 9443, `{"tls":true,` + parityCertPaths + `,"tls_settings":{"server_name":"sni.example.com"}}`},
		{"naive-insecure", "naive", 9444, `{"tls":true,` + parityCertPaths + `,"tls_settings":{"allow_insecure":true}}`},
		{"naive-fallback", "naive", 9445, `{"tls":true,` + parityCertPaths + `,"fallback":"decoy.example.net:80","tls_settings":{"server_name":"sni.example.com"}}`},
		{"juicity", "juicity", 8445, `{` + parityCertPaths + `,"congestion_control":"bbr"}`},
		{"socks", "socks", 1080, `{"network":"tcp"}`},
		{"socks-udp", "socks", 1081, `{"network":"udp"}`},
		{"socks-tls", "socks", 1082, `{"tls":true,` + parityCertPaths + `}`},
		{"http", "http", 8080, `{}`},
		{"http-tls", "http", 8081, `{"tls":true,` + parityCertPaths + `}`},
		{"mieru-tcp", "mieru", 2999, `{"transport":"TCP"}`},
		{"mieru-udp", "mieru", 3000, `{"transport":"UDP"}`},
		{"shadowtls", "shadowtls", 4443, `{"password":"stls-pass-1","handshake_server":"www.example.com","server_port":443,"method":"aes-128-gcm","version":3}`},
		{"trojan-tls-tcp", "trojan", 7443, `{"tls":1,"network":"tcp",` + parityCertPaths + `,"utls":"firefox","tls_settings":{"server_name":"sni.example.com","allow_insecure":true}}`},
		{"trojan-tls-ws", "trojan", 7444, `{"tls":1,"network":"ws",` + parityCertPaths + `,"network_settings":{"path":"/tw","headers":{"Host":"cdn.example.com"}}}`},
		{"trojan-tls-grpc", "trojan", 7445, `{"tls":1,"network":"grpc",` + parityCertPaths + `,"network_settings":{"serviceName":"tgrpc"}}`},
		{"trojan-tls-grpc-sni", "trojan", 7448, `{"tls":1,"network":"grpc",` + parityCertPaths + `,"network_settings":{"serviceName":"tg2"},"tls_settings":{"server_name":"sni.example.com"}}`},
		{"trojan-tls-httpupgrade", "trojan", 7447, `{"tls":1,"network":"httpupgrade",` + parityCertPaths + `,"network_settings":{"path":"/tu"}}`},
		{"trojan-tls-fallback", "trojan", 7449, `{"tls":1,"network":"tcp",` + parityCertPaths + `,"fallback":"decoy.example.net:80","tls_settings":{"server_name":"sni.example.com","allow_insecure":true}}`},
		{"trojan-reality", "trojan", 7446, `{"tls":2,"network":"tcp","utls":"firefox",` + parityRealitySettings() + `}`},
		{"trojan-reality-fallback", "trojan", 7450, `{"tls":2,"network":"tcp","fallback":"decoy.example.net:80",` + parityRealitySettings() + `}`},
		{"vless-reality-vision", "vless", 443, `{"tls":2,"network":"tcp","flow":"xtls-rprx-vision","utls":"chrome",` + parityRealitySettings() + `}`},
		{"vless-reality-multi", "vless", 449, `{"tls":2,"network":"tcp","flow":"xtls-rprx-vision",` + parityRealityMultiSettings() + `}`},
		{"vless-reality-grpc", "vless", 444, `{"tls":2,"network":"grpc","utls":"safari","network_settings":{"serviceName":"vgrpc"},` + parityRealitySettings() + `}`},
		{"vless-reality-xhttp", "vless", 445, `{"tls":2,"network":"xhttp","network_settings":{"path":"/xh","mode":"auto"},` + parityRealitySettings() + `}`},
		{"vless-xhttp-header", "vless", 447, `{"tls":2,"network":"xhttp","session_placement":"header","network_settings":{"path":"/xh"},` + parityRealitySettings() + `}`},
		{"vless-ws", "vless", 10080, `{"tls":0,"network":"ws","network_settings":{"path":"/vw","headers":{"Host":"cdn.example.com"}}}`},
		{"vless-ws-nohost", "vless", 10084, `{"tls":0,"network":"ws","network_settings":{"path":"/vw"}}`},
		{"vless-httpupgrade", "vless", 10081, `{"tls":0,"network":"httpupgrade","network_settings":{"path":"/vu","headers":{"Host":"cdn.example.com"}}}`},
		{"vless-grpc", "vless", 10082, `{"tls":0,"network":"grpc","network_settings":{"serviceName":"vg"}}`},
		{"vless-grpc-cdnhost", "vless", 10085, `{"tls":0,"network":"grpc","network_settings":{"serviceName":"vg","headers":{"Host":"cdn.example.com"}}}`},
		{"vless-mkcp", "vless", 10083, `{"tls":0,"network":"mkcp"}`},
		{"vless-mkcp-mask", "vless", 10086, `{"tls":0,"network":"mkcp","mask":"mkcp-aes128gcm","mask_password":"mask-pass-1"}`},
		{"vmess-ws", "vmess", 10091, `{"tls":0,"network":"ws","network_settings":{"path":"/mw","headers":{"Host":"cdn.example.com"}}}`},
		{"vmess-tcp", "vmess", 10090, `{"tls":0,"network":"tcp"}`},
		{"vmess-grpc", "vmess", 10092, `{"tls":0,"network":"grpc","network_settings":{"serviceName":"mg"}}`},
		{"vmess-httpupgrade", "vmess", 10093, `{"tls":0,"network":"httpupgrade","network_settings":{"path":"/mu"}}`},
		{"vmess-xhttp", "vmess", 10094, `{"tls":0,"network":"xhttp","network_settings":{"path":"/mx"}}`},
	} {
		out = append(out, parityFixture{group: "form", name: f.id, typ: f.typ, port: f.port, config: f.config})
	}

	// legacy：存量里还有、新写入已被拒的形状
	for _, f := range []struct {
		id, typ string
		port    int
		config  string
	}{
		{"vless-tcp-plain", "vless", 10087, `{"tls":0,"network":"tcp"}`},
		{"vless-reality-xhttp-h3", "vless", 448, `{"tls":2,"network":"xhttp-h3","network_settings":{"path":"/h3"},` + parityRealitySettings() + `}`},
		{"trojan-legacy-cert", "trojan", 7451, `{"tls":1,"network":"tcp","cert_path":"/etc/pdnd/c.pem","key_path":"/etc/pdnd/k.pem"}`},
	} {
		out = append(out, parityFixture{group: "legacy", name: f.id, typ: f.typ, port: f.port, config: f.config})
	}

	// extra：render_shared_test.go 的 raw 表 10 项（嵌套配置、重名、保留名、空名）
	sharedRaw := []struct {
		name, typ, config string
	}{
		{"香港", "vless", `{"tls":2,"network":"grpc","utls":"chrome","network_settings":{"serviceName":"svc"},"reality_settings":{"dest":"www.example.com:443","server_name":"a.example.com","private_key":"k","public_key":"pk","short_id":"01ab"}}`},
		{"香港", "vmess", `{"tls":0,"network":"ws","network_settings":{"path":"/ws","headers":{"Host":"edge.example.com"}}}`},
		{"自动选择", "trojan", `{"tls":1,"network":"tcp","cert_path":"/c","key_path":"/k","tls_settings":{"server_name":"edge.example.com","allow_insecure":true}}`},
		{"", "hysteria2", `{"cert_path":"/c","key_path":"/k","obfs":{"type":"salamander","password":"secret"},"bandwidth":{"up":10,"down":20}}`},
		{"东京", "shadowsocks", `{"cipher":"aes-128-gcm"}`},
		{"东京", "tuic", `{"cert_path":"/c","key_path":"/k","congestion_control":"bbr"}`},
		{"新加坡", "vless", `{"tls":0,"network":"mkcp","mask":"wechat-video","mask_password":"x"}`},
		{"首尔", "anytls", `{"tls":true,"cert_path":"/c","key_path":"/k"}`},
		{"首尔", "shadowtls", `{"password":"p","handshake_server":"www.example.com","server_port":443,"version":3}`},
		{"旧", "vless", `{"network":"tcp","security":"reality","public_key":"pk","server_names":["a.example.com","b.example.com"],"short_ids":["01ab"],"flow":"xtls-rprx-vision"}`},
	}
	for i, r := range sharedRaw {
		out = append(out, parityFixture{group: "extra", name: r.name, typ: r.typ, port: 20000 + i, config: r.config})
	}

	// 名字回归：撞后缀、保留名、全空白、撞 ShadowTLS 外层 tag
	ssCfg := `{"cipher":"aes-128-gcm"}`
	for i, name := range []string{
		"香港 · 1", "direct", "DIRECT", "REJECT", "block", "节点选择", "   ", "shadowtls · shadowtls",
	} {
		out = append(out, parityFixture{group: "extra", name: name, typ: "shadowsocks", port: 21000 + i, config: ssCfg})
	}

	out = append(out, parityFixture{group: "extra", name: "plain-anytls", typ: "anytls", port: 22000, config: `{}`})

	out = append(out, parityFixture{
		group: "extra", name: "hk-01", typ: "vless", port: 22001,
		config: `{"network":"tcp","security":"reality","dest":"www.example.com:443","server_names":["www.example.com"],"public_key":"` + parityRealityPub + `","short_ids":["0a1b2c3d"],"fingerprint":"chrome"}`,
	})
	out = append(out, parityFixture{
		group: "extra", name: "hk-02", typ: "vless", port: 22002,
		config: `{"network":"tcp","security":"reality","dest":"www.example.com:443","server_names":"www.example.com","public_key":"` + parityRealityPub + `","short_ids":"0a1b2c3d","fingerprint":"chrome"}`,
	})

	return out
}

// parityNodeJSON 是 nodes.json 的一项：config 已是内核形状。
type parityNodeJSON struct {
	Group  string         `json:"group"`
	Name   string         `json:"name"`
	Type   string         `json:"type"`
	Port   int            `json:"port"`
	Config map[string]any `json:"config"`
}

func parityLegacyOutput(t *testing.T) map[int]parityNodeJSON {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "subscription", "render", "testdata", "nodes.json"))
	if err != nil {
		t.Fatalf("读取旧 KernelConfig 输出 nodes.json 失败：%v", err)
	}
	var nodes []parityNodeJSON
	if err := json.Unmarshal(raw, &nodes); err != nil {
		t.Fatalf("nodes.json 不是合法 JSON：%v", err)
	}
	out := make(map[int]parityNodeJSON, len(nodes))
	for _, n := range nodes {
		if _, dup := out[n.Port]; dup {
			t.Fatalf("nodes.json 端口 %d 重复，按端口对照的前提不成立", n.Port)
		}
		out[n.Port] = n
	}
	return out
}

func parityConfig(t *testing.T, f parityFixture) map[string]any {
	t.Helper()
	var cfg map[string]any
	if err := json.Unmarshal([]byte(f.config), &cfg); err != nil {
		t.Fatalf("%s：夹具不是 JSON：%v", f.name, err)
	}
	return cfg
}

// parityNormalize 经一次 JSON 往返，抹掉 []string 与 []any 之类的 Go 类型差别。
func parityNormalize(t *testing.T, v any) (any, string) {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("序列化失败：%v", err)
	}
	var out any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("反序列化失败：%v", err)
	}
	return out, string(b)
}

func TestKernelConfigMatchesLegacyOutput(t *testing.T) {
	legacy := parityLegacyOutput(t)
	fixtures := parityFixtures()
	if len(fixtures) != len(legacy) {
		t.Fatalf("夹具 %d 项，nodes.json %d 项：两边必须一一对应", len(fixtures), len(legacy))
	}
	for _, f := range fixtures {
		t.Run(fmt.Sprintf("%d-%s", f.port, f.name), func(t *testing.T) {
			want, ok := legacy[f.port]
			if !ok {
				t.Fatalf("nodes.json 里没有端口 %d", f.port)
			}
			if want.Group != f.group || want.Name != f.name || want.Type != CanonicalType(f.typ) {
				t.Fatalf("nodes.json 端口 %d 是 %s/%q/%s，夹具是 %s/%q/%s",
					f.port, want.Group, want.Name, want.Type, f.group, f.name, f.typ)
			}
			got, gotJSON := parityNormalize(t, KernelConfig(f.typ, parityConfig(t, f)))
			exp, expJSON := parityNormalize(t, want.Config)
			if !reflect.DeepEqual(got, exp) {
				t.Errorf("%s 翻译结果与旧实现不同：\n  新 %s\n  旧 %s", f.name, gotJSON, expJSON)
			}
		})
	}
}

// KernelConfig 不改调用方的 map（订阅渲染与配置下发拿同一份库里读出的配置）。
func TestKernelConfigDoesNotMutateInput(t *testing.T) {
	for _, f := range parityFixtures() {
		cfg := parityConfig(t, f)
		before := parityConfig(t, f)
		_ = KernelConfig(f.typ, cfg)
		if !reflect.DeepEqual(cfg, before) {
			t.Errorf("%s：KernelConfig 改了输入：%v", f.name, cfg)
		}
	}
}

// form 组是后台真能保存的配置，否则对照的是一个永远不会出现的形状。
func TestFormFixturesPassValidate(t *testing.T) {
	for _, f := range parityFixtures() {
		if f.group != "form" {
			continue
		}
		version, fields := Validate(f.typ, f.port, json.RawMessage(f.config))
		if version != schemaVersion || len(fields) != 0 {
			t.Errorf("%s：表单形状被拒：version=%d fields=%v", f.name, version, fields)
		}
	}
}

// legacy 组是收紧前存进去的形状：新写入必须被拒，但照常翻译与下发（上面的对照覆盖）。
func TestLegacyParityFixturesRejectedForNewWrites(t *testing.T) {
	for _, f := range parityFixtures() {
		if f.group != "legacy" {
			continue
		}
		if _, fields := Validate(f.typ, f.port, json.RawMessage(f.config)); len(fields) == 0 {
			t.Errorf("%s：存量形状仍能写入，应挪回 form 组", f.name)
		}
	}
}

func TestWarningsOnParityFixtures(t *testing.T) {
	want := map[string]int{"vmess-tcp": 1, "vmess-ws": 0, "vless-reality-vision": 0}
	for _, f := range parityFixtures() {
		n, ok := want[f.name]
		if !ok || f.group != "form" {
			continue
		}
		if got := Warnings(f.typ, json.RawMessage(f.config)); len(got) != n {
			t.Errorf("%s：Warnings = %v，want %d 条", f.name, got, n)
		}
		delete(want, f.name)
	}
	if len(want) != 0 {
		t.Errorf("夹具里缺这些节点：%v", want)
	}
}
