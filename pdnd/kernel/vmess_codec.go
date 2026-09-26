// [INPUT]: 依赖标准库 crypto（aes / cipher / hmac / md5 / sha256）与 hash（fnv / crc 以外的摘要），依赖 golang.org/x/crypto 的 chacha20poly1305 与 sha3
// [OUTPUT]: 包内提供 VMess 正文的明文 / AEAD 分块读写器、vmessBodyAEAD、vmessCommandKey、vmessKDF、vmessOpen、vmessValidHash、vmessWriteResponse、aesGCM
// [POS]: kernel 的 VMess 编解码：从 vmess.go 拆出。正文按安全类型分块加解密，密钥与 nonce 由 VMess AEAD KDF 派生，响应头按请求密钥加密写回
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package kernel

import (
	"bufio"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/md5"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"hash"
	"hash/fnv"
	"io"
	"sync"

	"github.com/google/uuid"
	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/sha3"
)

type vmessBodyReader struct {
	reader           *bufio.Reader
	key, nonce       []byte
	security, option byte
	command          byte
	authID           [16]byte
	aead             *vmessAEADReader
	plainChunks      *vmessPlainChunkReader
}

func (r *vmessBodyReader) Read(p []byte) (int, error) {
	if r.security == vmessSecNone || r.security == vmessSecZero {
		if r.command == vmessUDP {
			if r.plainChunks == nil {
				r.plainChunks = &vmessPlainChunkReader{upstream: r.reader}
			}
			return r.plainChunks.Read(p)
		}
		return r.reader.Read(p)
	}
	if r.aead == nil {
		r.aead = newVMessAEADReader(r.reader, vmessBodyAEAD(r.security, r.key), r.nonce, r.option)
	}
	return r.aead.Read(p)
}

type vmessPlainChunkReader struct {
	upstream *bufio.Reader
	pending  []byte
}

func (r *vmessPlainChunkReader) Read(p []byte) (int, error) {
	if len(r.pending) > 0 {
		n := copy(p, r.pending)
		r.pending = r.pending[n:]
		return n, nil
	}
	var length [2]byte
	if _, err := io.ReadFull(r.upstream, length[:]); err != nil {
		return 0, err
	}
	n := int(binary.BigEndian.Uint16(length[:]))
	if n == 0 || n > 65535 {
		return 0, fmt.Errorf("vmess UDP chunk length %d invalid", n)
	}
	data := make([]byte, n)
	if _, err := io.ReadFull(r.upstream, data); err != nil {
		return 0, err
	}
	written := copy(p, data)
	if written < len(data) {
		r.pending = append(r.pending, data[written:]...)
	}
	return written, nil
}

type vmessPlainChunkWriter struct {
	mu       sync.Mutex
	upstream io.Writer
}

func (w *vmessPlainChunkWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(p) > 65535 {
		p = p[:65535]
	}
	var length [2]byte
	binary.BigEndian.PutUint16(length[:], uint16(len(p)))
	if _, err := w.upstream.Write(length[:]); err != nil {
		return 0, err
	}
	return w.upstream.Write(p)
}

type vmessAEADReader struct {
	upstream *bufio.Reader
	gcm      cipher.AEAD
	mask     sha3.ShakeHash
	nonce    [12]byte
	count    uint16
	pending  []byte
}

func newVMessAEADReader(upstream *bufio.Reader, aead cipher.AEAD, nonce []byte, option byte) *vmessAEADReader {
	var base [12]byte
	copy(base[:], nonce)
	var mask sha3.ShakeHash
	if option&vmessOptMask != 0 {
		mask = sha3.NewShake128()
		_, _ = mask.Write(nonce)
	}
	return &vmessAEADReader{upstream: upstream, gcm: aead, mask: mask, nonce: base}
}

func (r *vmessAEADReader) Read(p []byte) (int, error) {
	if len(r.pending) > 0 {
		n := copy(p, r.pending)
		r.pending = r.pending[n:]
		return n, nil
	}
	var rawLen [2]byte
	if _, err := io.ReadFull(r.upstream, rawLen[:]); err != nil {
		return 0, err
	}
	length := binary.BigEndian.Uint16(rawLen[:])
	if r.mask != nil {
		var maskCode uint16
		if err := binary.Read(r.mask, binary.BigEndian, &maskCode); err != nil {
			return 0, err
		}
		length ^= maskCode
	}
	if length < 16 || length > 65535 {
		return 0, fmt.Errorf("vmess AES chunk length %d invalid", length)
	}
	ciphertext := make([]byte, length)
	if _, err := io.ReadFull(r.upstream, ciphertext); err != nil {
		return 0, err
	}
	binary.BigEndian.PutUint16(r.nonce[:2], r.count)
	r.count++
	plaintext, err := r.gcm.Open(nil, r.nonce[:], ciphertext, nil)
	if err != nil {
		return 0, fmt.Errorf("vmess AES chunk authentication failed: %w", err)
	}
	if len(plaintext) == 0 {
		return 0, io.EOF
	}
	n := copy(p, plaintext)
	if n < len(plaintext) {
		r.pending = append(r.pending, plaintext[n:]...)
	}
	return n, nil
}

