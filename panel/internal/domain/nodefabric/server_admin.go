package nodefabric

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/audit"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

var serverStatuses = map[string]bool{
	"draft": true, "ready": true, "draining": true, "maintenance": true,
	"unhealthy": true, "quarantined": true, "retired": true,
}

func ValidServerStatus(status string) bool { return serverStatuses[status] }

type Server struct {
	ID              string     `json:"id"`
	Name            string     `json:"name"`
	Status          string     `json:"status"`
	StatusReason    *string    `json:"status_reason"`
	RowVersion      int64      `json:"row_version"`
	Region          *string    `json:"region"`
	Hostname        *string    `json:"hostname"`
	PublicIPv4      *string    `json:"public_ipv4"`
	PublicIPv6      *string    `json:"public_ipv6"`
	PrivateIPv4     *string    `json:"private_ipv4"`
	Architecture    *string    `json:"architecture"`
	OSName          *string    `json:"os_name"`
	AgentVersion    *string    `json:"agent_version"`
	LastHeartbeatAt *time.Time `json:"last_heartbeat_at"`
	HeartbeatOnline bool       `json:"heartbeat_online"`
	CPUCores        *int       `json:"cpu_cores"`
	MemoryMB        *int       `json:"memory_mb"`
	DiskGB          *int       `json:"disk_gb"`
	CapacityNodes   int        `json:"capacity_nodes"`
	Notes           *string    `json:"notes"`
	ControlNodeID   *string    `json:"control_node_id"`
	NodeCount       int        `json:"node_count"`
	ActiveNodeCount int        `json:"active_node_count"`
	// ServingNodeCount 是真正在给用户用的节点数：标了在役，而且上报过心跳。
	//
	// 和 ActiveNodeCount 分开报，是因为这两个数会不一样，而不一样的那部分
	// 恰恰是最需要有人看见的。订阅下发要求节点至少上报过一次心跳（见
	// subscription.DeliveryState），所以一个建好了但没装 agent 的节点，
	// serving_status 是 active、下发时却会被跳过。只报 ActiveNodeCount
	// 等于面板承诺了订阅引擎并不会兑现的容量。
	ServingNodeCount int `json:"serving_node_count"`
	// NeverSeenNodeCount 是标了在役但一次心跳都没上报过的节点数。
	// 这几乎总是同一个原因：在面板里建了节点，但那台机器上没装 agent。
	NeverSeenNodeCount int       `json:"never_seen_node_count"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`

	// 控制节点最近一次探针。cpu_cores/memory_mb/disk_gb 是机器规格，
	// 这里是实际占用——运维要判断的是「这台还吃得下节点吗」，
	// 只看规格答不了。全为 nil 表示这台还没上报过探针。
	CPUBasisPoints *int       `json:"cpu_bp"`
	MemUsedMB      *int       `json:"mem_used_mb"`
	MemTotalMB     *int       `json:"mem_total_mb"`
	DiskUsedGB     *int       `json:"disk_used_gb"`
	DiskTotalGB    *int       `json:"disk_total_gb"`
	MetricsAt      *time.Time `json:"metrics_at"`
}

type ListServersInput struct {
	Status string
	Query  string
}

type CreateServerInput struct {
	ActorID       string `json:"-"`
	Name          string `json:"name"`
	Region        string `json:"region"`
	Hostname      string `json:"hostname"`
	PublicIPv4    string `json:"public_ipv4"`
	PublicIPv6    string `json:"public_ipv6"`
	PrivateIPv4   string `json:"private_ipv4"`
	Architecture  string `json:"architecture"`
	OSName        string `json:"os_name"`
	CapacityNodes int    `json:"capacity_nodes"`
	Notes         string `json:"notes"`
}

