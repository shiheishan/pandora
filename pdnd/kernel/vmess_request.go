// [INPUT]: 依赖 vmess.go 的 vmessAdapter 与用户表，依赖 vmess_codec.go 的 KDF、AEAD 打开与头部校验
// [OUTPUT]: 包内提供 vmessDestination、vmessUserCandidate、readRequest、readVMessRequestWithCandidates、vmessDestinationUDPAddr
// [POS]: kernel 的 VMess 请求头解析：从 vmess.go 拆出。按 AuthID 在候选用户里定位、解开 AEAD 请求头并校验，得到用户、目的地址、正文读取器与命令
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package kernel

import (
	"bufio"
	"crypto/aes"
	"crypto/cipher"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"io"
	"net"
	"net/netip"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/aegispanel/nodeagent/core"
)

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
	a.mu.RLock()
	candidates := make([]vmessUserCandidate, 0, len(a.users))
	for id, u := range a.users {
		_, err := uuid.Parse(id)
		if err != nil {
			continue
		}
		block, err := aes.NewCipher(vmessKDF(u.key[:], "AES Auth ID Encryption")[:16])
		if err == nil {
			candidates = append(candidates, vmessUserCandidate{user: core.User{ID: u.ID, UUID: id, DeviceLimit: u.DeviceLimit, SpeedLimit: u.SpeedLimit}, key: u.key, keyBlock: block})
		}
	}
	a.mu.RUnlock()
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
	for _, candidate := range candidates {
		plain := make([]byte, 16)
		candidate.keyBlock.Decrypt(plain, auth[:])
		if binary.BigEndian.Uint32(plain[12:]) != crc32.ChecksumIEEE(plain[:12]) {
			continue
		}
		ts := int64(binary.BigEndian.Uint64(plain[:8]))
		now := time.Now().Unix()
		if ts < now-120 || ts > now+120 {
			continue
		}
		user, key, found = candidate.user, candidate.key, true
		break
	}
	if !found {
		return core.User{}, out, nil, 0, fmt.Errorf("vmess auth id rejected")
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
		return core.User{}, out, nil, security, fmt.Errorf("vmess security %d unsupported", security)
	}
	if (security == vmessSecAES128 || security == vmessSecChaCha) && header[34]&vmessOptChunk == 0 {
		return core.User{}, out, nil, security, fmt.Errorf("vmess AES-GCM requires chunk framing")
	}
	if (security == vmessSecNone || security == vmessSecZero) && header[37] == vmessTCP && header[34] != 0 {
		return core.User{}, out, nil, security, fmt.Errorf("vmess chunk options require an authenticated body security mode")
	}
	if (security == vmessSecNone || security == vmessSecZero) && header[37] == vmessUDP && header[34] != vmessOptChunk {
		return core.User{}, out, nil, security, fmt.Errorf("vmess UDP requires plain chunk framing")
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
	return user, out, &vmessBodyReader{reader: r, key: append([]byte(nil), header[17:33]...), nonce: append([]byte(nil), header[1:17]...), security: security, option: header[34], command: header[37], authID: authID}, security, nil
}
