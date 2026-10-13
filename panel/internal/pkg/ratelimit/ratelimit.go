// Package ratelimit 基于 Redis 的限流，供 Gin 中间件与直接调用。
//
// 缺省是 GCRA（go-redis/redis_rate）：平滑、防刷用；它允许一次用满 Burst 后按速率回补，
// 任意一个窗口里最多约 2n−1 次。产品规则里写死的「每天 5 次」这类严格上限用 FixedWindow（计数加过期）。
package ratelimit

import (
	"context"
	"math"
	"net/netip"
	"strings"
	"time"

	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/httpx"
	plog "github.com/aegispanel/aegis/internal/pkg/log"
	"github.com/gin-gonic/gin"
	"github.com/go-redis/redis_rate/v10"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

// Rule 描述一条限流规则；Name 仅用小写字母、数字与下划线。
type Rule struct {
	Name   string
	Limit  redis_rate.Limit
	Strict bool
	// Fixed 为真时按固定窗口计数（见 FixedWindow），不走 GCRA
	Fixed bool
}

// PerMinute 构造每分钟最多 n 次的规则。
func PerMinute(name string, n int) Rule {
	return Rule{
		Name:  name,
		Limit: redis_rate.Limit{Rate: n, Burst: n, Period: time.Minute},
	}
}

// PerWindow 构造在 window 内最多 n 次的规则。
func PerWindow(name string, n int, window time.Duration) Rule {
	return Rule{
		Name:  name,
		Limit: redis_rate.Limit{Rate: n, Burst: n, Period: window},
	}
}

// FixedWindow 构造固定窗口的严格上限：从窗口内第一次起算，window 内最多 n 次，窗口过期后重新计数。
func FixedWindow(name string, n int, window time.Duration) Rule {
	return Rule{
		Name:  name,
		Limit: redis_rate.Limit{Rate: n, Burst: n, Period: window},
		Fixed: true,
	}
}

// Cooldown 构造每个窗口仅允许 1 次的规则。
func Cooldown(name string, window time.Duration) Rule {
	return PerWindow(name, 1, window)
}

// StrictMode 返回 Redis 故障时拒绝请求的规则副本。
func (r Rule) StrictMode() Rule {
	r.Strict = true
	return r
}

// Limiter 封装 redis_rate 限流器。
type Limiter struct {
	inner *redis_rate.Limiter
	rdb   *redis.Client
}

// New 用 Redis 客户端构造限流器。
func New(rdb *redis.Client) *Limiter {
	return &Limiter{inner: redis_rate.NewLimiter(rdb), rdb: rdb}
}

// Dimension 将规则与从请求提取的键绑定；Key 返回 false 时跳过该维度。
type Dimension struct {
	Rule Rule
	Key  func(*gin.Context) (string, bool)
}

// Allow 检查 key 是否仍有余量；key 为业务维度，不含规则名。
func (l *Limiter) Allow(ctx context.Context, rule Rule, key string) error {
	redisKey := "rl:" + rule.Name + ":" + key
	if rule.Fixed {
		return l.allowFixed(ctx, rule, redisKey)
	}
	res, err := l.inner.Allow(ctx, redisKey, rule.Limit)
	if err != nil {
		if rule.Strict {
			return errs.Wrap(errs.Unavailable, err)
		}
		plog.From(ctx).Warn("限流 Redis 不可用，非严格模式放行", zap.Error(err))
		return nil
	}
	if res.Allowed == 0 {
		e := errs.New(errs.RateLimited)
		e.RetryAfterSeconds = retryAfterSeconds(res.RetryAfter)
		return e
	}
	return nil
}

// fixedWindowScript 计数加一；第一次时设过期。返回 {计数, 剩余毫秒}。
var fixedWindowScript = redis.NewScript(`
local n = redis.call('INCR', KEYS[1])
if n == 1 then
  redis.call('PEXPIRE', KEYS[1], ARGV[1])
end
return {n, redis.call('PTTL', KEYS[1])}
`)

// allowFixed 是固定窗口计数：超过上限回 RateLimited，RetryAfter 是窗口剩余时间。
func (l *Limiter) allowFixed(ctx context.Context, rule Rule, redisKey string) error {
	res, err := fixedWindowScript.Run(ctx, l.rdb, []string{redisKey}, rule.Limit.Period.Milliseconds()).Int64Slice()
	if err != nil {
		if rule.Strict {
			return errs.Wrap(errs.Unavailable, err)
		}
		plog.From(ctx).Warn("限流 Redis 不可用，非严格模式放行", zap.Error(err))
		return nil
	}
	if len(res) == 2 && res[0] > int64(rule.Limit.Burst) {
		e := errs.New(errs.RateLimited)
		e.RetryAfterSeconds = retryAfterSeconds(time.Duration(res[1]) * time.Millisecond)
		return e
	}
	return nil
}

// Middleware 按顺序检查各维度，首个失败则中止请求。
func (l *Limiter) Middleware(dims ...Dimension) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx := c.Request.Context()
		for _, d := range dims {
			key, ok := d.Key(c)
			if !ok {
				continue
			}
			if err := l.Allow(ctx, d.Rule, key); err != nil {
				httpx.Fail(c, err)
				return
			}
		}
		c.Next()
	}
}

// ByIP 以客户端 IP 为键。
func ByIP(c *gin.Context) (string, bool) {
	ip := strings.TrimSpace(c.ClientIP())
	if ip == "" {
		return "", false
	}
	return ip, true
}

// ByIPPrefix 以 IPv4 /24 或 IPv6 /48 前缀为键。
func ByIPPrefix(c *gin.Context) (string, bool) {
	ip := strings.TrimSpace(c.ClientIP())
	if ip == "" {
		return "", false
	}
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return ip, true
	}
	addr = addr.Unmap()
	bits := 24
	if addr.Is6() {
		bits = 48
	}
	return netip.PrefixFrom(addr, bits).Masked().String(), true
}

// ByRoute 以方法、路由模板与客户端 IP 组合为键（不含原始路径）。
func ByRoute(c *gin.Context) (string, bool) {
	ip := strings.TrimSpace(c.ClientIP())
	if ip == "" {
		return "", false
	}
	path := c.FullPath()
	if path == "" {
		path = "-"
	}
	return c.Request.Method + ":" + path + "|" + ip, true
}

func retryAfterSeconds(d time.Duration) int {
	sec := int(math.Ceil(d.Seconds()))
	if sec < 1 {
		return 1
	}
	return sec
}
