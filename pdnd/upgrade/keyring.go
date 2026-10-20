package upgrade

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"regexp"
)

// keysFS 嵌入整个 keys 目录。目录内至少包含 README.md，因此在无 .pub 文件时也能正常编译。
//
//go:embed keys
var keysFS embed.FS

// keyIDHexRegex 匹配 16 位小写十六进制字符串。
var keyIDHexRegex = regexp.MustCompile(`^[0-9a-f]{16}$`)

// Keyring 管理内置的 ed25519 公钥集合，以 key_id 为索引。
type Keyring struct {
	keys map[string]ed25519.PublicKey // key_id → 32 字节 ed25519 公钥
}

// KeyID 计算公钥的 16 位小写十六进制标识符：hex(sha256(公钥 32 字节))[:16]。
func KeyID(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return hex.EncodeToString(sum[:])[:16]
}

// ParsePublicKey 去掉首尾空白后按标准 base64 解码，必须正好是 32 字节 ed25519 公钥。
func ParsePublicKey(text []byte) (ed25519.PublicKey, error) {
	trimmed := bytes.TrimSpace(text)
	data, err := base64.StdEncoding.DecodeString(string(trimmed))
	if err != nil {
		return nil, fmt.Errorf("base64 解码失败: %w", err)
	}
	if len(data) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("公钥长度错误: 期望 %d 字节，实际 %d 字节", ed25519.PublicKeySize, len(data))
	}
	return ed25519.PublicKey(data), nil
}

// NewKeyring 从给定的公钥列表构造 Keyring。若公钥列表为空，生成空 Keyring。
func NewKeyring(pubs ...ed25519.PublicKey) *Keyring {
	km := make(map[string]ed25519.PublicKey, len(pubs))
	for _, pub := range pubs {
		if len(pub) == ed25519.PublicKeySize {
			km[KeyID(pub)] = pub
		}
	}
	return &Keyring{keys: km}
}

// LoadKeyring 在 fsys 中按 pattern 匹配文件并逐个解析为公钥。
// 任一文件读取或解析失败时整体报错并带上文件名。
func LoadKeyring(fsys fs.FS, pattern string) (*Keyring, error) {
	matches, err := fs.Glob(fsys, pattern)
	if err != nil {
		return nil, fmt.Errorf("匹配公钥文件失败: %w", err)
	}
	km := make(map[string]ed25519.PublicKey, len(matches))
	for _, path := range matches {
		data, err := fs.ReadFile(fsys, path)
		if err != nil {
			return nil, fmt.Errorf("读取公钥文件 %s 失败: %w", path, err)
		}
		pub, err := ParsePublicKey(data)
		if err != nil {
			return nil, fmt.Errorf("解析公钥文件 %s 失败: %w", path, err)
		}
		km[KeyID(pub)] = pub
	}
	return &Keyring{keys: km}, nil
}

// BuiltinKeyring 加载二进制内编译进的 keys/*.pub 公钥。
// 当 keys 目录下没有任何 .pub 文件时，返回空 Keyring 且错误为 nil。
func BuiltinKeyring() (*Keyring, error) {
	return LoadKeyring(keysFS, "keys/*.pub")
}

// Len 返回钥匙环中的公钥数量。nil 接收者返回 0。
func (k *Keyring) Len() int {
	if k == nil {
		return 0
	}
	return len(k.keys)
}

// Verify 对比签名文件并验证清单的 ed25519 签名。
// 任何一步失败（超长、格式错误、未识别 key_id、签名不匹配、空 keyring）一律返回 *Error{Code: ReasonSignatureInvalid}。
func (k *Keyring) Verify(manifest, sigFile []byte) error {
	if int64(len(sigFile)) > MaxSignatureBytes {
		return Errorf(ReasonSignatureInvalid, "签名文件超过最大大小限制（%d 字节）", MaxSignatureBytes)
	}
	if k == nil || len(k.keys) == 0 {
		return Errorf(ReasonSignatureInvalid, "内置公钥环为空，无法验证升级签名")
	}

	var sigObj SignatureFile
	if err := json.Unmarshal(sigFile, &sigObj); err != nil {
		return Errorf(ReasonSignatureInvalid, "签名文件不是合法的 JSON: %v", err)
	}

	if !keyIDHexRegex.MatchString(sigObj.KeyID) {
		return Errorf(ReasonSignatureInvalid, "签名 key_id 格式不合规（须为 16 位小写十六进制）: %s", sigObj.KeyID)
	}

	rawSig, err := base64.StdEncoding.DecodeString(sigObj.Sig)
	if err != nil {
		return Errorf(ReasonSignatureInvalid, "签名 base64 解码失败: %v", err)
	}
	if len(rawSig) != ed25519.SignatureSize {
		return Errorf(ReasonSignatureInvalid, "签名长度不合规: 期望 %d 字节，实际 %d 字节", ed25519.SignatureSize, len(rawSig))
	}

	pub, ok := k.keys[sigObj.KeyID]
	if !ok {
		return Errorf(ReasonSignatureInvalid, "未知的签名 key_id: %s", sigObj.KeyID)
	}

	if !ed25519.Verify(pub, manifest, rawSig) {
		return Errorf(ReasonSignatureInvalid, "ed25519 签名校验失败")
	}

	return nil
}
