// [INPUT]: 依赖 trojan.go 的 trojanAdapter（DataPlane.ListenUDP、addTraffic）与 readTrojanAddress，依赖 vless_request.go 的 vlessDestination，依赖 udp_relay.go 的 relayUDPDirections
// [OUTPUT]: 包内提供 handleTrojanUDP、Trojan UDP 帧（地址 | 长度 | CRLF | 负载）的 readTrojanUDPPacket / writeTrojanUDPPacket 与地址序列化
// [POS]: kernel 的 Trojan UDP ASSOCIATE：trojan.go 在 UDP 命令时分派到这里。每个目的地址一条经 DataPlane 路由的 PacketConn，上下行各占一个 goroutine，按用户计量
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package kernel

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strconv"

	"github.com/aegispanel/nodeagent/core"
	"github.com/aegispanel/nodeagent/route"
	M "github.com/sagernet/sing/common/metadata"
)

const trojanUDPMaxPacket = 8192

type trojanUDPEvent struct {
	destination vlessDestination
	payload     []byte
}

type trojanUDPRoute struct {
	conn   net.PacketConn
	cancel context.CancelFunc
}

// trojanUDPSession 是一条 Trojan UDP 连接的状态。routes 只由上行 goroutine
// 读写，收尾时两个方向都已退出才遍历；conn 只有上行读、只有下行写。
type trojanUDPSession struct {
	adapter  *trojanAdapter
	conn     net.Conn
	user     core.User
	protocol string
	sourceIP netip.Addr
	events   chan trojanUDPEvent
	routes   map[string]*trojanUDPRoute
}

// handleTrojanUDP 转发 Trojan UDP ASSOCIATE。请求头里的目的地址不参与转发：
// 每个数据报自带地址，按地址建一条经 DataPlane 路由的 PacketConn。上行（帧 → 路由）
// 与下行（路由 → 帧）各占一个 goroutine，见 udp_relay.go；任一方向出错、
// ctx 结束即关闭连接收尾，错误原样返回给入站的错误上报。
func (a *trojanAdapter) handleTrojanUDP(ctx context.Context, conn net.Conn, user core.User, _ vlessDestination, realitySession *RealitySession, remoteIPValue string) error {
	sourceIP, _ := netip.ParseAddr(remoteIPValue)
	protocol := "trojan"
	if realitySession != nil {
		protocol = "trojan-reality"
	}
	s := &trojanUDPSession{
		adapter: a, conn: conn, user: user, protocol: protocol, sourceIP: sourceIP,
		events: make(chan trojanUDPEvent, 32),
		routes: make(map[string]*trojanUDPRoute),
	}
	defer s.closeRoutes()
	return relayUDPDirections(ctx, nil, func() { _ = conn.Close() }, s.uplink, s.downlink)
}

// uplink 阻塞读 Trojan UDP 帧。不设读超时：超时打断在帧中间会让流错位。
func (s *trojanUDPSession) uplink(ctx context.Context) error {
	buffer := make([]byte, trojanUDPMaxPacket)
	for {
		destination, payload, err := readTrojanUDPPacket(s.conn, buffer)
		if err != nil {
			return err
		}
		target := M.ParseSocksaddrHostPort(destination.Host, destination.Port)
		r, err := s.route(ctx, destination, target)
		if err != nil {
			return err
		}
		addr, resolveErr := resolveUDPAddr(ctx, target)
		if resolveErr != nil {
			continue
		}
		n, writeErr := r.conn.WriteTo(payload, addr)
		if writeErr != nil {
			return writeErr
		}
		s.adapter.addTraffic(s.user, int64(n), 0)
	}
}

// route 取或建目的地址对应的路由 PacketConn，并为新路由起一个读协程把回包投进 events。
func (s *trojanUDPSession) route(ctx context.Context, destination vlessDestination, target M.Socksaddr) (*trojanUDPRoute, error) {
	key := target.String()
	if r := s.routes[key]; r != nil {
		return r, nil
	}
	meta := route.Meta{Domain: destination.Domain, IP: destination.IP, Port: destination.Port, Network: "udp", Protocol: s.protocol, SourceIP: s.sourceIP}
	if host, port, splitErr := net.SplitHostPort(s.conn.RemoteAddr().String()); splitErr == nil {
		if parsed, parseErr := netip.ParseAddr(host); parseErr == nil {
			meta.SourceIP = parsed
		}
		if parsed, parseErr := strconv.ParseUint(port, 10, 16); parseErr == nil {
			meta.SourcePort = uint16(parsed)
		}
	}
	upstream, err := s.adapter.plane.ListenUDP(ctx, meta, target)
	if err != nil {
		return nil, err
	}
	routeCtx, routeCancel := context.WithCancel(ctx)
	r := &trojanUDPRoute{conn: upstream, cancel: routeCancel}
	s.routes[key] = r
	go readTrojanUDPRoute(routeCtx, upstream, destination, s.events)
	return r, nil
}

