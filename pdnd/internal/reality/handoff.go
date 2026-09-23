package reality

// This file is Pandora's handoff-only server boundary. The upstream REALITY
// Server function is a target-site mirror and intentionally waits for the
// mirror session to finish. NativeCore needs a different lifecycle: after the
// authenticated TLS 1.3 flight and client Finished are complete, it must own
// the live encrypted connection immediately. The wire cryptography remains
// the vendored REALITY implementation; routing and protocol ownership stay
// outside this package.

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/mlkem"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"net"
	"os"
	"sync"
	"time"

	"github.com/pires/go-proxyproto"
	"golang.org/x/crypto/curve25519"
	"golang.org/x/crypto/hkdf"
)

// ServerHandoff performs the REALITY server handshake and returns as soon as
// a client is authenticated. Invalid clients are closed instead of being
// silently handed to a different data plane.
func ServerHandoff(ctx context.Context, conn net.Conn, config *Config) (*Conn, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if conn == nil || config == nil || config.DialContext == nil {
		return nil, fmt.Errorf("REALITY: invalid handoff arguments")
	}
	target, err := config.DialContext(ctx, config.Type, config.Dest)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("REALITY: failed to dial dest: %w", err)
	}
	if config.Xver == 1 || config.Xver == 2 {
		if _, err := proxyproto.HeaderProxyFromAddrs(config.Xver, conn.RemoteAddr(), conn.LocalAddr()).WriteTo(target); err != nil {
			_ = target.Close()
			_ = conn.Close()
			return nil, fmt.Errorf("REALITY: failed to send PROXY protocol: %w", err)
		}
	} else if config.Xver != 0 {
		_ = target.Close()
		_ = conn.Close()
		return nil, fmt.Errorf("REALITY: unsupported PROXY protocol version %d", config.Xver)
	}

	mutex := new(sync.Mutex)
	handoff := &MirrorConn{Mutex: mutex, Conn: conn, Target: target}
	hs := serverHandshakeStateTLS13{
		c:   &Conn{conn: handoff, config: config},
		ctx: ctx,
	}

	mutex.Lock()
	hs.clientHello, _, err = hs.c.readClientHello(ctx)
	if err != nil {
		mutex.Unlock()
		_ = target.Close()
		_ = conn.Close()
		return nil, fmt.Errorf("REALITY: failed to read client hello: %w", err)
	}
	if hs.c.vers != VersionTLS13 || hs.clientHello == nil || !config.ServerNames[hs.clientHello.serverName] {
		mutex.Unlock()
		_ = target.Close()
		_ = conn.Close()
		return nil, fmt.Errorf("REALITY: client hello rejected")
	}
	if err := authenticateHandoff(&hs, config); err != nil {
		mutex.Unlock()
		_ = target.Close()
		_ = conn.Close()
		return nil, err
	}
	// From this point all TLS records must go directly to the client. The
	// mirror wrapper is only needed to forward the initial ClientHello.
	hs.c.conn = conn
	mutex.Unlock()

	if err := readTargetFlight(&hs, target, config); err != nil {
		_ = target.Close()
		_ = conn.Close()
		return nil, err
	}
	if err := hs.readClientFinished(); err != nil {
		_ = target.Close()
		_ = conn.Close()
		return nil, fmt.Errorf("REALITY: client Finished rejected: %w", err)
	}
	hs.c.isHandshakeComplete.Store(true)
	_ = target.Close()
	return hs.c, nil
}

