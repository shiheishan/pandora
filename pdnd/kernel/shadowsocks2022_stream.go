package kernel

import (
	"crypto/cipher"
	"errors"
	"io"
	"net"
	"sync"
	"time"

	"lukechampine.com/blake3"
)

// ss2022SessionKey 派生会话子密钥：blake3.DeriveKey("shadowsocks 2022 session subkey", psk || salt)。
func ss2022SessionKey(psk, salt []byte, length int) []byte {
	out := make([]byte, length)
	material := make([]byte, len(psk)+len(salt))
	copy(material, psk)
	copy(material[len(psk):], salt)
	blake3.DeriveKey(out, "shadowsocks 2022 session subkey", material)
	return out
}

// ss2022IdentitySubkey 派生身份子密钥：blake3.DeriveKey("shadowsocks 2022 identity subkey", psk || salt)。
func ss2022IdentitySubkey(psk, salt []byte, length int) []byte {
	material := make([]byte, len(psk)+len(salt))
	copy(material, psk)
	copy(material[len(psk):], salt)
	out := make([]byte, length)
	blake3.DeriveKey(out, "shadowsocks 2022 identity subkey", material)
	return out
}

// ss2022Conn 实现 SIP022 TCP 会话流，包装底层 net.Conn。
//
// 响应头懒发设计理由：
// 主流服务端（sing-box、shadowsocks-rust）都把首块数据放进响应固定头，客户端也照这个读；
// 原先先发一个空块再发数据，对按长度 0 不再读块的客户端会错位。
// respSalt 非 nil 表示响应头还没发出；第一次 Write 把 salt、固定头与首块数据一次合并写出；
// 如果没写过数据就 CloseWrite，则发送长度为 0 的固定头并补一个认证空块。
type ss2022Conn struct {
	conn       net.Conn
	readAEAD   cipher.AEAD
	readNonce  uint64
	nonce      [12]byte
	pending    []byte  // 已解密、调用方还没取走的明文
	pendingBuf *[]byte // 非 nil 时 pending 指向这块池化缓冲（frame_pool.go），取完即还

	writeMu       sync.Mutex
	writeAEAD     cipher.AEAD
	writeNonce    uint64
	writeNonceBuf [12]byte
	keyLen        int
	// 响应头懒发（SIP022 的响应固定头里带首块数据长度）：respSalt 非 nil 表示还没发，
	// 第一次 Write 把 salt、固定头与首块数据一次写出；没写过数据就 CloseWrite 时发长度 0 的固定头加一个空块。
	respSalt []byte
	reqSalt  []byte
}

func (s *ss2022Conn) Read(p []byte) (int, error) {
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
	if _, err := io.ReadFull(s.conn, encryptedLength[:]); err != nil {
		return 0, err
	}
	plainLength, err := s.readAEAD.Open(encryptedLength[:0], fillSSNonce(&s.nonce, s.readNonce), encryptedLength[:], nil)
	s.readNonce++
	if err != nil || len(plainLength) != 2 {
		return 0, errors.New("shadowsocks 2022 frame length authentication failed")
	}
	length := int(uint16(plainLength[0])<<8 | uint16(plainLength[1]))
	if length == 0 || length > ss2022MaxReadChunk {
		return 0, errors.New("shadowsocks 2022 frame length invalid")
	}
	frameLen := length + ss2022Overhead
	bp, frame := getFrameBuf(frameLen)
	if _, err := io.ReadFull(s.conn, frame); err != nil {
		putFrameBuf(bp)
		return 0, err
	}
	plain, err := s.readAEAD.Open(frame[:0], fillSSNonce(&s.nonce, s.readNonce), frame, nil)
	s.readNonce++
	if err != nil {
		putFrameBuf(bp)
		return 0, errors.New("shadowsocks 2022 frame authentication failed")
	}
	n := copy(p, plain)
	if n < len(plain) {
		s.pending = plain[n:]
		s.pendingBuf = bp
	} else {
		putFrameBuf(bp)
	}
	return n, nil
}

