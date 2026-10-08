package kernel

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aegispanel/nodeagent/core"
)

// ============================================================
//  测试用 slog 处理器：把记录收进切片，按消息查
// ============================================================

type capturedLog struct {
	msg   string
	attrs map[string]string
}

type captureHandler struct {
	mu      sync.Mutex
	records []capturedLog
}

func (h *captureHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *captureHandler) WithAttrs([]slog.Attr) slog.Handler       { return h }
func (h *captureHandler) WithGroup(string) slog.Handler            { return h }
func (h *captureHandler) Handle(_ context.Context, r slog.Record) error {
	rec := capturedLog{msg: r.Message, attrs: make(map[string]string)}
	r.Attrs(func(a slog.Attr) bool {
		rec.attrs[a.Key] = a.Value.String()
		return true
	})
	h.mu.Lock()
	h.records = append(h.records, rec)
	h.mu.Unlock()
	return nil
}

func (h *captureHandler) byMessage(msg string) []capturedLog {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []capturedLog
	for _, rec := range h.records {
		if rec.msg == msg {
			out = append(out, rec)
		}
	}
	return out
}

func waitForLogs(t *testing.T, h *captureHandler, msg string, n int) []capturedLog {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		got := h.byMessage(msg)
		if len(got) >= n {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("等 %d 条 %q 日志，只等到 %d 条: %+v", n, msg, len(got), got)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// ============================================================
//  限流
// ============================================================

func TestConnErrorLogSinkRateLimitsPerKeyAndSummarizes(t *testing.T) {
	h := &captureHandler{}
	sink := newConnErrorLogSink(slog.New(h), 2, 50*time.Millisecond)
	remote := &net.TCPAddr{IP: net.ParseIP("192.0.2.10"), Port: 40000}
	for i := 0; i < 10; i++ {
		sink.Report(ConnError{Tag: "edge-a", Protocol: "vless", Stage: StageSession, Remote: remote, Err: errors.New("vless version 无效")})
	}
	// 同一入站另一个分类有自己的配额，不被扫描器刷爆的 protocol 桶挤掉。
	sink.Report(ConnError{Tag: "edge-a", Protocol: "vless", Stage: StageSession, Remote: remote, Err: markConnError(connErrAuth, errors.New("vless 用户未授权"))})

	events := h.byMessage("入站连接失败")
	if len(events) != 3 {
		t.Fatalf("窗口内应逐条记 2+1 条，实际 %d: %+v", len(events), events)
	}
	if events[2].attrs["category"] != connErrAuth || events[0].attrs["category"] != connErrProtocol {
		t.Fatalf("分类字段不对: %+v", events)
	}
	if events[0].attrs["remote"] != "192.0.2.0/24" || strings.Contains(events[0].attrs["remote"], "192.0.2.10") {
		t.Fatalf("对端地址没有截成网段: %+v", events[0].attrs)
	}

	summary := waitForLogs(t, h, "入站连接失败已限流", 1)
	if len(summary) != 1 || summary[0].attrs["suppressed"] != "8" || summary[0].attrs["category"] != connErrProtocol {
		t.Fatalf("摘要应只有 protocol 桶、抑制 8 条: %+v", summary)
	}

	// 窗口过后配额恢复。
	sink.Report(ConnError{Tag: "edge-a", Protocol: "vless", Stage: StageSession, Err: errors.New("vless version 无效")})
	if got := len(h.byMessage("入站连接失败")); got != 4 {
		t.Fatalf("新窗口的第一条没有记下来，共 %d 条", got)
	}
	sink.Close()
}

func TestConnErrorLogSinkCloseFlushesAndStops(t *testing.T) {
	h := &captureHandler{}
	sink := newConnErrorLogSink(slog.New(h), 1, time.Hour)
	for i := 0; i < 4; i++ {
		sink.Report(ConnError{Tag: "edge-b", Protocol: "trojan", Stage: StageTLSHandshake, Err: errors.New("tls: bad record MAC")})
	}
	sink.Close()
	summary := h.byMessage("入站连接失败已限流")
	if len(summary) != 1 || summary[0].attrs["suppressed"] != "3" || summary[0].attrs["category"] != connErrTLS {
		t.Fatalf("Close 应补打最后一轮摘要: %+v", summary)
	}
	sink.Report(ConnError{Tag: "edge-b", Protocol: "trojan", Stage: StageTLSHandshake, Err: errors.New("late")})
	sink.Close()
	if got := len(h.byMessage("入站连接失败")); got != 1 {
		t.Fatalf("Close 之后不应再记: %d", got)
	}
}

func TestConnErrorLogSinkConcurrentReports(t *testing.T) {
	h := &captureHandler{}
	sink := newConnErrorLogSink(slog.New(h), 3, 20*time.Millisecond)
	var wg sync.WaitGroup
	for g := 0; g < 16; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				sink.Report(ConnError{Tag: "edge-c", Protocol: "socks", Stage: StageSession, Err: errors.New("socks version 9 unsupported")})
			}
		}()
	}
	wg.Wait()
	sink.Close()
	logged := len(h.byMessage("入站连接失败"))
	suppressed := 0
	for _, rec := range h.byMessage("入站连接失败已限流") {
		var n int
		for _, c := range rec.attrs["suppressed"] {
			n = n*10 + int(c-'0')
		}
		suppressed += n
	}
	if logged+suppressed != 16*200 {
		t.Fatalf("逐条 %d + 抑制 %d != 上报总数 %d", logged, suppressed, 16*200)
	}
}

