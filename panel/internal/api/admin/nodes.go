// [INPUT]: 依赖 domain/nodefabric 的节点、令牌、配置、协议与指标用例（含改状态报错翻译 NodeStatusRefusal），依赖 domain/subscription 的 DeliveryState 与 HeartbeatFreshWindow，依赖 platform 的 audit/db/httpx
// [OUTPUT]: 对外提供 handlers 的 nodeList、nodeIssueToken、serverIssueToken、nodeSetStatus、nodeRevokeIdentity、nodePublishConfig、nodeSetProtocol、nodeProtocolSchemas、nodeIssueServerToken、nodeMetrics、nodeRealityKeypair、nodeDelete；包内 nodeStatusLockSQL、projectNodeLifecycle
// [POS]: api/admin 的节点处理器（NODE / AGT）：从 handlers.go 拆出。节点列表的心跳判定只在 Go 侧做（last_heartbeat_at 以指针扫出），下发状态交给 subscription.DeliveryState，在线统计用租户设备窗口；旧状态接口改退役 / 销毁时先取发布锁再锁节点行并吊销身份；单节点路由在 node_routing.go
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package admin

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/domain/nodefabric"
	"github.com/aegispanel/aegis/internal/domain/subscription"
	"github.com/aegispanel/aegis/internal/platform/audit"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

//------------------------------------------------------------------------------
// 节点（NODE / AGT）
//------------------------------------------------------------------------------

