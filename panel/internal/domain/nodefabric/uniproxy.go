package nodefabric

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"math"
	"sort"
	"strconv"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/aegispanel/aegis/internal/platform/audit"
	"github.com/aegispanel/aegis/internal/platform/crypto"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// UniProxy 协议实现（Xboard / V2board 兼容）。
//
// 这套接口的调用方是 V2bX、XrayR、Xboard-Node 这类现成节点端。协议本身很朴素，
// 但有两处必须按它的原样来，否则节点端会静默不工作：
//
//   - 用户 ID 是整数，且 push 上报时作为 JSON 的 key（因此是字符串形式的数字）
//   - config 支持 ETag，节点端靠 304 判断「配置没变」，返回体必须逐字节稳定，
//     所以下面所有 map 都先转成结构体再序列化，不直接 marshal map
//
// 与节点接入的关系：这里只管数据面（谁能连、用了多少流量），
// 节点的生命周期、探针、配置签名走 pdnd（pandora-native）的两阶段接入与签名配置那条路。两者互不依赖。

//------------------------------------------------------------------------------
// 节点鉴权
//------------------------------------------------------------------------------

type ServingNode struct {
	ID       string
	Name     string
	NodeType string
	// DeclaredType 是节点端在 URL 上自称的协议，仅在它和 NodeType 不一致
	// 时才有值。用来让上层记一条日志——多半意味着刚在面板上改过协议、
	// 节点端还没追上，属于正常的过渡态，但值得看见。
	DeclaredType  string
	ServerHost    string
	ServerPort    int
	TrafficRate   float64
	Protocol      json.RawMessage
	PoolID        *string
	Status        string
	ServerStatus  string
	ServingStatus string
	// Kernel 决定由哪个内核承载：auto / pandora-native / sing-box / xray-core。
	// auto 与 pandora-native 均要求当前 NativeCore 数据面；后两者只为
	// 显式兼容迁移保留。
	Kernel string

	// 出站与分流，由 LoadRouting 填充
	Outbounds []NodeOutbound
	Routes    []NodeRoute

	// deliveryEpoch 是读出这个节点视图时的下发纪元（nodecache.go），与节点行在同一条
	// 查询里读出。epochKnown 为假（别处拼出来的节点视图）时用户集不走缓存。
	deliveryEpoch int64
	epochKnown    bool
}

