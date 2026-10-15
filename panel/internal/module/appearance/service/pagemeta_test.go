package service

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestPageMetaCache_get_firstCallAndTTL(t *testing.T) {
	var loads atomic.Int32
	c := &pageMetaCache{}
	ctx := context.Background()
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	want := PageMeta{SiteName: "测试站", Tagline: "标语"}
	load := func(context.Context) (PageMeta, error) {
		loads.Add(1)
		return want, nil
	}

	got := c.get(ctx, 0, base, load, func(error) {})
	if got != want {
		t.Fatalf("首次 got=%+v want=%+v", got, want)
	}
	if loads.Load() != 1 {
		t.Fatalf("首次应 load 一次，got %d", loads.Load())
	}

	_ = c.get(ctx, 0, base.Add(10*time.Second), load, func(error) {})
	if loads.Load() != 1 {
		t.Fatalf("30 秒内不应再 load，got %d", loads.Load())
	}

	_ = c.get(ctx, 0, base.Add(pageMetaTTL+time.Second), load, func(error) {})
	if loads.Load() != 2 {
		t.Fatalf("过期后应再 load，got %d", loads.Load())
	}
}

func TestPageMetaCache_get_genBump(t *testing.T) {
	var loads atomic.Int32
	c := &pageMetaCache{}
	ctx := context.Background()
	now := time.Now()
	load := func(context.Context) (PageMeta, error) {
		loads.Add(1)
		return PageMeta{SiteName: "A"}, nil
	}

	_ = c.get(ctx, 1, now, load, func(error) {})
	_ = c.get(ctx, 2, now, load, func(error) {})
	if loads.Load() != 2 {
		t.Fatalf("gen 变了应立刻 load，got %d", loads.Load())
	}
	// 失效后新代号的结果照样缓存
	_ = c.get(ctx, 2, now.Add(time.Second), load, func(error) {})
	if loads.Load() != 2 {
		t.Fatalf("新代号 30 秒内不应再 load，got %d", loads.Load())
	}
	// 晚到的旧代号结果不覆盖新代号的快照
	_ = c.get(ctx, 1, now.Add(time.Second), func(context.Context) (PageMeta, error) { return PageMeta{SiteName: "旧"}, nil }, func(error) {})
	if got := c.get(ctx, 2, now.Add(2*time.Second), load, func(error) {}); got.SiteName != "A" || loads.Load() != 2 {
		t.Fatalf("旧代号不应覆盖新快照：got %+v loads %d", got, loads.Load())
	}
}

func TestPageMetaCache_get_loadFailure(t *testing.T) {
	c := &pageMetaCache{}
	ctx := context.Background()
	base := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	boom := errors.New("db down")
	var logCalls atomic.Int32
	logErr := func(error) { logCalls.Add(1) }

	// 从没成功过：返回缺省站点名
	got := c.get(ctx, 0, base, func(context.Context) (PageMeta, error) {
		return PageMeta{}, boom
	}, logErr)
	if got.SiteName != DefaultSiteName || got.Tagline != "" {
		t.Fatalf("从未成功 got=%+v", got)
	}
	if logCalls.Load() != 1 {
		t.Fatalf("logErr 应被调用")
	}

	// 5 秒内不再 load
	var loads atomic.Int32
	load := func(context.Context) (PageMeta, error) {
		loads.Add(1)
		return PageMeta{}, boom
	}
	_ = c.get(ctx, 0, base.Add(2*time.Second), load, logErr)
	if loads.Load() != 0 {
		t.Fatalf("错误 TTL 内不应 load，got %d", loads.Load())
	}

	// 5 秒后再 load
	_ = c.get(ctx, 0, base.Add(pageMetaErrorTTL+time.Second), load, logErr)
	if loads.Load() != 1 {
		t.Fatalf("错误 TTL 过后应再 load，got %d", loads.Load())
	}

	// 先成功再失败：沿用上次成功值
	c2 := &pageMetaCache{}
	okVal := PageMeta{SiteName: "上次", Tagline: "t", Logo: "data:image/png;base64,AA=="}
	_ = c2.get(ctx, 0, base, func(context.Context) (PageMeta, error) { return okVal, nil }, func(error) {})
	got2 := c2.get(ctx, 0, base.Add(pageMetaTTL+time.Second), func(context.Context) (PageMeta, error) {
		return PageMeta{}, boom
	}, logErr)
	if got2 != okVal {
		t.Fatalf("失败后应沿用上次成功 got=%+v want=%+v", got2, okVal)
	}
}

func TestPageMetaCache_get_singleflight(t *testing.T) {
	c := &pageMetaCache{}
	ctx := context.Background()
	now := time.Now()
	var loads atomic.Int32
	var mu sync.Mutex
	mu.Lock() // 让 load 卡住直到我们放行

	load := func(context.Context) (PageMeta, error) {
		loads.Add(1)
		mu.Lock()
		mu.Unlock()
		return PageMeta{SiteName: "并发"}, nil
	}

	const n = 20
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			_ = c.get(ctx, 0, now, load, func(error) {})
		}()
	}
	mu.Unlock()
	wg.Wait()
	if loads.Load() != 1 {
		t.Fatalf("并发应只 load 一次，got %d", loads.Load())
	}
}

func TestService_invalidatePageMeta(t *testing.T) {
	s := &Service{}
	s.invalidatePageMeta()
	if s.metaGen.Load() != 1 {
		t.Fatalf("metaGen 应为 1，got %d", s.metaGen.Load())
	}
}
