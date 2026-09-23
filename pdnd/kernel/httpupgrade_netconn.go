package kernel

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"
)

func validHTTPUpgradeRequest(reqPath, reqHost string, req *http.Request) bool {
	if req == nil || req.URL == nil || req.Method != http.MethodGet || req.URL.Path != reqPath {
		return false
	}
	if reqHost != "" && !strings.EqualFold(strings.TrimSpace(req.Host), reqHost) {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(req.Header.Get("Upgrade")), "websocket") {
		return false
	}
	for _, token := range strings.Split(req.Header.Get("Connection"), ",") {
		if strings.EqualFold(strings.TrimSpace(token), "upgrade") {
			return true
		}
	}
	return false
}

// hijackedNetConn retains bytes buffered by net/http while turning a hijacked
// HTTP connection into the protocol's raw stream.
type hijackedNetConn struct {
	net.Conn
	reader *bufio.Reader
}

func newHijackedNetConn(conn net.Conn, reader *bufio.Reader) net.Conn {
	return &hijackedNetConn{Conn: conn, reader: reader}
}

func (c *hijackedNetConn) Read(p []byte) (int, error) {
	if c.reader != nil && c.reader.Buffered() > 0 {
		return c.reader.Read(p)
	}
	c.reader = nil
	return c.Conn.Read(p)
}

func writeHTTPUpgradeResponse(rw *bufio.ReadWriter) error {
	if rw == nil {
		return fmt.Errorf("httpupgrade hijack writer is nil")
	}
	if _, err := rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n"); err != nil {
		return err
	}
	return rw.Flush()
}

func setUpgradeDeadline(conn net.Conn) {
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
}
