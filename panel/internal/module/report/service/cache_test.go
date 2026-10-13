package service

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

type cacheTestPayload struct {
	Count int    `json:"count"`
	Name  string `json:"name"`
}

func TestCached(t *testing.T) {
	ctx := context.Background()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	defer mr.Close()

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	svc := &Service{}
	svc.UseCache(rdb)

	var loadCount int64

	// 1. 首次调用执行 load，第二次命中缓存不再调 load
	v1, err := cached(ctx, svc, "report:v1:test:a", func(ctx context.Context) (cacheTestPayload, error) {
		atomic.AddInt64(&loadCount, 1)
		return cacheTestPayload{Count: 1, Name: "first"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if v1.Count != 1 || v1.Name != "first" {
		t.Fatalf("v1: %+v", v1)
	}
	if atomic.LoadInt64(&loadCount) != 1 {
		t.Fatalf("loadCount: %d want 1", loadCount)
	}

	v2, err := cached(ctx, svc, "report:v1:test:a", func(ctx context.Context) (cacheTestPayload, error) {
		atomic.AddInt64(&loadCount, 1)
		return cacheTestPayload{Count: 2, Name: "second"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if v2.Count != 1 || v2.Name != "first" {
		t.Fatalf("v2 should come from cache: %+v", v2)
	}
	if atomic.LoadInt64(&loadCount) != 1 {
		t.Fatalf("loadCount should still be 1, got %d", loadCount)
	}

	// 2. 不同键分开
	v3, err := cached(ctx, svc, "report:v1:test:b", func(ctx context.Context) (cacheTestPayload, error) {
		atomic.AddInt64(&loadCount, 1)
		return cacheTestPayload{Count: 3, Name: "key_b"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if v3.Count != 3 || v3.Name != "key_b" {
		t.Fatalf("v3: %+v", v3)
	}
	if atomic.LoadInt64(&loadCount) != 2 {
		t.Fatalf("loadCount for different key: %d want 2", loadCount)
	}

	// 3. load 出错不写缓存
	errBoom := errors.New("boom")
	_, err = cached(ctx, svc, "report:v1:test:err", func(ctx context.Context) (cacheTestPayload, error) {
		return cacheTestPayload{}, errBoom
	})
	if !errors.Is(err, errBoom) {
		t.Fatalf("expected errBoom, got %v", err)
	}
	if mr.Exists("report:v1:test:err") {
		t.Fatal("failed load should not write cache")
	}

	// 4. Redis 关掉（mr.Close()）后照常 load 不报错
	mr.Close()
	v4, err := cached(ctx, svc, "report:v1:test:closed", func(ctx context.Context) (cacheTestPayload, error) {
		atomic.AddInt64(&loadCount, 1)
		return cacheTestPayload{Count: 4, Name: "after_close"}, nil
	})
	if err != nil {
		t.Fatalf("cached should not fail when redis is closed: %v", err)
	}
	if v4.Count != 4 || v4.Name != "after_close" {
		t.Fatalf("v4: %+v", v4)
	}
}
