package protocol

import (
	"slices"
	"strings"
)

// FormField 是协议表单上一个字段的显示元数据（中文名、显示条件、是否高级、默认值等）。
type FormField struct {
	Path     string `json:"path"`
	Label    string `json:"label"`
	Advanced bool   `json:"advanced,omitempty"`
	// ShowIf：每个键是另一个字段的路径，值是让本字段显示的取值（按表单字符串比较：
	// 布尔为 "true"/"false"，数字为十进制文本，"" 表示没填）；所有键同时满足才显示。
	ShowIf map[string][]string `json:"show_if,omitempty"`
	// Default 是新建时预填的值，类型同存储（number→数字、boolean→布尔、其余→字符串）；
	// 只给节点端实际缺省或唯一可选的值。
	Default     any    `json:"default,omitempty"`
	Placeholder string `json:"placeholder,omitempty"`
	// OptionLabels：枚举值 → 显示文字；键 "" 是选填枚举「不选」那一项的文字。
	OptionLabels map[string]string `json:"option_labels,omitempty"`
}

type formSpec struct {
	note   string
	fields []FormField
	hints  map[string]string
}

var utlsOptionLabels = map[string]string{"": "留空（chrome）"}

var showTLS2 = map[string][]string{"tls": {"2"}}
var showTLS1 = map[string][]string{"tls": {"1"}}
var showTLSTrue = map[string][]string{"tls": {"true"}}
var showNetworkTCP = map[string][]string{"network": {"tcp", ""}}
var showNetworkPath = map[string][]string{"network": {"ws", "httpupgrade", "xhttp", "xhttp-h3"}}
var showNetworkHost = map[string][]string{"network": {"ws", "httpupgrade", "xhttp", "xhttp-h3", "grpc"}}
var showNetworkGRPC = map[string][]string{"network": {"grpc"}}
var showNetworkXHTTP = map[string][]string{"network": {"xhttp", "xhttp-h3"}}
var showNetworkWS = map[string][]string{"network": {"ws"}}
var showNetworkMKCP = map[string][]string{"network": {"mkcp"}}
var showFlowVLESS = map[string][]string{"tls": {"2"}, "network": {"tcp", ""}}
var showObfsSalamander = map[string][]string{"obfs.type": {"salamander"}}
var showMaskAES = map[string][]string{"network": {"mkcp"}, "mask": {"mkcp-aes128gcm"}}

func cloneFields(in []FormField) []FormField {
	return append([]FormField(nil), in...)
}

func realityFields() []FormField {
	return []FormField{
		{Path: "reality_settings.dest", Label: "REALITY 目标站（dest）", ShowIf: showTLS2, Placeholder: "www.apple.com:443"},
		{Path: "reality_settings.server_name", Label: "REALITY SNI", ShowIf: showTLS2, Placeholder: "www.apple.com"},
		{Path: "reality_settings.private_key", Label: "REALITY 私钥", ShowIf: showTLS2},
		{Path: "reality_settings.public_key", Label: "REALITY 公钥", ShowIf: showTLS2},
		{Path: "reality_settings.short_id", Label: "REALITY short_id", ShowIf: showTLS2, Placeholder: "0a1b2c3d"},
	}
}

func transportFields() []FormField {
	return []FormField{
		{Path: "network_settings.path", Label: "路径（path）", ShowIf: showNetworkPath, Placeholder: "/ws"},
		{Path: "network_settings.headers.Host", Label: "Host 头", ShowIf: showNetworkHost, Placeholder: "cdn.example.com"},
		{Path: "network_settings.serviceName", Label: "gRPC serviceName", ShowIf: showNetworkGRPC, Placeholder: "grpc"},
		{Path: "network_settings.mode", Label: "XHTTP 模式", ShowIf: showNetworkXHTTP},
		{Path: "ws_path", Label: "ws_path（兼容字段）", Advanced: true, ShowIf: showNetworkWS},
		{Path: "grpc_path", Label: "grpc_path（兼容字段）", Advanced: true, ShowIf: showNetworkGRPC},
	}
}