// AuthenticateNode 校验 UniProxy 请求携带的 node_id + token。
//
// token 在库里只有哈希。node_id 走的是节点 UUID，而不是协议里常见的自增整数 ——
// 节点数量有限，用 UUID 不会给节点端造成困扰，却省掉一套自增 ID 映射。
//
// 每次都查库，不缓存：后台停用、退役节点之后下一次请求就必须 401（uniproxy_e2e
// 钉着这一点），而这只是一次主键点查。它顺手读出下发纪元，用户集缓存拿它判断
// 自己是否过期，不必为此多跑一次查询。
func (s *Service) AuthenticateNode(ctx context.Context, tenantID, nodeID, token, nodeType string) (*ServingNode, error) {
	if nodeID == "" || token == "" {
		return nil, httpx.New(httpx.CodeUnauthorized, "缺少 node_id 或 token")
	}

	var n ServingNode
	var proto []byte
	var isControlNode bool
	// 一次往返（QueryRowScoped）：节点每个 UniProxy 请求都先过这里。
	err := s.pool.QueryRowScoped(ctx, db.Scope{TenantID: tenantID}, `
			SELECT n.id, n.name, coalesce(n.node_type,''), coalesce(n.server_host,''),
			       coalesce(n.server_port,0), n.traffic_rate, n.protocol_config, n.pool_id,
			       n.status, coalesce(n.kernel,'auto'), s.status, n.serving_status,
			       coalesce(s.control_node_id=n.id,false), `+deliveryEpochSQL+`
			  FROM nodes n
			  JOIN servers s ON s.tenant_id=n.tenant_id AND s.id=n.server_id
			 WHERE n.tenant_id = $1 AND n.id = $2::uuid
			   AND n.server_token_hash = $3
			   AND s.deleted_at IS NULL
			   AND s.status IN ('ready','draining')
			   AND n.serving_status IN ('active','draining')
			   AND n.node_type IS NOT NULL
			   AND n.server_port BETWEEN 1 AND 65535
			   AND `+StableProtocolReadySQL("n"),
		[]any{tenantID, nodeID, crypto.HashToken(token)},
		&n.ID, &n.Name, &n.NodeType, &n.ServerHost, &n.ServerPort,
		&n.TrafficRate, &proto, &n.PoolID, &n.Status, &n.Kernel,
		&n.ServerStatus, &n.ServingStatus, &isControlNode, &n.deliveryEpoch)
	if errors.Is(err, pgx.ErrNoRows) {
		// 节点不存在与 token 不对返回同一种错误
		return nil, httpx.New(httpx.CodeUnauthorized, "节点认证失败")
	}
	if err != nil {
		return nil, err
	}
	n.NodeType = CanonicalNodeType(n.NodeType)
	// 节点端声明的协议和库里不一致时，以库为准，不再拒绝认证。
	//
	// 原先这里直接返回 401。它看着像一道安全检查，其实不是：能走到这行
	// 说明 node_id 和 token 都已经验过了，而持有正确 token 的人本来就能
	// 拿到这个节点的全部配置——URL 上多写一个协议名不会让他多拿到任何
	// 东西。它真正的作用只是「断言两边认知一致」。
	//
	// 代价却很大：管理员在面板上把节点从 shadowsocks 改成 vless，节点端
	// 还带着旧的 node_type 来拉配置，从这一刻起每次请求都 401——配置拉
	// 不到、心跳停、节点直接失联，而面板上看不出是自己刚才那次修改造成的。
	// 想恢复只能上服务器改 config.json 再重启，「不用手动碰节点端」这个
	// 前提就没了。
	//
	// 现在的做法：库里的协议是唯一权威，下发的配置里带着它（protocol
	// 字段），节点端据此切换适配器。不一致只记一条日志——它仍然是个值得
	// 知道的信号（多半意味着刚改过协议、节点端还没追上），但不该让节点掉线。
	// Service 没有 logger，也不值得为一条日志改它的构造签名。把这个事实
	// 挂在返回值上，由 handler 那层（有 logger）决定怎么记。
	if requestedType := CanonicalNodeType(nodeType); requestedType != "" && requestedType != n.NodeType {
		n.DeclaredType = requestedType
	}

	// 只有正式在役的节点能拉用户。standby/canary 阶段的节点若也能拉，
	// 灰度就失去意义了 —— 用户会被分配到还没验证完的机器上（NODE-010）。
	if !legacyNodeStatusAllowsServing(isControlNode, n.Status) {
		return nil, httpx.New(httpx.CodeForbidden, "节点当前状态不可提供服务")
	}
	n.Protocol = proto
	n.epochKnown = true
	return &n, nil
}

// A logical service Node has no Agent lifecycle of its own, so serving_status
// is authoritative. A Server control Node still carries the legacy physical
// Agent lifecycle and must pass both gates.
func legacyNodeStatusAllowsServing(isControlNode bool, legacyStatus string) bool {
	if !isControlNode {
		return true
	}
	return legacyStatus == "active" || legacyStatus == "canary" || legacyStatus == "draining"
}

