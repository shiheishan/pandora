// [INPUT]: 依赖 platform 的 crypto/db/httpx/audit；读 node_pool_user_groups（00093）与 users.user_group_id
// [OUTPUT]: 对外提供 ServingNode、AuthenticateNode、IssueServerToken、ListNodeUsers、PoolAdmitsUserSQL（池限定用户组的唯一谓词）、DeviceWindowMinutes 与 PurgeStaleAlive（清理截止 70 分钟，不小于最大设备窗口，R103）、ReportAlive / ReportRuntimeStatus
// [POS]: domain/nodefabric 的 UniProxy 兼容数据面：节点鉴权、令牌签发（写审计、记签发时间与签发人、拒绝已退出服务的节点）、用户下发（只下发给套餐绑定了本节点所在池的订阅，无池节点不下发任何人；池限定了用户组时只给名单内组的用户，R104）、在线与运行状态上报；配置组装在 uniproxy_config.go，流量上报与扣量在 uniproxy_traffic.go
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package nodefabric

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"math"
	"strconv"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

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
}

// AuthenticateNode 校验 UniProxy 请求携带的 node_id + token。
//
// token 在库里只有哈希。node_id 走的是节点 UUID，而不是协议里常见的自增整数 ——
// 节点数量有限，用 UUID 不会给节点端造成困扰，却省掉一套自增 ID 映射。
func (s *Service) AuthenticateNode(ctx context.Context, tenantID, nodeID, token, nodeType string) (*ServingNode, error) {
	if nodeID == "" || token == "" {
		return nil, httpx.New(httpx.CodeUnauthorized, "缺少 node_id 或 token")
	}

	var n ServingNode
	var proto []byte
	var isControlNode bool
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT n.id, n.name, coalesce(n.node_type,''), coalesce(n.server_host,''),
			       coalesce(n.server_port,0), n.traffic_rate, n.protocol_config, n.pool_id,
			       n.status, coalesce(n.kernel,'auto'), s.status, n.serving_status,
			       coalesce(s.control_node_id=n.id,false)
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
			tenantID, nodeID, crypto.HashToken(token),
		).Scan(&n.ID, &n.Name, &n.NodeType, &n.ServerHost, &n.ServerPort,
			&n.TrafficRate, &proto, &n.PoolID, &n.Status, &n.Kernel,
			&n.ServerStatus, &n.ServingStatus, &isControlNode)
	})
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
func (s *Service) ListNodeUsers(ctx context.Context, tenantID string, n *ServingNode) ([]ProxyUser, error) {
	users := []ProxyUser{}

	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
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
			   AND (s.current_period_end IS NULL OR s.current_period_end > now())
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
			          AND `+StableProtocolReadySQL("gate_node")+`)
			   -- 流量耗尽的订阅不下发到节点（USE-007）：套餐额度用完、
			   -- 而且用户名下的流量包也没有剩余（D-E-1 先扣套餐再扣流量包）
			   AND ( NOT EXISTS (
			           SELECT 1 FROM quota_balances qb
			            WHERE qb.subscription_id = s.id
			              AND qb.metric = 'traffic.bytes'
			              AND qb.remaining IS NOT NULL
			              AND qb.remaining <= 0)
			      OR EXISTS (
			           SELECT 1 FROM traffic_pack_grants g
			            WHERE g.tenant_id = s.tenant_id AND g.user_id = s.user_id
			              AND g.consumed_bytes < g.granted_bytes) )
			`+poolFilter+`
			 ORDER BY s.node_uid`,
			tenantID, n.PoolID, strict, grace, n.ID)
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

//------------------------------------------------------------------------------
// POST /api/v1/server/UniProxy/alive
//------------------------------------------------------------------------------

// ReportAlive 接收在线 IP 上报，用于设备数限制（XBD-008）。
//
// 只存 IP 的哈希：在线设备数是运营需要的指标，原始 IP 不是。
// 存哈希既能去重计数，又不会积累一份可回溯到个人的地址库。
func (s *Service) ReportAlive(ctx context.Context, tenantID string, n *ServingNode, raw []byte) (int, error) {
	var alive map[string][]string
	if err := json.Unmarshal(raw, &alive); err != nil {
		return 0, httpx.New(httpx.CodeBadRequest, "上报格式非法")
	}

	count := 0
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		for uidStr, ips := range alive {
			uid, err := strconv.ParseInt(uidStr, 10, 64)
			if err != nil {
				continue
			}
			var subID string
			if err := tx.QueryRow(ctx,
				`SELECT id FROM subscriptions WHERE tenant_id=$1 AND node_uid=$2`,
				tenantID, uid).Scan(&subID); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					continue
				}
				return err
			}
			for _, ip := range ips {
				h := sha256.Sum256([]byte(ip))
				if _, err := tx.Exec(ctx, `
					INSERT INTO node_alive_ips (tenant_id, node_id, subscription_id, ip_hash)
					VALUES ($1,$2,$3,$4)
					ON CONFLICT (node_id, subscription_id, ip_hash)
					DO UPDATE SET last_seen_at = now()`,
					tenantID, n.ID, subID, h[:]); err != nil {
					return err
				}
				count++
			}
		}
		return nil
	})
	return count, err
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

	return s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			INSERT INTO node_metrics
				(tenant_id,node_id,cpu_bp,mem_used_mb,mem_total_mb,disk_used_gb,disk_total_gb)
			VALUES ($1,$2,$3,$4,$5,$6,$7)
			ON CONFLICT (node_id,recorded_at) DO NOTHING`,
			tenantID, n.ID, cpuBP, memUsed, memTotal, diskUsed, diskTotal); err != nil {
			return err
		}
		command, err := tx.Exec(ctx, `
			UPDATE nodes SET last_heartbeat_at=now(), health_score=90
			 WHERE tenant_id=$1 AND id=$2`, tenantID, n.ID)
		if err != nil {
			return err
		}
		if command.RowsAffected() != 1 {
			return httpx.New(httpx.CodeNotFound, "节点不存在")
		}
		_, err = tx.Exec(ctx, `
			UPDATE servers SET last_heartbeat_at=now()
			 WHERE tenant_id=$1 AND id=(SELECT server_id FROM nodes WHERE tenant_id=$1 AND id=$2)`,
			tenantID, n.ID)
		return err
	})
}

// DeviceWindowMinutes 是设备识别窗口的可选值（R103），与迁移 00094 的
// app.device_limit_window_minutes 认的是同一组值；窗口本身只在库里算
// （缺行或非法值按 5），Go 侧只用它校验写入与定清理截止。
var DeviceWindowMinutes = [...]int{5, 10, 30, 60}

// staleAliveRetentionMinutes 是在线记录的清理截止：不小于最大窗口再留 10 分钟
// 余量，清理任务无论何时跑都不会删掉仍在某个租户窗口内的行（R103）。
const staleAliveRetentionMinutes = 70

// PurgeStaleAlive 清理过期的在线记录。由定时任务调用，幂等；目前还没有调用方。
func (s *Service) PurgeStaleAlive(ctx context.Context, tenantID string) (int64, error) {
	var n int64
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		ct, err := tx.Exec(ctx,
			`DELETE FROM node_alive_ips WHERE last_seen_at < now() - make_interval(mins => $1)`,
			staleAliveRetentionMinutes)
		if err != nil {
			return err
		}
		n = ct.RowsAffected()
		return nil
	})
	return n, err
}