func xhttpFields() []FormField {
	return []FormField{
		{Path: "headers", Label: "自定义响应头", Advanced: true, ShowIf: showNetworkXHTTP, Placeholder: `{"X-Foo":"bar"}`},
		{Path: "sc_max_each_post_bytes", Label: "每次上行最大字节", Advanced: true, ShowIf: showNetworkXHTTP, Placeholder: `1000000 或 "500000-1000000"`},
		{Path: "sc_min_posts_interval_ms", Label: "上行最小间隔（毫秒）", Advanced: true, ShowIf: showNetworkXHTTP},
		{Path: "sc_max_buffered_posts", Label: "最多缓存的上行包", Advanced: true, ShowIf: showNetworkXHTTP},
		{Path: "sc_stream_up_server_secs", Label: "stream-up 保活秒数", Advanced: true, ShowIf: showNetworkXHTTP, Placeholder: `"20-80"`},
		{Path: "session_placement", Label: "session 位置", Advanced: true, ShowIf: showNetworkXHTTP},
		{Path: "session_key", Label: "session 键名", Advanced: true, ShowIf: showNetworkXHTTP},
		{Path: "seq_placement", Label: "seq 位置", Advanced: true, ShowIf: showNetworkXHTTP},
		{Path: "seq_key", Label: "seq 键名", Advanced: true, ShowIf: showNetworkXHTTP},
		{Path: "uplink_http_method", Label: "上行 HTTP 方法", Advanced: true, ShowIf: showNetworkXHTTP},
		{Path: "uplink_data_placement", Label: "上行数据位置", Advanced: true, ShowIf: showNetworkXHTTP},
		{Path: "uplink_data_key", Label: "上行数据键名", Advanced: true, ShowIf: showNetworkXHTTP},
		{Path: "uplink_chunk_size", Label: "上行分块大小", Advanced: true, ShowIf: showNetworkXHTTP},
		{Path: "server_max_header_bytes", Label: "服务端最大头部字节", Advanced: true, ShowIf: showNetworkXHTTP},
	}
}

func mkcpFields() []FormField {
	return []FormField{
		{Path: "mask", Label: "mKCP 掩码", ShowIf: showNetworkMKCP, Placeholder: "none 或 mkcp-aes128gcm"},
		{Path: "mask_password", Label: "掩码密码", ShowIf: showMaskAES},
		{Path: "mtu", Label: "MTU", Advanced: true, ShowIf: showNetworkMKCP},
		{Path: "tti", Label: "TTI（毫秒）", Advanced: true, ShowIf: showNetworkMKCP},
		{Path: "uplink_capacity", Label: "上行容量（MB/s）", Advanced: true, ShowIf: showNetworkMKCP, Placeholder: "留空按 5"},
		{Path: "downlink_capacity", Label: "下行容量（MB/s）", Advanced: true, ShowIf: showNetworkMKCP, Placeholder: "留空按 20"},
		{Path: "congestion", Label: "拥塞控制", Advanced: true, ShowIf: showNetworkMKCP},
		{Path: "read_buffer_size", Label: "读缓冲（MB）", Advanced: true, ShowIf: showNetworkMKCP},
		{Path: "write_buffer_size", Label: "写缓冲（MB）", Advanced: true, ShowIf: showNetworkMKCP},
	}
}

func certFields(showIf map[string][]string) []FormField {
	certPh := nodeCertificateDir + "example.com/fullchain.pem"
	keyPh := nodeCertificateDir + "example.com/privkey.pem"
	out := []FormField{
		{Path: "cert_path", Label: "证书路径", Placeholder: certPh},
		{Path: "key_path", Label: "私钥路径", Placeholder: keyPh},
	}
	if showIf != nil {
		for i := range out {
			out[i].ShowIf = showIf
		}
	}
	return out
}

func clientTLSFields(showIf map[string][]string) []FormField {
	out := []FormField{
		{Path: "tls_settings.server_name", Label: "客户端 SNI", Placeholder: "example.com"},
		{Path: "tls_settings.allow_insecure", Label: "允许不安全证书"},
	}
	if showIf != nil {
		for i := range out {
			out[i].ShowIf = showIf
		}
	}
	return out
}

