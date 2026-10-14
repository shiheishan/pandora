package ratelimit

import (
	"context"
	"testing"
	"time"
)

func TestCounterIncrThresholdReleaseReset(t *testing.T) {
	mr, rdb := testRedis(t)
	c := NewCounter(rdb)
	ctx := context.Background()
	key := "lf:testhash"
	window := 15 * time.Minute
	pause := 20 * time.Minute

	for i := 1; i <= 9; i++ {
		n, ttl, err := c.Incr(ctx, key, window, 10, pause)
		if err != nil || n != i || ttl <= 0 || ttl > window {
			t.Fatalf("第 %d 次：n=%d ttl=%v err=%v", i, n, ttl, err)
		}
	}
	// 到阈值那一次期限改成暂停时长
	n, ttl, err := c.Incr(ctx, key, window, 10, pause)
	if err != nil || n != 10 || ttl <= window {
		t.Fatalf("第 10 次：n=%d ttl=%v err=%v", n, ttl, err)
	}
	// 过了阈值不再改期限
	n, ttl, _ = c.Incr(ctx, key, window, 10, pause)
	if n != 11 || ttl <= window {
		t.Fatalf("第 11 次 n=%d ttl=%v", n, ttl)
	}

	// 退名额
	if err := c.Release(ctx, key); err != nil {
		t.Fatal(err)
	}
	if v, _ := rdb.Get(ctx, key).Int(); v != 10 {
		t.Fatalf("退名额后=%d want 10", v)
	}
	// 清零
	if err := c.Reset(ctx, key); err != nil {
		t.Fatal(err)
	}
	if mr.Exists(key) {
		t.Fatal("Reset 后键应不存在")
	}
	// 键不存在时退名额不留下负数的新键
	if err := c.Release(ctx, key); err != nil {
		t.Fatal(err)
	}
	if mr.Exists(key) {
		t.Fatal("键不存在时 Release 不应建键")
	}
}

func TestCounterNoThresholdKeepsWindow(t *testing.T) {
	mr, rdb := testRedis(t)
	c := NewCounter(rdb)
	ctx := context.Background()
	window := 15 * time.Minute

	n, ttl, err := c.Incr(ctx, "pwfail:x", window, 0, 0)
	if err != nil || n != 1 || ttl <= 0 || ttl > window {
		t.Fatalf("无阈值第 1 次：n=%d ttl=%v err=%v", n, ttl, err)
	}
	// 窗口内后续计数不续期
	mr.FastForward(5 * time.Minute)
	n, ttl, err = c.Incr(ctx, "pwfail:x", window, 0, 0)
	if err != nil || n != 2 || ttl > window-5*time.Minute {
		t.Fatalf("无阈值第 2 次：n=%d ttl=%v err=%v", n, ttl, err)
	}
	// 窗口过去键过期，重新从 1 计
	mr.FastForward(window)
	n, _, err = c.Incr(ctx, "pwfail:x", window, 0, 0)
	if err != nil || n != 1 {
		t.Fatalf("窗口过后：n=%d err=%v", n, err)
	}
}

func TestCounterRedisDown(t *testing.T) {
	mr, rdb := testRedis(t)
	c := NewCounter(rdb)
	mr.Close()
	if _, _, err := c.Incr(context.Background(), "lf:x", time.Minute, 0, 0); err == nil {
		t.Fatal("Redis 不可用时 Incr 应返回错误")
	}
}

// 键丢了期限（比如被人手工 SET）时，下一次计数补上窗口期，不会让人一直被拦。
func TestCounterRestoresMissingTTL(t *testing.T) {
	mr, rdb := testRedis(t)
	c := NewCounter(rdb)
	ctx := context.Background()
	window := 15 * time.Minute
	if err := mr.Set("pwfail:y", "3"); err != nil {
		t.Fatal(err)
	}
	n, ttl, err := c.Incr(ctx, "pwfail:y", window, 0, 0)
	if err != nil || n != 4 || ttl <= 0 || ttl > window {
		t.Fatalf("没期限的键：n=%d ttl=%v err=%v", n, ttl, err)
	}
	mr.FastForward(window)
	if mr.Exists("pwfail:y") {
		t.Fatal("补上的窗口期过后键应过期")
	}
}
