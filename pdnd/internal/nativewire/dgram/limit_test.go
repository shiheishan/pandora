package dgram

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"math/big"
	"testing"
	"time"

	"github.com/sagernet/quic-go"
)

func selfSignedTLS(t *testing.T) *tls.Config {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), DNSNames: []string{"localhost"}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}, NextProtos: []string{"dgram-test"}, MinVersion: tls.VersionTLS13}
}

// quicPair 在回环上建一对开了 DATAGRAM 的 QUIC 连接。
func quicPair(t *testing.T, clientDatagrams bool) (client, server *quic.Conn) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	config := &quic.Config{EnableDatagrams: true, MaxIdleTimeout: 10 * time.Second}
	listener, err := quic.ListenAddr("127.0.0.1:0", selfSignedTLS(t), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	accepted := make(chan *quic.Conn, 1)
	go func() {
		conn, err := listener.Accept(ctx)
		if err == nil {
			accepted <- conn
		}
		close(accepted)
	}()
	clientConfig := config.Clone()
	clientConfig.EnableDatagrams = clientDatagrams
	client, err = quic.DialAddr(ctx, listener.Addr().String(), &tls.Config{InsecureSkipVerify: true, NextProtos: []string{"dgram-test"}}, clientConfig)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.CloseWithError(0, "") })
	server = <-accepted
	if server == nil {
		t.Fatal("服务端没接到连接")
	}
	t.Cleanup(func() { _ = server.CloseWithError(0, "") })
	return client, server
}

// 问上限不发包：quic-go 先比长度，超长立即报错，对端什么也收不到。
func TestLimitProbeSendsNothing(t *testing.T) {
	client, server := quicPair(t, true)
	var l Limit
	size := l.Size(server)
	if size <= 0 || size > 1452-AckMargin {
		t.Fatalf("上限 %d 不在 (0, %d] 内", size, 1452-AckMargin)
	}
	for i := 0; i < 20; i++ {
		l.Refresh(server)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if data, err := client.ReceiveDatagram(ctx); err == nil {
		t.Fatalf("问上限时对端收到了 %d 字节", len(data))
	}
}

// 贴着上限的 DATAGRAM 在双向都有流量（几乎每个包都捎带 ACK）时也不能被
// quic-go 丢掉：这正是要扣 AckMargin 的原因。
func TestLimitSizedDatagramsSurviveBidirectionalTraffic(t *testing.T) {
	client, server := quicPair(t, true)
	var clientLimit, serverLimit Limit
	const rounds = 200
	go func() {
		for {
			data, err := server.ReceiveDatagram(context.Background())
			if err != nil {
				return
			}
			reply := bytes.Repeat([]byte{data[0]}, serverLimit.Size(server))
			_ = server.SendDatagram(reply)
		}
	}()
	received := 0
	for i := 0; i < rounds; i++ {
		_ = client.SendDatagram(bytes.Repeat([]byte{byte(i)}, clientLimit.Size(client)))
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		data, err := client.ReceiveDatagram(ctx)
		cancel()
		if err == nil && data[0] == byte(i) {
			received++
		}
	}
	if received < rounds {
		t.Fatalf("贴着上限的回显只收到 %d/%d", received, rounds)
	}
}

// 对端没开 DATAGRAM，问不到上限：退回上游的 1197，不缓存。
func TestLimitFallbackWhenUnavailable(t *testing.T) {
	_, server := quicPair(t, false)
	var l Limit
	if got := l.Size(server); got != Fallback {
		t.Fatalf("问不到上限时 = %d，want %d", got, Fallback)
	}
	if l.size.Load() != 0 {
		t.Fatal("问不到的结果不该缓存")
	}
}
