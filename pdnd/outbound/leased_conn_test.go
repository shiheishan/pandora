package outbound

import (
	"io"
	"net"
	"testing"
	"time"
)

// leasedConn 内嵌 net.Conn 接口，底层 *net.TCPConn 的 CloseWrite 不会被自动提升。
// 少了它，转发对上游的半关闭断言失败、上游收不到 FIN，客户端断开后会话一直不释放
// （10 万连接实测）。这里钉住：经租约包装后 CloseWrite 仍能把 FIN 送到对端，
// 并且声明为透明包装、转发得以在底层裸 TCP 上无缓冲等待。
func TestLeasedConnForwardsHalfClose(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	gotEOF := make(chan error, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			gotEOF <- err
			return
		}
		defer conn.Close()
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		_, err = io.ReadAll(conn)
		gotEOF <- err
	}()
	raw, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	released := false
	var conn net.Conn = &leasedConn{Conn: raw, release: func() { released = true }}
	defer conn.Close()
	cw, ok := conn.(interface{ CloseWrite() error })
	if !ok {
		t.Fatal("leasedConn 必须提供 CloseWrite")
	}
	if err := cw.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	if err := <-gotEOF; err != nil {
		t.Fatalf("对端没有读到 EOF：%v", err)
	}
	if _, ok := conn.(io.ReaderFrom); !ok {
		t.Fatal("leasedConn 应转发 ReadFrom，零拷贝路径才走得通")
	}
	if tc, ok := conn.(interface{ TransparentConn() net.Conn }); !ok || tc.TransparentConn() != raw {
		t.Fatal("leasedConn 应声明为透明包装并交出底层连接")
	}
	_ = conn.Close()
	if !released {
		t.Fatal("Close 应释放租约")
	}
}
