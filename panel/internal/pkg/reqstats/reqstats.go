// Package reqstats 在 ctx 里携带每个请求的计数器（SQL 条数、SQL 总耗时、Redis 命令数），
// 由 httpx.RequestID 中间件放进 ctx；pkg/db 的 GORM 回调与 pkg/redisx 的 Hook 累加，访问日志与指标读取。
// 只用于观测，不影响业务行为。
package reqstats

import (
	"context"
	"sync/atomic"
	"time"
)

// Stats 是一个请求的计数器，可被多个 goroutine 并发累加。方法对 nil 接收者安全（ctx 里没有时什么也不做）。
type Stats struct {
	sqlN     atomic.Int64
	sqlNanos atomic.Int64
	redisN   atomic.Int64
}

// New 返回清零的计数器。
func New() *Stats { return &Stats{} }

type key struct{}

// With 把计数器放进 ctx。
func With(ctx context.Context, s *Stats) context.Context {
	return context.WithValue(ctx, key{}, s)
}

// From 取 ctx 里的计数器；不在 HTTP 请求里（任务、命令行）时为 nil。
func From(ctx context.Context) *Stats {
	if ctx == nil {
		return nil
	}
	s, _ := ctx.Value(key{}).(*Stats)
	return s
}

// AddSQL 记一条 SQL 及其耗时。
func (s *Stats) AddSQL(d time.Duration) {
	if s == nil {
		return
	}
	s.sqlN.Add(1)
	if d > 0 {
		s.sqlNanos.Add(int64(d))
	}
}

// AddRedis 记 n 条 Redis 命令（pipeline 按命令数记）。
func (s *Stats) AddRedis(n int) {
	if s == nil || n <= 0 {
		return
	}
	s.redisN.Add(int64(n))
}

// SQLCount 返回 SQL 条数。
func (s *Stats) SQLCount() int64 {
	if s == nil {
		return 0
	}
	return s.sqlN.Load()
}

// SQLDuration 返回 SQL 总耗时。
func (s *Stats) SQLDuration() time.Duration {
	if s == nil {
		return 0
	}
	return time.Duration(s.sqlNanos.Load())
}

// RedisCount 返回 Redis 命令数。
func (s *Stats) RedisCount() int64 {
	if s == nil {
		return 0
	}
	return s.redisN.Load()
}
