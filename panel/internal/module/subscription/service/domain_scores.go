// 订阅域名的分数（Redis，多进程共享）：有效权重 = 基础权重 × 分数，按分数放大或缩小一个域名的被选中概率。
// 每个域名一个 hash（key = subdom:score:<base_url>），field 为全国 "*"、省、省|运营商三级：
// 全国存绝对分（向 1 衰减、夹在 [domainScoreMin, domainScoreMax]）；省存相对全国的偏移、省|运营商存相对省的偏移
// （向 0 衰减、夹在 [domainOffsetMin, domainOffsetMax]）。读分 = clamp(全国分 + 省偏移 + 省|运营商偏移, 0.1, 3)，
// 某一级 24 小时内没写过（domainScoreFresh）偏移按 0 算；全国分不受新鲜度约束（与分桶前一致）。
// 这样同组的域名在任何地区都按同一口径（全国分）比较，细分级只在它之上加减，样本少的地区不会把全国分盖掉。
// 一次信号的加减量加在它写到的每一级上（地区不可用只写 "*"）：成功信号在全国加在绝对分上、在细分级加在偏移上，
// 所以一次成功在任何地区读到的分数都不会降。
// 分数有两个来源——(1) 经令牌主人所在组的已登记域名到达的成功拉取（每次 +0.05，同一凭据对同一域名每小时一次，进程内攒批）；
// (2) 门户探测页的上报（成功 +0.1、失败 −0.3，防操纵规则见 domain_probe_scores.go）。半衰期 6 小时；
// 连不上的域名拿不到加分，相对权重随衰减自然变低。读分夹在 [domainScoreMin, domainScoreMax]：
// 下限 0.1 不让任何域名被压成 0；上限 3 防止单个域名独占流量。
// 每一级都带写入时刻存成 hash（value = "<分数或偏移 四位小数>|<unix 毫秒>"），读时现算衰减。
// Redis 不可用时按基础权重（分数当 1），不让生成链接失败。
// 成功信号在进程内攒 domainScoreFlushEvery（10 秒）一批写，避免每次拉取都多一次 Redis 往返；
// 读分数时也顺带冲刷攒下的信号（低流量时不必等下一次拉取）；丢一批成功信号无妨，只影响分数的增长速度。

package service

import (
	"context"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"

	plog "github.com/aegispanel/aegis/internal/pkg/log"
)

const (
	// domainScoreKeyPrefix 是每个域名分数 hash 的 Redis 键前缀。
	domainScoreKeyPrefix = "subdom:score:"
	// domainScoreFresh 是细分级（省、省|运营商）偏移的新鲜度：最后写入超过这么久，读与写都把这一级偏移当 0。
	// 全国一级不受它约束。
	domainScoreFresh = 24 * time.Hour
	// domainScoreHalfLife 是半衰期：每过这么久，全国分与 1 的差距、细分级偏移与 0 的差距各减半。
	domainScoreHalfLife = 6 * time.Hour
	// domainScoreMin 是分数下限（任何域名都不会被压成 0）。
	domainScoreMin = 0.1
	// domainScoreMax 是分数上限（防止单个域名独占流量）。
	domainScoreMax = 3.0
	// domainOffsetMin、domainOffsetMax 是细分级偏移的界：单独一级就能把读分从任一端推到另一端，读分最后再夹进 [0.1, 3]。
	domainOffsetMin = domainScoreMin - domainScoreMax
	domainOffsetMax = domainScoreMax - domainScoreMin
	// scorePullSuccess 是一次经该域名的正常订阅拉取加的分。
	scorePullSuccess = 0.05
	// scoreProbeSuccess 是探测页报告一次「连得上」加的分（要满足多人规则才生效，见 domain_probe_scores.go）。
	scoreProbeSuccess = 0.1
	// scoreProbeFailure 是探测页报告一次「连不上」扣的分（要满足多人规则才生效，见 domain_probe_scores.go）。
	scoreProbeFailure = -0.3
	// domainScoreCredWindow 内同一凭据对同一域名只记一次成功，一个人高频拉取刷不动分数。
	domainScoreCredWindow = time.Hour
	// domainScoreCredMax 是去重表的条目上限，超了就丢弃新信号，防内存无界。
	domainScoreCredMax = 50000
	// domainScoreFlushEvery 是成功信号的攒批间隔。
	domainScoreFlushEvery = 10 * time.Second
	// domainScoreTTL 是分数 hash 的过期时间（长期没人用的域名，分数自然归零值）。
	domainScoreTTL = 7 * 24 * time.Hour
	// domainScoreTimeout 是单次 Redis 读写的超时。
	domainScoreTimeout = 2 * time.Second
	// domainScoreNeutral 是中性分数（没有记录、不可用时的缺省值）。
	domainScoreNeutral = 1.0
	// domainOffsetNeutral 是细分级的中性偏移（没有记录、已不新鲜时的缺省值）。
	domainOffsetNeutral = 0.0
)

