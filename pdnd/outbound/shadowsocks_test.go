package outbound

import (
	"bytes"
	"context"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	M "github.com/sagernet/sing/common/metadata"
)

func shadowsocksOptions(dialer Dialer) Options {
	return Options{
		Tag:  "ss-relay",
		Type: "shadowsocks",
		Settings: map[string]any{
			"server":      "127.0.0.1",
			"server_port": 8388,
			"method":      "aes-128-gcm",
			"password":    "test-password",
		},
		Dialer: dialer,
	}
}

type recordingConn struct {
	bytes.Buffer
	closed bool
}

func (c *recordingConn) Read([]byte) (int, error)         { return 0, io.EOF }
func (c *recordingConn) Close() error                     { c.closed = true; return nil }
func (c *recordingConn) LocalAddr() net.Addr              { return &net.TCPAddr{} }
func (c *recordingConn) RemoteAddr() net.Addr             { return &net.TCPAddr{} }
func (c *recordingConn) SetDeadline(time.Time) error      { return nil }
func (c *recordingConn) SetReadDeadline(time.Time) error  { return nil }
func (c *recordingConn) SetWriteDeadline(time.Time) error { return nil }

func TestShadowsocksConstructorRejectsUnsupportedVariants(t *testing.T) {
	dialer := testDialer{dial: func(context.Context, string, M.Socksaddr) (net.Conn, error) {
		return nil, nil
	}}
	for _, tc := range []struct {
		name  string
		key   string
		value any
		want  string
	}{
		{name: "plugin", key: "plugin", value: "v2ray-plugin", want: "plugin"},
		{name: "tls", key: "tls", value: true, want: "ShadowTLS"},
		{name: "method", key: "method", value: "not-a-cipher", want: "not-a-cipher"},
		{name: "none", key: "method", value: "none", want: "不安全"},
		{name: "legacy stream", key: "method", value: "rc4-md5", want: "不安全"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts := shadowsocksOptions(dialer)
			opts.Settings[tc.key] = tc.value
			_, err := New(opts)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want substring %q", err, tc.want)
			}
		})
	}
}

func TestShadowsocksTCPDialsServerAndEncryptsFirstWrite(t *testing.T) {
	recording := &recordingConn{}
	var network, upstream string
	dialer := testDialer{dial: func(_ context.Context, gotNetwork string, destination M.Socksaddr) (net.Conn, error) {
		network, upstream = gotNetwork, destination.String()
		return recording, nil
	}}
	o, err := New(shadowsocksOptions(dialer))
	if err != nil {
		t.Fatal(err)
	}
	conn, err := o.DialTCP(context.Background(), M.ParseSocksaddrHostPort("target.example", 443))
	if err != nil {
		t.Fatal(err)
	}
	if network != "tcp" || upstream != "127.0.0.1:8388" {
		t.Fatalf("upstream dial = %s %s", network, upstream)
	}

	payload := []byte("plaintext-payload")
	if _, err := conn.Write(payload); err != nil {
		t.Fatal(err)
	}
	wire := recording.Bytes()
	if len(wire) <= len(payload) {
		t.Fatalf("encrypted request too short: %d", len(wire))
	}
	if bytes.Contains(wire, payload) {
		t.Fatal("plaintext payload appeared on Shadowsocks wire")
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	if !recording.closed {
		t.Fatal("Shadowsocks TCP wrapper did not close the upstream transport")
	}
}

func TestShadowsocksUDPUsesConnectedUpstreamAndFramesDestination(t *testing.T) {
	recording := &recordingConn{}
	var network, upstream string
	dialer := testDialer{dial: func(_ context.Context, gotNetwork string, destination M.Socksaddr) (net.Conn, error) {
		network, upstream = gotNetwork, destination.String()
		return recording, nil
	}}
	o, err := New(shadowsocksOptions(dialer))
	if err != nil {
		t.Fatal(err)
	}
	pc, err := o.ListenUDP(context.Background(), M.ParseSocksaddrHostPort("dns.example", 53))
	if err != nil {
		t.Fatal(err)
	}
	if network != "udp" || upstream != "127.0.0.1:8388" {
		t.Fatalf("upstream dial = %s %s", network, upstream)
	}

	payload := []byte("udp-plaintext")
	if _, err := pc.WriteTo(payload, &net.UDPAddr{IP: net.ParseIP("203.0.113.7"), Port: 53}); err != nil {
		t.Fatal(err)
	}
	wire := recording.Bytes()
	if len(wire) <= len(payload) || bytes.Contains(wire, payload) {
		t.Fatalf("unexpected Shadowsocks UDP wire payload: %x", wire)
	}
	if err := pc.Close(); err != nil && err != io.EOF {
		t.Fatal(err)
	}
	if !recording.closed {
		t.Fatal("Shadowsocks UDP wrapper did not close the upstream transport")
	}
}
