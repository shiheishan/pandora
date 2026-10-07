package subscription

// URI 列表（v2rayN / v2rayNG / NekoBox / Shadowrocket 等）。
//
// 参数名按 v2rayN 的分享链接写法（Xray 的 VLESS 分享链接提案）为准：传输是
// type，WS / HTTPUpgrade / XHTTP 用 path 与 host，gRPC 用 serviceName，REALITY
// 用 pbk / sid / sni / fp，跳过证书校验是 allowInsecure（hysteria2 / anytls 用
// insecure，tuic / juicity 用 allow_insecure，各按其客户端的写法）。

import (
	"encoding/base64"
	"encoding/json"
	"net"
	"net/url"
	"strconv"
	"strings"
)

func renderURI(nodes []Node, uuid string) ([]byte, string, int) {
	var lines []string
	for _, n := range nodes {
		if u, _ := nodeToURI(n, uuid); u != "" {
			lines = append(lines, u)
		}
	}
	body := strings.Join(lines, "\n")
	// 整体 base64：这是这类客户端的既定约定，不是为了隐藏什么
	enc := base64.StdEncoding.EncodeToString([]byte(body))
	return []byte(enc), "text/plain; charset=utf-8", len(lines)
}

// nodeToURI 返回分享链接；不能渲染时返回空串和跳过原因。
func nodeToURI(n Node, uuid string) (string, string) {
	// 掩码参数在分享链接里无处安放，给出去的链接一定连不上。
	if hasUnshareableMask(n.Config) {
		return "", "mKCP 掩码在分享链接里没有参数可放"
	}
	addr := net.JoinHostPort(n.Host, strconv.Itoa(n.Port))
	frag := "#" + url.PathEscape(n.Name)
	q := url.Values{}
	userPass := url.UserPassword(uuid, uuid).String()

	switch n.Type {
	case "vless":
		o := parseStream(n, uuid)
		if o.Unsupported != "" {
			return "", o.Unsupported
		}
		q.Set("encryption", "none")
		setURIStream(q, o)
		if o.Flow != "" {
			q.Set("flow", o.Flow)
		}
		return "vless://" + uuid + "@" + addr + "?" + q.Encode() + frag, ""

	case "vmess":
		o := parseStream(n, uuid)
		if o.Unsupported != "" {
			return "", o.Unsupported
		}
		// VMess 用的是自成一体的 base64(JSON)，不是标准 URI 查询串（v2rayN 格式）
		m := map[string]any{
			"v": "2", "ps": n.Name, "add": n.Host, "port": strconv.Itoa(n.Port),
			"id": uuid, "aid": "0", "scy": "auto",
			"net": shareNetworkName(o.Network), "type": "none",
			"host": o.Host, "path": o.Path,
		}
		switch o.Network {
		case "grpc":
			// v2rayN 的 vmess 链接里 gRPC 服务名放在 path，type 是 gRPC 模式
			m["path"] = o.ServiceName
			m["type"] = "gun"
		case "xhttp":
			m["type"] = o.Mode
		}
		if o.TLS {
			m["tls"] = "tls"
			m["sni"] = o.SNI
			if o.Fingerprint != "" {
				m["fp"] = o.Fingerprint
			}
		}
		body, err := json.Marshal(m)
		if err != nil {
			return "", "VMess 链接序列化失败"
		}
		return "vmess://" + base64.StdEncoding.EncodeToString(body), ""

	case "trojan":
		o := parseStream(n, uuid)
		if o.Unsupported != "" {
			return "", o.Unsupported
		}
		setURIStream(q, o)
		if o.Flow != "" {
			q.Set("flow", o.Flow)
		}
		return "trojan://" + url.QueryEscape(uuid) + "@" + addr + "?" + q.Encode() + frag, ""

	case "shadowsocks":
		if ssConflict(n.Config) {
			return "", "Shadowsocks 的 cipher 与 method 冲突，节点端拒绝下发"
		}
		// SIP002：base64url(method:password) 放在 userinfo 位
		userinfo := base64.RawURLEncoding.EncodeToString([]byte(ssMethod(n.Config) + ":" + uuid))
		return "ss://" + userinfo + "@" + addr + frag, ""

	case "hysteria2":
		h := parseTLSHints(n.Config)
		if h.SNI != "" {
			q.Set("sni", h.SNI)
		}
		if h.Insecure {
			q.Set("insecure", "1")
		}
		if o := hysteria2ObfsPassword(n.Config); o != "" {
			q.Set("obfs", "salamander")
			q.Set("obfs-password", o)
		}
		return "hysteria2://" + url.QueryEscape(uuid) + "@" + addr + "?" + q.Encode() + frag, ""

	case "tuic":
		h := parseTLSHints(n.Config)
		if h.SNI != "" {
			q.Set("sni", h.SNI)
		}
		if h.Insecure {
			q.Set("allow_insecure", "1")
		}
		q.Set("congestion_control", cfgStr(n.Config, "congestion_control", "bbr"))
		q.Set("alpn", "h3")
		// TUIC v5 的凭据是 uuid:password，我们两者同值
		return "tuic://" + userPass + "@" + addr + "?" + q.Encode() + frag, ""

	case "anytls":
		if !anyTLSHasCertificate(n.Config) {
			return "", "AnyTLS 节点没有证书，节点端是明文而客户端强制 TLS"
		}
		h := parseTLSHints(n.Config)
		if h.SNI != "" {
			q.Set("sni", h.SNI)
		}
		if h.Insecure {
			q.Set("insecure", "1")
		}
		// fp 与 vless / trojan 分享链接同名；不认它的客户端会忽略这个参数
		q.Set("fp", anyTLSFingerprint(n.Config))
		return "anytls://" + url.QueryEscape(uuid) + "@" + addr + "?" + q.Encode() + frag, ""

	case "naive":
		// NekoBox / v2rayN 的写法：naive+https://用户:密码@主机:端口
		h := parseTLSHints(n.Config)
		if h.Insecure {
			return "", "Naive 客户端（Chromium 网络栈）不支持跳过证书校验"
		}
		if h.SNI != "" {
			q.Set("sni", h.SNI)
		}
		return "naive+https://" + userPass + "@" + addr + encodeQuery(q) + frag, ""

	case "juicity":
		h := parseTLSHints(n.Config)
		if h.SNI != "" {
			q.Set("sni", h.SNI)
		}
		if h.Insecure {
			q.Set("allow_insecure", "1")
		}
		q.Set("congestion_control", cfgStr(n.Config, "congestion_control", "bbr"))
		return "juicity://" + userPass + "@" + addr + "?" + q.Encode() + frag, ""

	case "socks":
		if cfgBool(n.Config, "tls") {
			return "", "SOCKS over TLS 没有通行的分享链接写法"
		}
		return "socks5://" + userPass + "@" + addr + frag, ""

	case "http":
		scheme := "http://"
		if cfgBool(n.Config, "tls") {
			scheme = "https://"
		}
		return scheme + userPass + "@" + addr + frag, ""

	default:
		// mieru / shadowtls 没有被广泛接受的 URI 写法。编一个出来只会让客户端
		// 解析失败，不如不发。
		return "", n.Type + " 没有通行的分享链接写法"
	}
}

