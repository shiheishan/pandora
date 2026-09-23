package kernel

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/tls"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aegispanel/nodeagent/core"
	"github.com/google/uuid"
	"golang.org/x/net/http2"
)

func writeGRPCFrame(w io.Writer, payload []byte) error {
	frame := make([]byte, 5+len(payload))
	binary.BigEndian.PutUint32(frame[1:5], uint32(len(payload)))
	copy(frame[5:], payload)
	_, err := w.Write(frame)
	return err
}

func readGRPCFrame(r io.Reader) ([]byte, error) {
	var header [5]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return nil, err
	}
	if header[0] != 0 {
		return nil, fmt.Errorf("compressed grpc frame")
	}
	payload := make([]byte, binary.BigEndian.Uint32(header[1:]))
	_, err := io.ReadFull(r, payload)
	return payload, err
}

func writeCompressedGRPCFrame(w io.Writer, payload []byte) error {
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	if _, err := writer.Write(payload); err != nil {
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}
	frame := make([]byte, 5+compressed.Len())
	frame[0] = 1
	binary.BigEndian.PutUint32(frame[1:5], uint32(compressed.Len()))
	copy(frame[5:], compressed.Bytes())
	_, err := w.Write(frame)
	return err
}

func TestGRPCDuplexConnGzipRead(t *testing.T) {
	payload := []byte("native-grpc-gzip")
	var wire bytes.Buffer
	if err := writeCompressedGRPCFrame(&wire, payload); err != nil {
		t.Fatal(err)
	}
	conn := newGRPCDuplexConnWithEncoding(context.Background(), io.NopCloser(bytes.NewReader(wire.Bytes())), httptest.NewRecorder(), 1<<20, "gzip")
	defer conn.Close()
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("gzip payload=%q", got)
	}
}

func TestVLESSNativeGRPCH2CLoopback(t *testing.T) {
	upstream, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer upstream.Close()
	go func() {
		conn, acceptErr := upstream.Accept()
		if acceptErr == nil {
			defer conn.Close()
			_, _ = io.Copy(conn, conn)
		}
	}()
	reserved, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := reserved.Addr().(*net.TCPAddr).Port
	_ = reserved.Close()
	id := uuid.New()
	spec := InboundSpec{Config: core.InboundConfig{Protocol: "vless", Listen: "127.0.0.1", Port: port, Raw: map[string]any{
		"network": "grpc", "grpc_service_name": "PandoraService",
	}}}
	adapterValue, err := newVLESSAdapter(spec)
	if err != nil {
		t.Fatal(err)
	}
	adapter := adapterValue.(*vlessAdapter)
	if err := adapter.Validate(spec); err != nil {
		t.Fatal(err)
	}
	if err := adapter.AddUsers([]core.User{{ID: 999, UUID: id.String()}}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := adapter.Start(ctx, spec, AdapterHooks{DataPlane: &vlessTestPlane{target: socksAddr(upstream.Addr())}}); err != nil {
		t.Fatal(err)
	}
	defer adapter.Close()
	pipeReader, pipeWriter := io.Pipe()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, fmt.Sprintf("http://127.0.0.1:%d/PandoraService/Tun", port), pipeReader)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/grpc")
	request.Header.Set("grpc-encoding", "gzip")
	request.Header.Set("TE", "trailers")
	transport := &http2.Transport{AllowHTTP: true, DialTLSContext: func(ctx context.Context, network, address string, _ *tls.Config) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, address)
	}}
	defer transport.CloseIdleConnections()
	payload := []byte("native-vless-grpc")
	vlessHeader := []byte{vlessVersion}
	vlessHeader = append(vlessHeader, id[:]...)
	vlessHeader = append(vlessHeader, 0, vlessTCP, 0x01, 0xbb, 1, 127, 0, 0, 1)
	go func() {
		_ = writeCompressedGRPCFrame(pipeWriter, append(vlessHeader, payload...))
		_ = pipeWriter.Close()
	}()
	response, err := transport.RoundTrip(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || response.Header.Get("Content-Type") != "application/grpc" {
		t.Fatalf("grpc status=%d content-type=%q", response.StatusCode, response.Header.Get("Content-Type"))
	}
	first, err := readGRPCFrame(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	second, err := readGRPCFrame(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(append(first, second...), append([]byte{vlessVersion, 0}, payload...)) {
		t.Fatalf("grpc vless response=%q", append(first, second...))
	}
}

func TestTrojanNativeGRPCH2CLoopback(t *testing.T) {
	upstream, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer upstream.Close()
	go func() {
		conn, acceptErr := upstream.Accept()
		if acceptErr == nil {
			defer conn.Close()
			_, _ = io.Copy(conn, conn)
		}
	}()
	reserved, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := reserved.Addr().(*net.TCPAddr).Port
	_ = reserved.Close()
	const password = "native-trojan-grpc"
	spec := InboundSpec{Config: core.InboundConfig{Protocol: "trojan", Listen: "127.0.0.1", Port: port, Raw: map[string]any{
		"network": "grpc", "grpc_service_name": "PandoraService",
	}}}
	adapterValue, err := newTrojanAdapter(spec)
	if err != nil {
		t.Fatal(err)
	}
	adapter := adapterValue.(*trojanAdapter)
	if err := adapter.Validate(spec); err != nil {
		t.Fatal(err)
	}
	if err := adapter.AddUsers([]core.User{{ID: 1000, UUID: password}}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := adapter.Start(ctx, spec, AdapterHooks{DataPlane: &vlessTestPlane{target: socksAddr(upstream.Addr())}}); err != nil {
		t.Fatal(err)
	}
	defer adapter.Close()
	pipeReader, pipeWriter := io.Pipe()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, fmt.Sprintf("http://127.0.0.1:%d/PandoraService/Tun", port), pipeReader)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/grpc")
	request.Header.Set("TE", "trailers")
	transport := &http2.Transport{AllowHTTP: true, DialTLSContext: func(ctx context.Context, network, address string, _ *tls.Config) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, address)
	}}
	defer transport.CloseIdleConnections()
	payload := []byte("native-trojan-grpc")
	header := []byte(trojanPasswordProof(password) + "\r\n")
	header = append(header, 1, 1, 127, 0, 0, 1, 0, 1, '\r', '\n')
	go func() {
		_ = writeGRPCFrame(pipeWriter, append(header, payload...))
		_ = pipeWriter.Close()
	}()
	response, err := transport.RoundTrip(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("grpc status=%d", response.StatusCode)
	}
	frame, err := readGRPCFrame(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(frame, payload) {
		t.Fatalf("grpc trojan response=%q", frame)
	}
}
