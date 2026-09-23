package kernel

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/tls"
	"io"
	"net"
	"testing"
	"time"

	"github.com/pires/go-proxyproto"
	xnet "github.com/xtls/xray-core/common/net"
	xrayreality "github.com/xtls/xray-core/transport/internet/reality"
)

func TestPandoraRealityListenerXrayClientInterop(t *testing.T) {
	key, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	shortID := [8]byte{1, 2, 3, 4, 5, 6, 7, 8}
	targetRaw, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer targetRaw.Close()
	targetTLS := testXHTTPServerTLSConfig(t)
	targetTLS.NextProtos = []string{"h2", "http/1.1"}
	proxyTarget := &proxyproto.Listener{Listener: targetRaw, Policy: func(net.Addr) (proxyproto.Policy, error) { return proxyproto.REQUIRE, nil }}
	target := tls.NewListener(proxyTarget, targetTLS)
	defer target.Close()
	go func() {
		for {
			conn, acceptErr := target.Accept()
			if acceptErr != nil {
				return
			}
			go func() {
				defer conn.Close()
				_, _ = io.Copy(io.Discard, conn)
			}()
		}
	}()

	listener, err := ListenReality("tcp", "127.0.0.1:0", RealityServerConfig{
		Dest:        targetRaw.Addr().String(),
		ServerNames: map[string]bool{"example.com": true},
		PrivateKey:  key.Bytes(),
		ShortIDs:    map[[8]byte]bool{shortID: true},
		Xver:        1,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serverDone := make(chan error, 1)
	go func() {
		serverDone <- listener.Serve(ctx, func(_ context.Context, session RealitySession) error {
			_, err := io.Copy(session.Conn, session.Conn)
			return err
		})
	}()

	raw, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	clientConfig := &xrayreality.Config{
		Fingerprint: "firefox",
		ServerName:  "example.com",
		PublicKey:   key.PublicKey().Bytes(),
		ShortId:     append([]byte(nil), shortID[:]...),
	}
	destination := xnet.TCPDestination(xnet.ParseAddress("127.0.0.1"), xnet.Port(443))
	client, err := xrayreality.UClient(raw, clientConfig, ctx, destination)
	if err != nil {
		_ = raw.Close()
		t.Fatalf("xray REALITY client handshake: %v", err)
	}
	defer client.Close()
	_ = client.SetDeadline(time.Now().Add(5 * time.Second))
	payload := []byte("pandora-reality-native")
	if _, err := client.Write(payload); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(client, got); err != nil {
		t.Fatal(err)
	}
	if string(got) != string(payload) {
		t.Fatalf("reality echo=%q", got)
	}
	_ = client.Close()
	cancel()
	select {
	case <-serverDone:
	case <-time.After(time.Second):
		t.Fatal("reality listener did not stop")
	}
}
