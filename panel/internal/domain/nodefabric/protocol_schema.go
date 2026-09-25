// [INPUT]: 依赖标准库 encoding/json 与 net，依赖 golang.org/x/crypto/curve25519 生成 REALITY 密钥对
// [OUTPUT]: 对外提供 ProtocolSchema 与 ProtocolSchemas 协议约束元数据、CanonicalNodeType、RedactProtocolConfig 抹敏、ValidateProtocolConfig 内核形状校验、GenerateRealityKeypair
// [POS]: domain/nodefabric 的协议约束中心：xboard_validate 先翻译再调这里校验，读接口经 RedactProtocolConfig 抹敏，protocol_secrets 是抹敏的逆运算；sensitiveProtocolKey 必须覆盖每个 schema 的 SensitiveProperties（单测守住）
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package nodefabric

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/curve25519"
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

var errDuplicateJSONKey = errors.New("duplicate JSON object key")

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

// ValidateProtocolConfig 校验一次新的协议写入。迁移前 schema_version=0 的记录
// 可以继续读取；任何新编辑必须升级到当前服务端 Schema。
func ValidateProtocolConfig(nodeType, kernel string, port int, raw json.RawMessage) (int, map[string]string) {
	fields := map[string]string{}
	nodeType = CanonicalNodeType(nodeType)
	if nodeType == "" {
		return 0, fields
	}
	if port < 1 || port > 65535 {
		fields["server_port"] = "端口必须在 1–65535 之间"
	}
	if kernel != "" && kernel != "auto" && kernel != "pandora-native" && kernel != "sing-box" && kernel != "xray-core" {
		fields["kernel"] = "只能是 auto、pandora-native、sing-box 或 xray-core"
	}
	if containsString(legacyProtocolTypes, nodeType) {
		// 兼容迁移前已经运行的协议。v0 只保证 JSON 对象和大小边界，
		// 不会写 config_validated_at，也不会冒充已通过版本化 Schema。
		if len(raw) == 0 || len(raw) > 16*1024 {
			fields["protocol_config"] = "协议配置必须是 16 KiB 以内的 JSON 对象"
			return 0, fields
		}
		if err := rejectDuplicateJSONKeys(raw); err != nil {
			if errors.Is(err, errDuplicateJSONKey) {
				fields["protocol_config"] = "协议配置不能包含重复字段"
			} else {
				fields["protocol_config"] = "协议配置必须是合法的 JSON 对象"
			}
			return 0, fields
		}
		var legacy map[string]any
		if err := json.Unmarshal(raw, &legacy); err != nil || legacy == nil {
			fields["protocol_config"] = "协议配置必须是 JSON 对象"
		} else if len(legacy) > 64 {
			fields["protocol_config"] = "协议配置字段过多"
		}
		return 0, fields
	}

	if !containsString([]string{"anytls", "http", "hysteria2", "juicity", "mieru", "naive", "shadowsocks", "shadowtls", "socks", "trojan", "tuic", "vless", "vmess"}, nodeType) {
		fields["node_type"] = "不支持的协议类型"
		return 0, fields
	}

	switch nodeType {
	case "shadowsocks":
		var cfg struct {
			Method string `json:"method"`
		}
		if err := decodeStrictProtocolObject(raw, &cfg); err != nil {
			setProtocolObjectError(fields, err, "必须是仅包含 method 的 JSON 对象")
		} else if !containsString(shadowsocksMethods, cfg.Method) {
			fields["protocol_config.method"] = "不支持的 Shadowsocks 加密方法"
		}
	case "socks", "http", "naive":
		var cfg struct {
			Network  string `json:"network"`
			TLS      bool   `json:"tls"`
			CertPath string `json:"cert_path"`
			KeyPath  string `json:"key_path"`
			Security string `json:"security"`
		}
		if err := decodeStrictProtocolObject(raw, &cfg); err != nil {
			setProtocolObjectError(fields, err, "SOCKS/HTTP 配置包含不支持的字段或类型")
			break
		}
		network := strings.ToLower(strings.TrimSpace(cfg.Network))
		if network == "" {
			network = "tcp"
		}
		if nodeType == "http" && network != "tcp" {
			fields["protocol_config.network"] = "Native HTTP 仅支持 TCP"
		}
		if nodeType == "socks" && network != "tcp" && network != "udp" {
			fields["protocol_config.network"] = "Native SOCKS 仅支持 TCP 或 UDP"
		}
		if nodeType == "naive" && network != "tcp" {
			fields["protocol_config.network"] = "Native Naive 仅支持 TCP"
		}
		security := strings.ToLower(strings.TrimSpace(cfg.Security))
		if security != "" && security != "none" {
			fields["protocol_config.security"] = "Native 代理仅支持 security=none"
		}
		if nodeType == "naive" && !cfg.TLS {
			fields["protocol_config.tls"] = "Naive 必须启用 TLS"
		}
		if (strings.TrimSpace(cfg.CertPath) == "") != (strings.TrimSpace(cfg.KeyPath) == "") {
			fields["protocol_config.cert_path"] = "cert_path 和 key_path 必须同时提供"
		}
		if cfg.TLS && (strings.TrimSpace(cfg.CertPath) == "" || strings.TrimSpace(cfg.KeyPath) == "") {
			fields["protocol_config.tls"] = "启用 TLS 时必须提供 cert_path 和 key_path"
		}
		if nodeType == "naive" && (strings.TrimSpace(cfg.CertPath) == "" || strings.TrimSpace(cfg.KeyPath) == "") {
			fields["protocol_config.cert_path"] = "Naive TLS 必须提供 cert_path 和 key_path"
		}
	case "mieru":
		var cfg struct {
			Transport string `json:"transport"`
		}
		if err := decodeStrictProtocolObject(raw, &cfg); err != nil {
			setProtocolObjectError(fields, err, "Mieru 配置包含不支持的字段或类型")
			break
		}
		transport := strings.ToLower(strings.TrimSpace(cfg.Transport))
		if transport != "" && transport != "tcp" && transport != "udp" {
			fields["protocol_config.transport"] = "Mieru 仅支持 transport=tcp 或 udp"
		}
	case "shadowtls":
		var cfg struct {
			Network         string          `json:"network"`
			Version         json.RawMessage `json:"version"`
			Password        string          `json:"password"`
			Server          string          `json:"server"`
			HandshakeServer string          `json:"handshake_server"`
			ServerPort      json.RawMessage `json:"server_port"`
			Method          string          `json:"method"`
			Strict          bool            `json:"strict"`
		}
		if err := decodeStrictProtocolObject(raw, &cfg); err != nil {
			setProtocolObjectError(fields, err, "ShadowTLS 配置包含不支持的字段或类型")
			break
		}
		if network := strings.ToLower(strings.TrimSpace(cfg.Network)); network != "" && network != "tcp" {
			fields["protocol_config.network"] = "ShadowTLS 仅支持 TCP"
		}
		if len(bytes.TrimSpace(cfg.Version)) > 0 && !bytes.Equal(bytes.TrimSpace(cfg.Version), []byte("null")) {
			if version, ok := protocolJSONIntFromRaw(cfg.Version); !ok || version != 3 {
				fields["protocol_config.version"] = "ShadowTLS 仅支持 version=3"
			}
		}
		if strings.TrimSpace(cfg.Password) == "" {
			fields["protocol_config.password"] = "ShadowTLS 必须提供外层密码"
		}
		server := firstNonEmpty(cfg.Server, cfg.HandshakeServer)
		if server == "" {
			fields["protocol_config.server"] = "ShadowTLS 必须提供握手服务器"
		} else {
			validateShadowTLSServer(fields, server, cfg.ServerPort)
		}
		if method := strings.ToLower(strings.TrimSpace(cfg.Method)); method != "" && !containsString(shadowsocksMethods, method) {
			fields["protocol_config.method"] = "ShadowTLS 仅支持当前 NativeCore 的 Shadowsocks 方法"
		}
	case "hysteria2":
		var cfg struct {
			Network    string          `json:"network"`
			CertPath   string          `json:"cert_path"`
			KeyPath    string          `json:"key_path"`
			Obfs       map[string]any  `json:"obfs"`
			UpMbps     json.RawMessage `json:"up_mbps"`
			DownMbps   json.RawMessage `json:"down_mbps"`
			UDPTimeout json.RawMessage `json:"udp_timeout"`
		}
		if err := decodeStrictProtocolObject(raw, &cfg); err != nil {
			setProtocolObjectError(fields, err, "Hysteria2 配置包含不支持的字段或类型")
			break
		}
		if network := strings.ToLower(strings.TrimSpace(cfg.Network)); network != "" && network != "udp" {
			fields["protocol_config.network"] = "Hysteria2 仅支持 UDP"
		}
		if strings.TrimSpace(cfg.CertPath) == "" {
			fields["protocol_config.cert_path"] = "Hysteria2 必须提供 cert_path"
		}
		if strings.TrimSpace(cfg.KeyPath) == "" {
			fields["protocol_config.key_path"] = "Hysteria2 必须提供 key_path"
		}
		validateHysteria2Int(fields, "up_mbps", cfg.UpMbps)
		validateHysteria2Int(fields, "down_mbps", cfg.DownMbps)
		validateHysteria2Timeout(fields, "udp_timeout", cfg.UDPTimeout)
		if cfg.Obfs != nil {
			typ, _ := cfg.Obfs["type"].(string)
			if strings.ToLower(strings.TrimSpace(typ)) != "salamander" {
				fields["protocol_config.obfs"] = "Hysteria2 仅支持 salamander 混淆"
			} else if password, _ := cfg.Obfs["password"].(string); strings.TrimSpace(password) == "" {
				fields["protocol_config.obfs"] = "salamander 混淆必须提供 password"
			}
		}
	case "juicity":
		var cfg struct {
			Network           string `json:"network"`
			CertPath          string `json:"cert_path"`
			KeyPath           string `json:"key_path"`
			CongestionControl string `json:"congestion_control"`
		}
		if err := decodeStrictProtocolObject(raw, &cfg); err != nil {
			setProtocolObjectError(fields, err, "Juicity 配置包含不支持的字段或类型")
			break
		}
		if network := strings.ToLower(strings.TrimSpace(cfg.Network)); network != "" && network != "udp" {
			fields["protocol_config.network"] = "Juicity 仅支持 UDP"
		}
		if strings.TrimSpace(cfg.CertPath) == "" {
			fields["protocol_config.cert_path"] = "Juicity 必须提供 cert_path"
		}
		if strings.TrimSpace(cfg.KeyPath) == "" {
			fields["protocol_config.key_path"] = "Juicity 必须提供 key_path"
		}
		if cc := strings.ToLower(strings.TrimSpace(cfg.CongestionControl)); cc != "" && !containsString([]string{"cubic", "new_reno", "bbr"}, cc) {
			fields["protocol_config.congestion_control"] = "Juicity 仅支持 cubic、new_reno 或 bbr"
		}
	case "tuic":
		var cfg struct {
			Network           string          `json:"network"`
			CertPath          string          `json:"cert_path"`
			KeyPath           string          `json:"key_path"`
			CongestionControl string          `json:"congestion_control"`
			AuthTimeout       json.RawMessage `json:"auth_timeout"`
			Heartbeat         json.RawMessage `json:"heartbeat"`
			UDPTimeout        json.RawMessage `json:"udp_timeout"`
			ZeroRTT           bool            `json:"zero_rtt"`
			// 刻意没有 udp_over_stream：它是客户端侧的 UDP 中继模式选择，
			// 服务端两种都收，入站没有对应的开关。字段不在这里，
			// decodeStrictProtocolObject 会把它判成不支持的字段直接拒掉——
			// 那比存下来然后被节点端忽略要好。
		}
		if err := decodeStrictProtocolObject(raw, &cfg); err != nil {
			setProtocolObjectError(fields, err, "TUIC 配置包含不支持的字段或类型")
			break
		}
		if network := strings.ToLower(strings.TrimSpace(cfg.Network)); network != "" && network != "udp" {
			fields["protocol_config.network"] = "TUIC 仅支持 UDP"
		}
		if strings.TrimSpace(cfg.CertPath) == "" {
			fields["protocol_config.cert_path"] = "TUIC 必须提供 cert_path"
		}
		if strings.TrimSpace(cfg.KeyPath) == "" {
			fields["protocol_config.key_path"] = "TUIC 必须提供 key_path"
		}
		if cc := strings.ToLower(strings.TrimSpace(cfg.CongestionControl)); cc != "" && !containsString([]string{"cubic", "new_reno", "bbr"}, cc) {
			fields["protocol_config.congestion_control"] = "TUIC 仅支持 cubic、new_reno 或 bbr"
		}
		validateHysteria2Timeout(fields, "auth_timeout", cfg.AuthTimeout)
		validateHysteria2Timeout(fields, "heartbeat", cfg.Heartbeat)
		validateHysteria2Timeout(fields, "udp_timeout", cfg.UDPTimeout)
	case "anytls":
		var cfg struct {
			Network       string          `json:"network"`
			TLS           bool            `json:"tls"`
			CertPath      string          `json:"cert_path"`
			KeyPath       string          `json:"key_path"`
			PaddingScheme json.RawMessage `json:"padding_scheme"`
		}
		if err := decodeStrictProtocolObject(raw, &cfg); err != nil {
			setProtocolObjectError(fields, err, "AnyTLS 配置包含不支持的字段或类型")
			break
		}
		if network := strings.ToLower(strings.TrimSpace(cfg.Network)); network != "" && network != "tcp" {
			fields["protocol_config.network"] = "AnyTLS 仅支持 TCP"
		}
		if (strings.TrimSpace(cfg.CertPath) == "") != (strings.TrimSpace(cfg.KeyPath) == "") {
			fields["protocol_config.cert_path"] = "cert_path 和 key_path 必须同时提供"
		}
		if cfg.TLS && (strings.TrimSpace(cfg.CertPath) == "" || strings.TrimSpace(cfg.KeyPath) == "") {
			fields["protocol_config.tls"] = "启用 AnyTLS TLS 时必须提供 cert_path 和 key_path"
		}
		validateAnyTLSPadding(fields, cfg.PaddingScheme)
	case "trojan":
		var cfg struct {
			Network         string   `json:"network"`
			TLS             *bool    `json:"tls"`
			Path            string   `json:"path"`
			WSPath          string   `json:"ws_path"`
			Host            string   `json:"host"`
			GRPCPath        string   `json:"grpc_path"`
			GRPCServiceName string   `json:"grpc_service_name"`
			Security        string   `json:"security"`
			Dest            string   `json:"dest"`
			ServerNames     []string `json:"server_names"`
			PrivateKey      string   `json:"private_key"`
			PublicKey       string   `json:"public_key"`
			ShortIDs        []string `json:"short_ids"`
			Fingerprint     string   `json:"fingerprint"`
			CertPath        string   `json:"cert_path"`
			KeyPath         string   `json:"key_path"`

			// mKCP 及其掩码，全部可选。
			MTU              json.RawMessage `json:"mtu"`
			TTI              json.RawMessage `json:"tti"`
			UplinkCapacity   json.RawMessage `json:"uplink_capacity"`
			DownlinkCapacity json.RawMessage `json:"downlink_capacity"`
			Congestion       bool            `json:"congestion"`
			ReadBufferSize   json.RawMessage `json:"read_buffer_size"`
			WriteBufferSize  json.RawMessage `json:"write_buffer_size"`
			Mask             string          `json:"mask"`
			MaskPassword     string          `json:"mask_password"`
		}
		if err := decodeStrictProtocolObject(raw, &cfg); err != nil {
			setProtocolObjectError(fields, err, "Trojan 配置包含不支持的字段或类型")
			break
		}
		validateMKCPConfig(fields, cfg.Network, cfg.MTU, cfg.TTI,
			cfg.UplinkCapacity, cfg.DownlinkCapacity,
			cfg.ReadBufferSize, cfg.WriteBufferSize)
		validateMKCPMask(fields, cfg.Network, cfg.Mask, cfg.MaskPassword, cfg.MTU)
		network := strings.ToLower(strings.TrimSpace(cfg.Network))
		if network == "" {
			network = "tcp"
		}
		if !containsString([]string{"tcp", "ws", "httpupgrade", "grpc", "mkcp"}, network) {
			fields["protocol_config.network"] = "Native Trojan 仅支持 tcp、ws、httpupgrade、grpc、mkcp"
		}
		if strings.ContainsAny(cfg.Host, "\r\n") {
			fields["protocol_config.host"] = "host 不能包含换行或控制字符"
		}
		if network == "ws" || network == "httpupgrade" {
			validateHTTPTransportPath(fields, "path", firstNonEmpty(cfg.WSPath, cfg.Path))
		}
		if network == "grpc" {
			if cfg.GRPCPath != "" && (len(cfg.GRPCPath) > 512 || !strings.HasPrefix(cfg.GRPCPath, "/") || strings.ContainsAny(cfg.GRPCPath, "\r\n")) {
				fields["protocol_config.grpc_path"] = "grpc_path 必须以 / 开头且不超过 512 字符"
			}
			if strings.ContainsAny(cfg.GRPCServiceName, "\r\n") {
				fields["protocol_config.grpc_service_name"] = "grpc_service_name 不能包含换行或控制字符"
			}
		}
		if cfg.TLS == nil {
			fields["protocol_config.tls"] = "Trojan 必须显式设置 tls=true 或 security=reality"
		}
		security := strings.ToLower(strings.TrimSpace(cfg.Security))
		switch security {
		case "", "none":
			if cfg.TLS == nil || !*cfg.TLS {
				fields["protocol_config.tls"] = "Trojan 普通传输必须启用 tls"
			}
			if (strings.TrimSpace(cfg.CertPath) == "") != (strings.TrimSpace(cfg.KeyPath) == "") {
				fields["protocol_config.cert_path"] = "cert_path 和 key_path 必须同时提供"
			} else if strings.TrimSpace(cfg.CertPath) == "" || strings.TrimSpace(cfg.KeyPath) == "" {
				fields["protocol_config.cert_path"] = "Trojan TLS 必须提供 cert_path 和 key_path"
			}
		case "reality":
			if cfg.TLS != nil && *cfg.TLS {
				fields["protocol_config.tls"] = "security=reality 时 tls 必须为 false"
			}
			if network != "tcp" {
				fields["protocol_config.security"] = "Native Trojan REALITY 目前仅支持 tcp"
			}
			validateRealityFields(fields, cfg.Dest, cfg.ServerNames, cfg.PrivateKey, cfg.PublicKey, cfg.ShortIDs)
		default:
			fields["protocol_config.security"] = "Trojan 仅支持 none 或 reality"
		}
	case "vless", "vmess":
		var cfg struct {
			Network              string            `json:"network"`
			TLS                  bool              `json:"tls"`
			Path                 string            `json:"path"`
			WSPath               string            `json:"ws_path"`
			Host                 string            `json:"host"`
			GRPCPath             string            `json:"grpc_path"`
			GRPCServiceName      string            `json:"grpc_service_name"`
			Mode                 string            `json:"mode"`
			Headers              map[string]string `json:"headers"`
			MaxEachPostBytes     json.RawMessage   `json:"sc_max_each_post_bytes"`
			MinPostsIntervalMS   json.RawMessage   `json:"sc_min_posts_interval_ms"`
			MaxBufferedPosts     json.RawMessage   `json:"sc_max_buffered_posts"`
			StreamUpServerSecs   json.RawMessage   `json:"sc_stream_up_server_secs"`
			SessionPlacement     string            `json:"session_placement"`
			SessionKey           string            `json:"session_key"`
			SeqPlacement         string            `json:"seq_placement"`
			SeqKey               string            `json:"seq_key"`
			UplinkHTTPMethod     string            `json:"uplink_http_method"`
			UplinkDataPlacement  string            `json:"uplink_data_placement"`
			UplinkDataKey        string            `json:"uplink_data_key"`
			UplinkChunkSize      json.RawMessage   `json:"uplink_chunk_size"`
			ServerMaxHeaderBytes json.RawMessage   `json:"server_max_header_bytes"`
			CertPath             string            `json:"cert_path"`
			KeyPath              string            `json:"key_path"`

			// REALITY。它是 TLS 之外的另一条路，也是目前唯一能落地的一条：
			// 普通 TLS 要证书，而证书生命周期还没做完；REALITY 借用真实站点
			// 的握手，服务端只要一对 x25519 密钥，不碰证书。
			Security    string   `json:"security"`
			Dest        string   `json:"dest"`
			ServerNames []string `json:"server_names"`
			PrivateKey  string   `json:"private_key"`
			PublicKey   string   `json:"public_key"`
			ShortIDs    []string `json:"short_ids"`
			Fingerprint string   `json:"fingerprint"`
			Flow        string   `json:"flow"`

			// mKCP。字段名跟随 xray 的 kcpSettings，从客户端配置里抄过来能用。
			// 全部可选——不填就是内核那边的默认值。
			MTU              json.RawMessage `json:"mtu"`
			TTI              json.RawMessage `json:"tti"`
			UplinkCapacity   json.RawMessage `json:"uplink_capacity"`
			DownlinkCapacity json.RawMessage `json:"downlink_capacity"`
			Congestion       bool            `json:"congestion"`
			ReadBufferSize   json.RawMessage `json:"read_buffer_size"`
			WriteBufferSize  json.RawMessage `json:"write_buffer_size"`
			Mask             string          `json:"mask"`
			MaskPassword     string          `json:"mask_password"`
		}
		if err := decodeStrictProtocolObject(raw, &cfg); err != nil {
			setProtocolObjectError(fields, err,
				"仅支持 network、tls、security、dest、server_names、"+
					"private_key、public_key、short_ids、fingerprint、flow、"+
					"mtu、tti、uplink_capacity、downlink_capacity、congestion、"+
					"read_buffer_size、write_buffer_size、mask、mask_password")
			break
		}
		validateMKCPConfig(fields, cfg.Network, cfg.MTU, cfg.TTI,
			cfg.UplinkCapacity, cfg.DownlinkCapacity,
			cfg.ReadBufferSize, cfg.WriteBufferSize)
		validateMKCPMask(fields, cfg.Network, cfg.Mask, cfg.MaskPassword, cfg.MTU)
		validateNativeStreamConfig(fields, nodeType, cfg.Network, cfg.Path, cfg.WSPath,
			cfg.Host, cfg.GRPCPath, cfg.GRPCServiceName, cfg.Mode, cfg.Headers,
			cfg.MaxEachPostBytes, cfg.MinPostsIntervalMS, cfg.MaxBufferedPosts,
			cfg.StreamUpServerSecs, cfg.SessionPlacement, cfg.SeqPlacement,
			cfg.UplinkHTTPMethod, cfg.UplinkDataPlacement, cfg.UplinkChunkSize,
			cfg.ServerMaxHeaderBytes)
		if strings.EqualFold(strings.TrimSpace(cfg.Network), "xhttp-h3") && (nodeType == "vmess" || strings.TrimSpace(cfg.Security) == "" || strings.EqualFold(strings.TrimSpace(cfg.Security), "none")) &&
			(strings.TrimSpace(cfg.CertPath) == "" || strings.TrimSpace(cfg.KeyPath) == "") {
			fields["protocol_config.cert_path"] = "xhttp-h3 非 REALITY 模式必须同时提供 cert_path 和 key_path"
		}

		security := strings.ToLower(strings.TrimSpace(cfg.Security))
		if nodeType == "vmess" {
			if cfg.TLS {
				fields["protocol_config.tls"] = "证书生命周期完成前仅允许 tls=false，请改用 NativeCore 已验证的传输模式"
			}
			if security == "reality" {
				fields["protocol_config.security"] = "REALITY 目前只支持 vless"
			} else if security != "" && !containsString([]string{"none", "zero", "aes-128-gcm", "chacha20-poly1305", "auto"}, security) {
				fields["protocol_config.security"] = "VMess 仅支持 none、zero、aes-128-gcm、chacha20-poly1305 或 auto"
			}
			break
		}
		switch security {
		case "", "none":
			if cfg.TLS {
				fields["protocol_config.tls"] = "证书生命周期完成前仅允许 tls=false，请改用 security=reality"
			}
		case "reality":
			if nodeType != "vless" {
				fields["protocol_config.security"] = "REALITY 目前只支持 vless"
			}
			if cfg.TLS {
				// 两者都是 TLS 层，同时开等于让内核二选一，
				// 而它选哪个取决于实现细节 —— 不该让配置有这种歧义
				fields["protocol_config.tls"] = "security=reality 时 tls 必须为 false"
			}
			validateRealityFields(fields, cfg.Dest, cfg.ServerNames,
				cfg.PrivateKey, cfg.PublicKey, cfg.ShortIDs)
		default:
			fields["protocol_config.security"] = "仅支持 none 或 reality"
		}
	}
	return 1, fields
}

