package kernel

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"net/netip"
	"strconv"
	"testing"
	"time"

	"github.com/aegispanel/nodeagent/core"
)

func TestTrojanUDPFraming(t *testing.T) {
	destination := vlessDestination{Domain: "example.com", Host: "example.com", Port: 443}
	var wire []byte
	if err := writeTrojanUDPPacket(&sliceWriter{dst: &wire}, destination, []byte("trojan-udp")); err != nil {
		t.Fatal(err)
	}
	gotDestination, got, err := readTrojanUDPPacket(&byteReader{data: wire}, make([]byte, trojanUDPMaxPacket))
	if err != nil {
		t.Fatal(err)
	}
	if gotDestination.Domain != destination.Domain || string(got) != "trojan-udp" {
		t.Fatalf("destination=%+v payload=%q", gotDestination, got)
	}
}

func TestTrojanAdapterUDPForwardAndTraffic(t *testing.T) {
	echo, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer echo.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 2048)
		n, addr, readErr := echo.ReadFromUDP(buf)
		if readErr == nil {
			_, _ = echo.WriteToUDP(buf[:n], addr)
		}
	}()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	adapter := &trojanAdapter{users: make(map[string]trojanUser), traffic: make(map[int64]core.UserTraffic), online: make(map[int64]map[string]struct{}), active: make(map[net.Conn]struct{})}
	spec := InboundSpec{Config: core.InboundConfig{Protocol: "trojan", Listen: "127.0.0.1", Port: port}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	plane := &vlessUDPTestPlane{}
	if err := adapter.Start(ctx, spec, AdapterHooks{DataPlane: plane}); err != nil {
		t.Fatal(err)
	}
	defer adapter.Close()
	if err := adapter.AddUsers([]core.User{{ID: 5252, UUID: "trojan-udp-secret"}}); err != nil {
		t.Fatal(err)
	}
	client, err := net.DialTimeout("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	_ = client.SetDeadline(time.Now().Add(3 * time.Second))
	header := []byte(trojanPasswordProof("trojan-udp-secret") + "\r\n")
	// CMD | ATYP | DST.ADDR | DST.PORT——Trojan 请求头没有 SOCKS5 的
	// VER 和 RSV 两个字节。
	header = append(header, trojanCommandUDP, 1, 127, 0, 0, 1)
	var targetPort [2]byte
	binary.BigEndian.PutUint16(targetPort[:], uint16(echo.LocalAddr().(*net.UDPAddr).Port))
	header = append(header, targetPort[:]...)
	header = append(header, '\r', '\n')
	if _, err := client.Write(header); err != nil {
		t.Fatal(err)
	}
	destination := vlessDestination{IP: netip.MustParseAddr("127.0.0.1"), Host: "127.0.0.1", Port: uint16(echo.LocalAddr().(*net.UDPAddr).Port)}
	if err := writeTrojanUDPPacket(client, destination, []byte("native-trojan-udp")); err != nil {
		t.Fatal(err)
	}
	_, got, err := readTrojanUDPPacket(client, make([]byte, trojanUDPMaxPacket))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "native-trojan-udp" {
		t.Fatalf("echo=%q", got)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("udp echo did not receive packet")
	}
	// UDP 包头里写的目标必须原样传到数据面，否则真实客户端会被发到别处。
	assertListenedTarget(t, plane, net.JoinHostPort("127.0.0.1", strconv.Itoa(echo.LocalAddr().(*net.UDPAddr).Port)))
	if err := adapter.Close(); err != nil {
		t.Fatal(err)
	}
	traffic, err := adapter.SnapshotTraffic()
	if err != nil {
		t.Fatal(err)
	}
	if len(traffic) != 1 || traffic[0].ID != 5252 || traffic[0].Upload == 0 || traffic[0].Download == 0 {
		t.Fatalf("traffic=%+v", traffic)
	}
}

type byteReader struct {
	data []byte
	pos  int
}

func (r *byteReader) Read(p []byte) (int, error) {
	if r.pos == len(r.data) {
		return 0, io.EOF
	}
	n := copy(p, r.data[r.pos:])
	r.pos += n
	return n, nil
}
