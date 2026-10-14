package reality

// Pandora 改动的守卫（见 handoff.go 文件头）：合并写、机会式多读、握手缓冲释放。

import (
	"bytes"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

// captureConn 记下每一次 Write（只写不读）。
type captureConn struct {
	net.Conn
	mu     sync.Mutex
	writes [][]byte
}

func (c *captureConn) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.writes = append(c.writes, append([]byte(nil), p...))
	return len(p), nil
}

func (c *captureConn) SetWriteDeadline(time.Time) error { return nil }

func (c *captureConn) all() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return bytes.Join(c.writes, nil)
}

// tls13Pair 造一对已经「握手完」的记录层：w 写、r 读，用同一份应用流量密钥。
func tls13Pair(t *testing.T, wConn, rConn net.Conn) (w, r *Conn) {
	t.Helper()
	suite := cipherSuiteTLS13ByID(TLS_AES_128_GCM_SHA256)
	secret := bytes.Repeat([]byte{0x42}, suite.hash.Size())
	mk := func(conn net.Conn) *Conn {
		c := &Conn{conn: conn, config: &Config{}, vers: VersionTLS13, haveVers: true}
		c.in.version, c.out.version = VersionTLS13, VersionTLS13
		c.isHandshakeComplete.Store(true)
		return c
	}
	w, r = mk(wConn), mk(rConn)
	w.out.setTrafficSecret(suite, QUICEncryptionLevelApplication, secret)
	r.in.setTrafficSecret(suite, QUICEncryptionLevelApplication, secret)
	w.bytesSent = recordSizeBoostThreshold // 跳过动态记录大小的爬坡，记录按 16KB 切
	return w, r
}

// 一次 Write 切出的多条记录合成一次底层写，对端照样逐条解出原文。
func TestWriteCoalescesRecordsIntoOneSyscall(t *testing.T) {
	capture := &captureConn{}
	w, _ := tls13Pair(t, capture, nil)
	payload := make([]byte, 40<<10) // 3 条记录
	for i := range payload {
		payload[i] = byte(i * 7)
	}
	if n, err := w.Write(payload); err != nil || n != len(payload) {
		t.Fatalf("Write = %d, %v", n, err)
	}
	if len(capture.writes) != 1 {
		t.Fatalf("40KB 应合成 1 次底层写，实际 %d 次", len(capture.writes))
	}

	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()
	_, r := tls13Pair(t, nil, client)
	go func() { _, _ = server.Write(capture.all()) }()
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(r, got); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatal("合并写出的记录解出来与原文不同")
	}
}

// 合并写的上限：超过 writeCoalesceLimit 分批写，不无限攒。
func TestWriteCoalesceIsBounded(t *testing.T) {
	capture := &captureConn{}
	w, _ := tls13Pair(t, capture, nil)
	if _, err := w.Write(make([]byte, 200<<10)); err != nil {
		t.Fatal(err)
	}
	for i, b := range capture.writes {
		if len(b) > writeCoalesceLimit+maxCiphertextTLS13+recordHeaderLen {
			t.Fatalf("第 %d 次底层写 %d 字节，超过上限", i, len(b))
		}
	}
	if len(capture.writes) < 3 {
		t.Fatalf("200KB 应分几批写出，实际 %d 次", len(capture.writes))
	}
}

// sealedRecords 把每段明文各封成一条记录，拼成一段线上字节。
func sealedRecords(t *testing.T, parts ...[]byte) []byte {
	t.Helper()
	capture := &captureConn{}
	w, _ := tls13Pair(t, capture, nil)
	for _, p := range parts {
		if _, err := w.Write(p); err != nil {
			t.Fatal(err)
		}
	}
	return capture.all()
}

