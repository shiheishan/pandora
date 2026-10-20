// 门户探测页上报的分数防操纵：票据一次性；账号×域名 30 分钟只记一次；成功与失败对称——同一小时桶内凑够不同账号
// 报同一域名成功才加分、报失败才扣分（防单个账号长期把某域名顶高、把用户引向被监控的域名，或把某域名压低）；
// 每个域名每小时每一级探测净变动封顶 ±0.5。以上在一段 Lua 里原子完成，一次上报一次 Redis 往返。
// 一次上报按上报者 IP 的地区同时作用于全国 / 省 / 省|运营商三级（地区不可用只作用于全国），人数门槛与净变动封顶按级别分开计：
// 全国一级 3 个账号；省、省|运营商要 5 个——小桶里能抵消的真实拉取少，门槛更高（10-20 审查应改 1）。
// 全国加减在绝对分上，细分级加减在相对上一级的偏移上（domain_scores.go），所以细分级的封顶就是「相对上一级每小时最多偏 0.5」。
// seen 去重仍按账号 × 域名。
// 拉取成功信号不走这里：它是真实流量，已按令牌主人所在组、同一凭据每小时一次计。

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
	probeOKKeyPrefix     = "subdom:probe:ok:"
	probeFailKeyPrefix   = "subdom:probe:fail:"
	probeNetKeyPrefix    = "subdom:probe:net:"
	// probeTicketKeyTTL 是票据一次性键的 TTL：票据有效期加验签容许的 60 秒偏差（parseProbeTicket）。
	probeTicketKeyTTL = probeTicketTTL + time.Minute
	// probeDedupeWindow 内同一账号对同一域名只记一次（成败都算一次）。
	probeDedupeWindow = 30 * time.Minute
	// probeMinReporters 是全国一级：同一小时里至少要有这么多不同账号报同一域名成功（或失败），成功才加分（失败才扣分）。
	probeMinReporters = 3
	// probeMinReportersRegional 是省、省|运营商两级的人数门槛（同上，按级别分开数）。
	probeMinReportersRegional = 5
	// probeWindow 是上报人数与净变动的统计窗口（固定小时桶）。
	probeWindow = time.Hour
	// probeNetCap 是每个域名每小时探测带来的净变动上限（正负都封顶）。
	probeNetCap = 0.5
)

// probeSignal 是一条已核对过的探测结果（域名已在票据所绑组里、同一次上报里不重复）。
type probeSignal struct {
	BaseURL string
	OK      bool
}

// applyProbeScript 原子处理一次探测上报。
// KEYS[1] = 票据键；每条信号占 2+3*nl 个键：分数 hash、seen、再对每个 field 各 ok 集合、fail 集合、net。
// ARGV[1..13]：now 毫秒、半衰期毫秒、全国分下限、上限、分数 TTL 毫秒、票据 TTL 毫秒、去重毫秒、全国一级最少人数、窗口毫秒、
// 封顶、成功分、失败分、acct；ARGV[14] = 细分级最少人数；ARGV[15]、ARGV[16] = 偏移下限、上限；ARGV[17] = 细分级新鲜度毫秒；
// ARGV[18] = nl（级别数）；接着 nl 个 field（第一个是全国 "*"）；接着每条信号一个 okFlag（"1" / "0"）。
var applyProbeScript = redis.NewScript(luaApplyDelta + `
if not redis.call('SET', KEYS[1], '1', 'NX', 'PX', ARGV[6]) then return 0 end
local now = tonumber(ARGV[1])
local cfg = {hl = tonumber(ARGV[2]), lo = tonumber(ARGV[3]), hi = tonumber(ARGV[4]),
  olo = tonumber(ARGV[15]), ohi = tonumber(ARGV[16]), fresh = tonumber(ARGV[17])}
local ttl = tonumber(ARGV[5])
local dedupe = tonumber(ARGV[7])
local minrepAll = tonumber(ARGV[8])
local minrepSub = tonumber(ARGV[14])
local win = tonumber(ARGV[9])
local cap = tonumber(ARGV[10])
local succ = tonumber(ARGV[11])
local fail = tonumber(ARGV[12])
local acct = ARGV[13]
local nl = tonumber(ARGV[18])
local fbase = 19
local per = 2 + 3 * nl
local n = (#KEYS - 1) / per
local applied = 0
for i = 0, n - 1 do
  local o = 2 + per * i
  local skey = KEYS[o]
  local seen = KEYS[o + 1]
  local ok = ARGV[fbase + nl + i] == '1'
  if redis.call('SET', seen, '1', 'NX', 'PX', dedupe) then
    local touched = false
    for j = 0, nl - 1 do
      local okset = KEYS[o + 2 + 3 * j]
      local fset = KEYS[o + 3 + 3 * j]
      local net = KEYS[o + 4 + 3 * j]
      local field = ARGV[fbase + j]
      local minrep = minrepSub
      if field == '*' then minrep = minrepAll end
      local set, d = fset, fail
      if ok then set, d = okset, succ end
      redis.call('SADD', set, acct)
      redis.call('PEXPIRE', set, win * 2)
      if redis.call('SCARD', set) >= minrep then
        local used = tonumber(redis.call('GET', net) or '0') or 0
        local target = math.min(math.max(used + d, -cap), cap)
        local nd = target - used
        if math.abs(nd) > 1e-9 then
          redis.call('SET', net, string.format('%.4f', target), 'PX', win * 2)
          apply_delta(skey, field, nd, now, cfg)
          touched = true
        end
      end
    end
    if touched then
      redis.call('PEXPIRE', skey, ttl)
      applied = applied + 1
    end
  end
end
return applied
`)

