// [INPUT]: 依赖标准库 crypto/cipher，依赖 lukechampine.com/blake3 等密钥派生
// [OUTPUT]: 包内提供 ss2022Key、ss2022SessionKey、ss2022ValidateIdentityHeaders、ss2022IdentitySubkey、ss2022Stream 与分块读写、ss2022IncNonce
// [POS]: kernel 的 Shadowsocks 2022 密钥与 TCP 流：从 shadowsocks2022.go 拆出。会话密钥由 PSK 与 salt 派生，多用户时逐层校验身份头，TCP 正文按 AEAD 分块加解密
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package kernel

import (
	"bufio"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"fmt"
	"io"
	"sync"

	"lukechampine.com/blake3"
)

func ss2022Key(key []byte, length int) []byte {
	sum := sha256.Sum256(key)
	return append([]byte(nil), sum[:length]...)
}

func ss2022SessionKey(psk, salt []byte, length int) []byte {
	out := make([]byte, length)
	material := make([]byte, len(psk)+len(salt))
	copy(material, psk)
	copy(material[len(psk):], salt)
	blake3.DeriveKey(out, "shadowsocks 2022 session subkey", material)
	return out
}

func ss2022ValidateIdentityHeaders(wire, salt []byte, psks [][]byte) error {
	if len(wire) != (len(psks)-1)*aes.BlockSize {
		return fmt.Errorf("shadowsocks 2022 identity header length invalid")
	}
	for i := 0; i < len(psks)-1; i++ {
		block, err := aes.NewCipher(ss2022IdentitySubkey(psks[i], salt, len(psks[i])))
		if err != nil {
			return err
		}
		plain := make([]byte, aes.BlockSize)
		block.Decrypt(plain, wire[i*aes.BlockSize:(i+1)*aes.BlockSize])
		expectedHash := blake3.Sum512(psks[i+1])
		if subtle.ConstantTimeCompare(plain, expectedHash[:aes.BlockSize]) != 1 {
			return fmt.Errorf("shadowsocks 2022 identity header authentication failed")
		}
	}
	return nil
}

func ss2022IdentitySubkey(psk, salt []byte, length int) []byte {
	material := make([]byte, len(psk)+len(salt))
	copy(material, psk)
	copy(material[len(psk):], salt)
	out := make([]byte, length)
	blake3.DeriveKey(out, "shadowsocks 2022 identity subkey", material)
	return out
}

type ss2022Stream struct {
	reader                *bufio.Reader
	writer                io.Writer
	readAEAD, writeAEAD   cipher.AEAD
	readNonce, writeNonce []byte
	pending               []byte
	mu                    sync.Mutex
}

func ss2022ReadRawChunk(r io.Reader, aead cipher.AEAD, nonce []byte, dst []byte) error {
	wire := make([]byte, len(dst)+aead.Overhead())
	if _, err := io.ReadFull(r, wire); err != nil {
		return err
	}
	plain, err := aead.Open(nil, nonce, wire, nil)
	if err != nil {
		return fmt.Errorf("shadowsocks 2022 raw chunk authentication failed: %w", err)
	}
	if len(plain) != len(dst) {
		return fmt.Errorf("shadowsocks 2022 raw chunk length %d, want %d", len(plain), len(dst))
	}
	copy(dst, plain)
	return nil
}

func (s *ss2022Stream) Read(p []byte) (int, error) {
	if len(s.pending) > 0 {
		n := copy(p, s.pending)
		s.pending = s.pending[n:]
		return n, nil
	}
	var encryptedLength [2 + ss2022Overhead]byte
	if _, err := io.ReadFull(s.reader, encryptedLength[:]); err != nil {
		return 0, err
	}
	plainLength, err := s.readAEAD.Open(nil, s.readNonce, encryptedLength[:], nil)
	ss2022IncNonce(s.readNonce)
	if err != nil || len(plainLength) != 2 {
		return 0, fmt.Errorf("shadowsocks 2022 length authentication failed")
	}
	length := int(binary.BigEndian.Uint16(plainLength))
	if length == 0 || length > ss2022MaxChunk {
		return 0, fmt.Errorf("shadowsocks 2022 frame length invalid")
	}
	frame := make([]byte, length+ss2022Overhead)
	if _, err := io.ReadFull(s.reader, frame); err != nil {
		return 0, err
	}
	plain, err := s.readAEAD.Open(nil, s.readNonce, frame, nil)
	ss2022IncNonce(s.readNonce)
	if err != nil {
		return 0, fmt.Errorf("shadowsocks 2022 frame authentication failed")
	}
	n := copy(p, plain)
	if n < len(plain) {
		s.pending = append(s.pending, plain[n:]...)
	}
	return n, nil
}

func (s *ss2022Stream) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	written := 0
	for len(p) > 0 {
		chunk := p
		if len(chunk) > ss2022MaxChunk {
			chunk = chunk[:ss2022MaxChunk]
		}
		length := []byte{byte(len(chunk) >> 8), byte(len(chunk))}
		header := s.writeAEAD.Seal(nil, s.writeNonce, length, nil)
		ss2022IncNonce(s.writeNonce)
		body := s.writeAEAD.Seal(nil, s.writeNonce, chunk, nil)
		ss2022IncNonce(s.writeNonce)
		if _, err := s.writer.Write(append(header, body...)); err != nil {
			return written, err
		}
		written += len(chunk)
		p = p[len(chunk):]
	}
	return written, nil
}

func ss2022IncNonce(nonce []byte) {
	for i := range nonce {
		nonce[i]++
		if nonce[i] != 0 {
			return
		}
	}
}