func decodeStrictProtocolObject(raw []byte, out any) error {
	if len(raw) == 0 || len(raw) > 16*1024 || len(bytes.TrimSpace(raw)) == 0 || bytes.TrimSpace(raw)[0] != '{' {
		return errors.New("protocol config must be a bounded JSON object")
	}
	if err := rejectDuplicateJSONKeys(raw); err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return err
	}
	return ensureJSONEOF(dec)
}

func setProtocolObjectError(fields map[string]string, err error, allowed string) {
	if errors.Is(err, errDuplicateJSONKey) {
		fields["protocol_config"] = "协议配置不能包含重复字段"
		return
	}
	fields["protocol_config"] = allowed
}

func validateNativeStreamConfig(fields map[string]string, nodeType, network, path, wsPath, host, grpcPath, grpcService, mode string, headers map[string]string, maxPost, minInterval json.RawMessage, maxBuffered json.RawMessage, streamUp json.RawMessage, sessionPlacement, seqPlacement, uplinkMethod, uplinkPlacement string, uplinkChunk json.RawMessage, maxHeader json.RawMessage) {
	network = strings.ToLower(strings.TrimSpace(network))
	if network == "" {
		network = "tcp"
	}
	if !containsString([]string{"tcp", "ws", "httpupgrade", "grpc", "xhttp", "xhttp-h3", "mkcp"}, network) {
		fields["protocol_config.network"] = "NativeCore 支持 tcp、ws、httpupgrade、grpc、xhttp、xhttp-h3 或 mkcp"
		return
	}
	if strings.ContainsAny(host, "\r\n") {
		fields["protocol_config.host"] = "host 不能包含换行或控制字符"
	}
	if network == "ws" || network == "httpupgrade" {
		validateHTTPTransportPath(fields, "path", firstNonEmpty(wsPath, path))
	}
	if network == "grpc" {
		if grpcPath != "" && (len(grpcPath) > 512 || !strings.HasPrefix(grpcPath, "/") || strings.ContainsAny(grpcPath, "\r\n")) {
			fields["protocol_config.grpc_path"] = "grpc_path 必须是以 / 开头且不超过 512 字符的路径"
		}
		if grpcService != "" && strings.ContainsAny(grpcService, "\r\n") {
			fields["protocol_config.grpc_service_name"] = "grpc_service_name 不能包含换行或控制字符"
		}
	}
	if network != "xhttp" && network != "xhttp-h3" {
		return
	}
	for key, value := range headers {
		if strings.TrimSpace(key) == "" || strings.ContainsAny(key+value, "\r\n") {
			fields["protocol_config.headers"] = "XHTTP headers 的名称和值不能包含空白键或换行"
			break
		}
	}
	validateHTTPTransportPath(fields, "path", path)
	if mode != "" && !containsString([]string{"auto", "packet-up", "stream-up", "stream-one", "stream-down"}, strings.ToLower(strings.TrimSpace(mode))) {
		fields["protocol_config.mode"] = "不支持的 XHTTP mode"
	}
	validateXHTTPRange(fields, "sc_max_each_post_bytes", maxPost, 1, 64*1024*1024)
	validateXHTTPRange(fields, "sc_min_posts_interval_ms", minInterval, 0, 24*60*60*1000)
	validateXHTTPRange(fields, "sc_stream_up_server_secs", streamUp, 0, 24*60*60)
	validateXHTTPRange(fields, "uplink_chunk_size", uplinkChunk, 64, 64*1024*1024)
	validateXHTTPInt(fields, "sc_max_buffered_posts", maxBuffered, 0, 10000)
	validateXHTTPInt(fields, "server_max_header_bytes", maxHeader, 1024, 1<<20)
	if uplinkMethod != "" && !containsString([]string{"GET", "POST"}, strings.ToUpper(strings.TrimSpace(uplinkMethod))) {
		fields["protocol_config.uplink_http_method"] = "uplink_http_method 仅支持 GET 或 POST"
	}
	if !containsString([]string{"", "path", "query", "header", "cookie"}, strings.ToLower(strings.TrimSpace(sessionPlacement))) {
		fields["protocol_config.session_placement"] = "session_placement 不受支持"
	}
	if !containsString([]string{"", "path", "query", "header", "cookie"}, strings.ToLower(strings.TrimSpace(seqPlacement))) {
		fields["protocol_config.seq_placement"] = "seq_placement 不受支持"
	}
	if !containsString([]string{"", "body", "path", "query", "header", "cookie"}, strings.ToLower(strings.TrimSpace(uplinkPlacement))) {
		fields["protocol_config.uplink_data_placement"] = "uplink_data_placement 不受支持"
	}
	_ = nodeType
	_ = headers // map[string]string decoding is the type and control-character gate.
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func validateHTTPTransportPath(fields map[string]string, key, path string) {
	path = strings.TrimSpace(path)
	if path != "" && (!strings.HasPrefix(path, "/") || strings.ContainsAny(path, "\r\n")) {
		fields["protocol_config."+key] = key + " 必须以 / 开头且不能包含控制字符"
	}
}

func validateXHTTPRange(fields map[string]string, key string, raw json.RawMessage, minValue, maxValue int) {
	if len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return
	}
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil || value == nil {
		fields["protocol_config."+key] = key + " 必须是 {from,to} 对象"
		return
	}
	from, fromOK := protocolJSONInt(value["from"])
	to, toOK := protocolJSONInt(value["to"])
	if !fromOK || !toOK || from < minValue || to < from || to > maxValue {
		fields["protocol_config."+key] = key + " 范围无效"
	}
}