type PatchServerInput struct {
	ActorID       string  `json:"-"`
	RowVersion    int64   `json:"row_version"`
	Name          *string `json:"name"`
	Region        *string `json:"region"`
	Hostname      *string `json:"hostname"`
	PublicIPv4    *string `json:"public_ipv4"`
	PublicIPv6    *string `json:"public_ipv6"`
	PrivateIPv4   *string `json:"private_ipv4"`
	Architecture  *string `json:"architecture"`
	OSName        *string `json:"os_name"`
	CapacityNodes *int    `json:"capacity_nodes"`
	Notes         *string `json:"notes"`
}

type SetServerStatusInput struct {
	ActorID    string `json:"-"`
	Status     string `json:"status"`
	Reason     string `json:"reason"`
	RowVersion int64  `json:"row_version"`
}

var serverStatusTransitions = map[string]map[string]bool{
	"draft":       {"ready": true, "maintenance": true, "retired": true},
	"ready":       {"draining": true, "unhealthy": true, "quarantined": true},
	"draining":    {"ready": true, "maintenance": true, "retired": true},
	"maintenance": {"ready": true, "retired": true},
	"unhealthy":   {"draining": true, "maintenance": true, "quarantined": true, "retired": true},
	"quarantined": {"draining": true, "maintenance": true, "retired": true},
	"retired":     {},
}

func ValidServerStatusTransition(from, to string) bool {
	return serverStatusTransitions[from][to]
}

type ServerNode struct {
	ID                string     `json:"id"`
	Name              string     `json:"name"`
	Status            string     `json:"status"`
	ServingStatus     string     `json:"serving_status"`
	NodeType          *string    `json:"node_type"`
	DisplayName       *string    `json:"display_name"`
	ServerHost        *string    `json:"server_host"`
	ServerPort        *int       `json:"server_port"`
	Kernel            string     `json:"kernel"`
	TrafficRate       float64    `json:"traffic_rate"`
	ConfigValidatedAt *time.Time `json:"config_validated_at"`
	LastHeartbeatAt   *time.Time `json:"last_heartbeat_at"`
	CreatedAt         time.Time  `json:"created_at"`
}

const serverSelect = `
	SELECT s.id, s.name, s.status, s.status_reason, s.row_version,
	       s.region, s.hostname, host(s.public_ipv4), host(s.public_ipv6),
	       host(s.private_ipv4), s.architecture, s.os_name, s.agent_version,
	       s.last_heartbeat_at,
	       coalesce(s.status='ready' AND s.deleted_at IS NULL AND
	        s.last_heartbeat_at >= now() - interval '90 seconds', false) AS heartbeat_online,
	       s.cpu_cores, s.memory_mb, s.disk_gb, s.capacity_nodes, s.notes,
	       s.control_node_id,
	       (SELECT count(*)::int FROM nodes n
	         WHERE n.tenant_id=s.tenant_id AND n.server_id=s.id),
	       (SELECT count(*)::int FROM nodes n
	         WHERE n.tenant_id=s.tenant_id AND n.server_id=s.id
	           AND n.serving_status='active'),
	       (SELECT count(*)::int FROM nodes n
	         WHERE n.tenant_id=s.tenant_id AND n.server_id=s.id
	           AND n.serving_status='active' AND n.last_heartbeat_at IS NOT NULL),
	       (SELECT count(*)::int FROM nodes n
	         WHERE n.tenant_id=s.tenant_id AND n.server_id=s.id
	           AND n.serving_status='active' AND n.last_heartbeat_at IS NULL),
	       s.created_at, s.updated_at,
	       m.cpu_bp, m.mem_used_mb, m.mem_total_mb, m.disk_used_gb, m.disk_total_gb,
	       m.recorded_at
	  FROM servers s
	  LEFT JOIN LATERAL (
	    SELECT nm.cpu_bp, nm.mem_used_mb, nm.mem_total_mb,
	           nm.disk_used_gb, nm.disk_total_gb, nm.recorded_at
	      FROM node_metrics nm
	     WHERE nm.tenant_id = s.tenant_id AND nm.node_id = s.control_node_id
	     ORDER BY nm.recorded_at DESC
	     LIMIT 1
	  ) m ON true`

