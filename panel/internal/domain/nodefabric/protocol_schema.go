// [INPUT]: 依赖标准库 encoding/json
// [OUTPUT]: 对外提供 ProtocolSchema 与 ProtocolSchemas 协议约束元数据、CanonicalNodeType、RedactProtocolConfig 抹敏
// [POS]: domain/nodefabric 的协议约束中心：schema 元数据与抹敏；内核形状校验在 protocol_validate*.go（xboard_validate 先翻译再调它），protocol_secrets 是抹敏的逆运算；sensitiveProtocolKey 必须覆盖每个 schema 的 SensitiveProperties（单测守住）
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package nodefabric

import (
	"encoding/json"
	"strings"
)

// ProtocolSchema 是管理端可读取、但不可由租户修改的协议约束元数据。
// Schema 与二进制一起发布，避免普通配置权限扩大服务端允许面。
type ProtocolSchema struct {
	NodeType            string              `json:"node_type"`
	Version             int                 `json:"version"`
	Status              string              `json:"status"`
	Required            []string            `json:"required"`
	AllowedProperties   []string            `json:"allowed_properties"`
	Methods             []string            `json:"methods,omitempty"`
	Enums               map[string][]string `json:"enums,omitempty"`
	PropertyTypes       map[string]string   `json:"property_types,omitempty"`
	SensitiveProperties []string            `json:"sensitive_properties,omitempty"`
}

var shadowsocksMethods = []string{
	// The per-user credential is a UUID string. Shadowsocks 2022 requires a
	// method-specific base64 key, so advertising 2022 methods here would create
	// subscriptions that cannot authenticate against the current user model.
	"aes-128-gcm", "aes-256-gcm", "chacha20-ietf-poly1305",
}

var sensitiveProtocolKey = map[string]struct{}{
	"password": {}, "passwd": {}, "secret": {}, "token": {},
	"private_key": {}, "private-key": {}, "psk": {},
	"obfs_password": {}, "obfs-password": {},
	// mKCP 加密掩码的口令：schema 早就把它标成敏感，这张表漏了，读接口曾明文回显
	"mask_password": {},
}

var legacyProtocolTypes = []string{
	"v2ray", "hysteria",
}

// CanonicalNodeType is the only representation written to storage and audit logs.
// Renderers intentionally switch on these stable lowercase identifiers.
func CanonicalNodeType(nodeType string) string {
	return strings.ToLower(strings.TrimSpace(nodeType))
}

