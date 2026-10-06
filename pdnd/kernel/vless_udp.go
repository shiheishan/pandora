package kernel

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strconv"
	"sync"
	"time"

	"github.com/aegispanel/nodeagent/core"
	"github.com/aegispanel/nodeagent/route"
	M "github.com/sagernet/sing/common/metadata"
)

const vlessUDPMaxPacket = 64 << 10

// handleVLESSUDP implements the VLESS command=UDP stream framing. The
// request header selects the first destination; each subsequent datagram is
// framed as a two-byte big-endian length followed by the payload. The native
// data plane remains responsible for routing and egress selection.
func (a *vlessAdapter) handleVLESSUDP(ctx context.Context, conn net.Conn, user core.User, destination vlessDestination, realitySession *RealitySession, remoteIPValue string) error {
	if destination.Port == 0 {
		return fmt.Errorf("vless udp destination port invalid")
	}
	target := M.ParseSocksaddrHostPort(destination.Host, destination.Port)
	sourceIP, _ := netip.ParseAddr(remoteIPValue)
	meta := route.Meta{
		Domain:   destination.Domain,
		IP:       destination.IP,
		Port:     destination.Port,
		Network:  "udp",
		Protocol: "vless",
		SourceIP: sourceIP,
	}
	if realitySession != nil {
		meta.Protocol = "vless-reality"
	}
	if host, port, err := net.SplitHostPort(conn.RemoteAddr().String()); err == nil {
		if parsed, parseErr := netip.ParseAddr(host); parseErr == nil {
			meta.SourceIP = parsed
		}
		if parsed, parseErr := strconv.ParseUint(port, 10, 16); parseErr == nil {
			meta.SourcePort = uint16(parsed)
		}
	}
	upstream, err := a.plane.ListenUDP(ctx, meta, target)
	if err != nil {
		return err
	}
	defer upstream.Close()
	upstreamAddr, err := resolveUDPAddr(ctx, target)
	if err != nil {
		return err
	}

	bridgeCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var bridgeWG sync.WaitGroup
	bridgeWG.Add(2)
	go func() {
		defer bridgeWG.Done()
		defer cancel()
		for {
			payload, readErr := readVLESSUDPPacket(conn)
			if readErr != nil {
				return
			}
			n, writeErr := upstream.WriteTo(payload, upstreamAddr)
			if writeErr != nil {
				return
			}
			a.addTraffic(user, int64(n), 0)
		}
	}()
	go func() {
		defer bridgeWG.Done()
		defer cancel()
		buffer := make([]byte, vlessUDPMaxPacket)
		for {
			n, _, readErr := upstream.ReadFrom(buffer)
			if readErr != nil {
				return
			}
			if n > vlessUDPMaxPacket {
				return
			}
			if writeErr := writeVLESSUDPPacket(conn, buffer[:n]); writeErr != nil {
				return
			}
			a.addTraffic(user, 0, int64(n))
		}
	}()
	go func() {
		<-bridgeCtx.Done()
		_ = conn.SetDeadline(time.Now())
		_ = upstream.SetDeadline(time.Now())
		_ = conn.Close()
		_ = upstream.Close()
	}()
	bridgeWG.Wait()
	return nil
}

func readVLESSUDPPacket(r io.Reader) ([]byte, error) {
	var length [2]byte
	if _, err := io.ReadFull(r, length[:]); err != nil {
		return nil, err
	}
	n := int(binary.BigEndian.Uint16(length[:]))
	if n > vlessUDPMaxPacket {
		return nil, fmt.Errorf("vless udp packet length %d exceeds limit", n)
	}
	payload := make([]byte, n)
	if _, err := io.ReadFull(r, payload); err != nil {
		return nil, err
	}
	return payload, nil
}

func writeVLESSUDPPacket(w io.Writer, payload []byte) error {
	if len(payload) > vlessUDPMaxPacket || len(payload) > int(^uint16(0)) {
		return fmt.Errorf("vless udp packet length %d exceeds limit", len(payload))
	}
	var length [2]byte
	binary.BigEndian.PutUint16(length[:], uint16(len(payload)))
	if _, err := w.Write(length[:]); err != nil {
		return err
	}
	_, err := w.Write(payload)
	return err
}
