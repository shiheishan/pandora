package xray

// 协议配置的构造。
//
// xray 的配置是 protobuf 而不是 JSON，好处是类型安全、字段名不会拼错，
// 坏处是每个协议都要手写一段翻译。这里的取舍与 sing-box 那边一致：
// 面板只下发协议无关的字段（端口、TLS、用户 UUID），
// 协议细节在这一层解释，新增协议不必改上层任何代码。

import (
	"fmt"
	"os"
	"encoding/base64"
	"encoding/hex"
	"strings"

	xcore "github.com/xtls/xray-core/core"

	"github.com/xtls/xray-core/common/protocol"
	"github.com/xtls/xray-core/common/serial"
	"github.com/xtls/xray-core/proxy/trojan"
	"github.com/xtls/xray-core/proxy/vless"
	vlessinbound "github.com/xtls/xray-core/proxy/vless/inbound"
	"github.com/xtls/xray-core/proxy/vmess"
	vmessinbound "github.com/xtls/xray-core/proxy/vmess/inbound"
	"github.com/xtls/xray-core/transport/internet"
	"github.com/xtls/xray-core/transport/internet/reality"
	xtls "github.com/xtls/xray-core/transport/internet/tls"

	"github.com/aegispanel/nodeagent/core"
)

// buildInbound 把面板下发的配置翻译成 xray 的入站描述。
func buildInbound(cfg *core.InboundConfig) (*xcore.InboundHandlerConfig, error) {
	stream, err := buildStream(cfg)
	if err != nil {
		return nil, err
	}

	var proxySettings *serial.TypedMessage
	switch lower(cfg.Protocol) {
	case "vless":
		// Decryption 必须显式写 none —— 留空会被 xray 判为配置错误。
		// VLESS 本身不加密，安全性由 TLS/REALITY 提供。
		proxySettings = serial.ToTypedMessage(&vlessinbound.Config{Decryption: "none"})
	case "vmess":
		proxySettings = serial.ToTypedMessage(&vmessinbound.Config{})
	case "trojan":
		proxySettings = serial.ToTypedMessage(&trojan.ServerConfig{})
	default:
		return nil, fmt.Errorf("xray 内核暂不支持协议 %q", cfg.Protocol)
	}

	return &xcore.InboundHandlerConfig{
		Tag:              cfg.Tag,
		ReceiverSettings: serial.ToTypedMessage(receiverConfig(cfg, stream)),
		ProxySettings:    proxySettings,
	}, nil
}

// buildAccount 构造单个用户的凭据。
//
// 与 sing-box 那边同样的约定：面板只给一个 UUID，
// 在 VLESS/VMess 里它是用户 ID，在 Trojan 里是密码。
func buildAccount(cfg *core.InboundConfig, u core.User) (protocol.Account, error) {
	switch lower(cfg.Protocol) {
	case "vless":
		flow, _ := cfg.Raw["flow"].(string)
		return (&vless.Account{Id: u.UUID, Flow: flow}).AsAccount()
	case "vmess":
		return (&vmess.Account{Id: u.UUID}).AsAccount()
	case "trojan":
		return (&trojan.Account{Password: u.UUID}).AsAccount()
	default:
		return nil, fmt.Errorf("xray 内核暂不支持协议 %q", cfg.Protocol)
	}
}

// buildStream 组装传输层与 TLS。
func buildStream(cfg *core.InboundConfig) (*internet.StreamConfig, error) {
	stream := &internet.StreamConfig{}

	if network, ok := cfg.Raw["network"].(string); ok && network != "" && network != "tcp" {
		// 传输层名字与 sing-box 的 v2ray transport 对齐，
		// 面板那边一套配置能同时喂给两个内核
		stream.ProtocolName = lower(network)
	}

	// REALITY 先判：它和普通 TLS 是互斥的两条路，而且不需要证书文件 ——
	// 这正是选它的原因。面板那边的证书生命周期还没做完，普通 TLS 落不了地，
	// 而 REALITY 借用真实站点的握手，服务端只要一对 x25519 密钥。
	if lower(str(cfg.Raw["security"])) == "reality" {
		return buildRealityStream(cfg, stream)
	}

	enabled := false
	switch v := cfg.Raw["tls"].(type) {
	case bool:
		enabled = v
	case float64:
		enabled = v > 0
	}
	if !enabled {
		return stream, nil
	}

	certPath, _ := cfg.Raw["cert_path"].(string)
	keyPath, _ := cfg.Raw["key_path"].(string)
	if certPath == "" || keyPath == "" {
		return nil, fmt.Errorf("启用 TLS 需要 cert_path 与 key_path")
	}
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return nil, fmt.Errorf("读取证书: %w", err)
	}
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, fmt.Errorf("读取私钥: %w", err)
	}

	tlsCfg := &xtls.Config{
		Certificate: []*xtls.Certificate{{
			Certificate: certPEM,
			Key:         keyPEM,
			Usage:       xtls.Certificate_ENCIPHERMENT,
		}},
	}
	if sni, ok := cfg.Raw["server_name"].(string); ok && sni != "" {
		tlsCfg.ServerName = sni
	}

	stream.SecurityType = serial.GetMessageType(&xtls.Config{})
	stream.SecuritySettings = []*serial.TypedMessage{serial.ToTypedMessage(tlsCfg)}
	return stream, nil
}