// IssueServerToken 为节点签发 UniProxy 接入令牌，明文只返回一次。
//
// 顺带返回节点类型：调用方要用它拼一键安装命令，而这里本来就要写这一行
// UPDATE，用 RETURNING 带出来比让上层再查一次省一个来回。
//
// 已退役 / 已销毁的节点拒绝签发（409）：给一个不再服务的节点发接入凭据，
// 等于让一台本该下线的机器重新拿到用户名单。签发写审计，与换令牌、吊销
// 身份同级（SEC-012）；审计只记签发这件事，不记令牌或其哈希。
func (s *Service) IssueServerToken(ctx context.Context, tenantID, actorID, nodeID string) (string, string, error) {
	if _, err := uuid.Parse(nodeID); err != nil {
		return "", "", httpx.New(httpx.CodeNotFound, "节点不存在")
	}
	tok, err := crypto.NewToken(24)
	if err != nil {
		return "", "", httpx.Internal(err)
	}
	var nodeType string
	err = s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: actorID}, func(tx pgx.Tx) error {
		var status, servingStatus string
		scanErr := tx.QueryRow(ctx,
			`SELECT status, serving_status FROM nodes WHERE tenant_id = $1 AND id = $2::uuid FOR UPDATE`,
			tenantID, nodeID).Scan(&status, &servingStatus)
		if errors.Is(scanErr, pgx.ErrNoRows) {
			return httpx.New(httpx.CodeNotFound, "节点不存在")
		}
		if scanErr != nil {
			return scanErr
		}
		if serverTokenRefused(status, servingStatus) {
			return httpx.New(httpx.CodeConflict, "节点已退役或已销毁，不能签发接入令牌")
		}
		if err := tx.QueryRow(ctx,
			`UPDATE nodes SET server_token_hash = $3,
			        server_token_issued_at = now(), server_token_issued_by = $4::uuid
			  WHERE tenant_id = $1 AND id = $2::uuid
			 RETURNING coalesce(node_type, '')`,
			tenantID, nodeID, crypto.HashToken(tok), actorID).Scan(&nodeType); err != nil {
			return err
		}
		return audit.Write(ctx, tx, tenantID, audit.Entry{ActorKind: "admin", ActorID: &actorID,
			Action: "node.server_token.issue", ResourceType: "node", ResourceID: &nodeID,
			APIDomain: "admin", RequestID: httpx.RequestIDFrom(ctx), Outcome: "success",
			AfterDigest: map[string]any{"node_type": nodeType, "old_token_revoked": true}})
	})
	if err != nil {
		return "", "", err
	}
	return tok, nodeType, nil
}

// serverTokenRefused 判定节点是否已退出服务、不能再签发接入令牌。
// 口径与 00058 的「非终态节点」一致：生命周期 retired / destroyed，
// 或服务状态 retired（后台「退役」即它）。
func serverTokenRefused(status, servingStatus string) bool {
	return status == "retired" || status == "destroyed" || servingStatus == "retired"
}

//------------------------------------------------------------------------------
// GET /api/v1/server/UniProxy/user
//------------------------------------------------------------------------------

// PoolAdmitsUserSQL 是节点池用户组限定（R104）的唯一 SQL 谓词：池没有限定
// （node_pool_user_groups 里没有它的行），或者用户所在的组在池的名单里。
// 默认组（users.user_group_id 为空）的用户进不了任何限定了的池——NULL 与
// 名单里的组连不上。
//
// 下发三处只能用它：本包 ListNodeUsers（节点拉用户），以及 subscription 的
// listEligibleNodesTx（订阅下载与门户预览）。三个参数是租户、池、用户的
// SQL 表达式，只接受这两个调用方的静态写法，绝不能是用户数据；写成白名单
// 也让「另写一份变体」在调用处就过不去。
func PoolAdmitsUserSQL(tenant, pool, user string) string {
	switch tenant + "|" + pool + "|" + user {
	case "s.tenant_id|$2::uuid|s.user_id", "n.tenant_id|n.pool_id|$4::uuid":
	default:
		panic("unsupported pool admission SQL expressions")
	}
	return "(NOT EXISTS (SELECT 1 FROM node_pool_user_groups npug" +
		" WHERE npug.tenant_id = " + tenant + " AND npug.pool_id = " + pool + ")" +
		" OR EXISTS (SELECT 1 FROM node_pool_user_groups npug" +
		" JOIN users npu ON npu.tenant_id = npug.tenant_id AND npu.user_group_id = npug.user_group_id" +
		" WHERE npug.tenant_id = " + tenant + " AND npug.pool_id = " + pool + " AND npu.id = " + user + "))"
}