func appendAll(dst []FormField, parts ...[]FormField) []FormField {
	for _, p := range parts {
		dst = append(dst, p...)
	}
	return dst
}

var protocolForms = map[string]formSpec{
	"vless": {
		note: "REALITY + tcp 时用 xtls-rprx-vision；普通 TLS（tls=1）暂未开放。",
		fields: appendAll(nil,
			[]FormField{
				{Path: "network", Label: "传输方式", Default: "tcp"},
				{Path: "tls", Label: "加密", OptionLabels: map[string]string{"0": "0 · 不加密", "2": "2 · REALITY"}},
				{Path: "flow", Label: "流控（flow）", ShowIf: showFlowVLESS, OptionLabels: map[string]string{"": "留空"}},
				{Path: "utls", Label: "uTLS 指纹", ShowIf: showTLS2, OptionLabels: utlsOptionLabels},
			},
			realityFields(), transportFields(),
			[]FormField{{Path: "fallback", Label: "回落地址", ShowIf: showNetworkTCP, Placeholder: "127.0.0.1:80"}},
			certFields(showTLS1), mkcpFields(), xhttpFields(),
		),
		hints: map[string]string{
			"tls":       "不加密时不能用裸 tcp。",
			"ws_path":   "旧字段名，与「路径」二选一即可。",
			"grpc_path": "旧字段名，与 serviceName 二选一即可。",
			"mask":      "把 UDP 包变成随机字节，盖掉 mKCP 自身的特征。",
		},
	},
	"vmess": {
		note: "VMess 不能开 TLS，只能用 ws / httpupgrade / grpc / xhttp 并套 CDN 或 TLS 反代；裸 tcp 不允许。",
		fields: appendAll(nil,
			[]FormField{
				{Path: "network", Label: "传输方式"},
				{Path: "tls", Label: "加密", Default: 0, OptionLabels: map[string]string{"0": "0 · 不加密"}},
				{Path: "security", Label: "加密方式", OptionLabels: map[string]string{"": "默认"}},
				{Path: "utls", Label: "uTLS 指纹", Advanced: true, OptionLabels: utlsOptionLabels},
			},
			transportFields(), certFields(showTLS1), mkcpFields(), xhttpFields(),
		),
		hints: map[string]string{
			"tls":       "VMess 不支持 REALITY，只有这一个选项。",
			"ws_path":   "旧字段名，与「路径」二选一即可。",
			"grpc_path": "旧字段名，与 serviceName 二选一即可。",
			"mask":      "把 UDP 包变成随机字节，盖掉 mKCP 自身的特征。",
		},
	},
	"trojan": {
		note: "Trojan 支持普通 TLS（1）与 REALITY（2），不能不加密；没有流控。",
		fields: appendAll(nil,
			[]FormField{
				{Path: "network", Label: "传输方式", Default: "tcp"},
				{Path: "tls", Label: "加密", OptionLabels: map[string]string{"1": "1 · TLS", "2": "2 · REALITY"}},
				{Path: "utls", Label: "uTLS 指纹", OptionLabels: utlsOptionLabels},
			},
			certFields(showTLS1), clientTLSFields(showTLS1), realityFields(), transportFields(),
			[]FormField{{Path: "fallback", Label: "回落地址", ShowIf: showNetworkTCP, Placeholder: "127.0.0.1:80"}},
			mkcpFields(),
		),
		hints: map[string]string{
			"ws_path":                     "旧字段名，与「路径」二选一即可。",
			"grpc_path":                   "旧字段名，与 serviceName 二选一即可。",
			"mask":                        "把 UDP 包变成随机字节，盖掉 mKCP 自身的特征。",
			"tls_settings.server_name":    "只进订阅，告诉客户端握手用的域名。",
			"tls_settings.allow_insecure": "只进订阅；自签证书时才打开。",
		},
	},
	"shadowsocks": {
		note:   "只有加密方式一项；用户密钥由面板按订阅生成。",
		fields: []FormField{{Path: "cipher", Label: "加密方式"}},
	},
	"hysteria2": {
		note: "走 UDP；证书必填。",
		fields: appendAll(nil,
			[]FormField{{Path: "network", Label: "传输方式", Default: "udp"}},
			certFields(nil),
			[]FormField{
				{Path: "obfs.type", Label: "混淆类型", OptionLabels: map[string]string{"": "不混淆"}},
				{Path: "obfs.password", Label: "混淆密码", ShowIf: showObfsSalamander},
				{Path: "bandwidth.up", Label: "上行带宽（Mbps）", Placeholder: "留空不限"},
				{Path: "bandwidth.down", Label: "下行带宽（Mbps）", Placeholder: "留空不限"},
				{Path: "udp_timeout", Label: "UDP 超时", Advanced: true, Placeholder: "留空按 5m"},
			},
			clientTLSFields(nil),
		),
		hints: map[string]string{
			"tls_settings.server_name":    "只进订阅，告诉客户端握手用的域名。",
			"tls_settings.allow_insecure": "只进订阅；自签证书时才打开。",
		},
	},
	"tuic": {
		note: "走 UDP；证书必填。",
		fields: appendAll(nil,
			[]FormField{{Path: "network", Label: "传输方式", Default: "udp"}},
			certFields(nil),
			[]FormField{
				{Path: "congestion_control", Label: "拥塞控制", OptionLabels: map[string]string{"": "留空（cubic）"}},
				{Path: "zero_rtt", Label: "0-RTT"},
				{Path: "auth_timeout", Label: "认证超时", Advanced: true, Placeholder: "留空按 3s"},
				{Path: "heartbeat", Label: "心跳间隔", Advanced: true, Placeholder: "留空按 10s"},
				{Path: "udp_timeout", Label: "UDP 超时", Advanced: true, Placeholder: "留空按 5m"},
			},
			clientTLSFields(nil),
		),
		hints: map[string]string{
			"tls_settings.server_name":    "只进订阅，告诉客户端握手用的域名。",
			"tls_settings.allow_insecure": "只进订阅；自签证书时才打开。",
		},
	},
	"juicity": {
		note: "走 UDP；证书必填。",
		fields: appendAll(nil,
			[]FormField{{Path: "network", Label: "传输方式", Default: "udp"}},
			certFields(nil),
			[]FormField{{Path: "congestion_control", Label: "拥塞控制", OptionLabels: map[string]string{"": "默认"}}},
		),
	},
	"anytls": {
		note: "证书必填：没证书时节点会以明文起，而客户端都强制 TLS，存得进去却谁都连不上。",
		fields: appendAll(nil,
			[]FormField{
				{Path: "network", Label: "传输方式", Default: "tcp"},
				{Path: "tls", Label: "开启 TLS"},
			},
			certFields(nil),
			[]FormField{
				{Path: "padding_scheme", Label: "填充方案", Advanced: true, Placeholder: `["stop=8","0=30-30"]`},
				{Path: "utls", Label: "uTLS 指纹", OptionLabels: utlsOptionLabels},
			},
			clientTLSFields(nil),
			[]FormField{{Path: "fallback", Label: "回落地址", Placeholder: "127.0.0.1:80"}},
		),
		hints: map[string]string{
			"tls_settings.server_name":    "只进订阅，告诉客户端握手用的域名。",
			"tls_settings.allow_insecure": "只进订阅；自签证书时才打开。",
		},
	},
	"naive": {
		note: "必须开 TLS 并填证书。",
		fields: appendAll(nil,
			[]FormField{
				{Path: "network", Label: "传输方式", Default: "tcp"},
				{Path: "tls", Label: "开启 TLS", Default: true},
			},
			certFields(nil),
			[]FormField{{Path: "security", Label: "security", Advanced: true, Default: "none"}},
			clientTLSFields(nil),
			[]FormField{{Path: "fallback", Label: "回落地址", Placeholder: "127.0.0.1:80"}},
		),
		hints: map[string]string{
			"tls_settings.server_name":    "只进订阅，告诉客户端握手用的域名。",
			"tls_settings.allow_insecure": "只进订阅；自签证书时才打开。",
		},
	},
	"shadowtls": {
		note: "借用一个真实站点的 TLS 握手，外面再套一层 Shadowsocks。",
		fields: []FormField{
			{Path: "network", Label: "传输方式", Default: "tcp"},
			{Path: "version", Label: "版本", Default: 3},
			{Path: "password", Label: "密码"},
			{Path: "handshake_server", Label: "握手站点", Placeholder: "www.apple.com"},
			{Path: "server_port", Label: "握手端口", Placeholder: "443"},
			{Path: "server", Label: "内层服务地址", Advanced: true},
			{Path: "method", Label: "内层加密方式", Default: "aes-256-gcm"},
			{Path: "strict", Label: "严格模式", Default: false},
			{Path: "wildcard_sni", Label: "通配 SNI", Default: "off"},
		},
		hints: map[string]string{
			"handshake_server": "被借用握手的真实站点。",
			"wildcard_sni":     "打开后服务端用客户端给的 SNI 当握手目标，等于认证用户能让节点去连任意主机的 443；默认 off。",
		},
	},
	"mieru": {
		note:   "只有传输方式一项。",
		fields: []FormField{{Path: "transport", Label: "传输方式", Default: "TCP"}},
	},
	"socks": {
		note: "开 TLS 时需要填证书。",
		fields: appendAll(nil,
			[]FormField{
				{Path: "network", Label: "传输方式"},
				{Path: "tls", Label: "开启 TLS", Default: false},
			},
			certFields(showTLSTrue),
			[]FormField{{Path: "security", Label: "security", Advanced: true, Default: "none"}},
		),
	},
	"http": {
		note: "开 TLS 时需要填证书。",
		fields: appendAll(nil,
			[]FormField{
				{Path: "network", Label: "传输方式", Default: "tcp"},
				{Path: "tls", Label: "开启 TLS", Default: false},
			},
			certFields(showTLSTrue),
			[]FormField{{Path: "security", Label: "security", Advanced: true, Default: "none"}},
		),
	},
}

