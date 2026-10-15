package protocol

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"strings"
)

// ECHPublicNameMaxLen 是 ECH 公开名（public_name）允许的最大字节长度。
const ECHPublicNameMaxLen = 253

const echConfigVersion = 0xfe0d
const echKEMX25519 = 0x0020

// ECHKey 是一把新生成的 ECH 密钥。
type ECHKey struct {
	ConfigList []byte // ECHConfigList：2 字节总长 + 唯一一个 ECHConfig（公开，进订阅与后台展示）
	Config     []byte // 那个 ECHConfig 本身（2 字节版本 0xfe0d + 2 字节内容长 + 内容），给节点端 tls.EncryptedClientHelloKey.Config
	PrivateKey []byte // X25519 私钥 32 字节（tls.EncryptedClientHelloKey.PrivateKey）
}

// GenerateECHKey 生成 ECH 密钥对及 ECHConfigList。
func GenerateECHKey(publicName string) (ECHKey, error) {
	norm, ok := NormalizeECHPublicName(publicName)
	if !ok {
		return ECHKey{}, errors.New("ECH 公开名必须是有效的域名")
	}

	priv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return ECHKey{}, err
	}
	pub := priv.PublicKey().Bytes()
	if len(pub) != 32 {
		return ECHKey{}, fmt.Errorf("X25519 公钥长度异常: %d", len(pub))
	}

	var configID [1]byte
	if _, err := rand.Read(configID[:]); err != nil {
		return ECHKey{}, err
	}

	nameBytes := []byte(norm)
	contentsLen := 1 + 2 + 2 + 32 + 2 + 8 + 1 + 1 + len(nameBytes) + 2
	contents := make([]byte, contentsLen)
	off := 0
	contents[off] = configID[0]
	off++
	binary.BigEndian.PutUint16(contents[off:], echKEMX25519)
	off += 2
	binary.BigEndian.PutUint16(contents[off:], 32)
	off += 2
	copy(contents[off:], pub)
	off += 32
	binary.BigEndian.PutUint16(contents[off:], 8)
	off += 2
	binary.BigEndian.PutUint16(contents[off:], 0x0001)
	off += 2
	binary.BigEndian.PutUint16(contents[off:], 0x0001)
	off += 2
	binary.BigEndian.PutUint16(contents[off:], 0x0001)
	off += 2
	binary.BigEndian.PutUint16(contents[off:], 0x0003)
	off += 2
	contents[off] = 0 // max_name_length
	off++
	contents[off] = byte(len(nameBytes))
	off++
	copy(contents[off:], nameBytes)
	off += len(nameBytes)
	binary.BigEndian.PutUint16(contents[off:], 0) // extensions

	echConfig := make([]byte, 4+len(contents))
	binary.BigEndian.PutUint16(echConfig[0:], echConfigVersion)
	binary.BigEndian.PutUint16(echConfig[2:], uint16(len(contents))) //nolint:gosec // 公开名是规整过的域名（不超过 253 字节），内容远小于 65535
	copy(echConfig[4:], contents)

	configList := make([]byte, 2+len(echConfig))
	binary.BigEndian.PutUint16(configList[0:], uint16(len(echConfig))) //nolint:gosec // 同上
	copy(configList[2:], echConfig)

	return ECHKey{
		ConfigList: configList,
		Config:     echConfig,
		PrivateKey: priv.Bytes(),
	}, nil
}

// NormalizeECHPublicName 规范化并校验 ECH 公开名。
func NormalizeECHPublicName(name string) (string, bool) {
	name = strings.ToLower(strings.TrimSpace(name))
	if len(name) == 0 || len(name) > ECHPublicNameMaxLen {
		return "", false
	}
	if strings.Contains(name, "*") {
		return "", false
	}
	if net.ParseIP(name) != nil {
		return "", false
	}
	// 客户端（Go crypto/tls 的 validDNSName 等）跳过只有一段的公开名（如 localhost），整份配置就无效了
	if !strings.Contains(name, ".") {
		return "", false
	}
	if !validServerName(name) {
		return "", false
	}
	return name, true
}

// ECHConfigFromList 从 ECHConfigList 解析出唯一的 ECHConfig 副本。
func ECHConfigFromList(list []byte) ([]byte, error) {
	if len(list) < 2 {
		return nil, errors.New("ECHConfigList 过短")
	}
	total := int(binary.BigEndian.Uint16(list[0:2]))
	if total != len(list)-2 {
		return nil, errors.New("ECHConfigList 总长度前缀与内容不符")
	}
	rest := list[2:]
	if len(rest) < 4 {
		return nil, errors.New("ECHConfig 过短")
	}
	if len(rest) != total {
		return nil, errors.New("ECHConfigList 应只含一个 ECHConfig")
	}
	ver := binary.BigEndian.Uint16(rest[0:2])
	if ver != echConfigVersion {
		return nil, fmt.Errorf("ECHConfig 版本应为 0x%04x", echConfigVersion)
	}
	contentLen := int(binary.BigEndian.Uint16(rest[2:4]))
	if 4+contentLen != len(rest) {
		return nil, errors.New("ECHConfig 内容长度与实际不符")
	}
	out := make([]byte, len(rest))
	copy(out, rest)
	return out, nil
}

