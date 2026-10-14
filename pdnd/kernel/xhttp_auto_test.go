package kernel

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/aegispanel/nodeagent/core"
	"github.com/google/uuid"
	M "github.com/sagernet/sing/common/metadata"
	"golang.org/x/net/http2"
)

// startAutoXHTTPVLESS 起一个不写 mode（缺省 auto）的 VLESS+XHTTP 明文入站，上游是回显。
func startAutoXHTTPVLESS(t *testing.T, ctx context.Context, userID int64, id uuid.UUID) (string, *vlessAdapter) {
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
	reserved, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := reserved.Addr().(*net.TCPAddr).Port
	_ = reserved.Close()
	spec := InboundSpec{Config: core.InboundConfig{Protocol: "vless", Listen: "127.0.0.1", Port: port, Raw: map[string]any{
		"network": "xhttp", "path": "/xhttp", "security": "none",
	}}}
	adapterValue, err := newVLESSAdapter(spec)
	if err != nil {
		t.Fatal(err)
	}
	adapter := adapterValue.(*vlessAdapter)
	if err := adapter.Validate(spec); err != nil {
		t.Fatal(err)
	}
	if err := adapter.AddUsers([]core.User{{ID: userID, UUID: id.String()}}); err != nil {
		t.Fatal(err)
	}
	if err := adapter.Start(ctx, spec, AdapterHooks{DataPlane: &vlessTestPlane{target: M.SocksaddrFromNet(upstream.Addr().(*net.TCPAddr)).Unwrap()}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = adapter.Close() })
	return "http://127.0.0.1:" + strconv.Itoa(port), adapter
}

func h2cTransport() *http2.Transport {
	return &http2.Transport{
		AllowHTTP: true,
		DialTLSContext: func(ctx context.Context, network, address string, _ *tls.Config) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, address)
		},
	}
}

func vlessTCPRequestHeader(id uuid.UUID) []byte {
	header := append([]byte{vlessVersion}, id[:]...)
	return append(header, 0, vlessTCP, 0x01, 0xbb, 1, 127, 0, 0, 1)
}

// readVLESSEcho 读 VLESS 响应头（2 字节）与 want 长度的回显。
func readVLESSEcho(t *testing.T, r io.Reader, want []byte) {
	t.Helper()
	got := make([]byte, 2+len(want))
	if _, err := io.ReadFull(r, got); err != nil {
		t.Fatalf("读回显: %v", err)
	}
	if got[0] != vlessVersion || got[1] != 0 || !bytes.Equal(got[2:], want) {
		t.Fatalf("回显不符：头=%v 长度=%d", got[:2], len(got)-2)
	}
}