func validateXHTTPInt(fields map[string]string, key string, raw json.RawMessage, minValue, maxValue int) {
	if len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		fields["protocol_config."+key] = key + " 必须是整数"
		return
	}
	parsed, ok := protocolJSONInt(value)
	if !ok || parsed < minValue || parsed > maxValue {
		fields["protocol_config."+key] = key + " 超出范围"
	}
}

func validateHysteria2Int(fields map[string]string, key string, raw json.RawMessage) {
	if len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		fields["protocol_config."+key] = key + " 必须是非负整数"
		return
	}
	parsed, ok := protocolJSONInt(value)
	if !ok || parsed < 0 {
		fields["protocol_config."+key] = key + " 必须是非负整数"
	}
}

func validateHysteria2Timeout(fields map[string]string, key string, raw json.RawMessage) {
	if len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		fields["protocol_config."+key] = key + " 必须是非负秒数或 duration 字符串"
		return
	}
	switch value := value.(type) {
	case float64:
		if value < 0 || value != float64(int(value)) {
			fields["protocol_config."+key] = key + " 必须是非负秒数或 duration 字符串"
		}
	case string:
		value = strings.TrimSpace(value)
		if value == "" {
			fields["protocol_config."+key] = key + " 不能为空"
			return
		}
		if seconds, err := strconv.Atoi(value); err == nil && seconds >= 0 {
			return
		}
		if duration, err := time.ParseDuration(value); err != nil || duration < 0 {
			fields["protocol_config."+key] = key + " 必须是非负秒数或 duration 字符串"
		}
	default:
		fields["protocol_config."+key] = key + " 必须是非负秒数或 duration 字符串"
	}
}

