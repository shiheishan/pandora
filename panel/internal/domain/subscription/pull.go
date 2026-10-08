package subscription

// 一次订阅拉取在库里的全部动作。
//
// 原先一次成功拉取要开 6 个事务、33 条语句（含连接归还时清会话变量的那条）：
// 前缀、认证、节点、用量、限流计数加日志、更新凭据各一个事务。现在是：
//
//   - 读：前缀走进程内缓存；凭据、订阅、用户组、用量与流量包余量在一个只读事务
//     的一条语句里取齐；节点按（套餐版本, 用户组）走进程内缓存，未命中才另开事务查。
//   - 写：限流计数、拉取日志、凭据上的拉取次数合在一个事务里。
//
// 认证本身从不缓存：轮换或吊销链接后，旧链接下一次拉取就失效。

import (
	"context"
	"crypto/hmac"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/domain/nodefabric"
	"github.com/aegispanel/aegis/internal/platform/crypto"
	"github.com/aegispanel/aegis/internal/platform/db"
)

// errNoUsageRow 表示订阅认证通过、却没有本期流量配额行。原先 LoadUsage 在这里
// 报库错误、回伪装页并记一条 error 日志，行为保持不变。
var errNoUsageRow = errors.New("订阅没有流量配额行")

// Pull 是一次订阅拉取在库里读到的东西。
type Pull struct {
	Cred  *Credential
	Nodes []Node
	Usage Usage
	// Label 与 PlanName 拼配置名（ProfileName）：Content-Disposition 的文件名
	Label    string
	PlanName string
	// Expired 非空表示令牌有效、订阅已过期（规则 1）：不取节点，调用方只回一条提示节点
	Expired *ExpiredPull
}

// ExpiredPull 是过期订阅拉取要的信息。
type ExpiredPull struct {
	PeriodEnd time.Time
	// Location 是提示里显示到期时刻用的时区：用户时区，未设（'UTC'）跟随站点时区
	Location *time.Location
}

// ErrExpired 是 checkCredential 的内部结论：令牌对、凭据没被吊销，但订阅已过期。
// 只在本包内流转，LoadPull 把它变成 Pull.Expired，不会返回给调用方。
var ErrExpired = errors.New("订阅已过期")

// pullAuthSQL 按令牌哈希取凭据、订阅、用户组与用量，$1 租户、$2 令牌哈希。
//
// token_hash 上有唯一索引（subscription_credentials_hash_unique），按它查是一次
// 索引等值查找。原先按 token_prefix 查，而那一列没有索引，每次拉取都要扫一遍
// 本租户的全部凭据。哈希是 SHA-256，扫描器控制不了它的取值，按它走 B-tree
// 不泄露任何可利用的时序信息；取回后仍做一次定时安全比较。
//
// 用量与 LoadUsage 原来的两条查询同口径：本期配额取 period_end 最晚的一行；
// 流量包余量是挂在这一份订阅上的流量包的「授予 − 已用」之和（购买模型统一：流量包按份挂，
// 走 idx_traffic_pack_grants_open_sub）。备注名与套餐名（按主键取一行）拼配置名。
const pullAuthSQL = `
	SELECT sc.id, sc.token_hash, sc.subscription_id, sc.user_id, sc.status,
	       sc.expires_at, sc.grace_until, sc.rate_limit_per_hour,
	       s.plan_version_id, s.proxy_uuid, s.node_uid, s.status, s.current_period_end,
	       s.renewal_closed_at IS NOT NULL,
	       COALESCE(u.timezone, ''),
	       COALESCE((SELECT t.timezone FROM tenants t WHERE t.id = s.tenant_id), ''),
	       COALESCE(u.user_group_id::text, ''),
	       q.granted, q.consumed,
	       (SELECT coalesce(sum(g.granted_bytes - g.consumed_bytes), 0)::bigint
	          FROM traffic_pack_grants g
	         WHERE g.tenant_id = s.tenant_id AND g.subscription_id = s.id
	           AND g.consumed_bytes < g.granted_bytes),
	       COALESCE(s.label, ''), COALESCE(pl.name, '')
	  FROM subscription_credentials sc
	  JOIN subscriptions s ON s.id = sc.subscription_id AND s.tenant_id = sc.tenant_id
	  LEFT JOIN plans pl ON pl.id = s.plan_id
	  LEFT JOIN users u ON u.tenant_id = s.tenant_id AND u.id = s.user_id
	  LEFT JOIN LATERAL (
	        SELECT COALESCE(qb.limit_value, 0) + COALESCE(qb.granted_addon, 0) + COALESCE(qb.adjusted, 0) AS granted,
	               COALESCE(qb.consumed, 0) AS consumed
	          FROM quota_balances qb
	         WHERE qb.tenant_id = sc.tenant_id AND qb.subscription_id = s.id
	           AND qb.metric = 'traffic.bytes'
	         ORDER BY qb.period_end DESC NULLS LAST
	         LIMIT 1) q ON true
	 WHERE sc.tenant_id = $1 AND sc.token_hash = $2`

