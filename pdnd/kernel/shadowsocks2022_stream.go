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
			return markConnError(connErrAuth, fmt.Errorf("shadowsocks 2022 identity header authentication failed"))
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
	pendingBuf            *[]byte
	mu                    sync.Mutex
}

func ss2022ReadRawChunk(r io.Reader, aead cipher.AEAD, nonce []byte, dst []byte) error {
	wire := make([]byte, len(dst)+aead.Overhead())
	if _, err := io.ReadFull(r, wire); err != nil {
		return err
	}
	plain, err := aead.Open(nil, nonce, wire, nil)
	if err != nil {
		// 请求头的第一块解不开就是 PSK 不对（或者根本不是 SS2022 流量），
		// 所以 AEAD 校验失败归 auth，读不满仍按底层 I/O 错误分类。
		return markConnError(connErrAuth, fmt.Errorf("shadowsocks 2022 raw chunk authentication failed: %w", err))
	}
	if len(plain) != len(dst) {
		return fmt.Errorf("shadowsocks 2022 raw chunk length %d, want %d", len(plain), len(dst))
	}
	copy(dst, plain)
	return nil
}

// Read / Write 与经典 AEAD 的 ssStream 同一套做法：装得下就原地解密，装不下借
// 池里的帧缓冲（frame_pool.go），写方向原地封装、一次写出，数据路径不逐块分配。
func (s *ss2022Stream) Read(p []byte) (int, error) {
	if len(s.pending) > 0 {
		n := copy(p, s.pending)
		s.pending = s.pending[n:]
		if len(s.pending) == 0 && s.pendingBuf != nil {
			putFrameBuf(s.pendingBuf)
			s.pendingBuf, s.pending = nil, nil
		}
		return n, nil
	}
	var encryptedLength [2 + ss2022Overhead]byte
	if _, err := io.ReadFull(s.reader, encryptedLength[:]); err != nil {
		return 0, err
	}
	plainLength, err := s.readAEAD.Open(encryptedLength[:0], s.readNonce, encryptedLength[:], nil)
	ss2022IncNonce(s.readNonce)
	if err != nil || len(plainLength) != 2 {
		return 0, fmt.Errorf("shadowsocks 2022 length authentication failed")
	}
	length := int(binary.BigEndian.Uint16(plainLength))
	if length == 0 || length > ss2022MaxChunk {
		return 0, fmt.Errorf("shadowsocks 2022 frame length invalid")
	}
	frameLen := length + ss2022Overhead
	var bp *[]byte
	frame := p
	if len(p) < frameLen {
		bp, frame = getFrameBuf(frameLen)
	}
	frame = frame[:frameLen]
	if _, err := io.ReadFull(s.reader, frame); err != nil {
		putFrameBuf(bp)
		return 0, err
	}
	plain, err := s.readAEAD.Open(frame[:0], s.readNonce, frame, nil)
	ss2022IncNonce(s.readNonce)
	if err != nil {
		putFrameBuf(bp)
		return 0, fmt.Errorf("shadowsocks 2022 frame authentication failed")
	}
	if bp == nil {
		return len(plain), nil
	}
	n := copy(p, plain)
	if n < len(plain) {
		s.pending, s.pendingBuf = plain[n:], bp
	} else {
		putFrameBuf(bp)
	}
	return n, nil
}

func (s *ss2022Stream) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	bp, _ := getFrameBuf(2 + 2*ss2022Overhead + ss2022MaxChunk)
	defer putFrameBuf(bp)
	written := 0
	for len(p) > 0 {
		chunk := p
		if len(chunk) > ss2022MaxChunk {
			chunk = chunk[:ss2022MaxChunk]
		}
		length := [2]byte{byte(len(chunk) >> 8), byte(len(chunk))}
		out := s.writeAEAD.Seal((*bp)[:0], s.writeNonce, length[:], nil)
		ss2022IncNonce(s.writeNonce)
		out = s.writeAEAD.Seal(out, s.writeNonce, chunk, nil)
		ss2022IncNonce(s.writeNonce)
		if _, err := s.writer.Write(out); err != nil {
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
