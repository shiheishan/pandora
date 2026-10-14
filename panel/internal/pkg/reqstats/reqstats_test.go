package reqstats

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestStatsCountsConcurrently(t *testing.T) {
	s := New()
	ctx := With(context.Background(), s)
	var wg sync.WaitGroup
	for range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 100 {
				From(ctx).AddSQL(time.Millisecond)
				From(ctx).AddRedis(2)
			}
		}()
	}
	wg.Wait()
	if got := s.SQLCount(); got != 5000 {
		t.Fatalf("SQLCount = %d，应为 5000", got)
	}
	if got := s.SQLDuration(); got != 5000*time.Millisecond {
		t.Fatalf("SQLDuration = %v，应为 5s", got)
	}
	if got := s.RedisCount(); got != 10000 {
		t.Fatalf("RedisCount = %d，应为 10000", got)
	}
}

// ctx 里没有计数器时 From 为 nil，各方法什么也不做、读数为 0。
func TestNilStatsIsNoop(t *testing.T) {
	s := From(context.Background())
	if s != nil {
		t.Fatal("空 ctx 应取不到计数器")
	}
	s.AddSQL(time.Second)
	s.AddRedis(3)
	if s.SQLCount() != 0 || s.SQLDuration() != 0 || s.RedisCount() != 0 {
		t.Fatal("nil 计数器读数应为 0")
	}
}

func TestAddRedisIgnoresNonPositive(t *testing.T) {
	s := New()
	s.AddRedis(0)
	s.AddRedis(-1)
	s.AddSQL(-time.Second)
	if s.RedisCount() != 0 || s.SQLCount() != 1 || s.SQLDuration() != 0 {
		t.Fatalf("redis=%d sql=%d dur=%v", s.RedisCount(), s.SQLCount(), s.SQLDuration())
	}
}
