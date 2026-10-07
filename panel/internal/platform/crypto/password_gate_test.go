package crypto

import (
	"context"
	"errors"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// 测试会替换全局闸门，结束时恢复缺省，免得影响同包其它测试。
func withGate(t testing.TB, concurrency int, wait time.Duration) {
	t.Helper()
	ConfigurePasswordHashing(concurrency, wait)
	t.Cleanup(func() {
		ConfigurePasswordHashing(defaultPasswordHashConcurrency, defaultPasswordHashQueueTimeout)
	})
}

func TestPasswordGateCapsConcurrencyAndTimesOut(t *testing.T) {
	withGate(t, 2, 50*time.Millisecond)
	ctx := context.Background()
	a, err := AcquirePasswordSlot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	b, err := AcquirePasswordSlot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if _, err := AcquirePasswordSlot(ctx); !errors.Is(err, ErrPasswordHashBusy) {
		t.Fatalf("third slot err=%v, want busy", err)
	}
	if waited := time.Since(start); waited < 40*time.Millisecond {
		t.Fatalf("busy returned after %s, before the queue timeout", waited)
	}

	a.Release()
	a.Release() // 重复归还不能多还一个名额
	c, err := AcquirePasswordSlot(ctx)
	if err != nil {
		t.Fatalf("slot not returned on release: %v", err)
	}
	if _, err := AcquirePasswordSlot(ctx); !errors.Is(err, ErrPasswordHashBusy) {
		t.Fatalf("double release leaked an extra slot: %v", err)
	}
	b.Release()
	c.Release()
}

func TestPasswordGateHonoursContextCancellation(t *testing.T) {
	withGate(t, 1, time.Minute)
	held, err := AcquirePasswordSlot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer held.Release()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := AcquirePasswordSlot(ctx)
		done <- err
	}()
	time.Sleep(10 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, ErrPasswordHashBusy) || !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled wait err=%v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("queued acquire ignored ctx cancellation")
	}

	dead, cancelDead := context.WithCancel(context.Background())
	cancelDead()
	if _, err := AcquirePasswordSlot(dead); !errors.Is(err, context.Canceled) {
		t.Fatalf("already-cancelled ctx err=%v", err)
	}
}

func TestPasswordSlotComputesAndRefusesAfterRelease(t *testing.T) {
	withGate(t, 1, time.Second)
	slot, err := AcquirePasswordSlot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	phc, err := slot.Hash("correct horse 1", DefaultArgon2Params())
	if err != nil {
		t.Fatal(err)
	}
	if ok, rehash, err := slot.Verify("correct horse 1", phc); !ok || rehash || err != nil {
		t.Fatalf("verify own hash ok=%v rehash=%v err=%v", ok, rehash, err)
	}
	if ok, _, _ := slot.Verify("wrong horse 1", phc); ok {
		t.Fatal("wrong password verified")
	}
	slot.Release()
	if _, err := slot.Hash("x1", DefaultArgon2Params()); !errors.Is(err, errSlotReleased) {
		t.Fatalf("hash after release err=%v", err)
	}
	if _, _, err := slot.Verify("x1", phc); !errors.Is(err, errSlotReleased) {
		t.Fatalf("verify after release err=%v", err)
	}
}

// 包级入口（命令行、后台批量）也走同一道闸：名额被占满时等，不越过上限。
func TestPackageLevelHashingSharesTheGate(t *testing.T) {
	withGate(t, 1, time.Second)
	held, err := AcquirePasswordSlot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		_, _ = HashPassword("blocked 1", DefaultArgon2Params())
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("package-level HashPassword ran while the only slot was held")
	case <-time.After(100 * time.Millisecond):
	}
	held.Release()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("package-level HashPassword never got the released slot")
	}
}

// 32 个并发请求在名额为 2 的闸门下，同一时刻持名额（即在算 Argon2）的从不超过 2 个。
func TestPasswordGateBoundsInFlightHashes(t *testing.T) {
	withGate(t, 2, 30*time.Second)
	var inFlight, peak atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			slot, err := AcquirePasswordSlot(context.Background())
			if err != nil {
				t.Error(err)
				return
			}
			n := inFlight.Add(1)
			for {
				p := peak.Load()
				if n <= p || peak.CompareAndSwap(p, n) {
					break
				}
			}
			slot.DummyVerify("load test 1")
			inFlight.Add(-1)
			slot.Release()
		}()
	}
	wg.Wait()
	if got := peak.Load(); got > 2 {
		t.Fatalf("peak concurrent hashes = %d, want ≤ 2", got)
	}
}

// BenchmarkArgon2Burst 模拟一次登录潮：每轮 32 个并发登录，报告每轮的峰值堆。
// 名额 2（缺省）对比 32（相当于没有上限）：
//
//	go test -run '^$' -bench Argon2Burst -benchtime 3x ./internal/platform/crypto/
func BenchmarkArgon2Burst(b *testing.B) {
	for _, limit := range []int{2, 32} {
		b.Run("slots="+strconv.Itoa(limit), func(b *testing.B) {
			withGate(b, limit, time.Minute)
			var peak uint64
			for i := 0; i < b.N; i++ {
				runtime.GC()
				stop := make(chan struct{})
				sampled := make(chan uint64)
				go func() {
					var ms runtime.MemStats
					var max uint64
					for {
						runtime.ReadMemStats(&ms)
						if ms.HeapInuse > max {
							max = ms.HeapInuse
						}
						select {
						case <-stop:
							sampled <- max
							return
						case <-time.After(2 * time.Millisecond):
						}
					}
				}()
				var wg sync.WaitGroup
				for j := 0; j < 32; j++ {
					wg.Add(1)
					go func() {
						defer wg.Done()
						slot, err := AcquirePasswordSlot(context.Background())
						if err != nil {
							b.Error(err)
							return
						}
						slot.DummyVerify("burst 1")
						slot.Release()
					}()
				}
				wg.Wait()
				close(stop)
				if m := <-sampled; m > peak {
					peak = m
				}
			}
			b.ReportMetric(float64(peak)/(1<<20), "peak-heap-MiB")
		})
	}
}