type ProxyUser struct {
	ID          int64  `json:"id"`
	UUID        string `json:"uuid"`
	SpeedLimit  int    `json:"speed_limit"`
	DeviceLimit int    `json:"device_limit"`
}

// ListNodeUsers 返回该节点应当放行的用户。
//
// 过滤条件是这套系统里最要紧的一段 SQL —— 它同时决定了「谁能用」和「谁不能用」：
//   - 订阅必须处于可用状态（active / trialing，宽限期内也算）
//   - 套餐必须授权了该节点所属的资源池（XBD-010）；没划进池的节点不服务任何人（R104）
//   - 池限定了用户组时，用户所在的组必须在名单里（R104，PoolAdmitsUserSQL）
//   - 流量必须没跑超（USE-007 超额停用）
//
// 任何一条漏掉，都会变成免费用或该用用不了，两种都是事故。
//
// 结果只取决于（租户, 节点池）加两条租户设置（设备限制模式与余量），与节点本身
// 无关——除了「这个节点此刻能不能服务」那道门槛，而它与 AuthenticateNode、
// loadServingNodeForPush 的 WHERE 是同一组条件，每个调用方拿到 n 之前都刚在同一条
// 查询里验过，并顺手读出了下发纪元。所以缓存开着时按（租户, 池）缓存、查询不带节点
// 门槛：同池 200 个节点合成一次查询；条目的纪元落后于 n 的纪元就重算（nodecache.go），
// 订阅、配额、套餐、池授权、用户组、设备判定设置的任何已提交改动下一次请求就生效。
// 缓存没开、或 n 不带纪元时照旧带门槛直查。返回的切片只读。
func (s *Service) ListNodeUsers(ctx context.Context, tenantID string, n *ServingNode) ([]ProxyUser, error) {
	// query 是唯一的下发查询。gateNodeID 非空时带上节点门槛（直查路径）；缓存路径
	// 按池共享结果，门槛由调用方的认证保证，见上。epoch 非空时先读下发纪元：先于
	// 名单查询读，名单只会比纪元新，不会更旧。
	query := func(ctx context.Context, gateNodeID string, epoch *int64) ([]ProxyUser, error) {
		users := []ProxyUser{}
		err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
			if epoch != nil {
				if err := tx.QueryRow(ctx, `SELECT `+deliveryEpochSQL).Scan(epoch); err != nil {
					return err
				}
			}
			// 节点必须划进节点池，且订阅的套餐版本绑定了这个池。没划进池的节点
			// 不服务任何订阅（R104，fail closed）：$2 为 NULL 时等式求值为 NULL，
			// EXISTS 为假，列表为空。这与订阅下载、门户预览里的
			// JOIN plan_node_pools 是同一口径；过去这里把无池节点当成对所有有效
			// 订阅开放的公共节点，节点就成了绕过套餐授权的后门。
			//
			// 池限定了用户组时，还要用户所在的组在名单里（R104）。
			poolFilter := `
				AND EXISTS (SELECT 1 FROM plan_node_pools pnp
				             WHERE pnp.tenant_id = s.tenant_id
				               AND pnp.plan_version_id = s.plan_version_id
				               AND pnp.pool_id = $2::uuid)
				AND ` + PoolAdmitsUserSQL("s.tenant_id", "$2::uuid", "s.user_id")

			// 设备限制的判定模式。读设置失败时按 loose 走 ——
			// 配置读不出来不该导致所有人被当成超限踢下线。
			var mode string
			var grace int
			_ = tx.QueryRow(ctx, `
				SELECT COALESCE((SELECT value #>> '{}' FROM system_settings
				                  WHERE tenant_id = $1 AND key = 'device_limit.mode'), 'loose'),
				       COALESCE((SELECT (value #>> '{}')::int FROM system_settings
				                  WHERE tenant_id = $1 AND key = 'device_limit.grace'), 1)`,
				tenantID).Scan(&mode, &grace)
			strict := mode == "strict"

			args := []any{tenantID, n.PoolID, strict, grace}
			nodeGate := ""
			if gateNodeID != "" {
				args = append(args, gateNodeID)
				nodeGate = `
				   AND EXISTS (
				       SELECT 1
				         FROM nodes gate_node
				         JOIN servers gate_server
				           ON gate_server.tenant_id=gate_node.tenant_id
				          AND gate_server.id=gate_node.server_id
				        WHERE gate_node.tenant_id=$1
				          AND gate_node.id=$5::uuid
				          AND gate_node.serving_status IN ('active','draining')
				          AND gate_server.status IN ('ready','draining')
				          AND gate_server.deleted_at IS NULL
				          AND gate_node.node_type IS NOT NULL
				          AND gate_node.server_port BETWEEN 1 AND 65535
				          AND ` + StableProtocolReadySQL("gate_node") + `)`
			}

			rows, err := tx.Query(ctx, `
				SELECT s.node_uid, s.proxy_uuid::text,
				       coalesce(pv.throttle_kbps, 0),
				       -- 管理员在订阅上的覆盖优先于套餐规定
				       coalesce(s.device_limit, pv.max_devices, 0)
				  FROM subscriptions s
				  JOIN plan_versions pv ON pv.id = s.plan_version_id
				 WHERE s.tenant_id = $1
				   -- strict 模式：跨节点去重后仍然超限的，本轮不下发到任何节点。
				   --
				   -- 这是与 loose 唯一的区别。loose 下每个节点各判各的，
				   -- 用户在 N 个节点上能连出 N 倍的设备；strict 则把他整条订阅摘掉，
				   -- 直到在线数掉回限额以内。
				   --
				   -- grace 留出的余量用来吸收 IP 抖动：手机切换网络会让同一台设备
				   -- 短暂占两个 IP，卡得太死会让通勤路上的用户反复掉线。
				   AND ( $3::bool = false
				      OR coalesce(s.device_limit, pv.max_devices, 0) <= 0
				      OR NOT EXISTS (
				           SELECT 1 FROM subscription_online_devices d
				            WHERE d.subscription_id = s.id
				              AND d.device_count > coalesce(s.device_limit, pv.max_devices, 0) + $4 ) )
				   AND s.status IN ('active', 'trialing', 'grace')
				   AND (s.current_period_end IS NULL OR s.current_period_end > now())`+nodeGate+`
				   -- 流量耗尽的订阅不下发到节点（USE-007）：套餐额度用完、
				   -- 而且用户名下的流量包也没有剩余（D-E-1 先扣套餐再扣流量包）
				   AND ( NOT EXISTS (
				           SELECT 1 FROM quota_balances qb
				            WHERE qb.tenant_id = s.tenant_id
				              AND qb.subscription_id = s.id
				              AND qb.metric = 'traffic.bytes'
				              AND qb.remaining IS NOT NULL
				              AND qb.remaining <= 0)
				      OR EXISTS (
				           SELECT 1 FROM traffic_pack_grants g
				            WHERE g.tenant_id = s.tenant_id AND g.user_id = s.user_id
				              AND g.consumed_bytes < g.granted_bytes) )
				`+poolFilter+`
				 ORDER BY s.node_uid`, args...)
			if err != nil {
				return err
			}
			defer rows.Close()

			for rows.Next() {
				var u ProxyUser
				if err := rows.Scan(&u.ID, &u.UUID, &u.SpeedLimit, &u.DeviceLimit); err != nil {
					return err
				}
				users = append(users, u)
			}
			return rows.Err()
		})
		return users, err
	}

	c := s.caches
	if c == nil || !n.epochKnown {
		return query(ctx, n.ID, nil)
	}
	if n.PoolID == nil {
		// 与直查路径的「$2 为 NULL 时列表为空」同一结论，不必为它查库
		return []ProxyUser{}, nil
	}
	want := n.deliveryEpoch
	set, err := c.users.get(ctx, usersCacheKey(tenantID, *n.PoolID), epochFlight(want),
		func(set nodeUserSet) bool { return set.epoch >= want },
		func(ctx context.Context) (nodeUserSet, error) {
			var epoch int64
			users, err := query(ctx, "", &epoch)
			if err != nil {
				return nodeUserSet{}, err
			}
			return nodeUserSet{users: users, version: UserSetVersion(users), epoch: epoch}, nil
		})
	if err != nil {
		return nil, err
	}
	return set.users, nil
}

