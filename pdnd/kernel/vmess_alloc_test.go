package kernel

import (
	"bufio"
	"bytes"
	"io"
	"testing"
)

func benchVMessAEAD(b *testing.B) (key, nonce []byte) {
	return make([]byte, 16), make([]byte, 16)
}

// 下行（加密写回客户端）：每 32KB 一次 Write。
func BenchmarkVMessAEADWrite32K(b *testing.B) {
	key, nonce := benchVMessAEAD(b)
	w := newVMessAEADWriter(io.Discard, vmessBodyAEAD(vmessSecAES128, key), nonce, vmessOptChunk|vmessOptMask)
	payload := make([]byte, 32<<10)
	b.SetBytes(int64(len(payload)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := w.Write(payload); err != nil {
			b.Fatal(err)
		}
	}
}

// 上行（解密客户端来的块）：转发用 32KB 缓冲读。
func BenchmarkVMessAEADRead32K(b *testing.B) {
	key, nonce := benchVMessAEAD(b)
	var wire bytes.Buffer
	w := newVMessAEADWriter(&wire, vmessBodyAEAD(vmessSecAES128, key), nonce, vmessOptChunk|vmessOptMask)
	payload := make([]byte, 32<<10)
	_, _ = w.Write(payload)
	encoded := wire.Bytes()
	buf := make([]byte, 32<<10)
	b.SetBytes(int64(len(payload)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r := newVMessAEADReader(bufio.NewReaderSize(bytes.NewReader(encoded), 4096), vmessBodyAEAD(vmessSecAES128, key), nonce, vmessOptChunk|vmessOptMask)
		for got := 0; got < len(payload); {
			n, err := r.Read(buf)
			if err != nil {
				b.Fatal(err)
			}
			got += n
		}
	}
}