type vmessAEADWriter struct {
	mu       sync.Mutex
	upstream io.Writer
	gcm      cipher.AEAD
	mask     sha3.ShakeHash
	nonce    [12]byte
	count    uint16
}

func newVMessAEADWriter(upstream io.Writer, aead cipher.AEAD, nonce []byte, option byte) *vmessAEADWriter {
	var base [12]byte
	copy(base[:], nonce)
	var mask sha3.ShakeHash
	if option&vmessOptMask != 0 {
		mask = sha3.NewShake128()
		_, _ = mask.Write(nonce)
	}
	return &vmessAEADWriter{upstream: upstream, gcm: aead, mask: mask, nonce: base}
}

func (w *vmessAEADWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	written := 0
	for len(p) > 0 {
		chunk := p
		if len(chunk) > 15000 {
			chunk = chunk[:15000]
		}
		binary.BigEndian.PutUint16(w.nonce[:2], w.count)
		w.count++
		ciphertext := w.gcm.Seal(nil, w.nonce[:], chunk, nil)
		length := uint16(len(ciphertext))
		if w.mask != nil {
			var maskCode uint16
			if err := binary.Read(w.mask, binary.BigEndian, &maskCode); err != nil {
				return written, err
			}
			length ^= maskCode
		}
		var header [2]byte
		binary.BigEndian.PutUint16(header[:], length)
		if _, err := w.upstream.Write(header[:]); err != nil {
			return written, err
		}
		if _, err := w.upstream.Write(ciphertext); err != nil {
			return written, err
		}
		written += len(chunk)
		p = p[len(chunk):]
	}
	return written, nil
}

func vmessBodyAEAD(security byte, key []byte) cipher.AEAD {
	if security == vmessSecChaCha {
		derived := make([]byte, 32)
		first := md5.Sum(key)
		second := md5.Sum(first[:])
		copy(derived, first[:])
		copy(derived[16:], second[:])
		aead, _ := chacha20poly1305.New(derived)
		return aead
	}
	aead, _ := aesGCM(key)
	return aead
}

func vmessCommandKey(id uuid.UUID) [16]byte {
	h := md5.New()
	_, _ = h.Write(id[:])
	_, _ = h.Write([]byte("c48619fe-8f02-49e0-b9e9-edf763e17e21"))
	var out [16]byte
	h.Sum(out[:0])
	return out
}

func vmessKDF(key []byte, salt string, path ...[]byte) []byte {
	factory := func() hash.Hash { return hmac.New(sha256.New, vmessKDFRoot) }
	values := make([][]byte, 0, len(path)+1)
	values = append(values, []byte(salt))
	values = append(values, path...)
	for _, value := range values {
		parent := factory
		copied := append([]byte(nil), value...)
		factory = func() hash.Hash { return hmac.New(parent, copied) }
	}
	h := factory()
	_, _ = h.Write(key)
	return h.Sum(nil)
}

func vmessOpen(key, nonce, ciphertext, aad []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	g, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return g.Open(nil, nonce, ciphertext, aad)
}

func vmessValidHash(header []byte) bool {
	if len(header) < 4 {
		return false
	}
	h := fnv.New32a()
	_, _ = h.Write(header[:len(header)-4])
	return binary.BigEndian.Uint32(header[len(header)-4:]) == h.Sum32()
}

func vmessWriteResponse(w io.Writer, requestKey, requestNonce []byte, response, option byte) error {
	keyHash := sha256.Sum256(requestKey)
	nonceHash := sha256.Sum256(requestNonce)
	responseKey, responseNonce := keyHash[:16], nonceHash[:16]
	lengthKey := vmessKDF(responseKey, "AEAD Resp Header Len Key")[:16]
	lengthNonce := vmessKDF(responseNonce, "AEAD Resp Header Len IV")[:12]
	lengthCipher, err := aesGCM(lengthKey)
	if err != nil {
		return err
	}
	lengthPlain := []byte{0, 4}
	lengthCiphertext := lengthCipher.Seal(nil, lengthNonce, lengthPlain, nil)
	payloadKey := vmessKDF(responseKey, "AEAD Resp Header Key")[:16]
	payloadNonce := vmessKDF(responseNonce, "AEAD Resp Header IV")[:12]
	payloadCipher, err := aesGCM(payloadKey)
	if err != nil {
		return err
	}
	payload := payloadCipher.Seal(nil, payloadNonce, []byte{response, option, 0, 0}, nil)
	_, err = w.Write(append(lengthCiphertext, payload...))
	return err
}

func aesGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