// LoadPull 完成一次订阅拉取的全部读取。
//
// 返回 ErrNotFound 表示前缀或令牌不对、凭据失效或订阅不可用——对外一律回伪装页。
// 令牌有效、订阅已过期（原地续费窗口没关）时不报错，返回的 Pull.Expired 非空、不带节点。
// 其余错误是库错误、超时或数据缺失；认证已经通过时，返回的 Pull 里带着 Cred，
// 调用方据此记一条带凭据的 error 日志。
func (s *Service) LoadPull(ctx context.Context, tenantID, prefix, token string) (*Pull, error) {
	ok, err := s.MatchPrefix(ctx, tenantID, prefix)
	if err != nil {
		return nil, fmt.Errorf("读订阅路径前缀: %w", err)
	}
	if !ok {
		return nil, ErrNotFound
	}
	if len(token) < 16 {
		// 长度都不对，连查库都不必 —— 扫描器的绝大多数试探止于此
		return nil, ErrNotFound
	}

	var c Credential
	var granted, consumed *int64
	var packRemaining int64
	var userTZ, tenantTZ, label, planName string
	want := crypto.HashToken(token)
	err = s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		var (
			hash                  []byte
			credStatus            string
			expiresAt, graceUntil *time.Time
			renewalClosed         bool
		)
		err := tx.QueryRow(ctx, pullAuthSQL, tenantID, want).Scan(
			&c.ID, &hash, &c.SubscriptionID, &c.UserID, &credStatus,
			&expiresAt, &graceUntil, &c.RateLimit,
			&c.PlanVersionID, &c.ProxyUUID, &c.NodeUID, &c.Status, &c.PeriodEnd,
			&renewalClosed, &userTZ, &tenantTZ,
			&c.UserGroupID, &granted, &consumed, &packRemaining, &label, &planName)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		return checkCredential(hash, want, credStatus, expiresAt, graceUntil, c.Status,
			renewalClosed, time.Now())
	})
	if errors.Is(err, ErrExpired) {
		pull := &Pull{Cred: &c, Label: label, PlanName: planName, Expired: &ExpiredPull{
			Location: nodefabric.UsageLocation(userTZ, tenantTZ)}}
		if c.PeriodEnd != nil {
			pull.Expired.PeriodEnd = *c.PeriodEnd
			pull.Usage.Expire = c.PeriodEnd.Unix()
		}
		if granted != nil && consumed != nil {
			pull.Usage.Total = *granted + packRemaining
			pull.Usage.Download = *consumed
		}
		return pull, nil
	}
	if err != nil {
		return nil, err
	}
	pull := &Pull{Cred: &c, Label: label, PlanName: planName}
	if granted == nil || consumed == nil {
		return pull, errNoUsageRow
	}
	if c.PeriodEnd != nil {
		pull.Usage.Expire = c.PeriodEnd.Unix()
	}
	// 流量包余额（D-E-1）挂在这一份订阅上，套餐额度用完后接着用：客户端显示的
	// 总量 = 套餐本期额度 + 流量包剩余，已用量只算套餐部分，剩余正好是两者之和。
	pull.Usage.Total = *granted + packRemaining
	// 客户端把 upload+download 相加当作已用量。
	// 我们只记总量，全部计入 download 而不是对半分 ——
	// 编造一个看似合理的上下行比例，会让用户在客户端里看到假数据。
	pull.Usage.Download = *consumed

	nodes, err := s.cachedNodes(ctx, tenantID, &c)
	if err != nil {
		return pull, fmt.Errorf("取订阅节点: %w", err)
	}
	pull.Nodes = nodes
	return pull, nil
}

