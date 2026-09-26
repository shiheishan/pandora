// [INPUT]: 依赖同包 protocol_schema / protocol_validate / xboard_validate 的协议校验、protocol_secrets 的敏感键保全与 nodestream.go 的 notifyNodeChanged，依赖 config_publish.go 的发布锁与期望版本物化，依赖 platform 的 audit/db/httpx
// [OUTPUT]: 对外提供 AdminNode 与各输入类型、StableProtocol* 服务协议白名单、Service 的 CreateAdminNode、GetAdminNode、PatchAdminNode
// [POS]: domain/nodefabric 的后台节点编排：乐观锁 row_version、服务器容量锁、协议 schema 校验；PATCH 缺席的敏感键保留原值（R78）；国家代码（00082）只在这里写、只进管理端；复制 / 移动 / 排序在 node_admin_placement.go，批量服务状态与删除在 node_admin_lifecycle.go；stableProtocolTypes 必须留在本文件（check_native_panel_parity.py 按文件名读）
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package nodefabric

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/audit"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

type AdminNode struct {
	ID                    string          `json:"id"`
	RowVersion            int64           `json:"row_version"`
	Name                  string          `json:"name"`
	ServerID              *string         `json:"server_id"`
	PoolID                *string         `json:"pool_id"`
	Status                string          `json:"status"`
	ServingStatus         string          `json:"serving_status"`
	NodeType              *string         `json:"node_type"`
	ServerHost            *string         `json:"server_host"`
	ServerPort            *int            `json:"server_port"`
	Kernel                string          `json:"kernel"`
	TrafficRate           float64         `json:"traffic_rate"`
	DisplayName           *string         `json:"display_name"`
	CountryCode           *string         `json:"country_code"`
	ProtocolConfig        json.RawMessage `json:"protocol_config"`
	ProtocolSchemaVersion int             `json:"protocol_schema_version"`
	ConfigValidatedAt     *time.Time      `json:"config_validated_at"`
	SortOrder             int             `json:"sort_order"`
	CreatedAt             time.Time       `json:"created_at"`
	UpdatedAt             time.Time       `json:"updated_at"`
	Warnings              []string        `json:"warnings,omitempty"`
}

// OptionalNullableString distinguishes omitted from explicit null. PATCH uses
// omitted=keep and null/empty=clear.
type OptionalNullableString struct {
	Set   bool
	Value *string
}

func (o *OptionalNullableString) UnmarshalJSON(data []byte) error {
	o.Set = true
	if string(data) == "null" {
		o.Value = nil
		return nil
	}
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	o.Value = &value
	return nil
}

type CreateAdminNodeInput struct {
	ActorID        string          `json:"-"`
	Name           string          `json:"name"`
	ServerID       string          `json:"server_id"`
	PoolID         string          `json:"pool_id"`
	NodeType       string          `json:"node_type"`
	ServerHost     string          `json:"server_host"`
	ServerPort     int             `json:"server_port"`
	Kernel         string          `json:"kernel"`
	TrafficRate    float64         `json:"traffic_rate"`
	DisplayName    string          `json:"display_name"`
	CountryCode    string          `json:"country_code"`
	ProtocolConfig json.RawMessage `json:"protocol_config"`
	SortOrder      int             `json:"sort_order"`
}

type PatchAdminNodeInput struct {
	ActorID        string                 `json:"-"`
	RowVersion     int64                  `json:"row_version"`
	Name           *string                `json:"name"`
	PoolID         OptionalNullableString `json:"pool_id"`
	NodeType       *string                `json:"node_type"`
	ServerHost     *string                `json:"server_host"`
	ServerPort     *int                   `json:"server_port"`
	Kernel         *string                `json:"kernel"`
	TrafficRate    *float64               `json:"traffic_rate"`
	DisplayName    *string                `json:"display_name"`
	CountryCode    OptionalNullableString `json:"country_code"`
	ProtocolConfig *json.RawMessage       `json:"protocol_config"`
}

type CloneAdminNodeInput struct {
	ActorID        string `json:"-"`
	Name           string `json:"name"`
	RowVersion     int64  `json:"row_version"`
	TargetServerID string `json:"target_server_id"`
	PoolID         string `json:"pool_id"`
	CopyRouting    bool   `json:"copy_routing"`
	SortOrder      *int   `json:"sort_order"`
}

type MoveAdminNodeInput struct {
	ActorID    string `json:"-"`
	ServerID   string `json:"server_id"`
	RowVersion int64  `json:"row_version"`
	Reason     string `json:"reason"`
}