// domainScoreKeyFor 返回某域名分数 hash 的 Redis 键。
func domainScoreKeyFor(baseURL string) string { return domainScoreKeyPrefix + baseURL }

// luaApplyDelta 定义 Lua 函数 apply_delta(key, field, delta, now, cfg)，cfg 是各脚本从 ARGV 组好的参数表
// {hl, lo, hi, olo, ohi, fresh}（半衰期、全国分的界、偏移的界、细分级新鲜度，单位毫秒）。
// 全国一级（field "*"）是绝对分：读旧值 "<score>|<毫秒>"，能解析就先向 1 衰减到 now，再加 delta、夹到 [lo, hi]；
// 解析不了按 1 起步。细分级是偏移：向 0 衰减、夹到 [olo, ohi]；解析不了或超过 fresh 没写过按 0 起步（与读分口径一致，
// 过期的旧偏移不会被一次新信号「复活」）。写回时带上 now。
const luaApplyDelta = `
local function apply_delta(key, field, delta, now, cfg)
  local offset = field ~= '*'
  local neutral, lo, hi = 1, cfg.lo, cfg.hi
  if offset then neutral, lo, hi = 0, cfg.olo, cfg.ohi end
  local s = neutral
  local old = redis.call('HGET', key, field)
  if old then
    local sep = string.find(old, '|', 1, true)
    if sep then
      local sc = tonumber(string.sub(old, 1, sep - 1))
      local ts = tonumber(string.sub(old, sep + 1))
      if sc and ts and not (offset and now - ts > cfg.fresh) then
        local age = math.max(now - ts, 0)
        s = neutral + (sc - neutral) * math.pow(0.5, age / cfg.hl)
      end
    end
  end
  s = math.min(math.max(s + delta, lo), hi)
  redis.call('HSET', key, field, string.format('%.4f|%d', s, now))
end
`

// scoreBucket 是攒批成功信号的键：域名 × 地区。
type scoreBucket struct {
	BaseURL string
	Region  domainRegion
}

// domainScores 是订阅域名分数的 Redis 存取；零值不可用，经 newDomainScores 构造。
// rdb 为 nil（或接收者为 nil）时所有方法按「Redis 不可用」处理：Read 回 (nil, false)、Apply 回 nil、NoteSuccess 丢弃。
type domainScores struct {
	rdb *redis.Client
	// mu 保护 pending、lastFlush 与 credSeen。
	mu sync.Mutex
	// pending 是还没批量写入的成功信号（域名 × 地区 → 攒下的加量）。
	pending map[scoreBucket]float64
	// lastFlush 是上一批的起点：第一次只开始计时，攒满 domainScoreFlushEvery 才写。
	lastFlush time.Time
	// flushing 标记后台写是否在进行，同时保证同一时间只有一批。
	flushing atomic.Bool
	// credSeen 是凭据去重表（凭据|域名 → 上次记成功的时刻）。
	credSeen map[string]time.Time
}

// newDomainScores 构造分数存取；rdb 为 nil 时返回的对象按「Redis 不可用」处理。
func newDomainScores(rdb *redis.Client) *domainScores {
	return &domainScores{rdb: rdb, pending: make(map[scoreBucket]float64), credSeen: make(map[string]time.Time)}
}

// UseDomainScores 为订阅模块注入 Redis 客户端，启用域名分数。只在启动装配时调用（不并发安全）。
func (s *Service) UseDomainScores(rdb *redis.Client) { s.scores = newDomainScores(rdb) }

