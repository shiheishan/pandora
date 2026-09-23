package core

import (
	"bytes"
	"io"
	"testing"
	"time"
)

func TestSpeedLimitersPerUserAndRefresh(t *testing.T) {
	var limiters SpeedLimiters

	if limiters.For(User{ID: 1}) != nil {
		t.Fatal("SpeedLimit 为 0 应当不限速")
	}

	user := User{ID: 1, SpeedLimit: 1000}
	first := limiters.For(user)
	if first == nil {
		t.Fatal("限速用户应当拿到令牌桶")
	}
	// 同一用户的多条连接必须共用一个桶，否则开 N 条连接就能跑 N 倍速率。
	if second := limiters.For(user); second != first {
		t.Fatal("同一用户重复取应当是同一个桶")
	}
	// 另一个用户不能被牵连。
	if other := limiters.For(User{ID: 2, SpeedLimit: 1000}); other == first {
		t.Fatal("不同用户不能共用一个桶")
	}

	// 面板改了套餐限速，桶必须跟着换，否则一直按老速率跑。
	changed := limiters.For(User{ID: 1, SpeedLimit: 2000})
	if changed == first {
		t.Fatal("限速值变化后应当重建令牌桶")
	}
	if got, want := changed.Limit(), float64(SpeedLimitBytesPerSecond(2000)); float64(got) != want {
		t.Fatalf("速率 = %v，期望 %v", got, want)
	}

	// 从限速改成不限速，桶要丢掉。
	if limiters.For(User{ID: 1}) != nil {
		t.Fatal("限速取消后应当不再限速")
	}
	limiters.mu.Lock()
	_, stillThere := limiters.m[1]
	limiters.mu.Unlock()
	if stillThere {
		t.Fatal("取消限速后应当清掉这个用户的桶")
	}

	limiters.Remove(2)
	limiters.mu.Lock()
	remaining := len(limiters.m)
	limiters.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("清理后还剩 %d 个桶", remaining)
	}
}

// 桶容量必须不小于单次读写量，否则 WaitN 会直接报错而不是等待，
// 表现为连接毫无征兆地断掉——这比限速不准严重得多。
func TestSpeedLimitBurstCoversLargeWrites(t *testing.T) {
	for _, kbps := range []int{1, 64, 1000, 100000} {
		if got := SpeedLimitBurst(kbps); got < 64*1024 {
			t.Fatalf("kbps=%d 的桶容量 %d 小于 64 KiB", kbps, got)
		}
	}
}

func TestSpeedLimitedCopyThrottles(t *testing.T) {
	if testing.Short() {
		t.Skip("限速测试需要真实等待")
	}
	// 32 kbps = 4000 字节/秒。先用一个桶那么大的搬运把初始令牌耗光，
	// 再计时量稳态速率——否则量到的是 burst，看不出限速有没有生效。
	var limiters SpeedLimiters
	limiter := limiters.For(User{ID: 9, SpeedLimit: 32})

	drain := bytes.NewReader(make([]byte, SpeedLimitBurst(32)))
	if _, err := SpeedLimitedCopy(io.Discard, drain, limiter); err != nil {
		t.Fatal(err)
	}

	const second = 4000 // 一秒的量
	start := time.Now()
	n, err := SpeedLimitedCopy(io.Discard, bytes.NewReader(make([]byte, second)), limiter)
	if err != nil {
		t.Fatal(err)
	}
	if n != second {
		t.Fatalf("搬运了 %d 字节，期望 %d", n, second)
	}
	elapsed := time.Since(start)
	// 桶已被上一次搬运抽干，这 4000 字节要按 4000 B/s 重新攒，约 1 秒。
	// 下界放宽到 700ms，避免调度抖动导致偶发失败。
	if elapsed < 700*time.Millisecond {
		t.Fatalf("限速没生效：搬 %d 字节只用了 %v", second, elapsed)
	}
}

func TestSpeedLimitedCopyUnlimitedIsPlainCopy(t *testing.T) {
	payload := make([]byte, 512*1024)
	for i := range payload {
		payload[i] = byte(i)
	}
	var sink bytes.Buffer
	// limiter 为 nil 时必须退化成 io.Copy：不限速的用户不该为此变慢，
	// 也不该有任何字节差异。
	n, err := SpeedLimitedCopy(&sink, bytes.NewReader(payload), nil)
	if err != nil {
		t.Fatal(err)
	}
	if n != int64(len(payload)) || !bytes.Equal(sink.Bytes(), payload) {
		t.Fatalf("不限速时搬运结果不一致：%d/%d 字节", n, len(payload))
	}
}
