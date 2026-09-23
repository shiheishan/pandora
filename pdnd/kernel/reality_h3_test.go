package kernel

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	realitytls "github.com/aegispanel/nodeagent/internal/reality"
	realityhttp3 "github.com/aegispanel/nodeagent/internal/realityquic/http3"
)

func TestNativeRealityH3RoundTrip(t *testing.T) {
	targetListener, err := tls.Listen("tcp", "127.0.0.1:0", testXHTTPServerTLSConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	defer targetListener.Close()
	go func() {
		conn, err := targetListener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		if tlsConn, ok := conn.(*tls.Conn); ok {
			_ = tlsConn.Handshake()
		}
	}()

	packet, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer packet.Close()
	serverKey, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var shortID [8]byte
	copy(shortID[:], []byte("pandora1"))
	xhttpConfig, err := ParseXHTTPConfig(map[string]any{"path": "/xhttp", "mode": "stream-one"})
	if err != nil {
		t.Fatal(err)
	}
	xhttp := XHTTPServer{Config: xhttpConfig, Handler: func(_ context.Context, session XHTTPSession) error {
		payload, err := io.ReadAll(session.Body)
		if err != nil {
			return err
		}
		_, err = session.Writer.Write(payload)
		return err
	}}
	h3Server, err := xhttp.ServeH3Reality(packet, RealityServerConfig{
		Dest:        targetListener.Addr().String(),
		ServerNames: map[string]bool{"localhost": true},
		PrivateKey:  serverKey.Bytes(),
		ShortIDs:    map[[8]byte]bool{shortID: true},
		MaxTimeDiff: time.Minute,
	}, (&net.Dialer{}).DialContext)
	if err != nil {
		t.Fatal(err)
	}
	defer h3Server.Close()

	clientTLS := &realitytls.Config{
		ServerName:         "localhost",
		PublicKey:          serverKey.PublicKey().Bytes(),
		ShortId:            shortID[:],
		InsecureSkipVerify: true,
	}
	transport := &realityhttp3.Transport{TLSClientConfig: clientTLS}
	defer transport.Close()
	client := &http.Client{Transport: transport}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://"+packet.LocalAddr().String()+"/xhttp/session-h3/1/", strings.NewReader("hello-reality-h3"))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.ProtoMajor != 3 || resp.StatusCode != http.StatusOK || string(body) != "hello-reality-h3" {
		t.Fatalf("proto=%s code=%d body=%q", resp.Proto, resp.StatusCode, body)
	}
}