// downlink 把各路由的回包按路由的目的地址封帧写回客户端，是 conn 唯一的写者。
// 回包地址恒为建路由时的目的地址，其端口非零（readTrojanAddress 拒绝端口 0）。
func (s *trojanUDPSession) downlink(ctx context.Context) error {
	for {
		select {
		case event := <-s.events:
			if err := writeTrojanUDPPacket(s.conn, event.destination, event.payload); err != nil {
				return err
			}
			s.adapter.addTraffic(s.user, 0, int64(len(event.payload)))
		case <-ctx.Done():
			return nil
		}
	}
}

func (s *trojanUDPSession) closeRoutes() {
	for _, r := range s.routes {
		r.cancel()
		_ = r.conn.Close()
	}
}

func readTrojanUDPRoute(ctx context.Context, conn net.PacketConn, destination vlessDestination, events chan<- trojanUDPEvent) {
	buffer := make([]byte, trojanUDPMaxPacket)
	for {
		n, _, err := conn.ReadFrom(buffer)
		if err != nil {
			return
		}
		event := trojanUDPEvent{destination: destination, payload: append([]byte(nil), buffer[:n]...)}
		select {
		case events <- event:
		case <-ctx.Done():
			return
		}
	}
}

func readTrojanUDPPacket(r io.Reader, scratch []byte) (vlessDestination, []byte, error) {
	var destination vlessDestination
	var addressType [1]byte
	if _, err := io.ReadFull(r, addressType[:]); err != nil {
		return destination, nil, err
	}
	if err := readTrojanAddress(r, addressType[0], &destination); err != nil {
		return destination, nil, err
	}
	var length [2]byte
	if _, err := io.ReadFull(r, length[:]); err != nil {
		return destination, nil, err
	}
	n := int(binary.BigEndian.Uint16(length[:]))
	if n > trojanUDPMaxPacket {
		return destination, nil, fmt.Errorf("trojan udp packet length %d exceeds limit", n)
	}
	var crlf [2]byte
	if _, err := io.ReadFull(r, crlf[:]); err != nil {
		return destination, nil, err
	}
	if crlf != [2]byte{'\r', '\n'} {
		return destination, nil, fmt.Errorf("trojan udp packet terminator invalid")
	}
	if cap(scratch) < n {
		scratch = make([]byte, n)
	}
	payload := scratch[:n]
	if _, err := io.ReadFull(r, payload); err != nil {
		return destination, nil, err
	}
	return destination, append([]byte(nil), payload...), nil
}

func writeTrojanUDPPacket(w io.Writer, destination vlessDestination, payload []byte) error {
	if len(payload) > trojanUDPMaxPacket || len(payload) > int(^uint16(0)) {
		return fmt.Errorf("trojan udp packet length %d exceeds limit", len(payload))
	}
	address, err := marshalTrojanAddress(destination)
	if err != nil {
		return err
	}
	frame := make([]byte, 0, len(address)+4+len(payload))
	frame = append(frame, address...)
	var length [2]byte
	binary.BigEndian.PutUint16(length[:], uint16(len(payload)))
	frame = append(frame, length[:]...)
	frame = append(frame, '\r', '\n')
	frame = append(frame, payload...)
	_, err = w.Write(frame)
	return err
}

func marshalTrojanAddress(destination vlessDestination) ([]byte, error) {
	if destination.IP.IsValid() {
		if destination.IP.Is4() {
			out := []byte{1}
			out = append(out, destination.IP.AsSlice()...)
			return appendPort(out, destination.Port), nil
		}
		out := []byte{4}
		out = append(out, destination.IP.AsSlice()...)
		return appendPort(out, destination.Port), nil
	}
	host := destination.Domain
	if host == "" {
		host = destination.Host
	}
	if len(host) == 0 || len(host) > 253 {
		return nil, fmt.Errorf("trojan udp domain is invalid")
	}
	out := append([]byte{3, byte(len(host))}, host...)
	return appendPort(out, destination.Port), nil
}

func appendPort(prefix []byte, port uint16) []byte {
	return append(prefix, byte(port>>8), byte(port))
}
