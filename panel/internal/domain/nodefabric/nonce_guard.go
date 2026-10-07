package nodefabric

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"log/slog"
	"sync"
	"time"

	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// 签名请求的防重放（nonce 认领）。
//
// 原先每个签名请求往 node_request_nonces 插一行：r3 实测它是库耗时第一的语句
// （30 分钟 5.6 万次、WAL 40 MB），外加每分钟一次的批量清理。现在主路径是 Valkey 的
// SET key NX PX（一次内存往返，到期自动消失），PG 那张表与清理任务保留作回落：
// Valkey 出错时改在 PG 认领，既不放行重放，也不拒绝全部节点。
//
// 两个存储之间的缝由两层补上：
//   - 进程内近期集：本进程认领过的 nonce（不论落在哪个存储）在保留期内都记着，
//     Valkey 与 PG 切换的那一刻，同一请求换个存储重放也会被认出来。它是第一道认领，
//     原子地「查并占」，并发的两份同一请求只有一份能过。
//   - PG 活跃窗口：PG 里可能还有未过期的 nonce（本进程或别的副本、上一次启动回落过）
//     时，Valkey 认领成功后再在 PG 认领一次，PG 里的旧记录照样拦得住重放。窗口在启动时
//     从表里读出（PrimeNonceFallback），之后每次回落都向后延。
//
// 剩下的缝写在报告里：Valkey 挂着、进程又刚重启时，重启前只记在 Valkey 里的 nonce
// 本进程不知道。要利用它，得同时满足这两件事，并且手里有一份 10 分钟内截获的签名请求。

// NonceStore 是 nonce 认领的主存储（aegis-node 里是 Valkey）。
type NonceStore interface {
	// ClaimNonce 原子地占用 key，ttl 后自动释放。claimed=false 表示 key 已被占用（重放）。
	ClaimNonce(ctx context.Context, key string, ttl time.Duration) (claimed bool, err error)
}

const (
	// signedNonceRetention 是一条 nonce 至少要记多久：签名时间戳只接受 ±5 分钟，一个
	// 认领时刻为 t 的请求，时间戳不晚于 t+5 分钟，最晚到 t+10 分钟还能被接受；再留 1 分钟
	// 余量，与 PG 回落的「入库时间 + 11 分钟」一致。
	signedNonceRetention = 2*SignedRequestAcceptanceWindow + time.Minute
	// nonceStoreTimeout 是一次 Valkey 认领的上限。超时按出错处理，回落 PG。
	nonceStoreTimeout = 250 * time.Millisecond
	// nonceStoreCooldown 是 Valkey 出错后直接走 PG 的时长，免得每个请求都先等一次超时。
	nonceStoreCooldown = 5 * time.Second
	// recentNonceMax 是进程内近期集的上限（300 节点每分钟约 2000 个签名请求，保留期内
	// 约 2 万条）。超出时挤掉最早的：近期集只是补缝的一层，主判定仍在存储里。
	recentNonceMax = 200_000
	// nonceKeyPrefix 是 Valkey 里 nonce 键的前缀。
	nonceKeyPrefix = "aegis:node-nonce:"
)

// errNonceReplayed 是 nonce 已被认领：对外与其他身份失败同一个 401。
var errNonceReplayed = httpx.New(httpx.CodeUnauthorized, "节点身份校验失败")

type recentNonce struct {
	key     string
	expires time.Time
}

type nonceGuard struct {
	store NonceStore
	log   *slog.Logger
	now   func() time.Time

	mu        sync.Mutex
	recent    map[string]time.Time
	order     []recentNonce // 按认领先后；保留期固定，所以也按到期先后
	downUntil time.Time
	// pgActiveUntil 之前 PG 里可能还有未过期的 nonce。
	pgActiveUntil time.Time
	degraded      bool
}

