// Package certbundle 是证书包契约 pandora-node-certificate-bundle-v1 的面板侧实现：
// 签名原像与签名、HPKE 封装托管证书私钥。
//
// 节点侧另有一份独立实现（pdnd/certbundle），两边用同一份金样本
// testdata/certbundle-v1-golden.json 互相钉住，改一边必须同步另一边并重新生成金样本
// （go test ./internal/platform/certbundle -run TestGolden -certbundle.update）。
// 写法沿用生效发布契约（nodefabric/effective_release_codec.go）。
package certbundle

import (
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/hpke"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	platformcrypto "github.com/aegispanel/aegis/internal/platform/crypto"
)

const (
	// Contract 是证书包签名原像的域分隔串，也是线上的 contract 字段。
	Contract = "pandora-node-certificate-bundle-v1"
	// InfoLabel 是 HPKE info 的首行，把密文绑定到「私钥封装」这一用途。
	InfoLabel = "pandora-cert-key-v1"
	// KeyAlg 是证书包里 key.alg 的取值：DHKEM(X25519, HKDF-SHA256) + HKDF-SHA256 + AES-256-GCM，单发 Seal。
	KeyAlg = "hpke-x25519-hkdfsha256-aes256gcm"
	// MaxDeliveryWindow 限制一个证书包的有效窗口，防止截获的包变成长期可重放的凭据。
	MaxDeliveryWindow = 10 * time.Minute
)

// hpkeSuite 固定套件；两端各自写死，金样本钉住。
func hpkeSuite() (hpke.KEM, hpke.KDF, hpke.AEAD) {
	return hpke.DHKEM(ecdh.X25519()), hpke.HKDFSHA256(), hpke.AES256GCM()
}

// SignatureFields 是证书包签名覆盖的字段。证书条目经 content_sha256（规范 JSON 的哈希）间接覆盖。
type SignatureFields struct {
	TenantID      string
	ServerID      string
	NodeID        string // 发起拉取的节点身份
	Epoch         uint64
	EncKeyID      string // 节点 X25519 加密公钥的 key id
	ContentSHA256 string
	KeyID         string // 面板配置签名公钥的 key id
	IssuedAt      time.Time
	ExpiresAt     time.Time
}

func canonicalUUID(field, value string) error {
	parsed, err := uuid.Parse(value)
	if err != nil || parsed == uuid.Nil || parsed.String() != value {
		return fmt.Errorf("%s must be a canonical lowercase UUID", field)
	}
	return nil
}

func canonicalSHA256(field, value string) error {
	raw, err := base64.StdEncoding.DecodeString(value)
	if err != nil || len(raw) != sha256.Size || base64.StdEncoding.EncodeToString(raw) != value {
		return fmt.Errorf("%s must be canonical base64 for exactly 32 bytes", field)
	}
	return nil
}

func canonicalKeyID(field, value string) error {
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(raw) != 8 || base64.RawURLEncoding.EncodeToString(raw) != value {
		return fmt.Errorf("%s must be canonical base64url for exactly 8 bytes", field)
	}
	return nil
}

func positiveBigint(field string, v uint64) error {
	if v == 0 || v > math.MaxInt64 {
		return fmt.Errorf("%s must be a positive PostgreSQL bigint", field)
	}
	return nil
}

// KeyID 是公钥的 key id：base64url(sha256(pub)[:8])，无填充。配置签名公钥与 X25519 加密公钥同一口径。
func KeyID(pub []byte) string {
	sum := sha256.Sum256(pub)
	return base64.RawURLEncoding.EncodeToString(sum[:8])
}

// ChainSHA256 是 fullchain PEM 原始字节的 SHA-256，标准 base64。
func ChainSHA256(chainPEM []byte) string {
	sum := sha256.Sum256(chainPEM)
	return base64.StdEncoding.EncodeToString(sum[:])
}

