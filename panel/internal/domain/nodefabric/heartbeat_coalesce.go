package nodefabric

import (
	"bytes"
	"context"
	"log/slog"
	"sort"
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
//     INSERT node_metrics、一条 UPDATE servers，同一个异步提交的批；服务器那条只带可能有同 id
//     服务器行的节点，整批都没有就不发，见 hbServerKnowledge）。
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
// 材料记录只认「库里确实是这样」：凡是立即写成功（签名门槛写 HeartbeatSigned、身份已复核的
// HeartbeatConfirmed、无签名的 Heartbeat，都经 Service.heartbeat）都更新它，失败就忘掉；兼容
// 通道 /status 写运行状态后忘掉；记录里另带写入时用的身份公钥，重新引导、两阶段接入换了身份
// （同时会改写版本与资产列）之后的第一拍一定立即写；即使还有别的写入口没挂上，每
// hbMaterialRefresh 也强制立即写一次，库里最多旧这么久就自愈（审查 #1）。
//
// 单副本设计：材料记录在进程内。aegis-node 目前只支持单实例（总协调 2026-10-09 定）；
// 将来多实例时要让同一节点固定落到同一实例（nginx 按节点一致性哈希），届时再补。
//
// 批量写不与后台的租户级多行写成环：节点行、服务器行都先按 id 排序、FOR NO KEY UPDATE SKIP
// LOCKED 锁住再改，锁不到（别的事务正持有）的不等、放回缓冲下一轮再写（审查 #6）。
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
	// hbMaterialRefresh：离上一次立即写超过这么久，即使材料没变也立即写一次（自愈兜底）。
	hbMaterialRefresh = 10 * time.Minute
	// hbServerKnowledgeTTL：「这个节点有没有同 id 的服务器行」的判断多久后重新核对一次，与
	// hbMaterialRefresh 同一个自愈量级。
	hbServerKnowledgeTTL = hbMaterialRefresh
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

// hbServerKnowledge 记着「库里有没有 id 等于这个节点 id 的服务器行」（两阶段接入与老的引导
// 路径建的服务器 id 就是控制节点 id；后台手建的服务器 id 与节点不同，查不到）。没有这样一行的
// 节点，批量写里的服务器那条 UPDATE 永远是 0 行：静默压测里 1000 个模拟节点每 15 秒空跑一次。
//
// 只在批量写里学（跟着探针语句在同一次往返里问一次），忘掉的时机见 hbCoalescer.written：
// 第一次见到、身份换了、材料字段变了（引导和接入建服务器行时都会换身份、改版本与资产，
// 之后的第一拍一定立即写），以及隔 hbServerKnowledgeTTL 重新核对。
type hbServerKnowledge struct {
	known bool
	has   bool
	at    time.Time // 学到的时刻
}

// fresh 报告这条判断此刻还能用。
func (k hbServerKnowledge) fresh(now time.Time) bool {
	return k.known && now.Sub(k.at) < hbServerKnowledgeTTL
}

type hbNodeState struct {
	material   hbMaterial
	identity   []byte    // 那次立即写用的身份公钥（无签名写为空）
	writtenAt  time.Time // 最近一次立即写成功的时刻
	acceptedAt time.Time // 最近一次收下的心跳（立即写或进缓冲）
	metricsAt  time.Time // 最近一次写进（或排进）探针点的心跳时刻
	pending    *hbPending
	server     hbServerKnowledge
	serverGen  uint64 // server 每被清空（或节点记录重建）一次换一个新值：批量写学到结果时据此丢掉过期的判断
}

type heartbeatCoalescer struct {
	mu    sync.Mutex
	nodes map[string]*hbNodeState // 键：租户 \x00 节点
	// serverStatements、serverProbes 是批量写里服务器 UPDATE 与探测各发了几条（测试用）
	serverStatements, serverProbes int
	gen                            uint64 // serverGen 的发号器
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
		at.Sub(st.acceptedAt) >= hbCoalesceMaxGap || at.Before(st.acceptedAt) ||
		len(publicKey) == 0 || !bytes.Equal(st.identity, publicKey) || at.Sub(st.writtenAt) >= hbMaterialRefresh {
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

// written 记下一次立即写成功：材料字段、身份、时刻；缓冲里更早的那一拍作废。
func (c *heartbeatCoalescer) written(key string, in HeartbeatInput, at time.Time, identity []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	st := c.nodes[key]
	if st == nil {
		st = &hbNodeState{}
		c.nodes[key] = st
		c.gen++
		st.serverGen = c.gen
	}
	// 第一次见到、换了身份或材料字段变了：服务器行可能刚建好（引导、接入会同时换身份、改版本与
	// 资产），之前的判断作废；例行的自愈写（同样的材料与身份）不动它
	if st.material != materialOf(in) || !bytes.Equal(st.identity, identity) {
		st.server = hbServerKnowledge{}
		c.gen++
		st.serverGen = c.gen
	}
	st.material, st.acceptedAt, st.writtenAt, st.pending = materialOf(in), at, at, nil
	st.identity = append([]byte(nil), identity...)
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
	server           hbServerKnowledge // 取走时的判断
	serverGen        uint64
}

// take 取走全部待写的心跳，顺手删掉久不来的节点。
func (c *heartbeatCoalescer) take(now time.Time) []hbFlushRow {
	c.mu.Lock()
	defer c.mu.Unlock()
	var rows []hbFlushRow
	for key, st := range c.nodes {
		if st.pending != nil {
			tenantID, nodeID := splitHBKey(key)
			rows = append(rows, hbFlushRow{tenantID: tenantID, nodeID: nodeID, key: key, p: *st.pending,
				server: st.server, serverGen: st.serverGen})
			st.pending = nil
			continue
		}
		if now.Sub(st.acceptedAt) >= hbForgetAfter {
			delete(c.nodes, key)
		}
	}
	return rows
}

// putBack 把没写成的心跳放回去：期间没有更新的一拍、且还不到 hbCoalesceMaxGap 那么旧才放
// （再旧的心跳写进去也只会让在线判定更早过期；节点删了、行一直锁不到时也不会无限重放）。
func (c *heartbeatCoalescer) putBack(rows []hbFlushRow, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, r := range rows {
		if now.Sub(r.p.at) >= hbCoalesceMaxGap {
			continue
		}
		if st := c.nodes[r.key]; st != nil && st.pending == nil && !st.acceptedAt.After(r.p.at) {
			p := r.p
			st.pending = &p
		}
	}
}

// countServerWork 记下一次批量写里发了服务器 UPDATE 与探测（测试用）。
func (c *heartbeatCoalescer) countServerWork(statement, probe bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if statement {
		c.serverStatements++
	}
	if probe {
		c.serverProbes++
	}
}

// learnServers 记下批量写里核对出的结果：has 里的节点有同 id 的服务器行，其余被核对的没有。
// 取走之后判断被清空过（serverGen 变了）的节点不记，下一拍重新核对。
func (c *heartbeatCoalescer) learnServers(probed []hbFlushRow, has map[string]bool, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, r := range probed {
		if st := c.nodes[r.key]; st != nil && st.serverGen == r.serverGen {
			st.server = hbServerKnowledge{known: true, has: has[r.nodeID], at: now}
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
	key := hbKey(tenantID, parsed.String())
	if v, ok := s.cachedNodeConfig(ctx, tenantID, nodeID, s.watchStamp()); ok && c.offer(key, in, time.Now(), check.publicKey) {
		return v.heartbeatOutput(), nil
	}
	// 立即写；合并器的记录由 Service.heartbeat 按写的结果更新
	return s.heartbeat(ctx, tenantID, nodeID, in, check.publicKey)
}

// heartbeatWritten 在一次立即写之后更新合并器：成功记下材料与身份，失败或节点不存在就忘掉。
func (s *Service) heartbeatWritten(tenantID, nodeID string, in HeartbeatInput, at time.Time, identity []byte, ok bool) {
	if s.hb == nil {
		return
	}
	parsed, err := uuid.Parse(nodeID)
	if err != nil {
		return
	}
	key := hbKey(tenantID, parsed.String())
	if ok {
		s.hb.written(key, in, at, identity)
		return
	}
	s.hb.forget(key)
}

// forgetHeartbeat 让合并器忘掉一个节点：别的入口改写了心跳类列（兼容通道 /status）之后调，
// 下一拍立即写。
func (s *Service) forgetHeartbeat(tenantID, nodeID string) {
	if s.hb == nil {
		return
	}
	if parsed, err := uuid.Parse(nodeID); err == nil {
		s.hb.forget(hbKey(tenantID, parsed.String()))
	}
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
		skipped, err := s.writeHeartbeatBatch(ctx, c, tenantID, batch)
		if err != nil {
			c.putBack(batch, time.Now())
			log.Warn("心跳批量写失败，下一轮重试", "tenant_id", tenantID, "rows", len(batch), "error", err.Error())
			continue
		}
		// 节点行被别的事务锁着、这一轮没写的：放回去下一轮再写
		c.putBack(skipped, time.Now())
	}
}

// writeHeartbeatBatch 一次往返写完一批（BatchScoped，异步提交，与立即写同为遥测级）。
//
// 加锁顺序：节点行、服务器行都按 id 排好序，先 FOR NO KEY UPDATE SKIP LOCKED 锁住再改（与普通
// UPDATE 同一档行锁，不挡外键检查的 KEY SHARE）。别的事务正持有的行直接跳过、不等，所以批量写
// 永远不会排在后台的租户级多行写（发布分流、发布配置）后面成环；跳过的节点返回给调用方放回缓冲。
func (s *Service) writeHeartbeatBatch(ctx context.Context, c *heartbeatCoalescer, tenantID string, rows []hbFlushRow) (skipped []hbFlushRow, err error) {
	sort.Slice(rows, func(i, j int) bool { return rows[i].nodeID < rows[j].nodeID })
	ids := make([]string, len(rows))
	ats := make([]time.Time, len(rows))
	keys := make([][]byte, len(rows))
	// 探针点按列摊开（只含带了探针值的那些拍），与节点行在同一条语句里写
	var m struct {
		ids                                            []string
		ats                                            []time.Time
		cpu, memU, memT, diskU, diskT, l1, l5, l15, tc []int32
		rx, tx, up                                     []int64
	}
	for i, r := range rows {
		ids[i], ats[i], keys[i] = r.nodeID, r.p.at, r.p.key
		if mt := r.p.metrics; mt != nil {
			m.ids, m.ats = append(m.ids, r.nodeID), append(m.ats, r.p.metricsAt)
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
	// 不往回写：立即写已经写了更新的时刻就跳过。探针点作为同一条语句里的写 CTE、只对锁到的
	// 节点写（node_metrics 对 nodes 的外键检查要 KEY SHARE，对锁不到的节点写就会去等别人的
	// FOR UPDATE、甚至成环）。返回锁到的节点 id（没锁到的连同探针点放回缓冲）。
	var locked []string
	b.Queue(`
		WITH v AS (
			SELECT * FROM unnest($2::uuid[], $3::timestamptz[], $4::bytea[]) AS v(id, beat_at, k)
		), mv AS (
			SELECT * FROM unnest($5::uuid[], $6::timestamptz[], $7::int[], $8::int[], $9::int[],
			                     $10::int[], $11::int[], $12::int[], $13::int[], $14::int[],
			                     $15::bigint[], $16::bigint[], $17::int[], $18::bigint[])
			       AS mv(id, beat_at, cpu, mem_u, mem_t, disk_u, disk_t, l1, l5, l15, rx, tx, tc, up)
		), locked AS (
			SELECT n.id FROM nodes n
			 WHERE n.tenant_id = $1 AND n.id IN (SELECT id FROM v)
			 ORDER BY n.id
			   FOR NO KEY UPDATE OF n SKIP LOCKED
		), upd AS (
			UPDATE nodes n SET last_heartbeat_at = v.beat_at
			  FROM locked l JOIN v ON v.id = l.id
			 WHERE n.tenant_id = $1 AND n.id = l.id
			   AND (n.last_heartbeat_at IS NULL OR n.last_heartbeat_at < v.beat_at)`+gate+`
			RETURNING n.id
		), met AS (
			INSERT INTO node_metrics
				(tenant_id, node_id, recorded_at, cpu_bp, mem_used_mb, mem_total_mb,
				 disk_used_gb, disk_total_gb, load1_cbp, load5_cbp, load15_cbp,
				 net_rx_bytes, net_tx_bytes, tcp_conns, uptime_sec)
			SELECT $1::uuid, mv.id, mv.beat_at, mv.cpu, mv.mem_u, mv.mem_t, mv.disk_u, mv.disk_t,
			       mv.l1, mv.l5, mv.l15, mv.rx, mv.tx, mv.tc, mv.up
			  FROM mv JOIN locked l ON l.id = mv.id JOIN v ON v.id = mv.id
			 WHERE true`+gate+`
			ON CONFLICT (node_id, recorded_at) DO NOTHING
		)
		SELECT coalesce(array_agg(id::text), '{}') FROM locked`,
		tenantID, ids, ats, keys, m.ids, m.ats, m.cpu, m.memU, m.memT, m.diskU, m.diskT,
		m.l1, m.l5, m.l15, m.rx, m.tx, m.tc, m.up).QueryRow(func(row pgx.Row) error { return row.Scan(&locked) })
	// 服务器行（两阶段接入的服务器 id = 控制节点 id）：与立即写同一个刷新间隔；锁不到的这一轮
	// 不刷新（下一次心跳再说），同样不等。
	//
	// 空集短路：没有同 id 服务器行的节点（后台手建的服务器 id 与节点不同；压测的模拟节点根本
	// 没有服务器）这条 UPDATE 永远是 0 行，却每 15 秒按整批节点探一遍主键。记着每个节点「有没有」
	// （hbServerKnowledge），核对过没有的不再带进来，整批都没有就不发这条语句；没核对过或
	// 过了 hbServerKnowledgeTTL 的节点，照旧带进 UPDATE，并在同一次往返里用一条主键探测核对，
	// 下一拍起就按结果走。服务器行刚建好的那一拍必然是立即写（身份和材料都变了，written 会清掉
	// 判断），库里的心跳时刻不会因此晚于离线判定。
	now := time.Now()
	var srvIDs []string
	var srvAts []time.Time
	var srvKeys [][]byte
	var probe []hbFlushRow
	for i, r := range rows {
		fresh := r.server.fresh(now)
		if fresh && !r.server.has {
			continue
		}
		srvIDs, srvAts, srvKeys = append(srvIDs, ids[i]), append(srvAts, ats[i]), append(srvKeys, keys[i])
		if !fresh {
			probe = append(probe, r)
		}
	}
	hasServer := make(map[string]bool, len(probe))
	if len(probe) > 0 {
		probeIDs := make([]string, len(probe))
		for i, r := range probe {
			probeIDs[i] = r.nodeID
		}
		b.Queue(`SELECT id::text FROM servers WHERE tenant_id = $1 AND deleted_at IS NULL AND id = ANY($2::uuid[])`,
			tenantID, probeIDs).Query(func(rs pgx.Rows) error {
			for rs.Next() {
				var id string
				if err := rs.Scan(&id); err != nil {
					return err
				}
				hasServer[id] = true
			}
			return rs.Err()
		})
	}
	if len(srvIDs) > 0 {
		b.Queue(`
			WITH v AS (
				SELECT * FROM unnest($2::uuid[], $3::timestamptz[], $4::bytea[]) AS v(id, beat_at, k)
			), locked AS (
				SELECT s.id FROM servers s JOIN v ON v.id = s.id
				 WHERE s.tenant_id = $1 AND s.deleted_at IS NULL
				   AND (s.last_heartbeat_at IS NULL
				        OR s.last_heartbeat_at < v.beat_at - interval '`+serverHeartbeatRefresh+`')
				 ORDER BY s.id
				   FOR NO KEY UPDATE OF s SKIP LOCKED
			)
			UPDATE servers s SET last_heartbeat_at = v.beat_at
			  FROM locked l JOIN v ON v.id = l.id
			 WHERE s.tenant_id = $1 AND s.id = l.id`+gate,
			tenantID, srvIDs, srvAts, srvKeys)
	}
	if err := s.pool.BatchScoped(ctx, db.Scope{TenantID: tenantID}, db.BatchOptions{AsyncCommit: true}, b); err != nil {
		return nil, err
	}
	c.countServerWork(len(srvIDs) > 0, len(probe) > 0)
	c.learnServers(probe, hasServer, now)
	got := make(map[string]bool, len(locked))
	for _, id := range locked {
		got[id] = true
	}
	for _, r := range rows {
		if !got[r.nodeID] {
			skipped = append(skipped, r)
		}
	}
	return skipped, nil
}

// FlushHeartbeats 立即把合并缓冲里的心跳写进库（测试与诊断用；批量写协程按节拍自己调）。
func (s *Service) FlushHeartbeats(ctx context.Context) {
	if s.hb != nil {
		s.flushHeartbeats(ctx, s.hb, slog.New(slog.DiscardHandler))
	}
}
