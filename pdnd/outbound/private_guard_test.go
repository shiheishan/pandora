package outbound

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"syscall"
	"testing"
	"time"

	M "github.com/sagernet/sing/common/metadata"
)

type fixedResolver []netip.Addr

func (r fixedResolver) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	return r, nil
}

func TestPrivateDestinationList(t *testing.T) {
	for _, s := range []string{"127.0.0.1", "10.1.2.3", "172.20.0.1", "192.168.1.1", "169.254.169.254",
		"100.64.0.1", "0.0.0.0", "::1", "fe80::1", "fd00::1", "::ffff:127.0.0.1", "224.0.0.1"} {
		if !isPrivateDestination(netip.MustParseAddr(s)) {
			t.Fatalf("%s 应算私网目标", s)
		}
	}
	for _, s := range []string{"1.1.1.1", "8.8.8.8", "172.32.0.1", "2001:4860:4860::8888"} {
		if isPrivateDestination(netip.MustParseAddr(s)) {
			t.Fatalf("%s 不该算私网目标", s)
		}
	}
}

// 默认拒绝：用户目标是回环 IP、或域名解析到内网，都连不上；中转出站连自己的
// 上游（DialContext，管理员配置）不受限；放开之后照常连。
func TestDirectBlocksPrivateDestinationsByDefault(t *testing.T) {
	if !BlockPrivateDestinations() {
		t.Fatal("默认应拒绝私网目标")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	port := uint16(ln.Addr().(*net.TCPAddr).Port)
	d := NewDirect("direct", "", fixedResolver{netip.MustParseAddr("127.0.0.1")})
	ctx := context.Background()
	if _, err := d.DialTCP(ctx, M.ParseSocksaddrHostPort("127.0.0.1", port)); !errors.Is(err, ErrPrivateDestination) {
		t.Fatalf("回环 IP 目标应被拒：%v", err)
	}
	if _, err := d.DialTCP(ctx, M.ParseSocksaddrHostPort("intranet.test", port)); !errors.Is(err, ErrPrivateDestination) {
		t.Fatalf("解析到回环的域名应被拒：%v", err)
	}
	conn, err := d.DialContext(ctx, "tcp", M.ParseSocksaddrHostPort("127.0.0.1", port))
	if err != nil {
		t.Fatalf("中转上游的拨号不该受限：%v", err)
	}
	_ = conn.Close()

	SetBlockPrivateDestinations(false)
	defer SetBlockPrivateDestinations(true)
	conn, err = d.DialTCP(ctx, M.ParseSocksaddrHostPort("127.0.0.1", port))
	if err != nil {
		t.Fatalf("放开后应能连：%v", err)
	}
	_ = conn.Close()
}

// UDP 逐包把关：发往私网的包被丢掉、发往公网的照发；把关的连接不透出裸 socket。
func TestDirectUDPDropsPrivatePackets(t *testing.T) {
	sink, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer sink.Close()
	d := NewDirect("direct", "", nil)
	pc, err := d.ListenUDP(context.Background(), M.ParseSocksaddrHostPort("1.1.1.1", 53))
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	if n, err := pc.WriteTo([]byte("private"), sink.LocalAddr()); err != nil || n != len("private") {
		t.Fatalf("私网包应静默丢弃：n=%d err=%v", n, err)
	}
	_ = sink.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	if n, _, err := sink.ReadFrom(make([]byte, 16)); err == nil {
		t.Fatalf("私网目标不该收到包（收到 %d 字节）", n)
	}
	if _, ok := pc.(*net.UDPConn); ok {
		t.Fatal("把关的连接不能就是裸 *net.UDPConn")
	}
	if _, ok := pc.(syscall.Conn); ok {
		t.Fatal("把关的连接不能透出 SyscallConn")
	}
	leased := &leasedPacketConn{PacketConn: pc, release: func() {}}
	if leased.RawUDPConn() != nil {
		t.Fatal("把关的连接不能被穿透成裸 socket（批量路径会绕过检查）")
	}
	if _, err := d.ListenUDP(context.Background(), M.ParseSocksaddrHostPort("10.0.0.1", 53)); !errors.Is(err, ErrPrivateDestination) {
		t.Fatalf("主目标就是私网时直接拒绝：%v", err)
	}

	SetBlockPrivateDestinations(false)
	defer SetBlockPrivateDestinations(true)
	open, err := d.ListenUDP(context.Background(), M.ParseSocksaddrHostPort("127.0.0.1", 53))
	if err != nil {
		t.Fatal(err)
	}
	defer open.Close()
	if _, ok := open.(*net.UDPConn); !ok {
		t.Fatal("放开后不套把关层，裸 socket 照常透出（hy2 批量收发要用）")
	}
}
