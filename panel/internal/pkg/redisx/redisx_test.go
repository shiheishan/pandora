package redisx

import (
	"context"
	"testing"

	"github.com/aegispanel/aegis/internal/pkg/config"
	"github.com/alicebob/miniredis/v2"
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
