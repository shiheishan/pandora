package kernel

import (
	"bufio"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/md5"
	"crypto/rand"
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
	// respHeader 是请求头里的 V 字节（header[33]），响应头首字节必须原样回显：
	// Xray 客户端逐字节核对，对不上就报 unexpected response header 断开。
	respHeader byte
	authID     [16]byte
	chunks     *vmessAEADReader
}

// chunked 表示请求体是分块的。aes / chacha 恒分块；none 由 ChunkStream 选项决定
// （Xray 的 none 开 ChunkStream + ChunkMasking，zero 与 sing-box 的 none 不分块）。
func (r *vmessBodyReader) chunked() bool {
	return r.security == vmessSecAES128 || r.security == vmessSecChaCha || r.option&vmessOptChunk != 0
}

// packet 对应 Xray 的 TransferTypePacket：UDP 每块就是一个包。
func (r *vmessBodyReader) packet() bool { return r.command == vmessUDP }

func (r *vmessBodyReader) Read(p []byte) (int, error) {
	if !r.chunked() {
		return r.reader.Read(p)
	}
	if r.chunks == nil {
		var aead cipher.AEAD
		if r.security == vmessSecAES128 || r.security == vmessSecChaCha {
			aead = vmessBodyAEAD(r.security, r.key)
		}
		r.chunks = newVMessChunkReader(r.reader, aead, r.nonce, r.option, r.packet())
	}
	return r.chunks.Read(p)
}

// writeResponse 写 AEAD 响应头，首字节回显请求的 V 字节。
func (r *vmessBodyReader) writeResponse(w io.Writer) error {
	return vmessWriteResponse(w, r.key, r.nonce, r.respHeader, r.option)
}

// responseWriter 按请求的安全类型与选项包装响应体写端：响应体的密钥与 IV 是
// 请求体密钥与 IV 的 SHA-256 前 16 字节，分块、掩码、填充选项与请求一致。
func (r *vmessBodyReader) responseWriter(w io.Writer) io.Writer {
	if !r.chunked() {
		return w
	}
	keyHash := sha256.Sum256(r.key)
	nonceHash := sha256.Sum256(r.nonce)
	var aead cipher.AEAD
	if r.security == vmessSecAES128 || r.security == vmessSecChaCha {
		aead = vmessBodyAEAD(r.security, keyHash[:16])
	}
	return newVMessChunkWriter(w, aead, nonceHash[:16], r.option, r.packet())
}

// VMess 分块的数据路径不再逐块分配（1c1g 实测 vmess 的加解密每个数据块都要
// 分配）：调用方缓冲装得下整块时读进去原地解密；装不下才借池里的块缓冲，
// 剩余明文留在借来的缓冲里、取完即还。写方向在池里的缓冲上原地封装，长度头
// 与密文一次写出（原先两次 Write，TLS 上就是两个记录）。

// VMess 分块（Xray common/crypto 的 AuthenticationReader / ChunkStreamReader）：
//
//	[2 字节长度][密文（长度 - 填充）][填充]
//
// 长度可被 SHAKE128(IV) 流掩码（ChunkMasking）；GlobalPadding 时每块末尾追加
// 0–63 字节随机填充，填充长度取自同一条 SHAKE 流，且先于长度掩码取（读写两侧
// 次序必须与 Xray 一致，错一次后面全错）。aead 为 nil 是 none 安全类型的分块：
// 没有认证标签，TCP 不加填充（ChunkStream），UDP 按包加填充（NoOp 认证器）。
// 长度等于「标签 + 填充」即空块，表示对端写完。
type vmessAEADReader struct {
	upstream   *bufio.Reader
	gcm        cipher.AEAD
	mask       sha3.ShakeHash
	padding    bool
	nonce      [12]byte
	count      uint16
	pending    []byte
	pendingBuf *[]byte
}

func newVMessAEADReader(upstream *bufio.Reader, aead cipher.AEAD, nonce []byte, option byte) *vmessAEADReader {
	return newVMessChunkReader(upstream, aead, nonce, option, false)
}

func newVMessChunkReader(upstream *bufio.Reader, aead cipher.AEAD, nonce []byte, option byte, packet bool) *vmessAEADReader {
	var base [12]byte
	copy(base[:], nonce)
	var mask sha3.ShakeHash
	if option&vmessOptMask != 0 {
		mask = sha3.NewShake128()
		_, _ = mask.Write(nonce)
	}
	padding := mask != nil && option&vmessOptPadding != 0 && (aead != nil || packet)
	return &vmessAEADReader{upstream: upstream, gcm: aead, mask: mask, padding: padding, nonce: base}
}

// nextMask 取下一个 16 位长度掩码（不经 binary.Read，免一次分配）。
func nextVMessMask(mask sha3.ShakeHash) (uint16, error) {
	var b [2]byte
	if _, err := io.ReadFull(mask, b[:]); err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint16(b[:]), nil
}

// vmessMaxPadding 是 GlobalPadding 每块填充长度的上界（Xray ShakeSizeParser.MaxPaddingLen）。
const vmessMaxPadding = 64

func (r *vmessAEADReader) overhead() int {
	if r.gcm == nil {
		return 0
	}
	return r.gcm.Overhead()
}

