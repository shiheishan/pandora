package udpmask

import (
	"bytes"
	"net"
	"sync"
	"testing"
	"time"
)

func localPair(t *testing.T) (net.PacketConn, net.PacketConn) {
	t.Helper()
	a, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	b, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		a.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close(); b.Close() })
	return a, b
}

// 套上掩码之后，上层看到的仍然是原样的明文。
func TestAES128GCMRoundTrip(t *testing.T) {
	rawA, rawB := localPair(t)
	a, err := Wrap(rawA, "mkcp-aes128gcm", "hunter2")
	if err != nil {
		t.Fatal(err)
	}
	b, err := Wrap(rawB, "mkcp-aes128gcm", "hunter2")
	if err != nil {
		t.Fatal(err)
	}

	for _, payload := range [][]byte{
		[]byte("x"),
		[]byte("hello over a masked udp packet"),
		bytes.Repeat([]byte("A"), 1350),
	} {
		if _, err := a.WriteTo(payload, rawB.LocalAddr()); err != nil {
			t.Fatalf("发 %d 字节失败：%v", len(payload), err)
		}
		_ = b.SetReadDeadline(time.Now().Add(5 * time.Second))
		buf := make([]byte, MaxPacketSize)
		n, _, err := b.ReadFrom(buf)
		if err != nil {
			t.Fatalf("收 %d 字节失败：%v", len(payload), err)
		}
		if !bytes.Equal(buf[:n], payload) {
			t.Fatalf("%d 字节负载收发不一致", len(payload))
		}
	}
}

// 线上不能出现明文。这条是这一层存在的全部理由。
func TestAES128GCMCiphertextLeaksNoPlaintext(t *testing.T) {
	rawA, rawB := localPair(t)
	a, err := Wrap(rawA, "mkcp-aes128gcm", "hunter2")
	if err != nil {
		t.Fatal(err)
	}

	marker := []byte("PANDORA-PLAINTEXT-MARKER")
	if _, err := a.WriteTo(marker, rawB.LocalAddr()); err != nil {
		t.Fatal(err)
	}
	_ = rawB.SetReadDeadline(time.Now().Add(5 * time.Second))
	wire := make([]byte, MaxPacketSize)
	n, _, err := rawB.ReadFrom(wire) // 不解掩码，直接看线上字节
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(wire[:n], marker) {
		t.Fatal("明文出现在了线上字节里")
	}
	if want := len(marker) + 12 + 16; n != want {
		t.Errorf("线上长度 %d，期望 %d（12 nonce + 明文 + 16 tag）", n, want)
	}
}

// 每个包的 nonce 必须不同。GCM 在同一密钥下重用 nonce 不是「安全性下降」，
// 是彻底失效——明文异或值直接泄露，密文还能被伪造。
func TestAES128GCMUsesFreshNoncePerPacket(t *testing.T) {
	rawA, rawB := localPair(t)
	a, err := Wrap(rawA, "mkcp-aes128gcm", "hunter2")
	if err != nil {
		t.Fatal(err)
	}

	payload := []byte("same payload every time")
	seen := map[string]bool{}
	for i := 0; i < 64; i++ {
		if _, err := a.WriteTo(payload, rawB.LocalAddr()); err != nil {
			t.Fatal(err)
		}
		_ = rawB.SetReadDeadline(time.Now().Add(5 * time.Second))
		wire := make([]byte, MaxPacketSize)
		n, _, err := rawB.ReadFrom(wire)
		if err != nil {
			t.Fatal(err)
		}
		nonce := string(wire[:12])
		if seen[nonce] {
			t.Fatal("同一个 nonce 出现了两次")
		}
		seen[nonce] = true
		// 同样的明文，密文也必须每次不同
		if n < 12 {
			t.Fatal("包太短")
		}
	}
}

// 伪造的包要被静默丢弃，而不是让收包循环报错退出。
func TestAES128GCMDropsForgedPackets(t *testing.T) {
	rawA, rawB := localPair(t)
	b, err := Wrap(rawB, "mkcp-aes128gcm", "hunter2")
	if err != nil {
		t.Fatal(err)
	}

	// 先灌一批垃圾：太短的、随机的、全零的
	for _, junk := range [][]byte{
		{},
		{1, 2, 3},
		bytes.Repeat([]byte{0}, 40),
		bytes.Repeat([]byte{0xFF}, 100),
	} {
		_, _ = rawA.WriteTo(junk, rawB.LocalAddr())
	}
	// 再发一个真的
	a, err := Wrap(rawA, "mkcp-aes128gcm", "hunter2")
	if err != nil {
		t.Fatal(err)
	}
	real := []byte("the real one")
	if _, err := a.WriteTo(real, rawB.LocalAddr()); err != nil {
		t.Fatal(err)
	}

	// 垃圾应当被跳过，读到的是那个真包
	_ = b.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, MaxPacketSize)
	n, _, err := b.ReadFrom(buf)
	if err != nil {
		t.Fatalf("垃圾包让读取失败了：%v", err)
	}
	if !bytes.Equal(buf[:n], real) {
		t.Fatalf("读到 %q，期望 %q", buf[:n], real)
	}
}

