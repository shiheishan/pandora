// [INPUT]: 依赖 protocol_validate.go 的 JSON 取整助手，依赖标准库 net
// [OUTPUT]: 包内提供 validateNativeStreamConfig、validateHTTPTransportPath、XHTTP / Hysteria2 数值区间、AnyTLS padding、ShadowTLS 服务器与服务器名的分项校验
// [POS]: domain/nodefabric 协议校验的传输层分项：从 protocol_schema.go 拆出，只被 ValidateProtocolConfig 调用
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package nodefabric

import (
	"bytes"
	"encoding/json"
	"net"
	"strconv"
	"strings"
	"time"
)

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