type ReorderNodeItem struct {
	ID         string `json:"id"`
	RowVersion int64  `json:"row_version"`
	SortOrder  int    `json:"sort_order"`
}

type ReorderNodesInput struct {
	ActorID string            `json:"-"`
	Items   []ReorderNodeItem `json:"items"`
}

type BatchNodeLifecycleItem struct {
	ID         string `json:"id"`
	RowVersion int64  `json:"row_version"`
}

type BatchNodeLifecycleInput struct {
	ActorID       string                   `json:"-"`
	Items         []BatchNodeLifecycleItem `json:"items"`
	ServingStatus string                   `json:"serving_status"`
	Reason        string                   `json:"reason"`
}

const (
	// StableProtocolSchemaVersion is the only protocol schema version that may
	// enter a serving path. Version 0 remains readable for migration and audit,
	// but it must never be authenticated or issued to subscribers.
	StableProtocolSchemaVersion = 1
	stableProtocolSQLLiterals   = "'anytls','http','hysteria2','juicity','mieru','naive','shadowsocks','shadowtls','socks','trojan','tuic','vless','vmess'"
)

var stableProtocolTypes = [...]string{
	"anytls", "http", "hysteria2", "juicity", "mieru", "naive", "shadowsocks", "shadowtls", "socks", "trojan", "tuic", "vless", "vmess",
}

// StableProtocolTypes returns a copy of the protocol allowlist used by every
// serving gate. Callers cannot mutate the process-wide policy.
func StableProtocolTypes() []string {
	return append([]string(nil), stableProtocolTypes[:]...)
}

// IsStableProtocolType reports whether nodeType is one of the explicitly
// opened v1 protocols. Unknown and legacy protocol identifiers fail closed.
func IsStableProtocolType(nodeType string) bool {
	nodeType = CanonicalNodeType(nodeType)
	for _, candidate := range stableProtocolTypes {
		if nodeType == candidate {
			return true
		}
	}
	return false
}

// StableProtocolReadySQL is the single SQL predicate used before a Node can
// serve. The alias is deliberately restricted to the static aliases used by
// this package and its admin/subscription callers; it must never be user data.
// Keeping the literal allowlist here makes an accidental permissive v0 OR
// impossible at call sites, while the catalog parity test catches schema drift.
func StableProtocolReadySQL(alias string) string {
	switch alias {
	case "", "n", "gate_node":
	default:
		panic("unsupported stable protocol SQL alias")
	}
	prefix := ""
	if alias != "" {
		prefix = alias + "."
	}
	return "(" + prefix + "protocol_schema_version = 1 AND " +
		prefix + "node_type IN (" + stableProtocolSQLLiterals + ") AND " +
		prefix + "config_validated_at IS NOT NULL)"
}

const adminNodeSelect = `SELECT n.id,n.row_version,n.name,n.server_id,n.pool_id,
	n.status,n.serving_status,n.node_type,n.server_host,n.server_port,coalesce(n.kernel,'auto'),
	n.traffic_rate,n.display_name,n.country_code,n.protocol_config,n.protocol_schema_version,
	n.config_validated_at,n.sort_order,n.created_at,n.updated_at FROM nodes n`

func scanAdminNode(row pgx.Row) (*AdminNode, error) {
	var n AdminNode
	err := row.Scan(&n.ID, &n.RowVersion, &n.Name, &n.ServerID, &n.PoolID, &n.Status,
		&n.ServingStatus, &n.NodeType, &n.ServerHost, &n.ServerPort, &n.Kernel,
		&n.TrafficRate, &n.DisplayName, &n.CountryCode, &n.ProtocolConfig, &n.ProtocolSchemaVersion,
		&n.ConfigValidatedAt, &n.SortOrder, &n.CreatedAt, &n.UpdatedAt)
	return &n, err
}

func validateAdminNodeName(name string) error {
	name = strings.TrimSpace(name)
	if len([]rune(name)) < 1 || len([]rune(name)) > 120 {
		return httpx.Invalid(map[string]string{"name": "名称必须为 1 到 120 个字符"})
	}
	return nil
}

// normalizeCountryCode 把国家代码规范成两位大写字母（ISO 3166-1 alpha-2 的形状），
// 空串表示不填。只校验形状不校验是否真有这个国家：国旗由前端按代码渲染，
// 认不出的代码显示成字母，比后端维护一张会过时的国家表更稳。
func normalizeCountryCode(raw string) (string, error) {
	code := strings.ToUpper(strings.TrimSpace(raw))
	if code == "" {
		return "", nil
	}
	if len(code) != 2 || code[0] < 'A' || code[0] > 'Z' || code[1] < 'A' || code[1] > 'Z' {
		return "", httpx.Invalid(map[string]string{"country_code": "必须是两位字母国家代码"})
	}
	return code, nil
}