// slowSummaryHandler 写「已限流」摘要时先报到、再慢一拍：用来把「窗口到点的
// flush 正在写摘要」这一刻钉住。
type slowSummaryHandler struct {
	captureHandler
	entered chan struct{}
	once    sync.Once
}

func (h *slowSummaryHandler) Handle(ctx context.Context, r slog.Record) error {
	if r.Message == "入站连接失败已限流" {
		h.once.Do(func() { close(h.entered) })
		time.Sleep(100 * time.Millisecond)
	}
	return h.captureHandler.Handle(ctx, r)
}

// TestConnErrorLogSinkCloseWaitsForFlush：TestConnErrorLogSinkConcurrentReports 在检查机
// 的 race 下偶发「逐条 + 抑制 != 上报总数」。根因不在计时本身：窗口到点的 flush 先在
// 锁内取走计数、再在锁外写摘要；Close 若恰好落在这两步之间，看到的是空计数，直接
// 返回，那一轮摘要还没写完。现在 Close 等在途的 flush 写完再返回。这里把 flush 钉在
// 「正在写摘要」的那一刻再 Close，不靠调度运气。
func TestConnErrorLogSinkCloseWaitsForFlush(t *testing.T) {
	h := &slowSummaryHandler{entered: make(chan struct{})}
	sink := newConnErrorLogSink(slog.New(h), 1, time.Millisecond)
	for i := 0; i < 6; i++ {
		sink.Report(ConnError{Tag: "edge-c", Protocol: "socks", Stage: StageSession, Err: errors.New("socks version 9 unsupported")})
	}
	select {
	case <-h.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("窗口到点后没有写摘要")
	}
	sink.Close()
	summaries := h.byMessage("入站连接失败已限流")
	if len(summaries) != 1 || summaries[0].attrs["suppressed"] != "5" {
		t.Fatalf("Close 返回时在途那一轮摘要还没写完：%+v", summaries)
	}
}

// ============================================================
//  生产接线：NativeCore 的两处 adapter.Start 都带上日志出口
// ============================================================

func TestNativeCoreLogsInboundConnErrors(t *testing.T) {
	h := &captureHandler{}
	nc := NewNativeCoreWithLogger(nil, slog.New(h))
	if err := nc.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	port := reserveTCPPort(t)
	const knownUser = "6f1e0c1a-2b3d-4c5e-8f70-9a8b7c6d5e4f"
	if err := nc.AddInbound(&core.InboundConfig{Tag: "edge-vless", Protocol: "vless", Listen: "127.0.0.1", Port: port}); err != nil {
		t.Fatal(err)
	}
	if err := nc.AddUsers("edge-vless", []core.User{{ID: 7, UUID: knownUser}}); err != nil {
		t.Fatal(err)
	}
	// 一个不在用户表里的 UUID：日志里必须看得到「auth」，但看不到这个 UUID。
	unknown := [16]byte{0xde, 0xad, 0xbe, 0xef, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12}
	sendRaw(t, port, append([]byte{vlessVersion}, unknown[:]...))

	got := waitForLogs(t, h, "入站连接失败", 1)[0]
	want := map[string]string{"inbound": "edge-vless", "protocol": "vless", "stage": StageSession, "category": connErrAuth, "remote": "127.0.0.0/24"}
	for key, value := range want {
		if got.attrs[key] != value {
			t.Fatalf("字段 %s = %q, want %q（全部: %+v）", key, got.attrs[key], value, got.attrs)
		}
	}
	for _, value := range got.attrs {
		if strings.Contains(value, "deadbeef") || strings.Contains(value, knownUser) || strings.Contains(value, "127.0.0.1") {
			t.Fatalf("日志泄露了凭据或完整地址: %+v", got.attrs)
		}
	}
}

// sendRaw 连上端口写一段字节，然后等服务端关连接。
func sendRaw(t *testing.T, port int, payload []byte) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", itoa(port)), 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.Write(payload); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, 512)
	for {
		if _, err := conn.Read(buf); err != nil {
			return
		}
	}
}
