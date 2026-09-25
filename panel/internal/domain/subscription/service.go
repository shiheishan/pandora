// [INPUT]: 依赖 subscription_credentials / subscriptions / quota_balances / traffic_pack_grants 表，依赖 platform/crypto、platform/db
// [OUTPUT]: 对外提供 Service、New 与订阅分发用例：ListLinks、Rotate、Authenticate、ListNodes、ListOwnedNodePreviews（两者按订阅主人过滤限定了用户组的节点池，R104）、DeliveryState（后台节点列表对下发规则的复述，含无池节点）、LoadUsage、Log 等
// [POS]: subscription 的订阅分发核心；LoadUsage 的总量 = 套餐本期额度 + 用户流量包剩余（D-E-1），供 Subscription-Userinfo
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

// Package subscription 实现订阅分发。
//
// 这是整条链路的最后一环：用户付了钱、节点也跑起来了，但只有订阅链接
// 能把两者接上。也因此它是唯一一个「未登录、来自公网、可被任意人访问」
// 的业务端点 —— 抗探测的要求比其它接口高一个量级。
//
// 安全模型（先说清楚不做什么）：内容加密解决不了订阅泄露。
// 客户端是 Clash、小火箭这类通用软件，必须能解开我们发的东西，
// 密钥只能随链接一起给出去，那与不加密等价。真正起作用的是下面这些：
//
//   - token 32 字节随机，枚举不可行
//   - 路径前缀按租户随机，避免全网按固定路径批量识别
//   - 认证失败一律回伪装内容，不泄露「这里是个机场面板」
//   - 每次拉取留审计，多来源并发可被发现，泄露可追溯
//   - 链接可轮换，泄露后一键失效
package subscription

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/domain/nodefabric"
	"github.com/aegispanel/aegis/internal/platform/crypto"
	"github.com/aegispanel/aegis/internal/platform/db"
)

var (
	// ErrNotFound 涵盖「token 不存在」「已吊销」「已过期」三种情况。
	//
	// 刻意合并成一个错误：调用方拿不到区别，也就无法把区别泄露出去。
	// 「这个 token 存在但过期了」这句话本身就告诉了探测者一件事 ——
	// 他猜中了一个真实存在的 token。
	ErrNotFound = errors.New("订阅不可用")
	// ErrRateLimited 单独区分，因为它需要回 429 而不是伪装内容。
	ErrRateLimited = errors.New("请求过于频繁")
)

type Service struct {
	pool *db.Pool
	// ipSalt 用于哈希审计日志里的 IP 与 UA。
	// 与数据库分开保管：库被拖走时，攻击者拿到的哈希无法反查出真实 IP。
	ipSalt []byte
	// envelope 用于还原订阅 token 原文（面板展示、轮换后回显）
	envelope *crypto.Envelope
}

func New(pool *db.Pool, ipSalt []byte, envelope *crypto.Envelope) *Service {
	return &Service{pool: pool, ipSalt: ipSalt, envelope: envelope}
}

// Link 是一条可展示给用户的订阅链接信息。
type Link struct {
	SubscriptionID string
	CredentialID   string
	Token          string
	PathPrefix     string
	ExpiresAt      *time.Time
	FetchCount     int64
	LastFetchedAt  *time.Time
	// DistinctSources 是近 24 小时内的不同来源数。
	// 明显偏高就意味着这条链接很可能被分享出去了。
	DistinctSources int
}