func validateAnyTLSPadding(fields map[string]string, raw json.RawMessage) {
	if len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		fields["protocol_config.padding_scheme"] = "padding_scheme 必须是非空字符串或字符串数组"
		return
	}
	switch value := value.(type) {
	case string:
		if strings.TrimSpace(value) == "" {
			fields["protocol_config.padding_scheme"] = "padding_scheme 不能为空"
		}
	case []any:
		if len(value) == 0 {
			fields["protocol_config.padding_scheme"] = "padding_scheme 不能为空"
			return
		}
		for _, item := range value {
			text, ok := item.(string)
			if !ok || strings.TrimSpace(text) == "" {
				fields["protocol_config.padding_scheme"] = "padding_scheme 数组必须全部是非空字符串"
				break
			}
		}
	default:
		fields["protocol_config.padding_scheme"] = "padding_scheme 必须是非空字符串或字符串数组"
	}
}

func protocolJSONInt(value any) (int, bool) {
	switch value := value.(type) {
	case float64:
		return int(value), value == float64(int(value))
	case json.Number:
		parsed, err := strconv.Atoi(string(value))
		return parsed, err == nil
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(value))
		return parsed, err == nil
	default:
		return 0, false
	}
}

func protocolJSONIntFromRaw(raw json.RawMessage) (int, bool) {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return 0, false
	}
	return protocolJSONInt(value)
}

