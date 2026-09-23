//go:build interop

package udpmask

import (
	"bytes"
	"net"
	"testing"
	"time"

	xaes "github.com/xtls/xray-core/transport/internet/finalmask/mkcp/aes128gcm"
)

// 与 xray 的 finalmask 互通。
//
// 掩码层的线格式必须逐字节一致，否则两边谁也解不开对方的包——而失败的
// 样子是「连上了但什么都传不了」，不会有任何报错指向掩码。所以这条测试
// 是这一层能不能上线的唯一判据。
//
// xray 那边的 conn 是原地变换语义（WriteTo 假定 p 的前 12 字节是预留的
// nonce 空间，加密后返回新长度，并不真的发出去），和我们这边正常的
// PacketConn 语义不同。这里直接按它的约定调用，绕开 PacketConn 那一层。

// xrayMaskConn 按 xray 的调用约定包一层，方便对拍。
func xrayMaskConn(t *testing.T, password string) net.PacketConn {
	t.Helper()
	conn, err := xaes.NewConnClient(&xaes.Config{Password: password}, nil)
	if err != nil {
		t.Fatalf("构造 xray 掩码失败：%v", err)
	}
	return conn
}

// 我们加密 → xray 解密。
func TestInteropXrayOpensOurCiphertext(t *testing.T) {
	const password = "pandora-mask-interop"
	rawA, rawB := localPair(t)
	ours, err := Wrap(rawA, "mkcp-aes128gcm", password)
	if err != nil {
		t.Fatal(err)
	}

	payload := []byte("sealed by pandora, opened by xray")
	if _, err := ours.WriteTo(payload, rawB.LocalAddr()); err != nil {
		t.Fatal(err)
	}

	// 收线上的原始字节，交给 xray 的解密逻辑
	_ = rawB.SetReadDeadline(time.Now().Add(5 * time.Second))
	wire := make([]byte, MaxPacketSize)
	n, _, err := rawB.ReadFrom(wire)
	if err != nil {
		t.Fatal(err)
	}

	xc := xrayMaskConn(t, password)
	got, _, err := xc.ReadFrom(wire[:n])
	if err != nil {
		t.Fatalf("xray 解不开我们的密文：%v", err)
	}
	// xray 的 ReadFrom 把明文留在 nonce 之后，返回明文长度
	plain := wire[12 : 12+got]
	if !bytes.Equal(plain, payload) {
		t.Fatalf("xray 解出 %q，期望 %q", plain, payload)
	}
}

// xray 加密 → 我们解密。
//
// 这个方向才是真实场景：客户端是 xray，服务端是我们。
func TestInteropWeOpenXrayCiphertext(t *testing.T) {
	const password = "pandora-mask-interop"
	rawA, rawB := localPair(t)
	ours, err := Wrap(rawB, "mkcp-aes128gcm", password)
	if err != nil {
		t.Fatal(err)
	}

	payload := []byte("sealed by xray, opened by pandora")

	// 按 xray 的约定组包：前 12 字节留给 nonce，明文跟在后面
	buf := make([]byte, MaxPacketSize)
	copy(buf[12:], payload)
	xc := xrayMaskConn(t, password)
	n, err := xc.WriteTo(buf[:12+len(payload)], nil)
	if err != nil {
		t.Fatalf("xray 加密失败：%v", err)
	}

	if _, err := rawA.WriteTo(buf[:n], rawB.LocalAddr()); err != nil {
		t.Fatal(err)
	}
	_ = ours.SetReadDeadline(time.Now().Add(5 * time.Second))
	got := make([]byte, MaxPacketSize)
	gotN, _, err := ours.ReadFrom(got)
	if err != nil {
		t.Fatalf("我们解不开 xray 的密文：%v", err)
	}
	if !bytes.Equal(got[:gotN], payload) {
		t.Fatalf("解出 %q，期望 %q", got[:gotN], payload)
	}
}

// 各种长度都要对得上，尤其是空载荷和接近 MTU 的。
func TestInteropAcrossPayloadSizes(t *testing.T) {
	const password = "size-matters"
	for _, size := range []int{0, 1, 12, 13, 28, 29, 512, 1350} {
		payload := bytes.Repeat([]byte{byte(size % 251)}, size)

		rawA, rawB := localPair(t)
		ours, err := Wrap(rawA, "mkcp-aes128gcm", password)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ours.WriteTo(payload, rawB.LocalAddr()); err != nil {
			t.Fatalf("%d 字节发送失败：%v", size, err)
		}
		_ = rawB.SetReadDeadline(time.Now().Add(5 * time.Second))
		wire := make([]byte, MaxPacketSize)
		n, _, err := rawB.ReadFrom(wire)
		if err != nil {
			t.Fatalf("%d 字节接收失败：%v", size, err)
		}

		xc := xrayMaskConn(t, password)
		got, _, err := xc.ReadFrom(wire[:n])
		if err != nil {
			t.Fatalf("%d 字节：xray 解不开：%v", size, err)
		}
		if got != size {
			t.Errorf("%d 字节：xray 解出 %d 字节", size, got)
		}
		if size > 0 && !bytes.Equal(wire[12:12+got], payload) {
			t.Errorf("%d 字节：内容不一致", size)
		}
	}
}

// 密钥派生必须一致：都是 sha256(password) 的前 16 字节。
// 派生方式不同的话，两边各自都能自洽，只有互通时才发现——
// 而那时症状是「密码明明填对了却连不上」。
func TestInteropKeyDerivationMatches(t *testing.T) {
	for _, password := range []string{"a", "hunter2", "带中文的密码", "very-long-" + string(bytes.Repeat([]byte("x"), 200))} {
		rawA, rawB := localPair(t)
		ours, err := Wrap(rawA, "mkcp-aes128gcm", password)
		if err != nil {
			t.Fatal(err)
		}
		payload := []byte("key derivation check")
		if _, err := ours.WriteTo(payload, rawB.LocalAddr()); err != nil {
			t.Fatal(err)
		}
		_ = rawB.SetReadDeadline(time.Now().Add(5 * time.Second))
		wire := make([]byte, MaxPacketSize)
		n, _, err := rawB.ReadFrom(wire)
		if err != nil {
			t.Fatal(err)
		}
		xc := xrayMaskConn(t, password)
		if _, _, err := xc.ReadFrom(wire[:n]); err != nil {
			t.Errorf("密码 %q：xray 解不开，密钥派生对不上：%v", password, err)
		}
	}
}
