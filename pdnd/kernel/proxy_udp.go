// [INPUT]: 依赖 proxy.go 的 proxyAdapter，依赖 vless_request.go 的 vlessDestination，依赖 DataPlane 的 UDP 路由
// [OUTPUT]: 包内提供 handleSOCKSUDP、SOCKS5 UDP 数据报的解析与封装、proxyUDPEvent / proxyUDPRoute
// [POS]: kernel 的 SOCKS5 UDP ASSOCIATE：从 proxy.go 拆出。控制连接存活期间转发数据报，每个目的地址一条经 DataPlane 路由的 PacketConn，按用户计量
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
	"time"

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

// handleSOCKSUDP implements RFC 1928 UDP ASSOCIATE. The association socket is
// only an inbound rendezvous; destination sockets are created per route through
// NativeCore DataPlane.ListenUDP, so policy selection and accounting remain
// identical to TCP CONNECT.
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
	events := make(chan proxyUDPEvent, 16)
	routes := make(map[string]*proxyUDPRoute)
	defer func() {
		for _, r := range routes {
			r.cancel()
			_ = r.conn.Close()
		}
	}()
	var clientAddr *net.UDPAddr
	buf := make([]byte, 1<<16)
	for {
		_ = assoc.SetReadDeadline(time.Now().Add(250 * time.Millisecond))
		n, src, readErr := assoc.ReadFromUDP(buf)
		if readErr == nil {
			if clientAddr == nil {
				clientAddr = src
			} else if !src.IP.Equal(clientAddr.IP) {
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
			dest := M.ParseSocksaddrHostPort(destination.Host, destination.Port)
			key := dest.String()
			r := routes[key]
			if r == nil {
				meta := route.Meta{Domain: destination.Domain, IP: destination.IP, Port: destination.Port, Network: "udp", Protocol: "socks"}
				if parsed, parseErr := netip.ParseAddr(ip); parseErr == nil {
					meta.SourceIP = parsed
				}
				pc, openErr := a.plane.ListenUDP(ctx, meta, dest)
				if openErr != nil {
					continue
				}
				routeCtx, routeCancel := context.WithCancel(ctx)
				r = &proxyUDPRoute{conn: pc, cancel: routeCancel}
				routes[key] = r
				go readProxyUDPRoute(routeCtx, pc, events)
			}
			if _, err := r.conn.WriteTo(payload, target); err != nil {
				continue
			}
			a.addTraffic(user, int64(len(payload)), 0)
		} else if ne, ok := readErr.(net.Error); !ok || !ne.Timeout() {
			return readErr
		}
		select {
		case event := <-events:
			if clientAddr == nil {
				continue
			}
			packet, err := marshalSOCKSUDPDatagram(event.addr, event.payload)
			if err == nil {
				if _, err = assoc.WriteToUDP(packet, clientAddr); err == nil {
					a.addTraffic(user, 0, int64(len(event.payload)))
				}
			}
		case err := <-controlDone:
			return err
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
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
	out.Write([]byte{5, 0, 0})
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