// ECHPublicNameFromList 从 ECHConfigList 解析 public_name。
func ECHPublicNameFromList(list []byte) (string, error) {
	cfg, err := ECHConfigFromList(list)
	if err != nil {
		return "", err
	}
	contents := cfg[4:]
	if len(contents) < 1+2+2+2+2+8+1+1 {
		return "", errors.New("ECHConfig 内容过短")
	}
	off := 0
	off++    // config_id
	off += 2 // kem_id
	pkLen := int(binary.BigEndian.Uint16(contents[off : off+2]))
	off += 2
	if off+pkLen > len(contents) {
		return "", errors.New("ECHConfig 公钥长度无效")
	}
	off += pkLen
	if off+2 > len(contents) {
		return "", errors.New("ECHConfig cipher_suites 长度缺失")
	}
	csLen := int(binary.BigEndian.Uint16(contents[off : off+2]))
	off += 2
	if off+csLen > len(contents) {
		return "", errors.New("ECHConfig cipher_suites 内容无效")
	}
	off += csLen
	if off+1 > len(contents) {
		return "", errors.New("ECHConfig max_name_length 缺失")
	}
	off++ // max_name_length
	if off+1 > len(contents) {
		return "", errors.New("ECHConfig public_name 长度缺失")
	}
	nameLen := int(contents[off])
	off++
	if off+nameLen > len(contents) {
		return "", errors.New("ECHConfig public_name 内容无效")
	}
	name := string(contents[off : off+nameLen])
	off += nameLen
	if off+2 > len(contents) {
		return "", errors.New("ECHConfig extensions 长度缺失")
	}
	extLen := int(binary.BigEndian.Uint16(contents[off : off+2]))
	if off+2+extLen != len(contents) {
		return "", errors.New("ECHConfig extensions 长度与实际不符")
	}
	return name, nil
}

// ECHConfigPEM 将 ECHConfigList 编码为与 Xboard 相同的 PEM 块。
func ECHConfigPEM(list []byte) string {
	encoded := base64.StdEncoding.EncodeToString(list)
	var b strings.Builder
	b.WriteString("-----BEGIN ECH CONFIGS-----\n")
	for i := 0; i < len(encoded); i += 64 {
		end := i + 64
		if end > len(encoded) {
			end = len(encoded)
		}
		b.WriteString(encoded[i:end])
		b.WriteByte('\n')
	}
	b.WriteString("-----END ECH CONFIGS-----")
	return b.String()
}

// ECHCapable 判断翻译后的内核配置是否能在节点端用 Go 标准库 TLS（TCP）启用 ECH。
// REALITY 自带伪装、不适用；QUIC 类节点端用的 quic-go 握手没接 ECH，不开放。
func ECHCapable(nodeType string, kernel map[string]any) bool {
	return ECHUnsupportedReason(nodeType, kernel) == ""
}

// ECHUnsupportedReason 在不能开 ECH 时返回给后台看的中文原因；能开时返回空串。
func ECHUnsupportedReason(nodeType string, kernel map[string]any) string {
	canonical := CanonicalType(nodeType)
	switch canonical {
	case "vless", "vmess", "trojan":
		sec, _ := kernel["security"].(string)
		if strings.EqualFold(strings.TrimSpace(sec), "reality") {
			return "REALITY 节点不支持 ECH（REALITY 自带伪装）"
		}
		netRaw, _ := kernel["network"].(string)
		net := strings.ToLower(strings.TrimSpace(netRaw))
		if net == "" {
			net = "tcp"
		}
		switch net {
		case "xhttp-h3", "mkcp":
			return "QUIC 类协议暂不支持 ECH"
		}
		if !kernelBoolTrue(kernel["tls"]) {
			return "只有开启 TLS 的节点能用 ECH"
		}
		return ""
	case "anytls":
		cert, _ := kernel["cert_path"].(string)
		if strings.TrimSpace(cert) == "" {
			return "只有开启 TLS 的节点能用 ECH"
		}
		return ""
	case "naive":
		if !kernelBoolTrue(kernel["tls"]) {
			return "只有开启 TLS 的节点能用 ECH"
		}
		return ""
	case "hysteria2", "tuic", "juicity":
		return "QUIC 类协议暂不支持 ECH"
	case "shadowsocks", "socks", "http", "mieru", "shadowtls":
		return "该协议不支持 ECH"
	default:
		if canonical == "" {
			return "该协议不支持 ECH"
		}
		// vless/vmess/trojan 以外若带 reality 安全层
		sec, _ := kernel["security"].(string)
		if strings.EqualFold(strings.TrimSpace(sec), "reality") {
			return "REALITY 节点不支持 ECH（REALITY 自带伪装）"
		}
		return "该协议不支持 ECH"
	}
}

func kernelBoolTrue(v any) bool {
	b, ok := v.(bool)
	return ok && b
}