// ApplyProbe 原子地记一次探测上报，返回这次真正改动了分数的域名个数（测试用）。
// acct 是账号 id 串，nonce 是票据随机串，region 是上报者 IP 的地区。票据键的 TTL 固定为 probeTicketKeyTTL（票据有效期加验签容许的偏差），
// 不按本进程时钟算剩余时长：多进程时钟有偏差时也不会在键过期后还留出重放窗口。
// Redis 不可用（d 或 d.rdb 为 nil）或 signals 为空时回 (0, nil)。
func (d *domainScores) ApplyProbe(ctx context.Context, acct, nonce string, region domainRegion, signals []probeSignal, now time.Time) (int, error) {
	if d == nil || d.rdb == nil || len(signals) == 0 {
		return 0, nil
	}
	fields := region.fields()
	nl := len(fields)
	bucket := probeHourBucket(now)
	names := make([]string, 0, 1+len(signals)*(2+3*nl))
	names = append(names, probeTicketKeyPrefix+nonce)
	args := []any{
		now.UnixMilli(),
		domainScoreHalfLife.Milliseconds(),
		domainScoreMin,
		domainScoreMax,
		domainScoreTTL.Milliseconds(),
		probeTicketKeyTTL.Milliseconds(),
		probeDedupeWindow.Milliseconds(),
		probeMinReporters,
		probeWindow.Milliseconds(),
		probeNetCap,
		scoreProbeSuccess,
		scoreProbeFailure,
		acct,
		probeMinReportersRegional,
		domainOffsetMin,
		domainOffsetMax,
		domainScoreFresh.Milliseconds(),
		nl,
	}
	for _, f := range fields {
		args = append(args, f)
	}
	for _, sig := range signals {
		names = append(names, domainScoreKeyFor(sig.BaseURL),
			probeRedisName(probeSeenKeyPrefix, acct, sig.BaseURL))
		for _, field := range fields {
			names = append(names,
				probeLevelName(probeOKKeyPrefix, sig.BaseURL, field, bucket),
				probeLevelName(probeFailKeyPrefix, sig.BaseURL, field, bucket),
				probeLevelName(probeNetKeyPrefix, sig.BaseURL, field, bucket))
		}
		okFlag := "0"
		if sig.OK {
			okFlag = "1"
		}
		args = append(args, okFlag)
	}
	cctx, cancel := context.WithTimeout(ctx, domainScoreTimeout)
	defer cancel()
	applied, err := applyProbeScript.Run(cctx, d.rdb, names, args...).Int()
	if err != nil {
		return 0, err
	}
	return applied, nil
}

// probeLevelName 拼按级别分开计的探测键：prefix + baseURL + "|" + field + "|" + bucket。
func probeLevelName(prefix, baseURL, field, bucket string) string {
	return prefix + baseURL + "|" + field + "|" + bucket
}

// probeRedisName 拼探测防操纵用的 Redis 键名：前缀 + a + "|" + b。
func probeRedisName(prefix, a, b string) string {
	return prefix + a + "|" + b
}

// probeHourBucket 是 now 所在的固定小时桶编号（unix 秒 / 3600），上报人集合与净变动按它分桶。
func probeHourBucket(now time.Time) string {
	return strconv.FormatInt(now.Unix()/int64(probeWindow/time.Second), 10)
}