func newNonceGuard(store NonceStore, log *slog.Logger, now func() time.Time) *nonceGuard {
	if now == nil {
		now = time.Now
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &nonceGuard{store: store, log: log, now: now, recent: make(map[string]time.Time)}
}

// SetNonceStore 让签名请求的 nonce 先在 store（Valkey）认领。不设时照旧只用 PG。
// 装配时调用一次，之后调 PrimeNonceFallback 读出 PG 的活跃窗口。
func (s *Service) SetNonceStore(store NonceStore, log *slog.Logger) {
	s.nonces = newNonceGuard(store, log, nil)
}

// PrimeNonceFallback 读出 PG 里未过期 nonce 的最晚到期时刻：在那之前 Valkey 认领成功
// 也要再在 PG 认领一次（上一次运行回落过，那些 nonce 只在 PG 里）。读不出来就按
// 「整个保留期都可能有」处理，宁可多写一阵 PG。
func (s *Service) PrimeNonceFallback(ctx context.Context, tenantID string) {
	g := s.nonces
	if g == nil {
		return
	}
	var until *time.Time
	err := s.pool.QueryRowScoped(ctx, db.Scope{TenantID: tenantID},
		`SELECT max(expires_at) FROM node_request_nonces WHERE tenant_id = $1 AND expires_at > now()`,
		[]any{tenantID}, &until)
	g.mu.Lock()
	defer g.mu.Unlock()
	switch {
	case err != nil:
		g.pgActiveUntil = g.now().Add(signedNonceRetention)
		g.log.Warn("读取 PG 中未过期的节点 nonce 失败，保留期内双写", "error", err.Error())
	case until != nil:
		g.pgActiveUntil = *until
	}
}

// claimRecent 在进程内近期集里原子地「查并占」key。已在集里返回 false。
func (g *nonceGuard) claimRecent(key string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.now()
	for len(g.order) > 0 && (!now.Before(g.order[0].expires) || len(g.order) >= recentNonceMax) {
		head := g.order[0]
		if at, ok := g.recent[head.key]; ok && at.Equal(head.expires) {
			delete(g.recent, head.key)
		}
		g.order[0] = recentNonce{}
		g.order = g.order[1:]
	}
	if at, ok := g.recent[key]; ok && now.Before(at) {
		return false
	}
	expires := now.Add(signedNonceRetention)
	g.recent[key] = expires
	g.order = append(g.order, recentNonce{key: key, expires: expires})
	return true
}

func (g *nonceGuard) storeUsable() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.store != nil && !g.now().Before(g.downUntil)
}

func (g *nonceGuard) pgActive() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.now().Before(g.pgActiveUntil)
}

// storeFailed 记下一次 Valkey 失败：冷却期内直接走 PG，PG 活跃窗口向后延。
func (g *nonceGuard) storeFailed(err error) {
	g.mu.Lock()
	now := g.now()
	g.downUntil = now.Add(nonceStoreCooldown)
	first := !g.degraded
	g.degraded = true
	g.mu.Unlock()
	if first {
		g.log.Warn("Valkey 认领节点 nonce 失败，回落到 PG", "error", err.Error())
	}
}

func (g *nonceGuard) storeRecovered() {
	g.mu.Lock()
	was := g.degraded
	g.degraded = false
	g.mu.Unlock()
	if was {
		g.log.Info("Valkey 认领节点 nonce 已恢复")
	}
}

func (g *nonceGuard) usedDatabase() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if until := g.now().Add(signedNonceRetention); until.After(g.pgActiveUntil) {
		g.pgActiveUntil = until
	}
}

// ClaimSignedRequestNonce 认领一个签名请求的 nonce。已被认领（重放）回 401；后端都不可用
// 回 ErrNodeAuthUnavailable（503）。
//
// epochKnown 为真时 epoch 是在 PG 认领那条语句里顺手读出的当前下发纪元，调用方可以
// 直接拿它复核缓存的身份；走 Valkey 时没有纪元，调用方另取（或并进后续查询）。
//
// 处理失败的请求不释放 nonce：重试必须换一个新的。
func (s *Service) ClaimSignedRequestNonce(ctx context.Context, tenantID, nodeID string, nonce, fingerprint []byte,
	requestTS time.Time) (epoch int64, epochKnown bool, err error) {
	if len(nonce) != 16 || len(fingerprint) != sha256.Size {
		return 0, false, httpx.New(httpx.CodeUnauthorized, "节点身份校验失败")
	}
	g := s.nonces
	if g == nil {
		epoch, err := s.claimNonceInDatabase(ctx, tenantID, nodeID, nonce, fingerprint, requestTS)
		return epoch, err == nil, err
	}
	key := tenantID + ":" + nodeID + ":" + base64.RawURLEncoding.EncodeToString(nonce)
	if !g.claimRecent(key) {
		return 0, false, errNonceReplayed
	}
	fellBack := true
	if g.storeUsable() {
		storeCtx, cancel := context.WithTimeout(ctx, nonceStoreTimeout)
		claimed, storeErr := g.store.ClaimNonce(storeCtx, nonceKeyPrefix+key, signedNonceRetention)
		cancel()
		switch {
		case storeErr == nil && !claimed:
			g.storeRecovered()
			return 0, false, errNonceReplayed
		case storeErr == nil:
			g.storeRecovered()
			if !g.pgActive() {
				return 0, false, nil
			}
			// PG 里可能还有回落期间的旧 nonce：再认领一次，重放照样撞主键。
			// 这只是补查，不算回落，不延长 PG 活跃窗口。
			fellBack = false
		case ctx.Err() != nil:
			return 0, false, ctx.Err()
		default:
			g.storeFailed(storeErr)
		}
	}
	epoch, err = s.claimNonceInDatabase(ctx, tenantID, nodeID, nonce, fingerprint, requestTS)
	if err != nil {
		return 0, false, err
	}
	if fellBack {
		g.usedDatabase()
	}
	return epoch, true, nil
}
