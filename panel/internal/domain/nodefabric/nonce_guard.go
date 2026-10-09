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
// 一次认领的顺序：
//  1. 近期集（nonce_recent.go）原子「查并占」，本进程保留期内认领过的一律拒。
//  2. 存储状态机允许时在 Valkey 认领；Valkey 说已占用就拒。
//  3. Valkey 认领成功后，只有签名时间戳落在「补查界」之内的请求再在 PG 认领一次。
//  4. Valkey 不可用（冷却中、出错、超时）时直接在 PG 认领（回落）。
//
// 存储状态机（storeState）：
//
//	healthy ──Valkey 出错或超时──▶ down（冷却 nonceStoreCooldown，期间全走 PG）
//	down ──冷却到期，下一个请求──▶ probing（只这一个请求去试 Valkey，其余照走 PG）
//	probing ──成功──▶ healthy      probing ──失败──▶ down（重新冷却）
//	probing ──调用方取消──▶ down（冷却已到期，下一个请求接着试）
//
// 所以一次 Valkey 抖动只让「抖动时长 + 至多一个冷却」内的请求写 PG；恢复后回到只走
// Valkey。原先每次回落都把一个「PG 活跃窗口」推到 11 分钟后，窗口内每个请求都在 PG
// 补认领一次：一次 250ms 超时就换来约 11 分钟、近 20 万次插入（w12nonce 修掉）。
//
// 为什么恢复后不用逐个补查 PG：回落期间认领的 nonce 已经进了近期集，保留期和 PG 一样长，
// 同一进程的重放在第 1 步就被拦下。PG 里有、近期集里却没有的 nonce 只有两种来源，
// 都折算成「补查界」（签名时间戳上界：重放带的是同一个签名时间戳）：
//   - 上一次运行留下的行：启动时 PrimeNonceFallback 读出未过期行的最大 request_ts。
//   - 近期集到了上限、挤掉了 Valkey 没确认持有的条目：按它的认领时刻 + 5 分钟抬界
//     （recentSet.pruneLocked）。
//
// 剩下的缝写在报告里：
//   - Valkey 挂着、进程又刚重启时，重启前只记在 Valkey 里的 nonce 本进程不知道。
//   - Valkey 是 allkeys-lru、不持久化，键可能在 TTL 前被挤掉或随重启丢失。
//   - 多副本时，别的副本回落写进 PG 的 nonce 本进程不补查（aegis-node 定为单实例）。
//
// 要利用它们，都还得手里有一份 10 分钟内截获的签名请求。

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
	// nonceStoreCooldown 是 Valkey 出错后直接走 PG 的时长，到期只放一个请求去探测，
	// 所以缩短它不会让更多请求等超时，只让恢复更快。
	nonceStoreCooldown = time.Second
	// recentNonceMax 是进程内近期集的上限。按 2026-10 的签名请求量（每节点每分钟约 6 次、
	// 保留 11 分钟）约 3000 节点触顶；触顶挤掉最早的条目，挤掉的若是 Valkey 没确认持有的，
	// 抬 PG 补查界，防重放不因触顶变弱。一条约 56 字节，满额约 11MB。
	recentNonceMax = 200_000
	// nonceKeyPrefix 是 Valkey 里 nonce 键的前缀。
	nonceKeyPrefix = "aegis:node-nonce:"
	// nonceOutageLogEvery 是回落告警的最短间隔：Valkey 时好时坏时不刷屏。
	nonceOutageLogEvery = time.Minute
)

// errNonceReplayed 是 nonce 已被认领：对外与其他身份失败同一个 401。
var errNonceReplayed = httpx.New(httpx.CodeUnauthorized, "节点身份校验失败")

type storeState uint8

const (
	storeHealthy storeState = iota
	storeDown
	storeProbing
)

type nonceGuard struct {
	store  NonceStore
	log    *slog.Logger
	now    func() time.Time
	recent *recentSet

	mu        sync.Mutex
	state     storeState
	downUntil time.Time
	// 回落告警与恢复日志：outageWarned 为真时恢复要报一句，带这段时间在 PG 认领的次数。
	lastWarn       time.Time
	outageWarned   bool
	fallbackClaims int64
}

