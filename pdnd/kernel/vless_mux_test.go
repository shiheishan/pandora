package kernel

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"net"
	"testing"
	"time"

	"github.com/aegispanel/nodeagent/core"
	M "github.com/sagernet/sing/common/metadata"
)

func TestVLESSMuxUDPFrameRoundTrip(t *testing.T) {
	echo, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer echo.Close()
	go func() {
		buffer := make([]byte, 2048)
		n, addr, readErr := echo.ReadFromUDP(buffer)
		if readErr == nil {
			_, _ = echo.WriteToUDP(buffer[:n], addr)
		}
	}()

	adapter := &vlessAdapter{
		users:   make(map[string]core.User),
		traffic: make(map[int64]core.UserTraffic),
		online:  make(map[int64]map[string]struct{}),
		plane:   &vlessUDPTestPlane{},
	}
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	user := core.User{ID: 9001, UUID: "vless-mux-test"}
	serverDone := make(chan error, 1)
	go func() {
		if _, err := server.Write([]byte{vlessVersion, 0}); err != nil {
			serverDone <- err
			return
		}
		serverDone <- adapter.handleVLESSMux(ctx, server, user, nil, "127.0.0.1")
	}()
	_ = client.SetDeadline(time.Now().Add(3 * time.Second))
	var response [2]byte
	if _, err := io.ReadFull(client, response[:]); err != nil {
		t.Fatal(err)
	}
	destination := M.ParseSocksaddrHostPort("127.0.0.1", uint16(echo.LocalAddr().(*net.UDPAddr).Port))
	var meta bytes.Buffer
	meta.WriteByte(vlessMuxNetworkUDP)
	if err := vmessAddressSerializer.WriteAddrPort(&meta, destination); err != nil {
		t.Fatal(err)
	}
	var header [6]byte
	binary.BigEndian.PutUint16(header[0:2], uint16(meta.Len()+4))
	binary.BigEndian.PutUint16(header[2:4], 7)
	header[4], header[5] = vlessMuxStatusNew, vlessMuxOptionData
	if _, err := client.Write(header[:]); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Write(meta.Bytes()); err != nil {
		t.Fatal(err)
	}
	payload := []byte("native-vless-xudp")
	var length [2]byte
	binary.BigEndian.PutUint16(length[:], uint16(len(payload)))
	if _, err := client.Write(length[:]); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Write(payload); err != nil {
		t.Fatal(err)
	}
	streamID, status, option, network, gotDestination, err := readVLESSMuxHeader(client)
	if err != nil {
		t.Fatal(err)
	}
	if streamID != 7 || status != vlessMuxStatusKeep || option != vlessMuxOptionData || network != vlessMuxNetworkUDP {
		t.Fatalf("response header=%d/%d/%d/%d", streamID, status, option, network)
	}
	data, _, err := readVLESSMuxData(client, gotDestination)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(payload) {
		t.Fatalf("response payload=%q", data)
	}
	_ = client.Close()
	if err := <-serverDone; err != nil && err != io.EOF {
		t.Fatal(err)
	}
}