// NodeUserSet 返回节点该放行的用户与版本（UniProxy 的 ETag）。缓存命中时版本是
// 算好的，handler 先拿它比 If-None-Match，没变就回 304，不必序列化也不必再算哈希。
func (s *Service) NodeUserSet(ctx context.Context, tenantID string, n *ServingNode) ([]ProxyUser, string, error) {
	users, err := s.ListNodeUsers(ctx, tenantID, n)
	if err != nil {
		return nil, "", err
	}
	return users, s.userSetVersionOf(tenantID, n, users), nil
}

//------------------------------------------------------------------------------
// POST /api/v1/server/UniProxy/alive
//------------------------------------------------------------------------------

// ReportAlive 接收在线 IP 上报，用于设备数限制（XBD-008）。
//
// 只存 IP 的哈希：在线设备数是运营需要的指标，原始 IP 不是。
// 存哈希既能去重计数，又不会积累一份可回溯到个人的地址库。
//
// 整份上报一条 SQL 落库：原先每个用户查一次订阅、每个 IP 插一次，一个 30 人
// 在线的节点每分钟就是 60 多条语句。现在按 node_uid 关联订阅、批量 upsert，
// 查不到订阅的 uid 照旧跳过。返回实际写入（新增或刷新）的行数。
func (s *Service) ReportAlive(ctx context.Context, tenantID string, n *ServingNode, raw []byte) (int, error) {
	var alive map[string][]string
	if err := json.Unmarshal(raw, &alive); err != nil {
		return 0, httpx.New(httpx.CodeBadRequest, "上报格式非法")
	}
	uids, hashes := aliveRows(alive)
	if len(uids) == 0 {
		return 0, nil
	}

	// 一次往返、异步提交（遥测，丢最后几百毫秒无妨，下一分钟就补上）。
	//
	// 已有的行只在 last_seen_at 落后超过 aliveRefresh 才刷新：节点每分钟报一次，原先
	// 每次都把全部在线行重写一遍，而 idx_node_alive_recent 含 last_seen_at，每次都是
	// 非 HOT 更新（r3：每 30 分钟 5.7 万行、24 MB WAL）。最短的设备识别窗口是 5 分钟，
	// 时间戳最多落后 2 分钟再加一个上报间隔，仍在窗口内；代价只是离线设备最多早
	// 2 分钟从在线数里掉出去（偏宽松，不会误判超限）。
	count := 0
	b := &pgx.Batch{}
	// 按（订阅, 哈希）排序插入：同一节点两份上报并发时加锁顺序一致，不互等成死锁。
	b.Queue(`
			INSERT INTO node_alive_ips (tenant_id, node_id, subscription_id, ip_hash)
			SELECT s.tenant_id, $2::uuid, s.id, a.ip_hash
			  FROM unnest($3::bigint[], $4::bytea[]) AS a(node_uid, ip_hash)
			  JOIN subscriptions s ON s.tenant_id = $1::uuid AND s.node_uid = a.node_uid
			 ORDER BY s.id, a.ip_hash
			ON CONFLICT (node_id, subscription_id, ip_hash)
			DO UPDATE SET last_seen_at = now()
			 WHERE node_alive_ips.last_seen_at < now() - interval '`+aliveRefresh+`'`,
		tenantID, n.ID, uids, hashes).Exec(func(tag pgconn.CommandTag) error {
		count = int(tag.RowsAffected())
		return nil
	})
	err := s.pool.BatchScoped(ctx, db.Scope{TenantID: tenantID}, db.BatchOptions{AsyncCommit: true}, b)
	return count, err
}

