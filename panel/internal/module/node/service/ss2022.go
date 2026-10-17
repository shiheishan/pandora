package service

// Shadowsocks 2022 的服务端身份密钥（iPSK）：每个节点一把，由主密钥经 HKDF 派生，不存库。
// 节点配置文档与订阅线路都在 protocol.KernelConfig 之后经 applySS2022ServerPSK 填 password = base64(iPSK)；
// 中转线路复用父节点的 Config，用的就是父节点那把（流量最终进父节点的入站）。换主密钥会让全部 2022 节点的 iPSK 一起变。

import (
	"encoding/base64"
	"errors"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/module/node/protocol"
	"github.com/aegispanel/aegis/internal/pkg/crypto"
)

// PSKDeriver 是 *crypto.Keys 的 HKDF 派生面（SS2022 服务端身份密钥用）。
type PSKDeriver interface {
	Derive(info string, n int) []byte
}

var _ PSKDeriver = (*crypto.Keys)(nil)

// UsePSKDeriver 只在启动装配时调用。没装配时 2022 节点的配置文档生成失败（只跳过该节点），订阅线路装载回错误。
func (s *Service) UsePSKDeriver(d PSKDeriver) { s.psk = d }

var errSS2022KeysMissing = errors.New("SS2022 服务端密钥的派生器未装配")

// ss2022ServerPSK 派生节点的服务端身份密钥，长度 keyLen：
// info = InfoSS2022ServerPSK || 0x00 || 方法名（内核名，如 2022-blake3-aes-256-gcm）|| 0x00 || 节点 id 的 16 字节。
// 带方法名是为了同一节点 128 与 256 两把彼此独立（只带节点 id 时 HKDF 输出的 16 字节恰是 32 字节的前缀）；
// 方法名里没有 0x00，分隔符保证拼接无歧义。
func ss2022ServerPSK(d PSKDeriver, nodeID uuid.UUID, method string, keyLen int) []byte {
	return d.Derive(crypto.InfoSS2022ServerPSK+"\x00"+method+"\x00"+string(nodeID[:]), keyLen)
}

// applySS2022ServerPSK 给翻译后的 shadowsocks 内核配置填服务端密钥：2022 方法写 password = base64(iPSK)，
// 其它方法删掉 password（schema 不收这个键，只有面板派生的才能出现）。非 shadowsocks 不动。
// kernel 必须是调用方自己的 map（KernelConfig 每次新建），不能是缓存共享的那份。
func applySS2022ServerPSK(d PSKDeriver, nodeID uuid.UUID, nodeType string, kernel map[string]any) error {
	if protocol.CanonicalType(nodeType) != "shadowsocks" {
		return nil
	}
	delete(kernel, "password")
	method, _ := kernel["method"].(string)
	keyLen, ok := protocol.SS2022KeyLen(method)
	if !ok {
		return nil
	}
	if d == nil {
		return errSS2022KeysMissing
	}
	kernel["password"] = base64.StdEncoding.EncodeToString(ss2022ServerPSK(d, nodeID, method, keyLen))
	return nil
}
