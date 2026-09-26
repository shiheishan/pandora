// [INPUT]: 依赖 service.go 的 Service，依赖 platform 的 db、httpx
// [OUTPUT]: 对外提供 Metrics、HeartbeatInput / HeartbeatOutput、MetricPoint、NodeMetrics，Service 的 Heartbeat、FetchMetrics、PurgeMetrics
// [POS]: domain/nodefabric 的心跳与探针（AGT-004）：从 service.go 拆出。指标全部是放大后的整数，避免浮点在存储与聚合时引入误差；后台按分钟窗口查询、定期按小时清理
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package nodefabric

import (
	"context"
	"encoding/base64"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

//------------------------------------------------------------------------------
// AGT-004 心跳
//------------------------------------------------------------------------------

// Metrics 是探针上报的一组瞬时值。全部为整数：
// 百分比放大 10000 倍、负载放大 100 倍，避免浮点在存储与聚合时引入误差。
type Metrics struct {
	CPUBasisPoints int   `json:"cpu_bp"`
	MemUsedMB      int   `json:"mem_used_mb"`
	MemTotalMB     int   `json:"mem_total_mb"`
	DiskUsedGB     int   `json:"disk_used_gb"`
	DiskTotalGB    int   `json:"disk_total_gb"`
	Load1CBP       int   `json:"load1_cbp"`
	Load5CBP       int   `json:"load5_cbp"`
	Load15CBP      int   `json:"load15_cbp"`
	NetRxBytes     int64 `json:"net_rx_bytes"`
	NetTxBytes     int64 `json:"net_tx_bytes"`
	TCPConns       int   `json:"tcp_conns"`
	UptimeSec      int64 `json:"uptime_sec"`
}

type HeartbeatInput struct {
	AgentVersion         string   `json:"agent_version"`
	RuntimeVersion       string   `json:"runtime_version"`
	ConfigSigningKeyID   string   `json:"config_signing_key_id"`
	ConfigVersion        int      `json:"applied_config_version"`
	ConfigHash           string   `json:"applied_config_hash"`
	AppliedReleaseID     string   `json:"applied_effective_release_id"`
	AppliedGeneration    uint64   `json:"applied_effective_generation"`
	AppliedContentSHA256 string   `json:"applied_effective_content_sha256"`
	CPUCores             int      `json:"cpu_cores"`
	MemoryMB             int      `json:"memory_mb"`
	DiskGB               int      `json:"disk_gb"`
	LoadPercent          int      `json:"load_percent"`
	RuntimeStatus        string   `json:"runtime_status"`
	Metrics              *Metrics `json:"metrics"`
	MetricsPartial       bool     `json:"metrics_partial"`
}

type HeartbeatOutput struct {
	NodeStatus string `json:"node_status"`
	// DesiredConfigVersion 与 Agent 上报的版本不同即表示有新配置待应用
	DesiredConfigVersion int    `json:"desired_config_version"`
	DesiredReleaseID     string `json:"desired_effective_release_id,omitempty"`
	DesiredGeneration    uint64 `json:"desired_effective_generation,omitempty"`
	IntervalSeconds      int    `json:"interval_seconds"`
}

func (s *Service) Heartbeat(ctx context.Context, tenantID, nodeID string, in HeartbeatInput) (*HeartbeatOutput, error) {
	if in.ConfigSigningKeyID != "" {
		if _, err := canonicalEffectiveReleaseKeyID(in.ConfigSigningKeyID); err != nil {
			return nil, httpx.New(httpx.CodeBadRequest, "invalid config signing key id").WithInternal(err)
		}
	}
	var out HeartbeatOutput
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		var hash []byte
		if in.ConfigHash != "" {
			hash, _ = base64.StdEncoding.DecodeString(in.ConfigHash)
		}
		// 健康分先给一个可解释的粗粒度值：能上报即 90，运行时异常降到 40。
		// POOL-004 的多维评分留到调度实装时再细化。
		score := 90
		if in.RuntimeStatus != "" && in.RuntimeStatus != "running" {
			score = 40
		}
		// 探针点独立于节点当前状态存放：节点行只保留「最新一眼」，
		// 时序表保留曲线。两者混在一张表会让节点表被高频写入拖慢。
		if in.Metrics != nil {
			m := in.Metrics
			if _, err := tx.Exec(ctx, `
				INSERT INTO node_metrics
					(tenant_id, node_id, cpu_bp, mem_used_mb, mem_total_mb,
					 disk_used_gb, disk_total_gb, load1_cbp, load5_cbp, load15_cbp,
					 net_rx_bytes, net_tx_bytes, tcp_conns, uptime_sec)
				VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
				ON CONFLICT (node_id, recorded_at) DO NOTHING`,
				tenantID, nodeID, m.CPUBasisPoints, m.MemUsedMB, m.MemTotalMB,
				m.DiskUsedGB, m.DiskTotalGB, m.Load1CBP, m.Load5CBP, m.Load15CBP,
				m.NetRxBytes, m.NetTxBytes, m.TCPConns, m.UptimeSec); err != nil {
				return err
			}
		}

		var desiredReleaseID *string
		var desiredGeneration *int64
		if err := tx.QueryRow(ctx, `
			UPDATE nodes
			   SET last_heartbeat_at = now(), agent_version = $3,
			       runtime_version = coalesce(nullif($4,''), runtime_version),
			       config_signing_key_id = coalesce(nullif($11,''), config_signing_key_id),
			       applied_config_version = nullif($5,0),
			       applied_config_hash = $6,
			       cpu_cores = coalesce(nullif($7,0), cpu_cores),
			       memory_mb = coalesce(nullif($8,0), memory_mb),
			       disk_gb   = coalesce(nullif($9,0), disk_gb),
			       health_score = $10
			 WHERE tenant_id = $1 AND id = $2
			RETURNING status, coalesce(desired_config_version, 0),
			          desired_effective_release_id::text, desired_effective_generation`,
			tenantID, nodeID, in.AgentVersion, in.RuntimeVersion,
			in.ConfigVersion, hash, in.CPUCores, in.MemoryMB, in.DiskGB, score, in.ConfigSigningKeyID,
		).Scan(&out.NodeStatus, &out.DesiredConfigVersion, &desiredReleaseID, &desiredGeneration); err != nil {
			return err
		}
		if desiredReleaseID != nil {
			out.DesiredReleaseID = *desiredReleaseID
		}
		if desiredGeneration != nil && *desiredGeneration > 0 {
			out.DesiredGeneration = uint64(*desiredGeneration)
		}
		_, err := tx.Exec(ctx, `
			UPDATE servers SET last_heartbeat_at=now(), agent_version=$3,
			       cpu_cores=coalesce(nullif($4,0),cpu_cores),
			       memory_mb=coalesce(nullif($5,0),memory_mb),
			       disk_gb=coalesce(nullif($6,0),disk_gb)
			 WHERE tenant_id=$1 AND id=$2 AND deleted_at IS NULL`,
			tenantID, nodeID, in.AgentVersion, in.CPUCores, in.MemoryMB, in.DiskGB)
		return err
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, httpx.New(httpx.CodeNotFound, "节点不存在")
		}
		return nil, err
	}
	out.IntervalSeconds = 30
	return &out, nil
}