func newNonceGuard(store NonceStore, log *slog.Logger, now func() time.Time) *nonceGuard {
	if now == nil {
		now = time.Now
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &nonceGuard{store: store, log: log, now: now, recent: newRecentSet(recentNonceMax)}
}

// SetNonceStore 让签名请求的 nonce 先在 store（Valkey）认领。不设时照旧只用 PG。
// 装配时调用一次，之后调 PrimeNonceFallback 读出上一次运行留在 PG 的 nonce。
func (s *Service) SetNonceStore(store NonceStore, log *slog.Logger) {
	s.nonces = newNonceGuard(store, log, nil)
}

// PrimeNonceFallback 读出 PG 里未过期 nonce（节点与服务器两张表）的最大签名时间戳：
// 签名时间戳不晚于它的请求，Valkey 认领成功也要在 PG 再认领一次（上一次运行回落过，
// 那些 nonce 只在 PG 里）。读不出来就按「启动时刻 + 5 分钟」处理：启动前认领的请求，
// 签名时间戳不会晚于这个时刻。
func (s *Service) PrimeNonceFallback(ctx context.Context, tenantID string) {
	g := s.nonces
	if g == nil {
		return
	}
	var latest *time.Time
	err := s.pool.QueryRowScoped(ctx, db.Scope{TenantID: tenantID}, `
		SELECT GREATEST(
			(SELECT max(request_ts) FROM node_request_nonces WHERE tenant_id = $1 AND expires_at > now()),
			(SELECT max(request_ts) FROM server_request_nonces WHERE tenant_id = $1 AND expires_at > now()))`,
		[]any{tenantID}, &latest)
	switch {
	case err != nil:
		g.recent.raiseRecheck(g.now().Add(SignedRequestAcceptanceWindow))
		g.log.Warn("读取 PG 中未过期的签名请求 nonce 失败，启动后 5 分钟内的请求在 PG 补查",
			"error", err.Error())
	case latest != nil:
		// 签名时间戳是整秒；多留 1 秒，不依赖库里时间戳的精度与舍入
		g.recent.raiseRecheck(latest.Add(time.Second))
	}
}

// acquireStore 按状态机决定这次认领要不要去 Valkey；probe 为真表示这次是冷却后的探测。
func (g *nonceGuard) acquireStore() (use, probe bool) {
	if g.store == nil {
		return false, false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	switch g.state {
	case storeHealthy:
		return true, false
	case storeDown:
		if g.now().Before(g.downUntil) {
			return false, false
		}
		g.state = storeProbing
		return true, true
	default: // storeProbing：已有一个探测在途
		return false, false
	}
}

// storeSucceeded 记下一次 Valkey 应答（占到或已被占用都算）。只有健康时的应答和探测的
// 应答改状态：冷却开始前发出、冷却中才回来的旧应答不把状态拉回 healthy。
func (g *nonceGuard) storeSucceeded(probe bool) {
	g.mu.Lock()
	if !probe {
		g.mu.Unlock()
		return
	}
	g.state = storeHealthy
	warned, claims, since := g.outageWarned, g.fallbackClaims, g.lastWarn
	g.outageWarned, g.fallbackClaims = false, 0
	now := g.now()
	g.mu.Unlock()
	if warned {
		g.log.Info("Valkey 认领签名请求 nonce 已恢复", "pg_claims", claims, "since_warn", now.Sub(since).String())
	}
}

// storeFailed 记下一次 Valkey 出错或超时：进入冷却，冷却期内直接走 PG。
func (g *nonceGuard) storeFailed(probe bool, err error) {
	g.mu.Lock()
	if !probe && g.state != storeHealthy {
		g.mu.Unlock() // 冷却开始前发出的请求晚到的失败：已在冷却，不重复计
		return
	}
	now := g.now()
	g.state = storeDown
	g.downUntil = now.Add(nonceStoreCooldown)
	warn := !g.outageWarned && now.Sub(g.lastWarn) >= nonceOutageLogEvery
	if warn {
		g.outageWarned, g.lastWarn = true, now
	}
	g.mu.Unlock()
	if warn {
		g.log.Warn("Valkey 认领签名请求 nonce 失败，回落到 PG", "error", err.Error())
	}
}

// storeAbandoned 是调用方取消了请求，没拿到 Valkey 的结论：探测让给下一个请求。
func (g *nonceGuard) storeAbandoned(probe bool) {
	if !probe {
		return
	}
	g.mu.Lock()
	g.state = storeDown
	g.mu.Unlock()
}

func (g *nonceGuard) countFallback() {
	g.mu.Lock()
	g.fallbackClaims++
	g.mu.Unlock()
}

// claim 走一遍认领：近期集 → Valkey（状态机允许时）→ PG（回落，或补查界内的补查）。
// replayed 是近期集或 Valkey 判重放时回的错误；pgClaim 在 PG 认领，重放与后端错误
// 由它自己按调用方的口径返回。
func (g *nonceGuard) claim(ctx context.Context, key recentKey, storeKey string, requestTS time.Time,
	replayed error, pgClaim func(context.Context) error) error {
	if !g.recent.claim(key, g.now()) {
		return replayed
	}
	if use, probe := g.acquireStore(); use {
		storeCtx, cancel := context.WithTimeout(ctx, nonceStoreTimeout)
		claimed, err := g.store.ClaimNonce(storeCtx, storeKey, signedNonceRetention)
		cancel()
		switch {
		case err == nil:
			g.storeSucceeded(probe)
			g.recent.markStored(key)
			if !claimed {
				return replayed
			}
			if !g.recent.needsRecheck(requestTS) {
				return nil
			}
			// 签名时间戳落在补查界内：PG 里可能有这条（上次运行或被挤出近期集的回落），
			// 再认领一次，重放照样撞主键。这只是补查，不算回落。
			return pgClaim(ctx)
		case ctx.Err() != nil:
			g.storeAbandoned(probe)
			return ctx.Err()
		default:
			g.storeFailed(probe, err)
		}
	}
	// 回落：这条在近期集里保持 unstored，被挤出近期集时会抬补查界。
	g.countFallback()
	return pgClaim(ctx)
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
	storeKey := nonceKeyPrefix + tenantID + ":" + nodeID + ":" + base64.RawURLEncoding.EncodeToString(nonce)
	err = g.claim(ctx, makeRecentKey(recentKindNode, tenantID, nodeID, nonce), storeKey, requestTS, errNonceReplayed,
		func(ctx context.Context) error {
			var dbErr error
			epoch, dbErr = s.claimNonceInDatabase(ctx, tenantID, nodeID, nonce, fingerprint, requestTS)
			epochKnown = dbErr == nil
			return dbErr
		})
	if err != nil {
		return 0, false, err
	}
	return epoch, epochKnown, nil
}
