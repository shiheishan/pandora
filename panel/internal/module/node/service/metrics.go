package service

// 机器指标入账与读取。指标按服务器记（一台机器一份），节点页按节点所在服务器读。

import (
	"context"
	"math"
	"time"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/module/node/repo"
	"github.com/aegispanel/aegis/internal/pkg/errs"
)

// 指标曲线的窗口（分钟）：缺省 60，最长 1440（与旧接口相同；指标保留 48 小时）。
const (
	defaultMetricsMinutes = 60
	maxMetricsMinutes     = 1440
)

// maxRateGap 是相邻两点能算速率的最大间隔：再长说明中间断过，差分出来的速率没有意义。
const maxRateGap = 300 * time.Second

// clampU64 把 uint64 封顶到 int64 上限。
func clampU64(v uint64) int64 {
	if v > math.MaxInt64 {
		return math.MaxInt64
	}
	return int64(v)
}

// clampU32 把 uint32 封顶到 int32 上限。
func clampU32(v uint32) int32 {
	if v > math.MaxInt32 {
		return math.MaxInt32
	}
	return int32(v)
}

// RecordMetrics 记一个机器指标点（整数口径；CPU 封顶 10000bp；同一时刻重复的点丢弃）。
func (s *Service) RecordMetrics(ctx context.Context, serverID uuid.UUID, m HostMetrics, at time.Time) error {
	return s.repo.InsertMetric(ctx, repo.MetricRow{
		ServerID:       serverID,
		RecordedAt:     at.UTC().Truncate(time.Microsecond),
		CPUBp:          clampU32(min(m.CPUBp, 10000)),
		MemUsedBytes:   clampU64(m.MemUsedBytes),
		MemTotalBytes:  clampU64(m.MemTotalBytes),
		DiskUsedBytes:  clampU64(m.DiskUsedBytes),
		DiskTotalBytes: clampU64(m.DiskTotalBytes),
		Load1Cp:        clampU32(m.Load1Cp),
		Load5Cp:        clampU32(m.Load5Cp),
		Load15Cp:       clampU32(m.Load15Cp),
		NetRxBytes:     clampU64(m.NetRxBytes),
		NetTxBytes:     clampU64(m.NetTxBytes),
		TCPConns:       clampU32(m.TCPConns),
		UptimeSec:      clampU64(m.UptimeSec),
		Partial:        m.Partial,
	})
}

// MetricPoint 是曲线上的一个点。
type MetricPoint struct {
	At time.Time
	// CPUPercent 是 cpu_bp / 100
	CPUPercent float64
	// MemPercent 是 used / total × 100，total 为 0 时 0
	MemPercent float64
	// Load1 是 load1_cp / 100
	Load1 float64
	// RxSpeed、TxSpeed 是字节/秒，由相邻两点的累计差分出（重启归零的负差与断点记 0）
	RxSpeed, TxSpeed int64
	TCPConns         int
}

// MetricsLatest 是最近一点的读数（MB、GB 按 1024 进制整除）。
type MetricsLatest struct {
	CPUPercent              float64
	MemUsedMB, MemTotalMB   int64
	DiskUsedGB, DiskTotalGB int64
	Load1, Load5, Load15    float64
	TCPConns                int
	UptimeSec               int64
	RxTotal, TxTotal        int64
}

// NodeMetrics 是一个节点所在服务器的指标曲线；节点没挂服务器或没有点时 Points 为空、Latest 为 nil。
type NodeMetrics struct {
	Points []MetricPoint
	Latest *MetricsLatest
}

// NodeMetrics 读节点所在服务器最近 minutes 分钟的指标（minutes 不在 1..1440 时按 60）。节点不存在或已删除回 errs.NotFound。
func (s *Service) NodeMetrics(ctx context.Context, nodeID uuid.UUID, minutes int) (NodeMetrics, error) {
	if minutes <= 0 || minutes > maxMetricsMinutes {
		minutes = defaultMetricsMinutes
	}
	serverID, found, err := s.repo.NodeServerID(ctx, nodeID)
	if err != nil {
		return NodeMetrics{}, err
	}
	if !found {
		return NodeMetrics{}, errs.New(errs.NotFound)
	}
	if serverID == nil {
		return buildNodeMetrics(nil), nil
	}
	rows, err := s.repo.MetricsSince(ctx, *serverID, s.now().Add(-time.Duration(minutes)*time.Minute))
	if err != nil {
		return NodeMetrics{}, err
	}
	return buildNodeMetrics(rows), nil
}

