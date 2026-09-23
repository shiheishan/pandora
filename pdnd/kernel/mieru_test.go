package kernel

import (
	"context"
	"io"
	"net"
	"testing"
	"time"

	"github.com/aegispanel/nodeagent/core"
	"github.com/aegispanel/nodeagent/route"
	mieruclient "github.com/enfein/mieru/v3/apis/client"
	apicommon "github.com/enfein/mieru/v3/apis/common"
	mierupb "github.com/enfein/mieru/v3/pkg/appctl/appctlpb"
	M "github.com/sagernet/sing/common/metadata"
	"google.golang.org/protobuf/proto"
)

type mieruUDPTestPlane struct{}

func (mieruUDPTestPlane) DialTCP(context.Context, route.Meta, M.Socksaddr) (net.Conn, error) {
	return nil, io.ErrUnexpectedEOF
}

func (mieruUDPTestPlane) ListenUDP(context.Context, route.Meta, M.Socksaddr) (net.PacketConn, error) {
	return net.ListenPacket("udp4", "127.0.0.1:0")
}

func TestNativeMieruTCPAdapterLifecycle(t *testing.T) {
	reserved, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := reserved.Addr().(*net.TCPAddr).Port
	_ = reserved.Close()
	spec := InboundSpec{Config: core.InboundConfig{Protocol: "mieru", Tag: "native-mieru", Listen: "127.0.0.1", Port: port, Raw: map[string]any{"transport": "tcp"}}}
	adapterValue, err := newMieruAdapter(spec)
	if err != nil {
		t.Fatal(err)
	}
	adapter := adapterValue.(*mieruAdapter)
	if err := adapter.Validate(spec); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := adapter.Start(ctx, spec, AdapterHooks{DataPlane: &vlessTestPlane{}}); err != nil {
		t.Fatal(err)
	}
	if err := adapter.AddUsers([]core.User{{ID: 21, UUID: "mieru-user"}}); err != nil {
		t.Fatal(err)
	}
	if err := adapter.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestNativeMieruAcceptsRoutedUDP(t *testing.T) {
	adapter, err := newMieruAdapter(InboundSpec{Config: core.InboundConfig{Protocol: "mieru", Port: 28446, Raw: map[string]any{"transport": "udp"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.Validate(InboundSpec{Config: core.InboundConfig{Protocol: "mieru", Port: 28446, Raw: map[string]any{"transport": "udp"}}}); err != nil {
		t.Fatalf("mieru UDP validation: %v", err)
	}
}

func TestNativeMieruTCPInterop(t *testing.T) {
	upstream, target := startProxyEcho(t)
	defer upstream.Close()
	reserved, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := reserved.Addr().(*net.TCPAddr).Port
	_ = reserved.Close()
	spec := InboundSpec{Config: core.InboundConfig{Protocol: "mieru", Tag: "native-mieru-interop", Listen: "127.0.0.1", Port: port, Raw: map[string]any{"transport": "tcp"}}}
	adapterValue, err := newMieruAdapter(spec)
	if err != nil {
		t.Fatal(err)
	}
	adapter := adapterValue.(*mieruAdapter)
	if err := adapter.Start(context.Background(), spec, AdapterHooks{DataPlane: &vlessTestPlane{target: socksAddr(target)}}); err != nil {
		t.Fatal(err)
	}
	defer adapter.Close()
	const user = "mieru-interop-user"
	if err := adapter.AddUsers([]core.User{{ID: 22, UUID: user}}); err != nil {
		t.Fatal(err)
	}
	client := mieruclient.NewClient()
	profile := &mierupb.ClientProfile{ProfileName: proto.String("native-test"), User: &mierupb.User{Name: proto.String(user), Password: proto.String(user)}, Servers: []*mierupb.ServerEndpoint{{IpAddress: proto.String("127.0.0.1"), PortBindings: []*mierupb.PortBinding{{Port: proto.Int32(int32(port)), Protocol: mierupb.TransportProtocol_TCP.Enum()}}}}}
	if err := client.Store(&mieruclient.ClientConfig{Profile: profile}); err != nil {
		t.Fatal(err)
	}
	if err := client.Start(); err != nil {
		t.Fatal(err)
	}
	defer client.Stop()
	conn, err := client.DialContext(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	payload := []byte("pandora-mieru-native")
	if _, err := conn.Write(payload); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatal(err)
	}
	if string(got) != string(payload) {
		t.Fatalf("echo=%q", got)
	}
}

func TestNativeMieruUDPInterop(t *testing.T) {
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
	reserved, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := reserved.Addr().(*net.TCPAddr).Port
	_ = reserved.Close()
	spec := InboundSpec{Config: core.InboundConfig{Protocol: "mieru", Tag: "native-mieru-udp", Listen: "127.0.0.1", Port: port, Raw: map[string]any{"transport": "udp"}}}
	adapterValue, err := newMieruAdapter(spec)
	if err != nil {
		t.Fatal(err)
	}
	adapter := adapterValue.(*mieruAdapter)
	if err := adapter.Start(context.Background(), spec, AdapterHooks{DataPlane: mieruUDPTestPlane{}}); err != nil {
		t.Fatal(err)
	}
	defer adapter.Close()
	const user = "mieru-udp-interop-user"
	if err := adapter.AddUsers([]core.User{{ID: 23, UUID: user}}); err != nil {
		t.Fatal(err)
	}
	client := mieruclient.NewClient()
	profile := &mierupb.ClientProfile{ProfileName: proto.String("native-udp-test"), User: &mierupb.User{Name: proto.String(user), Password: proto.String(user)}, Servers: []*mierupb.ServerEndpoint{{IpAddress: proto.String("127.0.0.1"), PortBindings: []*mierupb.PortBinding{{Port: proto.Int32(int32(port)), Protocol: mierupb.TransportProtocol_UDP.Enum()}}}}}
	if err := client.Store(&mieruclient.ClientConfig{Profile: profile}); err != nil {
		t.Fatal(err)
	}
	if err := client.Start(); err != nil {
		t.Fatal(err)
	}
	defer client.Stop()
	target := echo.LocalAddr().(*net.UDPAddr)
	conn, err := client.DialContext(context.Background(), &net.UDPAddr{IP: target.IP, Port: target.Port})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	wrapped := apicommon.NewUDPAssociateWrapper(apicommon.NewPacketOverStreamTunnel(conn))
	_ = wrapped.SetReadDeadline(time.Now().Add(3 * time.Second))
	payload := []byte("pandora-mieru-native-udp")
	if _, err := wrapped.WriteTo(payload, target); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, 2048)
	n, _, err := wrapped.ReadFrom(got)
	if err != nil {
		t.Fatal(err)
	}
	if string(got[:n]) != string(payload) {
		t.Fatalf("udp echo=%q", got[:n])
	}
}
