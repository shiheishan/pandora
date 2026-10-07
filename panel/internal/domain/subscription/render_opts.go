package subscription

// 渲染前把内核形状的协议配置读成一份各格式共用的连接参数。
//
// 三种格式的字段名各不相同，但「这个节点该怎么连」只有一个答案。先在这里
// 算一次（传输、TLS / REALITY、SNI、Host、指纹），各格式只负责换个写法，
// 不各自再读一遍原始配置——三份读法迟早会漂移，漂移的表现是同一个节点在
// Clash 里能连、在 sing-box 里连不上。

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"net"
	"strconv"
	"strings"
)

// defaultFingerprint 是没配 utls 时下发的 uTLS 指纹。不给的话各家客户端默认值
// 不一致（有的干脆用 Go 自己的 TLS 指纹），同一条订阅在不同客户端上长得不一样，
// 而 Go 的指纹本身就是一个代理特征。
const defaultFingerprint = "chrome"

// streamOpts 是 vless / vmess / trojan 共用的传输与安全层参数。
type streamOpts struct {
	// Network 是规范化后的传输：tcp、ws、httpupgrade、grpc、xhttp、xhttp-h3、mkcp。
	Network string
	// Reality 为真时走 REALITY；TLS 为真时走普通 TLS。两者互斥。
	Reality bool
	TLS     bool
	// SNI：REALITY 时是借用站点的 server name，普通 TLS 时是后台配的 SNI（可空，
	// 空则客户端用服务器地址）。
	SNI      string
	Insecure bool
	// Fingerprint 是 uTLS 指纹：后台配的 utls，没配时 REALITY 与普通 TLS 都用 chrome。
	Fingerprint string
	PublicKey   string
	ShortID     string
	Flow        string
	// HTTP 承载的参数。Host 已回落到节点地址：节点端在没配 Host 时校验的就是
	// server_host（BuildNodeConfig），订阅里显式写出来，与节点同一口径。
	Path        string
	Host        string
	ServiceName string
	Mode        string
	// Unsupported 非空表示这个传输配置没有客户端能表达的写法，各格式一律跳过。
	Unsupported string
}

// parseStream 读 vless / vmess / trojan 的连接参数。cfg 是内核形状；uuid 是
// 订阅所属用户的凭据，只用来在多个 REALITY server name / short id 里稳定地挑一个。
func parseStream(n Node, uuid string) streamOpts {
	cfg := n.Config
	o := streamOpts{Network: strings.ToLower(strings.TrimSpace(cfgStr(cfg, "network", "tcp")))}
	if isMKCPNetwork(o.Network) {
		o.Network = "mkcp"
	}
	o.Reality = strings.EqualFold(strings.TrimSpace(cfgStr(cfg, "security", "")), "reality")
	switch {
	case o.Reality:
	case n.Type == "trojan":
		// Trojan 没有不加密的形态：内核只接受 tls=true 或 security=reality。
		o.TLS = true
	default:
		o.TLS = cfgBool(cfg, "tls")
	}
	o.Fingerprint = strings.TrimSpace(cfgStr(cfg, "fingerprint", ""))
	o.Flow = strings.TrimSpace(cfgStr(cfg, "flow", ""))
	if o.Reality {
		// SNI 与 short id 都是单值，而服务端可以配多个。以前总发第一个：全体
		// 用户的握手长得一模一样，配了多个也起不到分散特征的作用。现在每个
		// 用户按稳定哈希分到其中一个——同一个用户每次拉订阅拿到的不变，
		// 不同用户分散在各个值上。两者各用各的盐，组合也是分散的。
		o.SNI = pickForUser(cfgStrings(cfg, "server_names"), uuid, n, "sni")
		o.PublicKey = cfgStr(cfg, "public_key", "")
		o.ShortID = pickForUser(cfgStrings(cfg, "short_ids"), uuid, n, "sid")
		if o.Fingerprint == "" {
			o.Fingerprint = defaultFingerprint
		}
	} else if o.TLS {
		o.SNI = strings.TrimSpace(cfgStr(cfg, "server_name", ""))
		o.Insecure = cfgBool(cfg, "allow_insecure")
		if o.Fingerprint == "" {
			o.Fingerprint = defaultFingerprint
		}
	}

	host := strings.TrimSpace(cfgStr(cfg, "host", ""))
	if host == "" {
		host = n.Host
	}
	switch o.Network {
	case "ws", "httpupgrade":
		o.Path = firstNonEmptyStr(cfgStr(cfg, "ws_path", ""), cfgStr(cfg, "path", ""), "/")
		o.Host = host
	case "xhttp", "xhttp-h3":
		o.Path = firstNonEmptyStr(cfgStr(cfg, "path", ""), "/")
		o.Host = host
		o.Mode = strings.ToLower(strings.TrimSpace(cfgStr(cfg, "mode", "auto")))
		if reason := xhttpClientUnsupported(cfg); reason != "" {
			o.Unsupported = reason
		}
		if o.Network == "xhttp-h3" {
			o.Unsupported = "XHTTP over HTTP/3 没有经第三方客户端验证的写法（能力矩阵 external-reality-xhttp-h3-unverified）"
		}
	case "grpc":
		o.Host = host
		service, ok := grpcServiceName(cfg)
		if !ok {
			o.Unsupported = "自定义 grpc_path 不是 /<服务名>/Tun 形状，客户端只能按服务名拼路径"
		}
		o.ServiceName = service
	}
	return o
}