func validateNewNodeProtocol(nodeType, kernel, host string, port int, raw json.RawMessage) (int, error) {
	host = strings.TrimSpace(host)
	if host == "" {
		return 0, httpx.Invalid(map[string]string{"server_host": "必填"})
	}
	if !validServerName(host) {
		return 0, httpx.Invalid(map[string]string{"server_host": "必须是有效的 IP 或 ASCII 主机名"})
	}
	// 走 Admin 版：管理端提交的是 xboard 形状，翻译成内核形状再交给
	// 原来那套校验，错误字段名再映射回来。
	version, fields := ValidateAdminProtocolConfig(nodeType, kernel, port, raw)
	if len(fields) > 0 {
		return 0, httpx.Invalid(fields)
	}
	// Legacy v0 remains readable and cloneable, but a new protocol write must
	// use an opened, versioned schema.
	if version != StableProtocolSchemaVersion || !IsStableProtocolType(nodeType) {
		return 0, httpx.Invalid(map[string]string{"node_type": "该协议仅兼容旧数据，尚未开放新写入"})
	}
	return version, nil
}

func (s *Service) lockServerCapacity(ctx context.Context, tx pgx.Tx, tenantID, serverID string) error {
	var status string
	var capacity, used int
	// Lock first and count in a second statement. If the count is a subquery of
	// the locking SELECT, READ COMMITTED may retain the pre-wait snapshot and
	// admit concurrent writes past capacity.
	err := tx.QueryRow(ctx, `SELECT s.status,s.capacity_nodes
		FROM servers s WHERE s.tenant_id=$1 AND s.id=$2::uuid
		 AND s.deleted_at IS NULL FOR UPDATE`, tenantID, serverID).Scan(&status, &capacity)
	if errors.Is(err, pgx.ErrNoRows) {
		return httpx.Invalid(map[string]string{"server_id": "服务器不存在或已删除"})
	}
	if err != nil {
		return err
	}
	if err := tx.QueryRow(ctx, `SELECT count(*)::int FROM nodes
		WHERE tenant_id=$1 AND server_id=$2::uuid AND serving_status<>'retired'`,
		tenantID, serverID).Scan(&used); err != nil {
		return err
	}
	if status == "retired" || status == "quarantined" {
		return httpx.New(httpx.CodeConflict, "目标服务器当前不接受节点")
	}
	if used >= capacity {
		return &httpx.Error{Code: httpx.CodeConflict, Message: "目标服务器容量已满",
			Fields: map[string]string{"capacity_nodes": fmt.Sprintf("%d/%d", used, capacity)}}
	}
	return nil
}

func validateAdminUUID(field, raw string, required bool) error {
	value := strings.TrimSpace(raw)
	if value == "" {
		if required {
			return httpx.Invalid(map[string]string{field: "必填"})
		}
		return nil
	}
	if _, err := uuid.Parse(value); err != nil {
		return httpx.Invalid(map[string]string{field: "格式不正确"})
	}
	return nil
}

func validatePool(ctx context.Context, tx pgx.Tx, tenantID, poolID string) error {
	if poolID == "" {
		return nil
	}
	var id string
	if err := tx.QueryRow(ctx, `SELECT id::text FROM node_pools
		WHERE tenant_id=$1 AND id=$2::uuid AND status<>'disabled'
		FOR SHARE`, tenantID, poolID).Scan(&id); errors.Is(err, pgx.ErrNoRows) {
		return httpx.Invalid(map[string]string{"pool_id": "资源池不存在或已禁用"})
	} else if err != nil {
		return err
	}
	return nil
}

