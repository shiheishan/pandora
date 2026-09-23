// Package crypto 提供平台统一的口令哈希、令牌生成、签名与信封加密。
//
// 一条贯穿全包的原则：凡是会落库的秘密，落的都是哈希或密文，从不是明文。
// 对应 IAM-002/003、XBD-002、MKT-002、SEC-010、SEC-011。
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

//------------------------------------------------------------------------------
// 口令哈希（IAM-003）
//------------------------------------------------------------------------------

// Argon2Params 是 Argon2id 的代价参数。
//
// 默认值按 OWASP 建议的低内存档（19 MiB / 2 次迭代）选取，
// 而不是更常见的 64 MiB —— 本平台首发跑在 2G 内存的机器上，
// 64 MiB × 并发登录会直接把内存打满，形成自伤式 DoS。
// 参数内嵌在 PHC 串里，将来换机器可以随用户登录逐步重算，无需一次性迁移。
type Argon2Params struct {
	Memory      uint32 // KiB
	Iterations  uint32
	Parallelism uint8
	SaltLength  uint32
	KeyLength   uint32
}

func DefaultArgon2Params() Argon2Params {
	return Argon2Params{
		Memory:      19 * 1024,
		Iterations:  2,
		Parallelism: 1,
		SaltLength:  16,
		KeyLength:   32,
	}
}

// HashPassword 返回 PHC 格式的 Argon2id 串。
func HashPassword(password string, p Argon2Params) (string, error) {
	salt := make([]byte, p.SaltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("生成盐值: %w", err)
	}
	key := argon2.IDKey([]byte(password), salt, p.Iterations, p.Memory, p.Parallelism, p.KeyLength)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, p.Memory, p.Iterations, p.Parallelism,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key)), nil
}

// VerifyPassword 以恒定时间比较口令与 PHC 串。
// needsRehash 为 true 表示该哈希用的参数弱于当前默认值，应在本次登录后静默升级。
func VerifyPassword(password, phc string) (ok bool, needsRehash bool, err error) {
	parts := strings.Split(phc, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, false, errors.New("不是合法的 argon2id PHC 串")
	}

	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false, false, errors.New("argon2 版本不匹配")
	}

	var p Argon2Params
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d",
		&p.Memory, &p.Iterations, &p.Parallelism); err != nil {
		return false, false, errors.New("无法解析 argon2 参数")
	}

	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false, false, errors.New("盐值不是合法 base64")
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false, false, errors.New("哈希不是合法 base64")
	}

	got := argon2.IDKey([]byte(password), salt,
		p.Iterations, p.Memory, p.Parallelism, uint32(len(want)))

	ok = subtle.ConstantTimeCompare(got, want) == 1

	def := DefaultArgon2Params()
	needsRehash = p.Memory < def.Memory || p.Iterations < def.Iterations

	return ok, needsRehash, nil
}

// DummyVerify 在用户不存在时也走一遍等价的计算量。
//
// 这是 IAM-006「不能通过正文、状态码或明显时间差确认账号存在」的必要条件：
// 若账号不存在就立即返回，攻击者用响应耗时就能枚举出哪些邮箱已注册。
func DummyVerify(password string) {
	p := DefaultArgon2Params()
	salt := make([]byte, p.SaltLength) // 全零盐，结果丢弃，只为消耗等量 CPU
	_ = argon2.IDKey([]byte(password), salt, p.Iterations, p.Memory, p.Parallelism, p.KeyLength)
}

//------------------------------------------------------------------------------
// 令牌与验证码
//------------------------------------------------------------------------------