// aliveRefresh 是在线记录 last_seen_at 的刷新粒度，见 ReportAlive。必须远小于最短的
// 设备识别窗口（DeviceWindowMinutes 的 5 分钟）。
const aliveRefresh = "2 minutes"

// aliveRows 把上报摊平成（node_uid, IP 哈希）两列，去重并排序。
//
// 去重是必须的：一条 INSERT … ON CONFLICT DO UPDATE 不能两次碰同一行，而 "01"
// 和 "1" 会解析成同一个 uid、同一 IP 也可能报两遍。uid 不是整数的整条跳过，
// 与逐条处理时一致。
func aliveRows(alive map[string][]string) ([]int64, [][]byte) {
	type row struct {
		uid  int64
		hash [sha256.Size]byte
	}
	seen := make(map[row]struct{})
	rows := make([]row, 0)
	for uidStr, ips := range alive {
		uid, err := strconv.ParseInt(uidStr, 10, 64)
		if err != nil {
			continue
		}
		for _, ip := range ips {
			r := row{uid: uid, hash: sha256.Sum256([]byte(ip))}
			if _, dup := seen[r]; dup {
				continue
			}
			seen[r] = struct{}{}
			rows = append(rows, r)
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].uid != rows[j].uid {
			return rows[i].uid < rows[j].uid
		}
		return bytes.Compare(rows[i].hash[:], rows[j].hash[:]) < 0
	})
	uids := make([]int64, len(rows))
	hashes := make([][]byte, len(rows))
	for i := range rows {
		uids[i] = rows[i].uid
		hashes[i] = append([]byte(nil), rows[i].hash[:]...)
	}
	return uids, hashes
}

