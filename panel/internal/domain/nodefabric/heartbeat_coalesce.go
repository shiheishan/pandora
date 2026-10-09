package nodefabric

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/db"
)

// 心跳写合并（w10quiet）。
//
// 10k-r1 稳态里心跳是库耗时第一：每次心跳一个事务写节点行 + 一行探针点（61973 次，
// 均值 2.45 + 1.25 毫秒，约占 12.7% 单核）。绝大多数心跳只是「我还活着」：版本、运行
// 状态、资产都没变，只有 last_heartbeat_at 往前走、探针点多一行。
//
// 现在按节点记着上一次落库的「材料字段」（除探针值外的全部上报）：
//   - 材料字段没变、上一次收下的心跳不到 hbCoalesceMaxGap、纪元监听健康（回包里的状态与
//     期望版本从节点配置视图出，见 config_delivery_view.go）时，只记进内存，回包照旧；
//     后台协程每 hbFlushInterval 把攒下的心跳一批写进库（一条 UPDATE nodes、一条
//     INSERT node_metrics、一条 UPDATE servers，同一个异步提交的批）。
//   - 其余情况（第一次见到、任何材料字段变了、断了一阵又回来、监听不健康）照旧立即写。
//
// 口径与最坏延迟：
//   - last_heartbeat_at 最多比真实晚 hbFlushInterval（加一次批量写的耗时）。节点 30 秒
//     （±10%）一次心跳，库里看到的心跳年龄最坏约 33 + 15 = 48 秒，离线判定（NodeStaleAfter
//     90 秒）、侧栏离线任务、下发新鲜窗口（10 分钟）、后台列表的「最后在线」都读这一列，
//     健康节点不会被误判离线；
//   - 停了 hbCoalesceMaxGap 以上又回来的那一拍一定立即写，「离线 → 在线」（00110 的触发器
//     按两次心跳间隔判）与以前同样即时；
//   - 运行状态、降级原因、版本、配置签名钥匙、资产的变化都是立即写，降级判定与告警不受影响；
//   - 探针点降到每 hbMetricsEvery 一点（原来每次心跳一点）：曲线按相邻两点差分，点距不影响
//     速率口径，24 小时曲线从 2880 点变成约 1440 点；
//   - 进程崩溃丢的只是还没落库的那一小段心跳（最多 hbFlushInterval），下一拍补上。
//
// 身份门槛不放松：进缓冲的心跳在请求时已由纪元监听证明身份新鲜（验签用的缓存身份之后
// 没有任何身份或节点状态的提交）；批量写时三条语句仍按 activeIdentityFromSQL 逐行加
// 「这把公钥此刻仍是有效身份」的门槛，期间被吊销的节点那一行什么都不写。

const (
	// hbFlushInterval 是批量写的间隔。
	hbFlushInterval = 15 * time.Second
	// hbCoalesceMaxGap：离上一次收下的心跳超过这么久，这一拍立即写。要小于
	// NodeStaleAfter − hbFlushInterval，库里的心跳年龄才不会跨过离线判定。
	hbCoalesceMaxGap = 60 * time.Second
	// hbMetricsEvery 是缓冲路径写探针点的最小间隔（30 秒心跳带抖动，50 秒即每隔一拍一点）。
	hbMetricsEvery = 50 * time.Second
	// hbForgetAfter：这么久没心跳的节点从内存里删掉（下次来就是立即写）。
	hbForgetAfter = 10 * time.Minute
	// hbFlushTimeout 是一次批量写的上限。
	hbFlushTimeout = 10 * time.Second
)

// hbMaterial 是心跳里除探针值之外的全部上报：任何一项变了都立即写。
type hbMaterial struct {
	in HeartbeatInput // Metrics 恒为 nil，可直接比较
}

func materialOf(in HeartbeatInput) hbMaterial {
	in.Metrics = nil
	return hbMaterial{in: in}
}

type hbPending struct {
	at        time.Time
	key       []byte
	metrics   *Metrics
	metricsAt time.Time
}

