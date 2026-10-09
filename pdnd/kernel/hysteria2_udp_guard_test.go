package kernel

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/aegispanel/nodeagent/internal/udprecv"
	"github.com/aegispanel/nodeagent/outbound"
	M "github.com/sagernet/sing/common/metadata"
	"golang.org/x/net/ipv4"
)

// guardedUpstream 按生产链路开一个上游 UDP：出站集合里的直连 → 租约包装 →
// 默认拦私网的把关层。
func guardedUpstream(t *testing.T) net.PacketConn {
	t.Helper()
	if !outbound.BlockPrivateDestinations() {
		t.Fatal("默认应拒绝私网目标")
	}
	set := outbound.NewSet()
	set.Replace(map[string]outbound.Outbound{"direct": outbound.NewDirect("direct", outbound.StrategyPreferIPv4, nil)})
	t.Cleanup(func() { _ = set.Close() })
	upstream, err := set.ListenUDP(context.Background(), "direct", M.ParseSocksaddrHostPort("1.1.1.1", 53))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = upstream.Close() })
	return upstream
}

// 默认拦私网时 hy2 / TUIC 的 UDP 转发仍走批量收发：上游不交裸 socket，而是经出站
// 给的带检查批量接口；发往私网的包在批里被剔除丢弃（照常计为已发出，与逐包
// WriteTo 同语义），策略放开后同一条批量通道照常送达；收方向经出站给的收包器收（只能收，不是裸 socket）。
//
// 非 Linux 上强制打开批量开关（x/net 每次只收发一包），批量逻辑在本机也有覆盖；
// Linux 上不强制的情形见 hysteria2_udp_guard_linux_test.go。
func TestHy2GuardedUpstreamUsesCheckedBatch(t *testing.T) {
	supported := hy2UDPBatchSupported
	defer func() { hy2UDPBatchSupported = supported }()
	hy2UDPBatchSupported = true

	upstream := guardedUpstream(t)
	u := newHy2UDPUpstream(upstream)
	if u.raw != nil {
		t.Fatal("默认拦私网时不能拿到裸 socket")
	}
	if u.batch == nil {
		t.Fatal("默认拦私网时应走带检查的批量通道")
	}
	// 必须是出站给的带检查接口，而不是直接包在裸 socket 上的 ipv4.PacketConn。
	if _, ok := u.batch.(outbound.UDPBatchConn); !ok {
		t.Fatalf("批量通道不是带检查的出站接口：%T", u.batch)
	}
	if _, ok := u.batch.(*ipv4.PacketConn); ok {
		t.Fatal("批量通道直接包在裸 socket 上")
	}
	sink, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer sink.Close()
	target := M.SocksaddrFromNet(sink.LocalAddr())
	send := func() int64 {
		writer := newHy2BatchWriter(upstream, u.batch)
		resolver := &hy2UDPResolver{ctx: context.Background()}
		writer.reset()
		for _, payload := range []string{"one", "two", "three"} {
			writer.add(resolver, []byte(payload), target, M.Socksaddr{})
		}
		written, err := writer.flush()
		if err != nil {
			t.Fatal(err)
		}
		return written
	}
	read := func(wait time.Duration) (string, bool) {
		_ = sink.SetReadDeadline(time.Now().Add(wait))
		buffer := make([]byte, 64)
		n, _, err := sink.ReadFrom(buffer)
		return string(buffer[:n]), err == nil
	}

	// 默认策略：回环目标是私网，整批丢弃但照常计数。
	if written := send(); written != int64(len("onetwothree")) {
		t.Fatalf("written=%d", written)
	}
	if got, ok := read(200 * time.Millisecond); ok {
		t.Fatalf("私网目标不该收到包，收到 %q", got)
	}

	// 放开策略：同一个把关层、同一条批量通道，包照常按序送达。
	outbound.SetBlockPrivateDestinations(false)
	defer outbound.SetBlockPrivateDestinations(true)
	if written := send(); written != int64(len("onetwothree")) {
		t.Fatalf("written=%d", written)
	}
	for _, want := range []string{"one", "two", "three"} {
		if got, ok := read(2 * time.Second); !ok || got != want {
			t.Fatalf("收到 %q（ok=%v），期望 %q", got, ok, want)
		}
	}
	outbound.SetBlockPrivateDestinations(true)

	// 收方向不过滤：上游 socket 收到的包经批量通道读出。
	local := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: upstream.LocalAddr().(*net.UDPAddr).Port}
	if _, err := sink.WriteTo([]byte("reply"), local); err != nil {
		t.Fatal(err)
	}
	_ = upstream.SetReadDeadline(time.Now().Add(2 * time.Second))
	if u.recv == nil {
		if udprecv.Supported {
			t.Fatal("默认拦私网时应有出站给的收包器")
		}
		return
	}
	b := udprecv.NewBatch([][]byte{make([]byte, 64)})
	n, err := u.recv.Recv(b, 1, true)
	if err != nil || n != 1 || string(b.Bufs[0][:b.N[0]]) != "reply" {
		t.Fatalf("Recv n=%d err=%v", n, err)
	}
}
