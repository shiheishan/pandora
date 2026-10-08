package kernel

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

// TestXHTTPUnauthSessionsCannotPinMemory（审查 X1）：auto 节点上，未认证客户端用
// 随机会话 id 发 seq≥1 的 packet-up 包（永远不发 seq 0，所以永远到不了认证），
// 原先每个会话能钉住 30 包 × 1MB，会话数不设上限，到 10 秒请求头截止才释放：
// 20 个会话实测 heap 涨 219MB。未认证会话的待处理字节必须按会话、按入站封顶。
func TestXHTTPUnauthSessionsCannotPinMemory(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	base, _ := startAutoXHTTPVLESS(t, ctx, 7601, uuid.New())
	transport := h2cTransport()
	defer transport.CloseIdleConnections()
	const sessions, posts, size = 20, 10, 1_000_000
	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	body := bytes.Repeat([]byte{0xAA}, size)
	var wg sync.WaitGroup
	var mu sync.Mutex
	codes := map[int]int{}
	for s := 0; s < sessions; s++ {
		id := "pin-" + strconv.Itoa(s)
		for seq := 1; seq <= posts; seq++ {
			wg.Add(1)
			go func(id string, seq int) {
				defer wg.Done()
				req, _ := http.NewRequestWithContext(ctx, http.MethodPost, base+"/xhttp/"+id+"/"+strconv.Itoa(seq), bytes.NewReader(body))
				resp, err := transport.RoundTrip(req)
				if err != nil {
					return
				}
				_, _ = io.Copy(io.Discard, resp.Body)
				_ = resp.Body.Close()
				mu.Lock()
				codes[resp.StatusCode]++
				mu.Unlock()
			}(id, seq)
		}
	}
	wg.Wait()
	runtime.GC()
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	delta := (int64(after.HeapAlloc) - int64(before.HeapAlloc)) >> 20
	t.Logf("status=%v heap delta=%dMB", codes, delta)
	if delta > 48 {
		t.Fatalf("未认证会话钉住了 %dMB（状态码 %v）", delta, codes)
	}
	if codes[http.StatusOK] == sessions*posts {
		t.Fatalf("超出预算的上行包全被收下：%v", codes)
	}
}

// TestXHTTPPacketConnConcurrentWrites（审查 X3）：net.Conn 允许并发 Write。原先序号在
// 锁内分配、入队在锁外等，高序号占满下行队列后低序号进不去，读端等低序号——死锁。
func TestXHTTPPacketConnConcurrentWrites(t *testing.T) {
	for attempt := 0; attempt < 50; attempt++ {
		up, _ := NewXHTTPPacketQueue(2)
		down, _ := NewXHTTPPacketQueue(2)
		ctx, cancel := context.WithCancel(context.Background())
		conn := newXHTTPPacketConn(ctx, &XHTTPPacketDuplex{Uplink: up, Downlink: down})
		const writers, perWriter = 3, 200
		var wg sync.WaitGroup
		for w := 0; w < writers; w++ {
			wg.Add(1)
			go func(w int) {
				defer wg.Done()
				for i := 0; i < perWriter; i++ {
					if _, err := conn.Write([]byte{byte(w)}); err != nil {
						return
					}
				}
			}(w)
		}
		var readErr error
		for i := 0; i < writers*perWriter; i++ {
			rctx, rcancel := context.WithTimeout(context.Background(), 2*time.Second)
			_, err := down.Read(rctx)
			rcancel()
			if err != nil {
				readErr = err
				break
			}
		}
		cancel()
		wg.Wait()
		if readErr != nil {
			t.Fatalf("第 %d 次：下行读卡住（死锁）：%v", attempt, readErr)
		}
	}
}

