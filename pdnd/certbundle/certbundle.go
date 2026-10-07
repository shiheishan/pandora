// Package certbundle 是证书包契约 pandora-node-certificate-bundle-v1 的节点侧实现：
// 签名原像与验签、HPKE 解密托管证书私钥。
//
// 面板侧另有一份独立实现（panel/internal/platform/certbundle），两边用同一份金样本
// testdata/certbundle-v1-golden.json 互相钉住，改一边必须同步另一边并重新生成金样本。
// 写法沿用生效发布契约 aegis-node-effective-config-release-v1（pdnd/panel/effective_release.go）。
package certbundle

import (
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/hpke"
	"crypto/rand"
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
	for field, value := range map[string]string{"tenant_id": f.TenantID, "server_id": f.ServerID, "node_id": f.NodeID} {
		if err := canonicalUUID(field, value); err != nil {
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
		return nil, errors.New("certificate bundle timestamps must use microsecond precision")
	}
	if !expires.After(issued) || expires.Sub(issued) > MaxDeliveryWindow {
		return nil, errors.New("invalid certificate bundle delivery window")
	}
	var b strings.Builder
	b.WriteString(Contract + "\n")
	b.WriteString("tenant_id=" + f.TenantID + "\n")
	b.WriteString("server_id=" + f.ServerID + "\n")
	b.WriteString("node_id=" + f.NodeID + "\n")
	b.WriteString("epoch=" + strconv.FormatUint(f.Epoch, 10) + "\n")
	b.WriteString("enc_key_id=" + f.EncKeyID + "\n")
	b.WriteString("content_sha256=" + f.ContentSHA256 + "\n")
	b.WriteString("key_id=" + f.KeyID + "\n")
	b.WriteString("issued_at=" + issued.Format(time.RFC3339Nano) + "\n")
	b.WriteString("expires_at=" + expires.Format(time.RFC3339Nano) + "\n")
	return []byte(b.String()), nil
}

// VerifySignature 校验证书包签名：key_id 必须就是钉住的配置签名公钥、签名是规范 base64 的 Ed25519、
// 原像字段合法、now 落在 [issued_at, expires_at) 内。
func VerifySignature(configPublicKey ed25519.PublicKey, f SignatureFields, signature string, now time.Time) error {
	if len(configPublicKey) != ed25519.PublicKeySize {
		return errors.New("invalid config public key")
	}
	if KeyID(configPublicKey) != f.KeyID {
		return errors.New("certificate bundle key_id does not match pinned config public key")
	}
	preimage, err := SignaturePreimage(f)
	if err != nil {
		return err
	}
	sig, err := base64.StdEncoding.DecodeString(signature)
	if err != nil || len(sig) != ed25519.SignatureSize || base64.StdEncoding.EncodeToString(sig) != signature ||
		!ed25519.Verify(configPublicKey, preimage, sig) {
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
	for field, value := range map[string]string{"tenant_id": f.TenantID, "server_id": f.ServerID, "cert_id": f.CertID} {
		if err := canonicalUUID(field, value); err != nil {
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

// RecipientPublicKey 由 32 字节 X25519 私钥算出公钥（登记到面板的就是它）。
func RecipientPublicKey(private []byte) ([]byte, error) {
	k, err := ecdh.X25519().NewPrivateKey(private)
	if err != nil {
		return nil, err
	}
	return k.PublicKey().Bytes(), nil
}

// GenerateRecipientKey 生成一把新的 X25519 加密密钥，返回 32 字节私钥与公钥。
func GenerateRecipientKey() (private, public []byte, err error) {
	k, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	return k.Bytes(), k.PublicKey().Bytes(), nil
}

// OpenPrivateKey 用节点的 X25519 私钥解开证书私钥；sealed 是 enc||ciphertext（hpke.Seal 的输出）。
// 明文必须是 PKCS#8 DER，否则按解密失败处理。info 任一字段不同、换了接收方密钥都会解密失败。
func OpenPrivateKey(recipientPrivate []byte, f KeyInfoFields, sealed []byte) ([]byte, error) {
	info, err := KeyInfo(f)
	if err != nil {
		return nil, err
	}
	kem, kdf, aead := hpkeSuite()
	sk, err := kem.NewPrivateKey(recipientPrivate)
	if err != nil {
		return nil, fmt.Errorf("invalid recipient private key: %w", err)
	}
	plaintext, err := hpke.Open(sk, kdf, aead, info, sealed)
	if err != nil {
		return nil, errors.New("certificate private key decryption failed")
	}
	if _, err := x509.ParsePKCS8PrivateKey(plaintext); err != nil {
		return nil, errors.New("decrypted certificate private key is not PKCS#8")
	}
	return plaintext, nil
}