// SignaturePreimage 返回面板签名、节点独立重建的逐行原像（每行以 LF 结尾）。
// 行序：contract、tenant_id、server_id、node_id、epoch、enc_key_id、content_sha256、key_id、issued_at、expires_at。
// 时间按 UTC、微秒精度、RFC3339Nano 输出（与 PostgreSQL timestamptz 一致）。
func SignaturePreimage(f SignatureFields) ([]byte, error) {
	for _, c := range []struct{ field, value string }{
		{"tenant_id", f.TenantID}, {"server_id", f.ServerID}, {"node_id", f.NodeID},
	} {
		if err := canonicalUUID(c.field, c.value); err != nil {
			return nil, err
		}
	}
	if err := positiveBigint("epoch", f.Epoch); err != nil {
		return nil, err
	}
	if err := canonicalKeyID("enc_key_id", f.EncKeyID); err != nil {
		return nil, err
	}
	if err := canonicalSHA256("content_sha256", f.ContentSHA256); err != nil {
		return nil, err
	}
	if err := canonicalKeyID("key_id", f.KeyID); err != nil {
		return nil, err
	}
	issued, expires := f.IssuedAt.UTC(), f.ExpiresAt.UTC()
	if issued.IsZero() || expires.IsZero() || issued.Nanosecond()%1000 != 0 || expires.Nanosecond()%1000 != 0 {
		return nil, errors.New("certificate bundle timestamps must use PostgreSQL microsecond precision")
	}
	if !expires.After(issued) {
		return nil, errors.New("expires_at must be after issued_at")
	}
	if expires.Sub(issued) > MaxDeliveryWindow {
		return nil, errors.New("certificate bundle delivery window exceeds ten minutes")
	}

	var b strings.Builder
	b.Grow(480)
	for _, line := range []string{
		Contract,
		"tenant_id=" + f.TenantID,
		"server_id=" + f.ServerID,
		"node_id=" + f.NodeID,
		"epoch=" + strconv.FormatUint(f.Epoch, 10),
		"enc_key_id=" + f.EncKeyID,
		"content_sha256=" + f.ContentSHA256,
		"key_id=" + f.KeyID,
		"issued_at=" + issued.Format(time.RFC3339Nano),
		"expires_at=" + expires.Format(time.RFC3339Nano),
	} {
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return []byte(b.String()), nil
}

// Sign 对原像签名并输出规范标准 base64；key_id 必须就是 signer 的 key id。
func Sign(signer *platformcrypto.Signer, f SignatureFields) (string, error) {
	if signer == nil {
		return "", errors.New("certificate bundle signer is required")
	}
	if f.KeyID != signer.KeyID() {
		return "", errors.New("certificate bundle key_id does not match signer")
	}
	preimage, err := SignaturePreimage(f)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(signer.Sign(preimage)), nil
}

// KeyInfoFields 是 HPKE info 绑定的字段：密文只能在这张证书的这个版本、这条链上打开。
type KeyInfoFields struct {
	TenantID    string
	ServerID    string
	CertID      string
	Version     uint64
	ChainSHA256 string
}

// KeyInfo 返回 HPKE info：
// "pandora-cert-key-v1\n<tenant>\n<server>\n<cert_id>\n<version>\n<chain_sha256>"，末尾不带换行。
func KeyInfo(f KeyInfoFields) ([]byte, error) {
	for _, c := range []struct{ field, value string }{
		{"tenant_id", f.TenantID}, {"server_id", f.ServerID}, {"cert_id", f.CertID},
	} {
		if err := canonicalUUID(c.field, c.value); err != nil {
			return nil, err
		}
	}
	if err := positiveBigint("version", f.Version); err != nil {
		return nil, err
	}
	if err := canonicalSHA256("chain_sha256", f.ChainSHA256); err != nil {
		return nil, err
	}
	return []byte(strings.Join([]string{
		InfoLabel, f.TenantID, f.ServerID, f.CertID, strconv.FormatUint(f.Version, 10), f.ChainSHA256,
	}, "\n")), nil
}

// SealPrivateKey 把 PKCS#8 DER 私钥 HPKE 封装给节点的 X25519 公钥，返回 enc||ciphertext。
// 每次调用重新封装（ephemeral 密钥随机），密文不入库。
func SealPrivateKey(recipientPublic []byte, f KeyInfoFields, pkcs8 []byte) ([]byte, error) {
	info, err := KeyInfo(f)
	if err != nil {
		return nil, err
	}
	if _, err := x509.ParsePKCS8PrivateKey(pkcs8); err != nil {
		return nil, errors.New("certificate private key must be PKCS#8 DER")
	}
	kem, kdf, aead := hpkeSuite()
	pk, err := kem.NewPublicKey(recipientPublic)
	if err != nil {
		return nil, fmt.Errorf("invalid recipient public key: %w", err)
	}
	return hpke.Seal(pk, kdf, aead, info, pkcs8)
}

// VerifySignature 供面板自检与测试：校验 key_id 与公钥一致、签名有效、now 在投递窗口内。
func VerifySignature(pub ed25519.PublicKey, f SignatureFields, signature string, now time.Time) error {
	if len(pub) != ed25519.PublicKeySize {
		return errors.New("certificate bundle public key is invalid")
	}
	if KeyID(pub) != f.KeyID {
		return errors.New("certificate bundle key_id does not match public key")
	}
	preimage, err := SignaturePreimage(f)
	if err != nil {
		return err
	}
	sig, err := base64.StdEncoding.DecodeString(signature)
	if err != nil || len(sig) != ed25519.SignatureSize || base64.StdEncoding.EncodeToString(sig) != signature ||
		!ed25519.Verify(pub, preimage, sig) {
		return errors.New("certificate bundle signature invalid")
	}
	now = now.UTC()
	if now.Before(f.IssuedAt.UTC()) {
		return errors.New("certificate bundle is not yet valid")
	}
	if !now.Before(f.ExpiresAt.UTC()) {
		return errors.New("certificate bundle expired")
	}
	return nil
}
