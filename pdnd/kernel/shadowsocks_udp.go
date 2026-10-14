package kernel

import (
	"context"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"time"

	M "github.com/sagernet/sing/common/metadata"

	"github.com/aegispanel/nodeagent/core"
	"github.com/aegispanel/nodeagent/route"
)

func (a *shadowsocksAdapter) packetLoop() {
	defer a.wg.Done()
	buffer := make([]byte, 64<<10)
	for {
		n, addr, err := a.packet.ReadFrom(buffer)
		if err != nil {
			a.mu.RLock()
			closed := a.closed
			a.mu.RUnlock()
			if closed || a.ctx.Err() != nil {
				return
			}
			continue
		}
		payload := append([]byte(nil), buffer[:n]...)
		a.wg.Add(1)
		go func() {
			defer a.wg.Done()
			_ = a.handlePacket(a.ctx, payload, addr)
		}()
	}
}

func (a *shadowsocksAdapter) handlePacket(ctx context.Context, wire []byte, clientAddr net.Addr) error {
	user, destination, payload, err := a.decodeUDPPacket(wire, clientAddr)
	if err != nil {
		return err
	}
	ip := remoteIP(clientAddr)
	if !a.online.enter(user, ip) {
		return fmt.Errorf("shadowsocks device limit")
	}
	defer a.online.leave(user, ip)
	sourceIP, _ := netip.ParseAddr(ip)
	meta := route.Meta{Domain: destination.Domain, IP: destination.IP, Port: destination.Port, Network: "udp", Protocol: "shadowsocks", SourceIP: sourceIP}
	upstream, err := a.plane.ListenUDP(ctx, meta, M.ParseSocksaddrHostPort(destination.Host, destination.Port))
	if err != nil {
		return err
	}
	defer upstream.Close()
	destinationAddr, err := destinationUDPAddr(destination)
	if err != nil {
		return err
	}
	if _, err := upstream.WriteTo(payload, destinationAddr); err != nil {
		return err
	}
	_ = upstream.SetReadDeadline(time.Now().Add(2 * time.Second))
	response := make([]byte, 64<<10)
	n, sourceAddr, err := upstream.ReadFrom(response)
	if err != nil {
		return err
	}
	responseDestination := destination
	if sourceAddr != nil {
		responseDestination = destinationFromNetAddr(sourceAddr)
	}
	encoded, err := a.encodeUDPPacket(user, responseDestination, response[:n])
	if err != nil {
		return err
	}
	_, err = a.packet.WriteTo(encoded, clientAddr)
	if err == nil {
		a.addTraffic(user, int64(len(payload)), int64(n))
	}
	return err
}

func destinationUDPAddr(destination vlessDestination) (net.Addr, error) {
	if destination.IP.IsValid() {
		return &net.UDPAddr{IP: net.IP(destination.IP.AsSlice()), Port: int(destination.Port)}, nil
	}
	return net.ResolveUDPAddr("udp", net.JoinHostPort(destination.Domain, strconv.Itoa(int(destination.Port))))
}

// decodeUDPPacket 与 TCP 握手共用快照、零分配派生和来源 IP 提示（clientAddr
// 可为 nil）；每个候选用户都做完整 AEAD 校验并解析出合法目标地址才算认证通过。
func (a *shadowsocksAdapter) decodeUDPPacket(wire []byte, clientAddr net.Addr) (core.User, vlessDestination, []byte, error) {
	var destination vlessDestination
	if len(wire) < a.method.SaltLen+16 {
		return core.User{}, destination, nil, fmt.Errorf("shadowsocks UDP packet too short")
	}
	salt := wire[:a.method.SaltLen]
	ciphertext := wire[a.method.SaltLen:]
	sc := getSSScratch()
	defer putSSScratch(sc)
	if cap(sc.plain) < len(ciphertext) {
		sc.plain = make([]byte, 0, len(ciphertext))
	}
	var payload []byte
	selected, aead := a.findSSUser(sc, salt, ssSourceKey(clientAddr), func(aead cipher.AEAD) bool {
		plain, err := aead.Open(sc.plain[:0], sc.zeroNonce(), ciphertext, nil)
		if err != nil {
			return false
		}
		var candidate vlessDestination
		consumed, err := parseSSDestination(plain, &candidate)
		if err != nil {
			return false
		}
		destination, payload = candidate, plain[consumed:]
		return true
	})
	if aead == nil {
		return core.User{}, destination, nil, fmt.Errorf("shadowsocks UDP user authentication failed")
	}
	return core.User{ID: selected.ID, DeviceLimit: selected.DeviceLimit, SpeedLimit: selected.SpeedLimit}, destination, append([]byte(nil), payload...), nil
}

func (a *shadowsocksAdapter) encodeUDPPacket(user core.User, destination vlessDestination, payload []byte) ([]byte, error) {
	key := a.userMasterKey(user.ID)
	if len(key) == 0 {
		return nil, fmt.Errorf("shadowsocks UDP user no longer exists")
	}
	salt := make([]byte, a.method.SaltLen)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	subkey, err := deriveSSSubkey(key, salt, a.method.KeyLen)
	if err != nil {
		return nil, err
	}
	aead, err := a.method.NewAEAD(subkey)
	if err != nil {
		return nil, err
	}
	address, err := serializeSSDestination(destination)
	if err != nil {
		return nil, err
	}
	plain := append(address, payload...)
	return append(salt, aead.Seal(nil, makeSSNonce(0), plain, nil)...), nil
}

func (a *shadowsocksAdapter) userMasterKey(id int64) []byte {
	// 快照里的 MasterKey 发布后不再修改，可以直接返回、不复制。
	if user := a.loadUsers().byID[id]; user != nil {
		return user.MasterKey
	}
	return nil
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
