package panel

import (
	"testing"
	"time"
)

func TestBackoffJitterUpperBound(t *testing.T) {
	b := Backoff{
		Base: time.Second,
		Max:  30 * time.Second,
		Rand: func(n int64) int64 { return n - 1 },
	}
	wantCeil := []time.Duration{
		time.Second,
		2 * time.Second,
		4 * time.Second,
		8 * time.Second,
		16 * time.Second,
		30 * time.Second,
		30 * time.Second,
	}
	for i, want := range wantCeil {
		if c := b.Ceiling(); c != want {
			t.Fatalf("第 %d 次 Ceiling: 要 %v 得 %v", i+1, want, c)
		}
		d := b.Next()
		if d != want-time.Nanosecond {
			t.Fatalf("第 %d 次 Next: 要 %v 得 %v", i+1, want-time.Nanosecond, d)
		}
	}
	for i := 0; i < 3; i++ {
		if b.Ceiling() != 30*time.Second {
			t.Fatal("封顶后 Ceiling 应一直是 30s")
		}
		if b.Next() != 30*time.Second-time.Nanosecond {
			t.Fatal("封顶后 Next 应一直是 30s-1ns")
		}
	}
}

func TestBackoffRandZero(t *testing.T) {
	b := Backoff{Base: time.Second, Max: 30 * time.Second, Rand: func(int64) int64 { return 0 }}
	d := b.Next()
	if d != 0 {
		t.Fatalf("Rand 返回 0 时 Next 应为 0，得 %v", d)
	}
}

func TestBackoffReset(t *testing.T) {
	b := Backoff{
		Base: time.Second,
		Max:  30 * time.Second,
		Rand: func(n int64) int64 { return n - 1 },
	}
	_ = b.Next()
	_ = b.Next()
	if b.Ceiling() != 4*time.Second {
		t.Fatalf("Reset 前 Ceiling 应为 4s，得 %v", b.Ceiling())
	}
	b.Reset()
	if b.Ceiling() != time.Second {
		t.Fatalf("Reset 后 Ceiling 应为 1s，得 %v", b.Ceiling())
	}
}