func (s *Service) CreateAdminNode(ctx context.Context, tenantID string, in CreateAdminNodeInput) (*AdminNode, error) {
	in.Name = strings.TrimSpace(in.Name)
	in.ServerID = strings.TrimSpace(in.ServerID)
	in.PoolID = strings.TrimSpace(in.PoolID)
	in.NodeType = CanonicalNodeType(in.NodeType)
	in.ServerHost = strings.TrimSpace(in.ServerHost)
	in.Kernel = strings.TrimSpace(in.Kernel)
	in.DisplayName = strings.TrimSpace(in.DisplayName)
	if err := validateAdminNodeName(in.Name); err != nil {
		return nil, err
	}
	country, err := normalizeCountryCode(in.CountryCode)
	if err != nil {
		return nil, err
	}
	if err := validateAdminUUID("server_id", in.ServerID, true); err != nil {
		return nil, err
	}
	if err := validateAdminUUID("pool_id", in.PoolID, false); err != nil {
		return nil, err
	}
	if in.Kernel == "" {
		in.Kernel = "auto"
	}
	if in.TrafficRate <= 0 {
		in.TrafficRate = 1
	}
	version, err := validateNewNodeProtocol(in.NodeType, in.Kernel, in.ServerHost, in.ServerPort, in.ProtocolConfig)
	if err != nil {
		return nil, err
	}
	var id string
	err = s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: in.ActorID}, func(tx pgx.Tx) error {
		if err := lockLegacyConfigRelease(ctx, tx, tenantID); err != nil {
			return err
		}
		if err := s.lockServerCapacity(ctx, tx, tenantID, in.ServerID); err != nil {
			return err
		}
		if err := validatePool(ctx, tx, tenantID, in.PoolID); err != nil {
			return err
		}
		err := tx.QueryRow(ctx, `INSERT INTO nodes
			(tenant_id,name,server_id,pool_id,status,serving_status,node_type,server_host,
			 server_port,kernel,traffic_rate,display_name,protocol_config,
			 protocol_schema_version,config_validated_at,sort_order,row_version,country_code)
			VALUES ($1,$2,$3::uuid,nullif($4,'')::uuid,'draft','draft',$5,nullif($6,''),
			 $7,$8,$9,nullif($10,''),$11,$12,now(),$13,1,nullif($14,'')) RETURNING id`,
			tenantID, in.Name, in.ServerID, in.PoolID, in.NodeType, in.ServerHost,
			in.ServerPort, in.Kernel, in.TrafficRate, in.DisplayName,
			in.ProtocolConfig, version, in.SortOrder, country).Scan(&id)
		if db.IsUniqueViolation(err) {
			return httpx.New(httpx.CodeConflict, "节点名称已存在")
		}
		if err != nil {
			return err
		}
		if err := syncLegacyDesiredConfigVersion(ctx, tx, tenantID, id); err != nil {
			return err
		}
		return audit.Write(ctx, tx, tenantID, audit.Entry{ActorKind: "admin", ActorID: &in.ActorID,
			Action: "node.create", ResourceType: "node", ResourceID: &id, APIDomain: "admin",
			RequestID: httpx.RequestIDFrom(ctx), AfterDigest: map[string]any{
				"name": in.Name, "server_id": in.ServerID, "node_type": in.NodeType, "schema_version": version}})
	})
	if err != nil {
		return nil, err
	}
	return s.GetAdminNode(ctx, tenantID, id)
}

func (s *Service) GetAdminNode(ctx context.Context, tenantID, id string) (*AdminNode, error) {
	var out *AdminNode
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		var err error
		out, err = scanAdminNode(tx.QueryRow(ctx, adminNodeSelect+` WHERE n.tenant_id=$1 AND n.id=$2::uuid`, tenantID, id))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, httpx.NotFoundOrForbidden()
	}
	if err == nil && out != nil {
		out.ProtocolConfig = RedactProtocolConfig(out.ProtocolConfig)
	}
	return out, err
}

