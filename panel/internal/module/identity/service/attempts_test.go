package service

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/testkit"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// 连续 6 次：前 5 次放行，第 6 次 429 且带剩余窗口；校验通过清零后重新计数。
func TestClaimPasswordAttemptSequential(t *testing.T) {
	_, rdb := testkit.Redis(t)
	s := &Service{rdb: rdb}
	ctx := context.Background()
	uid := uuid.New()

	for i := 0; i < passwordFailLimit; i++ {
		if err := s.claimPasswordAttempt(ctx, uid); err != nil {
			t.Fatalf("第 %d 次应放行: %v", i+1, err)
		}
	}
	err := s.claimPasswordAttempt(ctx, uid)
	e := errs.As(err)
	if err == nil || e.Code != errs.RateLimited {
		t.Fatalf("第 6 次应 429: %v", err)
	}
	if e.RetryAfterSeconds < 1 || e.RetryAfterSeconds > int(passwordFailWindow.Seconds()) {
		t.Fatalf("RetryAfterSeconds=%d", e.RetryAfterSeconds)
	}
	ttl, err := rdb.TTL(ctx, passwordFailKey(uid)).Result()
	if err != nil || ttl <= 0 || ttl > passwordFailWindow {
		t.Fatalf("窗口 ttl=%v err=%v", ttl, err)
	}

	s.clearPasswordAttempts(ctx, uid)
	if err := s.claimPasswordAttempt(ctx, uid); err != nil {
		t.Fatalf("清零后应放行: %v", err)
	}
	// 别的账号互不影响
	if err := s.claimPasswordAttempt(ctx, uuid.New()); err != nil {
		t.Fatalf("另一账号: %v", err)
	}
}

// 并发 100 个请求最多 5 个能进入校验。
func TestClaimPasswordAttemptConcurrent(t *testing.T) {
	_, rdb := testkit.Redis(t)
	s := &Service{rdb: rdb}
	uid := uuid.New()

	var passed, limited atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := s.claimPasswordAttempt(context.Background(), uid)
			switch {
			case err == nil:
				passed.Add(1)
			case errs.As(err).Code == errs.RateLimited:
				limited.Add(1)
			default:
				t.Errorf("意外错误: %v", err)
			}
		}()
	}
	wg.Wait()
	if passed.Load() != passwordFailLimit || limited.Load() != 100-passwordFailLimit {
		t.Fatalf("passed=%d limited=%d", passed.Load(), limited.Load())
	}
}

// Redis 不通时回 503，不放行。
func TestClaimPasswordAttemptRedisDown(t *testing.T) {
	rdb := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", MaxRetries: -1, DialTimeout: 200 * time.Millisecond})
	t.Cleanup(func() { _ = rdb.Close() })
	s := &Service{rdb: rdb}
	if err := s.claimPasswordAttempt(context.Background(), uuid.New()); errs.As(err).Code != errs.Unavailable {
		t.Fatalf("应 503: %v", err)
	}
}

// 没装配限流器时回 500，不静默放行。
func TestLimitWithoutLimiterIsInternal(t *testing.T) {
	s := &Service{}
	if err := s.limit(context.Background(), ruleRegisterEmail, "a@b.co"); errs.As(err).Code != errs.Internal {
		t.Fatalf("应 internal_error: %v", err)
	}
}