// decayScore 把 at 时刻的分数衰减到 now：每过 domainScoreHalfLife，与 1 的差距减半；
// at 晚于 now（时钟回拨）按 0 处理，即返回原值。
func decayScore(score float64, at, now time.Time) float64 {
	age := now.Sub(at)
	if age < 0 {
		age = 0
	}
	return 1 + (score-1)*math.Pow(0.5, age.Seconds()/domainScoreHalfLife.Seconds())
}

// decayOffset 把 at 时刻的细分级偏移衰减到 now：每过 domainScoreHalfLife，与 0 的差距减半；at 晚于 now 按 0 处理。
func decayOffset(offset float64, at, now time.Time) float64 {
	age := now.Sub(at)
	if age < 0 {
		age = 0
	}
	return offset * math.Pow(0.5, age.Seconds()/domainScoreHalfLife.Seconds())
}

// clampOffset 把细分级偏移夹进 [domainOffsetMin, domainOffsetMax]；NaN 当 0。
func clampOffset(v float64) float64 {
	if math.IsNaN(v) {
		return domainOffsetNeutral
	}
	return math.Min(math.Max(v, domainOffsetMin), domainOffsetMax)
}

// clampScore 把分数夹进 [domainScoreMin, domainScoreMax]；NaN 当中性分 1。
func clampScore(v float64) float64 {
	if math.IsNaN(v) {
		return domainScoreNeutral
	}
	if v < domainScoreMin {
		return domainScoreMin
	}
	if v > domainScoreMax {
		return domainScoreMax
	}
	return v
}

// scoreOf 取某域名的分数；map 里没有回中性分，有则再夹一次。
func scoreOf(scores map[string]float64, baseURL string) float64 {
	if v, ok := scores[baseURL]; ok {
		return clampScore(v)
	}
	return domainScoreNeutral
}

// parseScore 解析存储格式 "<score 四位小数>|<unix 毫秒>"；任何一步失败 ok=false。
func parseScore(v string) (score float64, at time.Time, ok bool) {
	parts := strings.Split(v, "|")
	if len(parts) != 2 {
		return 0, time.Time{}, false
	}
	score, err := strconv.ParseFloat(parts[0], 64)
	if err != nil {
		return 0, time.Time{}, false
	}
	ms, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return 0, time.Time{}, false
	}
	return score, time.UnixMilli(ms), true
}

// Read 批量读分数，返回 base_url → 读分（map 含全部 baseURLs，没有记录的当 1）。
// 读分 = clamp(全国分 + 省偏移 + 省|运营商偏移)：全国分没有记录当 1、不看新鲜度，向 1 衰减；
// region 决定读哪几级偏移（零值只读全国），偏移超过 domainScoreFresh 没写或解析不了当 0，向 0 衰减。
// 第二个返回值是 Redis 是否可用：不可用（没注入、读失败）时调用方按基础权重选域名。
// baseURLs 为空不碰 Redis，也算可用。
func (d *domainScores) Read(ctx context.Context, baseURLs []string, region domainRegion, now time.Time) (map[string]float64, bool) {
	if d == nil || d.rdb == nil || len(baseURLs) == 0 {
		return nil, d != nil && d.rdb != nil
	}
	// 低流量时攒下的成功信号不必等下一次拉取，有人生成链接或后台查看分数时也会顺带冲刷；
	// 仍可能滞后到下一次读或拉取，丢一批只影响增速
	d.mu.Lock()
	d.maybeFlushLocked(ctx, now)
	d.mu.Unlock()
	fields := region.fields()
	cctx, cancel := context.WithTimeout(ctx, domainScoreTimeout)
	defer cancel()
	pipe := d.rdb.Pipeline()
	cmds := make([]*redis.SliceCmd, len(baseURLs))
	for i, u := range baseURLs {
		cmds[i] = pipe.HMGet(cctx, domainScoreKeyFor(u), fields...)
	}
	if _, err := pipe.Exec(cctx); err != nil {
		// 只带 base_url（非敏感），不带别的
		plog.From(ctx).Warn("订阅域名分数读取失败，按基础权重", zap.Strings("base_urls", baseURLs))
		return nil, false
	}
	out := make(map[string]float64, len(baseURLs))
	for i, u := range baseURLs {
		out[u] = combineLevels(fields, cmds[i].Val(), now)
	}
	return out, true
}

