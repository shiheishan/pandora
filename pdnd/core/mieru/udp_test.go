package mieru

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/aegispanel/nodeagent/core"
	"github.com/aegispanel/nodeagent/route"
	apicommon "github.com/enfein/mieru/v3/apis/common"
	M "github.com/sagernet/sing/common/metadata"
)

func TestAddUsersRejectsInvalidBatchAtomically(t *testing.T) {
	in := New("test", 0, "tcp", slog.Default())
	if err := in.AddUsers([]core.User{{ID: 1, UUID: "valid"}, {ID: 2, UUID: "  "}}); err == nil {
		t.Fatal("mieru accepted a batch containing a blank identity")
	}
	if got := in.users.Count(); got != 0 {
		t.Fatalf("mieru partially published invalid batch: count=%d", got)
	}
}

type routedUDPTestTransport struct{}

func (routedUDPTestTransport) DialTCP(context.Context, route.Meta, M.Socksaddr) (net.Conn, error) {
	return nil, io.ErrUnexpectedEOF
}

func (routedUDPTestTransport) ListenUDP(context.Context, route.Meta, M.Socksaddr) (net.PacketConn, error) {
	return net.ListenPacket("udp4", "127.0.0.1:0")
}

func TestNativeMieruUDPRoutedPacketLoop(t *testing.T) {
	echo, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer echo.Close()
	go func() {
		buf := make([]byte, 2048)
		for {
			n, addr, readErr := echo.ReadFrom(buf)
			if readErr != nil {
				return
			}
			_, _ = echo.WriteTo(buf[:n], addr)
		}
	}()

	server, client := net.Pipe()
	defer client.Close()
	finished := make(chan error, 1)
	go func() {
		finished <- (&Inbound{}).runNativeUDPLoop(context.Background(), server, routedUDPTestTransport{})
	}()

	clientTunnel := apicommon.NewPacketOverStreamTunnel(client)
	dst := echo.LocalAddr().(*net.UDPAddr)
	packet := make([]byte, 0, 3+1+4+2+5)
	packet = append(packet, 0, 0, 0, 1)
	packet = append(packet, dst.IP.To4()...)
	port := make([]byte, 2)
	binary.BigEndian.PutUint16(port, uint16(dst.Port))
	packet = append(packet, port...)
	packet = append(packet, []byte("hello")...)
	if _, err := clientTunnel.Write(packet); err != nil {
		t.Fatal(err)
	}
	_ = client.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 2048)
	n, err := clientTunnel.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	if n < 3+1+4+2 || !bytes.HasSuffix(buf[:n], []byte("hello")) {
		t.Fatalf("unexpected routed UDP response %x", buf[:n])
	}
	client.Close()
	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("native Mieru UDP loop did not stop")
	}
}
