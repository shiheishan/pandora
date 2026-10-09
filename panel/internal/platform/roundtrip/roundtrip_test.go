package roundtrip

import (
	"context"
	"net"
	"testing"

	"github.com/redis/go-redis/v9"
)

type otherKey struct{}

func TestScopeCarriesCounterAndDelegates(t *testing.T) {
	parent := context.WithValue(context.Background(), otherKey{}, "v")
	if From(parent) != nil {
		t.Fatal("a context without a counter must yield nil")
	}
	var s Scope
	s.Init(parent)
	var ctx context.Context = &s
	if From(ctx) != &s.Counter {
		t.Fatal("From must find the scope's own counter")
	}
	if ctx.Value(otherKey{}) != "v" {
		t.Fatal("scope must delegate other keys to its parent")
	}
	// 子 context 一路往上能找到同一个计数器
	child, cancel := context.WithCancel(context.WithValue(ctx, otherKey{}, "w"))
	defer cancel()
	From(child).AddDB(2)
	From(child).AddPrepare()
	From(child).AddPing()
	From(child).AddReset()
	From(child).AddKV()
	got := s.Counter.Snapshot()
	want := Counts{DB: 2, Prepare: 1, Ping: 1, Reset: 1, KV: 1}
	if got != want || got.DBTotal() != 5 {
		t.Fatalf("snapshot = %+v (total %d), want %+v (total 5)", got, got.DBTotal(), want)
	}

	var c Counter
	if From(WithCounter(context.Background(), &c)) != &c {
		t.Fatal("WithCounter must be found by From")
	}
}

// terminal 代替 Valkey：钩子链的最后一环，不连网络。
type terminal struct{ calls int }

func (f *terminal) DialHook(redis.DialHook) redis.DialHook {
	return func(context.Context, string, string) (net.Conn, error) { return nil, net.ErrClosed }
}

func (f *terminal) ProcessHook(redis.ProcessHook) redis.ProcessHook {
	return func(context.Context, redis.Cmder) error { f.calls++; return nil }
}

func (f *terminal) ProcessPipelineHook(redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return func(context.Context, []redis.Cmder) error { f.calls++; return nil }
}

func TestValkeyHookCountsCommandsAndPipelines(t *testing.T) {
	rdb := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", MaxRetries: -1})
	t.Cleanup(func() { _ = rdb.Close() })
	fake := &terminal{}
	rdb.AddHook(ValkeyHook{}) // 先挂的在外层
	rdb.AddHook(fake)

	var c Counter
	ctx := WithCounter(context.Background(), &c)
	_ = rdb.Get(ctx, "a").Err()
	_ = rdb.SetNX(ctx, "b", "1", 0).Err()
	if _, err := rdb.Pipelined(ctx, func(p redis.Pipeliner) error {
		p.Incr(ctx, "x")
		p.Incr(ctx, "y")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := rdb.TxPipelined(ctx, func(p redis.Pipeliner) error {
		p.Incr(ctx, "z")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// 不带计数器的 ctx 不记
	_ = rdb.Get(context.Background(), "a").Err()

	if got := c.Snapshot().KV; got != 4 {
		t.Fatalf("kv round trips = %d, want 4 (2 commands + 2 pipelines)", got)
	}
	if fake.calls != 5 {
		t.Fatalf("terminal saw %d calls, want 5", fake.calls)
	}
}

func BenchmarkFromDeepContext(b *testing.B) {
	var s Scope
	s.Init(context.Background())
	var ctx context.Context = &s
	for i := 0; i < 8; i++ { // 与请求里鉴权、租户、请求 ID 等层数相当
		ctx = context.WithValue(ctx, otherKey{}, i)
	}
	b.ReportAllocs()
	for b.Loop() {
		if c := From(ctx); c != nil {
			c.AddDB(1)
		}
	}
}
