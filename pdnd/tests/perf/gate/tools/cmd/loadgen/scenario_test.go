package main

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"
)

type countWriter struct {
	n int64
}

func (c *countWriter) Write(p []byte) (int, error) {
	c.n += int64(len(p))
	return len(p), nil
}

func TestPacedSendRate(t *testing.T) {
	cw := &countWriter{}
	ctx := context.Background()
	// 8 Mbps × 300ms
	n, err := pacedSend(ctx, cw, 8_000_000, 300*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if n != cw.n {
		t.Fatalf("返回值与写入不一致")
	}
	min := int64(0.2 * 1024 * 1024)
	max := int64(0.45 * 1024 * 1024)
	if n < min || n > max {
		t.Fatalf("8Mbps×300ms 字节数 %d 不在 [%d,%d]", n, min, max)
	}
}

func TestPacedSendUnlimited(t *testing.T) {
	cw := &countWriter{}
	n, err := pacedSend(context.Background(), cw, 0, 50*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if n <= 0 {
		t.Fatalf("bps=0 应写出 >0 字节，得到 %d", n)
	}
}

func TestPacedSendCancel(t *testing.T) {
	cw := &countWriter{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	n, err := pacedSend(ctx, cw, 8_000_000, 5*time.Second)
	if err == nil {
		t.Fatal("期望 ctx 取消返回错误")
	}
	if n < 0 {
		t.Fatalf("n=%d", n)
	}
}

func TestUDPHeaderRoundTrip(t *testing.T) {
	buf := make([]byte, 1200)
	encodeUDPHeader(buf, 42, 123456789)
	seq, sentNs, ok := decodeUDPHeader(buf)
	if !ok || seq != 42 || sentNs != 123456789 {
		t.Fatalf("seq=%d sentNs=%d ok=%v", seq, sentNs, ok)
	}
	_, _, ok = decodeUDPHeader(buf[:10])
	if ok {
		t.Fatal("短包应 ok=false")
	}
}

func TestTopErrors(t *testing.T) {
	long := strings.Repeat("a", 100)
	m := map[string]int{
		long:    10,
		"err-b": 20,
		"err-c": 20,
		"err-d": 5,
		"err-e": 5,
		"err-f": 5,
		"err-g": 5,
	}
	out := topErrors(m, 5)
	if len(out) > 5 {
		t.Fatalf("len=%d", len(out))
	}
	for k := range out {
		if len(k) > 80 {
			t.Fatalf("键过长: len=%d", len(k))
		}
	}
	if out["err-b"] != 20 {
		t.Fatalf("out=%v", out)
	}
	keyLong := strings.Repeat("z", 100)
	out2 := topErrors(map[string]int{keyLong: 1}, 5)
	for k := range out2 {
		if len(k) != 80 {
			t.Fatalf("截断后 len=%d", len(k))
		}
	}
}

func TestStaggerDelay(t *testing.T) {
	hb := 30 * time.Second
	if staggerDelay(hb, 0) != 0 {
		t.Fatal("i=0")
	}
	if staggerDelay(hb, 500) != hb/2 {
		t.Fatalf("i=500: %v", staggerDelay(hb, 500))
	}
	if staggerDelay(hb, 1500) != hb/2 {
		t.Fatalf("i=1500: %v", staggerDelay(hb, 1500))
	}
}

func TestPacedSendDiscard(t *testing.T) {
	n, err := pacedSend(context.Background(), io.Discard, 1_000_000, 100*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if n <= 0 {
		t.Fatal("应写出数据")
	}
}