// withForms 给每个 schema 挂上表单元数据；表单表里的额外说明只补进 Hints 里还没有的键。
func withForms(in []Schema) []Schema {
	out := make([]Schema, len(in))
	for i, s := range in {
		out[i] = s
		spec, ok := protocolForms[s.NodeType]
		if !ok {
			continue
		}
		out[i].Note = spec.note
		out[i].Fields = pruneShowIf(s, cloneFields(spec.fields))
		if len(spec.hints) == 0 {
			continue
		}
		hints := out[i].Hints
		if hints == nil {
			hints = make(map[string]string, len(spec.hints))
		} else {
			hints = cloneStringMap(hints)
		}
		for k, v := range spec.hints {
			if _, exists := hints[k]; !exists {
				hints[k] = v
			}
		}
		out[i].Hints = hints
	}
	return out
}

// pruneShowIf 把共用的显示条件收窄到本协议的枚举：取值不在本协议枚举里的去掉（如 trojan 没有 xhttp）。
// 某个键一个可选值都不剩时留空列表，前端据此永不显示该字段（如 vless / vmess 暂不开放普通 TLS 的证书路径）；
// 字段本身仍保留，满足「每个 AllowedProperties 恰好出现一次」。
func pruneShowIf(s Schema, fields []FormField) []FormField {
	for i := range fields {
		if len(fields[i].ShowIf) == 0 {
			continue
		}
		next := make(map[string][]string, len(fields[i].ShowIf))
		for dep, vals := range fields[i].ShowIf {
			enums := schemaEnums(s, dep)
			kept := make([]string, 0, len(vals))
			for _, v := range vals {
				if v == "" || enums == nil || slices.Contains(enums, v) {
					kept = append(kept, v)
				}
			}
			next[dep] = kept
		}
		fields[i].ShowIf = next
	}
	return fields
}

// schemaEnums 是字段的枚举：先按完整路径，再按末段键名，cipher / method 取 Methods；没有枚举回 nil。
func schemaEnums(s Schema, path string) []string {
	if e, ok := s.Enums[path]; ok {
		return e
	}
	leaf := path
	if i := strings.LastIndex(path, "."); i >= 0 {
		leaf = path[i+1:]
	}
	if e, ok := s.Enums[leaf]; ok {
		return e
	}
	if path == "cipher" || path == "method" {
		return s.Methods
	}
	return nil
}