func (s *ss2022Conn) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	written := 0
	if s.respSalt != nil {
		firstChunkLen := min(len(p), ss2022WriteChunk)
		first := p[:firstChunkLen]
		if err := s.writeHeaderLocked(first); err != nil {
			return 0, err
		}
		written = firstChunkLen
		p = p[firstChunkLen:]
	}

	for len(p) > 0 {
		chunk := p
		if len(chunk) > ss2022WriteChunk {
			chunk = chunk[:ss2022WriteChunk]
		}
		needed := 2 + ss2022Overhead + len(chunk) + ss2022Overhead
		bp, buf := getFrameBuf(needed)
		length := [2]byte{byte(len(chunk) >> 8), byte(len(chunk))}
		out := s.writeAEAD.Seal(buf[:0], fillSSNonce(&s.writeNonceBuf, s.writeNonce), length[:], nil)
		s.writeNonce++
		out = s.writeAEAD.Seal(out, fillSSNonce(&s.writeNonceBuf, s.writeNonce), chunk, nil)
		s.writeNonce++
		_, err := s.conn.Write(out)
		putFrameBuf(bp)
		if err != nil {
			return written, err
		}
		written += len(chunk)
		p = p[len(chunk):]
	}
	return written, nil
}

// writeHeaderLocked 封装响应头与首块数据。调用方持有 writeMu。
func (s *ss2022Conn) writeHeaderLocked(first []byte) error {
	// 响应固定头长度 = 1 (type) + 8 (timestamp) + keyLen (reqSalt) + 2 (len(first))
	fixedPlainLen := 1 + 8 + s.keyLen + 2
	needed := s.keyLen + fixedPlainLen + ss2022Overhead + len(first) + ss2022Overhead
	bp, buf := getFrameBuf(needed)
	defer putFrameBuf(bp)

	// 1. response salt
	out := append(buf[:0], s.respSalt...)

	// 2. response fixed header: server_header(1) | timestamp(8) | req_salt(keyLen) | first_len(2)
	fixedPlain := make([]byte, fixedPlainLen)
	fixedPlain[0] = ss2022ServerHeader
	nowUnix := uint64(timeNowUnix())
	fixedPlain[1] = byte(nowUnix >> 56)
	fixedPlain[2] = byte(nowUnix >> 48)
	fixedPlain[3] = byte(nowUnix >> 40)
	fixedPlain[4] = byte(nowUnix >> 32)
	fixedPlain[5] = byte(nowUnix >> 24)
	fixedPlain[6] = byte(nowUnix >> 16)
	fixedPlain[7] = byte(nowUnix >> 8)
	fixedPlain[8] = byte(nowUnix)
	copy(fixedPlain[9:9+s.keyLen], s.reqSalt)
	fixedPlain[9+s.keyLen] = byte(len(first) >> 8)
	fixedPlain[10+s.keyLen] = byte(len(first))

	out = s.writeAEAD.Seal(out, fillSSNonce(&s.writeNonceBuf, s.writeNonce), fixedPlain, nil)
	s.writeNonce++

	// 3. first payload chunk: 即使长度为 0 也封装一个只有标签的空块
	// sing-shadowsocks 客户端总按声明长度读一块，长度 0 也读 16 字节标签
	out = s.writeAEAD.Seal(out, fillSSNonce(&s.writeNonceBuf, s.writeNonce), first, nil)
	s.writeNonce++

	if _, err := s.conn.Write(out); err != nil {
		return err
	}
	s.respSalt, s.reqSalt = nil, nil
	return nil
}

func (s *ss2022Conn) CloseWrite() error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if s.respSalt != nil {
		if err := s.writeHeaderLocked(nil); err != nil {
			return err
		}
	}
	if cw, ok := s.conn.(interface{ CloseWrite() error }); ok {
		return cw.CloseWrite()
	}
	return nil
}

func (s *ss2022Conn) Close() error {
	return s.conn.Close()
}

func (s *ss2022Conn) LocalAddr() net.Addr                { return s.conn.LocalAddr() }
func (s *ss2022Conn) RemoteAddr() net.Addr               { return s.conn.RemoteAddr() }
func (s *ss2022Conn) SetDeadline(t time.Time) error      { return s.conn.SetDeadline(t) }
func (s *ss2022Conn) SetReadDeadline(t time.Time) error  { return s.conn.SetReadDeadline(t) }
func (s *ss2022Conn) SetWriteDeadline(t time.Time) error { return s.conn.SetWriteDeadline(t) }

func timeNowUnix() int64 {
	return time.Now().Unix()
}
