//go:build linux

package hysteria2

import (
	"bytes"
	"encoding/binary"
	"net"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/net/ipv4"
)

func udpPair(t *testing.T) (*net.UDPConn, *net.UDPConn) {
	t.Helper()
	a, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	b, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close(); _ = b.Close() })
	return a, b
}

func segmentOOB(size uint16) []byte {
	b := make([]byte, syscall.CmsgSpace(2))
	h := (*syscall.Cmsghdr)(unsafe.Pointer(&b[0]))
	h.Level, h.Type = syscall.IPPROTO_UDP, udpSegmentOption
	h.SetLen(syscall.CmsgLen(2))
	binary.NativeEndian.PutUint16(b[syscall.CmsgLen(0):], size)
	return b
}

// 批量混淆层按 GSO 发出的一串段，对端用上游逐包实现逐个解出，内容与段边界不变。
func TestBatchSalamanderGSOWriteDecodesPerPacket(t *testing.T) {
	a, b := udpPair(t)
	password := []byte("obfs-secret")
	sender := newServerSalamanderConn(a, password).(*batchSalamanderConn)
	receiver := NewSalamanderConn(b, password)
	segments := [][]byte{bytes.Repeat([]byte{1}, 1200), bytes.Repeat([]byte{2}, 1200), bytes.Repeat([]byte{3}, 1200), bytes.Repeat([]byte{4}, 333)}
	joined := bytes.Join(segments, nil)
	n, _, err := sender.WriteMsgUDP(joined, segmentOOB(1200), b.LocalAddr().(*net.UDPAddr))
	if err != nil || n != len(joined) {
		t.Fatalf("WriteMsgUDP n=%d err=%v", n, err)
	}
	buffer := make([]byte, 2048)
	for i, want := range segments {
		_ = b.SetReadDeadline(time.Now().Add(2 * time.Second))
		k, _, err := receiver.ReadFrom(buffer)
		if err != nil || !bytes.Equal(buffer[:k], want) {
			t.Fatalf("第 %d 段 k=%d err=%v", i, k, err)
		}
	}
	// 不带 GSO 的单包同样可解。
	if _, err := sender.WriteTo([]byte("single"), b.LocalAddr()); err != nil {
		t.Fatal(err)
	}
	_ = b.SetReadDeadline(time.Now().Add(2 * time.Second))
	if k, _, err := receiver.ReadFrom(buffer); err != nil || string(buffer[:k]) != "single" {
		t.Fatalf("单包 %q err=%v", buffer[:k], err)
	}
}

// 对端逐包混淆发来的包，批量混淆层用 ReadBatch 一次收多包、逐个原地解出。
func TestBatchSalamanderReadBatchDecodes(t *testing.T) {
	a, b := udpPair(t)
	password := []byte("obfs-secret")
	receiver := newServerSalamanderConn(a, password).(*batchSalamanderConn)
	sender := NewSalamanderConn(b, password)
	var want [][]byte
	for i := 0; i < 5; i++ {
		p := bytes.Repeat([]byte{byte(10 + i)}, 100+i*300)
		want = append(want, p)
		if _, err := sender.WriteTo(append([]byte(nil), p...), a.LocalAddr()); err != nil {
			t.Fatal(err)
		}
	}
	ms := make([]ipv4.Message, 8)
	for i := range ms {
		ms[i].Buffers = [][]byte{make([]byte, 1452)}
	}
	var got [][]byte
	_ = a.SetReadDeadline(time.Now().Add(2 * time.Second))
	for len(got) < len(want) {
		n, err := receiver.ReadBatch(ms, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range ms[:n] {
			got = append(got, append([]byte(nil), m.Buffers[0][:m.N]...))
		}
	}
	for i := range want {
		if !bytes.Equal(got[i], want[i]) {
			t.Fatalf("第 %d 包解混淆不符", i)
		}
	}
}
