package mkcp

import (
	"bytes"
	"io"
	"math/rand"
	"net"
	"sync"
	"testing"
	"time"
)

// pipeWriter 把一端写出的段直接投进另一端的 Input，中间可以按需丢包。
//
// 用内存管道而不是真 UDP：丢包要能精确控制。真 socket 上想复现「第 3 个
// 段丢了」只能靠运气，而重传逻辑恰恰只在丢包时才走到。
type pipeWriter struct {
	mu   sync.Mutex
	peer *Connection
	// drop 返回 true 时这个段被丢掉。调用时持有 pipeWriter 的锁。
	drop func(seg Segment) bool
	// sent 统计写出的段数，用来断言「没有疯狂重发」这类行为。
	sent int
}

func (w *pipeWriter) Write(seg Segment) error {
	w.mu.Lock()
	peer, drop := w.peer, w.drop
	w.sent++
	w.mu.Unlock()

	if peer == nil {
		return nil
	}
	if drop != nil && drop(seg) {
		return nil
	}
	// 序列化再解析一遍，走真实的编解码路径。直接把对象传过去会掩盖
	// 掉线格式上的问题——那正是最要命的一类。
	buf := make([]byte, seg.ByteSize())
	if err := seg.Serialize(buf); err != nil {
		return err
	}
	parsed, _ := ReadSegment(buf)
	if parsed == nil {
		return nil
	}
	// 异步投递：Input 会拿对端的锁，同步调用在双向流量下会死锁。
	go peer.Input([]Segment{parsed})
	return nil
}

func (w *pipeWriter) setPeer(c *Connection) {
	w.mu.Lock()
	w.peer = c
	w.mu.Unlock()
}

func (w *pipeWriter) count() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.sent
}

type nopCloser struct{}

func (nopCloser) Close() error { return nil }

// newPair 建一对互联的连接。
func newPair(t *testing.T, cfg func(*Config)) (*Connection, *Connection, *pipeWriter, *pipeWriter) {
	t.Helper()
	makeConfig := func() *Config {
		c := DefaultConfig()
		c.Tick = 5 * time.Millisecond // 测试里跑快一点
		if cfg != nil {
			cfg(c)
		}
		return c
	}
	aOut, bOut := &pipeWriter{}, &pipeWriter{}
	addrA := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 1}
	addrB := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 2}

	a := NewConnection(ConnMetadata{LocalAddr: addrA, RemoteAddr: addrB, Conversation: 42},
		aOut, nopCloser{}, makeConfig())
	b := NewConnection(ConnMetadata{LocalAddr: addrB, RemoteAddr: addrA, Conversation: 42},
		bOut, nopCloser{}, makeConfig())
	aOut.setPeer(b)
	bOut.setPeer(a)

	t.Cleanup(func() {
		_ = a.Close()
		_ = b.Close()
	})
	return a, b, aOut, bOut
}

// readFull 在超时内读满 n 个字节。
func readFull(t *testing.T, c *Connection, n int, within time.Duration) []byte {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(within))
	out := make([]byte, n)
	if _, err := io.ReadFull(c, out); err != nil {
		t.Fatalf("读取失败：%v", err)
	}
	return out
}

// 最基本的一条：写进去的字节能原样读出来。
func TestConnectionRoundTrip(t *testing.T) {
	a, b, _, _ := newPair(t, nil)

	payload := []byte("pandora mkcp round trip")
	if _, err := a.Write(payload); err != nil {
		t.Fatal(err)
	}
	if got := readFull(t, b, len(payload), 3*time.Second); !bytes.Equal(got, payload) {
		t.Fatalf("收到 %q，期望 %q", got, payload)
	}
}

// 双向同时收发，验证两个方向的窗口互不干扰。
func TestConnectionBidirectional(t *testing.T) {
	a, b, _, _ := newPair(t, nil)

	fromA := []byte("message going from A to B")
	fromB := []byte("and this one goes back")

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _, _ = a.Write(fromA) }()
	go func() { defer wg.Done(); _, _ = b.Write(fromB) }()
	wg.Wait()

	if got := readFull(t, b, len(fromA), 3*time.Second); !bytes.Equal(got, fromA) {
		t.Errorf("B 收到 %q", got)
	}
	if got := readFull(t, a, len(fromB), 3*time.Second); !bytes.Equal(got, fromB) {
		t.Errorf("A 收到 %q", got)
	}
}

// 超过 MSS 的数据要被切成多段，再在对端拼回来。
func TestConnectionSegmentsLargePayload(t *testing.T) {
	a, b, _, _ := newPair(t, func(c *Config) { c.MTU = 100 }) // MSS = 82
	payload := bytes.Repeat([]byte("0123456789"), 50)         // 500 字节，要切 7 段

	go func() { _, _ = a.Write(payload) }()
	if got := readFull(t, b, len(payload), 5*time.Second); !bytes.Equal(got, payload) {
		t.Fatalf("拼回来的数据不一致：%d 字节", len(got))
	}
}