// buildRealityStream 按面板下发的配置装配 REALITY 入站。
//
// REALITY 的工作方式：客户端发起 TLS 握手时用的是 dest 那个真实站点的
// 证书，服务端拿私钥验证客户端的 auth key —— 对上就转给代理，对不上就
// 把连接原样转发给 dest。也就是说主动探测者看到的是一个货真价实的
// 目标站点，包括它真实的证书链。所以：
//
//   - dest 必须是一个真的、在线的、支持 TLS 1.3 + H2 的站点，
//     而且最好和本机在同一地区 —— 探测者会比较延迟
//   - server_names 必须和 dest 的证书对得上，否则握手一开始就穿帮
//
// 这几条不满足的话 REALITY 不会报错，只会安静地变成一个容易识别的节点，
// 所以下面对它们做硬校验，宁可起不来也不要假装安全。
func buildRealityStream(cfg *core.InboundConfig, stream *internet.StreamConfig) (*internet.StreamConfig, error) {
	dest := str(cfg.Raw["dest"])
	if dest == "" {
		return nil, fmt.Errorf("reality 需要 dest（借用握手的真实站点，如 www.microsoft.com:443）")
	}
	if !strings.Contains(dest, ":") {
		// 不默默补 :443：面板那边填错了应当当场知道，
		// 而不是等到用户连不上再来查
		return nil, fmt.Errorf("reality 的 dest 要带端口，如 %s:443", dest)
	}

	names := strSlice(cfg.Raw["server_names"])
	if len(names) == 0 {
		return nil, fmt.Errorf("reality 需要 server_names（要和 dest 的证书对得上）")
	}

	priv, err := decodeRealityKey(str(cfg.Raw["private_key"]))
	if err != nil {
		return nil, fmt.Errorf("reality private_key: %w", err)
	}

	shortIDs, err := decodeShortIDs(strSlice(cfg.Raw["short_ids"]))
	if err != nil {
		return nil, err
	}

	rc := &reality.Config{
		Dest:        dest,
		Type:        "tcp",
		ServerNames: names,
		PrivateKey:  priv,
		ShortIds:    shortIDs,
		// Show 会把握手细节打进日志，含客户端标识。生产恒关。
		Show: false,
	}
	if v, ok := cfg.Raw["max_time_diff"].(float64); ok && v > 0 {
		rc.MaxTimeDiff = uint64(v)
	}

	stream.SecurityType = serial.GetMessageType(&reality.Config{})
	stream.SecuritySettings = []*serial.TypedMessage{serial.ToTypedMessage(rc)}
	return stream, nil
}

// decodeRealityKey 解析 x25519 私钥。
//
// 只收 base64url 无填充（x25519 工具和各家面板的通行格式），
// 长度必须正好 32 字节 —— 短了长了都不是合法的曲线标量。
func decodeRealityKey(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, fmt.Errorf("不能为空")
	}
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		// 有些工具输出带填充的标准 base64，顺手兼容一下
		if b2, err2 := base64.StdEncoding.DecodeString(s); err2 == nil {
			b = b2
		} else {
			return nil, fmt.Errorf("不是合法的 base64url")
		}
	}
	if len(b) != 32 {
		return nil, fmt.Errorf("长度应为 32 字节，实际 %d", len(b))
	}
	return b, nil
}

// decodeShortIDs 解析 short id 列表。
//
// 每一项必须凑满 8 字节。xray 内部是 `*(*[8]byte)(shortId)` 这样直接
// 转数组的，短一个字节就是一次 panic —— 而 panic 会把整个 agent 带走，
// 连同这台机器上其它正常的节点。用户配的是「不超过 16 位十六进制」，
// 右侧补零到 8 字节由我们负责，xray 的客户端侧也是这么补的，
// 所以两边算出来是同一个值。
func decodeShortIDs(list []string) ([][]byte, error) {
	pad := func(b []byte) []byte {
		full := make([]byte, 8)
		copy(full, b)
		return full
	}
	if len(list) == 0 {
		// 一个都不给时补一个全零 id，等价于不校验 short id。
		// 这是 xray 的默认行为，显式写出来省得以后有人以为是漏了。
		return [][]byte{pad(nil)}, nil
	}
	out := make([][]byte, 0, len(list))
	for _, s := range list {
		s = strings.TrimSpace(s)
		if s == "" {
			out = append(out, pad(nil))
			continue
		}
		if len(s)%2 != 0 || len(s) > 16 {
			return nil, fmt.Errorf("short_id %q 应为不超过 16 位的十六进制", s)
		}
		b, err := hex.DecodeString(s)
		if err != nil {
			return nil, fmt.Errorf("short_id %q 不是十六进制", s)
		}
		out = append(out, pad(b))
	}
	return out, nil
}

func str(v any) string {
	s, _ := v.(string)
	return strings.TrimSpace(s)
}

func strSlice(v any) []string {
	switch x := v.(type) {
	case []string:
		return x
	case []any:
		out := make([]string, 0, len(x))
		for _, e := range x {
			if s, ok := e.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, strings.TrimSpace(s))
			}
		}
		return out
	case string:
		if strings.TrimSpace(x) == "" {
			return nil
		}
		return []string{strings.TrimSpace(x)}
	}
	return nil
}
