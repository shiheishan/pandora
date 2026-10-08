package kernel

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"io"
	"testing"

	"golang.org/x/crypto/sha3"
)

// TestVMessChunkReaderEndChunk（审查 VMess 1）：终止空块是「长度 = 标签 + 填充」，
// 读到它要把标签与填充一起读掉，并记住已结束：原先只丢了填充，16 字节标签留在
// 流里，再 Read 一次就把它当成下一块的长度。
func TestVMessChunkReaderEndChunk(t *testing.T) {
	key := make([]byte, 16)
	nonce := make([]byte, 16)
	_, _ = rand.Read(key)
	_, _ = rand.Read(nonce)
	option := vmessOptChunk | vmessOptMask | vmessOptPadding
	var wire bytes.Buffer
	w := newVMessChunkWriter(&wire, vmessBodyAEAD(vmessSecAES128, key), nonce, option, false)
	if _, err := w.Write([]byte("data")); err != nil {
		t.Fatal(err)
	}
	// 照 Xray seal([]byte{}) 手写终止空块：同一条 SHAKE 流上第一块已用掉两个数。
	mask := sha3.NewShake128()
	_, _ = mask.Write(nonce)
	next := func() uint16 {
		var b [2]byte
		_, _ = io.ReadFull(mask, b[:])
		return binary.BigEndian.Uint16(b[:])
	}
	next()
	next()
	pad := int(next() % vmessMaxPadding)
	var head [2]byte
	binary.BigEndian.PutUint16(head[:], uint16(16+pad)^next())
	wire.Write(head[:])
	wire.Write(make([]byte, 16+pad))
	wire.WriteString("NEXT")
	br := bufio.NewReaderSize(&wire, 4096)
	r := newVMessChunkReader(br, vmessBodyAEAD(vmessSecAES128, key), nonce, option, false)
	p := make([]byte, 64)
	if n, err := r.Read(p); err != nil || string(p[:n]) != "data" {
		t.Fatalf("第一块 = %q, %v", p[:n], err)
	}
	for i := 0; i < 2; i++ {
		if _, err := r.Read(p); err != io.EOF {
			t.Fatalf("终止块后第 %d 次 Read = %v，应为 io.EOF", i+1, err)
		}
	}
	if rest, _ := io.ReadAll(br); string(rest) != "NEXT" {
		t.Fatalf("终止块没读干净，剩下 %d 字节", len(rest))
	}
}
