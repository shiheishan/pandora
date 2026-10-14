package kernel

import (
	"crypto/cipher"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"sync/atomic"
)

// ssUDPDropStats 按原因记 SS UDP 丢掉的包数（诊断与测试用，不上报）。
type ssUDPDropStats struct {
	pending  atomic.Uint64 // 会话建立期间小队列已满
	queue    atomic.Uint64 // 慢路径队列已满（后续任务用）
	rate     atomic.Uint64 // 上行超出限速（后续任务用）
	route    atomic.Uint64 // 目标的出站与会话的上游不一致（后续任务用）
	replay   atomic.Uint64 // salt 重放（后续任务用）
	downlink atomic.Uint64 // 下行编码失败、超出桶容量或写回临时出错
}

var (
	errSSUDPTooShort = markConnError(connErrAuth, errors.New("shadowsocks UDP packet too short"))
	errSSUDPAuth     = markConnError(connErrAuth, errors.New("shadowsocks UDP user authentication failed"))
)

// ssUDPMaxPacket 是读循环的收包缓冲（一个 UDP 包的上限）。
const ssUDPMaxPacket = 65535

// packetLoop 是入站唯一的 UDP 读循环，不按包起 goroutine：快路径（已有会话的用户试解）
// 与上行写上游在这里同步做（非阻塞 UDP 写）；解不开的包交给慢路径 worker 池
// （shadowsocks_udp_pool.go）。wire、plain、found 归本循环独占。
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
		handled, err := a.fastPathUDP(wire[:n], plain, &found, from)
		if err != nil {
			a.connErr.addr(StageSession, net.UDPAddrFromAddrPort(from), err)
		}
		if !handled {
			a.dispatchSlowUDP(wire[:n], from)
		}
	}
}

// fastPathUDP 是读循环里的快路径：来自已有会话地址的包先用该会话的用户试解（用户记录
// retired 就跳过）。解开了就转发并返回 handled=true；过短的包直接判失败（handled=true）；
// 解不开或该地址没有会话返回 false，由调用方交给慢路径。明文写进读循环独占的 plain。
func (a *shadowsocksAdapter) fastPathUDP(wire, plain []byte, found *[]*ssUDPSession, from netip.AddrPort) (bool, error) {
	if len(wire) < a.method.SaltLen+16 {
		return true, errSSUDPTooShort
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
			return true, a.forwardUDP(s, dest, payload)
		}
	}
	return false, nil
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
		return nil, dest, nil, errSSUDPAuth
	}
	return selected, dest, payload, nil
}

// 以下三个函数经典 AEAD 已不用，只剩 SS2022（shadowsocks2022_udp.go）在用；SS2022 多用户重写时一起挪走。

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