func (h *handlers) nodeList(w http.ResponseWriter, r *http.Request) {
	includeRetired := r.URL.Query().Get("include_retired") == "1"
	// 原先写死 LIMIT 200、total 取本页条数：第 201 个节点起被静默截掉，
	// 前端还以为那就是全部（缺陷 21）。现在可分页，total 是真实总数。
	limit, offset := nodeListPage(r.URL.Query())
	type row struct {
		ID string `json:"id"`
		// NodeNo 是给人看的短编号（101、102…）。UUID 仍然是主键和接口
		// 参数，但后台列表第一列、工单和群里说的都是这个号——
		// 「103 挂了」比念一遍 UUID 快得多，也不会念错。
		NodeNo        int        `json:"node_no"`
		RowVersion    int64      `json:"row_version"`
		Name          string     `json:"name"`
		Status        string     `json:"status"`
		ServingStatus string     `json:"serving_status"`
		ServerID      *string    `json:"server_id"`
		ServerName    *string    `json:"server_name"`
		PoolID        *string    `json:"pool_id"`
		PoolName      *string    `json:"pool_name"`
		AgentVer      *string    `json:"agent_version"`
		Hostname      *string    `json:"hostname"`
		PublicIP      *string    `json:"public_ipv4"`
		CPUCores      *int       `json:"cpu_cores"`
		MemoryMB      *int       `json:"memory_mb"`
		DiskGB        *int       `json:"disk_gb"`
		HealthScore   *int       `json:"health_score"`
		AppliedVer    *int       `json:"applied_config_version"`
		DesiredVer    *int       `json:"desired_config_version"`
		LastBeat      *time.Time `json:"last_heartbeat_at"`
		// Stale 由数据库算：心跳超过 90 秒即视为失联（AGT-004 心跳超时停止新分配）
		Stale bool `json:"stale"`
		// Delivered 回答运营真正关心的那个问题：这个节点现在会不会
		// 出现在用户的订阅里。
		//
		// 它和 Stale 是两回事，不能合并：Stale 是 90 秒的实时视角，
		// 给管理员看「刚刚是不是失联了」；下发用的是 10 分钟窗口，
		// 而且从未心跳过的节点一律不发。只看 Stale 会得出错误结论 ——
		// 一个 stale=true 的节点很可能仍在下发（心跳刚断两分钟），
		// 而一个从未心跳的节点即使刚建好也永远不会下发。
		//
		// 少了这一列，管理员就得自己在脑子里跑一遍下发规则。
		Delivered bool `json:"delivered_to_users"`
		// DeliveryNote 说明「为什么不下发」。没有它，界面上就只是一个
		// 灰点，运营还得来问。
		DeliveryNote string    `json:"delivery_note"`
		Serial       *int      `json:"identity_serial"`
		CreatedAt    time.Time `json:"created_at"`

		// 以下是对外服务配置。表单保存时是全量覆盖，
		// 所以这里必须原样带回，缺一个字段就会在保存时被清掉。
		NodeType              *string         `json:"node_type"`
		ServerHost            *string         `json:"server_host"`
		ServerPort            *int            `json:"server_port"`
		TrafficRate           float64         `json:"traffic_rate"`
		DisplayName           *string         `json:"display_name"`
		CountryCode           *string         `json:"country_code"` // 只进管理端（保留规则 3）
		Kernel                string          `json:"kernel"`
		Protocol              json.RawMessage `json:"protocol_config"`
		ProtocolSchemaVersion int             `json:"protocol_schema_version"`
		ConfigValidatedAt     *time.Time      `json:"config_validated_at"`
		SortOrder             int             `json:"sort_order"`

		// 运营视角的三项。都是聚合出来的，不是 nodes 表上的列。
		//
		// OnlineUsers 是在线订阅数，OnlineIPs 是在线 IP 数：后者明显大于
		// 前者就说明有人在共享账号，这是两个不能合并的指标。
		OnlineUsers int `json:"online_users"`
		OnlineIPs   int `json:"online_ips"`
		// TrafficBytes24h 是近 24 小时上下行合计（原始量），设计表格的「24h 流量」列
		TrafficBytes24h int64 `json:"traffic_bytes_24h"`
		// 所在服务器控制节点最近一条探针（与服务器列表的 cpu_bp 同源）；无探针为 null
		CPUPercent *float64   `json:"cpu_percent"`
		MemPercent *float64   `json:"mem_percent"`
		MetricsAt  *time.Time `json:"metrics_at"`
		// TrafficBytes 是近 30 天上下行合计（原始量，未乘倍率）。
		// 不做全量累计：node_traffic_reports 每节点每分钟一条，
		// 全表 SUM 会随运行时长线性变慢，而列表页每次打开都要算。
		TrafficBytes int64 `json:"traffic_bytes"`
		// GrantedPlans 是通过所属分组授权到这个节点的套餐。
		// 对应 xboard 的「权限组」——回答「谁能用上这个节点」。
		GrantedPlans []string `json:"granted_plans"`
	}
	out := []row{}
	var total int64
	err := h.d.Pool.InTx(r.Context(), db.Scope{TenantID: httpx.TenantIDFrom(r.Context())},
		func(tx pgx.Tx) error {
			// total 用与列表相同的筛选单独数一次：翻过最后一页时列表为空，
			// 窗口函数就取不到总数了
			if err := tx.QueryRow(r.Context(), `
				SELECT count(*) FROM nodes n
				 WHERE n.tenant_id = $1 AND n.status <> 'destroyed'
				   AND ($2::boolean OR n.serving_status <> 'retired')`,
				httpx.TenantIDFrom(r.Context()), includeRetired).Scan(&total); err != nil {
				return err
			}
			// 在线数与流量先各自聚合成一张小表再 JOIN，而不是给每个节点
			// 挂相关子查询：后者会把同一个时间窗扫 200 遍。
			rows, err := tx.Query(r.Context(), `
				WITH alive AS (
				  SELECT node_id,
				         count(DISTINCT subscription_id)::int AS users,
				         count(*)::int AS ips
				    FROM node_alive_ips
				   -- 与在线设备视图同一个窗口（按租户设置，R103），不另写字面量
				   WHERE tenant_id = $1
				     AND last_seen_at > now() - make_interval(mins => app.device_limit_window_minutes($1))
				   GROUP BY node_id
				), traffic AS (
				  SELECT node_id, sum(total_upload + total_download)::bigint AS bytes,
				         coalesce(sum(total_upload + total_download)
				           FILTER (WHERE received_at > now() - interval '24 hours'), 0)::bigint AS bytes_24h
				    FROM node_traffic_reports
				   WHERE tenant_id = $1 AND duplicate_of IS NULL
				     AND received_at > now() - interval '30 days'
				   GROUP BY node_id
				), grants AS (
				  SELECT pnp.pool_id, array_agg(DISTINCT pl.name) AS plans
				    FROM plan_node_pools pnp
				    JOIN plan_versions pv ON pv.tenant_id = pnp.tenant_id
				                         AND pv.id = pnp.plan_version_id
				    JOIN plans pl ON pl.tenant_id = pv.tenant_id AND pl.id = pv.plan_id
				   WHERE pnp.tenant_id = $1
				   GROUP BY pnp.pool_id
				)
				SELECT n.id, n.row_version, n.name, n.status, n.serving_status,
				       n.server_id, s.name, n.pool_id, p.name, n.agent_version, n.hostname,
				       host(n.public_ipv4), n.cpu_cores, n.memory_mb, n.disk_gb,
				       n.health_score, n.applied_config_version, n.desired_config_version,
				       n.last_heartbeat_at,
				       (n.last_heartbeat_at IS NULL
				        OR n.last_heartbeat_at < now() - interval '90 seconds') AS stale,
				       i.serial, n.created_at,
				       n.node_type, n.server_host, n.server_port,
				       n.traffic_rate, n.display_name, n.country_code,
				       coalesce(n.kernel,'auto'), n.protocol_config,
				       n.protocol_schema_version,n.config_validated_at,n.sort_order,n.node_no,
				       coalesce(a.users,0), coalesce(a.ips,0), coalesce(t.bytes,0),
				       coalesce(g.plans, '{}'), coalesce(t.bytes_24h,0),
				       m.cpu_bp / 100.0,
				       CASE WHEN m.mem_total_mb > 0 THEN round(m.mem_used_mb * 100.0 / m.mem_total_mb, 1) END,
				       m.recorded_at
				  FROM nodes n
				  LEFT JOIN node_pools p ON p.id = n.pool_id
				  LEFT JOIN servers s ON s.id=n.server_id AND s.tenant_id=n.tenant_id
				  LEFT JOIN node_identities i ON i.node_id = n.id AND i.status = 'active'
				  LEFT JOIN alive a ON a.node_id = n.id
				  LEFT JOIN traffic t ON t.node_id = n.id
				  LEFT JOIN grants g ON g.pool_id = n.pool_id
				  LEFT JOIN LATERAL (
				        SELECT nm.cpu_bp, nm.mem_used_mb, nm.mem_total_mb, nm.recorded_at
				          FROM node_metrics nm
				         WHERE nm.tenant_id = s.tenant_id AND nm.node_id = s.control_node_id
				         ORDER BY nm.recorded_at DESC LIMIT 1) m ON true
				 WHERE n.tenant_id = $1 AND n.status <> 'destroyed'
				   AND ($2::boolean OR n.serving_status <> 'retired')
				 -- 与「调整排序」同一个顺序（契约后台-07）
				 ORDER BY n.sort_order, n.node_no, n.id
				 LIMIT $3 OFFSET $4`,
				httpx.TenantIDFrom(r.Context()), includeRetired, limit, offset)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var x row
				if err := rows.Scan(&x.ID, &x.RowVersion, &x.Name, &x.Status, &x.ServingStatus,
					&x.ServerID, &x.ServerName, &x.PoolID, &x.PoolName, &x.AgentVer,
					&x.Hostname, &x.PublicIP, &x.CPUCores, &x.MemoryMB, &x.DiskGB,
					&x.HealthScore, &x.AppliedVer, &x.DesiredVer, &x.LastBeat,
					&x.Stale, &x.Serial, &x.CreatedAt,
					&x.NodeType, &x.ServerHost, &x.ServerPort,
					&x.TrafficRate, &x.DisplayName, &x.CountryCode, &x.Kernel, &x.Protocol,
					&x.ProtocolSchemaVersion, &x.ConfigValidatedAt, &x.SortOrder, &x.NodeNo,
					&x.OnlineUsers, &x.OnlineIPs, &x.TrafficBytes,
					&x.GrantedPlans, &x.TrafficBytes24h, &x.CPUPercent, &x.MemPercent, &x.MetricsAt); err != nil {
					return err
				}
				// 心跳的两个事实由 Go 侧从 LastBeat 推出，不在 SQL 里算。
				//
				// 一开始是写成 SQL 布尔列的，结果 last_heartbeat_at 为 NULL 时
				// (NULL >= …) 求值成 NULL 而不是 false，扫进 bool 直接把整个
				// 节点列表打成 500。可以用 COALESCE 补，但那是在给一个本来
				// 就不该存在的三值逻辑打补丁 —— LastBeat 是 *time.Time，
				// 「从未心跳」本来就由 nil 表达得清清楚楚。
				everSeen := x.LastBeat != nil
				beatFresh := everSeen &&
					time.Since(*x.LastBeat) < subscription.HeartbeatFreshWindow
				x.Delivered, x.DeliveryNote = subscription.DeliveryState(
					x.ServingStatus, x.PoolID != nil, everSeen, beatFresh)
				x.Protocol = nodefabric.RedactProtocolConfig(x.Protocol)
				out = append(out, x)
			}
			return rows.Err()
		})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, httpx.Internal(err))
		return
	}
	httpx.OK(w, map[string]any{"nodes": out, "total": total})
}

