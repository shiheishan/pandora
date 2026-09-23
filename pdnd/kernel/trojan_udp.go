package kernel

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strconv"
	"time"

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

func (a *trojanAdapter) handleTrojanUDP(ctx context.Context, conn net.Conn, user core.User, initial vlessDestination, realitySession *RealitySession, remoteIPValue string) error {
	sourceIP, _ := netip.ParseAddr(remoteIPValue)
	protocol := "trojan"
	if realitySession != nil {
		protocol = "trojan-reality"
	}
	routes := make(map[string]*trojanUDPRoute)
	events := make(chan trojanUDPEvent, 32)
	defer func() {
		for _, r := range routes {
			r.cancel()
			_ = r.conn.Close()
		}
	}()
	var clientAddr vlessDestination
	if initial.Port != 0 {
		clientAddr = initial
	}
	buffer := make([]byte, trojanUDPMaxPacket)
	for {
		_ = conn.SetReadDeadline(time.Now().Add(250 * time.Millisecond))
		destination, payload, err := readTrojanUDPPacket(conn, buffer)
		if err == nil {
			clientAddr = destination
			target := M.ParseSocksaddrHostPort(destination.Host, destination.Port)
			key := target.String()
			r := routes[key]
			if r == nil {
				meta := route.Meta{Domain: destination.Domain, IP: destination.IP, Port: destination.Port, Network: "udp", Protocol: protocol, SourceIP: sourceIP}
				if host, port, splitErr := net.SplitHostPort(conn.RemoteAddr().String()); splitErr == nil {
					if parsed, parseErr := netip.ParseAddr(host); parseErr == nil {
						meta.SourceIP = parsed
					}
					if parsed, parseErr := strconv.ParseUint(port, 10, 16); parseErr == nil {
						meta.SourcePort = uint16(parsed)
					}
				}
				upstream, openErr := a.plane.ListenUDP(ctx, meta, target)
				if openErr != nil {
					return openErr
				}
				routeCtx, routeCancel := context.WithCancel(ctx)
				r = &trojanUDPRoute{conn: upstream, cancel: routeCancel}
				routes[key] = r
				go readTrojanUDPRoute(routeCtx, upstream, destination, events)
			}
			addr, resolveErr := resolveUDPAddr(ctx, target)
			if resolveErr != nil {
				continue
			}
			n, writeErr := r.conn.WriteTo(payload, addr)
			if writeErr != nil {
				return writeErr
			}
			a.addTraffic(user, int64(n), 0)
		} else if ne, ok := err.(net.Error); !ok || !ne.Timeout() {
			return err
		}
		select {
		case event := <-events:
			if event.destination.Port == 0 {
				event.destination = clientAddr
			}
			if err := writeTrojanUDPPacket(conn, event.destination, event.payload); err != nil {
				return err
			}
			a.addTraffic(user, 0, int64(len(event.payload)))
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
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
