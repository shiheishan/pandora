package kernel

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/aegispanel/nodeagent/route"
	M "github.com/sagernet/sing/common/metadata"
)

// uotRoutedPacketConn is the UDP side of AnyTLS UoT. Each destination gets a
// routed DataPlane packet socket, while responses are fanned back into one
// packet stream for the UoT framing layer. This preserves per-datagram target
// addresses instead of binding the whole session to its first packet.
type uotRoutedPacketConn struct {
	ctx      context.Context
	cancel   context.CancelFunc
	plane    DataPlane
	baseMeta route.Meta

	mu        sync.Mutex
	upstreams map[string]net.PacketConn
	results   chan uotDatagram
	deadline  time.Time
	closed    bool
	closeOnce sync.Once
}

type uotDatagram struct {
	payload []byte
	addr    net.Addr
	err     error
}

func newUOTRoutedPacketConn(parent context.Context, plane DataPlane, meta route.Meta) *uotRoutedPacketConn {
	ctx, cancel := context.WithCancel(parent)
	return &uotRoutedPacketConn{
		ctx: ctx, cancel: cancel, plane: plane, baseMeta: meta,
		upstreams: make(map[string]net.PacketConn), results: make(chan uotDatagram, 64),
	}
}

func (c *uotRoutedPacketConn) ensureUpstream(destination M.Socksaddr) (net.PacketConn, error) {
	key := destination.String()
	c.mu.Lock()
	if existing := c.upstreams[key]; existing != nil {
		c.mu.Unlock()
		return existing, nil
	}
	c.mu.Unlock()
	meta := c.baseMeta
	meta.Domain, meta.IP, meta.Port = destination.Fqdn, destination.Addr, destination.Port
	upstream, err := c.plane.ListenUDP(c.ctx, meta, destination)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		_ = upstream.Close()
		return nil, net.ErrClosed
	}
	if existing := c.upstreams[key]; existing != nil {
		c.mu.Unlock()
		_ = upstream.Close()
		return existing, nil
	}
	c.upstreams[key] = upstream
	c.mu.Unlock()
	go c.readUpstream(upstream)
	return upstream, nil
}

func (c *uotRoutedPacketConn) readUpstream(upstream net.PacketConn) {
	data := make([]byte, 64<<10)
	for {
		n, addr, err := upstream.ReadFrom(data)
		if err != nil {
			select {
			case c.results <- uotDatagram{err: err}:
			case <-c.ctx.Done():
			}
			return
		}
		packet := uotDatagram{payload: append([]byte(nil), data[:n]...), addr: addr}
		select {
		case c.results <- packet:
		case <-c.ctx.Done():
			return
		}
	}
}

func (c *uotRoutedPacketConn) ReadFrom(p []byte) (int, net.Addr, error) {
	c.mu.Lock()
	deadline := c.deadline
	c.mu.Unlock()
	var timer *time.Timer
	var timerC <-chan time.Time
	if !deadline.IsZero() {
		d := time.Until(deadline)
		if d <= 0 {
			return 0, nil, netErrTimeout{}
		}
		timer = time.NewTimer(d)
		timerC = timer.C
		defer timer.Stop()
	}
	select {
	case <-c.ctx.Done():
		return 0, nil, net.ErrClosed
	case <-timerC:
		return 0, nil, netErrTimeout{}
	case result := <-c.results:
		if result.err != nil {
			return 0, nil, result.err
		}
		if len(p) < len(result.payload) {
			return 0, nil, fmt.Errorf("uot packet exceeds read buffer")
		}
		return copy(p, result.payload), result.addr, nil
	}
}

func (c *uotRoutedPacketConn) WriteTo(p []byte, addr net.Addr) (int, error) {
	destination := M.SocksaddrFromNet(addr).Unwrap()
	if !destination.IsValid() || destination.Port == 0 {
		return 0, fmt.Errorf("uot destination is invalid")
	}
	upstream, err := c.ensureUpstream(destination)
	if err != nil {
		return 0, err
	}
	resolved, err := resolveUDPAddr(c.ctx, destination)
	if err != nil {
		return 0, err
	}
	return upstream.WriteTo(p, resolved)
}

func (c *uotRoutedPacketConn) Close() error {
	c.closeOnce.Do(func() {
		c.mu.Lock()
		c.closed = true
		upstreams := make([]net.PacketConn, 0, len(c.upstreams))
		for _, upstream := range c.upstreams {
			upstreams = append(upstreams, upstream)
		}
		c.mu.Unlock()
		c.cancel()
		for _, upstream := range upstreams {
			_ = upstream.Close()
		}
	})
	return nil
}

func (c *uotRoutedPacketConn) LocalAddr() net.Addr { return &net.UDPAddr{IP: net.IPv4zero} }

func (c *uotRoutedPacketConn) SetDeadline(deadline time.Time) error {
	c.mu.Lock()
	c.deadline = deadline
	c.mu.Unlock()
	return nil
}
func (c *uotRoutedPacketConn) SetReadDeadline(deadline time.Time) error {
	return c.SetDeadline(deadline)
}
func (c *uotRoutedPacketConn) SetWriteDeadline(time.Time) error { return nil }

type netErrTimeout struct{}

func (netErrTimeout) Error() string   { return "i/o timeout" }
func (netErrTimeout) Timeout() bool   { return true }
func (netErrTimeout) Temporary() bool { return true }
