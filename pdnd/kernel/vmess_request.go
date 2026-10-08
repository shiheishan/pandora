package kernel

import (
	"bufio"
	"crypto/cipher"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"net"
	"net/netip"
	"strconv"
	"time"

	"github.com/aegispanel/nodeagent/core"
)

// errVMessUnsupportedRequest 标记「已通过认证、但请求的安全类型或选项不支持」：
// 对端是持有凭据的真实客户端，读完请求头就拒绝断开；按探测处理读满 10 秒只会
// 让它干等（审查 VMess 4）。
var errVMessUnsupportedRequest = errors.New("vmess request option unsupported")

func vmessDestinationUDPAddr(destination vmessDestination) (net.Addr, error) {
	if destination.IP.IsValid() {
		return &net.UDPAddr{IP: net.IP(destination.IP.AsSlice()), Port: int(destination.Port)}, nil
	}
	return net.ResolveUDPAddr("udp", net.JoinHostPort(destination.Domain, strconv.Itoa(int(destination.Port))))
}

type vmessDestination struct {
	Host, Domain string
	IP           netip.Addr
	Port         uint16
}

type vmessUserCandidate struct {
	user     core.User
	key      [16]byte
	keyBlock cipher.Block
}

func (a *vmessAdapter) readRequest(r *bufio.Reader) (core.User, vmessDestination, io.Reader, byte, error) {
	var candidates []vmessUserCandidate
	if snapshot := a.authCandidates.Load(); snapshot != nil {
		candidates = *snapshot
	}
	return readVMessRequestWithCandidates(r, candidates)
}

