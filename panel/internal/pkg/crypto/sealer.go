package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"io"
)

const (
	sealVersion = 0x01
	nonceSize   = 12
)

// ErrOpen 表示封存数据无法通过认证打开。
var ErrOpen = errors.New("sealed blob open failed")

// Sealer 使用 AES-256-GCM 封存明文。
type Sealer struct {
	key   []byte
	keyID byte
	aead  cipher.AEAD
}

// NewSealer 构造封存器；key 必须为 32 字节。
func NewSealer(key []byte, keyID byte) (*Sealer, error) {
	if len(key) != 32 {
		return nil, errors.New("sealer key must be 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Sealer{key: key, keyID: keyID, aead: aead}, nil
}

// Seal 输出格式：[版本][keyID][12 字节 nonce][密文+tag]。
func (s *Sealer) Seal(plain, aad []byte) ([]byte, error) {
	nonce := make([]byte, nonceSize)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	ct := s.aead.Seal(nil, nonce, plain, aad)
	out := make([]byte, 0, 1+1+nonceSize+len(ct))
	out = append(out, sealVersion, s.keyID)
	out = append(out, nonce...)
	out = append(out, ct...)
	return out, nil
}

// Open 校验版本与 keyID 后解密；失败统一返回 ErrOpen。
func (s *Sealer) Open(sealed, aad []byte) ([]byte, error) {
	minLen := 1 + 1 + nonceSize + s.aead.Overhead()
	if len(sealed) < minLen {
		return nil, ErrOpen
	}
	if sealed[0] != sealVersion {
		return nil, ErrOpen
	}
	if sealed[1] != s.keyID {
		return nil, ErrOpen
	}
	nonce := sealed[2 : 2+nonceSize]
	ct := sealed[2+nonceSize:]
	plain, err := s.aead.Open(nil, nonce, ct, aad)
	if err != nil {
		return nil, ErrOpen
	}
	return plain, nil
}
