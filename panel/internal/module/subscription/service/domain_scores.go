// 订阅域名的分数（Redis，多进程共享）：有效权重 = 基础权重 × 分数，按分数放大或缩小一个域名的被选中概率。
// 分数只由「经令牌主人所在组的已登记域名到达的成功拉取」加分（每次 +0.05），随时间按 6 小时半衰期向 1 衰减；
// 连不上的域名拿不到加分，相对权重随衰减自然变低。夹在 [domainScoreMin, domainScoreMax]：
// 下限 0.1 不让任何域名被压成 0；上限 3 防止单个域名独占流量。
// 分数值带写入时刻存成 hash（field = base_url，value = "<score 四位小数>|<unix 毫秒>"），读时现算衰减。
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
	// domainScoreKey 是分数 hash 的 Redis 键。
	domainScoreKey = "subdom:score"
	// domainScoreHalfLife 是分数向 1 衰减的半衰期：每过这么久，分数与 1 的差距减半。
	domainScoreHalfLife = 6 * time.Hour
	// domainScoreMin 是分数下限（任何域名都不会被压成 0）。
	domainScoreMin = 0.1
	// domainScoreMax 是分数上限（防止单个域名独占流量）。
	domainScoreMax = 3.0
	// scorePullSuccess 是一次经该域名的正常订阅拉取加的分。
	scorePullSuccess = 0.05
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
)

// domainScores 是订阅域名分数的 Redis 存取；零值不可用，经 newDomainScores 构造。
// rdb 为 nil（或接收者为 nil）时所有方法按「Redis 不可用」处理：Read 回 (nil, false)、Apply 回 nil、NoteSuccess 丢弃。
type domainScores struct {
	rdb *redis.Client
	// mu 保护 pending、lastFlush 与 credSeen。
	mu sync.Mutex
	// pending 是还没批量写入的成功信号（base_url → 攒下的加量）。
	pending map[string]float64
	// lastFlush 是上一批的起点：第一次只开始计时，攒满 domainScoreFlushEvery 才写。
	lastFlush time.Time
	// flushing 标记后台写是否在进行，同时保证同一时间只有一批。
	flushing atomic.Bool
	// credSeen 是凭据去重表（凭据|域名 → 上次记成功的时刻）。
	credSeen map[string]time.Time
}

