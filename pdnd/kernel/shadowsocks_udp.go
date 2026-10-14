package kernel

import (
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"sync"

	M "github.com/sagernet/sing/common/metadata"
)

// ssUDPMaxPacket 是读循环的收包缓冲（一个 UDP 包的上限）。
const ssUDPMaxPacket = 65535

// ssUDPEncodeBufSize 是编一个下行包要的最大缓冲：salt（≤32）+ IPv6 地址头 19 + 负载 + 标签 16。
const ssUDPEncodeBufSize = 32 + 19 + ssUDPMaxPacket + 16

// ssUDPEncodePool 是下行编码缓冲：只在 WritePacket 期间借用，空闲会话不常驻。
var ssUDPEncodePool = sync.Pool{New: func() any {
	b := make([]byte, ssUDPEncodeBufSize)
	return &b
}}

// packetLoop 是入站唯一的 UDP 读循环：解密、认证、交给会话，上行在这里同步写上游
// （非阻塞 UDP 写），不按包起 goroutine。wire、plain、found 归本循环独占。
func (a *shadowsocksAdapter) packetLoop() {
	defer a.wg.Done()
	wire := make([]byte, ssUDPMaxPacket)
	plain := make([]byte, ssUDPMaxPacket)
	var found []*ssUDPSession
	for {
		n, from, err := a.udpConn.ReadFromUDPAddrPort(wire)
		if err != nil {
			if a.ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return
			}
			continue
		}
		from = netip.AddrPortFrom(from.Addr().Unmap(), from.Port())
		if err := a.handleUDPPacket(wire[:n], plain, &found, from); err != nil {
			a.connErr.addr(StageSession, net.UDPAddrFromAddrPort(from), err)
		}
	}
}

// handleUDPPacket 处理一个客户端包。快路径：来自已有会话地址的包先用该会话的用户
// 试解；解不开（或会话的用户记录已作废）再走完整的 findSSUser，认证通过后找到或
// 新建 (地址, 用户) 会话。明文写进 plain（不能原地解：GCM 失败会清零 dst，换下一个
// 用户就没有密文了），payload 指向 plain，forwardUDP 同步写完上游后不再引用。
func (a *shadowsocksAdapter) handleUDPPacket(wire, plain []byte, found *[]*ssUDPSession, from netip.AddrPort) error {
	if len(wire) < a.method.SaltLen+16 {
		return markConnError(connErrAuth, fmt.Errorf("shadowsocks UDP packet too short"))
	}
	salt := wire[:a.method.SaltLen]
	ciphertext := wire[a.method.SaltLen:]
	*found = a.lookupUDPSessions(from, (*found)[:0])
	for _, s := range *found {
		if s.user.retired.Load() {
			continue
		}
		sc := getSSScratch()
		dest, payload, ok := a.openUDPWith(sc, s.user, salt, ciphertext, plain)
		putSSScratch(sc)
		if ok {
			return a.forwardUDP(s, dest, payload)
		}
	}
	epoch := a.sessions.epoch()
	user, dest, payload, err := a.decodeUDPPacket(salt, ciphertext, plain, from)
	if err != nil {
		return markConnError(connErrAuth, fmt.Errorf("shadowsocks UDP user authentication failed"))
	}
	var sess *ssUDPSession
	*found = a.lookupUDPSessions(from, (*found)[:0])
	for _, s := range *found {
		if s.user == user {
			sess = s
			break
		}
	}
	if sess == nil {
		sess, err = a.openUDPSession(from, user, epoch, dest)
		if err != nil {
			return err
		}
	}
	return a.forwardUDP(sess, dest, payload)
}

func (a *shadowsocksAdapter) openUDPWith(sc *ssScratch, user *ssUser, salt, ciphertext, plain []byte) (vlessDestination, []byte, bool) {
	sc.setSalt(salt)
	aead, err := a.method.NewAEAD(sc.subkey(user.MasterKey, a.method.KeyLen))
	if err != nil {
		return vlessDestination{}, nil, false
	}
	if cap(plain) < len(ciphertext) {
		return vlessDestination{}, nil, false
	}
	opened, err := aead.Open(plain[:0], sc.zeroNonce(), ciphertext, nil)
	if err != nil {
		return vlessDestination{}, nil, false
	}
	var dest vlessDestination
	consumed, err := parseSSDestination(opened, &dest)
	if err != nil {
		return vlessDestination{}, nil, false
	}
	return dest, opened[consumed:], true
}

