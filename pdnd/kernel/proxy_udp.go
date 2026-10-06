// [INPUT]: 依赖 proxy.go 的 proxyAdapter，依赖 vless_request.go 的 vlessDestination，依赖 DataPlane 的 UDP 路由，依赖 udp_relay.go 的 relayUDPDirections
// [OUTPUT]: 包内提供 handleSOCKSUDP、SOCKS5 UDP 数据报的解析与封装、socksUDPAssociation / proxyUDPEvent / proxyUDPRoute
// [POS]: kernel 的 SOCKS5 UDP ASSOCIATE：从 proxy.go 拆出。控制连接存活期间转发数据报，关联锁定首个来源，每个目的地址一条经 DataPlane 路由的 PacketConn，上下行各占一个 goroutine，按用户计量
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package kernel

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strconv"
	"sync/atomic"

	M "github.com/sagernet/sing/common/metadata"

	"github.com/aegispanel/nodeagent/core"
	"github.com/aegispanel/nodeagent/route"
)

type proxyUDPEvent struct {
	payload []byte
	addr    net.Addr
}

type proxyUDPRoute struct {
	conn   net.PacketConn
	cancel context.CancelFunc
}

// socksUDPAssociation 是一次 UDP ASSOCIATE 的状态。routes 只由上行 goroutine
// 读写，收尾时两个方向都已退出才遍历；client 由上行写一次、下行读，用原子指针。
type socksUDPAssociation struct {
	adapter  *proxyAdapter
	user     core.User
	sourceIP string
	assoc    *net.UDPConn
	events   chan proxyUDPEvent
	routes   map[string]*proxyUDPRoute
	client   atomic.Pointer[net.UDPAddr]
}

// handleSOCKSUDP implements RFC 1928 UDP ASSOCIATE. The association socket is
// only an inbound rendezvous; destination sockets are created per route through
// NativeCore DataPlane.ListenUDP, so policy selection and accounting remain
// identical to TCP CONNECT.
//
// 上行（客户端 → 路由）与下行（路由 → 客户端）各占一个 goroutine，见 udp_relay.go。
// 控制连接读到任何字节或出错、ctx 结束、关联套接字出错，任一发生即收尾。
func (a *proxyAdapter) handleSOCKSUDP(ctx context.Context, control net.Conn, user core.User, reader *bufio.Reader, ip string) error {
	assoc, err := net.ListenUDP("udp", nil)
	if err != nil {
		return err
	}
	defer assoc.Close()
	if _, err := control.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, byte(assoc.LocalAddr().(*net.UDPAddr).Port >> 8), byte(assoc.LocalAddr().(*net.UDPAddr).Port)}); err != nil {
		return err
	}
	controlDone := make(chan error, 1)
	go func() {
		_, err := reader.ReadByte()
		controlDone <- err
	}()
	s := &socksUDPAssociation{
		adapter: a, user: user, sourceIP: ip, assoc: assoc,
		events: make(chan proxyUDPEvent, 16),
		routes: make(map[string]*proxyUDPRoute),
	}
	defer s.closeRoutes()
	return relayUDPDirections(ctx, controlDone, func() { _ = assoc.Close() }, s.uplink, s.downlink)
}

// uplink 阻塞读关联套接字。关联锁定第一个来源地址：此后只收同一 IP 的包（端口
// 不比较），回包始终发往第一个来源的 IP:端口。
func (s *socksUDPAssociation) uplink(ctx context.Context) error {
	buf := make([]byte, 1<<16)
	for {
		n, src, err := s.assoc.ReadFromUDP(buf)
		if err != nil {
			return err
		}
		if client := s.client.Load(); client == nil {
			s.client.Store(src)
		} else if !src.IP.Equal(client.IP) {
			continue
		}
		destination, payload, err := parseSOCKSUDPDatagram(buf[:n])
		if err != nil {
			continue
		}
		target, err := resolveProxyUDPAddr(ctx, destination)
		if err != nil {
			continue
		}
		r := s.route(ctx, destination)
		if r == nil {
			continue
		}
		if _, err := r.conn.WriteTo(payload, target); err != nil {
			continue
		}
		s.adapter.addTraffic(s.user, int64(len(payload)), 0)
	}
}

// route 取或建目的地址对应的路由 PacketConn，并为新路由起一个读协程把回包投进 events。
func (s *socksUDPAssociation) route(ctx context.Context, destination vlessDestination) *proxyUDPRoute {
	dest := M.ParseSocksaddrHostPort(destination.Host, destination.Port)
	key := dest.String()
	if r := s.routes[key]; r != nil {
		return r
	}
	meta := route.Meta{Domain: destination.Domain, IP: destination.IP, Port: destination.Port, Network: "udp", Protocol: "socks"}
	if parsed, parseErr := netip.ParseAddr(s.sourceIP); parseErr == nil {
		meta.SourceIP = parsed
	}
	pc, err := s.adapter.plane.ListenUDP(ctx, meta, dest)
	if err != nil {
		return nil
	}
	routeCtx, routeCancel := context.WithCancel(ctx)
	r := &proxyUDPRoute{conn: pc, cancel: routeCancel}
	s.routes[key] = r
	go readProxyUDPRoute(routeCtx, pc, s.events)
	return r
}