// TestXHTTPAutoAcceptsEveryClientMode：节点缺省 mode=auto 时，客户端自己选模式。
// Xray 在 REALITY 上选 stream-one（带 downloadSettings 选 stream-up），其余选
// packet-up；mihomo 同样。10-08 真节点测试里 auto 节点只收 Pandora 自己的写法，
// Xray / mihomo 一律 400。三种模式都要通。
func TestXHTTPAutoAcceptsEveryClientMode(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	id := uuid.New()
	base, adapter := startAutoXHTTPVLESS(t, ctx, 7301, id)
	transport := h2cTransport()
	defer transport.CloseIdleConnections()

	t.Run("stream-one 大于 1MB 的上行不被截断", func(t *testing.T) {
		// 以前 sc_max_each_post_bytes（缺省 1MB）套在整条 stream-one 请求体上。
		payload := make([]byte, 3<<20)
		_, _ = rand.Read(payload)
		bodyR, bodyW := io.Pipe()
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/xhttp/", bodyR)
		if err != nil {
			t.Fatal(err)
		}
		go func() {
			_, _ = bodyW.Write(vlessTCPRequestHeader(id))
			_, _ = bodyW.Write(payload)
		}()
		resp, err := transport.RoundTrip(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status=%d", resp.StatusCode)
		}
		readVLESSEcho(t, resp.Body, payload)
		_ = bodyW.Close()
	})

	t.Run("packet-up", func(t *testing.T) {
		session := "auto-packet-" + uuid.NewString()
		getReq, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/xhttp/"+session+"/", nil)
		if err != nil {
			t.Fatal(err)
		}
		getResp, err := transport.RoundTrip(getReq)
		if err != nil {
			t.Fatal(err)
		}
		defer getResp.Body.Close()
		if getResp.StatusCode != http.StatusOK {
			t.Fatalf("下行 GET status=%d", getResp.StatusCode)
		}
		first, second := []byte("packet-up-first|"), []byte("packet-up-second")
		for seq, body := range [][]byte{append(vlessTCPRequestHeader(id), first...), second} {
			postReq, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/xhttp/"+session+"/"+strconv.Itoa(seq)+"/", bytes.NewReader(body))
			if err != nil {
				t.Fatal(err)
			}
			postResp, err := transport.RoundTrip(postReq)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = io.Copy(io.Discard, postResp.Body)
			_ = postResp.Body.Close()
			if postResp.StatusCode != http.StatusOK {
				t.Fatalf("上行包 seq=%d status=%d", seq, postResp.StatusCode)
			}
		}
		readVLESSEcho(t, getResp.Body, append(first, second...))
	})

	t.Run("stream-up", func(t *testing.T) {
		session := "auto-stream-up-" + uuid.NewString()
		getReq, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/xhttp/"+session+"/", nil)
		if err != nil {
			t.Fatal(err)
		}
		getResp, err := transport.RoundTrip(getReq)
		if err != nil {
			t.Fatal(err)
		}
		defer getResp.Body.Close()
		bodyR, bodyW := io.Pipe()
		postReq, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/xhttp/"+session+"/", bodyR)
		if err != nil {
			t.Fatal(err)
		}
		postDone := make(chan error, 1)
		go func() {
			resp, err := transport.RoundTrip(postReq)
			if err == nil {
				_, _ = io.Copy(io.Discard, resp.Body)
				_ = resp.Body.Close()
				if resp.StatusCode != http.StatusOK {
					err = &net.AddrError{Err: "stream-up status " + strconv.Itoa(resp.StatusCode)}
				}
			}
			postDone <- err
		}()
		// 上行分几次写，回显要能边写边读：stream-up 的上行是一条流，不是一个包。
		if _, err := bodyW.Write(append(vlessTCPRequestHeader(id), []byte("stream-up-1|")...)); err != nil {
			t.Fatal(err)
		}
		readVLESSEcho(t, getResp.Body, []byte("stream-up-1|"))
		payload := make([]byte, 2<<20)
		_, _ = rand.Read(payload)
		go func() { _, _ = bodyW.Write(payload); _ = bodyW.Close() }()
		got := make([]byte, len(payload))
		if _, err := io.ReadFull(getResp.Body, got); err != nil {
			t.Fatalf("读 stream-up 大块回显: %v", err)
		}
		if !bytes.Equal(got, payload) {
			t.Fatal("stream-up 大块回显不符")
		}
		if err := <-postDone; err != nil {
			t.Fatalf("stream-up 上行请求: %v", err)
		}
	})

	t.Run("会话结束后不留在会话中转里", func(t *testing.T) {
		deadline := time.Now().Add(5 * time.Second)
		for {
			adapter.xhttpBroker.mu.Lock()
			left := len(adapter.xhttpBroker.sessions)
			adapter.xhttpBroker.mu.Unlock()
			if left == 0 {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("会话中转里还剩 %d 个会话", left)
			}
			transport.CloseIdleConnections()
			time.Sleep(50 * time.Millisecond)
		}
	})
}

// TestXHTTPPacketQueuePushWaitBlocksInsteadOfFailing：下行与 stream-up 上行是本端
// 产生的流，队列满时要等读端，不能像 Push 那样报「队列已满」把连接打断。
func TestXHTTPPacketQueuePushWaitBlocksInsteadOfFailing(t *testing.T) {
	queue, err := NewXHTTPPacketQueue(2)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for i := 0; i < 2; i++ {
		if err := queue.AppendWait(ctx, []byte{byte(i)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := queue.Push(XHTTPPacket{Seq: 2, Payload: []byte{2}}); err == nil {
		t.Fatal("满队列的 Push 应当报错（packet-up 线上包的既有语义）")
	}
	done := make(chan error, 1)
	go func() { done <- queue.AppendWait(ctx, []byte{2}) }()
	select {
	case err := <-done:
		t.Fatalf("队列满时 AppendWait 没有等待：%v", err)
	case <-time.After(50 * time.Millisecond):
	}
	for want := byte(0); want < 3; want++ {
		packet, err := queue.Read(ctx)
		if err != nil || packet.Payload[0] != want {
			t.Fatalf("read %d: %v %v", want, packet.Payload, err)
		}
		if want == 0 {
			if err := <-done; err != nil {
				t.Fatalf("腾出位置后 AppendWait: %v", err)
			}
		}
	}
	waitCtx, waitCancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer waitCancel()
	_ = queue.AppendWait(context.Background(), []byte{3})
	_ = queue.AppendWait(context.Background(), []byte{4})
	if err := queue.AppendWait(waitCtx, []byte{5}); err == nil {
		t.Fatal("ctx 到期时 AppendWait 应返回错误")
	}
}