// grpcAuthorityMismatch 判断 sing-box / mihomo 这类设不了 :authority 的客户端
// 能否通过节点端的 gRPC Host 校验。它们的 :authority 是 TLS SNI（配了的话），
// 否则是服务器地址；节点端接受下发的 host（后台配的 Host，否则 server_host），
// REALITY 下另外接受 REALITY server name（pdnd vless.go 的 grpc 分支）。
// 对不上返回跳过原因。
func grpcAuthorityMismatch(n Node, o streamOpts) string {
	authority := n.Host
	if (o.TLS || o.Reality) && o.SNI != "" {
		authority = o.SNI
	}
	if strings.EqualFold(authority, o.Host) || (o.Reality && strings.EqualFold(authority, o.SNI)) {
		return ""
	}
	return "节点要求的 gRPC Host（" + o.Host + "）与客户端的 :authority（" + authority + "）不同，该客户端设不了 :authority"
}

// grpcServiceName 推出客户端要填的 gRPC 服务名。节点端的路径是 grpc_path，
// 没配时是 /<grpc_service_name>/Tun（默认 GunService）；客户端只能填服务名，
// 路径固定拼成 /<服务名>/Tun，所以自定义 grpc_path 只有在这个形状里才表达得出。
func grpcServiceName(cfg map[string]any) (string, bool) {
	if path := strings.TrimSpace(cfgStr(cfg, "grpc_path", "")); path != "" {
		if !strings.HasPrefix(path, "/") || !strings.HasSuffix(path, "/Tun") {
			return "", false
		}
		service := strings.TrimSuffix(strings.TrimPrefix(path, "/"), "/Tun")
		if service == "" {
			return "", false
		}
		return service, true
	}
	service := strings.Trim(strings.TrimSpace(cfgStr(cfg, "grpc_service_name", "")), "/")
	if service == "" {
		service = "GunService"
	}
	return service, true
}

// xhttpClientUnsupported 检查 XHTTP 是否用了客户端订阅无法表达的定制。
//
// 会话 / 序号 / 上行数据的放置位置和上行方法改了默认值，客户端必须同样改，
// 而这些参数在 mihomo 的 xhttp-opts 和分享链接里都没有通行写法。服务端那几个
// sc_* 上限是服务端自己的约束，客户端按默认值发不会越界，不算在内。
func xhttpClientUnsupported(cfg map[string]any) string {
	defaults := map[string]string{
		"session_placement":     "path",
		"seq_placement":         "path",
		"uplink_http_method":    "post",
		"uplink_data_placement": "body",
	}
	for key, def := range defaults {
		if v := strings.ToLower(strings.TrimSpace(cfgStr(cfg, key, ""))); v != "" && v != def {
			return fmt.Sprintf("XHTTP 的 %s=%s 不是默认值，客户端订阅表达不了", key, v)
		}
	}
	return ""
}

// pickForUser 在 values 里按 (用户, 节点, 用途) 的稳定哈希挑一个；空列表返回空串。
//
// 哈希只依赖用户凭据与节点地址，不依赖节点名（改名不该让全体用户换一套握手
// 参数）。凭据本来就在这份订阅里，哈希结果不泄露任何别的东西。
func pickForUser(values []string, uuid string, n Node, purpose string) string {
	var clean []string
	for _, v := range values {
		if v = strings.TrimSpace(v); v != "" {
			clean = append(clean, v)
		}
	}
	switch len(clean) {
	case 0:
		return ""
	case 1:
		return clean[0]
	}
	sum := sha256.Sum256([]byte(purpose + "\x00" + uuid + "\x00" + n.Host + "\x00" + strconv.Itoa(n.Port)))
	return clean[binary.BigEndian.Uint64(sum[:8])%uint64(len(clean))]
}

// tlsHints 是 TLS 类协议（hysteria2、tuic、anytls、naive、juicity）的客户端提示。
type tlsHints struct {
	SNI      string
	Insecure bool
}

// anyTLSFingerprint 是 AnyTLS 的 uTLS 指纹：后台配的 utls（内核名 fingerprint），
// 没配用 chrome。AnyTLS 是 TLS 里跑的协议，客户端握手的指纹同样是识别点。
func anyTLSFingerprint(cfg map[string]any) string {
	return firstNonEmptyStr(cfgStr(cfg, "fingerprint", ""), defaultFingerprint)
}

func parseTLSHints(cfg map[string]any) tlsHints {
	return tlsHints{
		SNI:      strings.TrimSpace(cfgStr(cfg, "server_name", "")),
		Insecure: cfgBool(cfg, "allow_insecure"),
	}
}