func scanServer(row pgx.Row) (*Server, error) {
	var out Server
	err := row.Scan(
		&out.ID, &out.Name, &out.Status, &out.StatusReason, &out.RowVersion,
		&out.Region, &out.Hostname, &out.PublicIPv4, &out.PublicIPv6,
		&out.PrivateIPv4, &out.Architecture, &out.OSName, &out.AgentVersion,
		&out.LastHeartbeatAt, &out.HeartbeatOnline, &out.CPUCores, &out.MemoryMB,
		&out.DiskGB, &out.CapacityNodes, &out.Notes, &out.ControlNodeID,
		&out.NodeCount, &out.ActiveNodeCount,
		&out.ServingNodeCount, &out.NeverSeenNodeCount,
		&out.CreatedAt, &out.UpdatedAt,
		&out.CPUBasisPoints, &out.MemUsedMB, &out.MemTotalMB,
		&out.DiskUsedGB, &out.DiskTotalGB, &out.MetricsAt,
	)
	return &out, err
}

func (s *Service) ListServers(ctx context.Context, tenantID string, in ListServersInput) ([]Server, error) {
	out := []Server{}
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, serverSelect+`
			 WHERE s.tenant_id=$1 AND s.deleted_at IS NULL
			   AND ($2='' OR s.status=$2)
			   AND ($3='' OR s.name ILIKE '%'||$3||'%' OR
			        coalesce(s.hostname,'') ILIKE '%'||$3||'%')
			 ORDER BY s.created_at DESC, s.id DESC LIMIT 500`,
			tenantID, in.Status, strings.TrimSpace(in.Query))
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			item, err := scanServer(rows)
			if err != nil {
				return err
			}
			out = append(out, *item)
		}
		return rows.Err()
	})
	return out, err
}

func (s *Service) GetServer(ctx context.Context, tenantID, id string) (*Server, error) {
	var out *Server
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		item, err := scanServer(tx.QueryRow(ctx, serverSelect+`
			 WHERE s.tenant_id=$1 AND s.id=$2::uuid AND s.deleted_at IS NULL`, tenantID, id))
		out = item
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, httpx.NotFoundOrForbidden()
	}
	return out, err
}

func validateServerFields(name, status, publicIPv4, publicIPv6, privateIPv4 string,
	cpu, memory, disk, capacity int) error {
	fields := map[string]string{}
	if strings.TrimSpace(name) == "" || len([]rune(strings.TrimSpace(name))) > 120 {
		fields["name"] = "名称必须为 1 到 120 个字符"
	}
	if status != "" && !ValidServerStatus(status) {
		fields["status"] = "不支持的服务器状态"
	}
	validateIP := func(key, value string, want4 bool) {
		if value == "" {
			return
		}
		ip := net.ParseIP(value)
		if ip == nil || (want4 && ip.To4() == nil) || (!want4 && ip.To4() != nil) {
			fields[key] = "IP 地址格式不正确"
		}
	}
	validateIP("public_ipv4", publicIPv4, true)
	validateIP("public_ipv6", publicIPv6, false)
	validateIP("private_ipv4", privateIPv4, true)
	for key, value := range map[string]int{
		"cpu_cores": cpu, "memory_mb": memory, "disk_gb": disk, "capacity_nodes": capacity,
	} {
		if value < 0 {
			fields[key] = "不能小于 0"
		}
	}
	if capacity == 0 {
		fields["capacity_nodes"] = "必须大于 0"
	}
	if len(fields) > 0 {
		return httpx.Invalid(fields)
	}
	return nil
}

func ValidateCreateServerInput(in *CreateServerInput) error {
	in.Name = strings.TrimSpace(in.Name)
	if in.CapacityNodes == 0 {
		in.CapacityNodes = 32
	}
	return validateServerFields(in.Name, "draft", in.PublicIPv4, in.PublicIPv6,
		in.PrivateIPv4, 0, 0, 0, in.CapacityNodes)
}

