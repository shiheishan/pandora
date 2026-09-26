// [INPUT]: 依赖 golang.org/x/crypto/curve25519 生成与校验 X25519 密钥
// [OUTPUT]: 对外提供 GenerateRealityKeypair；包内提供 validateRealityFields、checkX25519Key
// [POS]: domain/nodefabric 协议校验的 REALITY 分项：从 protocol_schema.go 拆出。私钥留在面板并下发给节点、公钥进订阅链接；字段不齐或密钥非法在保存时拦下，不让「看起来配好了」的节点上线
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package nodefabric

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"

	"golang.org/x/crypto/curve25519"
)

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
