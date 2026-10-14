package service

import (
	"context"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/aegispanel/aegis/internal/module/subscription/model"
	"github.com/aegispanel/aegis/internal/module/subscription/render"
	"github.com/aegispanel/aegis/internal/pkg/crypto"
	plog "github.com/aegispanel/aegis/internal/pkg/log"
	"github.com/aegispanel/aegis/internal/pkg/reqmeta"
)

// 拉取日志的结果（与迁移 00017 的 CHECK 一致）。
const (
	FetchOK          = "ok"
	FetchExpired     = "expired"
	FetchRateLimited = "rate_limited"
	FetchError       = "error"
	FetchNotFound    = "not_found"
)

const (
	// FetchLogRetention 是拉取日志的保留期（库函数 purge_subscription_fetch_log 也守着这个下限）。
	FetchLogRetention = 31 * 24 * time.Hour

	// failureSampleWindow 与 failureSamplesPerWindow：未认证失败每分钟最多落 60 行，同一来源每分钟最多 1 行。
	// 后台仍能看到「有人在扫、从哪些来源扫」，写入量与扫描速度脱钩。
	failureSampleWindow     = time.Minute
	failureSamplesPerWindow = 60

	// fetchLogAAD 是封存拉取日志里 IP、UA 的附加数据：与审计、订阅凭据的密文隔开，互换密文解不开。
	fetchLogAAD = "sub-fetch-log"
	// maxUAFamily 是 ua_family 列的长度上限（迁移 CHECK）。
	maxUAFamily = 32
)

// fetchEntry 是一条要记的拉取。未认证失败的 credID、subID 为 nil。
type fetchEntry struct {
	credID    *uuid.UUID
	subID     *uuid.UUID
	result    string
	format    render.Format
	family    string
	nodeCount int
	bytesSent int
}

// with 返回改了结果与计数的副本。
func (e fetchEntry) with(result string, nodeCount, bytesSent int) fetchEntry {
	e.result, e.nodeCount, e.bytesSent = result, nodeCount, bytesSent
	return e
}

// fetchLogRow 把一条拉取转成日志行：来源 IP 与 UA 从 ctx 取，存 HMAC（关联用）与封存的原文（后台查看用）。
// 封存失败只少存密文，不让整条日志写不进去。
func (s *Service) fetchLogRow(ctx context.Context, d *Delivery, e fetchEntry) *model.FetchLog {
	meta := reqmeta.From(ctx)
	row := &model.FetchLog{
		CredentialID:   e.credID,
		SubscriptionID: e.subID,
		UAFamily:       cleanFamily(e.family),
		Result:         e.result,
		NodeCount:      e.nodeCount,
		BytesSent:      e.bytesSent,
	}
	if e.format != "" {
		f := string(e.format)
		row.Format = &f
	}
	row.IPHash, row.IPEnc = s.protect(d, meta.IP)
	row.UAHash, row.UAEnc = s.protect(d, meta.UserAgent)
	return row
}

// protect 返回一条来源信息的 HMAC 与封存密文；空串两样都不存。
func (s *Service) protect(d *Delivery, v string) (hash, sealed []byte) {
	if v == "" {
		return nil, nil
	}
	hash = d.Hasher.HMAC(crypto.InfoHMACSubAudit, []byte(v))
	if s.sealer != nil {
		if out, err := s.sealer.Seal([]byte(v), []byte(fetchLogAAD)); err == nil {
			sealed = out
		}
	}
	return hash, sealed
}

// cleanFamily 让客户端家族满足列约束（1–32 字）。
func cleanFamily(f string) string {
	if f == "" {
		return "unknown"
	}
	if utf8.RuneCountInString(f) > maxUAFamily {
		return string([]rune(f)[:maxUAFamily])
	}
	return f
}

