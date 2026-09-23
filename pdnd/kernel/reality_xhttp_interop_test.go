package kernel

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/aegispanel/nodeagent/core"
	"github.com/google/uuid"
	M "github.com/sagernet/sing/common/metadata"
	xnet "github.com/xtls/xray-core/common/net"
	xrayreality "github.com/xtls/xray-core/transport/internet/reality"
	"golang.org/x/net/http2"
)

func TestNativeRealityXHTTPXrayClientInterop(t *testing.T) {
	upstream, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer upstream.Close()
	go func() {
		for {
			conn, acceptErr := upstream.Accept()
			if acceptErr != nil {
				return
			}
			go func() {
				defer conn.Close()
				_, _ = io.Copy(conn, conn)
			}()
		}
	}()
	decoyRaw, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer decoyRaw.Close()
	decoyTLS := tls.NewListener(decoyRaw, testXHTTPServerTLSConfig(t))
	defer decoyTLS.Close()
	go func() {
		for {
			conn, acceptErr := decoyTLS.Accept()
			if acceptErr != nil {
				return
			}
			go func() {
				defer conn.Close()
				_, _ = io.Copy(io.Discard, conn)
			}()
		}
	}()
	key, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	shortID := [8]byte{9, 8, 7, 6, 5, 4, 3, 2}
	reserved, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := reserved.Addr().(*net.TCPAddr).Port
	_ = reserved.Close()
	certKey := base64.RawURLEncoding.EncodeToString(key.Bytes())
	id := [16]byte{1, 3, 3, 7, 9, 2, 0, 2, 4, 6, 8, 1, 1, 2, 3, 5}
	spec := InboundSpec{Config: core.InboundConfig{Protocol: "vless", Listen: "127.0.0.1", Port: port, Raw: map[string]any{
		"network": "xhttp", "security": "reality", "path": "/xhttp",
		"dest": decoyRaw.Addr().String(), "server_names": []any{"example.com"},
		"private_key": certKey, "short_ids": []any{"0908070605040302"},
	}}}
	adapterValue, err := newVLESSAdapter(spec)
	if err != nil {
		t.Fatal(err)
	}
	adapter := adapterValue.(*vlessAdapter)
	if err := adapter.Validate(spec); err != nil {
		t.Fatal(err)
	}
	if err := adapter.AddUsers([]core.User{{ID: 941, UUID: uuidString(id)}}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := adapter.Start(ctx, spec, AdapterHooks{DataPlane: &vlessTestPlane{target: M.SocksaddrFromNet(upstream.Addr().(*net.TCPAddr)).Unwrap()}}); err != nil {
		t.Fatal(err)
	}
	defer adapter.Close()

	raw, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		t.Fatal(err)
	}
	clientConfig := &xrayreality.Config{Fingerprint: "firefox", ServerName: "example.com", PublicKey: key.PublicKey().Bytes(), ShortId: append([]byte(nil), shortID[:]...)}
	realityConn, err := xrayreality.UClient(raw, clientConfig, ctx, xnet.TCPDestination(xnet.ParseAddress("127.0.0.1"), xnet.Port(443)))
	if err != nil {
		_ = raw.Close()
		t.Fatalf("xray REALITY client handshake: %v", err)
	}
	defer realityConn.Close()
	payload := []byte("reality-xhttp-native")
	header := append([]byte{vlessVersion}, id[:]...)
	header = append(header, 0, vlessTCP, 0, 0xbb, 1, 127, 0, 0, 1)
	bodyPayload := append(header, payload...)
	if _, err := fmt.Fprintf(realityConn, "POST /xhttp/reality-session/1/ HTTP/1.1\r\nHost: example.com\r\nContent-Length: %d\r\nContent-Type: application/octet-stream\r\nConnection: close\r\n\r\n", len(bodyPayload)); err != nil {
		t.Fatal(err)
	}
	if _, err := realityConn.Write(bodyPayload); err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(realityConn), &http.Request{Method: http.MethodPost})
	if err != nil {
		t.Fatal(err)
	}
	body, readErr := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if readErr != nil {
		t.Fatal(readErr)
	}
	if resp.StatusCode != http.StatusOK || len(body) != len(payload)+2 || body[0] != vlessVersion || body[1] != 0 || string(body[2:]) != string(payload) {
		t.Fatalf("status=%d body=%q", resp.StatusCode, body)
	}
	traffic, err := adapter.SnapshotTraffic()
	if err != nil || len(traffic) != 1 || traffic[0].ID != 941 || traffic[0].Upload == 0 || traffic[0].Download == 0 {
		t.Fatalf("traffic=%+v err=%v", traffic, err)
	}

	// REALITY clients select HTTP/2 for XHTTP. The same Pandora listener must
	// accept an h2 prior-knowledge session after the REALITY handshake instead
	// of only passing an HTTP/1.1 smoke test.
	h2Transport := &http2.Transport{AllowHTTP: true}
	h2Transport.DialTLSContext = func(dialCtx context.Context, network, _ string, _ *tls.Config) (net.Conn, error) {
		raw, dialErr := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(dialCtx, network, net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
		if dialErr != nil {
			return nil, dialErr
		}
		rc, realityErr := xrayreality.UClient(raw, clientConfig, dialCtx, xnet.TCPDestination(xnet.ParseAddress("127.0.0.1"), xnet.Port(443)))
		if realityErr != nil {
			_ = raw.Close()
			return nil, realityErr
		}
		return rc, nil
	}
	h2Client := &http.Client{Transport: h2Transport, Timeout: 5 * time.Second}
	h2Payload := []byte("reality-xhttp-h2-native")
	h2Header := append([]byte{vlessVersion}, id[:]...)
	h2Header = append(h2Header, 0, vlessTCP, 0, 0xbb, 1, 127, 0, 0, 1)
	h2Body := append(h2Header, h2Payload...)
	h2Req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://example.com/xhttp/h2-session/2/", bytes.NewReader(h2Body))
	if err != nil {
		t.Fatal(err)
	}
	h2Req.Host = "example.com"
	h2Req.Header.Set("Content-Type", "application/octet-stream")
	h2Resp, err := h2Client.Do(h2Req)
	if err != nil {
		t.Fatalf("xray REALITY H2 XHTTP request: %v", err)
	}
	h2ResponseBody, readErr := io.ReadAll(h2Resp.Body)
	_ = h2Resp.Body.Close()
	if readErr != nil {
		t.Fatal(readErr)
	}
	if h2Resp.StatusCode != http.StatusOK || len(h2ResponseBody) != len(h2Payload)+2 || h2ResponseBody[0] != vlessVersion || h2ResponseBody[1] != 0 || string(h2ResponseBody[2:]) != string(h2Payload) {
		t.Fatalf("h2 status=%d body=%q", h2Resp.StatusCode, h2ResponseBody)
	}
}

func uuidString(id [16]byte) string { return uuid.UUID(id).String() }