type uniProxyResourcePair struct {
	Total uint64 `json:"total"`
	Used  uint64 `json:"used"`
}

type uniProxyStatus struct {
	CPU  float64              `json:"cpu"`
	Mem  uniProxyResourcePair `json:"mem"`
	Swap uniProxyResourcePair `json:"swap"`
	Disk uniProxyResourcePair `json:"disk"`
}

func metricUnit(value, divisor uint64) int64 {
	const maxDBInt = int64(1<<31 - 1)
	converted := value / divisor
	if converted > uint64(maxDBInt) {
		return maxDBInt
	}
	return int64(converted)
}

// ReportRuntimeStatus accepts QNode's UniProxy resource report. It deliberately
// stores only coarse infrastructure metrics and never raw user or address data.
func (s *Service) ReportRuntimeStatus(ctx context.Context, tenantID string, n *ServingNode, raw []byte) error {
	var status uniProxyStatus
	if err := json.Unmarshal(raw, &status); err != nil {
		return httpx.New(httpx.CodeBadRequest, "状态上报格式非法")
	}
	if math.IsNaN(status.CPU) || math.IsInf(status.CPU, 0) || status.CPU < 0 || status.CPU > 100 ||
		status.Mem.Used > status.Mem.Total || status.Swap.Used > status.Swap.Total || status.Disk.Used > status.Disk.Total {
		return httpx.New(httpx.CodeBadRequest, "状态上报数值非法")
	}
	cpuBP := int(math.Round(status.CPU * 100))
	memUsed := metricUnit(status.Mem.Used, 1024*1024)
	memTotal := metricUnit(status.Mem.Total, 1024*1024)
	diskUsed := metricUnit(status.Disk.Used, 1024*1024*1024)
	diskTotal := metricUnit(status.Disk.Total, 1024*1024*1024)

	// 一次往返、异步提交（遥测）。节点行的心跳类列不触发变更通知、是 HOT 更新（00116）；
	// 服务器行按 serverHeartbeatRefresh 节流，与签名心跳同一口径。节点刚在同一请求里
	// 认证过，查不到行只会是认证之后被删，回 404。
	found := false
	b := &pgx.Batch{}
	b.Queue(`
			UPDATE nodes SET last_heartbeat_at=now(), health_score=90
			 WHERE tenant_id=$1 AND id=$2
			RETURNING server_id`, tenantID, n.ID).Query(func(rows pgx.Rows) error {
		for rows.Next() {
			found = true
		}
		return rows.Err()
	})
	b.Queue(`
			INSERT INTO node_metrics
				(tenant_id,node_id,cpu_bp,mem_used_mb,mem_total_mb,disk_used_gb,disk_total_gb)
			SELECT $1::uuid, $2::uuid, $3::int, $4::int, $5::int, $6::int, $7::int
			 WHERE EXISTS (SELECT 1 FROM nodes WHERE tenant_id = $1::uuid AND id = $2::uuid)
			ON CONFLICT (node_id,recorded_at) DO NOTHING`,
		tenantID, n.ID, cpuBP, memUsed, memTotal, diskUsed, diskTotal)
	b.Queue(`
			UPDATE servers SET last_heartbeat_at=now()
			 WHERE tenant_id=$1 AND id=(SELECT server_id FROM nodes WHERE tenant_id=$1 AND id=$2)
			   AND (last_heartbeat_at IS NULL OR last_heartbeat_at < now() - interval '`+serverHeartbeatRefresh+`')`,
		tenantID, n.ID)
	if err := s.pool.BatchScoped(ctx, db.Scope{TenantID: tenantID}, db.BatchOptions{AsyncCommit: true}, b); err != nil {
		return err
	}
	if !found {
		return httpx.New(httpx.CodeNotFound, "节点不存在")
	}
	return nil
}