func ValidatePatchServerInput(in PatchServerInput) error {
	if in.RowVersion <= 0 {
		return httpx.Invalid(map[string]string{"row_version": "必须提供正整数版本号"})
	}
	name := "valid"
	if in.Name != nil {
		name = strings.TrimSpace(*in.Name)
	}
	str := func(v *string) string {
		if v == nil {
			return ""
		}
		return strings.TrimSpace(*v)
	}
	capacity := 1
	if in.CapacityNodes != nil {
		capacity = *in.CapacityNodes
	}
	return validateServerFields(name, "", str(in.PublicIPv4), str(in.PublicIPv6),
		str(in.PrivateIPv4), 0, 0, 0, capacity)
}

func (s *Service) CreateServer(ctx context.Context, tenantID string, in CreateServerInput) (*Server, error) {
	if err := ValidateCreateServerInput(&in); err != nil {
		return nil, err
	}
	var id string
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: in.ActorID}, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `
			INSERT INTO servers (
			  tenant_id,name,status,region,hostname,public_ipv4,public_ipv6,
			  private_ipv4,architecture,os_name,capacity_nodes,notes,entered_status_at)
			VALUES ($1,$2,'draft',nullif($3,''),nullif($4,''),nullif($5,'')::inet,
			  nullif($6,'')::inet,nullif($7,'')::inet,nullif($8,''),nullif($9,''),
			  $10,nullif($11,''),now())
			RETURNING id`, tenantID, in.Name, in.Region, in.Hostname, in.PublicIPv4,
			in.PublicIPv6, in.PrivateIPv4, in.Architecture, in.OSName,
			in.CapacityNodes, in.Notes).Scan(&id)
		if db.IsUniqueViolation(err) {
			return httpx.New(httpx.CodeConflict, "服务器名称已存在")
		}
		if err != nil {
			return err
		}
		return audit.Write(ctx, tx, tenantID, audit.Entry{
			ActorKind: "admin", ActorID: &in.ActorID, Action: "server.create",
			ResourceType: "server", ResourceID: &id, APIDomain: "admin",
			RequestID:   httpx.RequestIDFrom(ctx),
			AfterDigest: map[string]any{"name": in.Name, "status": "draft"},
		})
	})
	if err != nil {
		return nil, err
	}
	return s.GetServer(ctx, tenantID, id)
}