// nodeListPage 解析节点列表的分页参数。默认 500 条、最多 1000 条：前端按
// 设计在本地做筛选与搜索，一页要装得下常规规模的全部节点。非法值回默认。
func nodeListPage(q url.Values) (limit, offset int) {
	limit, offset = 500, 0
	if v, err := strconv.Atoi(strings.TrimSpace(q.Get("limit"))); err == nil && v >= 1 && v <= 1000 {
		limit = v
	}
	if v, err := strconv.Atoi(strings.TrimSpace(q.Get("offset"))); err == nil && v > 0 {
		offset = v
	}
	return limit, offset
}

// panelURLFrom 推导机器该回连的面板地址。
//
// 后台通常挂在反代后面，r.TLS 和 r.Host 都反映不出用户实际访问的入口，
// 所以优先信反代给的 X-Forwarded-*。推错了的后果很直接：命令复制到
// 机器上跑，连的是内网地址，接不进来。
type issueTokenReq struct {
	NodeName   string `json:"node_name"`
	PoolID     string `json:"pool_id"`
	ServerID   string `json:"server_id"`
	TTLMinutes int    `json:"ttl_minutes"`
}

func (h *handlers) nodeIssueToken(w http.ResponseWriter, r *http.Request) {
	var req issueTokenReq
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	panelURL, err := h.d.Cfg.CanonicalPublicOrigin()
	if err != nil {
		httpx.Fail(w, r, h.d.Log, httpx.Internal(err))
		return
	}
	out, err := h.d.Node.IssueBootstrapToken(r.Context(), httpx.TenantIDFrom(r.Context()),
		nodefabric.IssueTokenInput{
			ActorID:    httpx.PrincipalFrom(r.Context()).UserID,
			NodeName:   req.NodeName,
			PoolID:     req.PoolID,
			ServerID:   req.ServerID,
			PanelURL:   panelURL,
			TTLMinutes: req.TTLMinutes,
		})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.Created(w, out)
}

