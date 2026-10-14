package ratelimit

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// Counter 是 Redis 里的试错计数（登录失败、二次验证、改密校验当前口令等）：先占名额再验，
// 校验通过后 Reset，校验没有结论时 Release 退回名额。并发请求各占一个名额，绕不过上限。
// 与 Limiter 的区别：Limiter 只管放行与否；Counter 把累计次数交给调用方，由调用方按产品规则
// （例如登录失败 10 次停 15 分钟）决定怎么处理。
type Counter struct{ rdb *redis.Client }

// NewCounter 用业务 Redis 客户端构造计数器。
func NewCounter(rdb *redis.Client) *Counter { return &Counter{rdb: rdb} }

// 先占名额再验：INCR；第一次设窗口期；到阈值（ARGV[2] > 0 时）那一次把期限改成暂停时长。
// 返回 {累计次数（含本次）, 剩余秒数}。
var counterIncrScript = redis.NewScript(`
local n = redis.call('INCR', KEYS[1])
if n == 1 then redis.call('EXPIRE', KEYS[1], ARGV[1]) end
local th = tonumber(ARGV[2])
if th > 0 and n == th then redis.call('EXPIRE', KEYS[1], ARGV[3]) end
return {n, redis.call('TTL', KEYS[1])}
`)

// Incr 占一次名额，返回窗口内累计次数（含本次）与键的剩余期限。
// 第一次占名额时设期限 window；threshold 为 0 时不再改期限，否则第 threshold 次把期限改成 pause。
func (c *Counter) Incr(ctx context.Context, key string, window time.Duration,
	threshold int, pause time.Duration) (int, time.Duration, error) {
	res, err := counterIncrScript.Run(ctx, c.rdb, []string{key},
		int64(window/time.Second), threshold, int64(pause/time.Second)).Int64Slice()
	if err != nil {
		return 0, 0, err
	}
	if len(res) != 2 {
		return 0, 0, fmt.Errorf("试错计数脚本返回 %d 项", len(res))
	}
	return int(res[0]), time.Duration(res[1]) * time.Second, nil
}

// Reset 清掉计数（校验通过后调用）。
func (c *Counter) Reset(ctx context.Context, key string) error {
	return c.rdb.Del(ctx, key).Err()
}

// 退名额：键还在才 DECR（过期后不留下负数的新键）。
var counterReleaseScript = redis.NewScript(`
if redis.call('EXISTS', KEYS[1]) == 1 then return redis.call('DECR', KEYS[1]) end
return 0
`)

// Release 退回一次名额（校验没有结论，或口令其实是对的）。
func (c *Counter) Release(ctx context.Context, key string) error {
	return counterReleaseScript.Run(ctx, c.rdb, []string{key}).Err()
}
