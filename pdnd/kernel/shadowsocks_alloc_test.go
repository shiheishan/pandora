package kernel

import (
	"bytes"
	"io"
	"net"
	"testing"
	"time"
)

// bufConn 是只读写内存缓冲的 net.Conn，量数据路径的分配用。
type bufConn struct {
	r io.Reader
	w io.Writer
}

func (c *bufConn) Read(p []byte) (int, error)       { return c.r.Read(p) }
func (c *bufConn) Write(p []byte) (int, error)      { return c.w.Write(p) }
func (c *bufConn) Close() error                     { return nil }
func (c *bufConn) LocalAddr() net.Addr              { return &net.TCPAddr{} }
func (c *bufConn) RemoteAddr() net.Addr             { return &net.TCPAddr{} }
func (c *bufConn) SetDeadline(time.Time) error      { return nil }
func (c *bufConn) SetReadDeadline(time.Time) error  { return nil }
func (c *bufConn) SetWriteDeadline(time.Time) error { return nil }

func benchSSAEAD(b *testing.B) (aeadFactory func() *ssStream) {
	key := make([]byte, 16)
	aead, err := newAESGCM(key)
	if err != nil {
		b.Fatal(err)
	}
	return func() *ssStream { return &ssStream{aead: aead} }
}

// 下行（加密写回客户端）：每 32KB 一次 Write。
func BenchmarkSSStreamWrite32K(b *testing.B) {
	newStream := benchSSAEAD(b)
	s := newStream()
	s.conn = &bufConn{w: io.Discard}
	payload := make([]byte, 32<<10)
	b.SetBytes(int64(len(payload)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := s.Write(payload); err != nil {
			b.Fatal(err)
		}
	}
}

// 上行（解密客户端来的块）：转发用 32KB 缓冲读。
func BenchmarkSSStreamRead32K(b *testing.B) {
	newStream := benchSSAEAD(b)
	var wire bytes.Buffer
	w := newStream()
	w.conn = &bufConn{w: &wire}
	payload := make([]byte, 32<<10)
	for i := 0; i < 64; i++ {
		_, _ = w.Write(payload)
	}
	encoded := wire.Bytes()
	buf := make([]byte, 32<<10)
	b.SetBytes(int64(len(payload)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r := newStream()
		r.conn = &bufConn{r: bytes.NewReader(encoded)}
		for got := 0; got < len(payload); {
			n, err := r.Read(buf)
			if err != nil {
				b.Fatal(err)
			}
			got += n
		}
	}
}