// ListLinks 返回某个用户全部有效的订阅链接。
func (s *Service) ListLinks(ctx context.Context, tenantID, userID string) ([]Link, error) {
	var prefix string
	var out []Link

	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: userID}, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx,
			`SELECT COALESCE(sub_path_prefix, '') FROM tenants WHERE id = $1`,
			tenantID).Scan(&prefix); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `
			SELECT sc.id, sc.subscription_id, sc.token_encrypted, sc.expires_at,
			       sc.fetch_count, sc.last_fetched_at
			  FROM subscription_credentials sc
			 WHERE sc.tenant_id = $1 AND sc.user_id = $2::uuid
			   AND sc.status = 'active' AND sc.scope = 'subscription'
			   AND ((sc.expires_at IS NULL AND sc.grace_until IS NULL)
			        OR GREATEST(sc.expires_at, sc.grace_until) > now())
			 ORDER BY sc.created_at DESC`, tenantID, userID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id, subID string
			var sealed []byte
			var l Link
			if err := rows.Scan(&id, &subID, &sealed, &l.ExpiresAt,
				&l.FetchCount, &l.LastFetchedAt); err != nil {
				return err
			}
			l.SubscriptionID = subID
			l.CredentialID = id
			l.PathPrefix = prefix
			if len(sealed) > 0 && s.envelope != nil {
				plain, err := s.envelope.Open(sealed, []byte(subID))
				if err != nil {
					// 解不开就跳过而不是报错：多半是这条凭据签发于
					// 加密上线之前（那时只存了哈希）。让用户看到「重新生成」
					// 按钮，比抛一个他看不懂的错误好。
					continue
				}
				l.Token = string(plain)
			}
			if l.Token == "" {
				continue
			}
			out = append(out, l)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	for i := range out {
		// DistinctSources 按凭据 ID 统计（每条链接是独立的凭据）。
		// 之前误传 SubscriptionID，导致永远查不到来源、恒为 0。
		out[i].DistinctSources = s.DistinctSources(ctx, tenantID, out[i].CredentialID, 24*time.Hour)
	}
	return out, nil
}

// Rotate 换掉一条订阅的链接：旧的立即失效，返回新的。
//
// 用「吊销 + 新签」而不是原地改 token：凭据表关联着追加写的审计日志，
// 删不掉也不该删 —— 泄露之后最需要回答的问题正是「旧链接被谁用过」，
// 把记录抹掉等于把唯一的线索也一起丢了。
func (s *Service) Rotate(ctx context.Context, tenantID, userID, subID string) (string, error) {
	parsedSubID, err := uuid.Parse(subID)
	if err != nil {
		return "", ErrNotFound
	}
	subID = parsedSubID.String()
	token, err := crypto.NewToken(32)
	if err != nil {
		return "", err
	}
	var sealed []byte
	if s.envelope != nil {
		if sealed, err = s.envelope.Seal([]byte(token), []byte(subID)); err != nil {
			return "", fmt.Errorf("加密订阅凭据: %w", err)
		}
	}

	err = s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: userID}, func(tx pgx.Tx) error {
		// 确认这条订阅确实属于该用户，避免拿别人的 subID 来换
		var owner string
		if err := tx.QueryRow(ctx,
			`SELECT user_id::text FROM subscriptions WHERE tenant_id = $1 AND id = $2::uuid`,
			tenantID, subID).Scan(&owner); err != nil {
			return ErrNotFound
		}
		if owner != userID {
			return ErrNotFound
		}

		if _, err := tx.Exec(ctx, `
			UPDATE subscription_credentials
			   SET status = 'revoked', revoked_at = now(), revoked_reason = 'rotated',
			       rotated_count = rotated_count + 1, rotated_at = now()
			 WHERE tenant_id = $1 AND subscription_id = $2::uuid AND status = 'active'`,
			tenantID, subID); err != nil {
			return err
		}

		var expires *time.Time
		_ = tx.QueryRow(ctx,
			`SELECT current_period_end FROM subscriptions WHERE tenant_id = $1 AND id = $2::uuid`,
			tenantID, subID).Scan(&expires)

		_, err := tx.Exec(ctx, `
			INSERT INTO subscription_credentials
				(tenant_id, subscription_id, user_id, token_hash, token_prefix,
				 scope, expires_at, token_encrypted)
			VALUES ($1,$2,$3::uuid,$4,$5,'subscription',$6,$7)`,
			tenantID, subID, userID, crypto.HashToken(token), token[:8], expires, sealed)
		return err
	})
	if err != nil {
		return "", err
	}
	return token, nil
}