func validateShadowTLSServer(fields map[string]string, server string, portRaw json.RawMessage) {
	server = strings.TrimSpace(server)
	if strings.ContainsAny(server, "\r\n") {
		fields["protocol_config.server"] = "握手服务器不能包含换行或控制字符"
		return
	}
	host := server
	if strings.Contains(server, ":") {
		parsedHost, parsedPort, err := net.SplitHostPort(server)
		if err != nil || parsedHost == "" {
			fields["protocol_config.server"] = "握手服务器应为主机名或 host:port"
			return
		}
		host = parsedHost
		if parsed, err := strconv.Atoi(parsedPort); err != nil || parsed < 1 || parsed > 65535 {
			fields["protocol_config.server"] = "握手服务器端口无效"
			return
		}
	}
	if !validServerName(host) {
		fields["protocol_config.server"] = "握手服务器必须是有效域名或 IP"
	}
	if len(bytes.TrimSpace(portRaw)) > 0 && !bytes.Equal(bytes.TrimSpace(portRaw), []byte("null")) {
		port, ok := protocolJSONIntFromRaw(portRaw)
		if !ok || port < 1 || port > 65535 {
			fields["protocol_config.server_port"] = "server_port 必须在 1-65535 之间"
		}
	}
}

func validateServerName(fields map[string]string, name string) {
	if name == "" {
		return
	}
	if len(name) > 253 || strings.TrimSpace(name) != name || !validServerName(name) {
		fields["protocol_config.server_name"] = "必须是有效的 IP 或 ASCII 主机名"
	}
}

