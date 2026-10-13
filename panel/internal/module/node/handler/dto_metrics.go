package handler

import (
	"time"

	"github.com/aegispanel/aegis/internal/module/node/service"
)

// 节点指标的响应 DTO，形状照前端 panel/frontend/src/admin/screens/nodes/schemas.ts 的 metricsSchema。

// metricPointDTO 是曲线上的一个点。
type metricPointDTO struct {
	At         time.Time `json:"at"`
	CPUPercent float64   `json:"cpu_percent"`
	MemPercent float64   `json:"mem_percent"`
	Load1      float64   `json:"load1"`
	RxSpeed    int64     `json:"rx_speed"`
	TxSpeed    int64     `json:"tx_speed"`
	TCPConns   int       `json:"tcp_conns"`
}

// metricsLatestDTO 是最近一点的读数。
type metricsLatestDTO struct {
	CPUPercent  float64 `json:"cpu_percent"`
	MemUsedMB   int64   `json:"mem_used_mb"`
	MemTotalMB  int64   `json:"mem_total_mb"`
	DiskUsedGB  int64   `json:"disk_used_gb"`
	DiskTotalGB int64   `json:"disk_total_gb"`
	Load1       float64 `json:"load1"`
	Load5       float64 `json:"load5"`
	Load15      float64 `json:"load15"`
	TCPConns    int     `json:"tcp_conns"`
	UptimeSec   int64   `json:"uptime_sec"`
	RxTotal     int64   `json:"rx_total"`
	TxTotal     int64   `json:"tx_total"`
}

// metricsResponse 是 GET /v1/nodes/:id/metrics 的响应；没有点时 points 是 []、latest 是 null。
type metricsResponse struct {
	Points []metricPointDTO  `json:"points"`
	Latest *metricsLatestDTO `json:"latest"`
}

func metricsResponseFrom(m service.NodeMetrics) metricsResponse {
	out := metricsResponse{Points: make([]metricPointDTO, 0, len(m.Points))}
	for _, p := range m.Points {
		out.Points = append(out.Points, metricPointDTO{
			At: p.At.UTC(), CPUPercent: p.CPUPercent, MemPercent: p.MemPercent, Load1: p.Load1,
			RxSpeed: p.RxSpeed, TxSpeed: p.TxSpeed, TCPConns: p.TCPConns,
		})
	}
	if l := m.Latest; l != nil {
		out.Latest = &metricsLatestDTO{
			CPUPercent: l.CPUPercent,
			MemUsedMB:  l.MemUsedMB, MemTotalMB: l.MemTotalMB,
			DiskUsedGB: l.DiskUsedGB, DiskTotalGB: l.DiskTotalGB,
			Load1: l.Load1, Load5: l.Load5, Load15: l.Load15,
			TCPConns: l.TCPConns, UptimeSec: l.UptimeSec,
			RxTotal: l.RxTotal, TxTotal: l.TxTotal,
		}
	}
	return out
}