func (a *shadowsocksAdapter) decodeUDPPacket(salt, ciphertext, plain []byte, from netip.AddrPort) (*ssUser, vlessDestination, []byte, error) {
	var dest vlessDestination
	sc := getSSScratch()
	defer putSSScratch(sc)
	var payload []byte
	selected, _ := a.findSSUser(sc, salt, ssSourceKey(net.UDPAddrFromAddrPort(from)), func(aead cipher.AEAD) bool {
		if cap(plain) < len(ciphertext) {
			return false
		}
		opened, err := aead.Open(plain[:0], sc.zeroNonce(), ciphertext, nil)
		if err != nil {
			return false
		}
		var candidate vlessDestination
		consumed, err := parseSSDestination(opened, &candidate)
		if err != nil {
			return false
		}
		dest, payload = candidate, opened[consumed:]
		return true
	})
	if selected == nil {
		return nil, dest, nil, fmt.Errorf("auth failed")
	}
	return selected, dest, payload, nil
}

func (a *shadowsocksAdapter) forwardUDP(s *ssUDPSession, dest vlessDestination, payload []byte) error {
	n, err := s.writeUplink(dest, payload)
	if n > 0 {
		s.sess.up().Add(int64(n))
	}
	if err != nil && errors.Is(err, net.ErrClosed) {
		return nil
	}
	return err
}

// encodeUDPPacket 把上游回包编成 SS UDP 包（salt + AEAD(来源地址 + 负载)），写进 dst
// （容量至少 ssUDPEncodeBufSize，调用方从 ssUDPEncodePool 借），返回 dst 的前缀。
// 回包的地址写上游的真实来源（总是 IP），客户端据此区分不同目标的回包。
func (a *shadowsocksAdapter) encodeUDPPacket(dst, master []byte, source M.Socksaddr, payload []byte) ([]byte, error) {
	if len(payload) > ssUDPMaxPacket {
		return nil, fmt.Errorf("shadowsocks UDP payload too large")
	}
	addr := source.Addr.Unmap()
	if !addr.IsValid() || source.Port == 0 {
		return nil, fmt.Errorf("shadowsocks UDP response source invalid")
	}
	saltLen := a.method.SaltLen
	salt := dst[:saltLen]
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	sc := getSSScratch()
	sc.setSalt(salt)
	aead, err := a.method.NewAEAD(sc.subkey(master, a.method.KeyLen))
	putSSScratch(sc)
	if err != nil {
		return nil, err
	}
	plain := dst[saltLen:]
	n := 0
	if addr.Is4() {
		plain[0] = 1
		ip4 := addr.As4()
		n = 1 + copy(plain[1:], ip4[:])
	} else {
		plain[0] = 4
		ip16 := addr.As16()
		n = 1 + copy(plain[1:], ip16[:])
	}
	binary.BigEndian.PutUint16(plain[n:], source.Port)
	n += 2
	n += copy(plain[n:], payload)
	// 每个 UDP 包的 salt 都是新的、子密钥只用一次，nonce 用全零（SIP004）。
	var nonce [12]byte
	sealed := aead.Seal(plain[:0], nonce[:], plain[:n], nil)
	return dst[:saltLen+len(sealed)], nil
}
func destinationUDPAddr(destination vlessDestination) (net.Addr, error) {
	if destination.IP.IsValid() {
		return &net.UDPAddr{IP: net.IP(destination.IP.AsSlice()), Port: int(destination.Port)}, nil
	}
	return net.ResolveUDPAddr("udp", net.JoinHostPort(destination.Domain, strconv.Itoa(int(destination.Port))))
}
func destinationFromNetAddr(addr net.Addr) vlessDestination {
	if udp, ok := addr.(*net.UDPAddr); ok {
		ip, ok := netip.AddrFromSlice(udp.IP)
		if ok {
			return vlessDestination{Host: ip.String(), IP: ip, Port: uint16(udp.Port)}
		}
	}
	host, port, err := net.SplitHostPort(addr.String())
	if err != nil {
		return vlessDestination{Host: addr.String()}
	}
	parsed, _ := strconv.ParseUint(port, 10, 16)
	if ip, parseErr := netip.ParseAddr(host); parseErr == nil {
		return vlessDestination{Host: host, IP: ip, Port: uint16(parsed)}
	}
	return vlessDestination{Host: host, Domain: host, Port: uint16(parsed)}
}
func serializeSSDestination(destination vlessDestination) ([]byte, error) {
	var out []byte
	if destination.IP.IsValid() {
		if destination.IP.Is4() {
			out = append(out, 1)
			ip := destination.IP.As4()
			out = append(out, ip[:]...)
		} else {
			out = append(out, 4)
			ip := destination.IP.As16()
			out = append(out, ip[:]...)
		}
	} else {
		host := destination.Domain
		if host == "" {
			host = destination.Host
		}
		if len(host) == 0 || len(host) > 253 {
			return nil, fmt.Errorf("shadowsocks destination domain invalid")
		}
		out = append(out, 3, byte(len(host)))
		out = append(out, host...)
	}
	if destination.Port == 0 {
		return nil, fmt.Errorf("shadowsocks destination port invalid")
	}
	var port [2]byte
	binary.BigEndian.PutUint16(port[:], destination.Port)
	return append(out, port[:]...), nil
}