func validServerName(name string) bool {
	if net.ParseIP(name) != nil {
		return true
	}
	name = strings.TrimSuffix(name, ".")
	if name == "" {
		return false
	}
	for _, label := range strings.Split(name, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, r := range label {
			if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-') {
				return false
			}
		}
	}
	return true
}

// rejectDuplicateJSONKeys walks the token stream before unmarshalling so a
// later duplicate key cannot silently replace a value that was already
// validated. It rejects duplicates at every object nesting level.
func rejectDuplicateJSONKeys(raw []byte) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if err := walkJSONValue(dec, tok); err != nil {
		return err
	}
	return ensureJSONEOF(dec)
}

func walkJSONValue(dec *json.Decoder, tok json.Token) error {
	delim, ok := tok.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := make(map[string]struct{})
		for dec.More() {
			keyToken, err := dec.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("object key is not a string")
			}
			if _, exists := seen[key]; exists {
				return errDuplicateJSONKey
			}
			seen[key] = struct{}{}
			valueToken, err := dec.Token()
			if err != nil {
				return err
			}
			if err := walkJSONValue(dec, valueToken); err != nil {
				return err
			}
		}
		end, err := dec.Token()
		if err != nil || end != json.Delim('}') {
			return errors.New("unterminated JSON object")
		}
	case '[':
		for dec.More() {
			valueToken, err := dec.Token()
			if err != nil {
				return err
			}
			if err := walkJSONValue(dec, valueToken); err != nil {
				return err
			}
		}
		end, err := dec.Token()
		if err != nil || end != json.Delim(']') {
			return errors.New("unterminated JSON array")
		}
	default:
		return errors.New("unexpected JSON delimiter")
	}
	return nil
}