// serverIssueToken 给一台已建好的服务器签发接入令牌。
//
// 和 nodeIssueToken 是同一件事，区别只在 server_id 从路径来、节点名默认
// 取服务器名。单独开一个入口是为了让「在服务器详情里拿接入命令」这个
// 动作不需要前端自己拼节点名——那正是容易填错、导致同一台机器接出两条
// 记录的地方。
func (h *handlers) serverIssueToken(w http.ResponseWriter, r *http.Request) {
	serverID := chi.URLParam(r, "id")
	var req issueTokenReq
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	tenantID := httpx.TenantIDFrom(r.Context())
	panelURL, err := h.d.Cfg.CanonicalPublicOrigin()
	if err != nil {
		httpx.Fail(w, r, h.d.Log, httpx.Internal(err))
		return
	}
	nodeName := strings.TrimSpace(req.NodeName)
	if nodeName == "" {
		srv, err := h.d.Node.GetServer(r.Context(), tenantID, serverID)
		if err != nil {
			httpx.Fail(w, r, h.d.Log, err)
			return
		}
		nodeName = srv.Name
	}
	out, err := h.d.Node.IssueBootstrapToken(r.Context(), tenantID,
		nodefabric.IssueTokenInput{
			ActorID:    httpx.PrincipalFrom(r.Context()).UserID,
			NodeName:   nodeName,
			PoolID:     req.PoolID,
			ServerID:   serverID,
			PanelURL:   panelURL,
			TTLMinutes: req.TTLMinutes,
		})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.Created(w, out)
}

type nodeStatusReq struct {
	RowVersion int64  `json:"row_version"`
	Status     string `json:"status"`
	Reason     string `json:"reason"`
}

func nodeStatusLockSQL() string {
	return `SELECT status,row_version,
		COALESCE(node_type IS NOT NULL
		AND server_port BETWEEN 1 AND 65535
		AND ` + nodefabric.StableProtocolReadySQL("") + `, false)
	FROM nodes WHERE tenant_id=$1 AND id=$2 FOR UPDATE`
}

