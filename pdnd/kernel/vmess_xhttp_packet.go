package kernel

import (
	"context"
	"fmt"
)

func (a *vmessAdapter) startXHTTPPacketSession(id string) (*xhttpSession, error) {
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
	session := newXHTTPSession(duplex, ctx, cancel)
	a.xhttpSessions[id] = session
	a.wg.Add(1)
	a.mu.Unlock()
	session.once.Do(func() {
		go func() {
			defer a.wg.Done()
			conn := newXHTTPPacketConn(ctx, duplex)
			err := a.handleConn(ctx, conn)
			cancel()
			session.stopReaper()
			_ = a.xhttpBroker.Close(id, err)
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
	return serveXHTTPSessionRequest(ctx, session, xhttpDownlinkGrace(a.xhttpConfig.Mode), func() (*xhttpSession, error) {
		return a.startXHTTPPacketSession(session.ID)
	})
}
