package kernel

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/aegispanel/nodeagent/core"
	"github.com/aegispanel/nodeagent/route"
	"github.com/google/uuid"
	M "github.com/sagernet/sing/common/metadata"
)

type vlessUDPTestPlane struct {
	testPlaneRecorder
}

func (p *vlessUDPTestPlane) DialTCP(_ context.Context, _ route.Meta, destination M.Socksaddr) (net.Conn, error) {
	p.recordDial(destination)
	return nil, io.ErrUnexpectedEOF
}

func (p *vlessUDPTestPlane) ListenUDP(_ context.Context, _ route.Meta, destination M.Socksaddr) (net.PacketConn, error) {
	p.recordListen(destination)
	return net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero})
}

func TestVLESSUDPFraming(t *testing.T) {
	payload := []byte("vless-udp")
	var wire []byte
	writer := &sliceWriter{dst: &wire}
	if err := writeVLESSUDPPacket(writer, payload); err != nil {
		t.Fatal(err)
	}
	got, err := readVLESSUDPPacket(bytes.NewReader(wire))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(payload) {
		t.Fatalf("payload=%q", got)
	}
}

func TestVLESSAdapterUDPForwardAndTraffic(t *testing.T) {
	echo, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer echo.Close()
	echoDone := make(chan struct{})
	go func() {
		defer close(echoDone)
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
	id := uuid.New()
	adapter := &vlessAdapter{users: make(map[string]core.User), traffic: make(map[int64]core.UserTraffic), online: make(map[int64]map[string]struct{})}
	spec := InboundSpec{Config: core.InboundConfig{Protocol: "vless", Listen: "127.0.0.1", Port: port}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	plane := &vlessUDPTestPlane{}
	if err := adapter.Start(ctx, spec, AdapterHooks{DataPlane: plane}); err != nil {
		t.Fatal(err)
	}
	defer adapter.Close()
	if err := adapter.AddUsers([]core.User{{ID: 4242, UUID: id.String()}}); err != nil {
		t.Fatal(err)
	}

	client, err := net.DialTimeout("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	_ = client.SetDeadline(time.Now().Add(3 * time.Second))
	header := []byte{vlessVersion}
	header = append(header, id[:]...)
	header = append(header, 0, vlessUDP, byte(echo.LocalAddr().(*net.UDPAddr).Port>>8), byte(echo.LocalAddr().(*net.UDPAddr).Port), 1, 127, 0, 0, 1)
	if _, err := client.Write(header); err != nil {
		t.Fatal(err)
	}
	var response [2]byte
	if _, err := io.ReadFull(client, response[:]); err != nil {
		t.Fatal(err)
	}
	if response != [2]byte{vlessVersion, 0} {
		t.Fatalf("response=%v", response)
	}
	payload := []byte("native-vless-udp")
	var length [2]byte
	binary.BigEndian.PutUint16(length[:], uint16(len(payload)))
	if _, err := client.Write(append(length[:], payload...)); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(client, length[:]); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, int(binary.BigEndian.Uint16(length[:])))
	if _, err := io.ReadFull(client, got); err != nil {
		t.Fatal(err)
	}
	if string(got) != string(payload) {
		t.Fatalf("echo=%q", got)
	}
	select {
	case <-echoDone:
	case <-time.After(time.Second):
		t.Fatal("udp echo did not receive packet")
	}
	// 请求头里写的 UDP 目标必须原样传到数据面。
	assertListenedTarget(t, plane, net.JoinHostPort("127.0.0.1", strconv.Itoa(echo.LocalAddr().(*net.UDPAddr).Port)))
	if err := adapter.Close(); err != nil {
		t.Fatal(err)
	}
	traffic, err := adapter.SnapshotTraffic()
	if err != nil {
		t.Fatal(err)
	}
	if len(traffic) != 1 || traffic[0].ID != 4242 || traffic[0].Upload != int64(len(payload)) || traffic[0].Download != int64(len(payload)) {
		t.Fatalf("traffic=%+v", traffic)
	}
}

type sliceWriter struct{ dst *[]byte }

func (w *sliceWriter) Write(p []byte) (int, error) {
	*w.dst = append(*w.dst, p...)
	return len(p), nil
}
