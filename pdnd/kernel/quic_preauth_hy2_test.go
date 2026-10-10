package kernel

import (
	"context"
	"crypto/tls"
	"runtime"
	"testing"
	"time"

	"github.com/aegispanel/nodeagent/core"
	hy2 "github.com/aegispanel/nodeagent/internal/nativewire/hysteria2"
	"github.com/sagernet/quic-go"
	"github.com/sagernet/quic-go/http3"
)

// hy2 认证前的放大（Pandora 改动的守卫）：hy2 是 HTTP/3 服务端，认证前的请求按
// 伪装站点处理，http3 每接一条双向流就起一个 goroutine 读请求头。上游
// MaxIncomingStreams 是 1<<60：只完成握手、不认证的客户端开流不发完请求头，就能
// 让服务端堆起任意多的 goroutine，而且 hy2 没有认证超时，连接不断就一直挂着。
// 现在流数受 ServerMaxIncomingStreams 约束（与 Hysteria 官方服务端缺省一致），
// 认证前同时在途的请求至多几十条、多出的当场拒掉，读请求头超时只拒那条流，
// 不认证的连接没有在途请求、空闲到点被关（nativewire/hysteria2 的 preauth_test.go
// 量回落）。这里经生产适配器量一条连接。
func TestHysteria2PreAuthStreamFloodIsBounded(t *testing.T) {
	certPath, keyPath := testXHTTPServerCertFiles(t)
	port := freeUDPPort(t)
	spec := InboundSpec{Config: core.InboundConfig{Protocol: "hysteria2", Listen: "127.0.0.1", Port: port, Raw: map[string]any{
		"cert_path": certPath, "key_path": keyPath, "network": "udp",
	}}}
	adapter, err := newHysteria2Adapter(spec)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := adapter.Start(ctx, spec, AdapterHooks{DataPlane: &hysteriaEchoPlane{}}); err != nil {
		t.Fatal(err)
	}
	defer adapter.Close()
	if err := adapter.AddUsers([]core.User{{ID: 1, UUID: "pw-a"}}); err != nil {
		t.Fatal(err)
	}
	runtime.GC()
	baseG := runtime.NumGoroutine()
	tlsConf := &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS13, ServerName: "localhost", NextProtos: []string{http3.NextProtoH3}}
	conn := dialQUICRetry(t, ctx, port, tlsConf, &quic.Config{EnableDatagrams: true})
	defer conn.CloseWithError(0, "")
	// HEADERS 帧头，声明 100 字节却一个也不发：服务端读请求头的 goroutine 一直等。
	partialHeaders := []byte{0x01, 0x40, 0x64}
	const attempts = 4000
	opened := 0
	for i := 0; i < attempts; i++ {
		s, err := conn.OpenStream()
		if err != nil {
			break
		}
		if _, err := s.Write(partialHeaders); err != nil {
			break
		}
		opened++
	}
	time.Sleep(time.Second)
	extra := runtime.NumGoroutine() - baseG
	t.Logf("开出双向流 %d；服务端多出 goroutine %d", opened, extra)
	// 阈值写死而不引用常量：每连接认证前在途至多几十条请求，加控制流与收流循环。
	if extra > 64 || hy2.ServerMaxIncomingStreams > 1024 {
		t.Fatalf("认证前 goroutine 多出 %d 个", extra)
	}
}