// 密码不一致的对端解不开，同样是静默丢弃。
func TestAES128GCMRejectsWrongPassword(t *testing.T) {
	rawA, rawB := localPair(t)
	a, _ := Wrap(rawA, "mkcp-aes128gcm", "correct")
	b, _ := Wrap(rawB, "mkcp-aes128gcm", "wrong")

	if _, err := a.WriteTo([]byte("secret"), rawB.LocalAddr()); err != nil {
		t.Fatal(err)
	}
	_ = b.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	buf := make([]byte, MaxPacketSize)
	if _, _, err := b.ReadFrom(buf); err == nil {
		t.Fatal("密码不对却读出了数据")
	} else if netErr, ok := err.(net.Error); !ok || !netErr.Timeout() {
		t.Fatalf("期望读超时（包被丢弃），得到 %v", err)
	}
}

// 并发写不能互相踩到写缓冲。
func TestAES128GCMConcurrentWrites(t *testing.T) {
	rawA, rawB := localPair(t)
	a, _ := Wrap(rawA, "mkcp-aes128gcm", "hunter2")
	b, _ := Wrap(rawB, "mkcp-aes128gcm", "hunter2")

	const senders = 8
	const perSender = 16
	var wg sync.WaitGroup
	for i := 0; i < senders; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			payload := bytes.Repeat([]byte{byte('A' + i)}, 200)
			for j := 0; j < perSender; j++ {
				_, _ = a.WriteTo(payload, rawB.LocalAddr())
			}
		}(i)
	}
	wg.Wait()

	// 收到的每个包都必须是某个发送者的完整负载，不能是拼接出来的碎片
	got := 0
	for got < senders*perSender/2 { // 收一半就够——UDP 本来就允许丢
		_ = b.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
		buf := make([]byte, MaxPacketSize)
		n, _, err := b.ReadFrom(buf)
		if err != nil {
			break
		}
		if n != 200 {
			t.Fatalf("收到 %d 字节，期望 200——写缓冲被并发踩了", n)
		}
		for _, c := range buf[:n] {
			if c != buf[0] {
				t.Fatal("单个包里混进了别的发送者的数据")
			}
		}
		got++
	}
	if got == 0 {
		t.Fatal("一个包都没收到")
	}
}

func TestMaskFactory(t *testing.T) {
	// 不加掩码的几种写法
	for _, name := range []string{"", "none", "mkcp-original", "  NONE  "} {
		m, err := New(name, "")
		if err != nil {
			t.Errorf("%q 应当表示不加掩码，却报错 %v", name, err)
		}
		if m != nil {
			t.Errorf("%q 应当返回 nil 掩码", name)
		}
	}
	// 别名
	for _, name := range []string{"mkcp-aes128gcm", "aes128gcm", "AES-128-GCM"} {
		m, err := New(name, "pw")
		if err != nil || m == nil {
			t.Errorf("%q 没被认成 aes128gcm：%v", name, err)
		}
	}
	// 未知类型要报错，不能静默当成不加掩码——那会让配了掩码的节点
	// 裸奔，而管理员以为它加密着。
	if _, err := New("header-srtp", ""); err != ErrUnknownMask {
		t.Errorf("未知掩码返回 %v，期望 ErrUnknownMask", err)
	}
	// 空密码不许启用加密：密钥会变成 sha256("")，谁都算得出来
	if _, err := New("mkcp-aes128gcm", ""); err == nil {
		t.Error("空密码的 aes128gcm 应当被拒")
	}
}

func TestOverheadMatchesWireFormat(t *testing.T) {
	n, err := Overhead("mkcp-aes128gcm", "pw")
	if err != nil {
		t.Fatal(err)
	}
	if n != 28 { // 12 nonce + 16 tag
		t.Errorf("开销 = %d，期望 28", n)
	}
	if n, _ := Overhead("none", ""); n != 0 {
		t.Errorf("不加掩码的开销 = %d，期望 0", n)
	}
}
