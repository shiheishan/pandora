package kernel

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aegispanel/nodeagent/core"
	"github.com/gofrs/uuid/v5"
	"github.com/sagernet/quic-go"
	"github.com/sagernet/quic-go/http3"
)

// 同一条 QUIC 连接上并发认证两次（S v3 对抗审查的 pdnd 子审查找出，原为探针）。
//   - TUIC：上游两条认证流各起 goroutine，都能过「还没认证」的检查，第二次
//     close(authDone) 直接 panic，一个合法用户就能打崩整个 pdnd。现在认证在
//     loopUniStreams 里串行，之后的认证流被拒并关连接。
//   - hy2：上游 authenticated / authUser 无同步，并发 /auth 让身份来回变（流量记到
//     另一个用户）并起两个 loopMessages。现在在 connAccess 内只成功一次，之后的
//     /auth 照旧回 OK 但不改身份。这条靠 -race 抓（CI 的 race 套件）。

func freeUDPPort(t *testing.T) int {
	t.Helper()
	probe, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := probe.LocalAddr().(*net.UDPAddr).Port
	_ = probe.Close()
	return port
}

// dialQUICRetry 拨号失败时重试：本机回环在 -race 下偶有握手包被丢、撞上握手空闲
// 超时；拨号本身不是被测对象。
func dialQUICRetry(t *testing.T, ctx context.Context, port int, tlsConf *tls.Config, conf *quic.Config) *quic.Conn {
	t.Helper()
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		dctx, dcancel := context.WithTimeout(ctx, 10*time.Second)
		conn, err := quic.DialAddr(dctx, "127.0.0.1:"+strconv.Itoa(port), tlsConf, conf)
		dcancel()
		if err == nil {
			return conn
		}
		lastErr = err
	}
	t.Fatal(lastErr)
	return nil
}

func TestTUICConcurrentDualAuthDoesNotPanic(t *testing.T) {
	certPath, keyPath := testXHTTPServerCertFiles(t)
	port := freeUDPPort(t)
	const idA = "5d6f0a52-6ae1-4e8e-91b3-1f269c8f5c7a"
	const idB = "6d6f0a52-6ae1-4e8e-91b3-1f269c8f5c7b"
	spec := InboundSpec{Config: core.InboundConfig{Protocol: "tuic", Listen: "127.0.0.1", Port: port, Raw: map[string]any{
		"cert_path": certPath, "key_path": keyPath, "network": "udp",
	}}}
	adapter, err := newTUICAdapter(spec)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := adapter.Start(ctx, spec, AdapterHooks{DataPlane: &hysteriaEchoPlane{}}); err != nil {
		t.Fatal(err)
	}
	defer adapter.Close()
	if err := adapter.AddUsers([]core.User{{ID: 1, UUID: idA}, {ID: 2, UUID: idB}}); err != nil {
		t.Fatal(err)
	}
	ua, _ := uuid.FromString(idA)
	ub, _ := uuid.FromString(idB)
	tlsConf := &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS13, ServerName: "localhost", NextProtos: []string{"h3"}}
	for _, tc := range []struct {
		name   string
		second uuid.UUID
		pw     string
	}{{"两个用户", ub, idB}, {"同一用户两次", ua, idA}} {
		t.Run(tc.name, func(t *testing.T) {
			for i := 0; i < 100; i++ {
				conn := dialQUICRetry(t, ctx, port, tlsConf, &quic.Config{})
				state := conn.ConnectionState()
				request := func(u uuid.UUID, pw string) []byte {
					token, err := state.TLS.ExportKeyingMaterial(string(u[:]), []byte(pw), 32)
					if err != nil {
						t.Fatal(err)
					}
					return append(append([]byte{5, 0}, u[:]...), token...)
				}
				first, second := request(ua, idA), request(tc.second, tc.pw)
				s1, err1 := conn.OpenUniStream()
				s2, err2 := conn.OpenUniStream()
				if err1 != nil || err2 != nil {
					t.Fatal(err1, err2)
				}
				var wg sync.WaitGroup
				wg.Add(2)
				go func() { defer wg.Done(); _, _ = s1.Write(first); _ = s1.Close() }()
				go func() { defer wg.Done(); _, _ = s2.Write(second); _ = s2.Close() }()
				wg.Wait()
				// 第二条认证流被拒、连接被服务端关掉。
				select {
				case <-conn.Context().Done():
				case <-time.After(3 * time.Second):
					t.Fatalf("第 %d 次：第二条认证流没被拒，连接仍开着", i+1)
				}
			}
		})
	}
}

func TestHysteria2ConcurrentDualAuthKeepsFirstUser(t *testing.T) {
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
	if err := adapter.AddUsers([]core.User{{ID: 1, UUID: "pw-a"}, {ID: 2, UUID: "pw-b"}}); err != nil {
		t.Fatal(err)
	}
	tlsConf := &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS13, ServerName: "localhost", NextProtos: []string{http3.NextProtoH3}}
	for i := 0; i < 100; i++ {
		conn := dialQUICRetry(t, ctx, port, tlsConf, &quic.Config{EnableDatagrams: true})
		cc := (&http3.Transport{}).NewClientConn(conn)
		var wg sync.WaitGroup
		var ok atomic.Int64
		for _, pw := range []string{"pw-a", "pw-b"} {
			wg.Add(1)
			go func(pw string) {
				defer wg.Done()
				req, _ := http.NewRequest(http.MethodPost, "https://hysteria/auth", nil)
				req.Header.Set("Hysteria-Auth", pw)
				req.Header.Set("Hysteria-CC-RX", "0")
				resp, err := cc.RoundTrip(req)
				if err == nil {
					if resp.StatusCode == 233 {
						ok.Add(1)
					}
					_ = resp.Body.Close()
				}
			}(pw)
		}
		wg.Wait()
		// 协议行为不变：已认证之后的 /auth 仍回 OK。
		if got := ok.Load(); got != 2 {
			t.Fatalf("第 %d 次：两次 /auth 只有 %d 次回 233", i+1, got)
		}
		_ = conn.CloseWithError(0, "")
	}
}
