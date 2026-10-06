package kernel

import (
	"context"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"time"
)

type vlessXHTTPPacketSession struct {
	duplex *XHTTPPacketDuplex
	once   sync.Once
	ctx    context.Context
	cancel context.CancelFunc
}

type xhttpPacketConn struct {
	ctx      context.Context
	uplink   *XHTTPPacketQueue
	downlink *XHTTPPacketQueue
	mu       sync.Mutex
	readBuf  []byte
	writeSeq uint64
	closed   bool
}

func newXHTTPPacketConn(ctx context.Context, duplex *XHTTPPacketDuplex) *xhttpPacketConn {
	return &xhttpPacketConn{ctx: ctx, uplink: duplex.Uplink, downlink: duplex.Downlink}
}

func (c *xhttpPacketConn) Read(p []byte) (int, error) {
	for len(c.readBuf) == 0 {
		packet, err := c.uplink.Read(c.ctx)
		if err != nil {
			return 0, err
		}
		c.readBuf = append(c.readBuf, packet.Payload...)
	}
	n := copy(p, c.readBuf)
	c.readBuf = c.readBuf[n:]
	return n, nil
}

func (c *xhttpPacketConn) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return 0, io.ErrClosedPipe
	}
	packet := XHTTPPacket{Seq: c.writeSeq, Payload: append([]byte(nil), p...)}
	if err := c.downlink.Push(packet); err != nil {
		return 0, err
	}
	c.writeSeq++
	return len(p), nil
}

func (c *xhttpPacketConn) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	c.mu.Unlock()
	_ = c.uplink.Close(io.EOF)
	_ = c.downlink.Close(io.EOF)
	return nil
}

func (c *xhttpPacketConn) LocalAddr() net.Addr              { return xhttpAddr("pandora-xhttp-packet") }
func (c *xhttpPacketConn) RemoteAddr() net.Addr             { return xhttpAddr("xhttp-client") }
func (c *xhttpPacketConn) SetDeadline(time.Time) error      { return nil }
func (c *xhttpPacketConn) SetReadDeadline(time.Time) error  { return nil }
func (c *xhttpPacketConn) SetWriteDeadline(time.Time) error { return nil }

func (a *vlessAdapter) startXHTTPPacketSession(id string, realitySession *RealitySession) (*vlessXHTTPPacketSession, error) {
	if a.xhttpBroker == nil {
		return nil, fmt.Errorf("vless xhttp packet mode is not enabled")
	}
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return nil, fmt.Errorf("vless adapter closed")
	}
	if existing := a.xhttpSessions[id]; existing != nil {
		a.mu.Unlock()
		return existing, nil
	}
	duplex, err := a.xhttpBroker.OpenDuplex(id)
	if err != nil {
		a.mu.Unlock()
		return nil, err
	}
	ctx, cancel := context.WithCancel(a.ctx)
	session := &vlessXHTTPPacketSession{duplex: duplex, ctx: ctx, cancel: cancel}
	a.xhttpSessions[id] = session
	// Register the worker while holding the adapter lock. Close() takes the
	// same lock before waiting, so a concurrent HTTP request cannot Add to the
	// WaitGroup after shutdown has begun.
	a.wg.Add(1)
	a.mu.Unlock()
	session.once.Do(func() {
		go func() {
			defer a.wg.Done()
			conn := newXHTTPPacketConn(ctx, duplex)
			err := a.handleConnSession(ctx, conn, realitySession)
			cancel()
			_ = duplex.Uplink.Close(err)
			_ = duplex.Downlink.Close(err)
			a.mu.Lock()
			delete(a.xhttpSessions, id)
			a.mu.Unlock()
		}()
	})
	return session, nil
}

func (a *vlessAdapter) xhttpPacketHandler(ctx context.Context, session XHTTPSession) error {
	var realitySession *RealitySession
	if captured, ok := RealitySessionFromContext(ctx); ok {
		realitySession = &captured
	}
	if session.Request != nil && session.Request.Method == "GET" {
		packetSession, err := a.startXHTTPPacketSession(session.ID, realitySession)
		if err != nil {
			return err
		}
		for {
			packet, readErr := packetSession.duplex.Downlink.Read(ctx)
			if readErr != nil {
				if readErr == io.EOF || ctx.Err() != nil {
					return nil
				}
				return readErr
			}
			if _, writeErr := session.Writer.Write(packet.Payload); writeErr != nil {
				return writeErr
			}
			if flusher, ok := session.Writer.(interface{ Flush() }); ok {
				flusher.Flush()
			}
		}
	}
	if session.Seq == "" {
		return fmt.Errorf("xhttp packet uplink sequence is required")
	}
	seq, err := strconv.ParseUint(session.Seq, 10, 64)
	if err != nil {
		return fmt.Errorf("xhttp packet uplink sequence invalid: %w", err)
	}
	payload, err := io.ReadAll(session.Body)
	if err != nil {
		return err
	}
	packetSession, err := a.startXHTTPPacketSession(session.ID, realitySession)
	if err != nil {
		return err
	}
	return packetSession.duplex.Uplink.Push(XHTTPPacket{Seq: seq, Payload: payload})
}
