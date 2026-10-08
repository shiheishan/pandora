package kernel

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"io"
	"math/big"
	"net"
	"testing"
)

// TestVisionTLSTapNeverCrossesRecordBoundary：tap 按记录交付时，任何一次 Read 都不能
// 越过当前 TLS 记录的末尾（否则 tls.Conn 会把对端切直通后的裸字节读进 rawInput）；
// 切到透传后原样交付，字节一个不丢。
func TestVisionTLSTapNeverCrossesRecordBoundary(t *testing.T) {
	var wire []byte
	var ends []int
	for i := 0; i < 40; i++ {
		n, _ := rand.Int(rand.Reader, big.NewInt(3000))
		body := make([]byte, int(n.Int64()))
		_, _ = rand.Read(body)
		wire = append(wire, 0x17, 0x03, 0x03)
		wire = binary.BigEndian.AppendUint16(wire, uint16(len(body)))
		wire = append(wire, body...)
		ends = append(ends, len(wire))
	}
	tail := []byte("raw-bytes-after-switch")
	client, server := net.Pipe()
	go func() {
		// 故意按不对齐的小块写，记录头也会被拆开。
		all := append(append([]byte(nil), wire...), tail...)
		for len(all) > 0 {
			n := 7 + len(all)%13
			if n > len(all) {
				n = len(all)
			}
			_, _ = client.Write(all[:n])
			all = all[n:]
		}
		_ = client.Close()
	}()
	tap := newVisionTLSTap(server)
	var got []byte
	buf := make([]byte, 64<<10)
	next := 0
	for len(got) < len(wire) {
		n, err := tap.Read(buf)
		if err != nil {
			t.Fatal(err)
		}
		for next < len(ends) && ends[next] <= len(got) {
			next++
		}
		if len(got)+n > ends[next] {
			t.Fatalf("一次 Read 越过了记录末尾：起点 %d 长度 %d 记录末尾 %d", len(got), n, ends[next])
		}
		got = append(got, buf[:n]...)
	}
	if !bytes.Equal(got, wire) {
		t.Fatal("按记录交付的字节不符")
	}
	tap.passthrough()
	rest, err := io.ReadAll(tap)
	if err != nil || !bytes.Equal(rest, tail) {
		t.Fatalf("透传后的字节 = %q, %v", rest, err)
	}
}
