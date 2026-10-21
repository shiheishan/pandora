package protocol

import (
	"encoding/json"
	"sort"
	"strings"
)

// shadowsocksAEADMethods 是经典 AEAD（SIP004）三种；ShadowTLS 的内层只收这几种。
var shadowsocksAEADMethods = []string{"aes-128-gcm", "aes-256-gcm", "chacha20-ietf-poly1305"}

// shadowsocksMethods 是 shadowsocks 节点可选的加密方式：经典 AEAD 加 Shadowsocks 2022（ss2022.go 的 ss2022Methods）。
// 2022 的服务端身份密钥由面板按节点派生（node/service/ss2022.go），用户密钥从代理 UUID 算（订阅渲染 render/opts.go）。
var shadowsocksMethods = append(append([]string(nil), shadowsocksAEADMethods...), ss2022Methods...)

var sensitiveProtocolKey = map[string]struct{}{
	"password": {}, "passwd": {}, "secret": {}, "token": {},
	"private_key": {}, "private-key": {}, "psk": {},
	"obfs_password": {}, "obfs-password": {},
	// mKCP 加密掩码的口令：schema 早就把它标成敏感，这张表漏了，读接口曾明文回显
	"mask_password": {},
}

// schemaVersion 是唯一能进下发路径的协议 schema 版本。
const schemaVersion = 1

// stableTypes 是可下发的稳定协议名单，与 pdnd kernel/capabilities.go、stableSchemas 的
// 稳定项三份完全相同（pdnd/release/check_native_panel_parity.py 按这个字面写法读，别改声明方式）。
var stableTypes = [...]string{
	"anytls", "http", "hysteria2", "juicity", "mieru", "naive", "shadowsocks", "shadowtls", "socks", "trojan", "tuic", "vless", "vmess",
}