// NewToken 生成 n 字节熵的 URL-safe 随机令牌。
// 订阅凭据、Bootstrap Token、礼品码都用它，配合只存哈希即可满足
// XBD-002 / NODE-008 / MKT-002。
func NewToken(nBytes int) (string, error) {
	b := make([]byte, nBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("生成随机令牌: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// HashToken 返回令牌的 SHA-256。
// 令牌本身是高熵随机串，不存在字典攻击面，因此无需慢哈希。
func HashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

// NewNumericCode 生成 n 位数字验证码，使用无模偏的拒绝采样。
func NewNumericCode(n int) (string, error) {
	const digits = "0123456789"
	out := make([]byte, n)
	buf := make([]byte, 1)
	for i := 0; i < n; {
		if _, err := rand.Read(buf); err != nil {
			return "", fmt.Errorf("生成验证码: %w", err)
		}
		// 256 不是 10 的整数倍，直接取模会让 0–5 的概率略高于 6–9。
		// 丢弃落在 250–255 的采样即可消除偏差。
		if buf[0] >= 250 {
			continue
		}
		out[i] = digits[buf[0]%10]
		i++
	}
	return string(out), nil
}

// HashIdentifier 对邮箱/IP 等标识做带盐哈希，用于日志与风控关联而不落明文（SEC-011）。
func HashIdentifier(salt []byte, value string) []byte {
	m := hmac.New(sha256.New, salt)
	m.Write([]byte(strings.ToLower(strings.TrimSpace(value))))
	return m.Sum(nil)
}

// ConstantTimeEqual 恒定时间比较两个字节切片。
func ConstantTimeEqual(a, b []byte) bool {
	return subtle.ConstantTimeCompare(a, b) == 1
}

//------------------------------------------------------------------------------
// 内容签名（CLI-004 / AGT-007 / USE-001）
//------------------------------------------------------------------------------

// Signer 用 Ed25519 为订阅配置、节点配置与 Agent 任务签名。
type Signer struct {
	priv  ed25519.PrivateKey
	keyID string
}

func NewSigner(seed []byte) (*Signer, error) {
	if len(seed) < ed25519.SeedSize {
		return nil, fmt.Errorf("签名种子需要至少 %d 字节", ed25519.SeedSize)
	}
	priv := ed25519.NewKeyFromSeed(seed[:ed25519.SeedSize])
	// 密钥 ID 取公钥指纹前 8 字节，客户端据此选择验签公钥，支持平滑轮换
	sum := sha256.Sum256(priv.Public().(ed25519.PublicKey))
	return &Signer{priv: priv, keyID: base64.RawURLEncoding.EncodeToString(sum[:8])}, nil
}

func (s *Signer) KeyID() string                { return s.keyID }
func (s *Signer) PublicKey() ed25519.PublicKey { return s.priv.Public().(ed25519.PublicKey) }
func (s *Signer) Sign(payload []byte) []byte   { return ed25519.Sign(s.priv, payload) }

func Verify(pub ed25519.PublicKey, payload, sig []byte) bool {
	return ed25519.Verify(pub, payload, sig)
}

//------------------------------------------------------------------------------
// 信封加密（SEC-010）
//------------------------------------------------------------------------------

// Envelope 用 AES-256-GCM 加密支付渠道密钥、云凭据、TOTP 种子等高敏字段。
//
// 当前用本地主密钥派生数据密钥；生产环境应把 Unwrap 换成 KMS/Vault 调用，
// 接口保持不变。SEC-010 验收「数据库备份泄漏时密钥仍不可直接读取」的前提是
// 主密钥不与数据库同处一地 —— 这是部署约束，代码只能保证密文本身安全。
type Envelope struct {
	master []byte
}

func NewEnvelope(masterKey []byte) (*Envelope, error) {
	if len(masterKey) < 32 {
		return nil, errors.New("主密钥至少需要 32 字节")
	}
	return &Envelope{master: masterKey[:32]}, nil
}

// Seal 加密明文。输出格式：version(1) || nonce(12) || ciphertext+tag。
// aad 传入字段的业务标识（如 "payment_provider:stripe:credentials"），
// 这样密文即便被搬到另一行也无法解开。
func (e *Envelope) Seal(plaintext, aad []byte) ([]byte, error) {
	block, err := aes.NewCipher(e.master)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("生成 nonce: %w", err)
	}

	out := make([]byte, 0, 1+len(nonce)+len(plaintext)+gcm.Overhead())
	out = append(out, 0x01) // 版本位，为将来换算法留出升级路径
	out = append(out, nonce...)
	return gcm.Seal(out, nonce, plaintext, aad), nil
}

func (e *Envelope) Open(sealed, aad []byte) ([]byte, error) {
	if len(sealed) < 1 {
		return nil, errors.New("密文为空")
	}
	if sealed[0] != 0x01 {
		return nil, fmt.Errorf("不支持的信封版本 %d", sealed[0])
	}

	block, err := aes.NewCipher(e.master)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	ns := gcm.NonceSize()
	if len(sealed) < 1+ns {
		return nil, errors.New("密文长度不足")
	}
	return gcm.Open(nil, sealed[1:1+ns], sealed[1+ns:], aad)
}

// HMACSign 为 Webhook 出站签名（EXT-004）。
func HMACSign(secret, payload []byte) []byte {
	m := hmac.New(sha256.New, secret)
	m.Write(payload)
	return m.Sum(nil)
}

func HMACVerify(secret, payload, sig []byte) bool {
	return hmac.Equal(HMACSign(secret, payload), sig)
}

// SubscriptionAuditSalt 派生订阅审计日志专用的哈希盐。
//
// 不直接用主密钥：万一审计库泄露，也不该能从里面倒推出任何与加密相关的
// 东西。又必须是确定性派生而不是随机生成——重启后算出不同的盐，同一个 IP
// 前后哈希就对不上，多来源检测立刻失效。
//
// 抽成函数是因为它有两个使用方：写入端（订阅服务）和查询端（后台按 IP
// 反查）。公式散在两处的话，改一处忘一处的症状是「筛选永远查不到东西」，
// 而且不报错——2026-08-07 就是这么踩的，当时后台用主密钥去比对订阅表用
// 这个盐算出的哈希。
func SubscriptionAuditSalt(masterKey []byte) []byte {
	sum := sha256.Sum256(append([]byte("aegis/subscription/audit-salt/v1"), masterKey...))
	return sum[:]
}

// HashRaw 用给定的盐做 HMAC，不对输入做规范化。
//
// 与 HashIdentifier 的区别只在这里：那个会 lower + trim（为了邮箱这类
// 大小写无关的标识），而订阅审计日志里的 IP 和 UA 是按原样哈希的。
// UA 大小写有意义，改了就对不上已有数据。
func HashRaw(salt []byte, value string) []byte {
	if value == "" {
		return nil
	}
	m := hmac.New(sha256.New, salt)
	m.Write([]byte(value))
	return m.Sum(nil)
}