//------------------------------------------------------------------------------
// 探针查询
//------------------------------------------------------------------------------

type MetricPoint struct {
	At         time.Time `json:"at"`
	CPUPercent float64   `json:"cpu_percent"`
	MemPercent float64   `json:"mem_percent"`
	Load1      float64   `json:"load1"`
	// 速率由相邻两点的累计值差分得出，单位 字节/秒
	RxSpeed  int64 `json:"rx_speed"`
	TxSpeed  int64 `json:"tx_speed"`
	TCPConns int   `json:"tcp_conns"`
}

type NodeMetrics struct {
	Points []MetricPoint `json:"points"`
	// Latest 是最近一个点的原始值，前端用它显示当前数值
	Latest *struct {
		CPUPercent  float64 `json:"cpu_percent"`
		MemUsedMB   int     `json:"mem_used_mb"`
		MemTotalMB  int     `json:"mem_total_mb"`
		DiskUsedGB  int     `json:"disk_used_gb"`
		DiskTotalGB int     `json:"disk_total_gb"`
		Load1       float64 `json:"load1"`
		Load5       float64 `json:"load5"`
		Load15      float64 `json:"load15"`
		TCPConns    int     `json:"tcp_conns"`
		UptimeSec   int64   `json:"uptime_sec"`
		RxTotal     int64   `json:"rx_total"`
		TxTotal     int64   `json:"tx_total"`
	} `json:"latest"`
}