// checkCredential 判定一条按哈希取回的凭据能不能用：能用回 nil；令牌对、凭据没被吊销、
// 订阅只是过期了（原地续费窗口没关）回 ErrExpired；其余一律 ErrNotFound。
//
// 伪装 404 只留给「令牌不存在、已吊销、前缀不对」与不在服务的订阅（暂停、取消……）：
// 持有有效令牌的人本来就知道链接是真的，告诉他「已过期」不泄露新信息（规则 1）。
// 过期满 30 天关窗后凭据被吊销（billing/expire.go），从那以后回 404。
func checkCredential(hash, want []byte, credStatus string, expiresAt, graceUntil *time.Time,
	subStatus string, renewalClosed bool, now time.Time) error {
	// 定时安全比较，避免按字节比对泄露信息
	if !hmac.Equal(hash, want) {
		return ErrNotFound
	}
	if credStatus != "active" && credStatus != "grace" {
		return ErrNotFound
	}
	// 宽限期内仍然放行：付费用户续费晚了几小时就完全断连，
	// 换来的是客服工单而不是收入。grace 状态与 active 一样
	// 按最终截止时间（expires_at 与 grace_until 取大者）判定。
	deadline := expiresAt
	if graceUntil != nil && (deadline == nil || graceUntil.After(*deadline)) {
		deadline = graceUntil
	}
	lapsed := deadline != nil && now.After(*deadline)
	if lapsed && credStatus != "active" {
		// 换发链接后留着宽限的旧凭据，宽限一过就是失效的链接，不是「订阅过期」
		return ErrNotFound
	}
	switch subStatus {
	case "active", "trialing", "grace":
		if lapsed {
			// 状态还没被过期扫描改过来，但已经过了截止时间
			return ErrExpired
		}
		return nil
	case "expired":
		if renewalClosed {
			return ErrNotFound
		}
		return ErrExpired
	default:
		return ErrNotFound
	}
}

// cachedNodes 按（租户, 套餐版本, 用户组）取可下发节点，未命中时现查一次。
//
// 资格查询只依赖这三样（listEligibleNodesTx 里与用户相关的只有节点池限定的
// 用户组），所以同组同套餐版本的用户共用一份结果。现查用的是本次拉取的订阅主人，
// 查出来的正是他那一组的答案。
func (s *Service) cachedNodes(ctx context.Context, tenantID string, c *Credential) ([]Node, error) {
	key := nodeCacheKey{tenant: tenantID, planVersion: c.PlanVersionID, userGroup: c.UserGroupID}
	return s.nodes.load(ctx, key, func(ctx context.Context) ([]Node, error) {
		return s.ListNodes(ctx, tenantID, c)
	})
}

// RecordSuccessfulFetch 在同一事务里检查限流、记下本次成功拉取并更新凭据上的拉取信息。
//
// 先 UPDATE 凭据行：它拿到行锁，把同一凭据的并发请求串行化（原先是
// SELECT … FOR UPDATE），避免「先查后写」一起越过上限；拉取次数与时间原先在写完
// 响应后另开一个事务记，现在顺带记上。拿到锁之后的下一条语句取新快照，看得到先
// 拿锁那次已提交的日志，计数不会漏。超限时整个事务回滚：日志不写、次数也不加，
// 与原先一致（失败请求不占额度）。
func (s *Service) RecordSuccessfulFetch(ctx context.Context, tenantID, credID, subID,
	format, ip, ua, uaFamily string, nodeCount, bytesSent, limit int) error {
	return s.recordFetch(ctx, tenantID, credID, subID, "ok", format, ip, ua, uaFamily,
		nodeCount, bytesSent, limit)
}