// TestXHTTPAppendWaitCancelLeavesNoGap（审查 X4）：AppendWait 等位置时被取消，原先
// 序号已经占掉，留下空洞，之后排进去的数据永远读不到。
func TestXHTTPAppendWaitCancelLeavesNoGap(t *testing.T) {
	q, _ := NewXHTTPPacketQueue(1)
	bg := context.Background()
	if err := q.AppendWait(bg, []byte("a")); err != nil {
		t.Fatal(err)
	}
	short, cancel := context.WithTimeout(bg, 30*time.Millisecond)
	defer cancel()
	if err := q.AppendWait(short, []byte("b")); err == nil {
		t.Fatal("队列满且 ctx 到期，AppendWait 应返回错误")
	}
	if p, err := q.Read(bg); err != nil || string(p.Payload) != "a" {
		t.Fatalf("read = %q, %v", p.Payload, err)
	}
	if err := q.AppendWait(bg, []byte("c")); err != nil {
		t.Fatal(err)
	}
	rctx, rcancel := context.WithTimeout(bg, time.Second)
	defer rcancel()
	if p, err := q.Read(rctx); err != nil || string(p.Payload) != "c" {
		t.Fatalf("取消之后排进去的数据读不到：%q, %v（空洞）", p.Payload, err)
	}
}

// TestXHTTPSecondStreamUpRejected（审查 X5）：同一会话只许一条上行。原先第二条
// stream-up（或 packet-up 包）也被收下，知道会话 id 的第三方能往别人的上行里插数据。
// Xray 对第二条 stream-up 回 409。
func TestXHTTPSecondStreamUpRejected(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	id := uuid.New()
	base, _ := startAutoXHTTPVLESS(t, ctx, 7602, id)
	victim, attacker := h2cTransport(), h2cTransport()
	defer victim.CloseIdleConnections()
	defer attacker.CloseIdleConnections()
	session := "victim-" + uuid.NewString()
	getReq, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+"/xhttp/"+session+"/", nil)
	getResp, err := victim.RoundTrip(getReq)
	if err != nil {
		t.Fatal(err)
	}
	defer getResp.Body.Close()
	vR, vW := io.Pipe()
	defer vW.Close()
	vReq, _ := http.NewRequestWithContext(ctx, http.MethodPost, base+"/xhttp/"+session+"/", vR)
	go func() {
		if resp, err := victim.RoundTrip(vReq); err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}
	}()
	_, _ = vW.Write(append(vlessTCPRequestHeader(id), []byte("victim|")...))
	readVLESSEcho(t, getResp.Body, []byte("victim|"))
	for _, path := range []string{"/xhttp/" + session + "/", "/xhttp/" + session + "/5/"} {
		aReq, _ := http.NewRequestWithContext(ctx, http.MethodPost, base+path, bytes.NewReader([]byte("INJECTED")))
		aResp, err := attacker.RoundTrip(aReq)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, aResp.Body)
		_ = aResp.Body.Close()
		if aResp.StatusCode != http.StatusConflict {
			t.Fatalf("第二条上行 %s 回 %d，应为 409", path, aResp.StatusCode)
		}
	}
	got := make([]byte, 1)
	done := make(chan error, 1)
	go func() { _, err := io.ReadFull(getResp.Body, got); done <- err }()
	select {
	case err := <-done:
		t.Fatalf("受害方下行收到了注入的字节：%q, %v", got, err)
	case <-time.After(500 * time.Millisecond):
	}
}

// TestXHTTPErrorResponsesCarryNoText（审查 X8）：出错响应只给状态码、不带文本。原先
// 会话出错时把 Go 的错误文本写进响应体，是现成的指纹（Xray 只回状态码）。
func TestXHTTPErrorResponsesCarryNoText(t *testing.T) {
	c, err := ParseXHTTPConfig(map[string]any{"path": "/xhttp", "host": "edge.example"})
	if err != nil {
		t.Fatal(err)
	}
	server := XHTTPServer{Config: c, Handler: func(context.Context, XHTTPSession) error {
		return errors.New("internal detail")
	}}
	for _, tc := range []struct {
		name, method, url string
	}{
		{"host 不符", http.MethodPost, "https://other.example/xhttp/"},
		{"元数据无效", http.MethodPost, "https://edge.example/xhttp/s/notanumber/"},
		{"方法不符", http.MethodPut, "https://edge.example/xhttp/"},
		{"会话出错", http.MethodPost, "https://edge.example/xhttp/s/0/"},
	} {
		recorder := httptest.NewRecorder()
		server.ServeHTTP(recorder, httptest.NewRequest(tc.method, tc.url, bytes.NewReader([]byte("x"))))
		if recorder.Code < 400 || recorder.Body.Len() != 0 {
			t.Errorf("%s：status=%d body=%q", tc.name, recorder.Code, recorder.Body.String())
		}
	}
}