// MatchPrefix 校验路径前缀是否属于该租户。
//
// 前缀本身不是密钥（它会出现在每个用户的链接里，谈不上保密），
// 作用是把「按固定路径全网扫」这条最省力的路堵死：
// 所有部署都用 /sub/ 的话，扫描器试一个路径就能把同类站点一网打尽。
func (s *Service) MatchPrefix(ctx context.Context, tenantID, prefix string) (bool, error) {
	if prefix == "" {
		return false, nil
	}
	var want string
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT COALESCE(sub_path_prefix, '') FROM tenants WHERE id = $1`, tenantID).Scan(&want)
	})
	if err != nil {
		return false, err
	}
	if want == "" {
		return false, nil
	}
	// 定时安全比较：前缀虽不保密，但逐字节比对会泄露「猜对了几位」，
	// 那足以把爆破难度从指数级降到线性
	return hmac.Equal([]byte(want), []byte(prefix)), nil
}

// PathPrefix 返回该租户的订阅路径前缀。
func (s *Service) PathPrefix(ctx context.Context, tenantID string) (string, error) {
	var prefix string
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT COALESCE(sub_path_prefix, '') FROM tenants WHERE id = $1`, tenantID).Scan(&prefix)
	})
	return prefix, err
}

// Credential 是一次成功认证的结果。
type Credential struct {
	ID             string
	SubscriptionID string
	UserID         string
	PlanVersionID  string
	ProxyUUID      string
	NodeUID        int64
	Status         string
	PeriodEnd      *time.Time
	RateLimit      int
}

// Node 是下发给客户端的一个节点。
type Node struct {
	Name        string
	Type        string
	Host        string
	Port        int
	Config      map[string]any
	TrafficRate float64
	// HeartbeatFresh 表示这个节点近期上报过心跳。
	// 只在 listEligibleNodesTx 内部用于分层，不出现在任何对外结构里。
	HeartbeatFresh bool
}

// NodePreview 是用户面板可见的最小节点信息。
//
// 它刻意不包含地址、端口和协议配置。面板只是让用户确认套餐当前可用的
// 线路与倍率，不是另一条订阅下发通道；敏感连接参数只能从订阅链接取得。
type NodePreview struct {
	Name        string
	Protocol    string
	TrafficRate float64
}

// Usage 是回传给客户端的用量，对应 Subscription-Userinfo 响应头。
type Usage struct {
	Upload   int64
	Download int64
	Total    int64
	Expire   int64 // Unix 秒，0 表示不过期
}