type hbNodeState struct {
	material   hbMaterial
	acceptedAt time.Time // 最近一次收下的心跳（立即写或进缓冲）
	metricsAt  time.Time // 最近一次写进（或排进）探针点的心跳时刻
	pending    *hbPending
}

type heartbeatCoalescer struct {
	mu    sync.Mutex
	nodes map[string]*hbNodeState // 键：租户 \x00 节点
}

func newHeartbeatCoalescer() *heartbeatCoalescer {
	return &heartbeatCoalescer{nodes: make(map[string]*hbNodeState)}
}

func hbKey(tenantID, nodeID string) string { return tenantID + "\x00" + nodeID }

// offer 判断这一拍能不能只进缓冲；能就收下并返回 true。
func (c *heartbeatCoalescer) offer(key string, in HeartbeatInput, at time.Time, publicKey []byte) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	st := c.nodes[key]
	if st == nil || st.material != materialOf(in) || st.acceptedAt.IsZero() ||
		at.Sub(st.acceptedAt) >= hbCoalesceMaxGap || at.Before(st.acceptedAt) {
		return false
	}
	p := st.pending
	if p == nil {
		p = &hbPending{}
		st.pending = p
	}
	p.at, p.key = at, publicKey
	if in.Metrics != nil && at.Sub(st.metricsAt) >= hbMetricsEvery {
		m := *in.Metrics
		p.metrics, p.metricsAt = &m, at
		st.metricsAt = at
	}
	st.acceptedAt = at
	return true
}

// written 记下一次立即写成功：材料字段、时刻；缓冲里更早的那一拍作废。
func (c *heartbeatCoalescer) written(key string, in HeartbeatInput, at time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	st := c.nodes[key]
	if st == nil {
		st = &hbNodeState{}
		c.nodes[key] = st
	}
	st.material, st.acceptedAt, st.pending = materialOf(in), at, nil
	if in.Metrics != nil {
		st.metricsAt = at
	}
}

// forget 丢掉一个节点的记录（立即写失败时：下一拍必须再立即写）。
func (c *heartbeatCoalescer) forget(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.nodes, key)
}

type hbFlushRow struct {
	tenantID, nodeID string
	key              string
	p                hbPending
}

// take 取走全部待写的心跳，顺手删掉久不来的节点。
func (c *heartbeatCoalescer) take(now time.Time) []hbFlushRow {
	c.mu.Lock()
	defer c.mu.Unlock()
	var rows []hbFlushRow
	for key, st := range c.nodes {
		if st.pending != nil {
			tenantID, nodeID := splitHBKey(key)
			rows = append(rows, hbFlushRow{tenantID: tenantID, nodeID: nodeID, key: key, p: *st.pending})
			st.pending = nil
			continue
		}
		if now.Sub(st.acceptedAt) >= hbForgetAfter {
			delete(c.nodes, key)
		}
	}
	return rows
}

// putBack 把写失败的心跳放回去（期间没有更新的一拍才放）。
func (c *heartbeatCoalescer) putBack(rows []hbFlushRow) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, r := range rows {
		if st := c.nodes[r.key]; st != nil && st.pending == nil && !st.acceptedAt.After(r.p.at) {
			p := r.p
			st.pending = &p
		}
	}
}

func splitHBKey(key string) (string, string) {
	for i := 0; i < len(key); i++ {
		if key[i] == 0 {
			return key[:i], key[i+1:]
		}
	}
	return key, ""
}

