package outbound

import (
	"context"
	"net"
	"net/netip"
	"os"
	"syscall"
	"testing"
	"time"

	M "github.com/sagernet/sing/common/metadata"
	"golang.org/x/net/ipv4"
)

// withPrivatePrefixes 临时换掉私网清单：本机只有回环可收包，测「公网目标照发」
// 得把 127.0.0.1 当成公网、另拿一个回环地址当私网。
func withPrivatePrefixes(t testing.TB, prefixes ...string) {
	t.Helper()
	saved := privatePrefixes
	privatePrefixes = nil
	for _, s := range prefixes {
		privatePrefixes = append(privatePrefixes, netip.MustParsePrefix(s))
	}
	t.Cleanup(func() { privatePrefixes = saved })
}

func guardedBatchFor(t *testing.T) (net.PacketConn, UDPBatchConn) {
	t.Helper()
	d := NewDirect("direct", "", nil)
	pc, err := d.ListenUDP(context.Background(), M.ParseSocksaddrHostPort("1.1.1.1", 53))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pc.Close() })
	// 生产里直连 UDP 外面还有一层出站租约，批量接口要能穿过它。
	leased := &leasedPacketConn{PacketConn: pc, release: func() {}}
	if leased.RawUDPConn() != nil {
		t.Fatal("默认拦私网时租约不能交出裸 socket")
	}
	batch := leased.UDPBatch()
	if batch == nil {
		t.Fatal("默认拦私网时应有带检查的批量接口")
	}
	return leased, batch
}

func batchMessage(payload string, to net.Addr) ipv4.Message {
	return ipv4.Message{Buffers: [][]byte{[]byte(payload)}, Addr: to}
}

func readOne(t *testing.T, conn net.PacketConn, wait time.Duration) (string, bool) {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(wait))
	buffer := make([]byte, 64)
	n, _, err := conn.ReadFrom(buffer)
	if err != nil {
		return "", false
	}
	return string(buffer[:n]), true
}

// 带检查的批量写：被拦的目标逐条剔除、静默丢弃（N 记为负载长度，与 WriteTo 同
// 语义），放行的目标按原顺序送达；返回值覆盖整批。
func TestGuardedUDPBatchDropsPrivateDestinations(t *testing.T) {
	public, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer public.Close()
	// 127.0.0.2 在 Linux 上是可绑定的回环地址，拿它当「私网」收包端证明真的没发出去；
	// macOS 默认只配了 127.0.0.1，绑不上就只核对返回值。
	private, privateErr := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 2)})
	privateAddr := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 2), Port: 9}
	if privateErr == nil {
		defer private.Close()
		privateAddr = private.LocalAddr().(*net.UDPAddr)
	}
	withPrivatePrefixes(t, "127.0.0.2/32", "10.0.0.0/8")
	_, batch := guardedBatchFor(t)

	ms := []ipv4.Message{
		batchMessage("p0", public.LocalAddr()),
		batchMessage("x1", privateAddr),
		batchMessage("p2", public.LocalAddr()),
		batchMessage("x3-long", privateAddr),
		batchMessage("x4", &net.UDPAddr{IP: net.IPv4(10, 1, 2, 3), Port: 53}),
		batchMessage("p5", public.LocalAddr()),
	}
	written := 0
	for written < len(ms) {
		n, err := batch.WriteBatch(ms[written:], 0)
		if err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			t.Fatal("WriteBatch 没有进展")
		}
		written += n
	}
	for i, m := range ms {
		if m.N != len(m.Buffers[0]) {
			t.Fatalf("第 %d 条 N=%d，期望 %d（被拦的也按已发出记）", i, m.N, len(m.Buffers[0]))
		}
	}
	for _, want := range []string{"p0", "p2", "p5"} {
		if got, ok := readOne(t, public, 2*time.Second); !ok || got != want {
			t.Fatalf("公网目标收到 %q（ok=%v），期望 %q", got, ok, want)
		}
	}
	if got, ok := readOne(t, public, 100*time.Millisecond); ok {
		t.Fatalf("公网目标多收到 %q", got)
	}
	if privateErr == nil {
		if got, ok := readOne(t, private, 200*time.Millisecond); ok {
			t.Fatalf("私网目标不该收到包，收到 %q", got)
		}
	}

	// 整批都是私网：一个系统调用都不发，照样全部记为已处理。
	allBlocked := []ipv4.Message{batchMessage("a", privateAddr), batchMessage("b", privateAddr)}
	if n, err := batch.WriteBatch(allBlocked, 0); err != nil || n != 2 {
		t.Fatalf("全私网批 n=%d err=%v", n, err)
	}
}