// logFetch 尽力追加一条拉取日志（自己的语句，不在调用方事务里）；失败只记警告，不影响响应。
func (s *Service) logFetch(ctx context.Context, d *Delivery, e fetchEntry) {
	if err := s.repo.InsertFetchLog(ctx, s.fetchLogRow(ctx, d, e)); err != nil {
		plog.From(ctx).Warn("订阅拉取日志写入失败", zap.String("result", e.result), zap.Error(err))
	}
}

// recordFetch 在一个事务里记下一次成功（或过期提示）拉取：凭据的拉取次数与时间、拉取日志。
func (s *Service) recordFetch(ctx context.Context, d *Delivery, e fetchEntry) error {
	return s.tx.Do(ctx, func(ctx context.Context) error {
		if e.credID != nil {
			if err := s.repo.TouchCredential(ctx, *e.credID, s.clock()); err != nil {
				return subErr(err)
			}
		}
		return subErr(s.repo.InsertFetchLog(ctx, s.fetchLogRow(ctx, d, e)))
	})
}

// recordUnauthenticated 记一次未认证失败（前缀或令牌不对、凭据失效、订阅已停用、主人停用）。
// 这类请求几乎全是扫描器：按来源（IP 的 HMAC）采样落库，其余只计数；窗口切换时把上一窗口没落库的次数打一条汇总日志。
// 采样状态放在进程内：部署是单实例，采样只为削减扫描带来的写入，不需要跨进程精确，也不给扫描流量再加一次 Redis 往返。
func (s *Service) recordUnauthenticated(ctx context.Context, d *Delivery, family string) {
	source := ""
	if ip := reqmeta.From(ctx).IP; ip != "" {
		source = string(d.Hasher.HMAC(crypto.InfoHMACSubAudit, []byte(ip)))
	}
	admit, dropped := s.failures.admit(source)
	if dropped > 0 {
		plog.From(ctx).Warn("订阅未认证失败超出采样额度，上一窗口未落库", zap.Int64("dropped", dropped))
	}
	if admit {
		s.logFetch(ctx, d, fetchEntry{result: FetchNotFound, family: family})
	}
}

// PurgeFetchLogs 删一批超过保留期（31 天）的拉取日志（最多 limit 行），返回删除行数。
// 由保留期任务经 jobs.PurgeInBatches 分批调用。拉取日志只追加，删除只经库函数。
func (s *Service) PurgeFetchLogs(ctx context.Context, limit int) (int64, error) {
	n, err := s.repo.PurgeFetchLogs(ctx, s.clock().Add(-FetchLogRetention), limit)
	if err != nil {
		return 0, subErr(err)
	}
	return n, nil
}

// failureSampler 决定哪些未认证失败落库。窗口内记过的来源只存 HMAC，条目数不超过每窗口额度，窗口切换时整体清空。
type failureSampler struct {
	mu        sync.Mutex
	window    time.Duration
	perWindow int
	now       func() time.Time

	start    time.Time
	seen     map[string]struct{}
	admitted int
	dropped  int64
}

// newFailureSampler 构造采样器。
func newFailureSampler(window time.Duration, perWindow int) *failureSampler {
	return &failureSampler{window: window, perWindow: perWindow, now: time.Now,
		seen: make(map[string]struct{}, perWindow)}
}

// admit 返回这次是否落库，以及刚结束的窗口里没落库的次数（只在窗口切换时非零）。采样器为 nil 时一律落库。
func (f *failureSampler) admit(source string) (bool, int64) {
	if f == nil {
		return true, 0
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	var rolled int64
	now := f.now()
	if now.Before(f.start) || now.Sub(f.start) >= f.window {
		rolled = f.dropped
		f.start, f.admitted, f.dropped = now, 0, 0
		clear(f.seen)
	}
	if _, dup := f.seen[source]; dup || f.admitted >= f.perWindow {
		f.dropped++
		return false, rolled
	}
	f.seen[source] = struct{}{}
	f.admitted++
	return true, rolled
}