func readVMessRequestWithCandidates(r *bufio.Reader, candidates []vmessUserCandidate) (core.User, vmessDestination, io.Reader, byte, error) {
	// This indirection keeps the exported parser deterministic while allowing
	// the adapter to authenticate against a snapshot instead of a global map.
	var out vmessDestination
	var auth [16]byte
	if _, err := io.ReadFull(r, auth[:]); err != nil {
		return core.User{}, out, nil, 0, err
	}
	var user core.User
	var key [16]byte
	found := false
	now := time.Now().Unix()
	var plain [16]byte
	for _, candidate := range candidates {
		candidate.keyBlock.Decrypt(plain[:], auth[:])
		if binary.BigEndian.Uint32(plain[12:]) != crc32.ChecksumIEEE(plain[:12]) {
			continue
		}
		ts := int64(binary.BigEndian.Uint64(plain[:8]))
		if ts < now-120 || ts > now+120 {
			continue
		}
		user, key, found = candidate.user, candidate.key, true
		break
	}
	if !found {
		return core.User{}, out, nil, 0, markConnError(connErrAuth, fmt.Errorf("vmess auth id rejected"))
	}
	var lenCipher [18]byte
	if _, err := io.ReadFull(r, lenCipher[:]); err != nil {
		return core.User{}, out, nil, 0, err
	}
	var nonce [8]byte
	if _, err := io.ReadFull(r, nonce[:]); err != nil {
		return core.User{}, out, nil, 0, err
	}
	lengthPlain, err := vmessOpen(vmessKDF(key[:], "VMess Header AEAD Key_Length", auth[:], nonce[:])[:16], vmessKDF(key[:], "VMess Header AEAD Nonce_Length", auth[:], nonce[:])[:12], lenCipher[:], auth[:])
	if err != nil || len(lengthPlain) != 2 {
		return core.User{}, out, nil, 0, fmt.Errorf("vmess header length authentication failed")
	}
	headerLen := int(binary.BigEndian.Uint16(lengthPlain))
	if headerLen < 42 || headerLen > 4096 {
		return core.User{}, out, nil, 0, fmt.Errorf("vmess header length %d invalid", headerLen)
	}
	headerCipher := make([]byte, headerLen+16)
	if _, err := io.ReadFull(r, headerCipher); err != nil {
		return core.User{}, out, nil, 0, err
	}
	header, err := vmessOpen(vmessKDF(key[:], "VMess Header AEAD Key", auth[:], nonce[:])[:16], vmessKDF(key[:], "VMess Header AEAD Nonce", auth[:], nonce[:])[:12], headerCipher, auth[:])
	if err != nil {
		return core.User{}, out, nil, 0, fmt.Errorf("vmess header authentication failed")
	}
	if len(header) != headerLen || header[0] != vmessVersion || (header[37] != vmessTCP && header[37] != vmessUDP && header[37] != vmessMux) {
		return core.User{}, out, nil, 0, fmt.Errorf("vmess header version or command unsupported")
	}
	if !vmessValidHash(header) {
		return core.User{}, out, nil, 0, fmt.Errorf("vmess header checksum invalid")
	}
	security := header[35] & 0x0f
	if security != vmessSecNone && security != vmessSecZero && security != vmessSecAES128 && security != vmessSecChaCha {
		return core.User{}, out, nil, security, fmt.Errorf("%w: vmess security %d unsupported", errVMessUnsupportedRequest, security)
	}
	option := header[34]
	if (security == vmessSecAES128 || security == vmessSecChaCha) && option&vmessOptChunk == 0 {
		return core.User{}, out, nil, security, fmt.Errorf("%w: vmess AES-GCM requires chunk framing", errVMessUnsupportedRequest)
	}
	if option&vmessOptAuthLength != 0 {
		return core.User{}, out, nil, security, fmt.Errorf("%w: vmess AuthenticatedLength (experimental) is not supported", errVMessUnsupportedRequest)
	}
	if option&vmessOptPadding != 0 && option&vmessOptMask == 0 {
		// Xray 同样拒绝：填充长度取自掩码流，没有掩码就没有填充长度。
		return core.User{}, out, nil, security, fmt.Errorf("%w: vmess GlobalPadding requires ChunkMasking", errVMessUnsupportedRequest)
	}
	// none / zero：不分块（option 0，zero 与 sing-box 的 none）或 Xray 的 none 分块
	// （ChunkStream + ChunkMasking，UDP 另带 GlobalPadding 时也收）。掩码、填充离开
	// 分块没有意义，拒绝。
	if (security == vmessSecNone || security == vmessSecZero) && option&vmessOptChunk == 0 && option != 0 {
		return core.User{}, out, nil, security, fmt.Errorf("%w: vmess chunk options require ChunkStream", errVMessUnsupportedRequest)
	}
	if (security == vmessSecNone || security == vmessSecZero) && header[37] == vmessUDP && option&vmessOptChunk == 0 {
		return core.User{}, out, nil, security, fmt.Errorf("%w: vmess UDP requires chunk framing", errVMessUnsupportedRequest)
	}
	pos := 38
	if header[37] == vmessMux {
		out.Domain, out.Host, out.Port = "v1.mux.cool", "v1.mux.cool", 666
	} else {
		if pos+3 > len(header) {
			return core.User{}, out, nil, 0, io.ErrUnexpectedEOF
		}
		out.Port = binary.BigEndian.Uint16(header[pos:])
		pos += 2
		if out.Port == 0 {
			return core.User{}, out, nil, 0, fmt.Errorf("vmess destination port invalid")
		}
		switch header[pos] {
		case 1:
			pos++
			if pos+4 > len(header) {
				return core.User{}, out, nil, 0, io.ErrUnexpectedEOF
			}
			var ip4 [4]byte
			copy(ip4[:], header[pos:pos+4])
			out.IP = netip.AddrFrom4(ip4)
			out.Host = out.IP.String()
		case 2:
			pos++
			if pos >= len(header) || header[pos] == 0 || header[pos] > 253 {
				return core.User{}, out, nil, 0, fmt.Errorf("vmess domain length invalid")
			}
			n := int(header[pos])
			pos++
			if pos+n > len(header) {
				return core.User{}, out, nil, 0, io.ErrUnexpectedEOF
			}
			out.Domain = string(header[pos : pos+n])
			out.Host = out.Domain
		case 3:
			pos++
			if pos+16 > len(header) {
				return core.User{}, out, nil, 0, io.ErrUnexpectedEOF
			}
			var ip6 [16]byte
			copy(ip6[:], header[pos:pos+16])
			out.IP = netip.AddrFrom16(ip6)
			out.Host = out.IP.String()
		default:
			return core.User{}, out, nil, 0, fmt.Errorf("vmess address type unsupported")
		}
	}
	var authID [16]byte
	copy(authID[:], auth[:])
	return user, out, &vmessBodyReader{reader: r, key: append([]byte(nil), header[17:33]...), nonce: append([]byte(nil), header[1:17]...), security: security, option: header[34], command: header[37], respHeader: header[33], authID: authID}, security, nil
}
