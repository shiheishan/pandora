package handler

// 节点后台接口的请求与响应 DTO（JSON 形状对齐前端 screens/nodes/schemas.ts）。

import (
	"bytes"
	"encoding/json"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/module/node/protocol"
	"github.com/aegispanel/aegis/internal/module/node/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
)

// ---------------------------------------------------------------------------
// 请求
// ---------------------------------------------------------------------------

// nodeOptString 区分「没给」「给了 null」「给了字符串」：UnmarshalJSON 被调用即 Set。
type nodeOptString struct {
	Set   bool
	Value *string
}

// UnmarshalJSON 实现 json.Unmarshaler。
func (o *nodeOptString) UnmarshalJSON(b []byte) error {
	o.Set = true
	if bytes.Equal(bytes.TrimSpace(b), []byte("null")) {
		o.Value = nil
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	o.Value = &s
	return nil
}

type nodeCreateRequest struct {
	Name           string          `json:"name"`
	ServerID       string          `json:"server_id"`
	PoolID         string          `json:"pool_id"`
	NodeType       string          `json:"node_type"`
	ServerHost     string          `json:"server_host"`
	ServerPort     int             `json:"server_port"`
	TrafficRate    *float64        `json:"traffic_rate"`
	DisplayName    string          `json:"display_name"`
	CountryCode    string          `json:"country_code"`
	ProtocolConfig json.RawMessage `json:"protocol_config"`
	SortOrder      int             `json:"sort_order"`
}

type nodePatchRequest struct {
	RowVersion     int64           `json:"row_version"`
	Name           *string         `json:"name"`
	PoolID         nodeOptString   `json:"pool_id"`
	NodeType       *string         `json:"node_type"`
	ServerHost     *string         `json:"server_host"`
	ServerPort     *int            `json:"server_port"`
	TrafficRate    *float64        `json:"traffic_rate"`
	DisplayName    *string         `json:"display_name"`
	CountryCode    nodeOptString   `json:"country_code"`
	ProtocolConfig json.RawMessage `json:"protocol_config"`
}

type nodeCopyRequest struct {
	RowVersion     int64  `json:"row_version"`
	Name           string `json:"name"`
	TargetServerID string `json:"target_server_id"`
	PoolID         string `json:"pool_id"`
	CopyRouting    bool   `json:"copy_routing"`
	SortOrder      *int   `json:"sort_order"`
	ServerPort     *int   `json:"server_port"`
}

type nodeMoveRequest struct {
	ServerID   string `json:"server_id"`
	RowVersion int64  `json:"row_version"`
	Reason     string `json:"reason"`
}

type nodeOrderItemRequest struct {
	ID         string `json:"id"`
	RowVersion int64  `json:"row_version"`
	SortOrder  int    `json:"sort_order"`
}

type nodeReorderRequest struct {
	Items []nodeOrderItemRequest `json:"items"`
}

type nodeVersionRefRequest struct {
	ID         string `json:"id"`
	RowVersion int64  `json:"row_version"`
}

type nodeStatusBatchRequest struct {
	Items         []nodeVersionRefRequest `json:"items"`
	ServingStatus string                  `json:"serving_status"`
	Reason        string                  `json:"reason"`
}

type nodeActivateRequest struct {
	RowVersion int64 `json:"row_version"`
}

type nodeRetireRequest struct {
	RowVersion int64  `json:"row_version"`
	Reason     string `json:"reason"`
}

type nodeDeleteRequest struct {
	RowVersion int64  `json:"row_version"`
	Reason     string `json:"reason"`
}

type nodePublishRequest struct {
	Scope    string          `json:"scope"`
	ScopeRef string          `json:"scope_ref"`
	Payload  json.RawMessage `json:"payload"`
}

// nodeParseOptionalID 解析请求体里可空的 uuid 字段：空串返回 nil，格式不对回 422。
func nodeParseOptionalID(field, raw string) (*uuid.UUID, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		return nil, errs.Invalid(map[string]string{field: "格式不正确"})
	}
	return &id, nil
}