// stableSchemas 是全部稳定协议的 schema，按 node_type 排序。每项开头必须保持
// NodeType、Version: 1、Status: "stable" 依次写在同一个字面量里：对齐门按正则读这个文件。
func stableSchemas() []Schema {
	streamNetworks := []string{"tcp", "ws", "httpupgrade", "grpc", "xhttp", "xhttp-h3", "mkcp"}
	// 字段名跟 xboard：传输参数收在 network_settings 里，uTLS 指纹叫 utls。
	// 内核那边仍然是扁平的 path / host / grpc_service_name / fingerprint，
	// 翻译在 toKernelConfig（kernel_config.go）。
	//
	// ws_path / grpc_path 和我们比 xboard 多出来的那些（XHTTP 的 sc_* 系列、
	// mKCP 调优、掩码）保持原名：xboard 里根本没有对应字段，硬编一个名字
	// 只会让照着 xboard 教程填的人找不到，也让看我们文档的人对不上。
	streamProperties := []string{
		"network", "tls", "utls",
		"network_settings.path", "network_settings.headers.Host",
		"network_settings.serviceName", "network_settings.mode",
		"ws_path", "grpc_path",
		"headers", "sc_max_each_post_bytes", "sc_min_posts_interval_ms",
		"sc_max_buffered_posts", "sc_stream_up_server_secs", "session_placement", "session_key",
		"seq_placement", "seq_key", "uplink_http_method", "uplink_data_placement", "uplink_data_key",
		"uplink_chunk_size", "server_max_header_bytes", "cert_path", "key_path",
		// mKCP 调优参数，只在 network=mkcp 时有意义
		"mtu", "tti", "uplink_capacity", "downlink_capacity", "congestion",
		"read_buffer_size", "write_buffer_size",
		// mKCP 掩码：把 UDP 包变成随机字节，盖掉 mKCP 自身的特征
		"mask", "mask_password",
	}
	streamPropertyTypes := map[string]string{
		// tls 现在是 xboard 的三态整数：0 不加密、1 普通 TLS、2 REALITY。
		// 界面上用下拉而不是勾选框——勾选框只能表达两态，硬塞三态会变成
		// 「勾了是 TLS 还是 REALITY」这种没人能猜对的界面。
		"tls": "number",
		"mtu": "number", "tti": "number", "uplink_capacity": "number",
		"downlink_capacity": "number", "congestion": "boolean",
		"read_buffer_size": "number", "write_buffer_size": "number",
		"headers": "json", "sc_max_each_post_bytes": "json", "sc_min_posts_interval_ms": "json",
		"sc_stream_up_server_secs": "json", "uplink_chunk_size": "json",
		"sc_max_buffered_posts": "number", "server_max_header_bytes": "number",
	}
	out := []Schema{
		// 字段名跟 xboard：加密方式叫 cipher，不叫 method。内核那边仍然
		// 是 method，翻译在 toKernelConfig 里，见 kernel_config.go。
		{NodeType: "shadowsocks", Version: 1, Status: "stable",
			Required: []string{"cipher"}, AllowedProperties: []string{"cipher"},
			Methods: append([]string(nil), shadowsocksMethods...),
			Hints:   map[string]string{"cipher": ss2022CipherHint}},
		// xboard 把带宽收在 bandwidth 对象里、混淆收在 obfs 对象里。
		// cert_path / key_path 是我们独有的（xboard 用它自己的证书管理），
		// cert_mode=acme 时由节点端自动申请（docs/node-certificates.md）；
		// 没有对应名字就保持原样——硬编一个只会两边都对不上。
		{NodeType: "hysteria2", Version: 1, Status: "stable",
			Required: []string{"cert_mode", "cert_path", "key_path"},
			AllowedProperties: []string{"network", "cert_mode", "cert_path", "key_path",
				"obfs.type", "obfs.password", "bandwidth.up", "bandwidth.down", "udp_timeout",
				"tls_settings.server_name", "tls_settings.allow_insecure"},
			Enums: map[string][]string{"network": {"udp"}, "cert_mode": {"acme", "file"}, "obfs.type": {"salamander"}},
			PropertyTypes: map[string]string{
				"bandwidth.up": "number", "bandwidth.down": "number",
				"tls_settings.allow_insecure": "boolean"},
			SensitiveProperties: []string{"obfs.password"},
			Hints:               withCertModeHints(nil)},
		{NodeType: "juicity", Version: 1, Status: "stable",
			Required:          []string{"cert_mode", "cert_path", "key_path"},
			AllowedProperties: []string{"network", "cert_mode", "cert_path", "key_path", "congestion_control"},
			Enums:             map[string][]string{"network": {"udp"}, "cert_mode": {"acme", "file"}, "congestion_control": {"cubic", "new_reno", "bbr"}},
			Hints:             withCertModeHints(nil)},
		{NodeType: "socks", Version: 1, Status: "stable",
			Required:          []string{"cert_mode"},
			AllowedProperties: []string{"network", "tls", "cert_mode", "cert_path", "key_path", "security"},
			Enums:             map[string][]string{"network": {"tcp", "udp"}, "cert_mode": {"acme", "file"}, "security": {"none"}},
			PropertyTypes:     map[string]string{"tls": "boolean"},
			Hints:             withCertModeHints(nil)},
		{NodeType: "http", Version: 1, Status: "stable",
			Required:          []string{"cert_mode"},
			AllowedProperties: []string{"network", "tls", "cert_mode", "cert_path", "key_path", "security"},
			Enums:             map[string][]string{"network": {"tcp"}, "cert_mode": {"acme", "file"}, "security": {"none"}},
			PropertyTypes:     map[string]string{"tls": "boolean"},
			Hints:             withCertModeHints(nil)},
		{NodeType: "naive", Version: 1, Status: "stable",
			Required: []string{"tls", "cert_mode", "cert_path", "key_path"},
			AllowedProperties: []string{"network", "tls", "cert_mode", "cert_path", "key_path", "security",
				"tls_settings.server_name", "tls_settings.allow_insecure", "fallback"},
			Enums:         map[string][]string{"network": {"tcp"}, "cert_mode": {"acme", "file"}, "security": {"none"}},
			PropertyTypes: map[string]string{"tls": "boolean", "tls_settings.allow_insecure": "boolean"},
			Hints:         withCertModeHints(map[string]string{"fallback": fallbackHint})},
		// xboard 的 transport 是大写的 TCP / UDP，内核要小写，
		// 转换在 applyKernelShapeFixups 里。
		{NodeType: "mieru", Version: 1, Status: "stable",
			AllowedProperties: []string{"transport"},
			Enums:             map[string][]string{"transport": {"TCP", "UDP"}}},
		{NodeType: "shadowtls", Version: 1, Status: "stable",
			Required:          []string{"password"},
			AllowedProperties: []string{"network", "version", "password", "server", "handshake_server", "server_port", "method", "strict", "wildcard_sni"},
			// wildcard_sni 打开后，服务端拿客户端给的 SNI 当握手目标，而不是
			// 配置里那个被借用的站点——等于任何通过认证的用户都能让节点去连
			// 任意主机的 443。默认 off，和 sing-box 一致。
			Enums:               map[string][]string{"network": {"tcp"}, "method": append([]string(nil), shadowsocksAEADMethods...), "wildcard_sni": {"off", "authed", "all"}},
			PropertyTypes:       map[string]string{"version": "number", "server_port": "number", "strict": "boolean"},
			SensitiveProperties: []string{"password"}},
		{NodeType: "tuic", Version: 1, Status: "stable",
			Required: []string{"cert_mode", "cert_path", "key_path"},
			AllowedProperties: []string{"network", "cert_mode", "cert_path", "key_path", "congestion_control", "auth_timeout", "heartbeat", "udp_timeout", "zero_rtt",
				"tls_settings.server_name", "tls_settings.allow_insecure"},
			Enums:         map[string][]string{"network": {"udp"}, "cert_mode": {"acme", "file"}, "congestion_control": {"cubic", "new_reno", "bbr"}},
			PropertyTypes: map[string]string{"zero_rtt": "boolean", "tls_settings.allow_insecure": "boolean"},
			Hints:         withCertModeHints(nil)},
		// AnyTLS 证书必填：没证书时节点端以明文起，而所有客户端都强制 TLS，
		// 存得进去却谁都连不上（校验在 validate.go:requireAnyTLSCertificate）。
		{NodeType: "anytls", Version: 1, Status: "stable",
			Required: []string{"cert_mode", "cert_path", "key_path"},
			AllowedProperties: []string{"network", "tls", "cert_mode", "cert_path", "key_path", "padding_scheme",
				"tls_settings.server_name", "tls_settings.allow_insecure", "utls", "fallback"},
			Enums:         map[string][]string{"network": {"tcp"}, "cert_mode": {"acme", "file"}, "utls": append([]string(nil), utlsFingerprints...)},
			PropertyTypes: map[string]string{"tls": "boolean", "padding_scheme": "json", "tls_settings.allow_insecure": "boolean"},
			Hints:         withCertModeHints(map[string]string{"fallback": fallbackHint, "utls": utlsHint})},
		{NodeType: "trojan", Version: 1, Status: "stable",
			// Trojan 的管理端形状与 vless/vmess 保持一致：REALITY 参数收进
			// reality_settings，传输参数收进 network_settings，uTLS 指纹叫 utls。
			// Native Trojan 同时支持普通 TLS 与 REALITY，所以使用 xboard 的
			// 1/2 两项；0 会在服务端校验层明确拒绝，而不是让后台保存一份
			// 永远不能服务的节点。
			Required: []string{"tls", "cert_mode"},
			AllowedProperties: []string{"network", "tls", "utls",
				"network_settings.path", "network_settings.headers.Host",
				"network_settings.serviceName", "network_settings.mode",
				"ws_path", "grpc_path", "cert_mode", "cert_path", "key_path",
				"reality_settings.dest", "reality_settings.server_name",
				"reality_settings.private_key", "reality_settings.public_key",
				"reality_settings.short_id",
				// 认证失败的回落目标；只在 tcp 上生效。没有 flow：Trojan 没有
				// 流控，内核校验器也从来不收这个键（以前填了就整份 422）。
				"fallback",
				// 普通 TLS（tls=1）时给客户端的 SNI 与「跳过证书校验」，只进订阅。
				"tls_settings.server_name", "tls_settings.allow_insecure",
				// mKCP 及其掩码。network 枚举里有 mkcp，属性表里就得有对应
				// 字段，否则后台能选 mkcp 却配不了任何 mKCP 参数。
				"mtu", "tti", "uplink_capacity", "downlink_capacity", "congestion",
				"read_buffer_size", "write_buffer_size", "mask", "mask_password"},
			Enums: map[string][]string{
				"network":   {"tcp", "ws", "httpupgrade", "grpc", "mkcp"},
				"tls":       {"1", "2"},
				"cert_mode": {"acme", "file"},
				"utls":      append([]string(nil), utlsFingerprints...),
			},
			PropertyTypes: map[string]string{"tls": "number", "tls_settings.allow_insecure": "boolean",
				"mtu": "number", "tti": "number", "uplink_capacity": "number",
				"downlink_capacity": "number", "congestion": "boolean",
				"read_buffer_size": "number", "write_buffer_size": "number",
				"reality_settings.server_name": "list", "reality_settings.short_id": "list"},
			SensitiveProperties: []string{"private_key", "mask_password"},
			Hints: withCertModeHints(withRealityHints(map[string]string{
				"fallback": fallbackHint + "只在 tcp 传输上生效。",
				"utls":     utlsHint,
			}))},
		{NodeType: "vless", Version: 1, Status: "stable",
			AllowedProperties: append(append([]string(nil), streamProperties...),
				"reality_settings.dest", "reality_settings.server_name",
				"reality_settings.private_key", "reality_settings.public_key",
				"reality_settings.short_id", "flow",
				// 认证失败的回落目标；pdnd 只在 tcp 承载（REALITY / TLS / 明文）上读。
				"fallback"),
			Enums: map[string][]string{
				"network": append([]string(nil), streamNetworks...),
				// xboard 的三态。1（普通 TLS）暂时不给选：vless / vmess 的普通 TLS 还没开放
				// （校验器回 plainTLSNotYetMessage），放出来只会让人填完保存失败。
				// 面板不签发节点证书，要开得先让这两个协议像 trojan 一样收 file 模式的
				// cert_path / key_path（docs/node-certificates.md），校验、翻译、渲染一起放开。
				"tls":                   {"0", "2"},
				"flow":                  append([]string(nil), vlessFlows...),
				"utls":                  append([]string(nil), utlsFingerprints...),
				"mode":                  {"auto", "packet-up", "stream-up", "stream-one", "stream-down"},
				"session_placement":     {"path", "query", "header", "cookie"},
				"seq_placement":         {"path", "query", "header", "cookie"},
				"uplink_data_placement": {"body", "query", "header", "cookie"},
				"uplink_http_method":    {"GET", "POST"},
			},
			PropertyTypes: withRealityListTypes(cloneStringMap(streamPropertyTypes)),
			// private_key 会被 Redact 从读接口里抹掉，
			// 后台只能写不能回显 —— 和其它密钥一个待遇。
			SensitiveProperties: []string{"private_key", "mask_password"},
			Hints: withCertHints(withRealityHints(map[string]string{
				"flow":     "REALITY + tcp 时用 xtls-rprx-vision（默认）；其它传输必须留空。",
				"utls":     utlsHint,
				"fallback": fallbackHint + "只在 tcp 传输上生效。",
			}))},
		{NodeType: "vmess", Version: 1, Status: "stable",
			AllowedProperties: append([]string(nil), append(streamProperties,
				"security")...),
			Enums: map[string][]string{
				"network": append([]string(nil), streamNetworks...),
				// VMess 不支持 REALITY，所以只有 0 一个可选值。留着这个下拉
				// 而不是把字段藏掉：藏掉之后照 xboard 教程填的人会以为漏了
				// 什么，摆在那里显示只有一个选项，一眼就知道是不支持。
				"tls":                   {"0"},
				"utls":                  append([]string(nil), utlsFingerprints...),
				"security":              {"none", "zero", "aes-128-gcm", "chacha20-poly1305", "auto"},
				"mode":                  {"auto", "packet-up", "stream-up", "stream-one", "stream-down"},
				"session_placement":     {"path", "query", "header", "cookie"},
				"seq_placement":         {"path", "query", "header", "cookie"},
				"uplink_data_placement": {"body", "query", "header", "cookie"},
				"uplink_http_method":    {"GET", "POST"},
			},
			PropertyTypes: cloneStringMap(streamPropertyTypes),
			Hints: withCertHints(map[string]string{
				"network": "VMess 不能开 TLS：建议用 ws / httpupgrade / grpc / xhttp 并套 CDN 或 TLS 反代；裸 tcp 能保存，但特征明显、容易被识别封锁。",
			}),
		},
	}
	sort.Slice(out, func(a, b int) bool { return out[a].NodeType < out[b].NodeType })
	return out
}

func cloneStringMap(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

// redact 在任意深度按键名抹掉敏感值：存进来的对象可能带任意历史字段，所以不按 schema 只按键名扫。
func redact(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage(`{}`)
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return json.RawMessage(`{}`)
	}
	redactProtocolValue(value)
	out, err := json.Marshal(value)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return out
}

func redactProtocolValue(value any) {
	switch item := value.(type) {
	case map[string]any:
		for key, child := range item {
			if _, sensitive := sensitiveProtocolKey[strings.ToLower(key)]; sensitive {
				delete(item, key)
				continue
			}
			redactProtocolValue(child)
		}
	case []any:
		for _, child := range item {
			redactProtocolValue(child)
		}
	}
}
