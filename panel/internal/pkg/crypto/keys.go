// Package crypto 提供主密钥派生、HMAC 与 AES-GCM 封存。
package crypto

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"

	"golang.org/x/crypto/hkdf"
)

// HKDF info 串（域分隔，勿复用）。
const (
	InfoSeal         = "pandora/seal/v1"
	InfoHMACIP       = "pandora/hmac/ip/v1"
	InfoHMACIdentity = "pandora/hmac/identity/v1"
	InfoHMACSubAudit = "pandora/hmac/sub-audit/v1"
	InfoHMACNotify   = "pandora/hmac/notify/v1"
	InfoHMACGiftCard = "pandora/hmac/giftcard/v1"
	// InfoHMACIdempotency 是幂等请求指纹（落库的请求体摘要）
	InfoHMACIdempotency = "pandora/hmac/idempotency/v1"
	// InfoHMACNodeDevice 是节点在线设备键（IPv4 地址 / IPv6 /64）：IPv4 只有 2^32 个，不加密钥的摘要可穷举还原
	InfoHMACNodeDevice = "pandora/hmac/node-device/v1"
	// InfoSS2022ServerPSK 是 Shadowsocks 2022 节点的服务端身份密钥（iPSK）：HKDF 的 info 后接 0x00、方法名、0x00、节点 id 的 16 字节，不存库
	InfoSS2022ServerPSK = "pandora/ss2022/server-psk/v1"
	// InfoHMACSubProbe 是订阅域名探测票据的签名（绑账号与所在域名组版本，5 分钟有效）
	InfoHMACSubProbe = "pandora/hmac/sub-probe/v1"
	// InfoHMACRefreshNext 派生轮换出的下一枚 refresh 随机串（auth 的 nextSecret）：宽限内能重算出同一枚当前值
	InfoHMACRefreshNext = "pandora/hmac/refresh-next/v1"
)

const minMasterLen = 32

// Keys 持有主密钥材料，用于 HKDF 派生与 HMAC。
type Keys struct {
	master []byte
}

// NewKeys 从 base64 编码的主密钥构造 Keys；解码后少于 32 字节则报错。
func NewKeys(masterB64 string) (*Keys, error) {
	master, err := base64.StdEncoding.DecodeString(masterB64)
	if err != nil {
		return nil, fmt.Errorf("decode master: %w", err)
	}
	if len(master) < minMasterLen {
		return nil, errors.New("master key shorter than 32 bytes")
	}
	return &Keys{master: master}, nil
}

// Derive 用 HKDF-SHA256 从主密钥派生 n 字节。
func (k *Keys) Derive(info string, n int) []byte {
	r := hkdf.New(sha256.New, k.master, nil, []byte(info))
	out := make([]byte, n)
	if _, err := r.Read(out); err != nil {
		panic("hkdf read: " + err.Error())
	}
	return out
}

// HMAC 用派生密钥对 data 做 HMAC-SHA256。
func (k *Keys) HMAC(info string, data []byte) []byte {
	key := k.Derive(info, 32)
	mac := hmac.New(sha256.New, key)
	mac.Write(data)
	return mac.Sum(nil)
}

// Sealer 返回用于信封加密的封存器（keyID=1）。
func (k *Keys) Sealer() (*Sealer, error) {
	key := k.Derive(InfoSeal, 32)
	return NewSealer(key, 1)
}