// HeartbeatConfirmed 是签名通道心跳在身份已复核之后的入口（签名中间件判定不欠复核时用）：
// 能合并就只记内存，否则立即写，立即写仍以这把公钥为有效身份做门槛。
func (s *Service) HeartbeatConfirmed(ctx context.Context, tenantID, nodeID string, in HeartbeatInput,
	check NodeSignatureCheck) (*HeartbeatOutput, error) {
	if len(check.publicKey) == 0 {
		return nil, ErrNodeIdentityInvalid
	}
	if err := in.validate(); err != nil {
		return nil, err
	}
	c := s.hb
	parsed, perr := uuid.Parse(nodeID)
	if c == nil || perr != nil {
		return s.heartbeat(ctx, tenantID, nodeID, in, check.publicKey)
	}
	key, now := hbKey(tenantID, parsed.String()), time.Now()
	if v, ok := s.cachedNodeConfig(ctx, tenantID, nodeID, s.watchStamp()); ok && c.offer(key, in, now, check.publicKey) {
		return v.heartbeatOutput(), nil
	}
	out, err := s.heartbeat(ctx, tenantID, nodeID, in, check.publicKey)
	if err != nil {
		c.forget(key)
		return nil, err
	}
	c.written(key, in, now)
	return out, nil
}

// heartbeatOutput 是缓冲路径的回包，与立即写的 RETURNING 同口径。
func (v *nodeConfigView) heartbeatOutput() *HeartbeatOutput {
	out := &HeartbeatOutput{NodeStatus: v.status, DesiredConfigVersion: v.desiredConfigVersion, IntervalSeconds: 30}
	if v.desiredReleaseID != nil {
		out.DesiredReleaseID = *v.desiredReleaseID
	}
	if v.desiredGeneration != nil && *v.desiredGeneration > 0 {
		out.DesiredGeneration = uint64(*v.desiredGeneration)
	}
	return out
}

// StartHeartbeatCoalescer 打开心跳合并并起批量写协程（aegis-node 装配时调用一次）。
// ctx 结束时把缓冲里剩下的写完再退出；返回的 wait 阻塞到协程退出。
func (s *Service) StartHeartbeatCoalescer(ctx context.Context, log *slog.Logger) (wait func()) {
	if s.pool == nil {
		return func() {}
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	c := newHeartbeatCoalescer()
	s.hb = c
	done := make(chan struct{})
	go func() {
		defer close(done)
		t := time.NewTicker(hbFlushInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				// 停机：剩下的一批用独立的限时上下文写完
				fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), hbFlushTimeout)
				s.flushHeartbeats(fctx, c, log)
				cancel()
				return
			case <-t.C:
				fctx, cancel := context.WithTimeout(ctx, hbFlushTimeout)
				s.flushHeartbeats(fctx, c, log)
				cancel()
			}
		}
	}()
	return func() { <-done }
}

// flushHeartbeats 把缓冲里的心跳按租户一批写进库。
func (s *Service) flushHeartbeats(ctx context.Context, c *heartbeatCoalescer, log *slog.Logger) {
	rows := c.take(time.Now())
	if len(rows) == 0 {
		return
	}
	byTenant := make(map[string][]hbFlushRow)
	for _, r := range rows {
		byTenant[r.tenantID] = append(byTenant[r.tenantID], r)
	}
	for tenantID, batch := range byTenant {
		if err := s.writeHeartbeatBatch(ctx, tenantID, batch); err != nil {
			c.putBack(batch)
			log.Warn("心跳批量写失败，下一轮重试", "tenant_id", tenantID, "rows", len(batch), "error", err.Error())
		}
	}
}