// 丢包后靠重传补上。这是 mKCP 存在的全部理由——UDP 上不重传就是丢数据。
func TestConnectionRecoversFromPacketLoss(t *testing.T) {
	a, b, aOut, _ := newPair(t, func(c *Config) { c.MTU = 100 })

	// 丢掉前 3 个数据段，之后放行。重传必须把它们补回来。
	var dropped int
	aOut.mu.Lock()
	aOut.drop = func(seg Segment) bool {
		if _, ok := seg.(*DataSegment); ok && dropped < 3 {
			dropped++
			return true
		}
		return false
	}
	aOut.mu.Unlock()

	payload := bytes.Repeat([]byte("abcdefghij"), 40) // 400 字节
	go func() { _, _ = a.Write(payload) }()

	if got := readFull(t, b, len(payload), 10*time.Second); !bytes.Equal(got, payload) {
		t.Fatalf("丢包后数据没补齐：收到 %d 字节，期望 %d", len(got), len(payload))
	}
	if dropped != 3 {
		t.Errorf("实际丢了 %d 个段，期望 3 个", dropped)
	}
}

// 随机丢包下也要保序、不丢字节。
func TestConnectionSurvivesRandomLoss(t *testing.T) {
	if testing.Short() {
		t.Skip("随机丢包测试需要多轮重传")
	}
	a, b, aOut, _ := newPair(t, func(c *Config) { c.MTU = 100 })

	rng := rand.New(rand.NewSource(1)) // 固定种子，失败可复现
	var mu sync.Mutex
	aOut.mu.Lock()
	aOut.drop = func(seg Segment) bool {
		if _, ok := seg.(*DataSegment); !ok {
			return false
		}
		mu.Lock()
		defer mu.Unlock()
		return rng.Intn(100) < 30 // 30% 丢包
	}
	aOut.mu.Unlock()

	payload := bytes.Repeat([]byte("xyz"), 200) // 600 字节
	go func() { _, _ = a.Write(payload) }()

	if got := readFull(t, b, len(payload), 20*time.Second); !bytes.Equal(got, payload) {
		t.Fatalf("30%% 丢包下数据错乱")
	}
}

// 本地 Close 之后，对端读到 EOF 而不是一直挂着。
func TestConnectionCloseNotifiesPeer(t *testing.T) {
	a, b, _, _ := newPair(t, nil)

	payload := []byte("last words")
	if _, err := a.Write(payload); err != nil {
		t.Fatal(err)
	}
	readFull(t, b, len(payload), 3*time.Second)

	if err := a.Close(); err != nil {
		t.Fatalf("关闭失败：%v", err)
	}

	// 对端应当在几个 tick 内感知到，Read 返回 EOF
	_ = b.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, 16)
	if _, err := b.Read(buf); err != io.EOF {
		t.Fatalf("对端读到 %v，期望 EOF", err)
	}
}

// 关掉之后不能再写。重复 Close 也不该 panic 或把状态往回带。
func TestConnectionRejectsWriteAfterClose(t *testing.T) {
	a, _, _, _ := newPair(t, nil)
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	if err := a.Close(); err != ErrConnClosed {
		t.Errorf("重复关闭返回 %v，期望 ErrConnClosed", err)
	}
	if _, err := a.Write([]byte("x")); err == nil {
		t.Error("关闭后仍能写入")
	}
}

// 读超时要如实返回，而不是永久阻塞。
func TestConnectionReadDeadline(t *testing.T) {
	_, b, _, _ := newPair(t, nil)
	_ = b.SetReadDeadline(time.Now().Add(100 * time.Millisecond))

	start := time.Now()
	buf := make([]byte, 16)
	_, err := b.Read(buf)
	if err == nil {
		t.Fatal("没有数据却成功返回了")
	}
	if netErr, ok := err.(net.Error); !ok || !netErr.Timeout() {
		t.Fatalf("返回 %v，期望一个超时错误", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("超时用了 %v，远超设定的 100ms", elapsed)
	}
}

// 会话号对不上的段要丢掉：多半是上一条连接的残留包打到了新会话，
// 处理它会污染窗口状态。
func TestConnectionIgnoresForeignConversation(t *testing.T) {
	_, b, _, _ := newPair(t, nil)

	b.Input([]Segment{&DataSegment{Conv: 999, Number: 0, Data: []byte("intruder")}})

	_ = b.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	buf := make([]byte, 16)
	if n, err := b.Read(buf); err == nil {
		t.Fatalf("异会话的数据被交付了：%q", buf[:n])
	}
}

// 空闲太久要自己收场，否则 UDP 上对端消失后会话表一直泄漏。
func TestConnectionIdleTimeout(t *testing.T) {
	if testing.Short() {
		t.Skip("空闲超时测试需要真实等待")
	}
	a, _, aOut, _ := newPair(t, nil)
	// 掐断出向：对端收不到任何东西，也就不会回任何东西
	aOut.setPeer(nil)

	// 把 lastIncoming 推到很久以前，模拟长时间没有对端消息
	a.mu.Lock()
	a.lastIncoming = 0
	a.since = time.Now().Add(-time.Duration(idleTimeout+1000) * time.Millisecond)
	a.mu.Unlock()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if a.State().Is(StateTerminating, StateTerminated) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("空闲超时后状态仍是 %v", a.State())
}

// 连接建立后应当有心跳在跑——它同时承担保活和交换接收进度。
func TestConnectionSendsPeriodicPing(t *testing.T) {
	if testing.Short() {
		t.Skip("心跳测试需要真实等待")
	}
	a, _, aOut, _ := newPair(t, nil)
	before := aOut.count()

	// 心跳间隔 3 秒，等够一轮
	time.Sleep(3500 * time.Millisecond)
	if aOut.count() <= before {
		t.Error("等了一个心跳周期，一个段都没发出去")
	}
	_ = a
}
