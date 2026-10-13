package handler

// 本文件归 p5-inventory：服务器后台接口的请求与响应 DTO（JSON 形状对齐前端 screens/nodes/schemas.ts）。

import (
	"time"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/module/node/service"
)

// ---------------------------------------------------------------------------
// 请求
// ---------------------------------------------------------------------------

type serverCreateRequest struct {
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

type serverPatchRequest struct {
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

type serverStatusRequest struct {
	Status     string `json:"status"`
	Reason     string `json:"reason"`
	RowVersion int64  `json:"row_version"`
}

type serverDeleteRequest struct {
	RowVersion int64 `json:"row_version"`
}

// ---------------------------------------------------------------------------
// 响应
// ---------------------------------------------------------------------------

// serverResponse 是一台服务器（serverSchema）。control_node_id 恒为 null：新设计一台服务器一条会话，没有「控制节点」。
// 资源指标取最新一个指标点，没有点时为 null。
type serverResponse struct {
	ID                 uuid.UUID  `json:"id"`
	Name               string     `json:"name"`
	Status             string     `json:"status"`
	StatusReason       *string    `json:"status_reason"`
	RowVersion         int64      `json:"row_version"`
	Region             *string    `json:"region"`
	Hostname           *string    `json:"hostname"`
	PublicIPv4         *string    `json:"public_ipv4"`
	PublicIPv6         *string    `json:"public_ipv6"`
	PrivateIPv4        *string    `json:"private_ipv4"`
	Architecture       *string    `json:"architecture"`
	OSName             *string    `json:"os_name"`
	AgentVersion       *string    `json:"agent_version"`
	LastHeartbeatAt    *time.Time `json:"last_heartbeat_at"`
	HeartbeatOnline    bool       `json:"heartbeat_online"`
	CPUCores           *int       `json:"cpu_cores"`
	MemoryMB           *int       `json:"memory_mb"`
	DiskGB             *int       `json:"disk_gb"`
	CapacityNodes      int        `json:"capacity_nodes"`
	Notes              *string    `json:"notes"`
	ControlNodeID      *uuid.UUID `json:"control_node_id"`
	NodeCount          int        `json:"node_count"`
	ActiveNodeCount    int        `json:"active_node_count"`
	ServingNodeCount   int        `json:"serving_node_count"`
	NeverSeenNodeCount int        `json:"never_seen_node_count"`
	CreatedAt          time.Time  `json:"created_at"`
	UpdatedAt          time.Time  `json:"updated_at"`
	CPUBP              *int       `json:"cpu_bp"`
	MemUsedMB          *int64     `json:"mem_used_mb"`
	MemTotalMB         *int64     `json:"mem_total_mb"`
	DiskUsedGB         *int64     `json:"disk_used_gb"`
	DiskTotalGB        *int64     `json:"disk_total_gb"`
	MetricsAt          *time.Time `json:"metrics_at"`
}

type serversResponse struct {
	Servers []serverResponse `json:"servers"`
	Total   int              `json:"total"`
}

// serverNodeResponse 是服务器名下的一个节点（serverNodeSchema）。
type serverNodeResponse struct {
	ID                uuid.UUID  `json:"id"`
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

type serverNodesResponse struct {
	Nodes []serverNodeResponse `json:"nodes"`
	Total int                  `json:"total"`
}

type serverDeletedResponse struct {
	OK bool      `json:"ok"`
	ID uuid.UUID `json:"id"`
}

// serverFrom 把 service 的服务器转成响应。
func serverFrom(s service.Server) serverResponse {
	return serverResponse{
		ID:                 s.ID,
		Name:               s.Name,
		Status:             s.Status,
		StatusReason:       s.StatusReason,
		RowVersion:         s.RowVersion,
		Region:             s.Region,
		Hostname:           s.Hostname,
		PublicIPv4:         s.PublicIPv4,
		PublicIPv6:         s.PublicIPv6,
		PrivateIPv4:        s.PrivateIPv4,
		Architecture:       s.Architecture,
		OSName:             s.OSName,
		AgentVersion:       s.AgentVersion,
		LastHeartbeatAt:    s.LastHeartbeatAt,
		HeartbeatOnline:    s.HeartbeatOnline,
		CPUCores:           s.CPUCores,
		MemoryMB:           s.MemoryMB,
		DiskGB:             s.DiskGB,
		CapacityNodes:      s.CapacityNodes,
		Notes:              s.Notes,
		NodeCount:          s.NodeCount,
		ActiveNodeCount:    s.ActiveNodeCount,
		ServingNodeCount:   s.ServingNodeCount,
		NeverSeenNodeCount: s.NeverSeenNodeCount,
		CreatedAt:          s.CreatedAt,
		UpdatedAt:          s.UpdatedAt,
		CPUBP:              s.CPUBp,
		MemUsedMB:          s.MemUsedMB,
		MemTotalMB:         s.MemTotalMB,
		DiskUsedGB:         s.DiskUsedGB,
		DiskTotalGB:        s.DiskTotalGB,
		MetricsAt:          s.MetricsAt,
	}
}

// serversFrom 把服务器列表转成响应（切片非 nil）。
func serversFrom(list []service.Server) serversResponse {
	out := make([]serverResponse, 0, len(list))
	for _, s := range list {
		out = append(out, serverFrom(s))
	}
	return serversResponse{Servers: out, Total: len(out)}
}

// serverNodeFrom 把服务器名下的节点转成响应。
func serverNodeFrom(n service.ServerNode) serverNodeResponse {
	return serverNodeResponse{
		ID:                n.ID,
		Name:              n.Name,
		Status:            n.ServingStatus,
		ServingStatus:     n.ServingStatus,
		NodeType:          n.NodeType,
		DisplayName:       n.DisplayName,
		ServerHost:        n.ServerHost,
		ServerPort:        n.ServerPort,
		Kernel:            n.Kernel,
		TrafficRate:       n.TrafficRate.InexactFloat64(),
		ConfigValidatedAt: n.ConfigValidatedAt,
		LastHeartbeatAt:   n.LastHeartbeatAt,
		CreatedAt:         n.CreatedAt,
	}
}

// serverNodesFrom 把服务器名下的节点列表转成响应（切片非 nil）。
func serverNodesFrom(list []service.ServerNode) serverNodesResponse {
	out := make([]serverNodeResponse, 0, len(list))
	for _, n := range list {
		out = append(out, serverNodeFrom(n))
	}
	return serverNodesResponse{Nodes: out, Total: len(out)}
}
