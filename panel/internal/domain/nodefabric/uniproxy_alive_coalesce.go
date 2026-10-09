package nodefabric

import (
	"bytes"
	"context"
	"log/slog"
	"sort"
	"sync"
	"time"
)

// 在线上报合并（w10quiet）。
//
// 节点每分钟报一次在线 IP（UniProxy /alive），每份原先一个事务：1000 个节点、30% 在线时
// 每秒约 17 个事务，而其中绝大多数行因为 aliveRefresh 根本不改写。在线记录只喂设备数
// 判定（设备识别窗口最短 5 分钟），晚几秒落库不改变任何判定，所以 aegis-node 把收下的
// 行攒在内存里，每 aliveFlushInterval 全部节点合成一条语句写（writeAliveRows，与当场写
// 同一条 SQL、同一个刷新粒度）。
//
// 最坏延迟：在线记录晚 aliveFlushInterval（加一次写的耗时）可见；进程崩溃丢掉的是最后
// 一段还没写的上报，节点下一分钟再报就补上。库长时间不可用时缓冲上限 aliveBufferMax 行，
// 超出的丢掉并告警（在线记录是遥测，宁可少记，不让内存跟着故障一起涨）。

const (
	aliveFlushInterval = 10 * time.Second
	aliveBufferMax     = 500_000
	aliveFlushTimeout  = 10 * time.Second
)

type aliveRowKey struct {
	tenantID, nodeID string
	uid              int64
	hash             [32]byte
}

type aliveBuffer struct {
	mu      sync.Mutex
	rows    map[aliveRowKey]struct{}
	dropped int
}

func newAliveBuffer() *aliveBuffer { return &aliveBuffer{rows: make(map[aliveRowKey]struct{})} }

// add 收下一份上报（已去重的 uid、哈希两列）。
func (b *aliveBuffer) add(tenantID, nodeID string, uids []int64, hashes [][]byte) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for i := range uids {
		k := aliveRowKey{tenantID: tenantID, nodeID: nodeID, uid: uids[i]}
		copy(k.hash[:], hashes[i])
		if _, ok := b.rows[k]; ok {
			continue
		}
		if len(b.rows) >= aliveBufferMax {
			b.dropped++
			continue
		}
		b.rows[k] = struct{}{}
	}
}

// take 取走全部待写的行，按租户分组、组内按（节点, uid, 哈希）排序；另返回期间丢掉的行数。
func (b *aliveBuffer) take() (map[string][]aliveRowKey, int) {
	b.mu.Lock()
	rows, dropped := b.rows, b.dropped
	b.rows, b.dropped = make(map[aliveRowKey]struct{}), 0
	b.mu.Unlock()
	out := make(map[string][]aliveRowKey)
	for k := range rows {
		out[k.tenantID] = append(out[k.tenantID], k)
	}
	for _, list := range out {
		sort.Slice(list, func(i, j int) bool {
			if list[i].nodeID != list[j].nodeID {
				return list[i].nodeID < list[j].nodeID
			}
			if list[i].uid != list[j].uid {
				return list[i].uid < list[j].uid
			}
			return bytes.Compare(list[i].hash[:], list[j].hash[:]) < 0
		})
	}
	return out, dropped
}

// StartAliveCoalescer 打开在线上报合并并起写入协程（aegis-node 装配时调用一次）。ctx 结束时
// 写完剩下的再退出；返回的 wait 阻塞到协程退出。
func (s *Service) StartAliveCoalescer(ctx context.Context, log *slog.Logger) (wait func()) {
	if s.pool == nil {
		return func() {}
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	b := newAliveBuffer()
	s.alive = b
	done := make(chan struct{})
	go func() {
		defer close(done)
		t := time.NewTicker(aliveFlushInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), aliveFlushTimeout)
				s.flushAlive(fctx, b, log)
				cancel()
				return
			case <-t.C:
				fctx, cancel := context.WithTimeout(ctx, aliveFlushTimeout)
				s.flushAlive(fctx, b, log)
				cancel()
			}
		}
	}()
	return func() { <-done }
}

// FlushAlive 立即把合并缓冲里的在线记录写进库（测试与诊断用）。
func (s *Service) FlushAlive(ctx context.Context) {
	if s.alive != nil {
		s.flushAlive(ctx, s.alive, slog.New(slog.DiscardHandler))
	}
}

func (s *Service) flushAlive(ctx context.Context, b *aliveBuffer, log *slog.Logger) {
	byTenant, dropped := b.take()
	if dropped > 0 {
		log.Warn("在线上报缓冲已满，丢掉了一部分在线记录", "rows", dropped)
	}
	for tenantID, rows := range byTenant {
		nodeIDs := make([]string, len(rows))
		uids := make([]int64, len(rows))
		hashes := make([][]byte, len(rows))
		for i, r := range rows {
			nodeIDs[i], uids[i] = r.nodeID, r.uid
			hashes[i] = append([]byte(nil), r.hash[:]...)
		}
		if _, err := s.writeAliveRows(ctx, tenantID, nodeIDs, uids, hashes); err != nil {
			// 放回去下一轮再写（受 aliveBufferMax 约束）
			for i := range rows {
				b.add(tenantID, nodeIDs[i], uids[i:i+1], hashes[i:i+1])
			}
			log.Warn("在线记录批量写失败，下一轮重试", "tenant_id", tenantID, "rows", len(rows), "error", err.Error())
		}
	}
}
