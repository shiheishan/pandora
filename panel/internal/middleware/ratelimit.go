package middleware

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// Limit 是一个限流维度。
type Limit struct {
	Name   string
	Window time.Duration
	Max    int
	// KeyFn 返回该维度的计数键；返回空串表示本次请求不适用此维度。
	KeyFn func(*http.Request) string
}

// rateLimitScript 在一次 Valkey 往返里按维度顺序计数（审计 P11）。
//
// 以前每个维度一次 INCR 往返、串行执行，首次建键还要再补一次 EXPIRE（两条不原子：
// 进程恰在中间退出，计数键就永不过期）。现在整组维度一个脚本：
//   - 维度按声明顺序逐个 INCR，首次建键当场 PEXPIRE，原子；
//   - 遇到第一个超限的维度立即返回，后面的维度不计数——与原来逐个往返、超限即拒的
//     短路完全一致，被拒的请求不会多吃其它维度的额度；
//   - 经 EVALSHA 发出（redis.Script 在脚本缓存丢失时自动退回 EVAL），不必每次带整段脚本。
//
// KEYS 是各维度的计数键；ARGV 依次是每个维度的 (上限, 过期毫秒)。
// 返回 {序号, 计数}：序号为 0 表示全部放行，否则是第一个超限维度在 KEYS 里的序号（从 1 起）。
const rateLimitScriptSource = `
for i, key in ipairs(KEYS) do
  local n = redis.call('INCR', key)
  if n == 1 then
    redis.call('PEXPIRE', key, ARGV[2 * i])
  end
  if n > tonumber(ARGV[2 * i - 1]) then
    return {i, n}
  end
end
return {0, 0}`

var rateLimitScript = redis.NewScript(rateLimitScriptSource)

// rateLimitHit 是一次限流判定的结果：limit 为 nil 表示放行。
type rateLimitHit struct {
	limit *Limit
	count int64
}

// checkLimits 计算本请求适用的维度，在一次往返里计数并判定。没有适用维度时不碰 Valkey。
// 计数键的窗口编号仍按各维度自己的窗口在本进程取当前时刻，与原来逐个计数时同一口径。
func checkLimits(ctx context.Context, rdb *redis.Client, r *http.Request, limits []Limit) (rateLimitHit, error) {
	var keys []string
	var args []any
	var applied []*Limit
	for i := range limits {
		l := &limits[i]
		suffix := l.KeyFn(r)
		if suffix == "" {
			continue
		}
		keys = append(keys, fmt.Sprintf("rl:%s:%s:%d", l.Name, suffix,
			time.Now().UnixNano()/int64(l.Window)))
		args = append(args, l.Max, (l.Window + time.Second).Milliseconds())
		applied = append(applied, l)
	}
	if len(keys) == 0 {
		return rateLimitHit{}, nil
	}
	if rdb == nil {
		return rateLimitHit{}, errors.New("限流后端未配置")
	}
	res, err := rateLimitScript.Run(ctx, rdb, keys, args...).Int64Slice()
	if err != nil {
		return rateLimitHit{}, err
	}
	if len(res) != 2 || res[0] < 0 || res[0] > int64(len(applied)) {
		return rateLimitHit{}, fmt.Errorf("限流脚本返回了意外的结果 %v", res)
	}
	if res[0] == 0 {
		return rateLimitHit{}, nil
	}
	return rateLimitHit{limit: applied[res[0]-1], count: res[1]}, nil
}

// RateLimit 按多个维度联合限流。
//
// SEC-002 验收要求「攻击者不能通过轮换单一维度轻易绕过」，
// 因此这里对每个维度独立计数，任一维度超限即拒绝 ——
// 换 IP 挡不住账号维度，换账号挡不住 IP 段维度。
//
// 全部维度在一次 Valkey 往返里判定（rateLimitScript）。
//
// Redis 不可用时选择放行而非拒绝：限流是防滥用手段，
// 让它的故障演变成全站不可用是本末倒置（NFR-002 舱壁思想）。
func RateLimit(rdb *redis.Client, log *slog.Logger, limits ...Limit) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()
			hit, err := checkLimits(ctx, rdb, r, limits)
			if err != nil {
				log.Warn("限流计数失败，本次放行",
					slog.String("dimensions", limitNames(limits)),
					slog.String("error", err.Error()))
				next.ServeHTTP(w, r)
				return
			}
			if hit.limit != nil {
				log.Info("触发限流",
					slog.String("dimension", hit.limit.Name),
					slog.Int64("count", hit.count),
					slog.Int("max", hit.limit.Max),
					slog.String("request_id", httpx.RequestIDFrom(ctx)))
				httpx.Fail(w, r, log,
					httpx.New(httpx.CodeRateLimited, "请求过于频繁，请稍后再试"))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// RateLimitStrict is for security-critical anonymous mutations. Any Redis
// counter or expiry failure returns a neutral 503 instead of bypassing controls.
// 计数与过期在同一个脚本里原子完成，所有维度一次往返（与 RateLimit 同一个脚本）。
func RateLimitStrict(rdb *redis.Client, log *slog.Logger, limits ...Limit) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hit, err := checkLimits(r.Context(), rdb, r, limits)
			if err != nil {
				log.Warn("strict rate-limit counter failed",
					slog.String("dimensions", limitNames(limits)), slog.String("error", err.Error()))
				httpx.Fail(w, r, log, httpx.New(httpx.CodeUnavailable,
					"服务暂时不可用，请稍后重试"))
				return
			}
			if hit.limit != nil {
				httpx.Fail(w, r, log,
					httpx.New(httpx.CodeRateLimited, "请求过于频繁，请稍后再试"))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// limitNames 把维度名拼成一串，只用于日志。
func limitNames(limits []Limit) string {
	var b []byte
	for i, l := range limits {
		if i > 0 {
			b = append(b, ',')
		}
		b = append(b, l.Name...)
	}
	return string(b)
}
