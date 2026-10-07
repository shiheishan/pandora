// Package bindingcontract 是「服务器级绑定 + 多面板」合约的节点侧原像实现：
// 服务器请求签名、服务器清单、按池名单、升级指令、官方发布清单、服务器密钥持有证明
// 六个签名原像，以及面板公钥指纹、网关 SPKI 钉住、面板来源规范化、版本比较这几个纯函数。
//
// 合约正文在仓库根 docs/server-binding-contract.md。面板侧另有一份实现
// （panel/internal/platform/bindingcontract），两边用同一份金样本
// testdata/bindingcontract-v1-golden.json 互相钉住：改一边必须同步另一边，
// 金样本只在面板侧重新生成
// （在 panel/ 下 go test ./internal/platform/bindingcontract -run TestGolden -bindingcontract.update），
// 生成时两份一起写。
//
// 本包只算原像、只做格式校验，不签名、不验签、不碰网络与磁盘；签名与验签由调用方
// 用 crypto/ed25519 对原像字节完成。首行是域分隔串，其后每行 key=value，
// 每行以 LF 结尾；所有字段先校验规范形式再拼接，非规范输入一律报错而不是悄悄改写。
package bindingcontract

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	// ServerRequestContract 是服务器身份请求签名的域分隔串。
	ServerRequestContract = "aegis-server-request-v1"
	// ManifestContract 是服务器清单签名的域分隔串，也是清单 JSON 的 contract 字段。
	ManifestContract = "aegis-server-manifest-v1"
	// UsersContract 是按池名单响应签名的域分隔串。
	UsersContract = "aegis-server-users-v1"
	// UpgradeOrderContract 是面板「升级到官方版本 X」指令的域分隔串。
	UpgradeOrderContract = "aegis-agent-upgrade-order-v1"
	// ReleaseManifestContract 是官方发布清单的域分隔串（由离线发布密钥签名，不是面板密钥）。
	ReleaseManifestContract = "pandora-release-manifest-v1"
	// KeyPossessionContract 是旧节点升级为服务器绑定时，新服务器密钥的持有证明。
	KeyPossessionContract = "aegis-server-key-possession-v1"

	// MaxDeliveryWindow 是清单、名单、升级指令的签名有效窗口上限，与生效发布一致。
	MaxDeliveryWindow = 10 * time.Minute
	// RequestSkew 是服务器请求时间戳允许的偏差，与节点请求签名一致（nonce 至少保留这么久）。
	RequestSkew = 5 * time.Minute

	// EncKEM 是服务器加密公钥的 KEM 名，与证书设计里登记加密公钥时的 kem 取值一致。
	EncKEM = "dhkem-x25519-hkdf-sha256"
	// ReleaseProduct 是官方发布清单里唯一允许的 product。
	ReleaseProduct = "pandora-native"
)

// line 写一行 key=value 并以 LF 结尾。调用前每个值都已校验过不含 LF。
func line(b *strings.Builder, key, value string) {
	b.WriteString(key)
	b.WriteByte('=')
	b.WriteString(value)
	b.WriteByte('\n')
}

func checkUUID(field, value string) error {
	parsed, err := uuid.Parse(value)
	if err != nil || parsed == uuid.Nil || parsed.String() != value {
		return fmt.Errorf("%s must be a canonical lowercase non-nil UUID", field)
	}
	return nil
}

// checkSHA256B64 要求标准 base64（带填充）编码的恰好 32 字节。
func checkSHA256B64(field, value string) error {
	raw, err := base64.StdEncoding.DecodeString(value)
	if err != nil || len(raw) != sha256.Size || base64.StdEncoding.EncodeToString(raw) != value {
		return fmt.Errorf("%s must be canonical base64 for exactly 32 bytes", field)
	}
	return nil
}