// writeHeartbeatBatch 一次往返写完一批（BatchScoped，异步提交，与立即写同为遥测级）。
func (s *Service) writeHeartbeatBatch(ctx context.Context, tenantID string, rows []hbFlushRow) error {
	ids := make([]string, len(rows))
	ats := make([]time.Time, len(rows))
	keys := make([][]byte, len(rows))
	var m struct {
		ids                                            []string
		ats                                            []time.Time
		keys                                           [][]byte
		cpu, memU, memT, diskU, diskT, l1, l5, l15, tc []int32
		rx, tx, up                                     []int64
	}
	for i, r := range rows {
		ids[i], ats[i], keys[i] = r.nodeID, r.p.at, r.p.key
		if mt := r.p.metrics; mt != nil {
			m.ids, m.ats, m.keys = append(m.ids, r.nodeID), append(m.ats, r.p.metricsAt), append(m.keys, r.p.key)
			m.cpu, m.memU, m.memT = append(m.cpu, int32(mt.CPUBasisPoints)), append(m.memU, int32(mt.MemUsedMB)), append(m.memT, int32(mt.MemTotalMB))
			m.diskU, m.diskT = append(m.diskU, int32(mt.DiskUsedGB)), append(m.diskT, int32(mt.DiskTotalGB))
			m.l1, m.l5, m.l15 = append(m.l1, int32(mt.Load1CBP)), append(m.l5, int32(mt.Load5CBP)), append(m.l15, int32(mt.Load15CBP))
			m.tc = append(m.tc, int32(mt.TCPConns))
			m.rx, m.tx, m.up = append(m.rx, mt.NetRxBytes), append(m.tx, mt.NetTxBytes), append(m.up, mt.UptimeSec)
		}
	}
	gate := ` AND EXISTS (SELECT 1` + activeIdentityFromSQL("v.id") + ` AND i.public_key = v.k)`
	b := &pgx.Batch{}
	// 节点行只动 last_heartbeat_at（HOT 更新、不触发变更通知与 00153 的配置通知）；
	// 不往回写：立即写已经写了更新的时刻就跳过
	b.Queue(`
		UPDATE nodes n SET last_heartbeat_at = v.at
		  FROM unnest($2::uuid[], $3::timestamptz[], $4::bytea[]) AS v(id, at, k)
		 WHERE n.tenant_id = $1 AND n.id = v.id
		   AND (n.last_heartbeat_at IS NULL OR n.last_heartbeat_at < v.at)`+gate,
		tenantID, ids, ats, keys)
	if len(m.ids) > 0 {
		b.Queue(`
			INSERT INTO node_metrics
				(tenant_id, node_id, recorded_at, cpu_bp, mem_used_mb, mem_total_mb,
				 disk_used_gb, disk_total_gb, load1_cbp, load5_cbp, load15_cbp,
				 net_rx_bytes, net_tx_bytes, tcp_conns, uptime_sec)
			SELECT $1::uuid, v.id, v.at, v.cpu, v.mem_u, v.mem_t, v.disk_u, v.disk_t,
			       v.l1, v.l5, v.l15, v.rx, v.tx, v.tc, v.up
			  FROM unnest($2::uuid[], $3::timestamptz[], $4::bytea[], $5::int[], $6::int[], $7::int[],
			              $8::int[], $9::int[], $10::int[], $11::int[], $12::int[],
			              $13::bigint[], $14::bigint[], $15::int[], $16::bigint[])
			       AS v(id, at, k, cpu, mem_u, mem_t, disk_u, disk_t, l1, l5, l15, rx, tx, tc, up)
			 WHERE true`+gate+`
			ON CONFLICT (node_id, recorded_at) DO NOTHING`,
			tenantID, m.ids, m.ats, m.keys, m.cpu, m.memU, m.memT, m.diskU, m.diskT,
			m.l1, m.l5, m.l15, m.rx, m.tx, m.tc, m.up)
	}
	// 服务器行（两阶段接入的服务器 id = 控制节点 id）：与立即写同一个刷新间隔
	b.Queue(`
		UPDATE servers s SET last_heartbeat_at = v.at
		  FROM unnest($2::uuid[], $3::timestamptz[], $4::bytea[]) AS v(id, at, k)
		 WHERE s.tenant_id = $1 AND s.id = v.id AND s.deleted_at IS NULL
		   AND (s.last_heartbeat_at IS NULL
		        OR s.last_heartbeat_at < v.at - interval '`+serverHeartbeatRefresh+`')`+gate,
		tenantID, ids, ats, keys)
	return s.pool.BatchScoped(ctx, db.Scope{TenantID: tenantID}, db.BatchOptions{AsyncCommit: true}, b)
}

// FlushHeartbeats 立即把合并缓冲里的心跳写进库（测试与诊断用；批量写协程按节拍自己调）。
func (s *Service) FlushHeartbeats(ctx context.Context) {
	if s.hb != nil {
		s.flushHeartbeats(ctx, s.hb, slog.New(slog.DiscardHandler))
	}
}