// hysteria2ObfsPassword 读 salamander 混淆口令：表单形状是 obfs 对象
// （{type, password}，内核也是这个形状），旧扁平数据是 obfs_password。
func hysteria2ObfsPassword(cfg map[string]any) string {
	if obfs, ok := cfg["obfs"].(map[string]any); ok {
		if typ, _ := obfs["type"].(string); typ == "" || strings.EqualFold(typ, "salamander") {
			if password, _ := obfs["password"].(string); password != "" {
				return password
			}
		}
	}
	return cfgStr(cfg, "obfs_password", "")
}

// shadowTLSHandshakeServer 是 ShadowTLS 借用握手的站点名，也就是客户端的 SNI。
// 与节点端同一取法：server 优先，其次 handshake_server；可能带端口，去掉。
func shadowTLSHandshakeServer(cfg map[string]any) string {
	server := strings.TrimSpace(firstNonEmptyStr(cfgStr(cfg, "server", ""), cfgStr(cfg, "handshake_server", "")))
	if host, _, err := net.SplitHostPort(server); err == nil {
		return host
	}
	return server
}

// anyTLSHasCertificate：节点端没有 cert_path 时以明文起 AnyTLS，而所有客户端
// 都强制 TLS。这种存量节点（后台现在已要求证书）在订阅里一律跳过。
func anyTLSHasCertificate(cfg map[string]any) bool {
	return strings.TrimSpace(cfgStr(cfg, "cert_path", "")) != ""
}

// mieruTransport 是 mieru 的传输层，客户端写大写 TCP / UDP，与服务端一致。
func mieruTransport(cfg map[string]any) string {
	if strings.EqualFold(strings.TrimSpace(cfgStr(cfg, "transport", "")), "udp") {
		return "UDP"
	}
	return "TCP"
}

// ssMethod 是 Shadowsocks 加密方式（内核名 method，表单名 cipher 已翻译过来）。
func ssMethod(cfg map[string]any) string {
	return cfgStr(cfg, "method", "aes-256-gcm")
}

// ssConflict 见 kernelShapedNodes：cipher 与 method 打架的节点不下发、也不渲染。
func ssConflict(cfg map[string]any) bool {
	_, conflict := cfg[conflictMarker]
	return conflict
}

func firstNonEmptyStr(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func cfgStr(m map[string]any, key, def string) string {
	if v, ok := m[key].(string); ok && v != "" {
		return v
	}
	return def
}

// cfgBool 读一个布尔配置。
// 兼容 JSON 里可能出现的三种写法：真布尔、数字 1/0、字符串 "true"。
// 面板下发的 protocol_config 是 jsonb，历史数据里三种都存在过。
func cfgBool(m map[string]any, key string) bool {
	switch v := m[key].(type) {
	case bool:
		return v
	case float64:
		return v > 0
	case int:
		return v > 0
	case string:
		return v == "true" || v == "1"
	}
	return false
}

// cfgStrings 读一个字符串数组配置。
//
// protocol_config 是 jsonb，反序列化后数组元素是 any，
// 所以不能直接断言成 []string。顺手兼容单个字符串的写法 ——
// server_names 只有一项时，手写配置的人很容易漏掉方括号。
func cfgStrings(m map[string]any, key string) []string {
	switch v := m[key].(type) {
	case []any:
		out := make([]string, 0, len(v))
		for _, e := range v {
			if s, ok := e.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	case []string:
		return v
	case string:
		if v == "" {
			return nil
		}
		return []string{v}
	}
	return nil
}

// hasUnshareableMask 判断这个节点是否配了无法通过分享链接传达的掩码。
//
// mKCP 的掩码（finalmask）是 xray 新加的功能，而 vless:// 这类分享链接的
// 参数是早就定死的那一套，没有地方放掩码类型和密码——客户端从链接里
// 读不到这些，只会用裸 mKCP 去连一个开着加密的服务端。
//
// 结果不是「降级」而是「完全连不上」，且客户端不会报出有用的错：包发出去
// 服务端解不开直接丢弃，用户看到的就是节点转圈。所以这种节点必须整个从
// 订阅里去掉，而不是给一条注定失败的链接。
//
// 要用掩码的话，目前只能让用户手动往客户端配置文件里写 finalmask。等
// 分享链接规范补上对应参数，这里再放开。
func hasUnshareableMask(config map[string]any) bool {
	if !isMKCPNetwork(cfgStr(config, "network", "")) {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(cfgStr(config, "mask", ""))) {
	case "", "none", "mkcp-original":
		return false
	}
	return true
}

// isMKCPNetwork 判断传输是不是 mKCP。面板里存 "mkcp"，从客户端配置抄
// 过来的写法是 "kcp"，两种都得认。
func isMKCPNetwork(network string) bool {
	switch strings.ToLower(strings.TrimSpace(network)) {
	case "mkcp", "kcp", "m-kcp":
		return true
	}
	return false
}