// Authenticate 用 token 换取订阅凭据。
//
// 认证与鉴权分两步：这里只确认「token 有效且订阅可用」，
// 具体能看到哪些节点由 ListNodes 决定。
func (s *Service) Authenticate(ctx context.Context, tenantID, token string) (*Credential, error) {
	if len(token) < 16 {
		// 长度都不对，连查库都不必 —— 扫描器的绝大多数试探止于此
		return nil, ErrNotFound
	}

	var c Credential
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		// 先用前缀缩小范围，再用哈希做定时安全比对。
		//
		// 只靠前缀查不安全（前缀会碰撞），只靠哈希查则无法走索引 ——
		// 每次拉取都全表扫在订阅这种高频接口上不可接受。
		rows, err := tx.Query(ctx, `
			SELECT sc.id, sc.token_hash, sc.subscription_id, sc.user_id, sc.status,
			       sc.expires_at, sc.grace_until, sc.rate_limit_per_hour,
			       s.plan_version_id, s.proxy_uuid, s.node_uid, s.status, s.current_period_end
			  FROM subscription_credentials sc
			  JOIN subscriptions s ON s.id = sc.subscription_id AND s.tenant_id = sc.tenant_id
			 WHERE sc.tenant_id = $1 AND sc.token_prefix = $2`,
			tenantID, token[:8])
		if err != nil {
			return err
		}
		defer rows.Close()

		want := crypto.HashToken(token)
		found := false
		for rows.Next() {
			var (
				id, subID, userID, credStatus string
				hash                          []byte
				expiresAt, graceUntil, perEnd *time.Time
				rate                          int
				planVersionID, proxyUUID      string
				nodeUID                       int64
				subStatus                     string
			)
			if err := rows.Scan(&id, &hash, &subID, &userID, &credStatus,
				&expiresAt, &graceUntil, &rate,
				&planVersionID, &proxyUUID, &nodeUID, &subStatus, &perEnd); err != nil {
				return err
			}
			// 定时安全比较，避免按字节比对泄露信息
			if !hmac.Equal(hash, want) {
				continue
			}
			found = true

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
			if deadline != nil && time.Now().After(*deadline) {
				return ErrNotFound
			}
			switch subStatus {
			case "active", "trialing", "grace":
			default:
				return ErrNotFound
			}

			c = Credential{
				ID: id, SubscriptionID: subID, UserID: userID,
				PlanVersionID: planVersionID, ProxyUUID: proxyUUID,
				NodeUID: nodeUID, Status: subStatus,
				PeriodEnd: perEnd, RateLimit: rate,
			}
			break
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if !found {
			return ErrNotFound
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// RecordSuccessfulFetch 在同一事务里检查限流并记下本次成功拉取。
// 凭据行锁会把同一凭据的并发请求串行化，避免“先查后写”一起越过上限。
func (s *Service) RecordSuccessfulFetch(ctx context.Context, tenantID, credID, subID,
	format, ip, ua, uaFamily string, nodeCount, bytesSent, limit int) error {
	return s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		var lockedID string
		if err := tx.QueryRow(ctx, `
			SELECT id::text FROM subscription_credentials
			 WHERE tenant_id=$1 AND id=$2::uuid FOR UPDATE`, tenantID, credID).
			Scan(&lockedID); err != nil {
			return err
		}
		if limit > 0 {
			var n int
			if err := tx.QueryRow(ctx, `
				SELECT count(*) FROM subscription_fetch_log
				 WHERE tenant_id=$1 AND credential_id=$2::uuid
				   AND fetched_at > now() - interval '1 hour'
				   AND result='ok'`, tenantID, credID).Scan(&n); err != nil {
				return err
			}
			if n >= limit {
				return ErrRateLimited
			}
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO subscription_fetch_log
				(tenant_id, credential_id, subscription_id, ip_hash, ua_hash,
				 ua_family, result, format, node_count, bytes_sent, ip_enc, ua_enc)
			VALUES ($1,$2::uuid,$3::uuid,$4,$5,$6,'ok',$7,$8,$9,$10,$11)`,
			tenantID, credID, subID, s.hash(ip), s.hash(ua), uaFamily,
			nullIfEmpty(format), nodeCount, bytesSent, s.seal(ip), s.seal(ua))
		return err
	})
}

// ListNodes 返回该订阅可用的节点。
func (s *Service) ListNodes(ctx context.Context, tenantID string, c *Credential) ([]Node, error) {
	var out []Node
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		var err error
		out, err = listEligibleNodesTx(ctx, tx, tenantID, c.UserID, c.PlanVersionID)
		return err
	})
	return out, err
}

// ListOwnedNodePreviews 返回当前用户一条可用订阅对应的安全节点摘要。
// 不存在、跨租户、非本人和不可用状态都折叠成同一个 ErrNotFound。
func (s *Service) ListOwnedNodePreviews(ctx context.Context, tenantID, userID, rawSubscriptionID string) ([]NodePreview, error) {
	subscriptionID, err := uuid.Parse(rawSubscriptionID)
	if err != nil {
		return nil, ErrNotFound
	}

	out := []NodePreview{}
	err = s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: userID}, func(tx pgx.Tx) error {
		var planVersionID string
		if err := tx.QueryRow(ctx, `
			SELECT plan_version_id::text
			  FROM subscriptions s
			 WHERE s.tenant_id=$1 AND s.id=$2::uuid AND s.user_id=$3::uuid
			   AND s.status IN ('active','trialing','grace')
			   AND EXISTS (
			     SELECT 1 FROM subscription_credentials sc
			      WHERE sc.tenant_id=s.tenant_id AND sc.subscription_id=s.id
			        AND sc.user_id=s.user_id AND sc.scope='subscription'
			        AND sc.status='active'
			        AND ((sc.expires_at IS NULL AND sc.grace_until IS NULL)
			             OR GREATEST(sc.expires_at,sc.grace_until) > now())
			   )`,
			tenantID, subscriptionID.String(), userID).Scan(&planVersionID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		nodes, err := listEligibleNodesTx(ctx, tx, tenantID, userID, planVersionID)
		if err != nil {
			return err
		}
		out = make([]NodePreview, 0, len(nodes))
		for _, node := range nodes {
			out = append(out, NodePreview{
				Name: node.Name, Protocol: node.Type, TrafficRate: node.TrafficRate,
			})
		}
		return nil
	})
	return out, err
}

// HeartbeatFreshWindow 是判定节点心跳「新鲜」的窗口。
//
// 生产上节点每十几秒上报一次，10 分钟给足了网络抖动和 agent 重启的余量。
// 它刻意比后台列表的 90 秒 stale 宽得多 —— 两个数字服务不同目的：
// stale 是给管理员看的实时状态，越灵敏越好；这个窗口决定用户能不能拿到
// 线路，误杀的代价是用户没得用，所以要保守。
//
// 后台的「是否下发」判定也用它（见 DeliveryState）。窗口只能改这一处。
const HeartbeatFreshWindow = 10 * time.Minute

// DeliveryState 回答「这个节点现在会不会出现在用户订阅里」，
// 并在不会时说明原因。
//
// 下发规则本身写在 listEligibleNodesTx 的 SQL 里，这个函数是它面向
// 管理后台的复述。两处必须同步 —— 有一条契约测试锁着这件事，
// 因为「同一个规则写在两处、改了一处」正是这类问题最常见的死法。
//
// pooled    = 划进了节点池（pool_id IS NOT NULL；SQL 里是 JOIN plan_node_pools）
// everSeen  = 曾经上报过心跳（last_heartbeat_at IS NOT NULL）
// beatFresh = 心跳在 HeartbeatFreshWindow 之内
func DeliveryState(servingStatus string, pooled, everSeen, beatFresh bool) (bool, string) {
	if servingStatus != "active" {
		return false, "服务状态不是 active，不下发"
	}
	if !pooled {
		// 节点用户列表（ListNodeUsers）同样不下发任何人（R104）
		return false, "未划入节点池，不服务任何用户"
	}
	if !everSeen {
		return false, "从未上报过心跳，不下发 —— 多半是建了没装 agent，" +
			"或是测试留下的记录。发出去就是一条必然连不上的线路"
	}
	if !beatFresh {
		return true, "心跳已超时，但仍在下发 —— 只有当套餐里一个新鲜节点都没有时" +
			"才会用到它。空订阅会让客户端清空服务器列表，比给一条可能不通的线路更糟"
	}
	return true, ""
}

// listEligibleNodesTx 是订阅下发和面板预览共用的唯一资格查询。
// 任何维护状态、协议稳定性或套餐资源池规则都只能在这里修改，避免两处漂移。
//
// 要带上用户：节点池可以限定用户组（R104），同一个套餐版本下不同组的用户
// 拿到的节点可能不同。谓词与节点拉用户（nodefabric.ListNodeUsers）共用
// nodefabric.PoolAdmitsUserSQL。userID 必须是订阅的主人。
func listEligibleNodesTx(ctx context.Context, tx pgx.Tx, tenantID, userID, planVersionID string) ([]Node, error) {
	out := []Node{}
	rows, err := tx.Query(ctx, `
			SELECT COALESCE(NULLIF(n.display_name, ''), n.name),
			       n.node_type,
			       COALESCE(NULLIF(n.server_host, ''), n.public_ipv4::text, n.hostname, ''),
			       COALESCE(n.server_port, 0),
			       COALESCE(n.protocol_config, '{}'::jsonb),
			       COALESCE(n.traffic_rate, 1.0),
			       (n.last_heartbeat_at >= now() - $3::interval) AS heartbeat_fresh
			  FROM nodes n
			  JOIN servers s
			    ON s.id = n.server_id AND s.tenant_id = n.tenant_id
			  JOIN plan_node_pools p
			    ON p.pool_id = n.pool_id AND p.tenant_id = n.tenant_id
			 WHERE n.tenant_id = $1
			   AND p.plan_version_id = $2::uuid
			   -- Logical service Nodes are governed by serving_status. A Server's
			   -- control/Agent Node additionally remains gated by the legacy state.
			   AND (s.control_node_id IS DISTINCT FROM n.id OR n.status = 'active')
			   AND n.node_type IS NOT NULL
			   AND n.server_port BETWEEN 1 AND 65535
			   AND s.status = 'ready' AND s.deleted_at IS NULL
			   AND n.serving_status = 'active'
			   -- 从未心跳过的节点不下发。它从来没接进来过 —— 多半是
			   -- 建了没装 agent，或是测试留下的记录。发给客户端就是
			   -- 一条必然连不上的线路，和下面「没有可连地址」是同一类。
			   --
			   -- 心跳「超时」不在这里排除：心跳走 agent → 面板的 HTTPS，
			   -- 代理走 用户 → 节点，两条独立链路。agent 挂了而 xray 还在
			   -- 跑是常见情况，在 SQL 里一刀切会把还能用的节点也踢掉。
			   -- 超时的降级处理放在下面 Go 侧。
			   AND n.last_heartbeat_at IS NOT NULL
			   AND `+nodefabric.StableProtocolReadySQL("n")+`
			   -- 池限定了用户组时，订阅的主人必须在名单内的组里（R104）
			   AND `+nodefabric.PoolAdmitsUserSQL("n.tenant_id", "n.pool_id", "$4::uuid")+`
			 ORDER BY n.sort_order, n.id`,
		tenantID, planVersionID, HeartbeatFreshWindow.String(), userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var n Node
		var raw []byte
		if err := rows.Scan(&n.Name, &n.Type, &n.Host, &n.Port, &raw, &n.TrafficRate,
			&n.HeartbeatFresh); err != nil {
			return nil, err
		}
		if len(raw) > 0 {
			_ = json.Unmarshal(raw, &n.Config)
		}
		if n.Config == nil {
			n.Config = map[string]any{}
		}
		// 没有可连地址的节点直接跳过：发给客户端只会变成一条连不上的线路，
		// 用户看到的是「你们家节点坏了」
		if n.Host == "" || n.Port == 0 {
			continue
		}
		out = append(out, n)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return preferFreshNodes(out), nil
}

// preferFreshNodes 优先只给心跳新鲜的节点，一个都没有时退回全部。
//
// 为什么不直接把超时的过滤掉：空订阅比「可能连不上」更糟。客户端拿到
// 空列表会把服务器全清掉，用户从「有几条线路可能不通」变成「一条都没有」，
// 而且这正好会放大已知的空订阅竞态。
//
// 心跳失联往往是面板侧的观测问题（agent 崩了、上报链路被挡），
// 节点本身还在转发。所以这里的规则是「有新鲜的就只给新鲜的，
// 没有就把手上的都给出去」，而不是「不新鲜就一律不给」。
func preferFreshNodes(nodes []Node) []Node {
	fresh := make([]Node, 0, len(nodes))
	for _, n := range nodes {
		if n.HeartbeatFresh {
			fresh = append(fresh, n)
		}
	}
	if len(fresh) == 0 {
		return nodes
	}
	return fresh
}

// LoadUsage 取出该订阅的流量用量，用于 Subscription-Userinfo。
func (s *Service) LoadUsage(ctx context.Context, tenantID string, c *Credential) (Usage, error) {
	var u Usage
	if c.PeriodEnd != nil {
		u.Expire = c.PeriodEnd.Unix()
	}
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		var granted, consumed int64
		err := tx.QueryRow(ctx, `
			SELECT COALESCE(limit_value, 0) + COALESCE(granted_addon, 0) + COALESCE(adjusted, 0),
			       COALESCE(consumed, 0)
			  FROM quota_balances
			 WHERE tenant_id = $1 AND subscription_id = $2::uuid AND metric = 'traffic.bytes'
			 ORDER BY period_end DESC NULLS LAST
			 LIMIT 1`, tenantID, c.SubscriptionID).Scan(&granted, &consumed)
		if err != nil {
			return err
		}
		// 流量包余额（D-E-1）挂在用户身上，套餐额度用完后接着用：客户端显示的
		// 总量 = 套餐本期额度 + 流量包剩余，已用量只算套餐部分，剩余正好是两者之和。
		var packRemaining int64
		if err := tx.QueryRow(ctx, `
			SELECT coalesce(sum(g.granted_bytes - g.consumed_bytes), 0)::bigint
			  FROM traffic_pack_grants g
			  JOIN subscriptions s ON s.tenant_id = g.tenant_id AND s.user_id = g.user_id
			 WHERE g.tenant_id = $1 AND s.id = $2::uuid`,
			tenantID, c.SubscriptionID).Scan(&packRemaining); err != nil {
			return err
		}
		u.Total = granted + packRemaining
		// 客户端把 upload+download 相加当作已用量。
		// 我们只记总量，全部计入 download 而不是对半分 ——
		// 编造一个看似合理的上下行比例，会让用户在客户端里看到假数据。
		u.Download = consumed
		return nil
	})
	return u, err
}

// Log 记一条审计。失败不影响主流程。
func (s *Service) Log(ctx context.Context, tenantID, credID, subID, result, format,
	ip, ua, uaFamily string, nodeCount, bytesSent int) {

	var credArg, subArg any
	if credID != "" {
		credArg = credID
	}
	if subID != "" {
		subArg = subID
	}
	// 哈希用来做关联（同一个 IP 拉了几条链接），密文用来给管理员看原文。
	// 两份都留：只存哈希的话，管理员在风控页看到的永远是一串十六进制，
	// 没法判断那到底是机房 IP 还是家宽。
	_ = s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO subscription_fetch_log
				(tenant_id, credential_id, subscription_id, ip_hash, ua_hash,
				 ua_family, result, format, node_count, bytes_sent, ip_enc, ua_enc)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`,
			tenantID, credArg, subArg, s.hash(ip), s.hash(ua),
			uaFamily, result, nullIfEmpty(format), nodeCount, bytesSent,
			s.seal(ip), s.seal(ua))
		return err
	})
}

// TouchCredential 更新凭据上的最后拉取信息。
func (s *Service) TouchCredential(ctx context.Context, tenantID, credID, ip string) {
	_ = s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			UPDATE subscription_credentials
			   SET fetch_count = fetch_count + 1,
			       last_fetched_at = now(),
			       last_fetch_ip_hash = $3
			 WHERE tenant_id = $1 AND id = $2::uuid`,
			tenantID, credID, s.hash(ip))
		return err
	})
}

// DistinctSources 返回最近一段时间内拉取过该凭据的不同来源数。
//
// 这是发现「链接被分享」最直接的信号：正常用户就算多设备，
// 出口 IP 也集中在少数几个；一条被挂到群里的链接，来源数会迅速发散。
func (s *Service) DistinctSources(ctx context.Context, tenantID, credID string, within time.Duration) int {
	var n int
	_ = s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, fmt.Sprintf(`
			SELECT count(DISTINCT ip_hash) FROM subscription_fetch_log
			 WHERE tenant_id = $1 AND credential_id = $2::uuid
			   AND result = 'ok'
			   AND fetched_at > now() - interval '%d seconds'`, int(within.Seconds())),
			tenantID, credID).Scan(&n)
	})
	return n
}

// seal 加密一条来源信息。
//
// AAD 用固定的 "subfetch"：与审计表的 "audit" 分开，
// 这样即使有人把两张表的密文互换，解密也会直接失败而不是给出错值。
// 密钥没配时返回 nil —— 少存一列，好过让整条日志写不进去。
func (s *Service) seal(v string) []byte {
	if v == "" || s.envelope == nil {
		return nil
	}
	out, err := s.envelope.Seal([]byte(v), []byte("subfetch"))
	if err != nil {
		return nil
	}
	return out
}

// hash 对 IP / UA 做带盐哈希。
func (s *Service) hash(v string) []byte {
	if v == "" {
		return nil
	}
	m := hmac.New(sha256.New, s.ipSalt)
	m.Write([]byte(v))
	return m.Sum(nil)
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