// nodeSetStatus 推进节点状态。合法性由数据库的 node_transitions 表强制 ——
// 这里不重复实现一遍状态机，避免两处规则漂移（NODE-010）。
func (h *handlers) nodeSetStatus(w http.ResponseWriter, r *http.Request) {
	var req nodeStatusReq
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	id := chi.URLParam(r, "id")
	actor := httpx.PrincipalFrom(r.Context()).UserID
	tenantID := httpx.TenantIDFrom(r.Context())
	terminal := req.Status == "retired" || req.Status == "destroyed"

	err := h.d.Pool.InTx(r.Context(), db.Scope{TenantID: tenantID, ActorID: actor},
		func(tx pgx.Tx) error {
			if terminal {
				if _, err := tx.Exec(r.Context(),
					`SELECT pg_catalog.pg_advisory_xact_lock(pg_catalog.hashtextextended($1, 0))`,
					"node-config-release/"+tenantID); err != nil {
					return err
				}
			}
			var before string
			var currentVersion int64
			var protocolReady bool
			if err := tx.QueryRow(r.Context(), nodeStatusLockSQL(),
				tenantID, id).Scan(&before, &currentVersion, &protocolReady); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return httpx.New(httpx.CodeNotFound, "节点不存在")
				}
				return err
			}
			if req.RowVersion <= 0 {
				return httpx.Invalid(map[string]string{"row_version": "必须提供正整数版本号"})
			}
			if currentVersion != req.RowVersion {
				return &httpx.Error{Code: httpx.CodeConflict, Message: "节点已被其他管理员修改，请刷新后重试",
					Fields: map[string]string{"row_version": fmt.Sprintf("current=%d", currentVersion)}}
			}
			servingStatus, serverStatus := projectNodeLifecycle(req.Status, protocolReady)
			if _, err := tx.Exec(r.Context(),
				`UPDATE nodes SET status=$3,serving_status=$4,
				 desired_config_version=CASE WHEN $4='retired' THEN NULL ELSE desired_config_version END,
				 row_version=row_version+1
				 WHERE tenant_id=$1 AND id=$2 AND row_version=$5`,
				tenantID, id, req.Status, servingStatus, currentVersion); err != nil {
				// 违反状态机的跳转由触发器抛 check_violation；约束的英文原句不透给页面
				if db.IsCheckViolation(err) {
					return nodefabric.NodeStatusRefusal(err)
				}
				return err
			}
			// Server 与 Node 的状态词不同；第二条更新单独写入正确的宿主状态。
			if _, err := tx.Exec(r.Context(), `
				UPDATE servers SET status=$3, status_reason=nullif($4,''),
				       entered_status_at=now(), row_version=row_version+1,
				       retired_at=CASE WHEN $3='retired' THEN now() ELSE retired_at END
				 WHERE tenant_id=$1 AND id=(SELECT server_id FROM nodes WHERE tenant_id=$1 AND id=$2)
				   AND control_node_id=$2 AND deleted_at IS NULL`,
				tenantID, id, serverStatus, req.Reason); err != nil {
				return err
			}
			if terminal {
				if _, err := tx.Exec(r.Context(), `UPDATE node_identities
					SET status='revoked',revoked_at=now(),revoked_reason='节点已退役'
					WHERE tenant_id=$1 AND node_id=$2 AND status='active'`, tenantID, id); err != nil {
					return err
				}
			}
			return audit.Write(r.Context(), tx, tenantID, audit.Entry{
				ActorKind: "admin", ActorID: &actor,
				Action: "node.status_change", ResourceType: "node", ResourceID: &id,
				APIDomain: "admin", Outcome: "success",
				RequestID:    httpx.RequestIDFrom(r.Context()),
				BeforeDigest: map[string]any{"status": before},
				AfterDigest:  map[string]any{"status": req.Status, "reason": req.Reason},
			})
		})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, map[string]any{"ok": true, "row_version": req.RowVersion + 1})
}

// projectNodeLifecycle 是旧状态接口对 nodefabric.ProjectNodeLifecycle 的调用点：
// 映射只有一份，与一步上线（POST v1/nodes/{id}/activate）共用。
func projectNodeLifecycle(nodeStatus string, protocolReady bool) (servingStatus, serverStatus string) {
	return nodefabric.ProjectNodeLifecycle(nodeStatus, protocolReady)
}