func (r *vmessAEADReader) Read(p []byte) (int, error) {
	if len(r.pending) > 0 {
		n := copy(p, r.pending)
		r.pending = r.pending[n:]
		if len(r.pending) == 0 && r.pendingBuf != nil {
			putFrameBuf(r.pendingBuf)
			r.pendingBuf, r.pending = nil, nil
		}
		return n, nil
	}
	var rawLen [2]byte
	if _, err := io.ReadFull(r.upstream, rawLen[:]); err != nil {
		return 0, err
	}
	paddingLen := 0
	if r.padding {
		next, err := nextVMessMask(r.mask)
		if err != nil {
			return 0, err
		}
		paddingLen = int(next % vmessMaxPadding)
	}
	length := binary.BigEndian.Uint16(rawLen[:])
	if r.mask != nil {
		maskCode, err := nextVMessMask(r.mask)
		if err != nil {
			return 0, err
		}
		length ^= maskCode
	}
	if int(length) == r.overhead()+paddingLen {
		// 空块：对端写完。填充照样在线上，读掉免得留给下一个读者。
		if paddingLen > 0 {
			if _, err := r.upstream.Discard(paddingLen); err != nil {
				return 0, err
			}
		}
		return 0, io.EOF
	}
	if int(length) < r.overhead()+paddingLen {
		return 0, fmt.Errorf("vmess chunk length %d invalid", length)
	}
	var bp *[]byte
	ciphertext := p
	if len(p) < int(length) {
		bp, ciphertext = getFrameBuf(int(length))
	}
	ciphertext = ciphertext[:length]
	if _, err := io.ReadFull(r.upstream, ciphertext); err != nil {
		if bp != nil {
			putFrameBuf(bp)
		}
		return 0, err
	}
	ciphertext = ciphertext[:int(length)-paddingLen]
	plaintext := ciphertext
	if r.gcm != nil {
		binary.BigEndian.PutUint16(r.nonce[:2], r.count)
		r.count++
		var err error
		plaintext, err = r.gcm.Open(ciphertext[:0], r.nonce[:], ciphertext, nil)
		if err != nil {
			if bp != nil {
				putFrameBuf(bp)
			}
			return 0, fmt.Errorf("vmess AES chunk authentication failed: %w", err)
		}
	}
	if bp == nil {
		return len(plaintext), nil
	}
	n := copy(p, plaintext)
	if n < len(plaintext) {
		r.pending, r.pendingBuf = plaintext[n:], bp
	} else {
		putFrameBuf(bp)
	}
	return n, nil
}

type vmessAEADWriter struct {
	mu       sync.Mutex
	upstream io.Writer
	gcm      cipher.AEAD
	mask     sha3.ShakeHash
	padding  bool
	packet   bool
	nonce    [12]byte
	count    uint16
}

func newVMessAEADWriter(upstream io.Writer, aead cipher.AEAD, nonce []byte, option byte) *vmessAEADWriter {
	return newVMessChunkWriter(upstream, aead, nonce, option, false)
}

func newVMessChunkWriter(upstream io.Writer, aead cipher.AEAD, nonce []byte, option byte, packet bool) *vmessAEADWriter {
	var base [12]byte
	copy(base[:], nonce)
	var mask sha3.ShakeHash
	if option&vmessOptMask != 0 {
		mask = sha3.NewShake128()
		_, _ = mask.Write(nonce)
	}
	padding := mask != nil && option&vmessOptPadding != 0 && (aead != nil || packet)
	return &vmessAEADWriter{upstream: upstream, gcm: aead, mask: mask, padding: padding, packet: packet, nonce: base}
}

// vmessStreamChunk 是流式分块的单块明文上限；UDP 一个包一块，上限是长度字段能
// 表示的最大值减去标签与填充。
const (
	vmessStreamChunk = 15000
	vmessPacketChunk = 65535 - 16 - vmessMaxPadding
)

func (w *vmessAEADWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	limit := vmessStreamChunk
	if w.packet {
		limit = vmessPacketChunk
	}
	overhead := 0
	if w.gcm != nil {
		overhead = w.gcm.Overhead()
	}
	bp, _ := getFrameBuf(2 + limit + overhead + vmessMaxPadding)
	defer putFrameBuf(bp)
	written := 0
	for len(p) > 0 {
		chunk := p
		if len(chunk) > limit {
			chunk = chunk[:limit]
		}
		out := (*bp)[:2]
		if w.gcm != nil {
			binary.BigEndian.PutUint16(w.nonce[:2], w.count)
			w.count++
			out = w.gcm.Seal(out, w.nonce[:], chunk, nil)
		} else {
			out = append(out, chunk...)
		}
		paddingLen := 0
		if w.padding {
			next, err := nextVMessMask(w.mask)
			if err != nil {
				return written, err
			}
			paddingLen = int(next % vmessMaxPadding)
		}
		length := uint16(len(out) - 2 + paddingLen)
		if w.mask != nil {
			maskCode, err := nextVMessMask(w.mask)
			if err != nil {
				return written, err
			}
			length ^= maskCode
		}
		binary.BigEndian.PutUint16(out[:2], length)
		if paddingLen > 0 {
			// 填充明文上线：用密码学随机数，免得泄露 PRNG 状态（Xray 同此）。
			start := len(out)
			out = out[:start+paddingLen]
			if _, err := rand.Read(out[start:]); err != nil {
				return written, err
			}
		}
		if _, err := w.upstream.Write(out); err != nil {
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