// setURIStream 写 vless / trojan 共用的传输与安全层参数。
func setURIStream(q url.Values, o streamOpts) {
	q.Set("type", shareNetworkName(o.Network))
	switch {
	case o.Reality:
		// REALITY 的客户端参数：公钥和 short id 缺一不可，
		// 少了任何一个客户端都握不上手，而且报错通常只是「超时」。
		q.Set("security", "reality")
		q.Set("pbk", o.PublicKey)
		if o.SNI != "" {
			q.Set("sni", o.SNI)
		}
		if o.ShortID != "" {
			q.Set("sid", o.ShortID)
		}
		q.Set("fp", o.Fingerprint)
	case o.TLS:
		q.Set("security", "tls")
		if o.SNI != "" {
			q.Set("sni", o.SNI)
		}
		if o.Fingerprint != "" {
			q.Set("fp", o.Fingerprint)
		}
		if o.Insecure {
			q.Set("allowInsecure", "1")
		}
	default:
		q.Set("security", "none")
	}
	switch o.Network {
	case "ws", "httpupgrade":
		q.Set("path", o.Path)
		q.Set("host", o.Host)
	case "xhttp":
		q.Set("path", o.Path)
		q.Set("host", o.Host)
		q.Set("mode", o.Mode)
	case "grpc":
		q.Set("serviceName", o.ServiceName)
		q.Set("mode", "gun")
		if o.Host != "" {
			// Xray 的 gRPC 客户端用 authority 设 :authority；节点端按 Host 校验它
			q.Set("authority", o.Host)
		}
	}
}

// shareNetworkName 是分享链接里该写的传输名。
//
// 面板内部统一用 "mkcp"，但 v2rayN 那一系的客户端只认 "kcp"——写 mkcp
// 它会当成未知传输，回退到 tcp，然后连不上。
func shareNetworkName(network string) string {
	if isMKCPNetwork(network) {
		return "kcp"
	}
	return network
}

func encodeQuery(q url.Values) string {
	if len(q) == 0 {
		return ""
	}
	return "?" + q.Encode()
}