// nodeRevokeIdentity 吊销节点身份（NODE-014）。
// 吊销后该节点的 Agent 下一次请求就会被拒，必须重新引导。
func (h *handlers) nodeRevokeIdentity(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	actor := httpx.PrincipalFrom(r.Context()).UserID
	tenantID := httpx.TenantIDFrom(r.Context())

	err := h.d.Pool.InTx(r.Context(), db.Scope{TenantID: tenantID, ActorID: actor},
		func(tx pgx.Tx) error {
			ct, err := tx.Exec(r.Context(), `
				UPDATE node_identities
				   SET status='revoked', revoked_at=now(), revoked_reason='管理员手工吊销'
				 WHERE tenant_id=$1 AND node_id=$2 AND status='active'`, tenantID, id)
			if err != nil {
				return err
			}
			if ct.RowsAffected() == 0 {
				return httpx.New(httpx.CodeNotFound, "该节点没有有效身份")
			}
			return audit.Write(r.Context(), tx, tenantID, audit.Entry{
				ActorKind: "admin", ActorID: &actor,
				Action: "node.identity.revoke", ResourceType: "node", ResourceID: &id,
				APIDomain: "admin", Outcome: "success",
				RequestID: httpx.RequestIDFrom(r.Context()),
			})
		})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, map[string]any{"ok": true})
}

type publishCfgReq struct {
	Scope    string          `json:"scope"`     // global / pool / node
	ScopeRef string          `json:"scope_ref"` // pool/node 时必填
	Payload  json.RawMessage `json:"payload"`
}

// nodePublishConfig 发布一层配置并签名（AGT-007）。
func (h *handlers) nodePublishConfig(w http.ResponseWriter, r *http.Request) {
	var req publishCfgReq
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	if req.Scope != "global" && req.Scope != "pool" && req.Scope != "node" {
		httpx.Fail(w, r, h.d.Log, httpx.Invalid(map[string]string{"scope": "只支持 global/pool/node"}))
		return
	}
	if req.Scope != "global" && req.ScopeRef == "" {
		httpx.Fail(w, r, h.d.Log, httpx.Invalid(map[string]string{"scope_ref": "该层级必须指定对象"}))
		return
	}
	var probe map[string]any
	if err := json.Unmarshal(req.Payload, &probe); err != nil {
		httpx.Fail(w, r, h.d.Log, httpx.Invalid(map[string]string{"payload": "必须是 JSON 对象"}))
		return
	}

	out, err := h.d.Node.PublishConfig(r.Context(), httpx.TenantIDFrom(r.Context()),
		nodefabric.PublishInput{
			ActorID:  httpx.PrincipalFrom(r.Context()).UserID,
			Scope:    req.Scope,
			ScopeRef: req.ScopeRef,
			Payload:  req.Payload,
		})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.Created(w, out)
}

type nodeProtoReq struct {
	RowVersion  int64           `json:"row_version"`
	NodeType    string          `json:"node_type"`
	ServerHost  string          `json:"server_host"`
	ServerPort  int             `json:"server_port"`
	TrafficRate float64         `json:"traffic_rate"`
	DisplayName string          `json:"display_name"`
	Kernel      string          `json:"kernel"`
	Protocol    json.RawMessage `json:"protocol_config"`
}

// nodeSetProtocol 配置节点的对外服务参数（UniProxy 下发给节点端的内容）。
func (h *handlers) nodeSetProtocol(w http.ResponseWriter, r *http.Request) {
	var req nodeProtoReq
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	if req.TrafficRate <= 0 {
		req.TrafficRate = 1
	}
	if req.Kernel == "" {
		req.Kernel = "auto"
	}
	if len(req.Protocol) == 0 {
		req.Protocol = json.RawMessage(`{}`)
	}
	id := chi.URLParam(r, "id")
	tenantID := httpx.TenantIDFrom(r.Context())
	actor := httpx.PrincipalFrom(r.Context()).UserID
	req.NodeType = nodefabric.CanonicalNodeType(req.NodeType)
	// Keep the compatibility endpoint, but route it through the same stable
	// schema validation, optimistic lock and audit path as PATCH /nodes/{id}.
	out, err := h.d.Node.PatchAdminNode(r.Context(), tenantID, id, nodefabric.PatchAdminNodeInput{
		ActorID: actor, RowVersion: req.RowVersion, NodeType: &req.NodeType,
		ServerHost: &req.ServerHost, ServerPort: &req.ServerPort, Kernel: &req.Kernel,
		TrafficRate: &req.TrafficRate, DisplayName: &req.DisplayName,
		ProtocolConfig: &req.Protocol,
	})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, out)
}

