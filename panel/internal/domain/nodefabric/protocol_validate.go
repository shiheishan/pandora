// [INPUT]: 依赖 protocol_schema.go 的 ProtocolSchemas 与 CanonicalNodeType，依赖同包 protocol_validate_*.go 的分项校验，依赖标准库 encoding/json
// [OUTPUT]: 对外提供 ValidateProtocolConfig；包内提供严格 JSON 解码（拒绝重复键、尾随内容）与取整数、查成员等小助手
// [POS]: domain/nodefabric 协议配置的内核形状校验入口：从 protocol_schema.go 拆出。按协议逐字段校验并返回字段级中文错误，传输层、REALITY、mKCP 的分项校验在 protocol_validate_transport.go / protocol_validate_reality.go / protocol_validate_mkcp.go
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package nodefabric

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
)

var errDuplicateJSONKey = errors.New("duplicate JSON object key")

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

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
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