// downlink 把各路由的回包封成 SOCKS5 UDP 数据报发回锁定的客户端地址。
// 客户端地址还没锁定时（理论上不会：路由只因上行而建）回包丢弃。
func (s *socksUDPAssociation) downlink(ctx context.Context) error {
	for {
		select {
		case event := <-s.events:
			client := s.client.Load()
			if client == nil {
				continue
			}
			packet, err := marshalSOCKSUDPDatagram(event.addr, event.payload)
			if err != nil {
				continue
			}
			if _, err := s.assoc.WriteToUDP(packet, client); err == nil {
				s.adapter.addTraffic(s.user, 0, int64(len(event.payload)))
			}
		case <-ctx.Done():
			return nil
		}
	}
}

func (s *socksUDPAssociation) closeRoutes() {
	for _, r := range s.routes {
		r.cancel()
		_ = r.conn.Close()
	}
}

func readProxyUDPRoute(ctx context.Context, conn net.PacketConn, events chan<- proxyUDPEvent) {
	buf := make([]byte, 1<<16)
	for {
		n, addr, err := conn.ReadFrom(buf)
		if err != nil {
			return
		}
		item := proxyUDPEvent{payload: append([]byte(nil), buf[:n]...), addr: addr}
		select {
		case events <- item:
		case <-ctx.Done():
			return
		}
	}
}

func parseSOCKSUDPDatagram(packet []byte) (vlessDestination, []byte, error) {
	var destination vlessDestination
	if len(packet) < 4 || packet[0] != 0 || packet[1] != 0 || packet[2] != 0 {
		return destination, nil, fmt.Errorf("socks5 UDP header invalid")
	}
	offset := 3
	switch packet[offset] {
	case 1:
		if len(packet) < offset+1+4+2 {
			return destination, nil, io.ErrUnexpectedEOF
		}
		var ip [4]byte
		copy(ip[:], packet[offset+1:offset+5])
		destination.IP = netip.AddrFrom4(ip)
		destination.Host = destination.IP.String()
		offset += 5
	case 3:
		if len(packet) < offset+2 {
			return destination, nil, io.ErrUnexpectedEOF
		}
		length := int(packet[offset+1])
		if length == 0 || len(packet) < offset+2+length+2 {
			return destination, nil, fmt.Errorf("socks5 UDP domain invalid")
		}
		destination.Domain = string(packet[offset+2 : offset+2+length])
		destination.Host = destination.Domain
		offset += 2 + length
	case 4:
		if len(packet) < offset+1+16+2 {
			return destination, nil, io.ErrUnexpectedEOF
		}
		var ip [16]byte
		copy(ip[:], packet[offset+1:offset+17])
		destination.IP = netip.AddrFrom16(ip)
		destination.Host = destination.IP.String()
		offset += 17
	default:
		return destination, nil, fmt.Errorf("socks5 UDP address type unsupported")
	}
	destination.Port = uint16(packet[offset])<<8 | uint16(packet[offset+1])
	if destination.Port == 0 {
		return destination, nil, fmt.Errorf("socks5 UDP destination port invalid")
	}
	offset += 2
	return destination, packet[offset:], nil
}

func resolveProxyUDPAddr(ctx context.Context, destination vlessDestination) (*net.UDPAddr, error) {
	if destination.IP.IsValid() {
		return &net.UDPAddr{IP: destination.IP.AsSlice(), Port: int(destination.Port)}, nil
	}
	return net.ResolveUDPAddr("udp", net.JoinHostPort(destination.Domain, strconv.Itoa(int(destination.Port))))
}

func marshalSOCKSUDPDatagram(addr net.Addr, payload []byte) ([]byte, error) {
	host, port, err := net.SplitHostPort(addr.String())
	if err != nil {
		return nil, err
	}
	p, err := strconv.ParseUint(port, 10, 16)
	if err != nil {
		return nil, err
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	// RFC 1928 §7：RSV(0x0000) | FRAG(0) | ATYP。mihomo/Clash 系客户端校验 RSV 为零。
	out.Write([]byte{0, 0, 0})
	if ip.Is4() {
		out.WriteByte(1)
		v4 := ip.As4()
		out.Write(v4[:])
	} else {
		out.WriteByte(4)
		v6 := ip.As16()
		out.Write(v6[:])
	}
	out.WriteByte(byte(p >> 8))
	out.WriteByte(byte(p))
	out.Write(payload)
	return out.Bytes(), nil
}
