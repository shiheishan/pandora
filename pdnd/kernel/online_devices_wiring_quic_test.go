package kernel

// 在线设备接线的 QUIC 系（Hysteria2、TUIC、Juicity）与 AnyTLS 用例，跑法见
// online_devices_wiring_test.go。Hysteria2 / TUIC 按子流登记设备，Juicity 按整条
// QUIC 连接登记；AnyTLS 的参考客户端自带数据竞争（只在 interop 门里用），这里直接
// 把一条已认证的子流交给适配器的 NewConnectionEx，走的是同一段接客代码。

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"time"

	quic "github.com/apernet/quic-go"
	gofrsuuid "github.com/gofrs/uuid/v5"
	"github.com/google/uuid"
	hy2 "github.com/sagernet/sing-quic/hysteria2"
	tuic "github.com/sagernet/sing-quic/tuic"
	"github.com/sagernet/sing/common/auth"
	M "github.com/sagernet/sing/common/metadata"

	"github.com/aegispanel/nodeagent/outbound"
)

func deviceWiringQUICCases() []deviceWiringCase {
	quicRaw := deviceWiringTLSRaw(map[string]any{"network": "udp"})
	return []deviceWiringCase{
		{name: "hysteria2", proto: "hysteria2", uuid: "device-wiring-hy2", raw: quicRaw, open: openDeviceWiringHy2},
		{name: "tuic", proto: "tuic", uuid: "6b1f6f2e-2a43-4c38-9f36-6f7f3c0a7201", raw: quicRaw, open: openDeviceWiringTUIC},
		{name: "juicity", proto: "juicity", uuid: "6b1f6f2e-2a43-4c38-9f36-6f7f3c0a7202", raw: quicRaw, open: openDeviceWiringJuicity},
		{name: "anytls", proto: "anytls", uuid: "device-wiring-anytls", open: openDeviceWiringAnyTLS},
	}
}

func deviceWiringQUICClientTLS() *hysteria2TLSConfig {
	return &hysteria2TLSConfig{std: &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS13, ServerName: "localhost", NextProtos: []string{"h3"}}} //nolint:gosec -- ephemeral test certificate.
}

// streamThenClient 关一条子流（或 UDP 会话）时连同它独占的客户端（QUIC 连接）一起关。
func streamThenClient(stream io.Closer, closeClient func() error) io.Closer {
	return closerFunc(func() error { return errors.Join(stream.Close(), closeClient()) })
}

func openDeviceWiringHy2(env deviceWiringEnv) (io.Closer, error) {
	ctx := context.Background()
	client, err := hy2.NewClient(hy2.ClientOptions{
		Context: ctx, Dialer: outbound.NewDirect("test", outbound.StrategyPreferIPv4, nil),
		ServerAddress: M.ParseSocksaddrHostPort("127.0.0.1", uint16(env.port)),
		Password:      env.user.UUID, TLSConfig: deviceWiringQUICClientTLS(),
	})
	if err != nil {
		return nil, err
	}
	closeClient := func() error { return client.CloseWithError(nil) }
	dialCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	stream, err := client.DialConn(dialCtx, M.SocksaddrFromNet(env.target))
	if err != nil {
		_ = closeClient()
		return nil, err
	}
	if err := echoOnce(stream, "device-wiring"); err != nil {
		_ = stream.Close()
		_ = closeClient()
		return nil, err
	}
	return streamThenClient(stream, closeClient), nil
}

func openDeviceWiringTUIC(env deviceWiringEnv) (io.Closer, error) {
	ctx := context.Background()
	parsed, err := gofrsuuid.FromString(env.user.UUID)
	if err != nil {
		return nil, err
	}
	client, err := tuic.NewClient(tuic.ClientOptions{
		Context: ctx, Dialer: outbound.NewDirect("test", outbound.StrategyPreferIPv4, nil),
		ServerAddress: M.ParseSocksaddrHostPort("127.0.0.1", uint16(env.port)),
		TLSConfig:     deviceWiringQUICClientTLS(), UUID: [16]byte(parsed), Password: env.user.UUID,
	})
	if err != nil {
		return nil, err
	}
	closeClient := func() error { return client.CloseWithError(nil) }
	dialCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	stream, err := client.DialConn(dialCtx, M.SocksaddrFromNet(env.target))
	if err != nil {
		_ = closeClient()
		return nil, err
	}
	if err := echoOnce(stream, "device-wiring"); err != nil {
		_ = stream.Close()
		_ = closeClient()
		return nil, err
	}
	return streamThenClient(stream, closeClient), nil
}

func openDeviceWiringJuicity(env deviceWiringEnv) (io.Closer, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, err := quic.DialAddr(ctx, net.JoinHostPort("127.0.0.1", fmt.Sprint(env.port)), &tls.Config{
		InsecureSkipVerify: true, MinVersion: tls.VersionTLS13, NextProtos: []string{"h3"}, ServerName: "localhost", //nolint:gosec -- ephemeral test certificate.
	}, &quic.Config{HandshakeIdleTimeout: 3 * time.Second})
	if err != nil {
		return nil, err
	}
	closeConn := func() error { return conn.CloseWithError(0, "test complete") }
	fail := func(err error) (io.Closer, error) { _ = closeConn(); return nil, err }
	id, err := uuid.Parse(env.user.UUID)
	if err != nil {
		return fail(err)
	}
	state := conn.ConnectionState().TLS
	token, err := state.ExportKeyingMaterial(string(id[:]), []byte(env.user.UUID), 32)
	if err != nil {
		return fail(err)
	}
	authStream, err := conn.OpenUniStream()
	if err != nil {
		return fail(err)
	}
	authBytes := append([]byte{juicityVersion, juicityAuthenticate}, id[:]...)
	authBytes = append(authBytes, token...)
	if _, err := authStream.Write(authBytes); err != nil {
		return fail(err)
	}
	if err := authStream.Close(); err != nil {
		return fail(err)
	}
	stream, err := conn.OpenStream()
	if err != nil {
		return fail(err)
	}
	header := append([]byte{juicityNetworkTCP}, marshalJuicityAddress(juicityAddress{typ: 1, Host: env.target.IP.String(), Port: uint16(env.target.Port)})...)
	payload := []byte("device-wiring")
	if _, err := stream.Write(append(header, payload...)); err != nil {
		return fail(err)
	}
	_ = stream.SetReadDeadline(time.Now().Add(3 * time.Second))
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(stream, got); err != nil {
		return fail(err)
	}
	if string(got) != string(payload) {
		return fail(fmt.Errorf("echo=%q", got))
	}
	// Juicity 按整条 QUIC 连接登记设备：关这条连接才算这台「连接」离开。
	return closerFunc(closeConn), nil
}

func openDeviceWiringAnyTLS(env deviceWiringEnv) (io.Closer, error) {
	a, ok := env.adapter.(*anyTLSAdapter)
	if !ok {
		return nil, fmt.Errorf("adapter=%T", env.adapter)
	}
	client, server := net.Pipe()
	ctx := auth.ContextWithUser(context.Background(), env.user.UUID)
	a.NewConnectionEx(ctx, server, M.ParseSocksaddrHostPort("127.0.0.1", 40000), M.SocksaddrFromNet(env.target), nil)
	if err := echoOnce(client, "device-wiring"); err != nil {
		_ = client.Close()
		return nil, err
	}
	return client, nil
}