func ensureJSONEOF(dec *json.Decoder) error {
	var extra any
	err := dec.Decode(&extra)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err != nil {
		return err
	}
	return errors.New("additional JSON value")
}

func containsString(items []string, value string) bool {
	for _, item := range items {
		if value == item {
			return true
		}
	}
	return false
}

// validateRealityFields 校验 REALITY 的必填项。
//
// 这里严一点是有道理的：REALITY 配错不会报错，只会安静地退化成一个
// 容易识别的节点 —— 探测者拿 dest 的证书一比就露馅。所以宁可在保存时
// 拦住，也不要让一个「看起来配好了」的节点上线。
func validateRealityFields(fields map[string]string, dest string,
	names []string, privKey, pubKey string, shortIDs []string) {

	dest = strings.TrimSpace(dest)
	if dest == "" {
		fields["protocol_config.dest"] = "必填：借用握手的真实站点，如 www.microsoft.com:443"
	} else {
		host, port, err := net.SplitHostPort(dest)
		if err != nil || host == "" {
			fields["protocol_config.dest"] = "格式应为 域名:端口"
		} else if p, err := strconv.Atoi(port); err != nil || p < 1 || p > 65535 {
			fields["protocol_config.dest"] = "端口不合法"
		} else if ip := net.ParseIP(host); ip != nil {
			// 用 IP 当 dest 拿不到有意义的证书，SNI 也无从对应
			fields["protocol_config.dest"] = "要填域名而不是 IP"
		}
	}

	if len(names) == 0 {
		fields["protocol_config.server_names"] = "必填：至少一个，且要和 dest 的证书对得上"
	} else {
		for _, n := range names {
			if strings.TrimSpace(n) == "" || strings.ContainsAny(n, " /:") {
				fields["protocol_config.server_names"] = "每一项都应是纯域名"
				break
			}
		}
	}

	if err := checkX25519Key(privKey); err != nil {
		fields["protocol_config.private_key"] = "私钥" + err.Error()
	}
	// 公钥不参与服务端握手，但订阅链接要发给客户端。
	// 缺了它节点能起来、用户却连不上，是最难查的一类问题。
	if err := checkX25519Key(pubKey); err != nil {
		fields["protocol_config.public_key"] = "公钥" + err.Error() + "（客户端要用它，不能省）"
	}

	for _, s := range shortIDs {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if len(s) > 16 || len(s)%2 != 0 {
			fields["protocol_config.short_ids"] = "short id 应为不超过 16 位的十六进制"
			break
		}
		if _, err := hex.DecodeString(s); err != nil {
			fields["protocol_config.short_ids"] = "short id 必须是十六进制"
			break
		}
	}
}

func checkX25519Key(s string) error {
	s = strings.TrimSpace(s)
	if s == "" {
		return errors.New("必填")
	}
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		if b2, err2 := base64.StdEncoding.DecodeString(s); err2 == nil {
			b = b2
		} else {
			return errors.New("不是合法的 base64url")
		}
	}
	if len(b) != 32 {
		return fmt.Errorf("解出来应为 32 字节，实际 %d", len(b))
	}
	return nil
}

// GenerateRealityKeypair 生成一对 x25519 密钥，供后台「生成」按钮调用。
//
// 私钥留在面板并下发给节点，公钥进订阅链接发给客户端。
// 用 crypto/rand + curve25519，不引第三方工具。
func GenerateRealityKeypair() (privB64, pubB64 string, err error) {
	var priv [32]byte
	if _, err = rand.Read(priv[:]); err != nil {
		return "", "", err
	}
	// RFC 7748 的 clamping。不做的话某些实现算出的共享密钥会对不上，
	// 表现为「配置看着没错但就是连不上」。
	priv[0] &= 248
	priv[31] &= 127
	priv[31] |= 64

	pub, err := curve25519.X25519(priv[:], curve25519.Basepoint)
	if err != nil {
		return "", "", err
	}
	return base64.RawURLEncoding.EncodeToString(priv[:]),
		base64.RawURLEncoding.EncodeToString(pub), nil
}

