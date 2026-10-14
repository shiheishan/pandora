package main

import (
	"bytes"
	"net"
	"testing"
	"time"
)

func TestParseSinkHeader(t *testing.T) {
	mode, bps, secs, err := parseSinkHeader("UP")
	if err != nil || mode != "UP" || bps != 0 || secs != 0 {
		t.Fatalf("UP: mode=%s bps=%d secs=%d err=%v", mode, bps, secs, err)
	}

	mode, bps, secs, err = parseSinkHeader("DOWN 300000000 30")
	if err != nil || mode != "DOWN" || bps != 300000000 || secs != 30 {
		t.Fatalf("DOWN 300000000 30: mode=%s bps=%d secs=%d err=%v", mode, bps, secs, err)
	}

	mode, bps, secs, err = parseSinkHeader("DOWN 0 5")
	if err != nil || mode != "DOWN" || bps != 0 || secs != 5 {
		t.Fatalf("DOWN 0 5: mode=%s bps=%d secs=%d err=%v", mode, bps, secs, err)
	}

	bad := []string{"", "DOWN x 1", "DOWN 1 0", "DOWN 1 99999", "FOO"}
	for _, line := range bad {
		_, _, _, err := parseSinkHeader(line)
		if err == nil {
			t.Fatalf("期望错误: %q", line)
		}
	}
}

func TestPacedWriteRateLimited(t *testing.T) {
	var buf bytes.Buffer
	const bps = 8_000_000
	n, err := pacedWrite(&buf, bps, 300*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	const minBytes = int64(1024 * 1024 / 5)
	const maxBytes = int64(1024 * 1024 * 45 / 100)
	if n < minBytes || n > maxBytes {
		t.Fatalf("写出 %d 字节，期望 [%d, %d]", n, minBytes, maxBytes)
	}
}

func TestPacedWriteUnlimited(t *testing.T) {
	var buf bytes.Buffer
	n, err := pacedWrite(&buf, 0, 50*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if n <= 0 {
		t.Fatalf("bps=0 应写出 >0 字节，实际 %d", n)
	}
}

func TestDrainCount(t *testing.T) {
	data := bytes.Repeat([]byte{'x'}, 1000)
	n := drainCount(bytes.NewReader(data))
	if n != 1000 {
		t.Fatalf("drainCount=%d", n)
	}
}

func TestServeSinkUPNoPanic(t *testing.T) {
	client, server := net.Pipe()
	done := make(chan struct{})
	go func() {
		serveSink(server)
		close(done)
	}()

	payload := bytes.Repeat([]byte{'a'}, 1000)
	_, err := client.Write(append([]byte("UP\n"), payload...))
	if err != nil {
		t.Fatal(err)
	}
	client.Close()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("serveSink 阻塞")
	}
}
