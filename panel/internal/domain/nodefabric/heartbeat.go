package nodefabric

import (
	"context"
	"encoding/base64"
	"fmt"
	"math"
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

// validate 在写库之前核对探针值的范围：cpu_bp 撞 node_metrics 的 CHECK、int 字段
// 超出 int4 列宽，都会让整条心跳事务回滚成 500，节点连「活着」都报不上来。
// 越界按兼容通道 ReportRuntimeStatus 的做法回 400，整条心跳不落库。
func (m *Metrics) validate() error {
	if m.CPUBasisPoints < 0 || m.CPUBasisPoints > 10000 {
		return httpx.New(httpx.CodeBadRequest, "心跳指标 cpu_bp 必须在 0 到 10000 之间")
	}
	const maxInt4 = math.MaxInt32
	for _, f := range []struct {
		name  string
		value int
	}{
		{"mem_used_mb", m.MemUsedMB}, {"mem_total_mb", m.MemTotalMB},
		{"disk_used_gb", m.DiskUsedGB}, {"disk_total_gb", m.DiskTotalGB},
		{"load1_cbp", m.Load1CBP}, {"load5_cbp", m.Load5CBP}, {"load15_cbp", m.Load15CBP},
		{"tcp_conns", m.TCPConns},
	} {
		if f.value < 0 || f.value > maxInt4 {
			return httpx.New(httpx.CodeBadRequest, "心跳指标 "+f.name+" 必须是 0 到 2147483647 之间的整数")
		}
	}
	// bigint 列与 Go 的 int64 同宽，只需不为负
	for _, f := range []struct {
		name  string
		value int64
	}{
		{"net_rx_bytes", m.NetRxBytes}, {"net_tx_bytes", m.NetTxBytes}, {"uptime_sec", m.UptimeSec},
	} {
		if f.value < 0 {
			return httpx.New(httpx.CodeBadRequest, "心跳指标 "+f.name+" 不能为负数")
		}
	}
	return nil
}

type HeartbeatInput struct {
	AgentVersion         string `json:"agent_version"`
	RuntimeVersion       string `json:"runtime_version"`
	ConfigSigningKeyID   string `json:"config_signing_key_id"`
	ConfigVersion        int    `json:"applied_config_version"`
	ConfigHash           string `json:"applied_config_hash"`
	AppliedReleaseID     string `json:"applied_effective_release_id"`
	AppliedGeneration    uint64 `json:"applied_effective_generation"`
	AppliedContentSHA256 string `json:"applied_effective_content_sha256"`
	CPUCores             int    `json:"cpu_cores"`
	MemoryMB             int    `json:"memory_mb"`
	DiskGB               int    `json:"disk_gb"`
	LoadPercent          int    `json:"load_percent"`
	RuntimeStatus        string `json:"runtime_status"`
	// RuntimeReason 是 degraded 的机器可读原因，来自请求头 X-Node-Runtime-Reason（不在正文：
	// 正文按 DisallowUnknownFields 解码），由 handler 填
	RuntimeReason  string   `json:"-"`
	Metrics        *Metrics `json:"metrics"`
	MetricsPartial bool     `json:"metrics_partial"`
}

// validate 在写库（或进合并缓冲）之前核对上报：配置签名钥匙标识与探针值的范围。
func (in HeartbeatInput) validate() error {
	if in.ConfigSigningKeyID != "" {
		if _, err := canonicalEffectiveReleaseKeyID(in.ConfigSigningKeyID); err != nil {
			return httpx.New(httpx.CodeBadRequest, "配置签名密钥标识非法").WithInternal(err)
		}
	}
	if in.Metrics != nil {
		return in.Metrics.validate()
	}
	return nil
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
	return s.heartbeat(ctx, tenantID, nodeID, in, nil)
}

// HeartbeatSigned 是签名通道的心跳：写入在 SQL 里以「这把公钥仍是节点的有效身份」
// 为门槛（activeIdentitySQL），所以签名中间件不必先单独读一次纪元复核缓存身份——
// 身份在写入那一刻已被吊销、过期或节点已退役，整批什么都不写，回 ErrNodeIdentityInvalid。
// 这比「先复核、再写」更严：两步之间的提交缝也被堵上了。
func (s *Service) HeartbeatSigned(ctx context.Context, tenantID, nodeID string, in HeartbeatInput,
	check NodeSignatureCheck) (*HeartbeatOutput, error) {
	if len(check.publicKey) == 0 {
		return nil, ErrNodeIdentityInvalid
	}
	return s.heartbeat(ctx, tenantID, nodeID, in, check.publicKey)
}

// serverHeartbeatRefresh 是服务器行心跳时刻的刷新间隔。后台判在线用 90 秒，节点每
// 30 秒一次心跳：每隔一次写一下（最旧约 66 秒）就够，版本与资产变了照样立刻写。
const serverHeartbeatRefresh = "45 seconds"

// heartbeat 一次往返写完心跳（BatchScoped，异步提交）：
//   - 节点行：只改心跳类列与运行状态；去掉 idx_nodes_heartbeat、fillfactor 85（迁移 00116）之后是
//     HOT 更新，也不触发变更通知（00110 / 00116 的 WHEN）；运行状态或原因真变了才通知（00122）；
//   - 探针点：追加一行；
//   - 服务器行：两阶段接入建的服务器 id 就是控制节点 id（enrollment.go），后台服务器
//     列表的在线状态读它，所以不能删；只在过了刷新间隔或版本、资产变了时才写。
//     后台手建的服务器 id 与节点不同，这条按主键查不到行，什么也不写。
//
// gateKey 非空时三条写都以它为有效身份为门槛（HeartbeatSigned）。
//
// 遥测走异步提交：数据库崩溃最多丢掉最后几百毫秒的心跳，下一个节拍就补上；
// 主机的 fsync 抖动不再直接变成心跳端点的尾延迟。
func (s *Service) heartbeat(ctx context.Context, tenantID, nodeID string, in HeartbeatInput,
	gateKey []byte) (*HeartbeatOutput, error) {
	if err := in.validate(); err != nil {
		return nil, err
	}
	var hash []byte
	if in.ConfigHash != "" {
		hash, _ = base64.StdEncoding.DecodeString(in.ConfigHash)
	}
	// 健康分先给一个可解释的粗粒度值：能上报即 90，运行时异常降到 40（RuntimeState.HealthScore）。
	runtime := NormalizeRuntimeState(in.RuntimeStatus, in.RuntimeReason)
	score := runtime.HealthScore()

	gate, gateArgs := "", []any{}
	if gateKey != nil {
		gate = ` AND EXISTS (SELECT 1` + activeIdentitySQL + ` AND i.public_key = $%d)`
	}
	gated := func(n int) string {
		if gate == "" {
			return ""
		}
		return fmt.Sprintf(gate, n)
	}
	if gateKey != nil {
		gateArgs = append(gateArgs, gateKey)
	}

	var out HeartbeatOutput
	var desiredReleaseID *string
	var desiredGeneration *int64
	found := false
	b := &pgx.Batch{}
	b.Queue(`
			UPDATE nodes
			   SET last_heartbeat_at = now(), agent_version = $3,
			       runtime_version = coalesce(nullif($4,''), runtime_version),
			       config_signing_key_id = coalesce(nullif($11,''), config_signing_key_id),
			       applied_config_version = nullif($5,0),
			       applied_config_hash = $6,
			       cpu_cores = coalesce(nullif($7,0), cpu_cores),
			       memory_mb = coalesce(nullif($8,0), memory_mb),
			       disk_gb   = coalesce(nullif($9,0), disk_gb),
			       health_score = $10,
			       `+runtimeStateSetSQL("$12", "$13")+`
			 WHERE tenant_id = $1 AND id = $2`+gated(14)+`
			RETURNING status, coalesce(desired_config_version, 0),
			          desired_effective_release_id::text, desired_effective_generation`,
		append([]any{tenantID, nodeID, in.AgentVersion, in.RuntimeVersion,
			in.ConfigVersion, hash, in.CPUCores, in.MemoryMB, in.DiskGB, score, in.ConfigSigningKeyID,
			runtime.Status, runtime.Reason}, gateArgs...)...,
	).Query(func(rows pgx.Rows) error {
		for rows.Next() {
			found = true
			if err := rows.Scan(&out.NodeStatus, &out.DesiredConfigVersion, &desiredReleaseID, &desiredGeneration); err != nil {
				return err
			}
		}
		return rows.Err()
	})
	// 探针点独立于节点当前状态存放：节点行只保留「最新一眼」，时序表保留曲线。
	// 两者混在一张表会让节点表被高频写入拖慢。节点行不存在（或门槛不过）时一并不写。
	if in.Metrics != nil {
		m := in.Metrics
		b.Queue(`
				INSERT INTO node_metrics
					(tenant_id, node_id, cpu_bp, mem_used_mb, mem_total_mb,
					 disk_used_gb, disk_total_gb, load1_cbp, load5_cbp, load15_cbp,
					 net_rx_bytes, net_tx_bytes, tcp_conns, uptime_sec)
				SELECT $1::uuid, $2::uuid, $3::int, $4::int, $5::int, $6::int, $7::int,
				       $8::int, $9::int, $10::int, $11::bigint, $12::bigint, $13::int, $14::bigint
				 WHERE EXISTS (SELECT 1 FROM nodes WHERE tenant_id = $1::uuid AND id = $2::uuid)`+gated(15)+`
				ON CONFLICT (node_id, recorded_at) DO NOTHING`,
			append([]any{tenantID, nodeID, m.CPUBasisPoints, m.MemUsedMB, m.MemTotalMB,
				m.DiskUsedGB, m.DiskTotalGB, m.Load1CBP, m.Load5CBP, m.Load15CBP,
				m.NetRxBytes, m.NetTxBytes, m.TCPConns, m.UptimeSec}, gateArgs...)...)
	}
	b.Queue(`
			UPDATE servers SET last_heartbeat_at=now(), agent_version=$3,
			       cpu_cores=coalesce(nullif($4,0),cpu_cores),
			       memory_mb=coalesce(nullif($5,0),memory_mb),
			       disk_gb=coalesce(nullif($6,0),disk_gb)
			 WHERE tenant_id=$1 AND id=$2 AND deleted_at IS NULL
			   AND (last_heartbeat_at IS NULL
			        OR last_heartbeat_at < now() - interval '`+serverHeartbeatRefresh+`'
			        OR agent_version IS DISTINCT FROM $3
			        OR cpu_cores IS DISTINCT FROM coalesce(nullif($4,0),cpu_cores)
			        OR memory_mb IS DISTINCT FROM coalesce(nullif($5,0),memory_mb)
			        OR disk_gb IS DISTINCT FROM coalesce(nullif($6,0),disk_gb))`+gated(7),
		append([]any{tenantID, nodeID, in.AgentVersion, in.CPUCores, in.MemoryMB, in.DiskGB}, gateArgs...)...)
	if err := s.pool.BatchScoped(ctx, db.Scope{TenantID: tenantID}, db.BatchOptions{AsyncCommit: true}, b); err != nil {
		return nil, err
	}
	if !found {
		if gateKey != nil {
			return nil, ErrNodeIdentityInvalid
		}
		return nil, httpx.New(httpx.CodeNotFound, "节点不存在")
	}
	if desiredReleaseID != nil {
		out.DesiredReleaseID = *desiredReleaseID
	}
	if desiredGeneration != nil && *desiredGeneration > 0 {
		out.DesiredGeneration = uint64(*desiredGeneration)
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

// MetricsRetentionHours 是探针点的保留期：后台读得最远的是 FetchMetrics 的 24 小时
// 曲线（minutes 上限 1440），节点 / 服务器列表只取最近一点；留 48 小时（00015 的保留
// 策略，也是 app.purge_node_metrics 接受的下限）给事后排查多一天余量。
const MetricsRetentionHours = 48

// metricsPurgeBatch 是每个短事务最多删的行数，metricsPurgeMaxBatches 是一次调用最多
// 跑几批；积压由下一轮继续清。
const (
	metricsPurgeBatch      = 5000
	metricsPurgeMaxBatches = 200
)

// PurgeMetrics 清理超出保留期的探针点。由 aegis-admin 的保留期任务定时调用，幂等。
//
// node_metrics 是追加写表，运行角色不能直接删；删除只经 app.purge_node_metrics
// （00100：定义者权限、只删当前租户、保留期不少于 48 小时、每次最多一批）。这里按批循环，
// 每批一个短事务。keepHours 小于保留期时按保留期算。
func (s *Service) PurgeMetrics(ctx context.Context, tenantID string, keepHours int) (int64, error) {
	keepHours = max(keepHours, MetricsRetentionHours)
	var total int64
	for range metricsPurgeMaxBatches {
		var n int64
		err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT app.purge_node_metrics($1, $2)`,
				keepHours, metricsPurgeBatch).Scan(&n)
		})
		total += n
		if err != nil || n < metricsPurgeBatch {
			return total, err
		}
	}
	return total, nil
}
