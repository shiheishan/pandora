package kernel

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"io"
	"net"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/aegispanel/nodeagent/core"
	M "github.com/sagernet/sing/common/metadata"
	xnet "github.com/xtls/xray-core/common/net"
	xrayreality "github.com/xtls/xray-core/transport/internet/reality"
)

// realityVLESSEnv 是一个起好的 VLESS REALITY 入站（server name example.com，上游回显）
// 与对应的 Xray REALITY 客户端配置。
type realityVLESSEnv struct {
	port    int
	id      [16]byte
	client  *xrayreality.Config
	adapter *vlessAdapter
}

// startRealityVLESS 起 VLESS REALITY 入站，transport 是传输相关的键（network、path 等）。
func startRealityVLESS(t *testing.T, transport map[string]any) realityVLESSEnv {
	t.Helper()
	upstream, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = upstream.Close() })
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
	decoyTLS := tls.NewListener(decoyRaw, testXHTTPServerTLSConfig(t))
	t.Cleanup(func() { _ = decoyTLS.Close() })
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
	port := reserveTCPPort(t)
	raw := map[string]any{
		"security": "reality", "dest": decoyRaw.Addr().String(), "server_names": []any{"example.com"},
		"private_key": base64.RawURLEncoding.EncodeToString(key.Bytes()), "short_ids": []any{"0908070605040302"},
	}
	for k, v := range transport {
		raw[k] = v
	}
	spec := InboundSpec{Config: core.InboundConfig{Protocol: "vless", Listen: "127.0.0.1", Port: port, Raw: raw}}
	adapterValue, err := newVLESSAdapter(spec)
	if err != nil {
		t.Fatal(err)
	}
	adapter := adapterValue.(*vlessAdapter)
	if err := adapter.Validate(spec); err != nil {
		t.Fatal(err)
	}
	id := [16]byte{1, 3, 3, 7, 9, 2, 0, 2, 4, 6, 8, 1, 1, 2, 3, 5}
	if err := adapter.AddUsers([]core.User{{ID: 941, UUID: uuidString(id)}}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := adapter.Start(ctx, spec, AdapterHooks{DataPlane: &vlessTestPlane{target: M.SocksaddrFromNet(upstream.Addr().(*net.TCPAddr)).Unwrap()}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = adapter.Close() })
	client := &xrayreality.Config{Fingerprint: "firefox", ServerName: "example.com", PublicKey: key.PublicKey().Bytes(), ShortId: append([]byte(nil), shortID[:]...)}
	return realityVLESSEnv{port: port, id: id, client: client, adapter: adapter}
}

// dial 握一条 Xray REALITY 客户端连接。
func (e realityVLESSEnv) dial(ctx context.Context) (net.Conn, error) {
	raw, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(e.port)))
	if err != nil {
		return nil, err
	}
	rc, err := xrayreality.UClient(raw, e.client, ctx, xnet.TCPDestination(xnet.ParseAddress("127.0.0.1"), xnet.Port(443)))
	if err != nil {
		_ = raw.Close()
		return nil, err
	}
	return rc, nil
}

// REALITY 上的 gRPC：REALITY 解出的流不是 *tls.Conn，net/http 按明文 HTTP/2（prior knowledge）
// 接；:authority 是 REALITY server name。给 reality.Conn 加返回 tls.ConnectionState 的方法会让它红。
func TestNativeRealityGRPCXrayClient(t *testing.T) {
	env := startRealityVLESS(t, map[string]any{"network": "grpc", "grpc_service_name": "PandoraService"})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	transport := h2cTestTransport(func(dialCtx context.Context, _, _ string) (net.Conn, error) { return env.dial(dialCtx) })
	defer transport.CloseIdleConnections()
	pipeReader, pipeWriter := io.Pipe()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://example.com/PandoraService/Tun", pipeReader)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/grpc")
	req.Header.Set("TE", "trailers")
	payload := []byte("reality-grpc-native")
	header := append([]byte{vlessVersion}, env.id[:]...)
	header = append(header, 0, vlessTCP, 0x01, 0xbb, 1, 127, 0, 0, 1)
	go func() {
		_ = writeGRPCFrame(pipeWriter, append(header, payload...))
		_ = pipeWriter.Close()
	}()
	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatalf("REALITY gRPC：%v", err)
	}
	defer resp.Body.Close()
	if resp.ProtoMajor != 2 || resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "application/grpc" {
		t.Fatalf("proto=%s status=%d content-type=%q", resp.Proto, resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	var got []byte
	want := append([]byte{vlessVersion, 0}, payload...)
	for len(got) < len(want) {
		frame, err := readGRPCFrame(resp.Body)
		if err != nil {
			t.Fatalf("读 gRPC 帧：%v（已收 %q）", err, got)
		}
		got = append(got, frame...)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("REALITY gRPC 回显=%q", got)
	}
}