// FetchMetrics 返回该节点最近 minutes 分钟的探针曲线。
//
// 网络速率在这里差分而不是让 Agent 上报：Agent 重启会让累计计数器归零，
// 若由它算速率就会产生一个巨大的虚假尖峰；在服务端差分则表现为一个负值，
// 可以识别并跳过。
func (s *Service) FetchMetrics(ctx context.Context, tenantID, nodeID string, minutes int) (*NodeMetrics, error) {
	if minutes <= 0 || minutes > 1440 {
		minutes = 60
	}
	out := &NodeMetrics{Points: []MetricPoint{}}

	type raw struct {
		at              time.Time
		cpu, memU, memT *int
		l1, l5, l15     *int
		rx, tx          *int64
		conns           *int
		diskU, diskT    *int
		uptime          *int64
	}
	var rows []raw

	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		rs, err := tx.Query(ctx, `
			SELECT recorded_at, cpu_bp, mem_used_mb, mem_total_mb,
			       load1_cbp, load5_cbp, load15_cbp,
			       net_rx_bytes, net_tx_bytes, tcp_conns,
			       disk_used_gb, disk_total_gb, uptime_sec
			  FROM node_metrics
			 WHERE tenant_id = $1 AND node_id = $2
			   AND recorded_at > now() - make_interval(mins => $3)
			 ORDER BY recorded_at`,
			tenantID, nodeID, minutes)
		if err != nil {
			return err
		}
		defer rs.Close()
		for rs.Next() {
			var r raw
			if err := rs.Scan(&r.at, &r.cpu, &r.memU, &r.memT,
				&r.l1, &r.l5, &r.l15, &r.rx, &r.tx, &r.conns,
				&r.diskU, &r.diskT, &r.uptime); err != nil {
				return err
			}
			rows = append(rows, r)
		}
		return rs.Err()
	})
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return out, nil
	}

	iv := func(p *int) int {
		if p == nil {
			return 0
		}
		return *p
	}
	i64 := func(p *int64) int64 {
		if p == nil {
			return 0
		}
		return *p
	}

	for i, r := range rows {
		pt := MetricPoint{
			At:         r.at,
			CPUPercent: float64(iv(r.cpu)) / 100,
			Load1:      float64(iv(r.l1)) / 100,
			TCPConns:   iv(r.conns),
		}
		if t := iv(r.memT); t > 0 {
			pt.MemPercent = float64(iv(r.memU)) / float64(t) * 100
		}
		if i > 0 {
			prev := rows[i-1]
			dt := r.at.Sub(prev.at).Seconds()
			// 间隔过长说明中间有断点，差分出来的「速率」没有意义
			if dt > 0 && dt < 300 {
				if d := i64(r.rx) - i64(prev.rx); d >= 0 {
					pt.RxSpeed = int64(float64(d) / dt)
				}
				if d := i64(r.tx) - i64(prev.tx); d >= 0 {
					pt.TxSpeed = int64(float64(d) / dt)
				}
			}
		}
		out.Points = append(out.Points, pt)
	}

	last := rows[len(rows)-1]
	out.Latest = &struct {
		CPUPercent  float64 `json:"cpu_percent"`
		MemUsedMB   int     `json:"mem_used_mb"`
		MemTotalMB  int     `json:"mem_total_mb"`
		DiskUsedGB  int     `json:"disk_used_gb"`
		DiskTotalGB int     `json:"disk_total_gb"`
		Load1       float64 `json:"load1"`
		Load5       float64 `json:"load5"`
		Load15      float64 `json:"load15"`
		TCPConns    int     `json:"tcp_conns"`
		UptimeSec   int64   `json:"uptime_sec"`
		RxTotal     int64   `json:"rx_total"`
		TxTotal     int64   `json:"tx_total"`
	}{
		CPUPercent: float64(iv(last.cpu)) / 100,
		MemUsedMB:  iv(last.memU), MemTotalMB: iv(last.memT),
		DiskUsedGB: iv(last.diskU), DiskTotalGB: iv(last.diskT),
		Load1:    float64(iv(last.l1)) / 100,
		Load5:    float64(iv(last.l5)) / 100,
		Load15:   float64(iv(last.l15)) / 100,
		TCPConns: iv(last.conns), UptimeSec: i64(last.uptime),
		RxTotal: i64(last.rx), TxTotal: i64(last.tx),
	}
	return out, nil
}

// PurgeMetrics 清理超出保留期的探针点。幂等。
func (s *Service) PurgeMetrics(ctx context.Context, tenantID string, keepHours int) (int64, error) {
	if keepHours <= 0 {
		keepHours = 48
	}
	var n int64
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT app.purge_node_metrics($1)`, keepHours).Scan(&n)
	})
	return n, err
}