func ProtocolSchemas() []ProtocolSchema {
	streamNetworks := []string{"tcp", "ws", "httpupgrade", "grpc", "xhttp", "xhttp-h3", "mkcp"}
	// 字段名跟 xboard：传输参数收在 network_settings 里，uTLS 指纹叫 utls。
	// 内核那边仍然是扁平的 path / host / grpc_service_name / fingerprint，
	// 翻译在 toKernelConfig（xboard_field_names.go）。
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
	out := []ProtocolSchema{
		// 字段名跟 xboard：加密方式叫 cipher，不叫 method。内核那边仍然
		// 是 method，翻译在 toKernelConfig 里，见 xboard_field_names.go。
		{NodeType: "shadowsocks", Version: 1, Status: "stable",
			Required: []string{"cipher"}, AllowedProperties: []string{"cipher"},
			Methods: append([]string(nil), shadowsocksMethods...)},
		// xboard 把带宽收在 bandwidth 对象里、混淆收在 obfs 对象里。
		// cert_path / key_path 是我们独有的（xboard 用它自己的证书管理），
		// 没有对应名字就保持原样——硬编一个只会两边都对不上。
		{NodeType: "hysteria2", Version: 1, Status: "stable",
			Required: []string{"cert_path", "key_path"},
			AllowedProperties: []string{"network", "cert_path", "key_path",
				"obfs.type", "obfs.password", "bandwidth.up", "bandwidth.down", "udp_timeout"},
			Enums: map[string][]string{"network": {"udp"}, "obfs.type": {"salamander"}},
			PropertyTypes: map[string]string{
				"bandwidth.up": "number", "bandwidth.down": "number"},
			SensitiveProperties: []string{"obfs.password"}},
		{NodeType: "juicity", Version: 1, Status: "stable",
			Required:          []string{"cert_path", "key_path"},
			AllowedProperties: []string{"network", "cert_path", "key_path", "congestion_control"},
			Enums:             map[string][]string{"network": {"udp"}, "congestion_control": {"cubic", "new_reno", "bbr"}}},
		{NodeType: "socks", Version: 1, Status: "stable",
			AllowedProperties: []string{"network", "tls", "cert_path", "key_path", "security"},
			Enums:             map[string][]string{"network": {"tcp", "udp"}, "security": {"none"}},
			PropertyTypes:     map[string]string{"tls": "boolean"}},
		{NodeType: "http", Version: 1, Status: "stable",
			AllowedProperties: []string{"network", "tls", "cert_path", "key_path", "security"},
			Enums:             map[string][]string{"network": {"tcp"}, "security": {"none"}},
			PropertyTypes:     map[string]string{"tls": "boolean"}},
		{NodeType: "naive", Version: 1, Status: "stable",
			Required:          []string{"tls", "cert_path", "key_path"},
			AllowedProperties: []string{"network", "tls", "cert_path", "key_path", "security"},
			Enums:             map[string][]string{"network": {"tcp"}, "security": {"none"}},
			PropertyTypes:     map[string]string{"tls": "boolean"}},
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
			Enums:               map[string][]string{"network": {"tcp"}, "method": append([]string(nil), shadowsocksMethods...), "wildcard_sni": {"off", "authed", "all"}},
			PropertyTypes:       map[string]string{"version": "number", "server_port": "number", "strict": "boolean"},
			SensitiveProperties: []string{"password"}},
		{NodeType: "tuic", Version: 1, Status: "stable",
			Required:          []string{"cert_path", "key_path"},
			AllowedProperties: []string{"network", "cert_path", "key_path", "congestion_control", "auth_timeout", "heartbeat", "udp_timeout", "zero_rtt"},
			Enums:             map[string][]string{"network": {"udp"}, "congestion_control": {"cubic", "new_reno", "bbr"}},
			PropertyTypes:     map[string]string{"zero_rtt": "boolean"}},
		{NodeType: "anytls", Version: 1, Status: "stable",
			AllowedProperties: []string{"network", "tls", "cert_path", "key_path", "padding_scheme"},
			Enums:             map[string][]string{"network": {"tcp"}},
			PropertyTypes:     map[string]string{"tls": "boolean", "padding_scheme": "json"}},
		{NodeType: "trojan", Version: 1, Status: "stable",
			// Trojan 的管理端形状与 vless/vmess 保持一致：REALITY 参数收进
			// reality_settings，传输参数收进 network_settings，uTLS 指纹叫 utls。
			// Native Trojan 同时支持普通 TLS 与 REALITY，所以使用 xboard 的
			// 1/2 两项；0 会在服务端校验层明确拒绝，而不是让后台保存一份
			// 永远不能服务的节点。
			Required: []string{"tls"},
			AllowedProperties: []string{"network", "tls", "utls",
				"network_settings.path", "network_settings.headers.Host",
				"network_settings.serviceName", "network_settings.mode",
				"ws_path", "grpc_path", "cert_path", "key_path",
				"reality_settings.dest", "reality_settings.server_name",
				"reality_settings.private_key", "reality_settings.public_key",
				"reality_settings.short_id", "flow",
				// mKCP 及其掩码。network 枚举里有 mkcp，属性表里就得有对应
				// 字段，否则后台能选 mkcp 却配不了任何 mKCP 参数。
				"mtu", "tti", "uplink_capacity", "downlink_capacity", "congestion",
				"read_buffer_size", "write_buffer_size", "mask", "mask_password"},
			Enums: map[string][]string{
				"network": {"tcp", "ws", "httpupgrade", "grpc", "mkcp"},
				"tls":     {"1", "2"},
			},
			PropertyTypes: map[string]string{"tls": "number",
				"mtu": "number", "tti": "number", "uplink_capacity": "number",
				"downlink_capacity": "number", "congestion": "boolean",
				"read_buffer_size": "number", "write_buffer_size": "number"},
			SensitiveProperties: []string{"private_key", "mask_password"}},
		{NodeType: "vless", Version: 1, Status: "stable",
			AllowedProperties: append(append([]string(nil), streamProperties...),
				"reality_settings.dest", "reality_settings.server_name",
				"reality_settings.private_key", "reality_settings.public_key",
				"reality_settings.short_id", "flow"),
			Enums: map[string][]string{
				"network": append([]string(nil), streamNetworks...),
				// xboard 的三态。1（普通 TLS）暂时不给选：校验器那边写死了
				// "证书生命周期完成前仅允许 tls=false"，放出来只会让人填完
				// 保存失败。等证书那套做完再开。
				"tls":                   {"0", "2"},
				"mode":                  {"auto", "packet-up", "stream-up", "stream-one", "stream-down"},
				"session_placement":     {"path", "query", "header", "cookie"},
				"seq_placement":         {"path", "query", "header", "cookie"},
				"uplink_data_placement": {"body", "query", "header", "cookie"},
				"uplink_http_method":    {"GET", "POST"},
			},
			PropertyTypes: cloneStringMap(streamPropertyTypes),
			// private_key 会被 RedactProtocolConfig 从读接口里抹掉，
			// 后台只能写不能回显 —— 和其它密钥一个待遇。
			SensitiveProperties: []string{"private_key", "mask_password"}},
		{NodeType: "vmess", Version: 1, Status: "stable",
			AllowedProperties: append([]string(nil), append(streamProperties,
				"security")...),
			Enums: map[string][]string{
				"network": append([]string(nil), streamNetworks...),
				// VMess 不支持 REALITY，所以只有 0 一个可选值。留着这个下拉
				// 而不是把字段藏掉：藏掉之后照 xboard 教程填的人会以为漏了
				// 什么，摆在那里显示只有一个选项，一眼就知道是不支持。
				"tls":                   {"0"},
				"security":              {"none", "zero", "aes-128-gcm", "chacha20-poly1305", "auto"},
				"mode":                  {"auto", "packet-up", "stream-up", "stream-one", "stream-down"},
				"session_placement":     {"path", "query", "header", "cookie"},
				"seq_placement":         {"path", "query", "header", "cookie"},
				"uplink_data_placement": {"body", "query", "header", "cookie"},
				"uplink_http_method":    {"GET", "POST"},
			},
			PropertyTypes: cloneStringMap(streamPropertyTypes),
		},
	}
	for _, nodeType := range legacyProtocolTypes {
		out = append(out, ProtocolSchema{
			NodeType: nodeType,
			Version:  0,
			Status:   "legacy-read-compatible",
		})
	}
	return out
}

func cloneStringMap(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

// RedactProtocolConfig removes credential-like keys before protocol JSON is
// returned by an admin read API. Stable schemas intentionally contain no
// stored secret today, while legacy v0 objects can have arbitrary historical
// fields and must therefore be treated as sensitive by name at every depth.
func RedactProtocolConfig(raw json.RawMessage) json.RawMessage {
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
