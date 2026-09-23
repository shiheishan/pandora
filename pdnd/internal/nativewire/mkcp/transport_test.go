package mkcp

import (
	"bytes"
	"crypto/rand"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

func testConfig() *Config {
	c := DefaultConfig()
	c.Tick = 10 * time.Millisecond
	return c
}

// listenLocal 起一个只监听回环的监听器。
func listenLocal(t *testing.T) *Listener {
	t.Helper()
	l, err := Listen("udp", "127.0.0.1:0", testConfig())
	if err != nil {
		t.Fatalf("监听失败：%v", err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return l
}

func dialTo(t *testing.T, l *Listener) *Connection {
	t.Helper()
	c, err := Dial("udp", l.Addr().String(), testConfig())
	if err != nil {
		t.Fatalf("拨号失败：%v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// 真 UDP 上跑一个来回。前面所有单元测试都在内存里，这条才证明它能上网络。
func TestTransportEchoOverUDP(t *testing.T) {
	l := listenLocal(t)

	go func() {
		conn, err := l.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = io.Copy(conn, conn) // 回声
	}()

	client := dialTo(t, l)
	payload := []byte("hello over real udp")
	if _, err := client.Write(payload); err != nil {
		t.Fatal(err)
	}

	_ = client.SetReadDeadline(time.Now().Add(10 * time.Second))
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(client, got); err != nil {
		t.Fatalf("读回声失败：%v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("回声是 %q，期望 %q", got, payload)
	}
}

// 传一兆随机数据，验证分段、重组、窗口流控在有量的时候不出错。
// 小载荷跑通不代表大载荷跑通——窗口满、序号推进这些路径只有量上来才走到。
func TestTransportLargeTransfer(t *testing.T) {
	if testing.Short() {
		t.Skip("大数据传输耗时")
	}
	l := listenLocal(t)

	payload := make([]byte, 1<<20) // 1 MiB
	if _, err := rand.Read(payload); err != nil {
		t.Fatal(err)
	}

	go func() {
		conn, err := l.Accept()
		if err != nil {
			return
		}
		// Write 返回不等于数据已送达——它只是进了发送窗口。紧接着 Close
		// 是故意的：这条路径要验证 Close 之后剩余数据仍会被送完。
		//
		// 之前这里 sleep 了 500ms 才关，正好把问题盖住了。真实的 bug 是
		// 收到对端 Close 就立刻返回 EOF，把还在重传的尾部数据截掉；有了
		// 这个 sleep，数据早就发完了，测试照样绿。
		_, _ = conn.Write(payload)
		_ = conn.Close()
	}()

	client := dialTo(t, l)
	// 客户端先说句话，服务端才会建会话——mKCP 是客户端先发起的
	if _, err := client.Write([]byte("go")); err != nil {
		t.Fatal(err)
	}

	_ = client.SetReadDeadline(time.Now().Add(60 * time.Second))
	got := make([]byte, len(payload))
	start := time.Now()
	if _, err := io.ReadFull(client, got); err != nil {
		t.Fatalf("只收到部分数据：%v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatal("1 MiB 数据内容不一致")
	}
	t.Logf("1 MiB 用时 %v（约 %.1f Mbps）", time.Since(start),
		float64(len(payload))*8/time.Since(start).Seconds()/1e6)
}

// 多个客户端同时连一个端口。mKCP 服务端只有一个 UDP socket，
// 所有会话共用它——分发错了会出现张冠李戴。
func TestTransportMultipleClients(t *testing.T) {
	l := listenLocal(t)

	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				_, _ = io.Copy(conn, conn)
			}()
		}
	}()

	const clients = 8
	var wg sync.WaitGroup
	errs := make(chan error, clients)
	for i := 0; i < clients; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			c, err := Dial("udp", l.Addr().String(), testConfig())
			if err != nil {
				errs <- err
				return
			}
			defer c.Close()

			// 每个客户端发不同的内容，验证没有串台
			payload := bytes.Repeat([]byte{byte('A' + i)}, 200)
			if _, err := c.Write(payload); err != nil {
				errs <- err
				return
			}
			_ = c.SetReadDeadline(time.Now().Add(20 * time.Second))
			got := make([]byte, len(payload))
			if _, err := io.ReadFull(c, got); err != nil {
				errs <- err
				return
			}
			if !bytes.Equal(got, payload) {
				t.Errorf("客户端 %d 收到了别人的数据：%q", i, got[:8])
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("客户端出错：%v", err)
	}
}

// 同一个来源地址开多条连接。NAT 后面这是常态，只靠地址区分会话会撞。
func TestTransportSameSourceMultipleSessions(t *testing.T) {
	l := listenLocal(t)
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			go func() { defer conn.Close(); _, _ = io.Copy(conn, conn) }()
		}
	}()

	// 两条连接从同一台机器出去，会话号不同
	a := dialTo(t, l)
	b := dialTo(t, l)
	if a.meta.Conversation == b.meta.Conversation {
		t.Fatal("两条连接拿到了同一个会话号")
	}

	for _, tc := range []struct {
		conn *Connection
		msg  []byte
	}{{a, []byte("first session")}, {b, []byte("second session!")}} {
		if _, err := tc.conn.Write(tc.msg); err != nil {
			t.Fatal(err)
		}
		_ = tc.conn.SetReadDeadline(time.Now().Add(10 * time.Second))
		got := make([]byte, len(tc.msg))
		if _, err := io.ReadFull(tc.conn, got); err != nil {
			t.Fatalf("读取失败：%v", err)
		}
		if !bytes.Equal(got, tc.msg) {
			t.Errorf("收到 %q，期望 %q", got, tc.msg)
		}
	}
	if n := l.SessionCount(); n != 2 {
		t.Errorf("会话数 = %d，期望 2", n)
	}
}

// 连接关闭后服务端要把会话表项删掉，否则长期运行会一直涨。
func TestTransportSessionIsReclaimed(t *testing.T) {
	l := listenLocal(t)
	go func() {
		conn, err := l.Accept()
		if err != nil {
			return
		}
		go func() { defer conn.Close(); _, _ = io.Copy(conn, conn) }()
	}()

	c := dialTo(t, l)
	if _, err := c.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, 4)
	_ = c.SetReadDeadline(time.Now().Add(10 * time.Second))
	if _, err := io.ReadFull(c, got); err != nil {
		t.Fatal(err)
	}
	if l.SessionCount() != 1 {
		t.Fatalf("握手后会话数 = %d，期望 1", l.SessionCount())
	}

	_ = c.Close()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if l.SessionCount() == 0 {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("关闭后会话仍未回收，当前 %d 条", l.SessionCount())
}

// 解析不出段的垃圾包不该建会话。端口扫描、打错端口的其他协议都会打过来，
// 每个都建一条会话等于给了对方一个廉价的内存耗尽手段。
func TestTransportIgnoresGarbage(t *testing.T) {
	l := listenLocal(t)

	raw, err := net.Dial("udp", l.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	for _, junk := range [][]byte{
		{},
		{0x00},
		[]byte("GET / HTTP/1.1\r\n\r\n"),
		bytes.Repeat([]byte{0xFF}, 64),
	} {
		_, _ = raw.Write(junk)
	}
	time.Sleep(300 * time.Millisecond)

	if n := l.SessionCount(); n != 0 {
		t.Errorf("垃圾包建出了 %d 条会话", n)
	}
}

// 关掉监听器之后 Accept 要返回错误，不能永久挂着。
func TestTransportListenerCloseUnblocksAccept(t *testing.T) {
	l, err := Listen("udp", "127.0.0.1:0", testConfig())
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := l.Accept()
		done <- err
	}()
	time.Sleep(100 * time.Millisecond)
	_ = l.Close()

	select {
	case err := <-done:
		if err != ErrListenerClosed {
			t.Errorf("Accept 返回 %v，期望 ErrListenerClosed", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("关闭监听器后 Accept 仍然阻塞")
	}
	// 重复关闭不该 panic
	_ = l.Close()
}

//------------------------------------------------------------------------------
// 打包写入
//------------------------------------------------------------------------------

// 多个小段要合并进一个 UDP 包。ACK 只有十几字节，一段一包的话
// 包头开销比载荷还大。
func TestPacketWriterBatchesSegments(t *testing.T) {
	var packets [][]byte
	w := newPacketWriter(1350, func(b []byte) error {
		packets = append(packets, append([]byte(nil), b...))
		return nil
	})

	for i := 0; i < 3; i++ {
		if err := w.Write(&DataSegment{Conv: 1, Number: uint32(i), Data: []byte("x")}); err != nil {
			t.Fatal(err)
		}
	}
	if len(packets) != 0 {
		t.Fatalf("Flush 之前就发了 %d 个包", len(packets))
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	if len(packets) != 1 {
		t.Fatalf("发了 %d 个包，三个小段应当合并成 1 个", len(packets))
	}
	// 收端要能把它们完整拆回来
	if segs := ReadSegments(packets[0]); len(segs) != 3 {
		t.Fatalf("合并后的包拆出 %d 个段，期望 3 个", len(segs))
	}
}

// 攒满一个 MTU 就得先发走，不能等 Flush——否则超出的段会被丢掉。
func TestPacketWriterFlushesWhenFull(t *testing.T) {
	var packets int
	w := newPacketWriter(64, func(b []byte) error { packets++; return nil })

	// 每段 18 字节头 + 10 字节数据 = 28 字节，64 字节里放得下 2 个
	for i := 0; i < 5; i++ {
		if err := w.Write(&DataSegment{Conv: 1, Number: uint32(i), Data: make([]byte, 10)}); err != nil {
			t.Fatal(err)
		}
	}
	if packets == 0 {
		t.Fatal("塞了 5 段进 64 字节缓冲，一个包都没自动发出去")
	}
	_ = w.Flush()
	if packets < 3 {
		t.Errorf("总共只发了 %d 个包，5 个段装不下", packets)
	}
}

// 超过 MTU 的单个段是内部不变量被破坏，要报错而不是悄悄截断。
func TestPacketWriterRejectsOversizedSegment(t *testing.T) {
	w := newPacketWriter(64, func(b []byte) error { return nil })
	err := w.Write(&DataSegment{Conv: 1, Data: make([]byte, 200)})
	if err != ErrSegmentTooLarge {
		t.Errorf("返回 %v，期望 ErrSegmentTooLarge", err)
	}
}

// 发送失败之后缓冲要清空。留着的话下一批会和这一批黏在一起，
// 收端拆出重复的段。
func TestPacketWriterClearsBufferOnSendError(t *testing.T) {
	failing := true
	var sizes []int
	w := newPacketWriter(1350, func(b []byte) error {
		sizes = append(sizes, len(b))
		if failing {
			return io.ErrClosedPipe
		}
		return nil
	})

	_ = w.Write(&DataSegment{Conv: 1, Number: 0, Data: []byte("first")})
	_ = w.Flush() // 失败
	failing = false
	_ = w.Write(&DataSegment{Conv: 1, Number: 1, Data: []byte("second")})
	_ = w.Flush()

	if len(sizes) != 2 {
		t.Fatalf("发了 %d 次，期望 2 次", len(sizes))
	}
	if sizes[1] != sizes[0]+1 { // "second" 比 "first" 多一个字节
		t.Errorf("第二个包 %d 字节，期望 %d——失败的那批没被清掉", sizes[1], sizes[0]+1)
	}
}