// DeviceWindowMinutes 是设备识别窗口的可选值（R103），与迁移 00094 的
// app.device_limit_window_minutes 认的是同一组值；窗口本身只在库里算
// （缺行或非法值按 5），Go 侧只用它校验写入与定清理截止。
var DeviceWindowMinutes = [...]int{5, 10, 30, 60}

// staleAliveRetentionMinutes 是在线记录的清理截止：不小于最大窗口再留 10 分钟
// 余量，清理任务无论何时跑都不会删掉仍在某个租户窗口内的行（R103）。读在线记录的
// 地方（在线设备视图、uniproxy 的 strict 判定、后台节点列表）都只看最近一个窗口
// （至多 60 分钟），70 分钟以前的行对任何读数都没有贡献。
const staleAliveRetentionMinutes = 70

// staleAliveBatch 是每个短事务最多删的行数，staleAliveMaxBatches 是一次调用最多跑几批：
// 单批锁的行有上限，一次调用的总量也有上限，积压由下一轮继续清。
const (
	staleAliveBatch      = 5000
	staleAliveMaxBatches = 200
)

// PurgeStaleAlive 清理过期的在线记录。由 aegis-admin 的保留期任务定时调用，幂等。
//
// 分批删：每批一个短事务，先按截止时间挑出至多 staleAliveBatch 行（SKIP LOCKED，
// 正被 alive 上报刷新的行跳过、下一轮再看），再按主键删。被挑中后又被刷新的行
// 由 FOR UPDATE 重新核对截止条件，不会误删；删除后同一 IP 再上报会重新插入。
func (s *Service) PurgeStaleAlive(ctx context.Context, tenantID string) (int64, error) {
	var total int64
	for range staleAliveMaxBatches {
		var n int64
		err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
			ct, err := tx.Exec(ctx, `
				DELETE FROM node_alive_ips a
				 USING (SELECT node_id, subscription_id, ip_hash
				          FROM node_alive_ips
				         WHERE tenant_id = $1
				           AND last_seen_at < now() - make_interval(mins => $2)
				         LIMIT $3
				         FOR UPDATE SKIP LOCKED) d
				 WHERE a.node_id = d.node_id AND a.subscription_id = d.subscription_id
				   AND a.ip_hash = d.ip_hash`,
				tenantID, staleAliveRetentionMinutes, staleAliveBatch)
			if err != nil {
				return err
			}
			n = ct.RowsAffected()
			return nil
		})
		total += n
		if err != nil || n < staleAliveBatch {
			return total, err
		}
	}
	return total, nil
}
