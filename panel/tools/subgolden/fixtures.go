package main

// 订阅渲染 golden 夹具表。form/legacy 与 internal/domain/subscription/render_fixtures_test.go 逐字一致。

type fixture struct {
	group  string
	name   string
	typ    string
	port   int
	config string // 表单形状的 JSON 文本
}

const (
	fixtureUUID       = "3f1c2b4a-5d6e-4f70-8a9b-0c1d2e3f4a5b"
	fixtureHost       = "node.example.com"
	fixtureRealityPri = "u03CTWUF4qEGT24p7bBnBU7OM0VUUrHThNjlklFEfcg"
	fixtureRealityPub = "EVmCoU4Swh5-Pv9WHB68iac25hNF2VwABHEnIZOTfyE"
)

func realitySettings() string {
	return `"reality_settings":{"dest":"www.example.com:443","server_name":"www.example.com",` +
		`"private_key":"` + fixtureRealityPri + `","public_key":"` + fixtureRealityPub + `","short_id":"0a1b2c3d"}`
}

// 证书放在约定目录下（docs/node-certificates.md），后台校验只收这个目录里的路径。
const certPaths = `"cert_path":"/etc/pandora-native/certs/example.com/fullchain.pem","key_path":"/etc/pandora-native/certs/example.com/privkey.pem"`

// realityMultiSettings 配了两个 server name、三个 short id：订阅按用户分散。
func realityMultiSettings() string {
	return `"reality_settings":{"dest":"www.example.com:443","server_name":["www.example.com","static.example.com"],` +
		`"private_key":"` + fixtureRealityPri + `","public_key":"` + fixtureRealityPub + `","short_id":["0a1b2c3d","4e5f","6a7b8c9d0e1f2a3b"]}`
}

func fixtures() []fixture {
	out := make([]fixture, 0, 128)

	// form：后台表单能存进库的形状
	for _, f := range []struct {
		id, typ string
		port    int
		config  string
	}{
		{"ss-aes128", "shadowsocks", 8388, `{"cipher":"aes-128-gcm"}`},
		{"ss-chacha", "shadowsocks", 8389, `{"cipher":"chacha20-ietf-poly1305"}`},
		{"hy2-obfs", "hysteria2", 8443, `{` + certPaths + `,"obfs":{"type":"salamander","password":"obfs-pass-1"},"bandwidth":{"up":100,"down":200},"tls_settings":{"server_name":"sni.example.com","allow_insecure":true}}`},
		{"hy2-plain", "hysteria2", 8444, `{` + certPaths + `}`},
		{"tuic", "tuic", 5443, `{` + certPaths + `,"congestion_control":"cubic","tls_settings":{"server_name":"sni.example.com"}}`},
		{"anytls", "anytls", 6443, `{"tls":true,` + certPaths + `,"tls_settings":{"server_name":"sni.example.com","allow_insecure":true}}`},
		{"anytls-utls-fallback", "anytls", 6445, `{"tls":true,` + certPaths + `,"utls":"safari","fallback":"decoy.example.net:80","tls_settings":{"server_name":"sni.example.com","allow_insecure":true}}`},
		{"naive", "naive", 9443, `{"tls":true,` + certPaths + `,"tls_settings":{"server_name":"sni.example.com"}}`},
		{"naive-insecure", "naive", 9444, `{"tls":true,` + certPaths + `,"tls_settings":{"allow_insecure":true}}`},
		{"naive-fallback", "naive", 9445, `{"tls":true,` + certPaths + `,"fallback":"decoy.example.net:80","tls_settings":{"server_name":"sni.example.com"}}`},
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
		{"trojan-tls-fallback", "trojan", 7449, `{"tls":1,"network":"tcp",` + certPaths + `,"fallback":"decoy.example.net:80","tls_settings":{"server_name":"sni.example.com","allow_insecure":true}}`},
		{"trojan-reality", "trojan", 7446, `{"tls":2,"network":"tcp","utls":"firefox",` + realitySettings() + `}`},
		{"trojan-reality-fallback", "trojan", 7450, `{"tls":2,"network":"tcp","fallback":"decoy.example.net:80",` + realitySettings() + `}`},
		{"vless-reality-vision", "vless", 443, `{"tls":2,"network":"tcp","flow":"xtls-rprx-vision","utls":"chrome",` + realitySettings() + `}`},
		{"vless-reality-multi", "vless", 449, `{"tls":2,"network":"tcp","flow":"xtls-rprx-vision",` + realityMultiSettings() + `}`},
		{"vless-reality-grpc", "vless", 444, `{"tls":2,"network":"grpc","utls":"safari","network_settings":{"serviceName":"vgrpc"},` + realitySettings() + `}`},
		{"vless-reality-xhttp", "vless", 445, `{"tls":2,"network":"xhttp","network_settings":{"path":"/xh","mode":"auto"},` + realitySettings() + `}`},
		{"vless-xhttp-header", "vless", 447, `{"tls":2,"network":"xhttp","session_placement":"header","network_settings":{"path":"/xh"},` + realitySettings() + `}`},
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
		out = append(out, fixture{group: "form", name: f.id, typ: f.typ, port: f.port, config: f.config})
	}

	// legacy：存量里还有、新写入已被拒的形状
	for _, f := range []struct {
		id, typ string
		port    int
		config  string
	}{
		{"vless-tcp-plain", "vless", 10087, `{"tls":0,"network":"tcp"}`},
		{"vless-reality-xhttp-h3", "vless", 448, `{"tls":2,"network":"xhttp-h3","network_settings":{"path":"/h3"},` + realitySettings() + `}`},
		{"trojan-legacy-cert", "trojan", 7451, `{"tls":1,"network":"tcp","cert_path":"/etc/pdnd/c.pem","key_path":"/etc/pdnd/k.pem"}`},
	} {
		out = append(out, fixture{group: "legacy", name: f.id, typ: f.typ, port: f.port, config: f.config})
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
		out = append(out, fixture{group: "extra", name: r.name, typ: r.typ, port: 20000 + i, config: r.config})
	}

	// 名字回归：撞后缀、保留名、全空白、撞 ShadowTLS 外层 tag
	ssCfg := `{"cipher":"aes-128-gcm"}`
	for i, name := range []string{
		"香港 · 1", "direct", "DIRECT", "REJECT", "block", "节点选择", "   ", "shadowtls · shadowtls",
	} {
		out = append(out, fixture{group: "extra", name: name, typ: "shadowsocks", port: 21000 + i, config: ssCfg})
	}

	out = append(out, fixture{group: "extra", name: "plain-anytls", typ: "anytls", port: 22000, config: `{}`})

	out = append(out, fixture{
		group: "extra", name: "hk-01", typ: "vless", port: 22001,
		config: `{"network":"tcp","security":"reality","dest":"www.example.com:443","server_names":["www.example.com"],"public_key":"` + fixtureRealityPub + `","short_ids":["0a1b2c3d"],"fingerprint":"chrome"}`,
	})
	out = append(out, fixture{
		group: "extra", name: "hk-02", typ: "vless", port: 22002,
		config: `{"network":"tcp","security":"reality","dest":"www.example.com:443","server_names":"www.example.com","public_key":"` + fixtureRealityPub + `","short_ids":"0a1b2c3d","fingerprint":"chrome"}`,
	})

	return out
}