func (s *Service) PatchAdminNode(ctx context.Context, tenantID, id string, in PatchAdminNodeInput) (*AdminNode, error) {
	if in.RowVersion <= 0 {
		return nil, httpx.Invalid(map[string]string{"row_version": "必须提供正整数版本号"})
	}
	if in.Name != nil && validateAdminNodeName(*in.Name) != nil {
		return nil, validateAdminNodeName(*in.Name)
	}
	if in.PoolID.Set && in.PoolID.Value != nil {
		if err := validateAdminUUID("pool_id", strings.TrimSpace(*in.PoolID.Value), false); err != nil {
			return nil, err
		}
	}
	// 国家代码：省略=不改，null 或空串=清空
	patchCountry := ""
	if in.CountryCode.Set && in.CountryCode.Value != nil {
		var err error
		if patchCountry, err = normalizeCountryCode(*in.CountryCode.Value); err != nil {
			return nil, err
		}
	}
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: in.ActorID}, func(tx pgx.Tx) error {
		before, err := scanAdminNode(tx.QueryRow(ctx, adminNodeSelect+` WHERE n.tenant_id=$1 AND n.id=$2::uuid FOR UPDATE`, tenantID, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NotFoundOrForbidden()
		}
		if err != nil {
			return err
		}
		if before.RowVersion != in.RowVersion {
			return nodeVersionConflict(before.RowVersion)
		}
		name, nodeType, host, kernel := before.Name, value(before.NodeType), value(before.ServerHost), before.Kernel
		poolID := value(before.PoolID)
		port, rate, display, raw := intValue(before.ServerPort), before.TrafficRate, value(before.DisplayName), before.ProtocolConfig
		country := value(before.CountryCode)
		if in.CountryCode.Set {
			country = patchCountry
		}
		if in.Name != nil {
			name = strings.TrimSpace(*in.Name)
		}
		if in.NodeType != nil {
			nodeType = CanonicalNodeType(*in.NodeType)
		}
		if in.ServerHost != nil {
			host = strings.TrimSpace(*in.ServerHost)
		}
		if in.ServerPort != nil {
			port = *in.ServerPort
		}
		if in.Kernel != nil {
			kernel = strings.TrimSpace(*in.Kernel)
		}
		if in.TrafficRate != nil {
			rate = *in.TrafficRate
		}
		if in.DisplayName != nil {
			display = strings.TrimSpace(*in.DisplayName)
		}
		if in.ProtocolConfig != nil {
			raw = *in.ProtocolConfig
			// R78：读接口抹掉了敏感键，请求里缺席的按原路径从库里补回。换了协议
			// 类型时旧密钥不属于新协议，补回去只会换来一条莫名其妙的 422，不补。
			if nodeType == value(before.NodeType) {
				if raw, err = PreserveRedactedProtocolSecrets(before.ProtocolConfig, raw); err != nil {
					return err
				}
			}
		}
		poolChanged := false
		if in.PoolID.Set {
			poolID = ""
			if in.PoolID.Value != nil {
				poolID = strings.TrimSpace(*in.PoolID.Value)
			}
			if err := validatePool(ctx, tx, tenantID, poolID); err != nil {
				return err
			}
			poolChanged = poolID != value(before.PoolID)
		}
		if rate <= 0 {
			return httpx.Invalid(map[string]string{"traffic_rate": "必须大于 0"})
		}
		protocolTouched := in.NodeType != nil || in.ServerHost != nil || in.ServerPort != nil || in.Kernel != nil || in.ProtocolConfig != nil
		configSourceTouched := protocolTouched || poolChanged
		version := before.ProtocolSchemaVersion
		if protocolTouched {
			version, err = validateNewNodeProtocol(nodeType, kernel, host, port, raw)
			if err != nil {
				return err
			}
		}
		ct, err := tx.Exec(ctx, `UPDATE nodes SET name=$4,pool_id=nullif($5,'')::uuid,
			node_type=nullif($6,''),server_host=nullif($7,''),
			server_port=nullif($8,0),kernel=$9,traffic_rate=$10,display_name=nullif($11,''),
			protocol_config=$12,protocol_schema_version=$13,
			config_validated_at=CASE WHEN $14 THEN now() ELSE config_validated_at END,
			config_source_generation=config_source_generation + CASE WHEN $15 THEN 1 ELSE 0 END,
			country_code=nullif($16,''),
			row_version=row_version+1
			WHERE tenant_id=$1 AND id=$2::uuid AND row_version=$3`, tenantID, id,
			in.RowVersion, name, poolID, nodeType, host, port, kernel, rate, display, raw, version, protocolTouched, configSourceTouched,
			country)
		if db.IsUniqueViolation(err) {
			return httpx.New(httpx.CodeConflict, "节点名称已存在")
		}
		if err != nil {
			return err
		}
		if ct.RowsAffected() == 0 {
			return nodeVersionConflict(before.RowVersion)
		}
		return audit.Write(ctx, tx, tenantID, audit.Entry{ActorKind: "admin", ActorID: &in.ActorID,
			Action: "node.update", ResourceType: "node", ResourceID: &id, APIDomain: "admin",
			RequestID: httpx.RequestIDFrom(ctx), BeforeDigest: map[string]any{"row_version": before.RowVersion, "name": before.Name},
			AfterDigest: map[string]any{"name": name, "pool_id": poolID, "schema_version": version, "country_code": country}})
	})
	if err != nil {
		return nil, err
	}
	// 事务提交之后再推。放在事务里的话，回滚了节点却已经收到一份不存在
	// 的配置——那种不一致没有任何机制能自动纠正，只能等下一次有人改配置。
	s.notifyNodeChanged(ctx, tenantID, id)
	return s.GetAdminNode(ctx, tenantID, id)
}

func nodeVersionConflict(current int64) error {
	return &httpx.Error{Code: httpx.CodeConflict, Message: "节点已被其他管理员修改，请刷新后重试",
		Fields: map[string]string{"row_version": fmt.Sprintf("current=%d", current)}}
}

func value(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}
func intValue(v *int) int {
	if v == nil {
		return 0
	}
	return *v
}
