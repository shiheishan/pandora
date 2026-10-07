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

	// 以下三样只服务订阅拉取（LoadPull），全在进程内、有上限、有 TTL：
	// prefixes 缓存租户的订阅路径前缀；nodes 按（套餐版本, 用户组）缓存可下发节点，
	// 收到节点变更信号即失效（AttachRealtime）；failures 给未认证失败的落库采样。
	prefixes *prefixCache
	nodes    *nodeCache
	failures *failureSampler
}

func New(pool *db.Pool, ipSalt []byte, envelope *crypto.Envelope) *Service {
	return &Service{pool: pool, ipSalt: ipSalt, envelope: envelope,
		prefixes: newPrefixCache(prefixCacheTTL),
		nodes:    newNodeCache(nodeCacheTTL, nodeCacheMaxEntries),
		failures: newFailureSampler(failureSampleWindow, failureSamplesPerWindow),
	}
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

// linkSourceWindow 是「近期不同来源数」的统计窗口：门户上显示为近 24 小时。
const linkSourceWindow = "24 hours"

// ListLinks 返回某个用户全部有效的订阅链接。
//
// 路径前缀与每条链接近 24 小时的不同来源数都在同一条语句里取：来源数原先是
// 每条链接另开一个事务各算一次（N+1），现在是按凭据走
// subscription_fetch_log_cred_idx 的相关子查询。
func (s *Service) ListLinks(ctx context.Context, tenantID, userID string) ([]Link, error) {
	var out []Link

	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: userID}, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT sc.id, sc.subscription_id, sc.token_encrypted, sc.expires_at,
			       sc.fetch_count, sc.last_fetched_at,
			       (SELECT COALESCE(t.sub_path_prefix, '') FROM tenants t WHERE t.id = sc.tenant_id),
			       -- 按凭据 ID 统计（每条链接是独立的凭据）。之前误传 SubscriptionID，
			       -- 导致永远查不到来源、恒为 0
			       (SELECT count(DISTINCT f.ip_hash)::int FROM subscription_fetch_log f
			         WHERE f.tenant_id = sc.tenant_id AND f.credential_id = sc.id
			           AND f.result = 'ok'
			           AND f.fetched_at > now() - interval '`+linkSourceWindow+`')
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
				&l.FetchCount, &l.LastFetchedAt, &l.PathPrefix, &l.DistinctSources); err != nil {
				return err
			}
			l.SubscriptionID = subID
			l.CredentialID = id
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
//
// 租户的前缀读自进程内缓存（prefixCacheTTL）：每次拉取都读一遍 tenants
// 对扫描流量就是白白的库负载，而前缀只在迁移 00018 里生成一次、代码从不改它。
func (s *Service) MatchPrefix(ctx context.Context, tenantID, prefix string) (bool, error) {
	if prefix == "" {
		return false, nil
	}
	want, err := s.PathPrefix(ctx, tenantID)
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

// PathPrefix 返回该租户的订阅路径前缀（经进程内缓存）。
func (s *Service) PathPrefix(ctx context.Context, tenantID string) (string, error) {
	if prefix, ok := s.prefixes.get(tenantID); ok {
		return prefix, nil
	}
	var prefix string
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT COALESCE(sub_path_prefix, '') FROM tenants WHERE id = $1`, tenantID).Scan(&prefix)
	})
	if err != nil {
		return "", err
	}
	s.prefixes.put(tenantID, prefix)
	return prefix, nil
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
	// UserGroupID 是订阅主人所在的用户组（默认组为空串）。节点池可以限定用户组，
	// 同一套餐版本下可下发的节点只随它变，所以它是节点缓存键的一部分
	UserGroupID string
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

// ListNodes 返回该订阅可用的节点，每次都现查（不经节点缓存）。
// 订阅拉取走 LoadPull 里的缓存；后台核对与测试要的是此刻的真实答案。
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
// 管理后台的复述；服务器、协议、地址、池绑套餐这几条由 NodeDeliveryFacts.Refine
// 按同一个 DeliverableNodeSQL 补齐。两处必须同步 —— 有一条契约测试锁着这件事，
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
// 与用户、套餐都无关的节点自身条件抽在 DeliverableNodeSQL，套餐页的可下发节点数
// 与后台节点列表的下发说明共用它；这里只在其上加套餐版本绑池与池的用户组限定。
//
// 要带上用户：节点池可以限定用户组（R104），同一个套餐版本下不同组的用户
// 拿到的节点可能不同。谓词与节点拉用户（nodefabric.ListNodeUsers）共用
// nodefabric.PoolAdmitsUserSQL。userID 必须是订阅的主人。
func listEligibleNodesTx(ctx context.Context, tx pgx.Tx, tenantID, userID, planVersionID string) ([]Node, error) {
	out := []Node{}
	rows, err := tx.Query(ctx, `
			SELECT COALESCE(NULLIF(n.display_name, ''), n.name),
			       n.node_type,
			       `+nodeHostSQL+`,
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
			   AND `+DeliverableNodeSQL()+`
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
		// 没有可连地址的节点 SQL 已经排除（DeliverableNodeSQL），这里兜底：
		// 发给客户端只会变成一条连不上的线路，用户看到的是「你们家节点坏了」
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
