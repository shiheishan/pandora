package hysteria2

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/aegispanel/nodeagent/internal/nativewire/hysteria2/internal/protocol"
	"github.com/sagernet/quic-go"
	"github.com/sagernet/quic-go/http3"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

// 认证前按「空闲」关连接（Pandora 改动，round-r5）：像真站一样，伪装站正常在服务、
// 有请求在途的连接不关；没有在途请求持续到空闲时限才关。

// slowMasquerade 在 /slow 上慢慢应答，其余立即 204。
var slowMasquerade = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/slow":
		time.Sleep(3 * time.Second)
	case "/body":
		// 请求体要读得到：读请求头的限时不能管到请求体。
		if body, err := io.ReadAll(r.Body); err != nil || string(body) != "late body" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
})

func masqueradeGet(t *testing.T, cc *http3.ClientConn, path string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://hy2.test"+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := cc.RoundTrip(req)
	if err != nil {
		t.Fatalf("伪装站请求 %s 失败：%v", path, err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("伪装站请求 %s 得到 %d", path, resp.StatusCode)
	}
}

func dialMasqueradeClient(t *testing.T, addr string) (*quic.Conn, *http3.ClientConn) {
	t.Helper()
	conn := dialPreAuth(t, addr)
	t.Cleanup(func() { _ = conn.CloseWithError(0, "") })
	return conn, (&http3.Transport{}).NewClientConn(conn)
}

// 不认证、但伪装站一直在正常服务的连接（浏览器访客）超过旧的 10 秒存活上限仍在：
// 先有一个比空闲时限还长的在途请求，再每 500ms 一个请求，持续 11 秒。
func TestPreAuthActiveMasqueradeConnSurvives(t *testing.T) {
	const idleTimeout = 2 * time.Second
	addr := startPreAuthTestService(t, preAuthTestOptions{idleTimeout: idleTimeout, masquerade: slowMasquerade})
	conn, cc := dialMasqueradeClient(t, addr)
	start := time.Now()
	masqueradeGet(t, cc, "/slow") // 在途 3 秒 > 空闲时限 2 秒
	for time.Since(start) < 11*time.Second {
		masqueradeGet(t, cc, "/")
		time.Sleep(500 * time.Millisecond)
	}
	if err := conn.Context().Err(); err != nil {
		t.Fatalf("伪装站正常服务中的连接在 %v 内被关：%v", time.Since(start), context.Cause(conn.Context()))
	}
	t.Logf("不认证的伪装站连接服务了 %v 仍在", time.Since(start))
}

// 没有在途请求的未认证连接空闲到点被关：握手后什么也不发的，与请求做完后不再发的各一条。
func TestPreAuthIdleConnClosed(t *testing.T) {
	const idleTimeout = 1 * time.Second
	addr := startPreAuthTestService(t, preAuthTestOptions{idleTimeout: idleTimeout, masquerade: slowMasquerade})
	type watched struct {
		name   string
		conn   *quic.Conn
		idleAt time.Time
		closed chan time.Time
	}
	watch := func(name string, conn *quic.Conn, idleAt time.Time) watched {
		w := watched{name: name, conn: conn, idleAt: idleAt, closed: make(chan time.Time, 1)}
		context.AfterFunc(conn.Context(), func() { w.closed <- time.Now() })
		return w
	}
	silent := dialPreAuth(t, addr)
	defer silent.CloseWithError(0, "")
	ws := []watched{watch("握手后不发", silent, time.Now())}
	conn, cc := dialMasqueradeClient(t, addr)
	masqueradeGet(t, cc, "/")
	ws = append(ws, watch("请求做完后不发", conn, time.Now()))
	for _, w := range ws {
		select {
		case at := <-w.closed:
			if idle := at.Sub(w.idleAt); idle < idleTimeout-300*time.Millisecond {
				t.Fatalf("%s：空闲时限 %v，连接空闲 %v 就被关", w.name, idleTimeout, idle)
			}
		case <-time.After(idleTimeout + 3*time.Second):
			t.Fatalf("%s：空闲时限 %v，连接到点仍未被关", w.name, idleTimeout)
		}
	}
}

// 读请求头的限时只管请求头：请求头读完之后，晚于限时才到的请求体照常读得到。
func TestPreAuthHeaderTimeoutSparesRequestBody(t *testing.T) {
	const headerTimeout = 1 * time.Second
	addr := startPreAuthTestService(t, preAuthTestOptions{headerTimeout: headerTimeout, masquerade: slowMasquerade})
	_, cc := dialMasqueradeClient(t, addr)
	pr, pw := io.Pipe()
	go func() {
		time.Sleep(headerTimeout + 500*time.Millisecond)
		_, _ = pw.Write([]byte("late body"))
		_ = pw.Close()
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://hy2.test/body", pr)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := cc.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("请求头读完后晚到的请求体读不到：得到 %d", resp.StatusCode)
	}
}

// echoHandler 把 TCP 请求的数据原样写回。
type echoHandler struct{ nopHandler }

func (echoHandler) NewConnectionEx(_ context.Context, conn net.Conn, _ M.Socksaddr, _ M.Socksaddr, _ N.CloseHandlerFunc) {
	defer conn.Close()
	_, _ = io.Copy(conn, conn)
}

// 收流时还没认证、认证之后才发完的 TCP 请求流不受读请求头限时约束：限时过后照常转发。
func TestPreAuthStreamUsedForTCPAfterAuth(t *testing.T) {
	const headerTimeout = 1 * time.Second
	addr := startPreAuthTestService(t, preAuthTestOptions{headerTimeout: headerTimeout, handler: echoHandler{}})
	conn, cc := dialMasqueradeClient(t, addr)
	request := protocol.WriteTCPRequest("127.0.0.1:80", nil)
	defer request.Release()
	head := request.Bytes()
	str, err := conn.OpenStream()
	if err != nil {
		t.Fatal(err)
	}
	// 只发帧类型的第一个字节：服务端在认证前收下这条流，Peek 帧类型等着。
	if _, err := str.Write(head[:1]); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	req, _ := http.NewRequest(http.MethodPost, "https://"+protocol.URLHost+protocol.URLPath, nil)
	protocol.AuthRequestToHeader(req.Header, protocol.AuthRequest{Auth: testAuthPassword})
	resp, err := cc.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != protocol.StatusAuthOK {
		t.Fatalf("认证得到 %d", resp.StatusCode)
	}
	if _, err := str.Write(head[1:]); err != nil {
		t.Fatal(err)
	}
	time.Sleep(headerTimeout + 500*time.Millisecond)
	if _, err := str.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	_ = str.SetReadDeadline(time.Now().Add(5 * time.Second))
	ok, msg, err := protocol.ReadTCPResponse(str)
	if err != nil || !ok {
		t.Fatalf("TCP 应答：ok=%v msg=%q err=%v", ok, msg, err)
	}
	got := make([]byte, 4)
	if _, err := io.ReadFull(str, got); err != nil || !bytes.Equal(got, []byte("ping")) {
		t.Fatalf("读请求头限时之后 TCP 转发断了：%q %v", got, err)
	}
}