// mKCP 的 MTU 边界。和节点端 kernel/mkcp_transport.go 里的同名常量
// 保持一致——两边对不上，就会出现「面板存进去了、节点起不来」。
//
// 上限 1460 是 UDP 载荷的上限：以太网 1500 减去 IPv4 20 + UDP 8。
// 下限 576 是 IPv4 要求的最小重组缓冲，再小没有实际意义。
const (
	mkcpMinMTU = 576
	mkcpMaxMTU = 1460
)

// validateMKCPConfig 校验 mKCP 的调优参数。
//
// 这些值最终由节点端的 ParseMKCPConfig 兜底，但那时已经晚了——配错的
// 节点要等到启动失败才发现，而管理员看到的只是「节点不健康」。在这里
// 拦住，他还站在表单前面，能立刻改。
//
// 边界跟节点端保持一致，两边对不上会出现「面板存进去了、节点起不来」
// 这种最难查的状态。
func validateMKCPConfig(fields map[string]string, network string,
	mtu, tti, uplink, downlink, readBuf, writeBuf json.RawMessage) {

	// 不是 mKCP 就不该出现这些字段：多半是从别的传输的配置里复制粘贴
	// 带过来的，留着会让人以为它们生效了。
	isMKCP := containsString([]string{"mkcp", "kcp", "m-kcp"},
		strings.ToLower(strings.TrimSpace(network)))
	if !isMKCP {
		for name, v := range map[string]json.RawMessage{
			"mtu": mtu, "tti": tti, "uplink_capacity": uplink,
			"downlink_capacity": downlink,
			"read_buffer_size":  readBuf, "write_buffer_size": writeBuf,
		} {
			if len(v) > 0 {
				fields["protocol_config."+name] = name + " 只在 network=mkcp 时有效"
			}
		}
		return
	}

	checkRange := func(name string, v json.RawMessage, lo, hi int) {
		if len(v) == 0 {
			return
		}
		n, ok := rawJSONInt(v)
		if !ok {
			fields["protocol_config."+name] = name + " 必须是整数"
			return
		}
		if n < lo || n > hi {
			fields["protocol_config."+name] = fmt.Sprintf("%s 应在 %d 到 %d 之间", name, lo, hi)
		}
	}
	checkRange("mtu", mtu, mkcpMinMTU, mkcpMaxMTU)
	// TTI 是发送周期，太长重传迟钝，太短纯烧 CPU。
	checkRange("tti", tti, 10, 100)
	// 带宽单位 MB/s，上限给到万兆。
	checkRange("uplink_capacity", uplink, 1, 1000)
	checkRange("downlink_capacity", downlink, 1, 1000)
	// 缓冲区单位 MB。
	checkRange("read_buffer_size", readBuf, 1, 64)
	checkRange("write_buffer_size", writeBuf, 1, 64)
}

// rawJSONInt 把一段 JSON 数字解成整数。
func rawJSONInt(v json.RawMessage) (int, bool) {
	var n json.Number
	if err := json.Unmarshal(v, &n); err != nil {
		return 0, false
	}
	i, err := n.Int64()
	if err != nil {
		return 0, false
	}
	return int(i), true
}

// mKCP 掩码的每包开销。和节点端 udpmask 的实现保持一致——两边对不上会
// 出现「面板存进去了、节点起不来」这种最难查的状态。
var mkcpMaskOverhead = map[string]int{
	"":               0,
	"none":           0,
	"mkcp-original":  0,  // 上游给「不加掩码」起的名字
	"mkcp-aes128gcm": 28, // 12 字节 nonce + 16 字节 tag
}

// validateMKCPMask 校验掩码设置。
func validateMKCPMask(fields map[string]string, network, mask, password string, mtu json.RawMessage) {
	mask = strings.ToLower(strings.TrimSpace(mask))

	isMKCP := containsString([]string{"mkcp", "kcp", "m-kcp"},
		strings.ToLower(strings.TrimSpace(network)))
	if !isMKCP {
		if mask != "" || password != "" {
			fields["protocol_config.mask"] = "mask 只在 network=mkcp 时有效"
		}
		return
	}

	overhead, known := mkcpMaskOverhead[mask]
	if !known {
		// 不认识的类型必须拒绝。放过去的话节点会裸奔，而管理员以为它
		// 加着密——这比不加密更糟，因为它给了错误的安全感。
		fields["protocol_config.mask"] = "只支持 none 或 mkcp-aes128gcm"
		return
	}

	if overhead > 0 && strings.TrimSpace(password) == "" {
		// 空密码时密钥退化成 sha256("")，谁都算得出来，那这层就只是
		// 障眼法而不是加密。
		fields["protocol_config.mask_password"] = "启用 mkcp-aes128gcm 必须设置密码"
		return
	}
	if overhead == 0 && strings.TrimSpace(password) != "" {
		fields["protocol_config.mask_password"] = "没有启用加密掩码时不应填密码"
		return
	}

	// MTU 要给掩码开销留出空间。mkcpMaxMTU 是 UDP 载荷上限，已经扣过
	// IP + UDP 头；掩码开销也加在载荷里，顶着上限再套掩码，发出去的
	// 以太网帧就超长了，会被 IP 分片。分片的 UDP 在不少网络上直接被丢，
	// 症状是「小包能通、大包不通」。
	if overhead > 0 && len(mtu) > 0 {
		if n, ok := rawJSONInt(mtu); ok && n > mkcpMaxMTU-overhead {
			fields["protocol_config.mtu"] = fmt.Sprintf(
				"启用 %s 后 mtu 不能超过 %d（掩码每包多占 %d 字节，再大会被 IP 分片）",
				mask, mkcpMaxMTU-overhead, overhead)
		}
	}
}