// newDomainScores 构造分数存取；rdb 为 nil 时返回的对象按「Redis 不可用」处理。
func newDomainScores(rdb *redis.Client) *domainScores {
	return &domainScores{rdb: rdb, pending: make(map[string]float64), credSeen: make(map[string]time.Time)}
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

// Read 批量读分数，返回 base_url → 衰减并夹紧后的分数（map 含全部 baseURLs，没有记录的当 1）。
// 第二个返回值是 Redis 是否可用：不可用（没注入、读失败）时调用方按基础权重选域名。
// baseURLs 为空不碰 Redis，也算可用。
func (d *domainScores) Read(ctx context.Context, baseURLs []string, now time.Time) (map[string]float64, bool) {
	if d == nil || d.rdb == nil || len(baseURLs) == 0 {
		return nil, d != nil && d.rdb != nil
	}
	// 低流量时攒下的成功信号不必等下一次拉取，有人生成链接或后台查看分数时也会顺带冲刷；
	// 仍可能滞后到下一次读或拉取，丢一批只影响增速
	d.mu.Lock()
	d.maybeFlushLocked(ctx, now)
	d.mu.Unlock()
	cctx, cancel := context.WithTimeout(ctx, domainScoreTimeout)
	defer cancel()
	vals, err := d.rdb.HMGet(cctx, domainScoreKey, baseURLs...).Result()
	if err != nil {
		// 只带 base_url（非敏感），不带别的
		plog.From(ctx).Warn("订阅域名分数读取失败，按基础权重", zap.Strings("base_urls", baseURLs))
		return nil, false
	}
	out := make(map[string]float64, len(baseURLs))
	for i, u := range baseURLs {
		out[u] = domainScoreNeutral
		if i >= len(vals) {
			continue
		}
		raw, ok := vals[i].(string)
		if !ok {
			continue
		}
		score, at, ok := parseScore(raw)
		if !ok {
			continue
		}
		out[u] = clampScore(decayScore(score, at, now))
	}
	return out, true
}

// applyScoresScript 原子地把一批加量套到分数 hash 上（KEYS[1]）：
// ARGV[1] = now 的 unix 毫秒；ARGV[2] = 半衰期毫秒；ARGV[3] = 分数下限；ARGV[4] = 分数上限；
// ARGV[5] = hash 的 TTL 毫秒；ARGV[6] 起（field, delta）两两一组。
// 每组：读旧值 "<score>|<毫秒>"，能解析就先把旧分数按半衰期衰减到 now，再加减量，夹到 [下限, 上限] 后写回；
// 解析不了旧值（没记录、格式变了）按中性分 1 起步。全部写完重设 hash 的 TTL，返回 1。
var applyScoresScript = redis.NewScript(`
local now = tonumber(ARGV[1])
local hl = tonumber(ARGV[2])
local lo = tonumber(ARGV[3])
local hi = tonumber(ARGV[4])
for i = 6, #ARGV, 2 do
  local field = ARGV[i]
  local delta = tonumber(ARGV[i + 1])
  local s = 1
  local old = redis.call('HGET', KEYS[1], field)
  if old then
    local sep = string.find(old, '|', 1, true)
    if sep then
      local sc = tonumber(string.sub(old, 1, sep - 1))
      local ts = tonumber(string.sub(old, sep + 1))
      if sc and ts then
        local age = math.max(now - ts, 0)
        s = 1 + (sc - 1) * math.pow(0.5, age / hl)
      end
    end
  end
  s = math.min(math.max(s + delta, lo), hi)
  redis.call('HSET', KEYS[1], field, string.format('%.4f|%d', s, now))
end
redis.call('PEXPIRE', KEYS[1], ARGV[5])
return 1
`)

// Apply 把一批加减量原子地套到分数上（field 按字典序排好，保证同一批的参数顺序稳定）。
// 没注入 Redis 或 deltas 为空时什么都不做。
func (d *domainScores) Apply(ctx context.Context, deltas map[string]float64, now time.Time) error {
	if d == nil || d.rdb == nil || len(deltas) == 0 {
		return nil
	}
	fields := make([]string, 0, len(deltas))
	for f := range deltas {
		fields = append(fields, f)
	}
	sort.Strings(fields)
	args := []any{
		now.UnixMilli(),
		domainScoreHalfLife.Milliseconds(),
		domainScoreMin,
		domainScoreMax,
		domainScoreTTL.Milliseconds(),
	}
	for _, f := range fields {
		args = append(args, f, deltas[f])
	}
	cctx, cancel := context.WithTimeout(ctx, domainScoreTimeout)
	defer cancel()
	return applyScoresScript.Run(cctx, d.rdb, []string{domainScoreKey}, args...).Err()
}

// NoteSuccess 记一次成功信号（credKey 是凭据标识）：加量先攒在进程内，到点由后台一次写入，
// 避免每次拉取都多一次 Redis 往返。同一凭据（credKey）对同一域名在 domainScoreCredWindow 里只记一次，
// 一个人高频拉取刷不动分数；去重表超过 domainScoreCredMax 条后丢弃新信号。单个域名攒的总量封顶
// domainScoreMax - domainScoreMin（再多也只会被夹回上限）。丢一批成功信号无妨，只影响分数的增长速度。
// Redis 不可用或 baseURL 为空时丢弃。
func (d *domainScores) NoteSuccess(ctx context.Context, baseURL, credKey string, now time.Time) {
	if d == nil || d.rdb == nil || baseURL == "" {
		return
	}
	d.mu.Lock()
	key := credKey + "|" + baseURL
	t, seen := d.credSeen[key]
	if !seen || now.Sub(t) >= domainScoreCredWindow {
		if len(d.credSeen) < domainScoreCredMax || seen {
			d.credSeen[key] = now
			d.pending[baseURL] += scorePullSuccess
			if most := domainScoreMax - domainScoreMin; d.pending[baseURL] > most {
				d.pending[baseURL] = most
			}
		}
	}
	d.maybeFlushLocked(ctx, now)
	d.mu.Unlock()
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
	d.pending = make(map[string]float64)
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
		if err := d.Apply(fctx, batch, now); err != nil {
			plog.From(ctx).Warn("订阅域名成功分数批量写入失败（丢一批只影响分数增速）", zap.Error(err))
		}
	}()
}