func (s *Service) PatchServer(ctx context.Context, tenantID, id string, in PatchServerInput) (*Server, error) {
	if err := ValidatePatchServerInput(in); err != nil {
		return nil, err
	}
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: in.ActorID}, func(tx pgx.Tx) error {
		var beforeName, beforeStatus string
		var currentVersion int64
		var currentNodes int
		if err := tx.QueryRow(ctx, `SELECT s.name,s.status,s.row_version
			FROM servers s
			WHERE s.tenant_id=$1 AND s.id=$2::uuid AND s.deleted_at IS NULL
			FOR UPDATE`, tenantID, id).
			Scan(&beforeName, &beforeStatus, &currentVersion); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return httpx.NotFoundOrForbidden()
			}
			return err
		}
		// Count only after the Server row lock is held. Node create/copy/move use
		// the same lock, so capacity changes and allocations serialize correctly.
		if err := tx.QueryRow(ctx, `SELECT count(*)::int FROM nodes
			WHERE tenant_id=$1 AND server_id=$2::uuid AND serving_status<>'retired'`,
			tenantID, id).Scan(&currentNodes); err != nil {
			return err
		}
		if currentVersion != in.RowVersion {
			return &httpx.Error{Code: httpx.CodeConflict, Message: "服务器已被其他管理员修改，请刷新后重试",
				Fields: map[string]string{"row_version": fmt.Sprintf("current=%d", currentVersion)}}
		}
		if in.CapacityNodes != nil && *in.CapacityNodes < currentNodes {
			return &httpx.Error{Code: httpx.CodeConflict, Message: "服务器容量不能低于当前节点占用",
				Fields: map[string]string{"capacity_nodes": fmt.Sprintf("minimum=%d", currentNodes)}}
		}
		ct, err := tx.Exec(ctx, `
			UPDATE servers SET
			 name=coalesce(nullif($4,''),name),
			 region=CASE WHEN $5::text IS NULL THEN region ELSE nullif($5,'') END,
			 hostname=CASE WHEN $6::text IS NULL THEN hostname ELSE nullif($6,'') END,
			 public_ipv4=CASE WHEN $7::text IS NULL THEN public_ipv4 ELSE nullif($7,'')::inet END,
			 public_ipv6=CASE WHEN $8::text IS NULL THEN public_ipv6 ELSE nullif($8,'')::inet END,
			 private_ipv4=CASE WHEN $9::text IS NULL THEN private_ipv4 ELSE nullif($9,'')::inet END,
			 architecture=CASE WHEN $10::text IS NULL THEN architecture ELSE nullif($10,'') END,
			 os_name=CASE WHEN $11::text IS NULL THEN os_name ELSE nullif($11,'') END,
			 capacity_nodes=coalesce($12,capacity_nodes),
			 notes=CASE WHEN $13::text IS NULL THEN notes ELSE nullif($13,'') END,
			 row_version=row_version+1
			WHERE tenant_id=$1 AND id=$2::uuid AND row_version=$3 AND deleted_at IS NULL`,
			tenantID, id, in.RowVersion, in.Name, in.Region, in.Hostname, in.PublicIPv4,
			in.PublicIPv6, in.PrivateIPv4, in.Architecture, in.OSName,
			in.CapacityNodes, in.Notes)
		if db.IsUniqueViolation(err) {
			return httpx.New(httpx.CodeConflict, "服务器名称已存在")
		}
		if err != nil {
			return err
		}
		if ct.RowsAffected() == 0 {
			return httpx.New(httpx.CodeConflict, "服务器版本已变化，请刷新后重试")
		}
		return audit.Write(ctx, tx, tenantID, audit.Entry{
			ActorKind: "admin", ActorID: &in.ActorID, Action: "server.update",
			ResourceType: "server", ResourceID: &id, APIDomain: "admin",
			RequestID:    httpx.RequestIDFrom(ctx),
			BeforeDigest: map[string]any{"name": beforeName, "status": beforeStatus, "row_version": currentVersion},
			AfterDigest:  map[string]any{"row_version": currentVersion + 1},
		})
	})
	if err != nil {
		return nil, err
	}
	return s.GetServer(ctx, tenantID, id)
}

