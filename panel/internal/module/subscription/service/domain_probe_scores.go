// 门户探测页上报的分数防操纵：票据一次性；账号×域名 30 分钟只记一次；失败需同一小时桶内至少 3 个不同账号才扣分；
// 每个域名每小时探测净变动封顶 ±0.5。以上在一段 Lua 里原子完成，一次上报一次 Redis 往返。

package service

import (
	"context"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	// probeTicketKeyPrefix、probeSeenKeyPrefix、probeFailKeyPrefix、probeNetKeyPrefix 是探测防操纵用的 Redis 键前缀。
	probeTicketKeyPrefix = "subdom:probe:ticket:"
	probeSeenKeyPrefix   = "subdom:probe:seen:"
	probeFailKeyPrefix   = "subdom:probe:fail:"
	probeNetKeyPrefix    = "subdom:probe:net:"
	// probeDedupeWindow 内同一账号对同一域名只记一次（成败都算一次）。
	probeDedupeWindow = 30 * time.Minute
	// probeFailMinReporters 是同一小时里至少要有这么多不同账号报同一域名失败，失败才开始扣分。
	probeFailMinReporters = 3
	// probeWindow 是失败人数与净变动的统计窗口（固定小时桶）。
	probeWindow = time.Hour
	// probeNetCap 是每个域名每小时探测带来的净变动上限（正负都封顶）。
	probeNetCap = 0.5
)

// probeSignal 是一条已核对过的探测结果（域名已在票据所绑组里、同一次上报里不重复）。
type probeSignal struct {
	BaseURL string
	OK      bool
}

// applyProbeScript 原子处理一次探测上报（KEYS[1] 分数 hash，KEYS[2] 票据键，之后每条信号三个键：seen、fail、net）。
var applyProbeScript = redis.NewScript(luaApplyDelta + `
if not redis.call('SET', KEYS[2], '1', 'NX', 'PX', ARGV[6]) then return 0 end
local now = tonumber(ARGV[1])
local hl = tonumber(ARGV[2])
local lo = tonumber(ARGV[3])
local hi = tonumber(ARGV[4])
local ttl = tonumber(ARGV[5])
local dedupe = tonumber(ARGV[7])
local minfail = tonumber(ARGV[8])
local win = tonumber(ARGV[9])
local cap = tonumber(ARGV[10])
local succ = tonumber(ARGV[11])
local fail = tonumber(ARGV[12])
local acct = ARGV[13]
local n = (#ARGV - 13) / 2
local applied = 0
for i = 0, n - 1 do
  local seen = KEYS[3 + 3 * i]
  local fset = KEYS[4 + 3 * i]
  local net = KEYS[5 + 3 * i]
  local field = ARGV[14 + 2 * i]
  local ok = ARGV[15 + 2 * i] == '1'
  if redis.call('SET', seen, '1', 'NX', 'PX', dedupe) then
    local delta = 0
    if ok then
      delta = succ
    else
      redis.call('SADD', fset, acct)
      redis.call('PEXPIRE', fset, win * 2)
      if redis.call('SCARD', fset) >= minfail then delta = fail end
    end
    if delta ~= 0 then
      local used = tonumber(redis.call('GET', net) or '0') or 0
      local target = math.min(math.max(used + delta, -cap), cap)
      local nd = target - used
      if math.abs(nd) > 1e-9 then
        redis.call('SET', net, string.format('%.4f', target), 'PX', win * 2)
        apply_delta(KEYS[1], field, nd, now, hl, lo, hi)
        applied = applied + 1
      end
    end
  end
end
if applied > 0 then redis.call('PEXPIRE', KEYS[1], ttl) end
return applied
`)

// ApplyProbe 原子地记一次探测上报，返回这次真正改动了分数的域名个数（测试用）。
// acct 是账号 id 串，nonce 是票据随机串，ticketLeft 是票据剩余有效期（小于 1 秒按 1 秒）。
// Redis 不可用（d 或 d.rdb 为 nil）或 signals 为空时回 (0, nil).
func (d *domainScores) ApplyProbe(ctx context.Context, acct, nonce string, ticketLeft time.Duration, signals []probeSignal, now time.Time) (int, error) {
	if d == nil || d.rdb == nil || len(signals) == 0 {
		return 0, nil
	}
	left := ticketLeft
	if left < time.Second {
		left = time.Second
	}
	bucket := strconv.FormatInt(now.Unix()/int64(probeWindow/time.Second), 10)
	names := make([]string, 0, 2+3*len(signals))
	names = append(names, domainScoreKey, probeTicketKeyPrefix+nonce)
	args := []any{
		now.UnixMilli(),
		domainScoreHalfLife.Milliseconds(),
		domainScoreMin,
		domainScoreMax,
		domainScoreTTL.Milliseconds(),
		left.Milliseconds(),
		probeDedupeWindow.Milliseconds(),
		probeFailMinReporters,
		probeWindow.Milliseconds(),
		probeNetCap,
		scoreProbeSuccess,
		scoreProbeFailure,
		acct,
	}
	for _, sig := range signals {
		names = append(names, probeRedisName(probeSeenKeyPrefix, acct, sig.BaseURL),
			probeRedisName(probeFailKeyPrefix, sig.BaseURL, bucket),
			probeRedisName(probeNetKeyPrefix, sig.BaseURL, bucket))
		okFlag := "0"
		if sig.OK {
			okFlag = "1"
		}
		args = append(args, sig.BaseURL, okFlag)
	}
	cctx, cancel := context.WithTimeout(ctx, domainScoreTimeout)
	defer cancel()
	applied, err := applyProbeScript.Run(cctx, d.rdb, names, args...).Int()
	if err != nil {
		return 0, err
	}
	return applied, nil
}

// probeRedisName 拼探测防操纵用的 Redis 键名：前缀 + a + "|" + b。
func probeRedisName(prefix, a, b string) string {
	return prefix + a + "|" + b
}
