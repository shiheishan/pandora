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

// leasedPacketConn 只对「底层直接就是裸 UDP socket」透出 RawUDPConn。会改写负载的
// 包装（这里用一个加密壳模拟）即使内部也是 *net.UDPConn，也绝不能被穿透：
// 批量收发路径拿到裸 socket 就会绕过它直接发明文。
func TestLeasedPacketConnRawUDPOnlyForBareSocket(t *testing.T) {
	bare, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer bare.Close()
	leased := &leasedPacketConn{PacketConn: bare, release: func() {}}
	if got := leased.RawUDPConn(); got != bare {
		t.Fatalf("裸 UDP socket 应透出，got %v", got)
	}

	inner, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer inner.Close()
	wrapped := &leasedPacketConn{PacketConn: &encryptingPacketConn{UDPConn: inner}, release: func() {}}
	if got := wrapped.RawUDPConn(); got != nil {
		t.Fatal("改写负载的包装不能被穿透成裸 socket")
	}
	// 包装本身也不该自称透明。
	var pc net.PacketConn = &encryptingPacketConn{UDPConn: inner}
	if _, ok := pc.(interface{ RawUDPConn() *net.UDPConn }); ok {
		t.Fatal("测试夹具不应实现 RawUDPConn")
	}
}

// encryptingPacketConn 模拟加密类出站的包装：内嵌 *net.UDPConn，但改写负载。
type encryptingPacketConn struct{ *net.UDPConn }

func (c *encryptingPacketConn) WriteTo(p []byte, addr net.Addr) (int, error) {
	out := make([]byte, len(p))
	for i, b := range p {
		out[i] = b ^ 0x5a
	}
	return c.UDPConn.WriteTo(out, addr)
}