// nodeParseRequiredID 解析请求体里必填的 uuid 字段。
func nodeParseRequiredID(field, raw string) (uuid.UUID, error) {
	id, err := nodeParseOptionalID(field, raw)
	if err != nil {
		return uuid.Nil, err
	}
	if id == nil {
		return uuid.Nil, errs.Invalid(map[string]string{field: "必填"})
	}
	return *id, nil
}

// nodeIsJSONNull 报告原始 JSON 是不是字面量 null。
func nodeIsJSONNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

// nodeJSONObject 解析原始 JSON 为对象；不是对象（缺席、null、数组、标量）返回 false。
func nodeJSONObject(raw json.RawMessage) (map[string]json.RawMessage, bool) {
	if len(bytes.TrimSpace(raw)) == 0 || nodeIsJSONNull(raw) {
		return nil, false
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil || obj == nil {
		return nil, false
	}
	return obj, true
}

// ---------------------------------------------------------------------------
// 响应
// ---------------------------------------------------------------------------

type nodeApplyFailureResponse struct {
	Phase  string    `json:"phase"`
	Detail string    `json:"detail"`
	At     time.Time `json:"at"`
}

// nodeRowResponse 是后台节点列表的一行（nodeRowSchema）。没有健康分与配置代数：配置是否生效看
// effective_state（按配置文档 SHA-256 比）。
type nodeRowResponse struct {
	ID                uuid.UUID                 `json:"id"`
	NodeNo            int64                     `json:"node_no"`
	RowVersion        int64                     `json:"row_version"`
	Name              string                    `json:"name"`
	ServingStatus     string                    `json:"serving_status"`
	ServerID          *uuid.UUID                `json:"server_id"`
	ServerName        *string                   `json:"server_name"`
	PoolID            *uuid.UUID                `json:"pool_id"`
	PoolName          *string                   `json:"pool_name"`
	AgentVersion      *string                   `json:"agent_version"`
	Hostname          *string                   `json:"hostname"`
	PublicIPv4        *string                   `json:"public_ipv4"`
	CPUCores          *int                      `json:"cpu_cores"`
	MemoryMB          *int                      `json:"memory_mb"`
	DiskGB            *int                      `json:"disk_gb"`
	LastHeartbeatAt   *time.Time                `json:"last_heartbeat_at"`
	Stale             bool                      `json:"stale"`
	DeliveredToUsers  bool                      `json:"delivered_to_users"`
	DeliveryNote      string                    `json:"delivery_note"`
	IdentitySerial    *int64                    `json:"identity_serial"`
	CreatedAt         time.Time                 `json:"created_at"`
	NodeType          *string                   `json:"node_type"`
	ServerHost        *string                   `json:"server_host"`
	ServerPort        *int                      `json:"server_port"`
	DisplayName       *string                   `json:"display_name"`
	CountryCode       *string                   `json:"country_code"`
	SortOrder         int                       `json:"sort_order"`
	OnlineUsers       int                       `json:"online_users"`
	OnlineIPs         int                       `json:"online_ips"`
	TrafficBytes24h   int64                     `json:"traffic_bytes_24h"`
	CPUPercent        *float64                  `json:"cpu_percent"`
	MemPercent        *float64                  `json:"mem_percent"`
	MetricsAt         *time.Time                `json:"metrics_at"`
	GrantedPlans      []string                  `json:"granted_plans"`
	EffectiveState    string                    `json:"effective_state"`
	LastApplyFailure  *nodeApplyFailureResponse `json:"last_apply_failure"`
	RuntimeStatus     *string                   `json:"runtime_status"`
	RuntimeReason     *string                   `json:"runtime_reason"`
	RuntimeStateAt    *time.Time                `json:"runtime_state_at"`
	RuntimeReasonNode *string                   `json:"runtime_reason_node"`
	DeliveryDegraded  bool                      `json:"delivery_degraded"`
}

// nodeDetailResponse 是单取（?id=）时的一行：列表行加编辑字段（nodeDetailSchema）。
type nodeDetailResponse struct {
	nodeRowResponse
	TrafficRate           float64         `json:"traffic_rate"`
	ProtocolConfig        json.RawMessage `json:"protocol_config"`
	ProtocolSchemaVersion int             `json:"protocol_schema_version"`
	ConfigValidatedAt     *time.Time      `json:"config_validated_at"`
	Warnings              []string        `json:"warnings,omitempty"`
}

type nodesListResponse struct {
	Nodes []nodeRowResponse `json:"nodes"`
	Total int64             `json:"total"`
}

type nodesDetailResponse struct {
	Nodes []nodeDetailResponse `json:"nodes"`
	Total int64                `json:"total"`
}

// adminNodeResponse 是节点写接口回的节点（adminNodeSchema）。
type adminNodeResponse struct {
	ID                    uuid.UUID       `json:"id"`
	RowVersion            int64           `json:"row_version"`
	Name                  string          `json:"name"`
	ServerID              *uuid.UUID      `json:"server_id"`
	PoolID                *uuid.UUID      `json:"pool_id"`
	ServingStatus         string          `json:"serving_status"`
	NodeType              *string         `json:"node_type"`
	ServerHost            *string         `json:"server_host"`
	ServerPort            *int            `json:"server_port"`
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

// nodeProtocolSchemaResponse 是一种协议的 schema（protocolSchemaSchema）。
type nodeProtocolSchemaResponse struct {
	NodeType            string              `json:"node_type"`
	Version             int                 `json:"version"`
	Status              string              `json:"status"`
	Required            []string            `json:"required"`
	AllowedProperties   []string            `json:"allowed_properties"`
	Methods             []string            `json:"methods,omitempty"`
	Enums               map[string][]string `json:"enums,omitempty"`
	PropertyTypes       map[string]string   `json:"property_types,omitempty"`
	SensitiveProperties []string            `json:"sensitive_properties,omitempty"`
	Hints               map[string]string   `json:"hints,omitempty"`
}

type nodeProtocolSchemasResponse struct {
	Schemas []nodeProtocolSchemaResponse `json:"schemas"`
}

type nodeOKUpdatedResponse struct {
	OK      bool `json:"ok"`
	Updated int  `json:"updated"`
}

type nodeBatchStatusResponse struct {
	OK            bool   `json:"ok"`
	Updated       int    `json:"updated"`
	ServingStatus string `json:"serving_status"`
}

type nodeRealityKeypairResponse struct {
	PrivateKey string `json:"private_key"`
	PublicKey  string `json:"public_key"`
	ShortID    string `json:"short_id"`
	Hint       string `json:"hint"`
}

type nodePublishResponse struct {
	ConfigID      uuid.UUID `json:"config_id"`
	Version       int64     `json:"version"`
	Scope         string    `json:"scope"`
	AffectedNodes int       `json:"affected_nodes"`
}

type nodeDeletedResponse struct {
	Deleted bool `json:"deleted"`
}

// nodeRowFrom 把 service 的列表行转成响应。
func nodeRowFrom(r service.NodeRow) nodeRowResponse {
	out := nodeRowResponse{
		ID:                r.ID,
		NodeNo:            r.NodeNo,
		RowVersion:        r.RowVersion,
		Name:              r.Name,
		ServingStatus:     r.ServingStatus,
		ServerID:          r.ServerID,
		ServerName:        r.ServerName,
		PoolID:            r.PoolID,
		PoolName:          r.PoolName,
		AgentVersion:      r.AgentVersion,
		Hostname:          r.Hostname,
		PublicIPv4:        r.PublicIPv4,
		CPUCores:          r.CPUCores,
		MemoryMB:          r.MemoryMB,
		DiskGB:            r.DiskGB,
		LastHeartbeatAt:   r.LastHeartbeatAt,
		Stale:             r.Stale,
		DeliveredToUsers:  r.Delivered,
		DeliveryNote:      r.DeliveryNote,
		CreatedAt:         r.CreatedAt,
		NodeType:          r.NodeType,
		ServerHost:        r.ServerHost,
		ServerPort:        r.ServerPort,
		DisplayName:       r.DisplayName,
		CountryCode:       r.CountryCode,
		SortOrder:         r.SortOrder,
		IdentitySerial:    r.IdentitySerial,
		OnlineUsers:       r.OnlineUsers,
		OnlineIPs:         r.OnlineIPs,
		TrafficBytes24h:   r.TrafficBytes24h,
		CPUPercent:        r.CPUPercent,
		MemPercent:        r.MemPercent,
		MetricsAt:         r.MetricsAt,
		GrantedPlans:      r.GrantedPlans,
		EffectiveState:    r.EffectiveState,
		RuntimeStatus:     r.RuntimeStatus,
		RuntimeReason:     r.RuntimeReason,
		RuntimeStateAt:    r.RuntimeStateAt,
		RuntimeReasonNode: r.RuntimeReasonNode,
		DeliveryDegraded:  r.DeliveryDegraded,
	}
	if out.GrantedPlans == nil {
		out.GrantedPlans = []string{}
	}
	if f := r.LastApplyFailure; f != nil {
		out.LastApplyFailure = &nodeApplyFailureResponse{Phase: f.Phase, Detail: f.Detail, At: f.At}
	}
	return out
}

// nodeDetailFrom 把单取的行转成响应；没有 Detail 时编辑字段取零值。
func nodeDetailFrom(r service.NodeRow) nodeDetailResponse {
	out := nodeDetailResponse{nodeRowResponse: nodeRowFrom(r), ProtocolConfig: json.RawMessage("{}")}
	if d := r.Detail; d != nil {
		out.TrafficRate = d.TrafficRate.InexactFloat64()
		if len(d.ProtocolConfig) > 0 {
			out.ProtocolConfig = d.ProtocolConfig
		}
		out.ProtocolSchemaVersion = d.ProtocolSchemaVersion
		out.ConfigValidatedAt = d.ConfigValidatedAt
		out.Warnings = d.Warnings
	}
	return out
}

// adminNodeFrom 把写接口回的节点转成响应。
func adminNodeFrom(n service.AdminNode) adminNodeResponse {
	cfg := n.ProtocolConfig
	if len(cfg) == 0 {
		cfg = json.RawMessage("{}")
	}
	var warnings []string
	if len(n.Warnings) > 0 {
		warnings = n.Warnings
	}
	return adminNodeResponse{
		ID:                    n.ID,
		RowVersion:            n.RowVersion,
		Name:                  n.Name,
		ServerID:              n.ServerID,
		PoolID:                n.PoolID,
		ServingStatus:         n.ServingStatus,
		NodeType:              n.NodeType,
		ServerHost:            n.ServerHost,
		ServerPort:            n.ServerPort,
		TrafficRate:           n.TrafficRate.InexactFloat64(),
		DisplayName:           n.DisplayName,
		CountryCode:           n.CountryCode,
		ProtocolConfig:        cfg,
		ProtocolSchemaVersion: n.ProtocolSchemaVersion,
		ConfigValidatedAt:     n.ConfigValidatedAt,
		SortOrder:             n.SortOrder,
		CreatedAt:             n.CreatedAt,
		UpdatedAt:             n.UpdatedAt,
		Warnings:              warnings,
	}
}

// nodeProtocolSchemaFrom 把协议 schema 转成响应；required、allowed_properties 恒为数组。
func nodeProtocolSchemaFrom(s protocol.Schema) nodeProtocolSchemaResponse {
	required := s.Required
	if required == nil {
		required = []string{}
	}
	allowed := s.AllowedProperties
	if allowed == nil {
		allowed = []string{}
	}
	return nodeProtocolSchemaResponse{
		NodeType:            s.NodeType,
		Version:             s.Version,
		Status:              s.Status,
		Required:            required,
		AllowedProperties:   allowed,
		Methods:             s.Methods,
		Enums:               s.Enums,
		PropertyTypes:       s.PropertyTypes,
		SensitiveProperties: s.SensitiveProperties,
		Hints:               s.Hints,
	}
}