// combineLevels 把一个域名 HMGET 回来的各级值合成读分；vals 与 fields 一一对应（fields[0] 是全国 "*"）。
func combineLevels(fields []string, vals []any, now time.Time) float64 {
	raw := func(j int) (float64, time.Time, bool) {
		if j >= len(vals) {
			return 0, time.Time{}, false
		}
		v, ok := vals[j].(string)
		if !ok || v == "" {
			return 0, time.Time{}, false
		}
		return parseScore(v)
	}
	total := domainScoreNeutral
	if score, at, ok := raw(0); ok {
		total = clampScore(decayScore(score, at, now))
	}
	for j := 1; j < len(fields); j++ {
		off, at, ok := raw(j)
		if !ok || now.Sub(at) > domainScoreFresh {
			continue
		}
		total += clampOffset(decayOffset(off, at, now))
	}
	return clampScore(total)
}

// applyScoresScript 原子地把一批加量套到多个域名分数 hash 上：
// KEYS[i] = 某域名的 hash 键；ARGV[1] = now 毫秒；ARGV[2] = 半衰期毫秒；ARGV[3]、ARGV[4] = 全国分下限、上限；
// ARGV[5] = 每个 key 的 TTL 毫秒；ARGV[6]、ARGV[7] = 偏移下限、上限；ARGV[8] = 细分级新鲜度毫秒；
// 随后每个 key 一段：field 个数 n，再 n 组 (field, delta)。
var applyScoresScript = redis.NewScript(luaApplyDelta + `
local now = tonumber(ARGV[1])
local cfg = {hl = tonumber(ARGV[2]), lo = tonumber(ARGV[3]), hi = tonumber(ARGV[4]),
  olo = tonumber(ARGV[6]), ohi = tonumber(ARGV[7]), fresh = tonumber(ARGV[8])}
local p = 9
for k = 1, #KEYS do
  local n = tonumber(ARGV[p])
  p = p + 1
  for j = 1, n do
    apply_delta(KEYS[k], ARGV[p], tonumber(ARGV[p + 1]), now, cfg)
    p = p + 2
  end
  redis.call('PEXPIRE', KEYS[k], ARGV[5])
end
return 1
`)

// Apply 把一批加减量原子地套到分数上（base 与 field 均按字典序排好，保证同一批的参数顺序稳定）。
// deltas 为 base_url → field → 加减量：全国 "*" 加在绝对分上，省、省|运营商加在各自的偏移上。没注入 Redis 或 deltas 为空时什么都不做。
func (d *domainScores) Apply(ctx context.Context, deltas map[string]map[string]float64, now time.Time) error {
	if d == nil || d.rdb == nil || len(deltas) == 0 {
		return nil
	}
	bases := make([]string, 0, len(deltas))
	for base, fm := range deltas {
		if len(fm) == 0 {
			continue
		}
		bases = append(bases, base)
	}
	if len(bases) == 0 {
		return nil
	}
	sort.Strings(bases)
	keys := make([]string, len(bases))
	for i, base := range bases {
		keys[i] = domainScoreKeyFor(base)
	}
	args := []any{
		now.UnixMilli(),
		domainScoreHalfLife.Milliseconds(),
		domainScoreMin,
		domainScoreMax,
		domainScoreTTL.Milliseconds(),
		domainOffsetMin,
		domainOffsetMax,
		domainScoreFresh.Milliseconds(),
	}
	for _, base := range bases {
		fm := deltas[base]
		fields := make([]string, 0, len(fm))
		for f := range fm {
			fields = append(fields, f)
		}
		sort.Strings(fields)
		args = append(args, len(fields))
		for _, f := range fields {
			args = append(args, f, fm[f])
		}
	}
	cctx, cancel := context.WithTimeout(ctx, domainScoreTimeout)
	defer cancel()
	return applyScoresScript.Run(cctx, d.rdb, keys, args...).Err()
}

