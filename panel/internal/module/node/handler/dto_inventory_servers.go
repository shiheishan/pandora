package handler

// 服务器后台接口的请求与响应 DTO（JSON 形状对齐前端 screens/nodes/schemas.ts）。

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

// serverUpgradeOptionResponse 是这台现在能不能在线升级（serverSchema.upgrade）。
type serverUpgradeOptionResponse struct {
	Available     bool    `json:"available"`
	TargetVersion *string `json:"target_version"` // 没有可用发布包时 null
	Reason        *string `json:"reason"`         // 能升级时 null
}

// serverLastUpgradeResponse 是最近一次在线升级任务（serverSchema.last_upgrade）。
type serverLastUpgradeResponse struct {
	Status        string    `json:"status"`
	TargetVersion string    `json:"target_version"`
	ReasonCode    *string   `json:"reason_code"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// serverResponse 是一台服务器（serverSchema）。
// 资源指标取最新一个指标点，没有点时为 null。
type serverResponse struct {
	ID                 uuid.UUID                   `json:"id"`
	Name               string                      `json:"name"`
	Status             string                      `json:"status"`
	StatusReason       *string                     `json:"status_reason"`
	RowVersion         int64                       `json:"row_version"`
	Region             *string                     `json:"region"`
	Hostname           *string                     `json:"hostname"`
	PublicIPv4         *string                     `json:"public_ipv4"`
	PublicIPv6         *string                     `json:"public_ipv6"`
	PrivateIPv4        *string                     `json:"private_ipv4"`
	Architecture       *string                     `json:"architecture"`
	OSName             *string                     `json:"os_name"`
	AgentVersion       *string                     `json:"agent_version"`
	LastHeartbeatAt    *time.Time                  `json:"last_heartbeat_at"`
	HeartbeatOnline    bool                        `json:"heartbeat_online"`
	Enrolled           bool                        `json:"enrolled"`
	CPUCores           *int                        `json:"cpu_cores"`
	MemoryMB           *int                        `json:"memory_mb"`
	DiskGB             *int                        `json:"disk_gb"`
	CapacityNodes      int                         `json:"capacity_nodes"`
	Notes              *string                     `json:"notes"`
	NodeCount          int                         `json:"node_count"`
	ActiveNodeCount    int                         `json:"active_node_count"`
	ServingNodeCount   int                         `json:"serving_node_count"`
	NeverSeenNodeCount int                         `json:"never_seen_node_count"`
	CreatedAt          time.Time                   `json:"created_at"`
	UpdatedAt          time.Time                   `json:"updated_at"`
	CPUBP              *int                        `json:"cpu_bp"`
	MemUsedMB          *int64                      `json:"mem_used_mb"`
	MemTotalMB         *int64                      `json:"mem_total_mb"`
	DiskUsedGB         *int64                      `json:"disk_used_gb"`
	DiskTotalGB        *int64                      `json:"disk_total_gb"`
	MetricsAt          *time.Time                  `json:"metrics_at"`
	BinarySHA256       *string                     `json:"binary_sha256"`
	Upgrade            serverUpgradeOptionResponse `json:"upgrade"`
	LastUpgrade        *serverLastUpgradeResponse  `json:"last_upgrade"`
}

type serversResponse struct {
	Servers []serverResponse `json:"servers"`
	Total   int              `json:"total"`
}

type serverDeletedResponse struct {
	OK bool      `json:"ok"`
	ID uuid.UUID `json:"id"`
}

// serverFrom 把 service 的服务器转成响应。
func serverFrom(s service.Server) serverResponse {
	var last *serverLastUpgradeResponse
	if s.LastUpgrade != nil {
		last = &serverLastUpgradeResponse{
			Status:        s.LastUpgrade.Status,
			TargetVersion: s.LastUpgrade.TargetVersion,
			ReasonCode:    s.LastUpgrade.ReasonCode,
			UpdatedAt:     s.LastUpgrade.UpdatedAt,
		}
	}
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
		Enrolled:           s.Enrolled,
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
		BinarySHA256:       s.BinarySHA256,
		Upgrade: serverUpgradeOptionResponse{
			Available:     s.Upgrade.Available,
			TargetVersion: optString(s.Upgrade.TargetVersion),
			Reason:        optString(s.Upgrade.Reason),
		},
		LastUpgrade: last,
	}
}

// optString 把空串转成 nil，便于 JSON 输出 null。
func optString(v string) *string {
	if v == "" {
		return nil
	}
	return &v
}

// serversFrom 把服务器列表转成响应（切片非 nil）。
func serversFrom(list []service.Server) serversResponse {
	out := make([]serverResponse, 0, len(list))
	for _, s := range list {
		out = append(out, serverFrom(s))
	}
	return serversResponse{Servers: out, Total: len(out)}
}