func (h *handlers) nodeProtocolSchemas(w http.ResponseWriter, r *http.Request) {
	httpx.OK(w, map[string]any{"schemas": nodefabric.ProtocolSchemas()})
}

// nodeIssueServerToken 签发 UniProxy 接入令牌，明文只返回一次。
func (h *handlers) nodeIssueServerToken(w http.ResponseWriter, r *http.Request) {
	nodeID := chi.URLParam(r, "id")
	tok, nodeType, err := h.d.Node.IssueServerToken(r.Context(), httpx.TenantIDFrom(r.Context()),
		httpx.PrincipalFrom(r.Context()).UserID, nodeID)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	if nodeType == "" {
		nodeType = "vless"
	}
	panelURL, err := h.d.Cfg.CanonicalPublicOrigin()
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	// 一并给出可直接粘贴的安装命令。令牌只显示这一次；命令本身通过
	// 终端读取令牌，不把运行凭据嵌进 argv 或 shell history。
	install := nodefabric.RenderLegacyInstallCommand(panelURL, nodeID, nodeType)
	httpx.Created(w, map[string]any{
		"token":           tok,
		"node_type":       nodeType,
		"panel_url":       panelURL,
		"install_command": install,
		"hint":            "该令牌只显示一次。重新签发会立即作废旧令牌——正在运行的节点会拉配置失败（401）直到用新令牌重装，请确认后再执行安装命令。",
	})
}

func nullStrAdmin(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// nodeMetrics 返回节点探针曲线。
func (h *handlers) nodeMetrics(w http.ResponseWriter, r *http.Request) {
	m, err := h.d.Node.FetchMetrics(r.Context(), httpx.TenantIDFrom(r.Context()),
		chi.URLParam(r, "id"), atoiDefault(r.URL.Query().Get("minutes"), 60))
	if err != nil {
		httpx.Fail(w, r, h.d.Log, httpx.Internal(err))
		return
	}
	httpx.OK(w, m)
}

// nodeRealityKeypair 生成一对 REALITY 用的 x25519 密钥。
//
// 放在服务端而不是让管理员自己跑 xray x25519：那条路要求他能登上某台机器，
// 而且生成的私钥会经过他的剪贴板和终端历史。这里私钥只在这一次响应里出现，
// 保存后就只以密文形式留在协议配置里，读接口不回显。
func (h *handlers) nodeRealityKeypair(w http.ResponseWriter, r *http.Request) {
	priv, pub, err := nodefabric.GenerateRealityKeypair()
	if err != nil {
		httpx.Fail(w, r, h.d.Log, httpx.Internal(err))
		return
	}
	// short id 一并给出：它不是密钥，但少了它客户端连不上，
	// 而「自己想一个十六进制串」是很容易填错的一步。
	var sid [4]byte
	if _, err := rand.Read(sid[:]); err != nil {
		httpx.Fail(w, r, h.d.Log, httpx.Internal(err))
		return
	}
	httpx.OK(w, map[string]any{
		"private_key": priv,
		"public_key":  pub,
		"short_id":    hex.EncodeToString(sid[:]),
		"hint":        "私钥只在这一次返回，保存后无法再查看",
	})
}

type nodeDeleteReq struct {
	RowVersion int64  `json:"row_version"`
	Reason     string `json:"reason"`
}

// nodeDelete 删除一个已下线的逻辑节点。
func (h *handlers) nodeDelete(w http.ResponseWriter, r *http.Request) {
	var req nodeDeleteReq
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	p := httpx.PrincipalFrom(r.Context())
	if err := h.d.Node.DeleteNode(r.Context(), httpx.TenantIDFrom(r.Context()),
		nodefabric.DeleteNodeInput{
			ID: chi.URLParam(r, "id"), RowVersion: req.RowVersion,
			ActorID: p.UserID, Reason: req.Reason,
		}); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, map[string]any{"deleted": true})
}
