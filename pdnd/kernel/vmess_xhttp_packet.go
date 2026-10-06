package kernel

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"sync"
)

type vmessXHTTPPacketSession struct {
	duplex *XHTTPPacketDuplex
	once   sync.Once
	ctx    context.Context
	cancel context.CancelFunc
}

func (a *vmessAdapter) startXHTTPPacketSession(id string) (*vmessXHTTPPacketSession, error) {
	if a.xhttpBroker == nil {
		return nil, fmt.Errorf("vmess xhttp packet mode is not enabled")
	}
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return nil, fmt.Errorf("vmess adapter closed")
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
	session := &vmessXHTTPPacketSession{duplex: duplex, ctx: ctx, cancel: cancel}
	a.xhttpSessions[id] = session
	a.wg.Add(1)
	a.mu.Unlock()
	session.once.Do(func() {
		go func() {
			defer a.wg.Done()
			conn := newXHTTPPacketConn(ctx, duplex)
			err := a.handleConn(ctx, conn)
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

func (a *vmessAdapter) xhttpPacketHandler(ctx context.Context, session XHTTPSession) error {
	if session.Request != nil && session.Request.Method == "GET" {
		packetSession, err := a.startXHTTPPacketSession(session.ID)
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
		return fmt.Errorf("vmess xhttp packet uplink sequence is required")
	}
	seq, err := strconv.ParseUint(session.Seq, 10, 64)
	if err != nil {
		return fmt.Errorf("vmess xhttp packet uplink sequence invalid: %w", err)
	}
	payload, err := io.ReadAll(session.Body)
	if err != nil {
		return err
	}
	packetSession, err := a.startXHTTPPacketSession(session.ID)
	if err != nil {
		return err
	}
	return packetSession.duplex.Uplink.Push(XHTTPPacket{Seq: seq, Payload: payload})
}