// authenticateHandoff mirrors only the authentication portion of the
// upstream server path. The caller holds the MirrorConn mutex.
func authenticateHandoff(hs *serverHandshakeStateTLS13, config *Config) error {
	var peerPub []byte
	for _, keyShare := range hs.clientHello.keyShares {
		if keyShare.group == X25519 && len(keyShare.data) == 32 {
			peerPub = keyShare.data
			break
		}
	}
	if peerPub == nil {
		for _, keyShare := range hs.clientHello.keyShares {
			if keyShare.group == X25519MLKEM768 && len(keyShare.data) == mlkem.EncapsulationKeySize768+32 {
				peerPub = keyShare.data[mlkem.EncapsulationKeySize768:]
				break
			}
		}
	}
	if peerPub == nil {
		return fmt.Errorf("REALITY: client has no supported X25519 key share")
	}
	var err error
	if hs.c.AuthKey, err = curve25519.X25519(config.PrivateKey, peerPub); err != nil {
		return fmt.Errorf("REALITY: derive auth key: %w", err)
	}
	if _, err = hkdf.New(sha256.New, hs.c.AuthKey, hs.clientHello.random[:20], []byte("REALITY")).Read(hs.c.AuthKey); err != nil {
		return fmt.Errorf("REALITY: derive handshake key: %w", err)
	}
	block, err := aes.NewCipher(hs.c.AuthKey)
	if err != nil {
		return fmt.Errorf("REALITY: auth cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return fmt.Errorf("REALITY: auth AEAD: %w", err)
	}
	ciphertext := make([]byte, 32)
	plainText := make([]byte, 32)
	copy(ciphertext, hs.clientHello.sessionId)
	// Xray computes the AEAD AAD over the ClientHello with the plaintext
	// 32-byte session envelope in place, then replaces that field with the
	// ciphertext. Reconstruct that AAD explicitly instead of relying on slice
	// aliasing between sessionId and original (which is not guaranteed by the
	// parser and breaks QUIC's bare-handshake path).
	aad := append([]byte(nil), hs.clientHello.original...)
	if len(aad) >= 39+32 && len(hs.clientHello.sessionId) == 32 {
		copy(aad[39:39+32], plainText)
	}
	if _, err = aead.Open(plainText[:0], hs.clientHello.random[20:], ciphertext, aad); err != nil {
		return fmt.Errorf("REALITY: client authentication failed: %w", err)
	}
	copy(hs.clientHello.sessionId, ciphertext)
	copy(hs.c.ClientVer[:], plainText)
	hs.c.ClientTime = time.Unix(int64(binary.BigEndian.Uint32(plainText[4:])), 0)
	copy(hs.c.ClientShortId[:], plainText[8:])
	if config.MinClientVer != nil && Value(hs.c.ClientVer[:]...) < Value(config.MinClientVer...) {
		return fmt.Errorf("REALITY: client version below minimum")
	}
	if config.MaxClientVer != nil && Value(hs.c.ClientVer[:]...) > Value(config.MaxClientVer...) {
		return fmt.Errorf("REALITY: client version above maximum")
	}
	if config.MaxTimeDiff != 0 && time.Since(hs.c.ClientTime).Abs() > config.MaxTimeDiff {
		return fmt.Errorf("REALITY: client time skew exceeds limit")
	}
	if !config.ShortIds[hs.c.ClientShortId] {
		return fmt.Errorf("REALITY: client shortId rejected")
	}
	return nil
}

func readTargetFlight(hs *serverHandshakeStateTLS13, target net.Conn, config *Config) error {
	s2cSaved := make([]byte, 0, size)
	buf := make([]byte, size)
	handshakeLen := 0
	for {
		n, err := target.Read(buf)
		if n == 0 {
			if err != nil {
				return fmt.Errorf("REALITY: target handshake read: %w", err)
			}
			continue
		}
		s2cSaved = append(s2cSaved, buf[:n]...)
		if len(s2cSaved) > size {
			return fmt.Errorf("REALITY: target handshake too large")
		}
		complete := true
		// The target's NewSessionTicket is optional. Capture its record length
		// when it is already in the buffered flight so the native handshake can
		// mirror it, but never wait forever for a post-handshake ticket.
		for i := range types {
			if hs.c.out.handshakeLen[i] != 0 {
				continue
			}
			if i == 6 && len(s2cSaved) == 0 {
				break
			}
			complete = false
			if handshakeLen == 0 {
				if len(s2cSaved) <= recordHeaderLen {
					break
				}
				if Value(s2cSaved[1:3]...) != VersionTLS12 ||
					(i == 0 && (recordType(s2cSaved[0]) != recordTypeHandshake || s2cSaved[5] != typeServerHello)) ||
					(i == 1 && (recordType(s2cSaved[0]) != recordTypeChangeCipherSpec || s2cSaved[5] != 1)) ||
					(i > 1 && recordType(s2cSaved[0]) != recordTypeApplicationData) {
					return fmt.Errorf("REALITY: target returned an invalid TLS flight")
				}
				handshakeLen = recordHeaderLen + Value(s2cSaved[3:5]...)
			}
			if handshakeLen > size || (i == 1 && handshakeLen != 6) {
				return fmt.Errorf("REALITY: target TLS record length invalid")
			}
			if i == 2 && handshakeLen > 512 {
				hs.c.out.handshakeLen[i] = handshakeLen
				// Do not alias the scratch read buffer: subsequent target reads
				// reuse buf and would corrupt the synthesized QUIC handshake flight.
				hs.c.out.handshakeBuf = make([]byte, 0, size)
				break
			}
			if handshakeLen == 0 || len(s2cSaved) < handshakeLen {
				break
			}
			if i == 0 {
				hs.hello = new(serverHelloMsg)
				if !hs.hello.unmarshal(s2cSaved[recordHeaderLen:handshakeLen]) ||
					hs.hello.vers != VersionTLS12 || hs.hello.supportedVersion != VersionTLS13 ||
					cipherSuiteTLS13ByID(hs.hello.cipherSuite) == nil ||
					(!((hs.hello.serverShare.group == X25519 && len(hs.hello.serverShare.data) == 32) ||
						(hs.hello.serverShare.group == X25519MLKEM768 && len(hs.hello.serverShare.data) == mlkem.CiphertextSize768+32))) {
					return fmt.Errorf("REALITY: target ServerHello invalid")
				}
			}
			hs.c.out.handshakeLen[i] = handshakeLen
			s2cSaved = s2cSaved[handshakeLen:]
			handshakeLen = 0
		}
		// REALITY 握手失败时，客户端只看得到连接被断，服务端这头连一行
		// 日志都没有。这条按需打开的诊断给出唯一能定位问题的一组数字：
		// 七段握手记录各自的长度（0 表示还没识别到）、缓冲区剩余、
		// 以及是否判定为完整。默认零开销。
		if os.Getenv("REALITY_TRACE") != "" {
			fmt.Fprintf(os.Stderr, "[REALITY] read=%d saved=%d lens=%v complete=%v\n", n, len(s2cSaved), hs.c.out.handshakeLen, complete)
		}
		if complete || hs.c.out.handshakeLen[5] != 0 {
			if err := hs.handshake(); err != nil {
				return fmt.Errorf("REALITY: synthesize server handshake: %w", err)
			}
			return nil
		}
	}
}

// realityTraceEnabled / realityTracef 是按需打开的握手诊断。
// REALITY 握手失败时两头都拿不到可用信息，这是唯一的抓手。
var realityTraceEnabled = os.Getenv("REALITY_TRACE") != ""

func realityTracef(format string, args ...any) {
	if !realityTraceEnabled {
		return
	}
	fmt.Fprintf(os.Stderr, format+"\n", args...)
}
