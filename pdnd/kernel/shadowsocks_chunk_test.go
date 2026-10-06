package kernel

import (
	"bytes"
	"crypto/cipher"
	"encoding/binary"
	"io"
	"net"
	"testing"
)

// SIP004：AEAD 分块负载最长 0x3FFF。超出的块规范客户端会判非法并断开。
const sip004MaxChunk = 0x3FFF

func testSSAEAD(t *testing.T) cipher.AEAD {
	t.Helper()
	aead, err := newAESGCM(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	return aead
}

// 一次写入远大于 16384 字节：每个分块都不得超过 0x3FFF，拼回来与原文一致。
func TestShadowsocksStreamWriteSplitsAtSIP004Limit(t *testing.T) {
	aead := testSSAEAD(t)
	client, server := net.Pipe()
	defer client.Close()
	stream := &ssStream{conn: server, aead: aead}

	payload := make([]byte, 3*16384+123)
	for i := range payload {
		payload[i] = byte(i * 31)
	}
	writeErr := make(chan error, 1)
	go func() {
		_, err := stream.Write(payload)
		_ = server.Close()
		writeErr <- err
	}()

	var got []byte
	var nonce uint64
	chunks := 0
	for {
		var encLen [2 + 16]byte
		if _, err := io.ReadFull(client, encLen[:]); err == io.EOF {
			break
		} else if err != nil {
			t.Fatalf("read length: %v", err)
		}
		plainLen, err := aead.Open(nil, makeSSNonce(nonce), encLen[:], nil)
		nonce++
		if err != nil {
			t.Fatalf("open length: %v", err)
		}
		n := int(binary.BigEndian.Uint16(plainLen))
		if n == 0 || n > sip004MaxChunk {
			t.Fatalf("chunk %d length %d exceeds 0x3FFF", chunks, n)
		}
		frame := make([]byte, n+aead.Overhead())
		if _, err := io.ReadFull(client, frame); err != nil {
			t.Fatalf("read frame: %v", err)
		}
		plain, err := aead.Open(nil, makeSSNonce(nonce), frame, nil)
		nonce++
		if err != nil {
			t.Fatalf("open frame: %v", err)
		}
		got = append(got, plain...)
		chunks++
	}
	if err := <-writeErr; err != nil {
		t.Fatalf("write: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("reassembled %d bytes, want %d identical bytes", len(got), len(payload))
	}
	if chunks < 4 {
		t.Fatalf("got %d chunks, want payload split into at least 4", chunks)
	}
}

// sealSSFrame 按 AEAD 分块格式封一个长度为 n 的块（长度不经 ssChunkLimit 截断，用来造超长块）。
func sealSSFrame(aead cipher.AEAD, nonce *uint64, n int) []byte {
	length := []byte{byte(n >> 8), byte(n)}
	out := aead.Seal(nil, makeSSNonce(*nonce), length, nil)
	*nonce++
	out = append(out, aead.Seal(nil, makeSSNonce(*nonce), make([]byte, n), nil)...)
	*nonce++
	return out
}

// 读取一侧：0x3FFF 的块照收，16384 的块必须报错。
func TestShadowsocksStreamReadRejectsOversizedChunk(t *testing.T) {
	for _, tc := range []struct {
		n      int
		wantOK bool
	}{{sip004MaxChunk, true}, {16384, false}} {
		aead := testSSAEAD(t)
		client, server := net.Pipe()
		var nonce uint64
		frame := sealSSFrame(aead, &nonce, tc.n)
		go func() { _, _ = client.Write(frame); _ = client.Close() }()
		stream := &ssStream{conn: server, aead: aead}
		_, err := io.ReadFull(stream, make([]byte, tc.n))
		_ = server.Close()
		if tc.wantOK && err != nil {
			t.Fatalf("chunk of %d bytes rejected: %v", tc.n, err)
		}
		if !tc.wantOK && err == nil {
			t.Fatalf("chunk of %d bytes accepted, want error", tc.n)
		}
	}
}