// RecordExpiredFetch 记一次过期订阅的拉取（规则 1）：结果记 expired，与扫描器的
// not_found 分开；限流与成功拉取同一套，只是按 expired 自己的次数算，拿着有效令牌
// 反复刷提示也一样被挡。
func (s *Service) RecordExpiredFetch(ctx context.Context, tenantID, credID, subID,
	format, ip, ua, uaFamily string, bytesSent, limit int) error {
	return s.recordFetch(ctx, tenantID, credID, subID, "expired", format, ip, ua, uaFamily,
		0, bytesSent, limit)
}

// recordFetch 是 RecordSuccessfulFetch / RecordExpiredFetch 的共同实现；result 只会是
// ok 或 expired，限流按同一 result 的近一小时次数算。
func (s *Service) recordFetch(ctx context.Context, tenantID, credID, subID, result,
	format, ip, ua, uaFamily string, nodeCount, bytesSent, limit int) error {
	return s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		ipHash := s.hash(ip)
		var lockedID string
		if err := tx.QueryRow(ctx, `
			UPDATE subscription_credentials
			   SET fetch_count = fetch_count + 1,
			       last_fetched_at = now(),
			       last_fetch_ip_hash = $3
			 WHERE tenant_id = $1 AND id = $2::uuid
			RETURNING id::text`, tenantID, credID, ipHash).Scan(&lockedID); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `
			INSERT INTO subscription_fetch_log
				(tenant_id, credential_id, subscription_id, ip_hash, ua_hash,
				 ua_family, result, format, node_count, bytes_sent, ip_enc, ua_enc)
			SELECT $1::uuid, $2::uuid, $3::uuid, $4::bytea, $5::bytea,
			       $6::text, $13::text, $7::text, $8::int, $9::int, $10::bytea, $11::bytea
			 WHERE $12::int <= 0
			    OR (SELECT count(*) FROM subscription_fetch_log
			         WHERE tenant_id = $1::uuid AND credential_id = $2::uuid
			           AND fetched_at > now() - interval '1 hour'
			           AND result = $13::text) < $12::int`,
			tenantID, credID, subID, ipHash, s.hash(ua), uaFamily,
			nullIfEmpty(format), nodeCount, bytesSent, s.seal(ip), s.seal(ua), limit, result)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrRateLimited
		}
		return nil
	})
}

// RecordUnauthenticated 记一次未认证失败（前缀或令牌不对、凭据失效）。
//
// 这类请求几乎全是扫描器，原先每次都写一行拉取日志，等于让扫描流量直接变成库写入。
// 现在按来源采样：同一来源每个窗口最多落一行，整个进程每个窗口最多落
// failureSamplesPerWindow 行，其余只计数。返回值是上一个窗口里没落库的次数
// （窗口切换后的第一次调用报一次），调用方据此打一条汇总日志。
func (s *Service) RecordUnauthenticated(ctx context.Context, tenantID, ip, ua, uaFamily string) (dropped int64) {
	admit, dropped := s.failures.admit(string(s.hash(ip)))
	if admit {
		s.Log(ctx, tenantID, "", "", "not_found", "", ip, ua, uaFamily, 0, 0)
	}
	return dropped
}

const (
	// failureSampleWindow 与 failureSamplesPerWindow：每分钟最多 60 行未认证失败日志，
	// 同一来源每分钟最多 1 行。后台访问日志里仍能看到「有人在扫、从哪些来源扫」，
	// 但写入量与扫描速度脱钩。
	failureSampleWindow     = time.Minute
	failureSamplesPerWindow = 60
)

// failureSampler 决定哪些未认证失败落库。窗口内记过的来源只存带盐哈希，
// 条目数不超过每窗口额度，窗口切换时整体清空。
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

func newFailureSampler(window time.Duration, perWindow int) *failureSampler {
	return &failureSampler{window: window, perWindow: perWindow, now: time.Now,
		seen: make(map[string]struct{}, perWindow)}
}

// admit 返回这次是否落库，以及刚结束的窗口里没落库的次数（只在窗口切换时非零）。
// 采样器为 nil 时一律落库（测试里直接构造的 Service）。
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