// rateBetween 算累计计数器的速率（字节/秒）：间隔不在 (0, maxRateGap) 或差为负（重启归零）时为 0。
func rateBetween(prev, cur int64, dt time.Duration) int64 {
	if dt <= 0 || dt >= maxRateGap || cur < prev {
		return 0
	}
	ms := dt.Milliseconds()
	if ms <= 0 {
		return 0
	}
	// 先除后乘再补余数，差值很大时也不溢出
	d := cur - prev
	return d/ms*1000 + d%ms*1000/ms
}

// buildNodeMetrics 把按时间升序的行换算成曲线与最近一点（纯函数）。
func buildNodeMetrics(rows []repo.MetricRow) NodeMetrics {
	out := NodeMetrics{Points: make([]MetricPoint, 0, len(rows))}
	for i, r := range rows {
		p := MetricPoint{
			At:         r.RecordedAt,
			CPUPercent: float64(r.CPUBp) / 100,
			Load1:      float64(r.Load1Cp) / 100,
			TCPConns:   int(r.TCPConns),
		}
		if r.MemTotalBytes > 0 {
			p.MemPercent = float64(r.MemUsedBytes) / float64(r.MemTotalBytes) * 100
		}
		if i > 0 {
			prev := rows[i-1]
			dt := r.RecordedAt.Sub(prev.RecordedAt)
			p.RxSpeed = rateBetween(prev.NetRxBytes, r.NetRxBytes, dt)
			p.TxSpeed = rateBetween(prev.NetTxBytes, r.NetTxBytes, dt)
		}
		out.Points = append(out.Points, p)
	}
	if len(rows) > 0 {
		l := rows[len(rows)-1]
		out.Latest = &MetricsLatest{
			CPUPercent: float64(l.CPUBp) / 100,
			MemUsedMB:  l.MemUsedBytes >> 20, MemTotalMB: l.MemTotalBytes >> 20,
			DiskUsedGB: l.DiskUsedBytes >> 30, DiskTotalGB: l.DiskTotalBytes >> 30,
			Load1: float64(l.Load1Cp) / 100, Load5: float64(l.Load5Cp) / 100, Load15: float64(l.Load15Cp) / 100,
			TCPConns:  int(l.TCPConns),
			UptimeSec: l.UptimeSec,
			RxTotal:   l.NetRxBytes, TxTotal: l.NetTxBytes,
		}
	}
	return out
}

// ServerMetricsSnapshot 是服务器最新一点的摘要（服务器 / 节点列表用）。
type ServerMetricsSnapshot struct {
	// CPUPercent 是 cpu_bp / 100
	CPUPercent float64
	// MemPercent 是 used × 100 / total，保留一位小数（四舍五入）；total 为 0 时 nil
	MemPercent *float64
	At         time.Time
}

// LatestServerMetrics 按服务器 id 批量取最新一点（一条 SQL）；没有点的服务器不在结果里。
func (s *Service) LatestServerMetrics(ctx context.Context, serverIDs []uuid.UUID) (map[uuid.UUID]ServerMetricsSnapshot, error) {
	out := make(map[uuid.UUID]ServerMetricsSnapshot, len(serverIDs))
	if len(serverIDs) == 0 {
		return out, nil
	}
	rows, err := s.repo.LatestMetrics(ctx, serverIDs)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		snap := ServerMetricsSnapshot{CPUPercent: float64(r.CPUBp) / 100, At: r.RecordedAt}
		if r.MemTotalBytes > 0 {
			v := math.Round(float64(r.MemUsedBytes)*1000/float64(r.MemTotalBytes)) / 10
			snap.MemPercent = &v
		}
		out[r.ServerID] = snap
	}
	return out, nil
}
