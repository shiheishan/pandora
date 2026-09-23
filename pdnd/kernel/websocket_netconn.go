package kernel

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

func parseWebSocketTransport(raw map[string]any) (path, host string, err error) {
	path = strings.TrimSpace(rawString(raw, "ws_path"))
	if path == "" {
		path = strings.TrimSpace(rawString(raw, "path"))
	}
	if path == "" {
		path = "/"
	}
	if !strings.HasPrefix(path, "/") || strings.ContainsAny(path, "\r\n") {
		return "", "", fmt.Errorf("websocket path must begin with / and contain no control characters")
	}
	host = strings.TrimSpace(rawString(raw, "host"))
	if host == "" {
		host = strings.TrimSpace(rawString(raw, "server_name"))
	}
	if strings.ContainsAny(host, "\r\n") {
		return "", "", fmt.Errorf("websocket host contains control characters")
	}
	return path, host, nil
}

// websocketNetConn exposes a binary WebSocket message stream as a net.Conn.
// Each Write becomes one binary message; reads consume binary messages in
// order and reject text frames so protocol bytes are never silently changed.
type websocketNetConn struct {
	ws       *websocket.Conn
	underlay net.Conn
	readMu   sync.Mutex
	writeMu  sync.Mutex
	reader   *bufio.Reader
}

func newWebSocketNetConn(ws *websocket.Conn) net.Conn {
	return &websocketNetConn{ws: ws, underlay: ws.UnderlyingConn()}
}

func (c *websocketNetConn) Read(p []byte) (int, error) {
	c.readMu.Lock()
	defer c.readMu.Unlock()
	for {
		if c.reader == nil {
			typ, reader, err := c.ws.NextReader()
			if err != nil {
				return 0, err
			}
			if typ != websocket.BinaryMessage {
				return 0, fmt.Errorf("websocket text frames are not allowed")
			}
			c.reader = bufio.NewReader(reader)
		}
		n, err := c.reader.Read(p)
		if err == io.EOF {
			c.reader = nil
			if n > 0 {
				return n, nil
			}
			continue
		}
		return n, err
	}
}

func (c *websocketNetConn) Write(p []byte) (int, error) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	writer, err := c.ws.NextWriter(websocket.BinaryMessage)
	if err != nil {
		return 0, err
	}
	n, writeErr := writer.Write(p)
	closeErr := writer.Close()
	if writeErr != nil {
		return n, writeErr
	}
	return n, closeErr
}

func (c *websocketNetConn) Close() error                      { return c.ws.Close() }
func (c *websocketNetConn) LocalAddr() net.Addr               { return c.underlay.LocalAddr() }
func (c *websocketNetConn) RemoteAddr() net.Addr              { return c.underlay.RemoteAddr() }
func (c *websocketNetConn) SetDeadline(t time.Time) error     { return c.underlay.SetDeadline(t) }
func (c *websocketNetConn) SetReadDeadline(t time.Time) error { return c.underlay.SetReadDeadline(t) }
func (c *websocketNetConn) SetWriteDeadline(t time.Time) error {
	return c.underlay.SetWriteDeadline(t)
}