// 默认清单下发往回环的批量包被丢；收方向不过滤（与 ReadFrom 一致）。
func TestGuardedUDPBatchDefaultPolicy(t *testing.T) {
	if !BlockPrivateDestinations() {
		t.Fatal("默认应拒绝私网目标")
	}
	sink, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer sink.Close()
	conn, batch := guardedBatchFor(t)
	if n, err := batch.WriteBatch([]ipv4.Message{batchMessage("loopback", sink.LocalAddr())}, 0); err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if got, ok := readOne(t, sink, 200*time.Millisecond); ok {
		t.Fatalf("回环目标不该收到包，收到 %q", got)
	}
	// 收方向：外面发进来的包照常批量收到。
	local := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: conn.LocalAddr().(*net.UDPAddr).Port}
	if _, err := sink.WriteTo([]byte("inbound"), local); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	ms := []ipv4.Message{{Buffers: [][]byte{make([]byte, 64)}}}
	n, err := batch.ReadBatch(ms, 0)
	if err != nil || n != 1 || string(ms[0].Buffers[0][:ms[0].N]) != "inbound" {
		t.Fatalf("ReadBatch n=%d err=%v", n, err)
	}
	if err := batch.SetReadBuffer(1 << 20); err != nil {
		t.Fatal(err)
	}
	if err := batch.SetWriteBuffer(1 << 20); err != nil {
		t.Fatal(err)
	}
	if batch.LocalAddr().String() != conn.LocalAddr().String() {
		t.Fatalf("LocalAddr %v / %v", batch.LocalAddr(), conn.LocalAddr())
	}
}

// 批量接口不能成为裸 socket 的后门：不是 *net.UDPConn、不透出 SyscallConn /
// RawUDPConn / File；放开私网后不套把关层，自然也没有这个接口（走 RawUDPConn）。
func TestGuardedUDPBatchDoesNotLeakRawSocket(t *testing.T) {
	_, batch := guardedBatchFor(t)
	var value any = batch
	if _, ok := value.(*net.UDPConn); ok {
		t.Fatal("批量接口就是裸 socket")
	}
	if _, ok := value.(syscall.Conn); ok {
		t.Fatal("批量接口透出了 SyscallConn")
	}
	if _, ok := value.(interface{ RawUDPConn() *net.UDPConn }); ok {
		t.Fatal("批量接口透出了 RawUDPConn")
	}
	if _, ok := value.(interface{ File() (*os.File, error) }); ok {
		t.Fatal("批量接口透出了 File")
	}
	// 加密类包装（不实现 UDPBatchProvider）经租约也拿不到批量接口。
	inner, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer inner.Close()
	wrapped := &leasedPacketConn{PacketConn: &encryptingPacketConn{UDPConn: inner}, release: func() {}}
	if wrapped.UDPBatch() != nil {
		t.Fatal("改写负载的包装不能给出批量接口")
	}

	SetBlockPrivateDestinations(false)
	defer SetBlockPrivateDestinations(true)
	d := NewDirect("direct", "", nil)
	open, err := d.ListenUDP(context.Background(), M.ParseSocksaddrHostPort("127.0.0.1", 53))
	if err != nil {
		t.Fatal(err)
	}
	defer open.Close()
	leased := &leasedPacketConn{PacketConn: open, release: func() {}}
	if leased.RawUDPConn() == nil || leased.UDPBatch() != nil {
		t.Fatal("放开私网后应走裸 socket，不经把关层")
	}
}

// 带检查的直连 UDP 上行：逐包 WriteTo（批量路径拿不到裸 socket 时的退回）对比
// 带检查的 WriteBatch（Linux 上即 sendmmsg，一批 32 包）。私网清单换成「默认清单
// 去掉 127/8」，每包都完整走一遍判定、并真正发到本机收包端。ns/op 即 ns/包。
//
//	go test -run '^$' -bench BenchmarkGuardedUDPUplink -benchmem ./outbound/
func BenchmarkGuardedUDPUplink(b *testing.B) {
	var list []string
	for _, p := range privatePrefixes {
		if p.String() != "127.0.0.0/8" {
			list = append(list, p.String())
		}
	}
	withPrivatePrefixes(b, list...)
	sink, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		b.Fatal(err)
	}
	defer sink.Close()
	_ = sink.SetReadBuffer(8 << 20)
	go func() {
		buffer := make([]byte, 2048)
		for {
			if _, _, err := sink.ReadFrom(buffer); err != nil {
				return
			}
		}
	}()
	target := sink.LocalAddr()
	payload := make([]byte, 1200)
	open := func(b *testing.B) (net.PacketConn, UDPBatchConn) {
		d := NewDirect("direct", "", nil)
		pc, err := d.ListenUDP(context.Background(), M.ParseSocksaddrHostPort("1.1.1.1", 53))
		if err != nil {
			b.Fatal(err)
		}
		b.Cleanup(func() { _ = pc.Close() })
		leased := &leasedPacketConn{PacketConn: pc, release: func() {}}
		return leased, leased.UDPBatch()
	}
	b.Run("writeto", func(b *testing.B) {
		conn, _ := open(b)
		b.SetBytes(int64(len(payload)))
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if _, err := conn.WriteTo(payload, target); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("batch32", func(b *testing.B) {
		_, batch := open(b)
		ms := make([]ipv4.Message, 32)
		for i := range ms {
			ms[i] = ipv4.Message{Buffers: [][]byte{payload}, Addr: target}
		}
		b.SetBytes(int64(len(payload)))
		b.ReportAllocs()
		b.ResetTimer()
		for sent := 0; sent < b.N; {
			chunk := ms[:min(len(ms), b.N-sent)]
			n, err := batch.WriteBatch(chunk, 0)
			if err != nil {
				b.Fatal(err)
			}
			sent += n
		}
	})
}