func (s *Service) SetServerStatus(ctx context.Context, tenantID, id string, in SetServerStatusInput) (*Server, error) {
	fields := map[string]string{}
	if in.RowVersion <= 0 {
		fields["row_version"] = "必须提供正整数版本号"
	}
	if !ValidServerStatus(in.Status) {
		fields["status"] = "不支持的服务器状态"
	}
	if len(fields) > 0 {
		return nil, httpx.Invalid(fields)
	}
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: in.ActorID}, func(tx pgx.Tx) error {
		var before string
		var currentVersion int64
		if err := tx.QueryRow(ctx, `SELECT status,row_version FROM servers
			WHERE tenant_id=$1 AND id=$2::uuid AND deleted_at IS NULL FOR UPDATE`, tenantID, id).
			Scan(&before, &currentVersion); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return httpx.NotFoundOrForbidden()
			}
			return err
		}
		if currentVersion != in.RowVersion {
			return &httpx.Error{Code: httpx.CodeConflict, Message: "服务器已被其他管理员修改，请刷新后重试",
				Fields: map[string]string{"row_version": fmt.Sprintf("current=%d", currentVersion)}}
		}
		if !ValidServerStatusTransition(before, in.Status) {
			return &httpx.Error{Code: httpx.CodeConflict, Message: "不允许的服务器状态转换",
				Fields: map[string]string{"status": before + " -> " + in.Status}}
		}
		if in.Status == "ready" {
			var ready bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(
				SELECT 1 FROM nodes WHERE tenant_id=$1 AND server_id=$2::uuid
				  AND status='active' AND serving_status='active' AND node_type IS NOT NULL
				  AND server_port BETWEEN 1 AND 65535
				  AND `+StableProtocolReadySQL("")+`
			)`, tenantID, id).Scan(&ready); err != nil {
				return err
			}
			if !ready {
				return &httpx.Error{Code: httpx.CodeConflict,
					Message: "服务器没有通过协议校验且可服务的活动节点，不能进入 ready",
					Fields:  map[string]string{"nodes": "requires_valid_active_node"}}
			}
		}
		_, err := tx.Exec(ctx, `UPDATE servers SET status=$3,status_reason=nullif($4,''),
			entered_status_at=now(),retired_at=CASE WHEN $3='retired' THEN now() ELSE NULL END,
			row_version=row_version+1 WHERE tenant_id=$1 AND id=$2::uuid`,
			tenantID, id, in.Status, strings.TrimSpace(in.Reason))
		if err != nil {
			return err
		}
		return audit.Write(ctx, tx, tenantID, audit.Entry{
			ActorKind: "admin", ActorID: &in.ActorID, Action: "server.status_change",
			ResourceType: "server", ResourceID: &id, APIDomain: "admin",
			RequestID:    httpx.RequestIDFrom(ctx),
			BeforeDigest: map[string]any{"status": before, "row_version": currentVersion},
			AfterDigest:  map[string]any{"status": in.Status, "reason": in.Reason, "row_version": currentVersion + 1},
		})
	})
	if err != nil {
		return nil, err
	}
	return s.GetServer(ctx, tenantID, id)
}

func (s *Service) DeleteServer(ctx context.Context, tenantID, actorID, id string, rowVersion int64) error {
	if rowVersion <= 0 {
		return httpx.Invalid(map[string]string{"row_version": "必须提供正整数版本号"})
	}
	return s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: actorID}, func(tx pgx.Tx) error {
		var status string
		var currentVersion int64
		var nodes, identities, tasks, controlNode int
		err := tx.QueryRow(ctx, `
			SELECT s.status,s.row_version,
			 (SELECT count(*)::int FROM nodes n WHERE n.tenant_id=s.tenant_id AND n.server_id=s.id),
			 (SELECT count(*)::int FROM node_identities i WHERE i.tenant_id=s.tenant_id
			   AND i.status='active' AND i.node_id IN (
			     SELECT n.id FROM nodes n WHERE n.tenant_id=s.tenant_id
			       AND (n.server_id=s.id OR n.id=s.control_node_id))),
			 (SELECT count(*)::int FROM node_tasks t WHERE t.tenant_id=s.tenant_id
			   AND t.status IN ('pending','dispatched','running') AND t.node_id IN (
			     SELECT n.id FROM nodes n WHERE n.tenant_id=s.tenant_id
			       AND (n.server_id=s.id OR n.id=s.control_node_id))),
			 CASE WHEN s.control_node_id IS NULL THEN 0 ELSE 1 END
			FROM servers s WHERE s.tenant_id=$1 AND s.id=$2::uuid
			  AND s.deleted_at IS NULL FOR UPDATE`, tenantID, id).
			Scan(&status, &currentVersion, &nodes, &identities, &tasks, &controlNode)
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NotFoundOrForbidden()
		}
		if err != nil {
			return err
		}
		if currentVersion != rowVersion {
			return &httpx.Error{Code: httpx.CodeConflict, Message: "服务器已被其他管理员修改，请刷新后重试",
				Fields: map[string]string{"row_version": fmt.Sprintf("current=%d", currentVersion)}}
		}
		// 服务器只能在草稿或已退役时删除；在役（ready/maintenance 等）仍需先退役。
		// 名下还挂着探针（节点）不拒绝删除：删除会把这些节点一起静默（级联），
		// 对应 Agent 心跳立即失效、不再下发配置，重新安装同名即可覆盖。
		if status != "retired" && status != "draft" {
			return &httpx.Error{Code: httpx.CodeConflict,
				Message: "服务器仍在服务，请先在「状态」里退役", Fields: map[string]string{"status": status}}
		}
		// 级联静默名下探针：吊销身份、作废在途任务、摘除控制节点关系。
		// 这些动作与 DeleteNode 语义一致，只是批量执行。
		if nodes > 0 {
			if _, err := tx.Exec(ctx, `
				UPDATE node_identities SET status='revoked', revoked_at=now(),
				       revoked_reason='服务器已删除，探针静默'
				 WHERE tenant_id=$1 AND status='active' AND node_id IN (
				   SELECT n.id FROM nodes n WHERE n.tenant_id=$1 AND n.server_id=$2::uuid)`,
				tenantID, id); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `
				UPDATE node_tasks SET status='failed', completed_at=now()
				 WHERE tenant_id=$1 AND node_id IN (
				   SELECT n.id FROM nodes n WHERE n.tenant_id=$1 AND n.server_id=$2::uuid)
				   AND status IN ('pending','dispatched','running')`,
				tenantID, id); err != nil {
				return err
			}
			// 探针静默 = serving_status 置 retired：身份查询（AGT-004）对
			// serving_status='retired' 的节点直接拒绝，Agent 心跳即失效、
			// 不再下发配置。lifecycle status 保留原值，避免与 NODE-010
			// 状态机（active 不能直接 retired/destroyed）冲突。
			// 额外置 silenced_by_server_delete=true 标记：这是「服务器删除级联
			// 静默」与「手工退役」的唯一区分依据，允许同名重装覆盖复活。
			if _, err := tx.Exec(ctx, `
				UPDATE nodes SET serving_status='retired', silenced_by_server_delete=true,
				       entered_status_at=now(), row_version=row_version+1,
				       server_id=NULL
				 WHERE tenant_id=$1 AND server_id=$2::uuid`,
				tenantID, id); err != nil {
				return err
			}
		}
		ct, err := tx.Exec(ctx, `UPDATE servers
			SET deleted_at=now(), status='retired', retired_at=coalesce(retired_at,now()),
			    control_node_id=NULL, row_version=row_version+1
			WHERE tenant_id=$1 AND id=$2::uuid AND row_version=$3 AND deleted_at IS NULL`, tenantID, id, rowVersion)
		if err != nil {
			return err
		}
		if ct.RowsAffected() == 0 {
			return httpx.NotFoundOrForbidden()
		}
		return audit.Write(ctx, tx, tenantID, audit.Entry{
			ActorKind: "admin", ActorID: &actorID, Action: "server.delete",
			ResourceType: "server", ResourceID: &id, APIDomain: "admin",
			RequestID: httpx.RequestIDFrom(ctx), BeforeDigest: map[string]any{"status": status},
			AfterDigest: map[string]any{"status": "retired", "deleted": true, "silenced_nodes": nodes},
		})
	})
}

func (s *Service) ListServerNodes(ctx context.Context, tenantID, id string) ([]ServerNode, error) {
	out := []ServerNode{}
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM servers
			WHERE tenant_id=$1 AND id=$2::uuid AND deleted_at IS NULL)`, tenantID, id).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return httpx.NotFoundOrForbidden()
		}
		rows, err := tx.Query(ctx, `
			SELECT id,name,status,serving_status,node_type,display_name,server_host,
			       server_port,coalesce(kernel,'auto'),traffic_rate,config_validated_at,
			       last_heartbeat_at,created_at
			  FROM nodes WHERE tenant_id=$1 AND server_id=$2::uuid
			 ORDER BY sort_order,name,id`, tenantID, id)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var n ServerNode
			if err := rows.Scan(&n.ID, &n.Name, &n.Status, &n.ServingStatus, &n.NodeType,
				&n.DisplayName, &n.ServerHost, &n.ServerPort, &n.Kernel, &n.TrafficRate,
				&n.ConfigValidatedAt, &n.LastHeartbeatAt, &n.CreatedAt); err != nil {
				return err
			}
			out = append(out, n)
		}
		return rows.Err()
	})
	return out, err
}
