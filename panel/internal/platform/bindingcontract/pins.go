package bindingcontract

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
)

const (
	// PanelKeyPrefix 是 --panel-key 的前缀；其后是面板配置签名公钥（32 字节原始 Ed25519）
	// 完整 sha256 的 64 位小写十六进制。刻意与 TLS 钉住值用不同写法，两者不会被填反。
	PanelKeyPrefix = "sha256:"
	// TLSPinPrefix 是 --pin 与绑定文件 tls_pin 的前缀，与 curl --pinnedpubkey 的写法一致：
	// 其后是网关证书 SubjectPublicKeyInfo DER 的 sha256 的标准 base64（带填充）。
	TLSPinPrefix = "sha256//"
)

// PanelKeyFingerprint 返回面板配置签名公钥的 --panel-key 写法。
func PanelKeyFingerprint(publicKey []byte) (string, error) {
	if len(publicKey) != 32 {
		return "", errors.New("panel config public key must be 32 bytes")
	}
	sum := sha256.Sum256(publicKey)
	return PanelKeyPrefix + hex.EncodeToString(sum[:]), nil
}

// ParsePanelKey 严格解析 --panel-key，返回 32 字节摘要。
func ParsePanelKey(pin string) ([]byte, error) {
	rest, ok := strings.CutPrefix(pin, PanelKeyPrefix)
	if !ok || checkSHA256Hex("panel_key", rest) != nil {
		return nil, errors.New("panel key must be sha256: followed by 64 lowercase hex characters")
	}
	raw, _ := hex.DecodeString(rest)
	return raw, nil
}

// CheckPanelKey 核对面板在接入响应里给出的配置公钥与安装命令里的 --panel-key 是否一致。
// 不一致必须中止接入、不写任何绑定文件。
func CheckPanelKey(pin string, publicKey []byte) error {
	want, err := ParsePanelKey(pin)
	if err != nil {
		return err
	}
	if len(publicKey) != 32 {
		return errors.New("panel config public key must be 32 bytes")
	}
	got := sha256.Sum256(publicKey)
	if subtle.ConstantTimeCompare(want, got[:]) != 1 {
		return errors.New("panel config public key does not match the pinned panel key")
	}
	return nil
}

// TLSPin 返回证书 SubjectPublicKeyInfo DER（x509.Certificate.RawSubjectPublicKeyInfo）的钉住值。
func TLSPin(spkiDER []byte) string {
	sum := sha256.Sum256(spkiDER)
	return TLSPinPrefix + base64.StdEncoding.EncodeToString(sum[:])
}

// ParseTLSPin 严格解析钉住值，返回 32 字节摘要。
func ParseTLSPin(pin string) ([]byte, error) {
	rest, ok := strings.CutPrefix(pin, TLSPinPrefix)
	if !ok || checkSHA256B64("tls_pin", rest) != nil {
		return nil, errors.New("tls pin must be sha256// followed by canonical base64 of 32 bytes")
	}
	raw, _ := base64.StdEncoding.DecodeString(rest)
	return raw, nil
}

// CheckTLSPin 核对对端叶子证书的 SPKI 是否命中任一钉住值（当前值，或清单预告的下一个值）。
func CheckTLSPin(spkiDER []byte, pins ...string) error {
	got := sha256.Sum256(spkiDER)
	matched := false
	for _, pin := range pins {
		if pin == "" {
			continue
		}
		want, err := ParseTLSPin(pin)
		if err != nil {
			return err
		}
		if subtle.ConstantTimeCompare(want, got[:]) == 1 {
			matched = true
		}
	}
	if !matched {
		return errors.New("panel gateway certificate does not match the pinned public key")
	}
	return nil
}

// CanonicalPanelOrigin 把 --panel 规范成 scheme://host:port：
//   - 只允许 https；http 只允许回环地址（localhost、127.0.0.0/8、::1），供开发与验收；
//   - 不允许用户信息、查询串、片段，路径只能为空或 /；
//   - IP 用 netip 的规范写法（IPv4 映射的 IPv6 还原成 IPv4，IPv6 加方括号），不允许 zone；
//   - 域名转小写，不允许末尾的点；
//   - 端口一律写出，缺省按 scheme 取 443 或 80。
func CanonicalPanelOrigin(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Opaque != "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" ||
		(u.Path != "" && u.Path != "/") {
		return "", errors.New("panel URL must be scheme://host[:port] without path, query or credentials")
	}
	host, portText := u.Hostname(), u.Port()
	if host == "" {
		return "", errors.New("panel URL needs a host")
	}
	loopback := false
	if addr, err := netip.ParseAddr(host); err == nil {
		if addr.Zone() != "" {
			return "", errors.New("panel URL must not carry an IPv6 zone")
		}
		addr = addr.Unmap()
		loopback = addr.IsLoopback()
		host = addr.String()
		if addr.Is6() {
			host = "[" + host + "]"
		}
	} else {
		host = strings.ToLower(host)
		if !isDNSName(host) {
			return "", errors.New("panel URL host is neither an IP address nor a DNS name")
		}
		loopback = host == "localhost"
	}
	var port int
	switch u.Scheme {
	case "https":
		port = 443
	case "http":
		if !loopback {
			return "", errors.New("panel URL must use https (http is allowed only on loopback)")
		}
		port = 80
	default:
		return "", errors.New("panel URL scheme must be https")
	}
	if portText != "" {
		port, err = strconv.Atoi(portText)
		if err != nil || port < 1 || port > 65535 {
			return "", errors.New("panel URL port must be within [1, 65535]")
		}
	}
	return u.Scheme + "://" + host + ":" + strconv.Itoa(port), nil
}

func isDNSName(host string) bool {
	if len(host) > 253 {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for i := 0; i < len(label); i++ {
			c := label[i]
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}

// PanelTenant 是判定「同一面板同一租户」用的三元组。Origin 必须是 CanonicalPanelOrigin 的输出，
// PanelKey 必须是 PanelKeyFingerprint 的输出。
type PanelTenant struct {
	Origin   string
	PanelKey string
	TenantID string
}

// SamePanelTenant 判定两个绑定是否属于同一面板的同一租户：租户相同，并且规范来源相同
// 或面板配置公钥指纹相同（同一面板换了访问地址也算同一个）。命中即是同一面板，
// 一台机器只能有一个这样的绑定。
func SamePanelTenant(a, b PanelTenant) bool {
	if a.TenantID == "" || a.TenantID != b.TenantID {
		return false
	}
	return a.Origin != "" && a.Origin == b.Origin || a.PanelKey != "" && a.PanelKey == b.PanelKey
}
