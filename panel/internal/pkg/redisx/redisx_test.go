package redisx

import (
	"context"
	"testing"

	"github.com/aegispanel/aegis/internal/pkg/config"
	"github.com/aegispanel/aegis/internal/pkg/reqstats"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestNewPingAndOptions(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mr.Close)

	cfg := config.Redis{
		URL:       config.Secret("redis://" + mr.Addr() + "/0"),
		PoolSize:  4,
		OpTimeout: 2e9, // 2s，避免零超时
	}
	c, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })

	if !c.Options().ContextTimeoutEnabled {
		t.Fatal("ContextTimeoutEnabled 应为 true")
	}
	if err := Ping(context.Background(), c); err != nil {
		t.Fatalf("Ping: %v", err)
	}
}

func TestNewBadURL(t *testing.T) {
	_, err := New(config.Redis{URL: config.Secret("://bad")})
	if err == nil {
		t.Fatal("期望非法 URL 报错")
	}
}

func TestStatsHookCounts(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mr.Close)

	cfg := config.Redis{
		URL:       config.Secret("redis://" + mr.Addr() + "/0"),
		PoolSize:  4,
		OpTimeout: 2e9,
	}
	c, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })

	// 先用 context.Background() Ping 一次让连接建好（建连握手不计入）
	if err := c.Ping(context.Background()).Err(); err != nil {
		t.Fatalf("Ping: %v", err)
	}

	s := reqstats.New()
	ctx := reqstats.With(context.Background(), s)

	if err := c.Set(ctx, "k", "v", 0).Err(); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := c.Get(ctx, "k").Err(); err != nil {
		t.Fatalf("Get: %v", err)
	}

	_, err = c.Pipelined(ctx, func(p redis.Pipeliner) error {
		p.Incr(ctx, "a")
		p.Incr(ctx, "b")
		p.Incr(ctx, "c")
		return nil
	})
	if err != nil {
		t.Fatalf("Pipelined: %v", err)
	}

	if got := s.RedisCount(); got != 5 {
		t.Fatalf("RedisCount = %d, 期望 5", got)
	}

	// 再用 context.Background() 执行一次 Get，断言 s 仍是 5
	if err := c.Get(context.Background(), "k").Err(); err != nil {
		t.Fatalf("Get background: %v", err)
	}
	if got := s.RedisCount(); got != 5 {
		t.Fatalf("RedisCount after background = %d, 期望 5", got)
	}
}