// NoteSuccess 记一次成功信号（credKey 是凭据标识）：加量先攒在进程内，到点由后台一次写入，
// 避免每次拉取都多一次 Redis 往返。同一凭据对同一域名在 domainScoreCredWindow 里只记一次，
// 一个人高频拉取刷不动分数；去重表超过 domainScoreCredMax 条后丢弃新信号。每个（域名, 地区）攒的总量封顶
// domainScoreMax - domainScoreMin（再多也只会被夹回上限）。丢一批成功信号无妨，只影响分数的增长速度。
// locate 为 nil 时只记全国一级；非 nil 时在去重通过之后、锁外调用一次查归属地：同一凭据对同一域名每小时只查一次，
// 不在每次拉取上查；锁外查是为了不让一次归属地查询（可能读文件）挡住别的拉取记分。
// Redis 不可用或 baseURL 为空时丢弃。
func (d *domainScores) NoteSuccess(ctx context.Context, baseURL, credKey string, locate func() domainRegion, now time.Time) {
	if d == nil || d.rdb == nil || baseURL == "" {
		return
	}
	d.mu.Lock()
	key := credKey + "|" + baseURL
	t, seen := d.credSeen[key]
	accept := false
	if !seen || now.Sub(t) >= domainScoreCredWindow {
		if len(d.credSeen) < domainScoreCredMax || seen {
			d.credSeen[key] = now
			accept = true
		}
	}
	if !accept {
		d.maybeFlushLocked(ctx, now)
		d.mu.Unlock()
		return
	}
	d.mu.Unlock()

	var region domainRegion
	if locate != nil {
		region = locate()
	}

	d.mu.Lock()
	b := scoreBucket{BaseURL: baseURL, Region: region}
	d.pending[b] += scorePullSuccess
	if most := domainScoreMax - domainScoreMin; d.pending[b] > most {
		d.pending[b] = most
	}
	d.maybeFlushLocked(ctx, now)
	d.mu.Unlock()
}

// expandBuckets 把攒批桶展开成 Apply 要的 base → field → delta：一个桶的加量写到它的每一级上
// （全国加绝对分、省与省|运营商加偏移）；同一域名不同地区的信号在全国一级 "*"、同省不同运营商的在省一级相加。
// 每级加量封顶 domainScoreMax - domainScoreMin（与 domainOffsetMax 相同，再多也只会被夹回上限）。
func expandBuckets(batch map[scoreBucket]float64) map[string]map[string]float64 {
	out := make(map[string]map[string]float64)
	most := domainScoreMax - domainScoreMin
	for bucket, delta := range batch {
		if out[bucket.BaseURL] == nil {
			out[bucket.BaseURL] = make(map[string]float64)
		}
		for _, field := range bucket.Region.fields() {
			out[bucket.BaseURL][field] += delta
		}
	}
	for _, fm := range out {
		for f, v := range fm {
			if v > most {
				fm[f] = most
			}
		}
	}
	return out
}

// maybeFlushLocked 到点就把攒下的成功信号交给后台写（调用方持有 mu）：
// lastFlush 为零只开始计时；不到 domainScoreFlushEvery 或后台写正在进行就不冲。
// 取走 pending 时顺便清掉 credSeen 里过了去重窗口的条目；pending 为空不起 goroutine。
func (d *domainScores) maybeFlushLocked(ctx context.Context, now time.Time) {
	if d.lastFlush.IsZero() {
		// 第一次只开始计时，让这批攒满一个间隔再写
		d.lastFlush = now
		return
	}
	if now.Sub(d.lastFlush) < domainScoreFlushEvery || !d.flushing.CompareAndSwap(false, true) {
		return
	}
	batch := d.pending
	d.pending = make(map[scoreBucket]float64)
	d.lastFlush = now
	for k, t := range d.credSeen {
		if now.Sub(t) >= domainScoreCredWindow {
			delete(d.credSeen, k)
		}
	}
	if len(batch) == 0 {
		d.flushing.Store(false)
		return
	}
	go func() {
		defer d.flushing.Store(false)
		// 背景写与调用方的 ctx（可能随请求取消）脱钩，只留超时
		fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), domainScoreTimeout)
		defer cancel()
		if err := d.Apply(fctx, expandBuckets(batch), now); err != nil {
			plog.From(ctx).Warn("订阅域名成功分数批量写入失败（丢一批只影响分数增速）", zap.Error(err))
		}
	}()
}