// checkSHA256Hex 要求 64 个小写十六进制字符（与 sha256sum 输出一致）。
func checkSHA256Hex(field, value string) error {
	raw, err := hex.DecodeString(value)
	if err != nil || len(raw) != sha256.Size || hex.EncodeToString(raw) != value {
		return fmt.Errorf("%s must be 64 lowercase hex characters", field)
	}
	return nil
}

// checkPublicKey32 要求标准 base64 编码的恰好 32 字节（Ed25519 或 X25519 公钥）。
func checkPublicKey32(field, value string) error {
	raw, err := base64.StdEncoding.DecodeString(value)
	if err != nil || len(raw) != 32 || base64.StdEncoding.EncodeToString(raw) != value {
		return fmt.Errorf("%s must be canonical base64 for a 32-byte public key", field)
	}
	return nil
}

// checkKeyID 要求无填充 base64url 编码的恰好 8 字节，即 KeyID 的输出形式。
func checkKeyID(field, value string) error {
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(raw) != 8 || base64.RawURLEncoding.EncodeToString(raw) != value {
		return fmt.Errorf("%s must be canonical base64url for exactly 8 bytes", field)
	}
	return nil
}

// checkNonce 要求无填充 base64url 编码的恰好 16 字节（22 个字符），与节点请求 nonce 同格式。
func checkNonce(field, value string) error {
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if len(value) != 22 || err != nil || len(raw) != 16 || base64.RawURLEncoding.EncodeToString(raw) != value {
		return fmt.Errorf("%s must be canonical base64url for exactly 16 bytes", field)
	}
	return nil
}

func checkSerial(value int64) error {
	if value <= 0 || value > math.MaxInt32 {
		return errors.New("serial must be a positive int4")
	}
	return nil
}

func checkGeneration(field string, value uint64) error {
	if value == 0 || value > math.MaxInt64 {
		return fmt.Errorf("%s must be a positive PostgreSQL bigint", field)
	}
	return nil
}

// canonicalTime 要求 time.RFC3339Nano 的 UTC 规范写法、精度不超过微秒
// （与 PostgreSQL timestamptz 往返一致），返回解析后的时间。
func canonicalTime(field, value string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339Nano, value)
	if err != nil || t.Nanosecond()%1000 != 0 || t.UTC().Format(time.RFC3339Nano) != value {
		return time.Time{}, fmt.Errorf("%s must be canonical RFC3339 UTC with at most microsecond precision", field)
	}
	return t, nil
}

// canonicalWindow 校验签发与过期时间都是规范写法，且窗口为正、不超过 MaxDeliveryWindow。
func canonicalWindow(issuedAt, expiresAt string) error {
	issued, err := canonicalTime("issued_at", issuedAt)
	if err != nil {
		return err
	}
	expires, err := canonicalTime("expires_at", expiresAt)
	if err != nil {
		return err
	}
	if !expires.After(issued) {
		return errors.New("expires_at must be after issued_at")
	}
	if expires.Sub(issued) > MaxDeliveryWindow {
		return errors.New("delivery window exceeds ten minutes")
	}
	return nil
}

// KeyID 是公钥的短标识：base64url(sha256(公钥原始字节)[:8])，无填充。
// 配置签名密钥、服务器身份密钥、服务器加密密钥、发布密钥都用这一种格式。
func KeyID(publicKey []byte) string {
	sum := sha256.Sum256(publicKey)
	return base64.RawURLEncoding.EncodeToString(sum[:8])
}

// BodySHA256 是服务器请求原像里 body_sha256 的取值：base64(sha256(请求体原始字节))。
// 没有请求体（GET）时传 nil，得到空串的哈希。
func BodySHA256(body []byte) string {
	sum := sha256.Sum256(body)
	return base64.StdEncoding.EncodeToString(sum[:])
}

// formatInt 只为让原像拼接处读起来整齐。
func formatInt(v int64) string { return strconv.FormatInt(v, 10) }

func formatUint(v uint64) string { return strconv.FormatUint(v, 10) }