// 打开机会式多读后，一次 Read 把 rawInput 里已到齐的多条记录都交出来；默认关闭时
// 与上游一样一次只交一条。
func TestReadCoalescingDrainsBufferedRecords(t *testing.T) {
	a, b, c := bytes.Repeat([]byte("a"), 100), bytes.Repeat([]byte("b"), 100), bytes.Repeat([]byte("c"), 100)
	wire := sealedRecords(t, a, b, c)
	for _, on := range []bool{false, true} {
		server, client := net.Pipe()
		_, r := tls13Pair(t, nil, client)
		r.SetReadCoalescing(on)
		go func() { _, _ = server.Write(wire) }() // net.Pipe 一次写的内容一次读完
		buf := make([]byte, 4096)
		n, err := r.Read(buf)
		if err != nil {
			t.Fatal(err)
		}
		want := a
		if on {
			want = append(append(append([]byte(nil), a...), b...), c...)
		}
		if !bytes.Equal(buf[:n], want) {
			t.Fatalf("coalesce=%v：一次 Read 交出 %d 字节，期望 %d", on, n, len(want))
		}
		server.Close()
		client.Close()
	}
}

// 机会式多读绝不等网络：后一条记录只到了一半时，先把已有的交出去，剩下的下次再读。
func TestReadCoalescingNeverBlocksOnPartialRecord(t *testing.T) {
	a, b := bytes.Repeat([]byte("a"), 100), bytes.Repeat([]byte("b"), 100)
	wire := sealedRecords(t, a, b)
	cut := len(wire) - 10 // 第二条缺最后 10 字节

	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()
	_, r := tls13Pair(t, nil, client)
	r.SetReadCoalescing(true)
	go func() { _, _ = server.Write(wire[:cut]) }()
	buf := make([]byte, 4096)
	done := make(chan struct{})
	var n int
	var err error
	go func() {
		n, err = r.Read(buf)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("后一条记录不完整时 Read 卡住了")
	}
	if err != nil || !bytes.Equal(buf[:n], a) {
		t.Fatalf("Read = %q, %v；期望先交出第一条", buf[:n], err)
	}
	go func() { _, _ = server.Write(wire[cut:]) }()
	n, err = r.Read(buf)
	if err != nil || !bytes.Equal(buf[:n], b) {
		t.Fatalf("补齐之后 Read = %d 字节, %v；期望第二条", n, err)
	}
}

// 多读途中碰到 close_notify：已解出的数据与 io.EOF 一起交出，不丢字节。
func TestReadCoalescingReturnsDataBeforeCloseNotify(t *testing.T) {
	capture := &captureConn{}
	w, _ := tls13Pair(t, capture, nil)
	a, b := bytes.Repeat([]byte("a"), 100), bytes.Repeat([]byte("b"), 100)
	_, _ = w.Write(a)
	_, _ = w.Write(b)
	if err := w.closeNotify(); err != nil {
		t.Fatal(err)
	}
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()
	_, r := tls13Pair(t, nil, client)
	r.SetReadCoalescing(true)
	go func() { _, _ = server.Write(capture.all()) }()
	buf := make([]byte, 4096)
	n, err := r.Read(buf)
	if !bytes.Equal(buf[:n], append(append([]byte(nil), a...), b...)) {
		t.Fatalf("Read 交出 %d 字节，期望两条记录共 200 字节", n)
	}
	if err != io.EOF {
		t.Fatalf("close_notify 之后应返回 io.EOF，实际 %v", err)
	}
}

// 握手完放掉被 ClientHello / Finished 撑大的读缓冲；rawInput 里已有的后续字节保留。
func TestReleaseHandshakeBuffers(t *testing.T) {
	c := &Conn{}
	c.hand.Write(make([]byte, 2000))
	c.hand.Next(2000)
	c.rawInput.Write(make([]byte, 4000))
	c.rawInput.Next(3990)
	tail := append([]byte(nil), c.rawInput.Bytes()...)
	c.releaseHandshakeBuffers()
	if c.hand.Cap() != 0 {
		t.Fatalf("hand 仍占 %d 字节", c.hand.Cap())
	}
	if c.rawInput.Cap() > 64 || !bytes.Equal(c.rawInput.Bytes(), tail) {
		t.Fatalf("rawInput cap=%d 内容=%v，期望只留下未读的 %d 字节", c.rawInput.Cap(), c.rawInput.Bytes(), len(tail))
	}
	c.rawInput.Next(len(tail))
	c.releaseHandshakeBuffers()
	if c.rawInput.Cap() != 0 {
		t.Fatalf("rawInput 读空后仍占 %d 字节", c.rawInput.Cap())
	}
}
